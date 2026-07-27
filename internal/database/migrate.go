package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/config"
)

var (
	ErrManifest     = errors.New("invalid migration manifest")
	ErrSequence     = errors.New("invalid migration sequence")
	ErrLock         = errors.New("migration lock unavailable")
	ErrDrift        = errors.New("migration drift")
	ErrLegacySchema = errors.New("unrecognized legacy schema")
)

var (
	migrationFilename = regexp.MustCompile(`^([0-9]{3})_([a-z0-9_]+)\.sql$`)
	blockComment      = regexp.MustCompile(`(?s)/\*.*?\*/`)
	databaseCharset   = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
)

type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

type Runner struct {
	DB          *sql.DB
	LockTimeout time.Duration

	afterStatement func(context.Context, *sql.Conn, Migration, int) error
}

const migrationLockName = "jxh_manager_migrations"

func (r Runner) Apply(ctx context.Context, migrations []Migration) (applied []Migration, err error) {
	recoveryManifest, err := classifyHistoricalManifest(migrations)
	if err != nil {
		return nil, err
	}
	if r.DB == nil {
		return nil, fmt.Errorf("open migration connection: database is not configured")
	}
	conn, err := r.DB.Conn(ctx)
	if err != nil {
		return nil, safeDatabaseError("open migration connection", err)
	}
	defer conn.Close()

	var locked sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", migrationLockName, r.lockTimeoutSeconds()).Scan(&locked); err != nil {
		discardMigrationConnection(conn)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("acquire migration lock: %w", err)
		}
		return nil, fmt.Errorf("%w: acquire migration lock", ErrLock)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return nil, fmt.Errorf("%w: lock was not granted", ErrLock)
	}
	defer func() {
		if releaseErr := releaseMigrationLock(conn); releaseErr != nil {
			discardMigrationConnection(conn)
			err = errors.Join(err, releaseErr)
		}
	}()

	if err := ensureMigrationLedgerTable(ctx, conn); err != nil {
		return nil, err
	}
	if err := ensureMigrationAttemptTable(ctx, conn); err != nil {
		return nil, err
	}

	appliedCount, err := validateAppliedMigrations(ctx, conn, migrations)
	if err != nil {
		return nil, err
	}
	if appliedCount == 0 {
		if err := validateLegacyObjectInventory(ctx, conn); err != nil {
			return nil, err
		}
	}
	activeAttempts := 0
	if recoveryManifest {
		activeAttempts, err = countMigrationAttempts(ctx, conn)
		if err != nil {
			return nil, err
		}
	}
	if appliedCount == 0 && activeAttempts == 0 {
		snapshot, err := loadLegacySchema(ctx, conn)
		if err != nil {
			return nil, err
		}
		legacyBaseline, err := recognizeLegacyBaseline(snapshot)
		if err != nil {
			return nil, err
		}
		if legacyBaseline > 0 && !recoveryManifest {
			return nil, fmt.Errorf("%w: legacy baseline requires the repository migration manifest", ErrLegacySchema)
		}
		if legacyBaseline > len(migrations) || legacyBaseline == 5 && len(migrations) < 8 {
			return nil, fmt.Errorf("%w: manifest does not contain the legacy baseline", ErrLegacySchema)
		}
		if legacyBaseline > 0 {
			if err := adoptLegacyBaseline(ctx, conn, migrations[:legacyBaseline]); err != nil {
				return nil, err
			}
			appliedCount = legacyBaseline
		}
	}
	if recoveryManifest {
		if err := validateMigrationAttemptSet(ctx, conn, migrations, appliedCount); err != nil {
			return nil, err
		}
	}
	applied = make([]Migration, 0, len(migrations)-appliedCount)
	for _, migration := range migrations[appliedCount:] {
		if isHistoricalRecoveryMigration(migration) {
			if err := r.applyHistoricalMigration(ctx, conn, migration); err != nil {
				return nil, err
			}
			applied = append(applied, migration)
			continue
		}
		if recoveryManifest && isRepositoryMigration007(migration) {
			if err := r.applyMigration007(ctx, conn, migration); err != nil {
				return nil, err
			}
			applied = append(applied, migration)
			continue
		}
		if _, err := conn.ExecContext(ctx, migration.SQL); err != nil {
			return nil, safeDatabaseError(fmt.Sprintf("execute migration %03d %s", migration.Version, migration.Name), err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO `schema_migrations` (`version`, `name`, `checksum`, `applied_at`) VALUES (?, ?, ?, CURRENT_TIMESTAMP(3))",
			migration.Version, migration.Name, migration.Checksum,
		); err != nil {
			return nil, safeDatabaseError(fmt.Sprintf("record migration %03d %s", migration.Version, migration.Name), err)
		}
		applied = append(applied, migration)
	}
	return applied, nil
}

