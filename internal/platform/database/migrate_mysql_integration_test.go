package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
)

var mysqlIntegrationSchemaID atomic.Uint64

func TestMySQLBotRestartMigrationAllowsAllSystemOperationTypes(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	if _, err := (Runner{DB: db, LockTimeout: 5 * time.Second}).Apply(t.Context(), migrations); err != nil {
		t.Fatal(err)
	}
	var clause string
	if err := db.QueryRowContext(t.Context(), `SELECT check_clause
FROM information_schema.check_constraints
WHERE constraint_schema = DATABASE() AND constraint_name = 'chk_system_operations_type'`).Scan(&clause); err != nil {
		t.Fatal(err)
	}
	for _, operationType := range []string{"napcat_restart", "knowledge_reload", "bot_restart"} {
		if !strings.Contains(clause, operationType) {
			t.Fatalf("system operation constraint %q does not allow %s", clause, operationType)
		}
	}
}

func TestMySQLHistoricalSchemaFingerprintsMatchEveryVersion(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	for version := 1; version <= 5; version++ {
		if _, err := db.ExecContext(t.Context(), migrations[version-1].SQL); err != nil {
			t.Fatalf("execute migration %03d directly: %v", version, err)
		}
		conn, err := db.Conn(t.Context())
		if err != nil {
			t.Fatalf("open fingerprint connection: %v", err)
		}
		actual, err := loadLegacySchema(t.Context(), conn)
		_ = conn.Close()
		if err != nil {
			t.Fatalf("load migration %03d schema: %v", version, err)
		}
		want := historicalSchemaAt(version)
		if !sameLegacySchema(actual, want) {
			t.Fatalf("migration %03d fingerprint mismatch:\n%s", version, legacySchemaDifference(actual, want))
		}
	}
}

func TestMySQLRunnerRejectsMalformedMigrationLedgerBeforeBusinessDDL(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE schema_migrations (
  version int unsigned NOT NULL,
  name varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  applied_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  unexpected int DEFAULT NULL,
  PRIMARY KEY (version),
  UNIQUE KEY uq_schema_migrations_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		t.Fatalf("create malformed migration ledger: %v", err)
	}

	migrations := repositoryMigrations(t)
	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:1]); !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift", err)
	}
	assertMySQLTableAbsent(t, db, "knowledge_trigger_logs")
}

func TestMySQLRunnerRejectsMalformedMigrationAttemptLedgerBeforeBusinessDDL(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE schema_migrations (
  version int unsigned NOT NULL,
  name varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  applied_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version),
  UNIQUE KEY uq_schema_migrations_name (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		t.Fatalf("create migration ledger: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE schema_migration_attempts (
  version int unsigned NOT NULL,
  name varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  stage int unsigned NOT NULL DEFAULT 0,
  started_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  unexpected int DEFAULT NULL,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		t.Fatalf("create malformed migration attempt ledger: %v", err)
	}

	migrations := repositoryMigrations(t)
	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:1]); !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift", err)
	}
	assertMySQLTableAbsent(t, db, "knowledge_trigger_logs")
}

func TestMySQLRunnerRejectsMalformedAttemptLedgerForGenericManifest(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE schema_migration_attempts (
  version int unsigned NOT NULL,
  name varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  stage int unsigned NOT NULL DEFAULT 0,
  started_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  unexpected int DEFAULT NULL,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		t.Fatalf("create malformed migration attempt ledger: %v", err)
	}
	script := "CREATE TABLE generic_business_table (id int NOT NULL PRIMARY KEY);"
	sum := sha256.Sum256([]byte(script))
	migration := Migration{
		Version:  1,
		Name:     "001_generic_business_table",
		SQL:      script,
		Checksum: hex.EncodeToString(sum[:]),
	}

	if _, err := (Runner{DB: db}).Apply(t.Context(), []Migration{migration}); !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift", err)
	}
	assertMySQLTableAbsent(t, db, "generic_business_table")
}

func TestMySQLRunnerRejectsInfrastructureIdentifierCaseDrift(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{
			name: "migration ledger index",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), `CREATE TABLE schema_migrations (
  version int unsigned NOT NULL,
  name varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  applied_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version),
  UNIQUE KEY UQ_SCHEMA_MIGRATIONS_NAME (name)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
					t.Fatalf("create case-drifted migration ledger: %v", err)
				}
			},
		},
		{
			name: "migration attempt column",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), `CREATE TABLE schema_migration_attempts (
  version int unsigned NOT NULL,
  name varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  checksum char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  Stage int unsigned NOT NULL DEFAULT 0,
  started_at datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
					t.Fatalf("create case-drifted migration attempt ledger: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			test.setup(t, db)

			migrations := repositoryMigrations(t)
			if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:1]); !errors.Is(err, ErrDrift) {
				t.Fatalf("Apply() error = %v, want ErrDrift", err)
			}
			assertMySQLTableAbsent(t, db, "knowledge_trigger_logs")
		})
	}
}

func TestMySQLRunnerRejectsUnknownPersistentObjectBeforeBusinessDDL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
	}{
		{name: "table", sql: "CREATE TABLE unexpected_table (id int NOT NULL PRIMARY KEY)"},
		{name: "view", sql: "CREATE VIEW unexpected_view AS SELECT 1 AS id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			if _, err := db.ExecContext(t.Context(), test.sql); err != nil {
				t.Fatalf("create unknown persistent object: %v", err)
			}

			migrations := repositoryMigrations(t)
			if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:1]); !errors.Is(err, ErrLegacySchema) {
				t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
			}
			assertMySQLTableAbsent(t, db, "knowledge_trigger_logs")
			if got := migrationLedgerCount(t, db); got != 0 {
				t.Fatalf("migration ledger rows = %d, want 0", got)
			}
		})
	}
}

func TestMySQLRunnerRejectsPartialManagerSchemaBeforeLegacyAdoption(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	applyMySQLMigrations(t, db, migrations[:5])
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE admin_users (
  id bigint unsigned NOT NULL AUTO_INCREMENT,
  unexpected_column int NOT NULL,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		t.Fatalf("create partial manager table: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "DELETE FROM schema_migrations"); err != nil {
		t.Fatalf("clear migration ledger: %v", err)
	}

	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations); !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
	}
	if got := migrationLedgerCount(t, db); got != 0 {
		t.Fatalf("legacy adoption recorded %d rows, want 0", got)
	}
	assertMySQLColumnPresent(t, db, "group_join_requests", "system_request_id")
}

func legacySchemaDifference(actual, want legacySchema) string {
	var differences []string
	diffKeys := func(label string, actualKeys, wantKeys []string) {
		actualSet := make(map[string]struct{}, len(actualKeys))
		wantSet := make(map[string]struct{}, len(wantKeys))
		for _, key := range actualKeys {
			actualSet[key] = struct{}{}
		}
		for _, key := range wantKeys {
			wantSet[key] = struct{}{}
		}
		for key := range actualSet {
			if _, ok := wantSet[key]; !ok {
				differences = append(differences, "actual-only "+label+": "+key)
			}
		}
		for key := range wantSet {
			if _, ok := actualSet[key]; !ok {
				differences = append(differences, "want-only "+label+": "+key)
			}
		}
	}
	actualColumns, wantColumns := make([]string, 0, len(actual.Columns)), make([]string, 0, len(want.Columns))
	for _, value := range actual.Columns {
		actualColumns = append(actualColumns, legacyColumnKey(value))
	}
	for _, value := range want.Columns {
		wantColumns = append(wantColumns, legacyColumnKey(value))
	}
	diffKeys("column", actualColumns, wantColumns)
	actualIndexes, wantIndexes := make([]string, 0, len(actual.Indexes)), make([]string, 0, len(want.Indexes))
	for _, value := range actual.Indexes {
		actualIndexes = append(actualIndexes, legacyIndexKey(value))
	}
	for _, value := range want.Indexes {
		wantIndexes = append(wantIndexes, legacyIndexKey(value))
	}
	diffKeys("index", actualIndexes, wantIndexes)
	return strings.Join(differences, "\n")
}

