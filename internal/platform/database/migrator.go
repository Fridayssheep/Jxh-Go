package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Migrator struct{}

func NewMigrator() *Migrator { return &Migrator{} }

func (m *Migrator) Apply(ctx context.Context, db *gorm.DB) error {
	if ctx == nil || db == nil {
		return fmt.Errorf("schema migrator requires a database")
	}
	var acquired int
	if err := db.WithContext(ctx).Raw("SELECT GET_LOCK(?, ?)", "jxh_schema_migrations", 30).Scan(&acquired).Error; err != nil {
		return fmt.Errorf("acquire schema migration lock: %w", err)
	} else if acquired != 1 {
		return fmt.Errorf("acquire schema migration lock: timed out after 30s, another instance may be migrating")
	}
	defer db.Exec("SELECT RELEASE_LOCK(?)", "jxh_schema_migrations")
	if err := db.WithContext(ctx).Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
version BIGINT UNSIGNED NOT NULL PRIMARY KEY,
name VARCHAR(255) NOT NULL,
applied_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`).Error; err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return fmt.Errorf("read schema migrations: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		versionText, _, ok := strings.Cut(entry.Name(), "_")
		version, parseErr := strconv.ParseUint(versionText, 10, 64)
		if !ok || parseErr != nil || version == 0 {
			return fmt.Errorf("invalid schema migration name %q", entry.Name())
		}
		var count int64
		if err := db.WithContext(ctx).Table("schema_migrations").Where("version = ?", version).Count(&count).Error; err != nil {
			return fmt.Errorf("check schema migration %d: %w", version, err)
		}
		if count > 0 {
			continue
		}
		content, readErr := migrationFiles.ReadFile("migrations/" + entry.Name())
		if readErr != nil {
			return fmt.Errorf("read schema migration %d: %w", version, readErr)
		}
		statements := splitMigrationStatements(string(content))
		if len(statements) == 0 {
			return fmt.Errorf("schema migration %d is empty", version)
		}
		for index, statement := range statements {
			if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
				if isAlreadyAppliedError(err) {
					continue
				}
				return fmt.Errorf("apply schema migration %q statement %d/%d (%s): %w",
					entry.Name(), index+1, len(statements), summarizeStatement(statement), err)
			}
		}
		if err := db.WithContext(ctx).Exec("INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
			version, entry.Name(), time.Now().UTC()).Error; err != nil {
			return fmt.Errorf("record schema migration %d: %w", version, err)
		}
	}
	return nil
}

// alreadyAppliedErrorNumbers are MySQL errors meaning the object a statement wants to
// create already exists. Migration statements are declarative: if the target object is
// already present the statement's goal is satisfied, so these are safe to skip. This is
// what lets a migration that failed partway (MySQL commits DDL and cannot roll it back)
// converge on the next run instead of failing forever on its first statement.
var alreadyAppliedErrorNumbers = map[uint16]struct{}{
	1050: {}, // ER_TABLE_EXISTS_ERROR
	1060: {}, // ER_DUP_FIELDNAME
	1061: {}, // ER_DUP_KEYNAME
	1826: {}, // ER_FK_DUP_NAME
}

func isAlreadyAppliedError(err error) bool {
	var mysqlError *drivermysql.MySQLError
	if !errors.As(err, &mysqlError) {
		return false
	}
	_, ok := alreadyAppliedErrorNumbers[mysqlError.Number]
	return ok
}

// summarizeStatement renders a single-line excerpt of a statement so migration failures
// identify the offending SQL without dumping a whole CREATE TABLE into the logs.
func summarizeStatement(statement string) string {
	const limit = 120
	collapsed := strings.Join(strings.Fields(statement), " ")
	if len(collapsed) <= limit {
		return collapsed
	}
	return collapsed[:limit] + "..."
}

func splitMigrationStatements(content string) []string {
	var result []string
	var current strings.Builder

	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		if current.Len() > 0 {
			current.WriteString("\n")
		}
		current.WriteString(line)

		// Naive terminator detection: adequate for plain DDL, but a statement containing
		// a ";" inside a string literal or a DELIMITER block would split incorrectly.
		if strings.HasSuffix(trimmed, ";") {
			if statement := strings.TrimSpace(current.String()); statement != "" && statement != ";" {
				result = append(result, statement)
			}
			current.Reset()
		}
	}

	if statement := strings.TrimSpace(current.String()); statement != "" {
		result = append(result, statement)
	}

	return result
}
