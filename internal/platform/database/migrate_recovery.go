package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type historicalMigrationIdentity struct {
	name     string
	checksum string
}

var historicalMigrationIdentities = [...]historicalMigrationIdentity{
	{name: "001_create_core_schema", checksum: "81f71d4c8db2a412f0f9b0f1d4d61d6d53ecc538b801b2c365d570f789fa66a9"},
	{name: "002_add_run_date_to_scheduled_jobs", checksum: "df0b7f62b0e0465a64d7c90b9d798cada31159ffa683e22abceab41724d395fa"},
	{name: "003_expand_group_request_flag", checksum: "0703d0fe865e6865d0047ae84ba75ab9a9506b72d90c60f74c3b36051ac11306"},
	{name: "004_use_binary_collation_for_identifiers", checksum: "254b502311291b48f7002c041fb6c96cad16f4386aa26e195a6bf373aa41bf17"},
	{name: "005_automate_group_request_processing", checksum: "a2239296a829056b33833806a7a064ab6db7ad677f915c723bfe21cd92f9bdae"},
	{name: "006_reparse_group_request_applicants", checksum: "42ad208b9fcbf9990fc295979d17b037bc7050410e9440b2dcffa46fae8e6248"},
}

var repositoryMigration007Identity = historicalMigrationIdentity{
	name:     "007_remove_group_request_system_request_id",
	checksum: "94c4e2d5edb46c0c920540684c63585973efa419c321cdcadc9e69e779ada971",
}

func classifyHistoricalManifest(migrations []Migration) (bool, error) {
	limit := min(len(migrations), len(historicalMigrationIdentities))
	repositoryHistory := false
	for i := 0; i < limit; i++ {
		migration := migrations[i]
		identity := historicalMigrationIdentities[i]
		sum := sha256.Sum256([]byte(migration.SQL))
		if migration.Version == i+1 && (migration.Name == identity.name ||
			migration.Checksum == identity.checksum || hex.EncodeToString(sum[:]) == identity.checksum) {
			repositoryHistory = true
		}
	}
	if !repositoryHistory {
		return false, nil
	}
	for i := 0; i < limit; i++ {
		if !isHistoricalRecoveryMigration(migrations[i]) {
			return false, fmt.Errorf("%w: repository historical migration %03d does not match the immutable manifest", ErrDrift, i+1)
		}
	}
	if len(migrations) >= 7 && !isRepositoryMigration007(migrations[6]) {
		return false, fmt.Errorf("%w: repository migration 007 does not match the immutable manifest", ErrDrift)
	}
	return limit > 0, nil
}

func isHistoricalRecoveryMigration(migration Migration) bool {
	if migration.Version < 1 || migration.Version > len(historicalMigrationIdentities) {
		return false
	}
	identity := historicalMigrationIdentities[migration.Version-1]
	if migration.Name != identity.name || migration.Checksum != identity.checksum {
		return false
	}
	sum := sha256.Sum256([]byte(migration.SQL))
	return hex.EncodeToString(sum[:]) == identity.checksum
}

func isRepositoryMigration007(migration Migration) bool {
	if migration.Version != 7 || migration.Name != repositoryMigration007Identity.name ||
		migration.Checksum != repositoryMigration007Identity.checksum {
		return false
	}
	sum := sha256.Sum256([]byte(migration.SQL))
	return hex.EncodeToString(sum[:]) == repositoryMigration007Identity.checksum
}

const post008CoreSchemaChecksum = "3216dd9ebc4b74952668222d776db25aac7604055bff8b5cb3c66d322c3e760d"