func TestMySQLLegacyRecognitionRejectsFunctionalIndex(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	applyMySQLMigrations(t, db, migrations[:7])

	if _, err := db.ExecContext(t.Context(), "CREATE INDEX `idx_group_join_requests_functional_test` ON `group_join_requests` ((`group_id` + 0))"); err != nil {
		t.Fatalf("create functional index: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "DELETE FROM `schema_migrations`"); err != nil {
		t.Fatalf("clear migration ledger: %v", err)
	}

	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations); !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
	}
}

func TestMySQLMigrationsRecoverFromEveryUnrecordedStage(t *testing.T) {
	migrations := repositoryMigrations(t)

	for _, stage := range []string{
		"group-columns", "group-backfill", "group-constraints",
		"scheduled-columns", "scheduled-backfill", "scheduled-constraints",
		"table-admin_users", "table-admin_sessions", "table-admin_audit_logs", "table-admin_idempotency_keys",
		"table-managed_groups", "table-feature_settings", "table-group_join_policies", "table-custom_commands",
		"table-custom_command_runs", "table-group_join_decisions", "table-scheduled_job_runs",
		"table-bot_operation_events", "table-bot_operation_daily", "table-system_operations",
		"seed-global-settings", "group-last-decision-fk",
		"session-trigger-drop", "session-trigger-insert", "session-trigger-update",
	} {
		t.Run("008 interrupted after "+stage, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			applyMySQLMigrations(t, db, migrations[:7])

			failed := append([]Migration(nil), migrations...)
			marker := "-- jxh:008-stage " + stage + "\n"
			position := strings.Index(failed[7].SQL, marker)
			if position < 0 {
				t.Fatalf("008 atomic stage marker %q is missing", stage)
			}
			position += len(marker)
			failed[7].SQL = failed[7].SQL[:position] + "SELECT * FROM `jxh_forced_008_failure`;\n" + failed[7].SQL[position:]
			if _, err := (Runner{DB: db}).Apply(t.Context(), failed); err == nil {
				t.Fatal("injected 008 failure unexpectedly succeeded")
			}
			if got := migrationLedgerCount(t, db); got != 7 {
				t.Fatalf("ledger rows after failed 008 = %d, want 7", got)
			}

			applyMySQLMigrations(t, db, migrations)
		})
	}

	t.Run("completed SQL without ledger", func(t *testing.T) {
		db := openMySQLIntegrationSchema(t)
		applyMySQLMigrations(t, db, migrations)
		latest := migrations[len(migrations)-1].Version
		if _, err := db.ExecContext(t.Context(), "DELETE FROM `schema_migrations` WHERE `version` = ?", latest); err != nil {
			t.Fatalf("remove latest ledger row: %v", err)
		}
		applyMySQLMigrations(t, db, migrations)
	})

	t.Run("007 and 008 completed without ledger", func(t *testing.T) {
		db := openMySQLIntegrationSchema(t)
		applyMySQLMigrations(t, db, migrations[:8])
		if _, err := db.ExecContext(t.Context(), "DELETE FROM `schema_migrations` WHERE `version` IN (7, 8)"); err != nil {
			t.Fatalf("remove 007-008 ledger rows: %v", err)
		}
		applyMySQLMigrations(t, db, migrations)
	})

	t.Run("existing incompatible manager table", func(t *testing.T) {
		db := openMySQLIntegrationSchema(t)
		applyMySQLMigrations(t, db, migrations[:7])
		if _, err := db.ExecContext(t.Context(), `CREATE TABLE admin_users (
  user_id varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  unexpected_column int NOT NULL,
  PRIMARY KEY (user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
			t.Fatalf("create incompatible admin_users: %v", err)
		}
		if _, err := (Runner{DB: db}).Apply(t.Context(), migrations); err == nil {
			t.Fatal("008 silently accepted an incompatible existing admin_users table")
		}
		if got := migrationLedgerCount(t, db); got != 7 {
			t.Fatalf("ledger rows after incompatible table = %d, want 7", got)
		}
	})
}

func TestMySQLMigration008RecoversKnownPartialMetadata(t *testing.T) {
	for _, test := range []struct {
		name                        string
		canonicalComments           bool
		legacyFlagComment           bool
		canonicalAfterNormalization bool
	}{
		{name: "current comments"},
		{name: "historical flag comment", legacyFlagComment: true},
		{name: "canonical comments", canonicalComments: true},
		{name: "canonical comments after normalization", canonicalAfterNormalization: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			migrations := repositoryMigrations(t)
			applyMySQLMigrations(t, db, migrations[:7])

			failed := append([]Migration(nil), migrations...)
			marker := "-- jxh:008-stage scheduled-columns\n"
			position := strings.Index(failed[7].SQL, marker)
			if position < 0 {
				t.Fatalf("008 atomic stage marker %q is missing", marker)
			}
			position += len(marker)
			failed[7].SQL = failed[7].SQL[:position] + "SELECT * FROM `jxh_forced_008_failure`;\n" + failed[7].SQL[position:]
			if _, err := (Runner{DB: db}).Apply(t.Context(), failed); err == nil {
				t.Fatal("injected 008 failure unexpectedly succeeded")
			}
			if !test.canonicalComments {
				corruptManagerMigration008Comments(t, db, test.legacyFlagComment)
			}
			if test.canonicalAfterNormalization {
				conn, err := db.Conn(t.Context())
				if err != nil {
					t.Fatalf("open connection for 008 recovery preparation: %v", err)
				}
				prepared, err := prepareManagerMigration008Recovery(t.Context(), conn, migrations[7])
				if err != nil {
					_ = conn.Close()
					t.Fatalf("prepare interrupted 008 recovery: %v", err)
				}
				if !strings.Contains(prepared, managerMigration008RecoveredGroupStage2Fingerprint) {
					_ = conn.Close()
					t.Fatal("interrupted 008 recovery did not prepare the recovered stage fingerprint")
				}
				if err := conn.Close(); err != nil {
					t.Fatalf("close connection after 008 comment normalization: %v", err)
				}
			}

			applied, err := (Runner{DB: db, LockTimeout: 5 * time.Second}).Apply(t.Context(), migrations)
			if err != nil {
				t.Fatalf("recover known 008 partial metadata: %v", err)
			}
			if len(applied) != 2 || applied[0].Version != 8 || applied[1].Version != 9 {
				t.Fatalf("applied migrations=%+v, want 008 and 009", applied)
			}
			if got := migrationLedgerCount(t, db); got != 9 {
				t.Fatalf("migration ledger rows=%d, want 9", got)
			}
			if got := migrationAttemptCount(t, db); got != 0 {
				t.Fatalf("migration attempt rows=%d, want 0", got)
			}
			for _, routine := range []string{"jxh_assert_table_008", "jxh_upgrade_core_008", "jxh_create_manager_tables_008"} {
				assertMySQLRoutineAbsent(t, db, routine)
			}
			assertManagerMigration008Comments(t, db)
		})
	}
}

type managerMigration008CommentFixture struct {
	table      string
	column     string
	definition string
	comment    string
	legacy     string
}

var managerMigration008CommentFixtures = []managerMigration008CommentFixture{
	{table: "group_join_requests", column: "flag", definition: "varchar(512) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL", comment: "NapCat 群通知标识；实时事件取 flag，补同步取 request_id 字符串", legacy: "NapCat 群通知标识；实时事件取 flag，系统消息取 request_id 字符串"},
	{table: "group_join_requests", column: "group_id", definition: "bigint DEFAULT NULL", comment: "QQ群号"},
	{table: "group_join_requests", column: "user_id", definition: "bigint DEFAULT NULL", comment: "申请人 QQ"},
	{table: "group_join_requests", column: "student_id", definition: "varchar(64) DEFAULT NULL", comment: "申请信息中显式填写的学号"},
	{table: "group_join_requests", column: "student_name", definition: "varchar(64) DEFAULT NULL", comment: "申请信息中显式填写的姓名"},
	{table: "group_join_requests", column: "major", definition: "varchar(128) DEFAULT NULL", comment: "AI 从验证信息中提取的专业"},
	{table: "group_join_requests", column: "comment", definition: "text DEFAULT NULL", comment: "申请验证信息"},
	{table: "group_join_requests", column: "raw_json", definition: "mediumtext DEFAULT NULL", comment: "NapCat 原始事件或系统消息 JSON"},
	{table: "group_join_requests", column: "system_raw_json", definition: "mediumtext DEFAULT NULL", comment: "NapCat 最近一次系统消息 JSON"},
	{table: "group_join_requests", column: "ai_parse_attempts", definition: "int unsigned NOT NULL DEFAULT 0", comment: "AI 解析尝试次数"},
	{table: "group_join_requests", column: "requested_at", definition: "datetime(3) DEFAULT NULL", comment: "申请时间"},
	{table: "group_join_requests", column: "processed_at", definition: "datetime(3) DEFAULT NULL", comment: "首次观察到已处理状态的时间"},
	{table: "group_join_requests", column: "first_seen_at", definition: "datetime(3) DEFAULT NULL", comment: "首次登记时间"},
	{table: "group_join_requests", column: "last_seen_at", definition: "datetime(3) DEFAULT NULL", comment: "最近出现时间"},
	{table: "group_join_requests", column: "ai_parsed_at", definition: "datetime(3) DEFAULT NULL", comment: "AI 解析完成时间"},
	{table: "scheduled_jobs", column: "type", definition: "varchar(16) NOT NULL", comment: "任务类型：每天/单次"},
	{table: "scheduled_jobs", column: "time_hhmm", definition: "varchar(5) NOT NULL", comment: "触发时间，格式 HH:MM"},
	{table: "scheduled_jobs", column: "run_date", definition: "date DEFAULT NULL", comment: "单次任务执行日期，格式 YYYY-MM-DD；每天任务此字段为 NULL"},
	{table: "scheduled_jobs", column: "group_id", definition: "bigint NOT NULL", comment: "QQ群号"},
	{table: "scheduled_jobs", column: "message", definition: "text NOT NULL", comment: "定时发送内容"},
	{table: "scheduled_jobs", column: "enabled", definition: "boolean NOT NULL", comment: "是否启用"},
	{table: "scheduled_jobs", column: "last_run_at", definition: "datetime(3) DEFAULT NULL", comment: "最近执行时间；用于防止同一天重复触发"},
}

func corruptManagerMigration008Comments(t *testing.T, db *sql.DB, legacyFlagComment bool) {
	t.Helper()
	for _, table := range []string{"group_join_requests", "scheduled_jobs"} {
		clauses := make([]string, 0, len(managerMigration008CommentFixtures))
		for _, fixture := range managerMigration008CommentFixtures {
			if fixture.table != table {
				continue
			}
			comment := fixture.comment
			if legacyFlagComment && fixture.legacy != "" {
				comment = fixture.legacy
			}
			corrupted := strings.ReplaceAll(doubleEncodeUTF8(comment), "'", "''")
			clauses = append(clauses, fmt.Sprintf("MODIFY COLUMN `%s` %s COMMENT '%s'", fixture.column, fixture.definition, corrupted))
		}
		if _, err := db.ExecContext(t.Context(), "ALTER TABLE `"+table+"` "+strings.Join(clauses, ", ")); err != nil {
			t.Fatalf("corrupt %s comments: %v", table, err)
		}
	}
}

func assertManagerMigration008Comments(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, fixture := range managerMigration008CommentFixtures {
		var comment string
		if err := db.QueryRowContext(t.Context(), `SELECT column_comment
FROM information_schema.columns
WHERE table_schema = DATABASE() AND BINARY table_name = BINARY ? AND BINARY column_name = BINARY ?`,
			fixture.table, fixture.column).Scan(&comment); err != nil {
			t.Fatalf("read %s.%s comment: %v", fixture.table, fixture.column, err)
		}
		if comment != fixture.comment {
			t.Fatalf("%s.%s comment=%q, want %q", fixture.table, fixture.column, comment, fixture.comment)
		}
	}
}

func doubleEncodeUTF8(value string) string {
	encoded := []byte(value)
	runes := make([]rune, 0, len(encoded))
	for _, value := range encoded {
		if replacement, ok := windows1252Runes[value]; ok {
			runes = append(runes, replacement)
		} else {
			runes = append(runes, rune(value))
		}
	}
	return string(runes)
}

var windows1252Runes = map[byte]rune{
	0x80: '\u20ac', 0x82: '\u201a', 0x83: '\u0192', 0x84: '\u201e', 0x85: '\u2026', 0x86: '\u2020', 0x87: '\u2021',
	0x88: '\u02c6', 0x89: '\u2030', 0x8a: '\u0160', 0x8b: '\u2039', 0x8c: '\u0152', 0x8e: '\u017d',
	0x91: '\u2018', 0x92: '\u2019', 0x93: '\u201c', 0x94: '\u201d', 0x95: '\u2022', 0x96: '\u2013', 0x97: '\u2014',
	0x98: '\u02dc', 0x99: '\u2122', 0x9a: '\u0161', 0x9b: '\u203a', 0x9c: '\u0153', 0x9e: '\u017e', 0x9f: '\u0178',
}

func TestMySQLHistoricalMigrationsRecoverFromSQLAndLedgerBoundaries(t *testing.T) {
	migrations := repositoryMigrations(t)
	tests := []struct {
		version    int
		boundaries []int
	}{
		{version: 1, boundaries: []int{1, 2, 3}},
		{version: 2, boundaries: []int{1, 2}},
		{version: 3, boundaries: []int{1, 2}},
		{version: 4, boundaries: []int{1, 2, 3, 4, 5, 6}},
		{version: 5, boundaries: []int{1, 2, 3}},
		{version: 6, boundaries: []int{1}},
	}

	for _, test := range tests {
		migration := migrations[test.version-1]
		for _, boundary := range test.boundaries {
			t.Run(fmt.Sprintf("%03d SQL boundary %d", test.version, boundary), func(t *testing.T) {
				db := openMySQLIntegrationSchema(t)
				applyMySQLMigrations(t, db, migrations[:test.version-1])

				killed := false
				runner := Runner{DB: db, afterStatement: func(ctx context.Context, conn *sql.Conn, got Migration, statement int) error {
					if killed || got.Version != test.version || statement != boundary {
						return nil
					}
					killed = true
					return killMySQLConnection(ctx, db, conn)
				}}
				if _, err := runner.Apply(t.Context(), migrations[:test.version]); err == nil {
					t.Fatalf("injected migration %03d failure unexpectedly succeeded", test.version)
				}
				if got := migrationLedgerCount(t, db); got != test.version-1 {
					t.Fatalf("ledger rows after failed %03d = %d, want %d", test.version, got, test.version-1)
				}

				applyMySQLMigrations(t, db, migrations[:test.version])
				assertMigrationLedgerRow(t, db, migration)
			})
		}

		t.Run(fmt.Sprintf("%03d ledger boundary", test.version), func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			applyMySQLMigrations(t, db, migrations[:test.version-1])
			installLedgerFailureTrigger(t, db, test.version)

			if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:test.version]); err == nil {
				t.Fatalf("migration %03d ledger failure unexpectedly succeeded", test.version)
			}
			dropLedgerFailureTrigger(t, db)
			if got := migrationLedgerCount(t, db); got != test.version-1 {
				t.Fatalf("ledger rows after failed %03d record = %d, want %d", test.version, got, test.version-1)
			}

			applyMySQLMigrations(t, db, migrations[:test.version])
			assertMigrationLedgerRow(t, db, migration)
		})
	}
}

func TestMySQLMigration007RejectsMissingOrIncompatibleTargetTable(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{name: "missing"},
		{
			name: "incompatible replacement",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), `CREATE TABLE group_join_requests (
  id bigint unsigned NOT NULL AUTO_INCREMENT,
  PRIMARY KEY (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
					t.Fatalf("create incompatible group_join_requests: %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			migrations := repositoryMigrations(t)
			applyMySQLMigrations(t, db, migrations[:6])
			if _, err := db.ExecContext(t.Context(), "DROP TABLE group_join_requests"); err != nil {
				t.Fatalf("drop group_join_requests: %v", err)
			}
			if test.setup != nil {
				test.setup(t, db)
			}

			if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:7]); !errors.Is(err, ErrDrift) {
				t.Fatalf("Apply() error = %v, want ErrDrift", err)
			}
			if got := migrationLedgerCount(t, db); got != 6 {
				t.Fatalf("ledger rows after rejected 007 = %d, want 6", got)
			}
		})
	}
}

