package database

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
	"time"

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
	if err := db.WithContext(ctx).Raw("SELECT GET_LOCK(?, ?)", "jxh_schema_migrations", 30).Scan(&acquired).Error; err != nil || acquired != 1 {
		return fmt.Errorf("acquire schema migration lock")
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
		for _, statement := range statements {
			if err := db.WithContext(ctx).Exec(statement).Error; err != nil {
				return fmt.Errorf("apply schema migration %d: %w", version, err)
			}
		}
		if err := db.WithContext(ctx).Exec("INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
			version, entry.Name(), time.Now().UTC()).Error; err != nil {
			return fmt.Errorf("record schema migration %d: %w", version, err)
		}
	}
	return nil
}

func splitMigrationStatements(content string) []string {
	parts := strings.Split(content, ";")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