func (r Runner) applyMigration007(ctx context.Context, conn *sql.Conn, migration Migration) error {
	sum := sha256.Sum256([]byte(migration.SQL))
	if migration.Checksum != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%w: migration 007 checksum does not match its SQL", ErrManifest)
	}
	snapshot, err := loadLegacySchema(ctx, conn)
	if err != nil {
		return err
	}
	if !sameLegacySchema(snapshot, knownPost005LegacySchema()) && !isRecoverableMigration007CompletedSchema(snapshot) {
		return historicalRecoveryDrift(migration)
	}
	if _, err := conn.ExecContext(ctx, migration.SQL); err != nil {
		return safeDatabaseError(fmt.Sprintf("execute migration %03d %s", migration.Version, migration.Name), err)
	}
	snapshot, err = loadLegacySchema(ctx, conn)
	if err != nil {
		return err
	}
	if !isRecoverableMigration007CompletedSchema(snapshot) {
		return historicalRecoveryDrift(migration)
	}
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO `schema_migrations` (`version`, `name`, `checksum`, `applied_at`) VALUES (?, ?, ?, CURRENT_TIMESTAMP(3))",
		migration.Version, migration.Name, migration.Checksum,
	); err != nil {
		return safeDatabaseError(fmt.Sprintf("record migration %03d %s", migration.Version, migration.Name), err)
	}
	return nil
}

func isRecoverableMigration007CompletedSchema(snapshot legacySchema) bool {
	return sameLegacySchema(snapshot, knownPost007LegacySchema()) ||
		legacySchemaChecksum(snapshot) == post008CoreSchemaChecksum
}

func legacySchemaChecksum(schema legacySchema) string {
	entries := make([]string, 0, len(schema.Columns)+len(schema.Indexes)+len(schema.Tables)+len(schema.Constraints))
	for _, column := range schema.Columns {
		entries = append(entries, "column:"+legacyColumnKey(column))
	}
	for _, index := range schema.Indexes {
		entries = append(entries, "index:"+legacyIndexKey(index))
	}
	for _, table := range schema.Tables {
		entries = append(entries, "table:"+legacyTableKey(table))
	}
	for _, constraint := range schema.Constraints {
		entries = append(entries, "constraint:"+legacyConstraintKey(constraint))
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "\n")))
	return hex.EncodeToString(sum[:])
}

func ensureMigrationAttemptTable(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+"`schema_migration_attempts`"+` (
  `+"`version`"+` int unsigned NOT NULL,
  `+"`name`"+` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `+"`checksum`"+` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `+"`stage`"+` int unsigned NOT NULL DEFAULT 0,
  `+"`started_at`"+` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`+"`version`"+`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`)
	if err != nil {
		return safeDatabaseError("create migration attempt ledger", err)
	}
	return verifyMigrationInfrastructureTable(ctx, conn, "schema_migration_attempts", migrationAttemptLedgerSchema())
}

func countMigrationAttempts(ctx context.Context, conn *sql.Conn) (int, error) {
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM `schema_migration_attempts`").Scan(&count); err != nil {
		return 0, safeDatabaseError("count migration attempts", err)
	}
	return count, nil
}