func TestMySQLMigration007RejectsUnexpectedPost007CoreDriftBefore008DDL(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	applyMySQLMigrations(t, db, migrations[:7])
	if _, err := db.ExecContext(t.Context(), "DELETE FROM `schema_migrations` WHERE `version` = 7"); err != nil {
		t.Fatalf("remove 007 ledger row: %v", err)
	}
	if _, err := db.ExecContext(t.Context(), "CREATE INDEX `idx_group_join_requests_unexpected` ON `group_join_requests` (`requested_at`)"); err != nil {
		t.Fatalf("create unexpected core index: %v", err)
	}

	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations); !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift", err)
	}
	if got := migrationLedgerCount(t, db); got != 6 {
		t.Fatalf("ledger rows after rejected 007 = %d, want 6", got)
	}
	assertMySQLColumnAbsent(t, db, "group_join_requests", "applicant_nickname")
}

func TestMySQLMigration008RejectsCoreDriftBeforeAnyDDL(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{
			name: "unknown group request column",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), "ALTER TABLE `group_join_requests` ADD COLUMN `unexpected_008_column` int DEFAULT NULL"); err != nil {
					t.Fatalf("add unexpected group request column: %v", err)
				}
			},
		},
		{
			name: "unknown scheduled job index",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), "CREATE INDEX `idx_scheduled_jobs_unexpected` ON `scheduled_jobs` (`enabled`)"); err != nil {
					t.Fatalf("add unexpected scheduled job index: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			migrations := repositoryMigrations(t)
			applyMySQLMigrations(t, db, migrations[:7])
			test.setup(t, db)

			for attempt := 1; attempt <= 2; attempt++ {
				if _, err := (Runner{DB: db, LockTimeout: 5 * time.Second}).Apply(t.Context(), migrations); err == nil {
					t.Fatalf("migration 008 attempt %d unexpectedly accepted core schema drift", attempt)
				}
				if got := migrationLedgerCount(t, db); got != 7 {
					t.Fatalf("ledger rows after rejected 008 attempt %d = %d, want 7", attempt, got)
				}
				assertMySQLColumnAbsent(t, db, "group_join_requests", "applicant_nickname")
				assertMySQLColumnAbsent(t, db, "scheduled_jobs", "name")
			}
		})
	}
}