func ensureMigrationLedgerTable(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+"`schema_migrations`"+` (
  `+"`version`"+` int unsigned NOT NULL,
  `+"`name`"+` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `+"`checksum`"+` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `+"`applied_at`"+` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`+"`version`"+`),
  UNIQUE KEY `+"`uq_schema_migrations_name`"+` (`+"`name`"+`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		return safeDatabaseError("create migration ledger", err)
	}
	return verifyMigrationInfrastructureTable(ctx, conn, "schema_migrations", migrationLedgerSchema())
}

func (r Runner) lockTimeoutSeconds() int {
	if r.LockTimeout <= 0 {
		return 10
	}
	seconds := int((r.LockTimeout + time.Second - 1) / time.Second)
	if seconds < 1 {
		return 1
	}
	return seconds
}

func releaseMigrationLock(conn *sql.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var released sql.NullInt64
	if err := conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName).Scan(&released); err != nil {
		return fmt.Errorf("%w: release migration lock", ErrLock)
	}
	if !released.Valid || released.Int64 != 1 {
		return fmt.Errorf("%w: migration lock was not released", ErrLock)
	}
	return nil
}

func discardMigrationConnection(conn *sql.Conn) {
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
}

type legacyColumn struct {
	Table     string
	Name      string
	Ordinal   int
	Type      string
	Nullable  bool
	Collation string
	Extra     string
	Default   string
}

func adoptLegacyBaseline(ctx context.Context, conn *sql.Conn, migrations []Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return safeDatabaseError("begin legacy baseline", err)
	}
	for _, migration := range migrations {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `schema_migrations` (`version`, `name`, `checksum`, `applied_at`) VALUES (?, ?, ?, CURRENT_TIMESTAMP(3))",
			migration.Version, migration.Name, migration.Checksum,
		); err != nil {
			_ = tx.Rollback()
			return safeDatabaseError(fmt.Sprintf("record legacy baseline %03d %s", migration.Version, migration.Name), err)
		}
	}
	if err := tx.Commit(); err != nil {
		return safeDatabaseError("commit legacy baseline", err)
	}
	return nil
}

type legacyIndex struct {
	Table      string
	Name       string
	Column     string
	Sequence   int
	Unique     bool
	SubPart    int
	IndexType  string
	Expression string
	Visible    bool
	Collation  string
}

type legacyTable struct {
	Name      string
	Engine    string
	Collation string
}

type legacySchema struct {
	Columns     []legacyColumn
	Indexes     []legacyIndex
	Tables      []legacyTable
	Constraints []legacyConstraint
}

type legacyConstraint struct {
	Table            string
	Name             string
	Type             string
	Ordinal          int
	Column           string
	ReferencedTable  string
	ReferencedColumn string
	CheckClause      string
}

const legacyNullDefault = "\x00NULL"

func loadLegacySchema(ctx context.Context, conn *sql.Conn) (legacySchema, error) {
	return loadSchemaMetadata(ctx, conn, []string{"knowledge_trigger_logs", "scheduled_jobs", "group_join_requests"}, "legacy schema")
}

func validateLegacyObjectInventory(ctx context.Context, conn *sql.Conn) error {
	rows, err := conn.QueryContext(ctx, `SELECT table_name, table_type
FROM information_schema.tables
WHERE table_schema = DATABASE()
ORDER BY BINARY table_name`)
	if err != nil {
		return safeDatabaseError("inspect legacy schema object inventory", err)
	}
	defer rows.Close()
	allowed := map[string]struct{}{
		"schema_migrations":         {},
		"schema_migration_attempts": {},
		"knowledge_trigger_logs":    {},
		"scheduled_jobs":            {},
		"group_join_requests":       {},
	}
	for rows.Next() {
		var name, objectType string
		if err := rows.Scan(&name, &objectType); err != nil {
			return safeDatabaseError("inspect legacy schema object", err)
		}
		if objectType != "BASE TABLE" {
			return fmt.Errorf("%w: persistent object %s is not a base table", ErrLegacySchema, name)
		}
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("%w: persistent object %s is not part of a recoverable schema", ErrLegacySchema, name)
		}
	}
	if err := rows.Err(); err != nil {
		return safeDatabaseError("inspect legacy schema object inventory", err)
	}
	return nil
}

func loadSchemaMetadata(ctx context.Context, conn *sql.Conn, tables []string, description string) (legacySchema, error) {
	tableArgs := make([]any, len(tables))
	for i, table := range tables {
		tableArgs[i] = table
	}
	tablePredicate := binaryTableNamePredicate("table_name", len(tables))
	rows, err := conn.QueryContext(ctx, `SELECT table_name, column_name, ordinal_position, column_type,
       is_nullable, COALESCE(collation_name, ''), column_default, extra
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND (`+tablePredicate+`)
ORDER BY table_name, ordinal_position`, tableArgs...)
	if err != nil {
		return legacySchema{}, safeDatabaseError("inspect "+description+" columns", err)
	}
	var snapshot legacySchema
	for rows.Next() {
		var column legacyColumn
		var nullable string
		var defaultValue sql.NullString
		if err := rows.Scan(&column.Table, &column.Name, &column.Ordinal, &column.Type, &nullable, &column.Collation, &defaultValue, &column.Extra); err != nil {
			rows.Close()
			return legacySchema{}, safeDatabaseError("inspect "+description+" column", err)
		}
		column.Nullable = strings.EqualFold(nullable, "YES")
		column.Default = legacyNullDefault
		if defaultValue.Valid {
			column.Default = defaultValue.String
		}
		snapshot.Columns = append(snapshot.Columns, column)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return legacySchema{}, safeDatabaseError("inspect "+description+" columns", err)
	}
	rows.Close()

	indexRows, err := conn.QueryContext(ctx, `SELECT table_name, index_name, non_unique, seq_in_index, COALESCE(column_name, ''),
       COALESCE(sub_part, 0), index_type, COALESCE(expression, ''), is_visible, COALESCE(collation, '')
FROM information_schema.statistics
WHERE table_schema = DATABASE()
  AND (`+tablePredicate+`)
ORDER BY table_name, index_name, seq_in_index`, tableArgs...)
	if err != nil {
		return legacySchema{}, safeDatabaseError("inspect "+description+" indexes", err)
	}
	for indexRows.Next() {
		var index legacyIndex
		var nonUnique int
		var column sql.NullString
		var visible string
		if err := indexRows.Scan(
			&index.Table, &index.Name, &nonUnique, &index.Sequence, &column,
			&index.SubPart, &index.IndexType, &index.Expression, &visible, &index.Collation,
		); err != nil {
			indexRows.Close()
			return legacySchema{}, safeDatabaseError("inspect "+description+" index", err)
		}
		index.Column = column.String
		index.Unique = nonUnique == 0
		index.Visible = strings.EqualFold(visible, "YES")
		snapshot.Indexes = append(snapshot.Indexes, index)
	}
	if err := indexRows.Err(); err != nil {
		indexRows.Close()
		return legacySchema{}, safeDatabaseError("inspect "+description+" indexes", err)
	}
	indexRows.Close()

	tableRows, err := conn.QueryContext(ctx, `SELECT table_name, engine, table_collation
FROM information_schema.tables
WHERE table_schema = DATABASE()
  AND (`+tablePredicate+`)
ORDER BY table_name`, tableArgs...)
	if err != nil {
		return legacySchema{}, safeDatabaseError("inspect "+description+" tables", err)
	}
	for tableRows.Next() {
		var table legacyTable
		if err := tableRows.Scan(&table.Name, &table.Engine, &table.Collation); err != nil {
			tableRows.Close()
			return legacySchema{}, safeDatabaseError("inspect "+description+" table", err)
		}
		snapshot.Tables = append(snapshot.Tables, table)
	}
	if err := tableRows.Err(); err != nil {
		tableRows.Close()
		return legacySchema{}, safeDatabaseError("inspect "+description+" tables", err)
	}
	tableRows.Close()

	constraintRows, err := conn.QueryContext(ctx, `SELECT tc.table_name, tc.constraint_name, tc.constraint_type,
       COALESCE(kcu.ordinal_position, 0), COALESCE(kcu.column_name, ''),
       COALESCE(kcu.referenced_table_name, ''), COALESCE(kcu.referenced_column_name, ''),
       COALESCE(cc.check_clause, '')
FROM information_schema.table_constraints AS tc
LEFT JOIN information_schema.key_column_usage AS kcu
  ON kcu.constraint_schema = tc.constraint_schema
 AND kcu.table_name = tc.table_name
 AND kcu.constraint_name = tc.constraint_name
LEFT JOIN information_schema.check_constraints AS cc
  ON cc.constraint_schema = tc.constraint_schema
 AND cc.constraint_name = tc.constraint_name
WHERE tc.table_schema = DATABASE()
  AND (`+binaryTableNamePredicate("tc.table_name", len(tables))+`)
ORDER BY tc.table_name, tc.constraint_name, kcu.ordinal_position`, tableArgs...)
	if err != nil {
		return legacySchema{}, safeDatabaseError("inspect "+description+" constraints", err)
	}
	for constraintRows.Next() {
		var constraint legacyConstraint
		if err := constraintRows.Scan(
			&constraint.Table, &constraint.Name, &constraint.Type, &constraint.Ordinal, &constraint.Column,
			&constraint.ReferencedTable, &constraint.ReferencedColumn, &constraint.CheckClause,
		); err != nil {
			constraintRows.Close()
			return legacySchema{}, safeDatabaseError("inspect "+description+" constraint", err)
		}
		snapshot.Constraints = append(snapshot.Constraints, constraint)
	}
	if err := constraintRows.Err(); err != nil {
		constraintRows.Close()
		return legacySchema{}, safeDatabaseError("inspect "+description+" constraints", err)
	}
	constraintRows.Close()
	return snapshot, nil
}

func binaryTableNamePredicate(column string, count int) string {
	terms := make([]string, count)
	for i := range terms {
		terms[i] = "BINARY " + column + " = BINARY ?"
	}
	return strings.Join(terms, " OR ")
}

func recognizeLegacyBaseline(snapshot legacySchema) (int, error) {
	if len(snapshot.Columns) == 0 && len(snapshot.Indexes) == 0 && len(snapshot.Tables) == 0 && len(snapshot.Constraints) == 0 {
		return 0, nil
	}
	if sameLegacySchema(snapshot, knownPost005LegacySchema()) {
		return 5, nil
	}
	if sameLegacySchema(snapshot, knownPost007LegacySchema()) {
		return 7, nil
	}
	return 0, fmt.Errorf("%w: core tables do not exactly match a known release", ErrLegacySchema)
}

func sameLegacySchema(left, right legacySchema) bool {
	if len(left.Columns) != len(right.Columns) || len(left.Indexes) != len(right.Indexes) ||
		len(left.Tables) != len(right.Tables) || len(left.Constraints) != len(right.Constraints) {
		return false
	}
	leftColumns := make(map[string]struct{}, len(left.Columns))
	for _, column := range left.Columns {
		leftColumns[legacyColumnKey(column)] = struct{}{}
	}
	if len(leftColumns) != len(left.Columns) {
		return false
	}
	for _, column := range right.Columns {
		if _, ok := leftColumns[legacyColumnKey(column)]; !ok {
			return false
		}
	}
	leftIndexes := make(map[string]struct{}, len(left.Indexes))
	for _, index := range left.Indexes {
		leftIndexes[legacyIndexKey(index)] = struct{}{}
	}
	if len(leftIndexes) != len(left.Indexes) {
		return false
	}
	for _, index := range right.Indexes {
		if _, ok := leftIndexes[legacyIndexKey(index)]; !ok {
			return false
		}
	}
	leftTables := make(map[string]struct{}, len(left.Tables))
	for _, table := range left.Tables {
		leftTables[legacyTableKey(table)] = struct{}{}
	}
	if len(leftTables) != len(left.Tables) {
		return false
	}
	for _, table := range right.Tables {
		if _, ok := leftTables[legacyTableKey(table)]; !ok {
			return false
		}
	}
	leftConstraints := make(map[string]struct{}, len(left.Constraints))
	for _, constraint := range left.Constraints {
		leftConstraints[legacyConstraintKey(constraint)] = struct{}{}
	}
	if len(leftConstraints) != len(left.Constraints) {
		return false
	}
	for _, constraint := range right.Constraints {
		if _, ok := leftConstraints[legacyConstraintKey(constraint)]; !ok {
			return false
		}
	}
	return true
}

func legacyColumnKey(column legacyColumn) string {
	return fmt.Sprintf("%s|%s|%d|%s|%t|%s|%s|%s",
		column.Table, column.Name, column.Ordinal,
		strings.ToLower(column.Type), column.Nullable, strings.ToLower(column.Collation),
		column.Default, normalizeLegacyExtra(column.Extra))
}

func legacyIndexKey(index legacyIndex) string {
	return fmt.Sprintf("%s|%s|%s|%d|%t|%d|%s|%s|%t|%s",
		index.Table, index.Name, index.Column,
		index.Sequence, index.Unique, index.SubPart, strings.ToLower(index.IndexType), index.Expression,
		index.Visible, strings.ToUpper(index.Collation))
}

func legacyTableKey(table legacyTable) string {
	return fmt.Sprintf("%s|%s|%s", table.Name, strings.ToLower(table.Engine), strings.ToLower(table.Collation))
}

func legacyConstraintKey(constraint legacyConstraint) string {
	return fmt.Sprintf("%s|%s|%s|%d|%s|%s|%s|%s",
		constraint.Table, constraint.Name, strings.ToUpper(constraint.Type), constraint.Ordinal,
		constraint.Column, constraint.ReferencedTable, constraint.ReferencedColumn, constraint.CheckClause)
}

func normalizeLegacyExtra(extra string) string {
	fields := strings.Fields(strings.ToLower(extra))
	kept := fields[:0]
	for _, field := range fields {
		if field != "default_generated" {
			kept = append(kept, field)
		}
	}
	return strings.Join(kept, " ")
}

func verifyMigrationInfrastructureTable(ctx context.Context, conn *sql.Conn, table string, want legacySchema) error {
	actual, err := loadSchemaMetadata(ctx, conn, []string{table}, "migration infrastructure")
	if err != nil {
		return err
	}
	if !sameLegacySchema(actual, want) {
		return fmt.Errorf("%w: migration infrastructure table %s does not match the required schema", ErrDrift, table)
	}
	return nil
}

func migrationLedgerSchema() legacySchema {
	return legacySchema{
		Columns: []legacyColumn{
			{Table: "schema_migrations", Name: "version", Ordinal: 1, Type: "int unsigned", Default: legacyNullDefault},
			{Table: "schema_migrations", Name: "name", Ordinal: 2, Type: "varchar(255)", Collation: "utf8mb4_bin", Default: legacyNullDefault},
			{Table: "schema_migrations", Name: "checksum", Ordinal: 3, Type: "char(64)", Collation: "ascii_bin", Default: legacyNullDefault},
			{Table: "schema_migrations", Name: "applied_at", Ordinal: 4, Type: "datetime(3)", Default: "CURRENT_TIMESTAMP(3)"},
		},
		Indexes: []legacyIndex{
			knownLegacyIndex("schema_migrations", "PRIMARY", "version", 1, true),
			knownLegacyIndex("schema_migrations", "uq_schema_migrations_name", "name", 1, true),
		},
		Tables: []legacyTable{{Name: "schema_migrations", Engine: "InnoDB", Collation: "utf8mb4_0900_ai_ci"}},
		Constraints: []legacyConstraint{
			{Table: "schema_migrations", Name: "PRIMARY", Type: "PRIMARY KEY", Ordinal: 1, Column: "version"},
			{Table: "schema_migrations", Name: "uq_schema_migrations_name", Type: "UNIQUE", Ordinal: 1, Column: "name"},
		},
	}
}

func migrationAttemptLedgerSchema() legacySchema {
	return legacySchema{
		Columns: []legacyColumn{
			{Table: "schema_migration_attempts", Name: "version", Ordinal: 1, Type: "int unsigned", Default: legacyNullDefault},
			{Table: "schema_migration_attempts", Name: "name", Ordinal: 2, Type: "varchar(255)", Collation: "utf8mb4_bin", Default: legacyNullDefault},
			{Table: "schema_migration_attempts", Name: "checksum", Ordinal: 3, Type: "char(64)", Collation: "ascii_bin", Default: legacyNullDefault},
			{Table: "schema_migration_attempts", Name: "stage", Ordinal: 4, Type: "int unsigned", Default: "0"},
			{Table: "schema_migration_attempts", Name: "started_at", Ordinal: 5, Type: "datetime(3)", Default: "CURRENT_TIMESTAMP(3)"},
		},
		Indexes: []legacyIndex{knownLegacyIndex("schema_migration_attempts", "PRIMARY", "version", 1, true)},
		Tables:  []legacyTable{{Name: "schema_migration_attempts", Engine: "InnoDB", Collation: "utf8mb4_0900_ai_ci"}},
		Constraints: []legacyConstraint{
			{Table: "schema_migration_attempts", Name: "PRIMARY", Type: "PRIMARY KEY", Ordinal: 1, Column: "version"},
		},
	}
}

func knownPost007LegacySchema() legacySchema {
	const defaultCollation = "utf8mb4_0900_ai_ci"
	columns := []legacyColumn{
		{Table: "knowledge_trigger_logs", Name: "id", Ordinal: 1, Type: "bigint unsigned", Extra: "auto_increment"},
		{Table: "knowledge_trigger_logs", Name: "source_key", Ordinal: 2, Type: "varchar(255)", Collation: "utf8mb4_bin"},
		{Table: "knowledge_trigger_logs", Name: "trigger_type", Ordinal: 3, Type: "varchar(32)", Collation: defaultCollation},
		{Table: "knowledge_trigger_logs", Name: "group_id", Ordinal: 4, Type: "bigint"},
		{Table: "knowledge_trigger_logs", Name: "triggered_at", Ordinal: 5, Type: "datetime(3)"},

		{Table: "scheduled_jobs", Name: "id", Ordinal: 1, Type: "bigint unsigned", Extra: "auto_increment"},
		{Table: "scheduled_jobs", Name: "type", Ordinal: 2, Type: "varchar(16)", Collation: defaultCollation},
		{Table: "scheduled_jobs", Name: "time_hhmm", Ordinal: 3, Type: "varchar(5)", Collation: defaultCollation},
		{Table: "scheduled_jobs", Name: "run_date", Ordinal: 4, Type: "date", Nullable: true},
		{Table: "scheduled_jobs", Name: "group_id", Ordinal: 5, Type: "bigint"},
		{Table: "scheduled_jobs", Name: "message", Ordinal: 6, Type: "text", Collation: defaultCollation},
		{Table: "scheduled_jobs", Name: "enabled", Ordinal: 7, Type: "tinyint(1)"},
		{Table: "scheduled_jobs", Name: "last_run_at", Ordinal: 8, Type: "datetime(3)", Nullable: true},
		{Table: "scheduled_jobs", Name: "created_at", Ordinal: 9, Type: "datetime(3)", Nullable: true},
		{Table: "scheduled_jobs", Name: "updated_at", Ordinal: 10, Type: "datetime(3)", Nullable: true},

		{Table: "group_join_requests", Name: "id", Ordinal: 1, Type: "bigint unsigned", Extra: "auto_increment"},
		{Table: "group_join_requests", Name: "flag", Ordinal: 2, Type: "varchar(512)", Collation: "utf8mb4_bin"},
		{Table: "group_join_requests", Name: "group_id", Ordinal: 3, Type: "bigint", Nullable: true},
		{Table: "group_join_requests", Name: "user_id", Ordinal: 4, Type: "bigint", Nullable: true},
		{Table: "group_join_requests", Name: "student_id", Ordinal: 5, Type: "varchar(64)", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "student_name", Ordinal: 6, Type: "varchar(64)", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "major", Ordinal: 7, Type: "varchar(128)", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "sub_type", Ordinal: 8, Type: "varchar(32)", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "comment", Ordinal: 9, Type: "text", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "status", Ordinal: 10, Type: "varchar(32)", Collation: defaultCollation},
		{Table: "group_join_requests", Name: "source", Ordinal: 11, Type: "varchar(32)", Collation: defaultCollation},
		{Table: "group_join_requests", Name: "raw_json", Ordinal: 12, Type: "mediumtext", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "system_raw_json", Ordinal: 13, Type: "mediumtext", Nullable: true, Collation: defaultCollation},
		{Table: "group_join_requests", Name: "ai_parse_status", Ordinal: 14, Type: "varchar(32)", Collation: defaultCollation},
		{Table: "group_join_requests", Name: "ai_parse_attempts", Ordinal: 15, Type: "int unsigned"},
		{Table: "group_join_requests", Name: "requested_at", Ordinal: 16, Type: "datetime(3)", Nullable: true},
		{Table: "group_join_requests", Name: "processed_at", Ordinal: 17, Type: "datetime(3)", Nullable: true},
		{Table: "group_join_requests", Name: "first_seen_at", Ordinal: 18, Type: "datetime(3)", Nullable: true},
		{Table: "group_join_requests", Name: "last_seen_at", Ordinal: 19, Type: "datetime(3)", Nullable: true},
		{Table: "group_join_requests", Name: "ai_parsed_at", Ordinal: 20, Type: "datetime(3)", Nullable: true},
	}
	for i := range columns {
		columns[i].Default = legacyNullDefault
	}
	for i := range columns {
		switch columns[i].Table + "." + columns[i].Name {
		case "knowledge_trigger_logs.triggered_at":
			columns[i].Default = "CURRENT_TIMESTAMP(3)"
		case "group_join_requests.ai_parse_status":
			columns[i].Default = "pending"
		case "group_join_requests.ai_parse_attempts":
			columns[i].Default = "0"
		}
	}
	indexes := []legacyIndex{
		knownLegacyIndex("knowledge_trigger_logs", "PRIMARY", "id", 1, true),
		knownLegacyIndex("knowledge_trigger_logs", "idx_trigger_stats", "triggered_at", 1, false),
		knownLegacyIndex("knowledge_trigger_logs", "idx_trigger_stats", "source_key", 2, false),
		knownLegacyIndex("knowledge_trigger_logs", "idx_trigger_stats", "trigger_type", 3, false),
		knownLegacyIndex("scheduled_jobs", "PRIMARY", "id", 1, true),
		knownLegacyIndex("group_join_requests", "PRIMARY", "id", 1, true),
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_flag", "flag", 1, true),
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_group_id", "group_id", 1, false),
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_user_id", "user_id", 1, false),
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_status", "status", 1, false),
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_ai_parse_status", "ai_parse_status", 1, false),
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_last_seen_at", "last_seen_at", 1, false),
	}
	tables := []legacyTable{
		{Name: "knowledge_trigger_logs", Engine: "InnoDB", Collation: defaultCollation},
		{Name: "scheduled_jobs", Engine: "InnoDB", Collation: defaultCollation},
		{Name: "group_join_requests", Engine: "InnoDB", Collation: defaultCollation},
	}
	constraints := []legacyConstraint{
		{Table: "knowledge_trigger_logs", Name: "PRIMARY", Type: "PRIMARY KEY", Ordinal: 1, Column: "id"},
		{Table: "scheduled_jobs", Name: "PRIMARY", Type: "PRIMARY KEY", Ordinal: 1, Column: "id"},
		{Table: "group_join_requests", Name: "PRIMARY", Type: "PRIMARY KEY", Ordinal: 1, Column: "id"},
		{Table: "group_join_requests", Name: "idx_group_join_requests_flag", Type: "UNIQUE", Ordinal: 1, Column: "flag"},
	}
	return legacySchema{Columns: columns, Indexes: indexes, Tables: tables, Constraints: constraints}
}

func knownLegacyIndex(table, name, column string, sequence int, unique bool) legacyIndex {
	return legacyIndex{
		Table: table, Name: name, Column: column, Sequence: sequence, Unique: unique,
		IndexType: "BTREE", Visible: true, Collation: "A",
	}
}

func knownPost005LegacySchema() legacySchema {
	schema := knownPost007LegacySchema()
	for i := range schema.Columns {
		if schema.Columns[i].Table == "group_join_requests" && schema.Columns[i].Ordinal > 2 {
			schema.Columns[i].Ordinal++
		}
	}
	schema.Columns = append(schema.Columns, legacyColumn{
		Table: "group_join_requests", Name: "system_request_id", Ordinal: 3, Type: "varchar(64)",
		Nullable: true, Collation: "utf8mb4_bin", Default: legacyNullDefault,
	})
	schema.Indexes = append(schema.Indexes,
		knownLegacyIndex("group_join_requests", "idx_group_join_requests_system_request_id", "system_request_id", 1, true))
	schema.Constraints = append(schema.Constraints, legacyConstraint{
		Table: "group_join_requests", Name: "idx_group_join_requests_system_request_id", Type: "UNIQUE",
		Ordinal: 1, Column: "system_request_id",
	})
	return schema
}

func validateAppliedMigrations(ctx context.Context, conn *sql.Conn, migrations []Migration) (int, error) {
	rows, err := conn.QueryContext(ctx, "SELECT `version`, `name`, `checksum` FROM `schema_migrations` ORDER BY `version`")
	if err != nil {
		return 0, safeDatabaseError("inspect applied migrations", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum); err != nil {
			return 0, safeDatabaseError("inspect applied migration", err)
		}
		count++
		if version != count || version > len(migrations) {
			return 0, fmt.Errorf("%w: applied version sequence is invalid", ErrDrift)
		}
		migration := migrations[version-1]
		if name != migration.Name || checksum != migration.Checksum {
			return 0, fmt.Errorf("%w: applied migration %03d does not match manifest", ErrDrift, version)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, safeDatabaseError("inspect applied migrations", err)
	}
	return count, nil
}

func safeDatabaseError(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: database operation failed", operation)
}

func LoadMigrations(dir string) ([]Migration, error) {
	return loadMigrationsWithHooks(dir, migrationLoadHooks{})
}

type migrationLoadHooks struct {
	afterDirectoryOpen func()
	afterFileLstat     func(string)
	afterFileOpen      func(string)
}

func loadMigrationsWithHooks(dir string, hooks migrationLoadHooks) ([]Migration, error) {
	directoryBefore, err := os.Lstat(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect directory: %v", ErrManifest, err)
	}
	if directoryBefore.Mode()&os.ModeSymlink != 0 || !directoryBefore.IsDir() {
		return nil, fmt.Errorf("%w: migration directory is not a regular directory", ErrManifest)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: open directory: %v", ErrManifest, err)
	}
	defer root.Close()
	if hooks.afterDirectoryOpen != nil {
		hooks.afterDirectoryOpen()
	}
	directoryHandle, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("%w: inspect opened directory: %v", ErrManifest, err)
	}
	if err := verifyMigrationDirectoryIdentity(dir, directoryBefore, directoryHandle); err != nil {
		return nil, err
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return nil, fmt.Errorf("%w: read directory: %v", ErrManifest, err)
	}
	migrations := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		matches := migrationFilename.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("%w: invalid SQL filename %q", ErrManifest, entry.Name())
		}
		infoBefore, err := root.Lstat(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: inspect %q: %v", ErrManifest, entry.Name(), err)
		}
		if entry.Type()&os.ModeSymlink != 0 || infoBefore.Mode()&os.ModeSymlink != 0 || !infoBefore.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: %q is not a regular migration file", ErrManifest, entry.Name())
		}
		if hooks.afterFileLstat != nil {
			hooks.afterFileLstat(entry.Name())
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("%w: invalid version in %q", ErrManifest, entry.Name())
		}
		file, err := root.Open(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: open %q: %v", ErrManifest, entry.Name(), err)
		}
		infoHandle, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("%w: inspect opened %q: %v", ErrManifest, entry.Name(), err)
		}
		if !sameRegularMigrationFile(infoBefore, infoHandle) {
			file.Close()
			return nil, fmt.Errorf("%w: %q changed while opening", ErrManifest, entry.Name())
		}
		if hooks.afterFileOpen != nil {
			hooks.afterFileOpen(entry.Name())
		}
		contents, err := io.ReadAll(file)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("%w: read %q: %v", ErrManifest, entry.Name(), err)
		}
		infoHandleAfter, statErr := file.Stat()
		closeErr := file.Close()
		if statErr != nil {
			return nil, fmt.Errorf("%w: re-inspect opened %q: %v", ErrManifest, entry.Name(), statErr)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("%w: close %q: %v", ErrManifest, entry.Name(), closeErr)
		}
		infoAfter, err := root.Lstat(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("%w: re-inspect %q: %v", ErrManifest, entry.Name(), err)
		}
		if infoAfter.Mode()&os.ModeSymlink != 0 || !sameRegularMigrationFile(infoBefore, infoHandleAfter) ||
			!sameRegularMigrationFile(infoBefore, infoAfter) {
			return nil, fmt.Errorf("%w: %q changed while reading", ErrManifest, entry.Name())
		}
		if sqlIsEmpty(string(contents)) {
			return nil, fmt.Errorf("%w: migration %q is empty", ErrManifest, entry.Name())
		}
		sum := sha256.Sum256(contents)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     strings.TrimSuffix(entry.Name(), ".sql"),
			SQL:      string(contents),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	directoryHandleAfter, err := root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("%w: re-inspect opened directory: %v", ErrManifest, err)
	}
	if !os.SameFile(directoryHandle, directoryHandleAfter) {
		return nil, fmt.Errorf("%w: migration directory changed while reading", ErrManifest)
	}
	if err := verifyMigrationDirectoryIdentity(dir, directoryBefore, directoryHandleAfter); err != nil {
		return nil, err
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	if len(migrations) == 0 {
		return nil, fmt.Errorf("%w: no migration files", ErrManifest)
	}
	for i, migration := range migrations {
		want := i + 1
		if migration.Version != want {
			return nil, fmt.Errorf("%w: version %03d found where %03d was required", ErrSequence, migration.Version, want)
		}
	}
	return migrations, nil
}

func verifyMigrationDirectoryIdentity(dir string, before, handle os.FileInfo) error {
	after, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%w: re-inspect directory: %v", ErrManifest, err)
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.IsDir() || !handle.IsDir() ||
		!os.SameFile(before, handle) || !os.SameFile(before, after) {
		return fmt.Errorf("%w: migration directory changed while opening", ErrManifest)
	}
	return nil
}

func sameRegularMigrationFile(left, right os.FileInfo) bool {
	return left.Mode().IsRegular() && right.Mode().IsRegular() &&
		os.SameFile(left, right) && left.Mode() == right.Mode() && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}

func sqlIsEmpty(script string) bool {
	script = blockComment.ReplaceAllString(script, "")
	lines := strings.Split(script, "\n")
	var executable strings.Builder
	for _, line := range lines {
		if comment := strings.Index(line, "--"); comment >= 0 {
			line = line[:comment]
		}
		if comment := strings.Index(line, "#"); comment >= 0 {
			line = line[:comment]
		}
		executable.WriteString(line)
	}
	return strings.Trim(executable.String(), " \t\r\n;") == ""
}

func BuildMigrationDSN(cfg config.DatabaseConfig) (string, error) {
	var driverConfig *drivermysql.Config
	var err error
	if cfg.DSN != "" {
		driverConfig, err = drivermysql.ParseDSN(cfg.DSN)
		if err != nil {
			return "", errors.New("parse database configuration: invalid DSN")
		}
	} else {
		location, err := time.LoadLocation(cfg.Loc)
		if err != nil {
			return "", errors.New("load database location: invalid location")
		}
		driverConfig = drivermysql.NewConfig()
		driverConfig.User = cfg.User
		driverConfig.Passwd = cfg.Password
		driverConfig.Net = "tcp"
		driverConfig.Addr = net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
		driverConfig.DBName = cfg.Name
		driverConfig.ParseTime = cfg.ParseTime
		driverConfig.Loc = location
		if !databaseCharset.MatchString(cfg.Charset) {
			return "", errors.New("apply database charset: invalid charset")
		}
		if err := driverConfig.Apply(drivermysql.Charset(cfg.Charset, "")); err != nil {
			return "", errors.New("apply database charset: invalid charset")
		}
	}
	driverConfig.MultiStatements = true
	if driverConfig.Timeout <= 0 {
		driverConfig.Timeout = 5 * time.Second
	}
	formattedDSN := driverConfig.FormatDSN()
	if err := validateMigrationDSNIdentifiers(formattedDSN); err != nil {
		return "", err
	}
	return formattedDSN, nil
}

func validateMigrationDSNIdentifiers(dsn string) error {
	queryAt := strings.LastIndexByte(dsn, '?')
	if queryAt < 0 {
		return nil
	}
	params, err := url.ParseQuery(dsn[queryAt+1:])
	if err != nil {
		return errors.New("parse database configuration: invalid DSN")
	}
	for _, charsets := range params["charset"] {
		for charset := range strings.SplitSeq(charsets, ",") {
			if !databaseCharset.MatchString(charset) {
				return errors.New("apply database charset: invalid charset")
			}
		}
	}
	for _, collation := range params["collation"] {
		if !databaseCharset.MatchString(collation) {
			return errors.New("apply database collation: invalid collation")
		}
	}
	return nil
}