func validateMigrationAttemptSet(ctx context.Context, conn *sql.Conn, migrations []Migration, appliedCount int) error {
	rows, err := conn.QueryContext(ctx, "SELECT `version`, `name`, `checksum`, `stage` FROM `schema_migration_attempts` ORDER BY `version`")
	if err != nil {
		return safeDatabaseError("inspect migration attempts", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var version, stage int
		var name, checksum string
		if err := rows.Scan(&version, &name, &checksum, &stage); err != nil {
			return safeDatabaseError("inspect migration attempt", err)
		}
		count++
		if count > 1 || version != appliedCount+1 || version > len(migrations) {
			return fmt.Errorf("%w: migration attempt sequence is invalid", ErrDrift)
		}
		migration := migrations[version-1]
		statements, splitErr := splitMigrationStatements(migration.SQL)
		if name != migration.Name || checksum != migration.Checksum || !isHistoricalRecoveryMigration(migration) ||
			splitErr != nil || stage < 0 || stage > len(statements) {
			return fmt.Errorf("%w: migration %03d attempt is invalid", ErrDrift, version)
		}
	}
	if err := rows.Err(); err != nil {
		return safeDatabaseError("inspect migration attempts", err)
	}
	return nil
}

func (r Runner) applyHistoricalMigration(ctx context.Context, conn *sql.Conn, migration Migration) error {
	statements, err := splitMigrationStatements(migration.SQL)
	if err != nil {
		return fmt.Errorf("%w: split historical migration %03d", ErrManifest, migration.Version)
	}
	snapshot, err := loadLegacySchema(ctx, conn)
	if err != nil {
		return err
	}
	attempted, stage, err := loadMigrationAttempt(ctx, conn, migration)
	if err != nil {
		return err
	}
	if attempted && !historicalAttemptStageMatchesSchema(migration.Version, stage, snapshot) {
		return fmt.Errorf("%w: migration %03d attempt stage does not match schema", ErrDrift, migration.Version)
	}
	if !attempted {
		if !sameLegacySchema(snapshot, historicalSchemaAt(migration.Version-1)) {
			return fmt.Errorf("%w: migration %03d has no durable attempt for the current schema", ErrDrift, migration.Version)
		}
		if err := insertMigrationAttempt(ctx, conn, migration); err != nil {
			return err
		}
	}

	switch migration.Version {
	case 1:
		err = r.recoverMigration001(ctx, conn, migration, statements, snapshot)
	case 2:
		err = r.recoverMigration002(ctx, conn, migration, statements, snapshot)
	case 3:
		err = r.recoverMigration003(ctx, conn, migration, statements, snapshot)
	case 4:
		err = r.recoverMigration004(ctx, conn, migration, statements, snapshot)
	case 5:
		err = r.recoverMigration005(ctx, conn, migration, statements, snapshot)
	case 6:
		err = r.recoverMigration006(ctx, conn, migration, statements, snapshot)
	default:
		err = fmt.Errorf("%w: unsupported historical migration %03d", ErrManifest, migration.Version)
	}
	if err != nil {
		return err
	}
	return nil
}

func loadMigrationAttempt(ctx context.Context, conn *sql.Conn, migration Migration) (bool, int, error) {
	var name, checksum string
	var stage int
	err := conn.QueryRowContext(ctx,
		"SELECT `name`, `checksum`, `stage` FROM `schema_migration_attempts` WHERE `version` = ?", migration.Version,
	).Scan(&name, &checksum, &stage)
	if errors.Is(err, sql.ErrNoRows) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, safeDatabaseError(fmt.Sprintf("inspect migration attempt %03d", migration.Version), err)
	}
	if name != migration.Name || checksum != migration.Checksum {
		return false, 0, fmt.Errorf("%w: migration %03d attempt does not match manifest", ErrDrift, migration.Version)
	}
	return true, stage, nil
}

func insertMigrationAttempt(ctx context.Context, conn *sql.Conn, migration Migration) error {
	if _, err := conn.ExecContext(ctx,
		"INSERT INTO `schema_migration_attempts` (`version`, `name`, `checksum`, `stage`) VALUES (?, ?, ?, 0)",
		migration.Version, migration.Name, migration.Checksum,
	); err != nil {
		return safeDatabaseError(fmt.Sprintf("record migration attempt %03d %s", migration.Version, migration.Name), err)
	}
	return nil
}

func (r Runner) recoverMigration001(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, snapshot legacySchema) error {
	want := historicalSchemaAt(1)
	if sameLegacySchema(snapshot, want) {
		return recordMigration(ctx, conn, migration)
	}
	if _, ok := historical001CompletedStatements(snapshot); !ok {
		return historicalRecoveryDrift(migration)
	}
	if err := r.executeHistoricalStatements(ctx, conn, migration, statements, 0); err != nil {
		return err
	}
	return verifyHistoricalSchemaAndRecord(ctx, conn, migration, want)
}