func TestMySQLPost008CoreSchemaMatchesRecoveryFingerprint(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	applyMySQLMigrations(t, db, repositoryMigrations(t))
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("open schema inspection connection: %v", err)
	}
	defer conn.Close()
	snapshot, err := loadLegacySchema(t.Context(), conn)
	if err != nil {
		t.Fatalf("load post-008 core schema: %v", err)
	}
	if got := legacySchemaChecksum(snapshot); got != post008CoreSchemaChecksum {
		t.Fatalf("post-008 core schema checksum = %s, want %s", got, post008CoreSchemaChecksum)
	}
}

func TestMySQLRawInitViaCLIThenRunnerIsNoOp(t *testing.T) {
	container := os.Getenv("JXH_MYSQL_INTEGRATION_CONTAINER")
	if container == "" {
		t.Skip("JXH_MYSQL_INTEGRATION_CONTAINER is not set")
	}
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	var schema string
	if err := db.QueryRowContext(t.Context(), "SELECT DATABASE()").Scan(&schema); err != nil {
		t.Fatalf("load integration schema name: %v", err)
	}

	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "init", "001_schema.sql"))
	if err != nil {
		t.Fatalf("read raw MySQL init: %v", err)
	}
	config, err := drivermysql.ParseDSN(os.Getenv("JXH_MYSQL_INTEGRATION_DSN"))
	if err != nil {
		t.Fatal("parse MySQL integration DSN for CLI: invalid DSN")
	}
	command := exec.CommandContext(t.Context(), "docker", "exec", "-i", "--env", "MYSQL_PWD",
		container, "mysql", "--user="+config.User, "--binary-mode=1", "--default-character-set=utf8mb4", "--database="+schema)
	command.Env = append(os.Environ(), "MYSQL_PWD="+config.Passwd)
	command.Stdin = bytes.NewReader(initSQL)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("execute raw MySQL init through CLI: %v: %s", err, strings.TrimSpace(string(output)))
	}

	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("open init infrastructure inspection connection: %v", err)
	}
	if err := verifyMigrationInfrastructureTable(t.Context(), conn, "schema_migration_attempts", migrationAttemptLedgerSchema()); err != nil {
		_ = conn.Close()
		t.Fatalf("verify init migration attempt ledger: %v", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("close init infrastructure inspection connection: %v", err)
	}
	if got := migrationAttemptCount(t, db); got != 0 {
		t.Fatalf("migration attempts after raw init = %d, want 0", got)
	}
	if got := migrationLedgerCount(t, db); got != len(migrations) {
		t.Fatalf("migration ledger rows after raw init = %d, want %d", got, len(migrations))
	}

	applied, err := (Runner{DB: db, LockTimeout: 5 * time.Second}).Apply(t.Context(), migrations)
	if err != nil {
		t.Fatalf("rerun migrations after raw init: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("rerun migrations after raw init applied = %d, want 0", len(applied))
	}
	if got := migrationLedgerCount(t, db); got != len(migrations) {
		t.Fatalf("migration ledger rows after Runner rerun = %d, want %d", got, len(migrations))
	}
	if got := migrationAttemptCount(t, db); got != 0 {
		t.Fatalf("migration attempts after Runner rerun = %d, want 0", got)
	}

	conn, err = db.Conn(t.Context())
	if err != nil {
		t.Fatalf("open raw init schema inspection connection: %v", err)
	}
	defer conn.Close()
	snapshot, err := loadLegacySchema(t.Context(), conn)
	if err != nil {
		t.Fatalf("load raw init core schema: %v", err)
	}
	if got := legacySchemaChecksum(snapshot); got != post008CoreSchemaChecksum {
		t.Fatalf("raw init core schema checksum = %s, want %s", got, post008CoreSchemaChecksum)
	}
}

func TestMySQLMigration007RecoversFromUnrecordedRoutineBoundaries(t *testing.T) {
	migrations := repositoryMigrations(t)
	script := migrations[6].SQL
	callStart := strings.Index(script, "CALL `jxh_guard_007`();")
	finalDropStart := strings.Index(script, "DROP PROCEDURE `jxh_guard_007`;")
	if callStart < 0 || finalDropStart < 0 || finalDropStart <= callStart {
		t.Fatal("migration 007 routine boundaries are missing or out of order")
	}
	tests := []struct {
		name   string
		prefix string
	}{
		{name: "procedure created before call", prefix: script[:callStart]},
		{name: "call completed before final drop", prefix: script[:finalDropStart]},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			applyMySQLMigrations(t, db, migrations[:6])
			if _, err := db.ExecContext(t.Context(), test.prefix); err != nil {
				t.Fatalf("execute migration 007 prefix: %v", err)
			}
			if got := migrationLedgerCount(t, db); got != 6 {
				t.Fatalf("ledger rows after unrecorded 007 prefix = %d, want 6", got)
			}

			applyMySQLMigrations(t, db, migrations[:7])
			assertMigrationLedgerRow(t, db, migrations[6])
			assertMySQLRoutineAbsent(t, db, "jxh_guard_007")
		})
	}
}

