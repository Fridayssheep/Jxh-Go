package database

import (
	"context"
	"os"
	"testing"

	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestMigratorAppliesJoinApprovalV2(t *testing.T) {
	dsn := os.Getenv("JXH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("JXH_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	migrator := NewMigrator()
	if err := migrator.Apply(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := migrator.Apply(context.Background(), db); err != nil {
		t.Fatalf("second migration run must be idempotent: %v", err)
	}
	for _, table := range []string{
		"schema_migrations", "join_approval_rule_state", "join_major_code_samples",
		"join_evidence_rebuild_operations", "admission_roster_versions", "admission_roster_entries",
	} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("migration did not create table %s", table)
		}
	}
	if !db.Migrator().HasColumn("group_join_requests", "automatic_review") ||
		!db.Migrator().HasColumn("group_join_decisions", "review_snapshot") {
		t.Fatal("migration did not add automatic review snapshot columns")
	}
	var count int64
	if err := db.Table("schema_migrations").Where("version = ?", 2).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("migration version count = %d, err = %v", count, err)
	}
}