func (r Runner) recoverMigration002(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, snapshot legacySchema) error {
	pre, post := historicalSchemaAt(1), historicalSchemaAt(2)
	switch {
	case sameLegacySchema(snapshot, pre):
		if err := r.executeHistoricalStatements(ctx, conn, migration, statements[:1], 0); err != nil {
			return err
		}
	case sameLegacySchema(snapshot, post):
	default:
		return historicalRecoveryDrift(migration)
	}
	if err := verifyHistoricalSchema(ctx, conn, migration, post); err != nil {
		return err
	}
	return r.executeDMLAndRecord(ctx, conn, migration, statements[1], 2)
}

func (r Runner) recoverMigration003(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, snapshot legacySchema) error {
	pre, post := historicalSchemaAt(2), historicalSchemaAt(3)
	switch {
	case sameLegacySchema(snapshot, pre):
		if err := r.executeHistoricalStatements(ctx, conn, migration, statements, 0); err != nil {
			return err
		}
	case sameLegacySchema(snapshot, post):
	default:
		return historicalRecoveryDrift(migration)
	}
	if err := verifyHistoricalSchema(ctx, conn, migration, post); err != nil {
		return err
	}
	if err := requireZeroRows(ctx, conn, migration, "SELECT COUNT(*) FROM `group_join_requests` WHERE `flag` IS NULL OR `flag` = ''"); err != nil {
		return err
	}
	return recordMigration(ctx, conn, migration)
}

func (r Runner) recoverMigration004(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, snapshot legacySchema) error {
	pre, intermediate, post := historicalSchemaAt(3), historicalMigration004IntermediateSchema(), historicalSchemaAt(4)
	start := -1
	switch {
	case sameLegacySchema(snapshot, pre):
		start = 0
	case sameLegacySchema(snapshot, intermediate):
		start = 1
	case sameLegacySchema(snapshot, post):
		start = len(statements)
	default:
		return historicalRecoveryDrift(migration)
	}
	if start < len(statements) {
		if err := r.executeMigration004Statements(ctx, conn, migration, statements, start); err != nil {
			return err
		}
	}
	return verifyHistoricalSchemaAndRecord(ctx, conn, migration, post)
}

func (r Runner) executeMigration004Statements(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, start int) (err error) {
	prepared := false
	for statementIndex := start; statementIndex < len(statements); statementIndex++ {
		if err = r.executeHistoricalStatements(ctx, conn, migration, statements[statementIndex:statementIndex+1], statementIndex); err != nil {
			if prepared || statementIndex >= 3 && statementIndex < 6 {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, cleanupErr := conn.ExecContext(cleanupCtx, "DEALLOCATE PREPARE alter_trigger_source_key_stmt")
				cancel()
				if cleanupErr != nil {
					err = errors.Join(err, safeDatabaseError("clean up migration 004 prepared statement", cleanupErr))
				}
			}
			return err
		}
		if statementIndex == 3 {
			prepared = true
		} else if statementIndex == 5 {
			prepared = false
		}
	}
	return nil
}

func (r Runner) recoverMigration005(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, snapshot legacySchema) error {
	pre, intermediate, post := historicalSchemaAt(4), historicalMigration005IntermediateSchema(), historicalSchemaAt(5)
	switch {
	case sameLegacySchema(snapshot, pre):
		if err := r.executeHistoricalStatements(ctx, conn, migration, statements, 0); err != nil {
			return err
		}
	case sameLegacySchema(snapshot, intermediate):
		if err := r.executeHistoricalStatements(ctx, conn, migration, statements[1:], 1); err != nil {
			return err
		}
	case sameLegacySchema(snapshot, post):
	default:
		return historicalRecoveryDrift(migration)
	}
	if err := verifyHistoricalSchema(ctx, conn, migration, post); err != nil {
		return err
	}
	if err := requireZeroRows(ctx, conn, migration, "SELECT COUNT(*) FROM `group_join_requests` WHERE `status` = 'observed'"); err != nil {
		return err
	}
	return recordMigration(ctx, conn, migration)
}