func TestMySQLRunnerAdoptsKnownZeroLedgerLegacySchemas(t *testing.T) {
	migrations := repositoryMigrations(t)
	tests := []struct {
		name           string
		legacyVersion  int
		wantFirstApply int
		wantApplyCount int
	}{
		{name: "post-005", legacyVersion: 5, wantFirstApply: 6, wantApplyCount: 4},
		{name: "post-007", legacyVersion: 7, wantFirstApply: 8, wantApplyCount: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			applyMySQLMigrations(t, db, migrations[:test.legacyVersion])
			if _, err := db.ExecContext(t.Context(), "DELETE FROM schema_migrations"); err != nil {
				t.Fatalf("clear migration ledger: %v", err)
			}

			applied, err := (Runner{DB: db}).Apply(t.Context(), migrations)
			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
			if len(applied) != test.wantApplyCount || applied[0].Version != test.wantFirstApply {
				t.Fatalf("applied = %+v, want %d migrations starting at %03d", applied, test.wantApplyCount, test.wantFirstApply)
			}
			if got := migrationLedgerCount(t, db); got != len(migrations) {
				t.Fatalf("ledger rows after adoption = %d, want %d", got, len(migrations))
			}
		})
	}
}

func TestMySQLHistoricalMigrationRecoveryRequiresDurableAttempt(t *testing.T) {
	migrations := repositoryMigrations(t)

	t.Run("partial SQL without attempt", func(t *testing.T) {
		db := openMySQLIntegrationSchema(t)
		applyMySQLMigrations(t, db, migrations[:1])
		if _, err := db.ExecContext(t.Context(), `ALTER TABLE scheduled_jobs
ADD COLUMN run_date date DEFAULT NULL AFTER time_hhmm`); err != nil {
			t.Fatalf("create untracked migration 002 partial state: %v", err)
		}
		if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:2]); !errors.Is(err, ErrDrift) {
			t.Fatalf("Apply() error = %v, want ErrDrift", err)
		}
		if got := migrationLedgerCount(t, db); got != 1 {
			t.Fatalf("ledger rows after untracked partial state = %d, want 1", got)
		}
	})

	t.Run("completed SQL without attempt", func(t *testing.T) {
		db := openMySQLIntegrationSchema(t)
		applyMySQLMigrations(t, db, migrations[:4])
		if _, err := db.ExecContext(t.Context(), "DELETE FROM schema_migrations WHERE version = 4"); err != nil {
			t.Fatalf("remove migration 004 ledger evidence: %v", err)
		}
		if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:4]); !errors.Is(err, ErrDrift) {
			t.Fatalf("Apply() error = %v, want ErrDrift", err)
		}
		if got := migrationLedgerCount(t, db); got != 3 {
			t.Fatalf("ledger rows after untracked complete state = %d, want 3", got)
		}
	})
}

func TestMySQLHistoricalMigrationRecoveryRejectsImpossibleState(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	migrations := repositoryMigrations(t)
	applyMySQLMigrations(t, db, migrations[:4])
	installLedgerFailureTrigger(t, db, 5)
	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:5]); err == nil {
		t.Fatal("migration 005 ledger failure unexpectedly succeeded")
	}
	dropLedgerFailureTrigger(t, db)

	if _, err := db.ExecContext(t.Context(), "ALTER TABLE `group_join_requests` DROP COLUMN `major`"); err != nil {
		t.Fatalf("create impossible migration 005 state: %v", err)
	}
	if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:5]); !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift", err)
	}
	if got := migrationLedgerCount(t, db); got != 4 {
		t.Fatalf("ledger rows after impossible state = %d, want 4", got)
	}
}