func (r Runner) recoverMigration006(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, snapshot legacySchema) error {
	want := historicalSchemaAt(5)
	if !sameLegacySchema(snapshot, want) {
		return historicalRecoveryDrift(migration)
	}
	return r.executeDMLAndRecord(ctx, conn, migration, statements[0], 1)
}

func (r Runner) executeHistoricalStatements(ctx context.Context, conn *sql.Conn, migration Migration, statements []string, offset int) error {
	for i, statement := range statements {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			return safeDatabaseError(fmt.Sprintf("execute migration %03d %s statement %d", migration.Version, migration.Name, offset+i+1), err)
		}
		if r.afterStatement != nil {
			if err := r.afterStatement(ctx, conn, migration, offset+i+1); err != nil {
				return safeDatabaseError(fmt.Sprintf("execute migration %03d %s statement %d", migration.Version, migration.Name, offset+i+1), err)
			}
		}
		if _, err := conn.ExecContext(ctx,
			"UPDATE `schema_migration_attempts` SET `stage` = ? WHERE `version` = ? AND `name` = ? AND `checksum` = ?",
			offset+i+1, migration.Version, migration.Name, migration.Checksum,
		); err != nil {
			return safeDatabaseError(fmt.Sprintf("record migration %03d stage %d", migration.Version, offset+i+1), err)
		}
	}
	return nil
}

func (r Runner) executeDMLAndRecord(ctx context.Context, conn *sql.Conn, migration Migration, statement string, statementNumber int) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return safeDatabaseError(fmt.Sprintf("begin migration %03d %s", migration.Version, migration.Name), err)
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		_ = tx.Rollback()
		return safeDatabaseError(fmt.Sprintf("execute migration %03d %s statement %d", migration.Version, migration.Name, statementNumber), err)
	}
	if r.afterStatement != nil {
		if err := r.afterStatement(ctx, conn, migration, statementNumber); err != nil {
			_ = tx.Rollback()
			return safeDatabaseError(fmt.Sprintf("execute migration %03d %s statement %d", migration.Version, migration.Name, statementNumber), err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE `schema_migration_attempts` SET `stage` = ? WHERE `version` = ? AND `name` = ? AND `checksum` = ?",
		statementNumber, migration.Version, migration.Name, migration.Checksum,
	); err != nil {
		_ = tx.Rollback()
		return safeDatabaseError(fmt.Sprintf("record migration %03d stage %d", migration.Version, statementNumber), err)
	}
	if err := recordMigrationTx(ctx, tx, migration); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := clearMigrationAttemptTx(ctx, tx, migration); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return safeDatabaseError(fmt.Sprintf("commit migration %03d %s", migration.Version, migration.Name), err)
	}
	return nil
}

func recordMigration(ctx context.Context, conn *sql.Conn, migration Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return safeDatabaseError(fmt.Sprintf("begin migration record %03d %s", migration.Version, migration.Name), err)
	}
	if err := recordMigrationTx(ctx, tx, migration); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := clearMigrationAttemptTx(ctx, tx, migration); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return safeDatabaseError(fmt.Sprintf("commit migration record %03d %s", migration.Version, migration.Name), err)
	}
	return nil
}

func recordMigrationTx(ctx context.Context, tx *sql.Tx, migration Migration) error {
	_, err := tx.ExecContext(ctx,
		"INSERT INTO `schema_migrations` (`version`, `name`, `checksum`, `applied_at`) VALUES (?, ?, ?, CURRENT_TIMESTAMP(3))",
		migration.Version, migration.Name, migration.Checksum,
	)
	if err != nil {
		return safeDatabaseError(fmt.Sprintf("record migration %03d %s", migration.Version, migration.Name), err)
	}
	return nil
}

func clearMigrationAttemptTx(ctx context.Context, tx *sql.Tx, migration Migration) error {
	result, err := tx.ExecContext(ctx,
		"DELETE FROM `schema_migration_attempts` WHERE `version` = ? AND `name` = ? AND `checksum` = ?",
		migration.Version, migration.Name, migration.Checksum,
	)
	if err != nil {
		return safeDatabaseError(fmt.Sprintf("clear migration attempt %03d %s", migration.Version, migration.Name), err)
	}
	deleted, err := result.RowsAffected()
	if err != nil || deleted != 1 {
		return fmt.Errorf("%w: migration %03d attempt disappeared before commit", ErrDrift, migration.Version)
	}
	return nil
}

func verifyHistoricalSchemaAndRecord(ctx context.Context, conn *sql.Conn, migration Migration, want legacySchema) error {
	if err := verifyHistoricalSchema(ctx, conn, migration, want); err != nil {
		return err
	}
	return recordMigration(ctx, conn, migration)
}

func verifyHistoricalSchema(ctx context.Context, conn *sql.Conn, migration Migration, want legacySchema) error {
	snapshot, err := loadLegacySchema(ctx, conn)
	if err != nil {
		return err
	}
	if !sameLegacySchema(snapshot, want) {
		return historicalRecoveryDrift(migration)
	}
	return nil
}

func requireZeroRows(ctx context.Context, conn *sql.Conn, migration Migration, query string) error {
	var count int
	if err := conn.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return safeDatabaseError(fmt.Sprintf("verify migration %03d %s data", migration.Version, migration.Name), err)
	}
	if count != 0 {
		return fmt.Errorf("%w: migration %03d data state is ambiguous", ErrDrift, migration.Version)
	}
	return nil
}

func historicalRecoveryDrift(migration Migration) error {
	return fmt.Errorf("%w: migration %03d schema is not an exact recoverable stage", ErrDrift, migration.Version)
}

func historicalAttemptStageMatchesSchema(version, stage int, snapshot legacySchema) bool {
	switch version {
	case 1:
		completed, ok := historical001CompletedStatements(snapshot)
		return ok && (stage == completed || completed > 0 && stage == completed-1)
	case 2:
		if sameLegacySchema(snapshot, historicalSchemaAt(1)) {
			return stage == 0
		}
		if sameLegacySchema(snapshot, historicalSchemaAt(2)) {
			return stage == 0 || stage == 1
		}
	case 3:
		if sameLegacySchema(snapshot, historicalSchemaAt(2)) {
			return stage == 0 || stage == 1
		}
		if sameLegacySchema(snapshot, historicalSchemaAt(3)) {
			return stage == 1 || stage == 2
		}
	case 4:
		if sameLegacySchema(snapshot, historicalSchemaAt(3)) {
			return stage == 0
		}
		if sameLegacySchema(snapshot, historicalMigration004IntermediateSchema()) {
			return stage >= 0 && stage <= 4
		}
		if sameLegacySchema(snapshot, historicalSchemaAt(4)) {
			return stage >= 4 && stage <= 6
		}
	case 5:
		if sameLegacySchema(snapshot, historicalSchemaAt(4)) {
			return stage == 0
		}
		if sameLegacySchema(snapshot, historicalMigration005IntermediateSchema()) {
			return stage >= 0 && stage <= 2
		}
		if sameLegacySchema(snapshot, historicalSchemaAt(5)) {
			return stage == 2 || stage == 3
		}
	case 6:
		return sameLegacySchema(snapshot, historicalSchemaAt(5)) && stage == 0
	}
	return false
}

func historical001CompletedStatements(snapshot legacySchema) (int, bool) {
	want := historicalSchemaAt(1)
	statementTables := []string{"knowledge_trigger_logs", "scheduled_jobs", "group_join_requests"}
	present := make(map[string]bool, len(snapshot.Tables))
	for _, table := range snapshot.Tables {
		present[table.Name] = true
	}
	completed := 0
	for _, table := range statementTables {
		if !present[table] {
			break
		}
		completed++
	}
	for _, table := range statementTables[completed:] {
		if present[table] {
			return 0, false
		}
	}
	expected := filterLegacySchemaTables(want, statementTables[:completed])
	return completed, sameLegacySchema(snapshot, expected)
}