func TestMySQLMigrationAttemptsFailClosed(t *testing.T) {
	migrations := repositoryMigrations(t)
	tests := []struct {
		name  string
		setup func(*testing.T, *sql.DB)
	}{
		{
			name: "stage mismatch",
			setup: func(t *testing.T, db *sql.DB) {
				insertMigrationAttemptFixture(t, db, migrations[1], 99)
			},
		},
		{
			name: "stage impossible for schema",
			setup: func(t *testing.T, db *sql.DB) {
				insertMigrationAttemptFixture(t, db, migrations[1], 1)
			},
		},
		{
			name: "identity mismatch",
			setup: func(t *testing.T, db *sql.DB) {
				if _, err := db.ExecContext(t.Context(), `INSERT INTO schema_migration_attempts
  (version, name, checksum, stage) VALUES (2, '002_wrong', ?, 0)`, migrations[1].Checksum); err != nil {
					t.Fatalf("insert mismatched attempt: %v", err)
				}
			},
		},
		{
			name: "multiple attempts",
			setup: func(t *testing.T, db *sql.DB) {
				insertMigrationAttemptFixture(t, db, migrations[1], 0)
				insertMigrationAttemptFixture(t, db, migrations[2], 0)
			},
		},
		{
			name: "attempt for recorded migration",
			setup: func(t *testing.T, db *sql.DB) {
				insertMigrationAttemptFixture(t, db, migrations[0], 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := openMySQLIntegrationSchema(t)
			applyMySQLMigrations(t, db, migrations[:1])
			test.setup(t, db)
			if _, err := (Runner{DB: db}).Apply(t.Context(), migrations[:3]); !errors.Is(err, ErrDrift) {
				t.Fatalf("Apply() error = %v, want ErrDrift", err)
			}
		})
	}

	t.Run("active attempt prevents legacy adoption", func(t *testing.T) {
		db := openMySQLIntegrationSchema(t)
		applyMySQLMigrations(t, db, migrations[:5])
		insertMigrationAttemptFixture(t, db, migrations[0], 1)
		if _, err := db.ExecContext(t.Context(), "DELETE FROM schema_migrations"); err != nil {
			t.Fatalf("clear ledger with active attempt: %v", err)
		}
		if _, err := (Runner{DB: db}).Apply(t.Context(), migrations); !errors.Is(err, ErrDrift) {
			t.Fatalf("Apply() error = %v, want ErrDrift", err)
		}
		if got := migrationLedgerCount(t, db); got != 0 {
			t.Fatalf("legacy adoption recorded %d rows with an active attempt", got)
		}
	})
}

func insertMigrationAttemptFixture(t *testing.T, db *sql.DB, migration Migration, stage int) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), `INSERT INTO schema_migration_attempts
  (version, name, checksum, stage) VALUES (?, ?, ?, ?)`,
		migration.Version, migration.Name, migration.Checksum, stage); err != nil {
		t.Fatalf("insert migration %03d attempt: %v", migration.Version, err)
	}
}

func installLedgerFailureTrigger(t *testing.T, db *sql.DB, version int) {
	t.Helper()
	statement := fmt.Sprintf(`CREATE TRIGGER jxh_fail_migration_ledger
BEFORE INSERT ON schema_migrations
FOR EACH ROW
BEGIN
  IF NEW.version = %d THEN
    SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced migration ledger failure';
  END IF;
END`, version)
	if _, err := db.ExecContext(t.Context(), statement); err != nil {
		t.Fatalf("install migration %03d ledger failure trigger: %v", version, err)
	}
}

func dropLedgerFailureTrigger(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), "DROP TRIGGER `jxh_fail_migration_ledger`"); err != nil {
		t.Fatalf("drop migration ledger failure trigger: %v", err)
	}
}

func assertMigrationLedgerRow(t *testing.T, db *sql.DB, migration Migration) {
	t.Helper()
	var name, checksum string
	if err := db.QueryRowContext(t.Context(),
		"SELECT `name`, `checksum` FROM `schema_migrations` WHERE `version` = ?", migration.Version,
	).Scan(&name, &checksum); err != nil {
		t.Fatalf("read migration %03d ledger row: %v", migration.Version, err)
	}
	if name != migration.Name || checksum != migration.Checksum {
		t.Fatalf("migration %03d ledger = (%q, %q), want (%q, %q)",
			migration.Version, name, checksum, migration.Name, migration.Checksum)
	}
}

func killMySQLConnection(ctx context.Context, db *sql.DB, _ *sql.Conn) error {
	var connectionID int64
	if err := db.QueryRowContext(ctx, "SELECT IS_USED_LOCK(?)", migrationLockName).Scan(&connectionID); err != nil {
		return fmt.Errorf("read migration connection id: %w", err)
	}
	if _, err := db.ExecContext(ctx, fmt.Sprintf("KILL CONNECTION %d", connectionID)); err != nil {
		return fmt.Errorf("kill migration connection: %w", err)
	}
	return nil
}