func filterLegacySchemaTables(schema legacySchema, tables []string) legacySchema {
	included := make(map[string]struct{}, len(tables))
	for _, table := range tables {
		included[table] = struct{}{}
	}
	var result legacySchema
	for _, column := range schema.Columns {
		if _, ok := included[column.Table]; ok {
			result.Columns = append(result.Columns, column)
		}
	}
	for _, index := range schema.Indexes {
		if _, ok := included[index.Table]; ok {
			result.Indexes = append(result.Indexes, index)
		}
	}
	for _, table := range schema.Tables {
		if _, ok := included[table.Name]; ok {
			result.Tables = append(result.Tables, table)
		}
	}
	for _, constraint := range schema.Constraints {
		if _, ok := included[constraint.Table]; ok {
			result.Constraints = append(result.Constraints, constraint)
		}
	}
	return result
}

func historicalSchemaAt(version int) legacySchema {
	switch version {
	case 0:
		return legacySchema{}
	case 1:
		return historicalSchema001()
	case 2:
		return historicalSchema002()
	case 3:
		return historicalSchema003()
	case 4:
		return historicalSchema004()
	case 5, 6:
		return knownPost005LegacySchema()
	default:
		return legacySchema{}
	}
}

func historicalSchema004() legacySchema {
	schema := knownPost007LegacySchema()
	removePost005Schema(&schema)
	return schema
}

func historicalSchema003() legacySchema {
	schema := historicalSchema004()
	setColumnCollation(&schema, "knowledge_trigger_logs", "source_key", "utf8mb4_0900_ai_ci")
	setColumnCollation(&schema, "group_join_requests", "flag", "utf8mb4_0900_ai_ci")
	return schema
}

func historicalSchema002() legacySchema {
	schema := historicalSchema003()
	for i := range schema.Columns {
		if schema.Columns[i].Table == "group_join_requests" && schema.Columns[i].Ordinal > 1 {
			schema.Columns[i].Ordinal++
		}
	}
	setColumn(&schema, legacyColumn{
		Table: "group_join_requests", Name: "request_key", Ordinal: 2, Type: "varchar(191)",
		Collation: "utf8mb4_0900_ai_ci", Default: legacyNullDefault,
	})
	for i := range schema.Columns {
		if schema.Columns[i].Table == "group_join_requests" && schema.Columns[i].Name == "flag" {
			schema.Columns[i].Nullable = true
		}
	}
	removeIndex(&schema, "group_join_requests", "idx_group_join_requests_flag")
	removeConstraint(&schema, "group_join_requests", "idx_group_join_requests_flag")
	schema.Indexes = append(schema.Indexes, knownLegacyIndex("group_join_requests", "idx_group_join_requests_request_key", "request_key", 1, true))
	schema.Constraints = append(schema.Constraints, legacyConstraint{
		Table: "group_join_requests", Name: "idx_group_join_requests_request_key", Type: "UNIQUE", Ordinal: 1, Column: "request_key",
	})
	return schema
}

func historicalSchema001() legacySchema {
	schema := historicalSchema002()
	removeColumn(&schema, "scheduled_jobs", "run_date")
	for i := range schema.Columns {
		if schema.Columns[i].Table == "scheduled_jobs" && schema.Columns[i].Ordinal > 4 {
			schema.Columns[i].Ordinal--
		}
	}
	return schema
}

func historicalMigration004IntermediateSchema() legacySchema {
	schema := historicalSchema003()
	setColumnCollation(&schema, "group_join_requests", "flag", "utf8mb4_bin")
	return schema
}

func historicalMigration005IntermediateSchema() legacySchema {
	schema := knownPost005LegacySchema()
	for i := range schema.Columns {
		if schema.Columns[i].Table == "group_join_requests" && schema.Columns[i].Name == "ai_parse_status" {
			schema.Columns[i].Default = "skipped"
		}
	}
	return schema
}