func TestMySQLSessionChainsEnumsAndRetentionConstraints(t *testing.T) {
	db := openMySQLIntegrationSchema(t)
	applyMySQLMigrations(t, db, repositoryMigrations(t))
	ctx := t.Context()

	if _, err := db.ExecContext(ctx, `INSERT INTO admin_users
  (user_id, username, display_name, password_hash, role)
VALUES ('user-1', 'root-admin', 'Root', 'argon2id-placeholder', 'super_admin'),
       ('user-2', 'maintainer', 'Maintainer', 'argon2id-placeholder', 'maintainer')`); err != nil {
		t.Fatalf("insert admin users: %v", err)
	}
	insertSessionForUser := func(id, userID, tokenByte, csrfByte, status string, depth int, absoluteExpired bool, targetID any, targetUser any, targetDepth any) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO admin_sessions
  (session_id, user_id, token_digest, csrf_digest, status, expires_at, absolute_expires_at,
   revoked_at, replacement_depth, replaced_by_session_id, replaced_by_user_id, replaced_by_depth)
VALUES (?, ?, ?, ?, ?, DATE_ADD(NOW(3), INTERVAL 1 HOUR),
        IF(?, DATE_SUB(NOW(3), INTERVAL 1 HOUR), DATE_ADD(NOW(3), INTERVAL 24 HOUR)),
        IF(? = 'revoked', NOW(3), NULL), ?, ?, ?, ?)`,
			id, userID, strings.Repeat(tokenByte, 64), strings.Repeat(csrfByte, 64), status,
			absoluteExpired, status, depth, targetID, targetUser, targetDepth)
		if err != nil {
			t.Fatalf("insert session %s: %v", id, err)
		}
	}
	insertSession := func(id, tokenByte, csrfByte, status string, depth int, absoluteExpired bool, targetID any, targetUser any, targetDepth any) {
		t.Helper()
		insertSessionForUser(id, "user-1", tokenByte, csrfByte, status, depth, absoluteExpired, targetID, targetUser, targetDepth)
	}

	insertSession("session-f", "h", "H", "active", 5, false, nil, nil, nil)
	insertSession("session-e", "g", "G", "active", 4, false, nil, nil, nil)
	insertSession("session-d", "f", "F", "active", 3, false, nil, nil, nil)
	insertSession("session-c", "c", "C", "active", 2, true, nil, nil, nil)
	insertSession("session-b", "b", "B", "active", 1, false, nil, nil, nil)
	insertSession("session-a", "a", "A", "revoked", 0, false, "session-b", "user-1", 1)
	for _, edge := range []struct {
		source, target string
		depth          int
	}{
		{source: "session-b", target: "session-c", depth: 2},
		{source: "session-c", target: "session-d", depth: 3},
		{source: "session-d", target: "session-e", depth: 4},
		{source: "session-e", target: "session-f", depth: 5},
	} {
		if _, err := db.ExecContext(ctx, `UPDATE admin_sessions
SET status = 'revoked', revoked_at = NOW(3),
	    replaced_by_session_id = ?, replaced_by_user_id = 'user-1', replaced_by_depth = ?
WHERE session_id = ?`, edge.target, edge.depth, edge.source); err != nil {
			t.Fatalf("extend six-session replacement chain %s -> %s: %v", edge.source, edge.target, err)
		}
	}
	if _, err := db.ExecContext(ctx, `UPDATE admin_sessions
SET status = 'revoked', revoked_at = NOW(3),
    replaced_by_session_id = 'session-a', replaced_by_user_id = 'user-1', replaced_by_depth = 0
WHERE session_id = 'session-f'`); err == nil {
		t.Fatal("replacement depth constraint accepted a six-session cycle")
	}

	insertSession("session-validation-source", "i", "I", "active", 10, false, nil, nil, nil)
	insertSession("session-active-source", "l", "L", "active", 10, false, nil, nil, nil)
	insertSession("session-validation-target", "j", "J", "active", 100, false, nil, nil, nil)
	insertSession("session-equal-depth-target", "m", "M", "active", 10, false, nil, nil, nil)
	insertSessionForUser("session-other-user", "user-2", "k", "K", "active", 100, false, nil, nil, nil)
	if _, err := db.ExecContext(ctx, `UPDATE admin_sessions
SET status = 'revoked', revoked_at = NOW(3)
WHERE session_id = 'session-validation-source'`); err != nil {
		t.Fatalf("revoke replacement validation source: %v", err)
	}
	for _, invalid := range []struct {
		name string
		stmt string
	}{
		{
			name: "incomplete replacement snapshot",
			stmt: `UPDATE admin_sessions
SET replaced_by_session_id = 'session-validation-target', replaced_by_user_id = 'user-1'
WHERE session_id = 'session-validation-source'`,
		},
		{
			name: "active replacement source",
			stmt: `UPDATE admin_sessions
SET replaced_by_session_id = 'session-validation-target', replaced_by_user_id = 'user-1', replaced_by_depth = 100
WHERE session_id = 'session-active-source'`,
		},
		{
			name: "cross-user replacement",
			stmt: `UPDATE admin_sessions
SET replaced_by_session_id = 'session-other-user', replaced_by_user_id = 'user-2', replaced_by_depth = 100
WHERE session_id = 'session-validation-source'`,
		},
		{
			name: "non-increasing replacement depth",
			stmt: `UPDATE admin_sessions
SET replaced_by_session_id = 'session-b', replaced_by_user_id = 'user-1', replaced_by_depth = 1
WHERE session_id = 'session-validation-source'`,
		},
		{
			name: "equal replacement depth",
			stmt: `UPDATE admin_sessions
SET replaced_by_session_id = 'session-equal-depth-target', replaced_by_user_id = 'user-1', replaced_by_depth = 10
WHERE session_id = 'session-validation-source'`,
		},
		{
			name: "replacement depth mutation",
			stmt: `UPDATE admin_sessions
SET replacement_depth = replacement_depth + 1
WHERE session_id = 'session-validation-source'`,
		},
	} {
		if _, err := db.ExecContext(ctx, invalid.stmt); err == nil {
			t.Fatalf("session trigger accepted %s", invalid.name)
		}
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE absolute_expires_at < NOW(3)"); err != nil {
		t.Fatalf("delete expired replacement target: %v", err)
	}
	var replacementID, replacementUser sql.NullString
	var replacementDepth sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT replaced_by_session_id, replaced_by_user_id, replaced_by_depth
FROM admin_sessions WHERE session_id = 'session-b'`).Scan(&replacementID, &replacementUser, &replacementDepth); err != nil {
		t.Fatalf("read detached replacement: %v", err)
	}
	if replacementID.Valid || replacementUser.Valid || replacementDepth.Valid {
		t.Fatalf("deleted replacement target left snapshots: id=%v user=%v depth=%v", replacementID, replacementUser, replacementDepth)
	}

	insertSession("session-result", "d", "D", "active", 0, true, nil, nil, nil)
	result, err := db.ExecContext(ctx, `INSERT INTO admin_idempotency_keys
  (actor_type, actor_id, operation, idempotency_key, request_hash, state, result_status,
   response_status, resulting_session_id, created_at, completed_at, expires_at)
VALUES ('admin_user', 'user-1', 'auth.change_password', 'idem-valid', ?, 'completed', 'succeeded',
		200, 'session-result', NOW(3), NOW(3), DATE_SUB(NOW(3), INTERVAL 1 HOUR))`, strings.Repeat("e", 64))
	if err != nil {
		t.Fatalf("insert valid idempotency row: %v", err)
	}
	idempotencyID, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("read idempotency id: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO admin_idempotency_keys
  (actor_type, actor_id, operation, idempotency_key, request_hash, state, result_status,
   created_at, expires_at)
VALUES ('admin_user', 'user-1', 'auth.change_password', 'idem-unexpired', ?, 'in_progress', NULL,
        NOW(3), DATE_ADD(NOW(3), INTERVAL 1 HOUR))`, strings.Repeat("g", 64)); err != nil {
		t.Fatalf("insert unexpired idempotency row: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO system_operations
  (operation_id, type, status, requested_by_type, requested_by, idempotency_id, request_id, requested_at, completed_at)
VALUES ('operation-old', 'napcat_restart', 'succeeded', 'admin_user', 'user-1', ?, 'request-old', DATE_SUB(NOW(3), INTERVAL 2 HOUR), DATE_SUB(NOW(3), INTERVAL 1 HOUR)),
       ('operation-recent', 'napcat_restart', 'succeeded', 'admin_user', 'user-1', NULL, 'request-recent', NOW(3), NOW(3))`, idempotencyID); err != nil {
		t.Fatalf("insert system operations: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE absolute_expires_at < NOW(3)"); err != nil {
		t.Fatalf("delete result session: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT resulting_session_id FROM admin_idempotency_keys WHERE idempotency_id = ?", idempotencyID).Scan(&replacementID); err != nil {
		t.Fatalf("read detached idempotency session: %v", err)
	}
	if replacementID.Valid {
		t.Fatalf("idempotency row still references deleted session %q", replacementID.String)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM admin_idempotency_keys WHERE expires_at < NOW(3)"); err != nil {
		t.Fatalf("delete expired idempotency key: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT idempotency_id FROM system_operations WHERE operation_id = 'operation-old'").Scan(&replacementDepth); err != nil {
		t.Fatalf("read operation after idempotency cleanup: %v", err)
	}
	if replacementDepth.Valid {
		t.Fatalf("system operation still references deleted idempotency row %d", replacementDepth.Int64)
	}
	var unexpiredIdempotencyCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*)
FROM admin_idempotency_keys
WHERE actor_type = 'admin_user'
  AND actor_id = 'user-1'
  AND operation = 'auth.change_password'
  AND idempotency_key = 'idem-unexpired'`).Scan(&unexpiredIdempotencyCount); err != nil {
		t.Fatalf("count unexpired idempotency row: %v", err)
	}
	if unexpiredIdempotencyCount != 1 {
		t.Fatalf("idempotency cleanup retained %d unexpired rows, want 1", unexpiredIdempotencyCount)
	}

	type invalidIdempotency struct {
		name, key, state, resultStatus string
		completed                      bool
		expectedConstraint             string
	}
	assertIdempotencyConstraint := func(invalid invalidIdempotency) {
		t.Helper()
		_, err := db.ExecContext(ctx, `INSERT INTO admin_idempotency_keys
  (actor_type, actor_id, operation, idempotency_key, request_hash, state, result_status, completed_at, expires_at)
VALUES ('admin_user', 'user-1', 'test.invalid', ?, ?, ?, NULLIF(?, ''),
        IF(?, DATE_SUB(NOW(3), INTERVAL 1 MINUTE), NULL), DATE_ADD(NOW(3), INTERVAL 1 HOUR))`,
			invalid.key, strings.Repeat("f", 64), invalid.state, invalid.resultStatus, invalid.completed)
		if err == nil {
			t.Fatalf("invalid idempotency %s was accepted: state=%q result=%q; want constraint %s",
				invalid.name, invalid.state, invalid.resultStatus, invalid.expectedConstraint)
		}
		var mysqlErr *drivermysql.MySQLError
		if !errors.As(err, &mysqlErr) {
			t.Fatalf("invalid idempotency %s returned %T, want *mysql.MySQLError for constraint %s",
				invalid.name, err, invalid.expectedConstraint)
		}
		if !strings.Contains(mysqlErr.Message, invalid.expectedConstraint) {
			t.Fatalf("invalid idempotency %s violated MySQL constraint %q, want %q (error %d)",
				invalid.name, mysqlErr.Message, invalid.expectedConstraint, mysqlErr.Number)
		}
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE admin_idempotency_keys
ALTER CHECK chk_admin_idempotency_completion NOT ENFORCED`); err != nil {
		t.Fatalf("disable idempotency completion check while isolating state enum: %v", err)
	}
	for _, invalid := range []invalidIdempotency{
		{name: "unknown state", key: "unknown-state", state: "unknown", resultStatus: "", completed: false, expectedConstraint: "chk_admin_idempotency_state"},
		{name: "state case variant", key: "state-case", state: "Completed", resultStatus: "succeeded", completed: true, expectedConstraint: "chk_admin_idempotency_state"},
	} {
		assertIdempotencyConstraint(invalid)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE admin_idempotency_keys
ALTER CHECK chk_admin_idempotency_completion ENFORCED`); err != nil {
		t.Fatalf("restore idempotency completion check after isolating state enum: %v", err)
	}
	for _, invalid := range []invalidIdempotency{
		{name: "unknown result status", key: "unknown-result", state: "completed", resultStatus: "not-a-result", completed: true, expectedConstraint: "chk_admin_idempotency_result_status"},
		{name: "result status case variant", key: "result-case", state: "completed", resultStatus: "Succeeded", completed: true, expectedConstraint: "chk_admin_idempotency_result_status"},
	} {
		assertIdempotencyConstraint(invalid)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM system_operations WHERE completed_at < DATE_SUB(NOW(3), INTERVAL 30 MINUTE)"); err != nil {
		t.Fatalf("delete retained system operation: %v", err)
	}
	var operationCount, userCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM system_operations").Scan(&operationCount); err != nil {
		t.Fatalf("count retained operations: %v", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users WHERE user_id = 'user-1'").Scan(&userCount); err != nil {
		t.Fatalf("count retained user: %v", err)
	}
	if operationCount != 1 || userCount != 1 {
		t.Fatalf("retention removed durable records: operations=%d users=%d", operationCount, userCount)
	}
}

func repositoryMigrations(t *testing.T) []Migration {
	t.Helper()
	migrations, err := LoadMigrations(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations"))
	if err != nil {
		t.Fatalf("load repository migrations: %v", err)
	}
	return migrations
}

func openMySQLIntegrationSchema(t *testing.T) *sql.DB {
	t.Helper()
	rawDSN := os.Getenv("JXH_MYSQL_INTEGRATION_DSN")
	if rawDSN == "" {
		t.Skip("JXH_MYSQL_INTEGRATION_DSN is not set")
	}
	parsed, err := drivermysql.ParseDSN(rawDSN)
	if err != nil {
		t.Fatal("parse MySQL integration DSN: invalid DSN")
	}
	adminConfig := parsed.Clone()
	adminConfig.DBName = ""
	adminConfig.MultiStatements = true
	adminDB, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatalf("open MySQL integration admin connection: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })

	schema := fmt.Sprintf("jxh_migration_test_%d_%d", time.Now().UnixNano(), mysqlIntegrationSchemaID.Add(1))
	if _, err := adminDB.ExecContext(t.Context(), "CREATE DATABASE `"+schema+"`"); err != nil {
		t.Fatalf("create MySQL integration schema: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(ctx, "DROP DATABASE `"+schema+"`"); err != nil {
			t.Errorf("drop MySQL integration schema: %v", err)
		}
		var count int
		if err := adminDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", schema).Scan(&count); err != nil {
			t.Errorf("verify MySQL integration schema cleanup: %v", err)
		} else if count != 0 {
			t.Errorf("MySQL integration schema %s still exists", schema)
		}
	})

	databaseConfig := parsed.Clone()
	databaseConfig.DBName = schema
	databaseConfig.MultiStatements = true
	db, err := sql.Open("mysql", databaseConfig.FormatDSN())
	if err != nil {
		t.Fatalf("open MySQL integration schema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("ping MySQL integration schema: %v", err)
	}
	return db
}

func applyMySQLMigrations(t *testing.T, db *sql.DB, migrations []Migration) {
	t.Helper()
	if _, err := (Runner{DB: db, LockTimeout: 5 * time.Second}).Apply(t.Context(), migrations); err != nil {
		t.Fatalf("apply MySQL migrations: %v", err)
	}
}

func migrationLedgerCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count migration ledger rows: %v", err)
	}
	return count
}

func migrationAttemptCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM schema_migration_attempts").Scan(&count); err != nil {
		t.Fatalf("count migration attempts: %v", err)
	}
	return count
}

func assertMySQLTableAbsent(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*)
FROM information_schema.tables
WHERE table_schema = DATABASE() AND BINARY table_name = BINARY ?`, table).Scan(&count); err != nil {
		t.Fatalf("inspect table %s: %v", table, err)
	}
	if count != 0 {
		t.Fatalf("table %s exists, want absent", table)
	}
}

func assertMySQLColumnPresent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND BINARY table_name = BINARY ?
  AND BINARY column_name = BINARY ?`, table, column).Scan(&count); err != nil {
		t.Fatalf("inspect column %s.%s: %v", table, column, err)
	}
	if count != 1 {
		t.Fatalf("column %s.%s count = %d, want 1", table, column, count)
	}
}

func assertMySQLColumnAbsent(t *testing.T, db *sql.DB, table, column string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*)
FROM information_schema.columns
WHERE table_schema = DATABASE()
  AND BINARY table_name = BINARY ?
  AND BINARY column_name = BINARY ?`, table, column).Scan(&count); err != nil {
		t.Fatalf("inspect column %s.%s: %v", table, column, err)
	}
	if count != 0 {
		t.Fatalf("column %s.%s count = %d, want 0", table, column, count)
	}
}

func assertMySQLRoutineAbsent(t *testing.T, db *sql.DB, routine string) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*)
FROM information_schema.routines
WHERE routine_schema = DATABASE() AND BINARY routine_name = BINARY ?`, routine).Scan(&count); err != nil {
		t.Fatalf("inspect routine %s: %v", routine, err)
	}
	if count != 0 {
		t.Fatalf("routine %s exists, want absent", routine)
	}
}