func removePost005Schema(schema *legacySchema) {
	for _, name := range []string{"system_request_id", "major", "system_raw_json", "ai_parse_status", "ai_parse_attempts", "processed_at", "ai_parsed_at"} {
		removeColumn(schema, "group_join_requests", name)
	}
	groupOrdinal := 0
	for i := range schema.Columns {
		if schema.Columns[i].Table == "group_join_requests" {
			groupOrdinal++
			schema.Columns[i].Ordinal = groupOrdinal
		}
	}
	removeIndex(schema, "group_join_requests", "idx_group_join_requests_ai_parse_status")
}

func setColumn(schema *legacySchema, column legacyColumn) {
	schema.Columns = append(schema.Columns, column)
}

func removeColumn(schema *legacySchema, table, name string) {
	kept := schema.Columns[:0]
	for _, column := range schema.Columns {
		if column.Table != table || column.Name != name {
			kept = append(kept, column)
		}
	}
	schema.Columns = kept
}

func setColumnCollation(schema *legacySchema, table, name, collation string) {
	for i := range schema.Columns {
		if schema.Columns[i].Table == table && schema.Columns[i].Name == name {
			schema.Columns[i].Collation = collation
		}
	}
}

func removeIndex(schema *legacySchema, table, name string) {
	kept := schema.Indexes[:0]
	for _, index := range schema.Indexes {
		if index.Table != table || index.Name != name {
			kept = append(kept, index)
		}
	}
	schema.Indexes = kept
}

func removeConstraint(schema *legacySchema, table, name string) {
	kept := schema.Constraints[:0]
	for _, constraint := range schema.Constraints {
		if constraint.Table != table || constraint.Name != name {
			kept = append(kept, constraint)
		}
	}
	schema.Constraints = kept
}

func splitMigrationStatements(script string) ([]string, error) {
	const (
		stateSQL = iota
		stateSingleQuote
		stateDoubleQuote
		stateBacktick
		stateLineComment
		stateHashComment
		stateBlockComment
	)
	state := stateSQL
	start := 0
	statements := make([]string, 0, 4)
	for i := 0; i < len(script); i++ {
		char := script[i]
		switch state {
		case stateSQL:
			switch char {
			case '\'':
				state = stateSingleQuote
			case '"':
				state = stateDoubleQuote
			case '`':
				state = stateBacktick
			case '#':
				state = stateHashComment
			case '-':
				if i+1 < len(script) && script[i+1] == '-' {
					state = stateLineComment
					i++
				}
			case '/':
				if i+1 < len(script) && script[i+1] == '*' {
					state = stateBlockComment
					i++
				}
			case ';':
				statements = append(statements, script[start:i+1])
				start = i + 1
			}
		case stateSingleQuote, stateDoubleQuote, stateBacktick:
			quote := byte('\'')
			if state == stateDoubleQuote {
				quote = '"'
			} else if state == stateBacktick {
				quote = '`'
			}
			if char == '\\' && state != stateBacktick && i+1 < len(script) {
				i++
			} else if char == quote {
				if i+1 < len(script) && script[i+1] == quote {
					i++
				} else {
					state = stateSQL
				}
			}
		case stateLineComment, stateHashComment:
			if char == '\n' {
				state = stateSQL
			}
		case stateBlockComment:
			if char == '*' && i+1 < len(script) && script[i+1] == '/' {
				state = stateSQL
				i++
			}
		}
	}
	if state == stateSingleQuote || state == stateDoubleQuote || state == stateBacktick || state == stateBlockComment {
		return nil, errors.New("unterminated SQL token")
	}
	if start < len(script) {
		if len(statements) == 0 || strings.TrimSpace(script[start:]) != "" {
			return nil, errors.New("SQL statement is missing a terminator")
		}
		statements[len(statements)-1] += script[start:]
	}
	return statements, nil
}
