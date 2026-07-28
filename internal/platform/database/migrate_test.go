package database

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/platform/config"
)

func TestLoadMigrationsSortsAndChecksums(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "002_second.sql", "SELECT 2;\n")
	writeMigration(t, dir, "001_first.sql", "SELECT 1;\n")

	migrations, err := LoadMigrations(dir)
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	if len(migrations) != 2 {
		t.Fatalf("len(migrations) = %d, want 2", len(migrations))
	}
	if migrations[0].Version != 1 || migrations[0].Name != "001_first" || migrations[0].SQL != "SELECT 1;\n" {
		t.Fatalf("first migration = %+v", migrations[0])
	}
	wantChecksum := sha256.Sum256([]byte("SELECT 1;\n"))
	if migrations[0].Checksum != hex.EncodeToString(wantChecksum[:]) {
		t.Fatalf("checksum = %q, want %x", migrations[0].Checksum, wantChecksum)
	}
}

func TestLoadMigrationsRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  error
	}{
		{
			name:  "invalid sql filename",
			files: map[string]string{"001_ok.sql": "SELECT 1;", "notes.sql": "SELECT 2;"},
			want:  ErrManifest,
		},
		{
			name:  "duplicate version",
			files: map[string]string{"001_first.sql": "SELECT 1;", "001_second.sql": "SELECT 2;"},
			want:  ErrSequence,
		},
		{
			name:  "does not start at one",
			files: map[string]string{"002_second.sql": "SELECT 2;"},
			want:  ErrSequence,
		},
		{
			name:  "gap",
			files: map[string]string{"001_first.sql": "SELECT 1;", "003_third.sql": "SELECT 3;"},
			want:  ErrSequence,
		},
		{
			name: "empty and comments only",
			files: map[string]string{
				"001_first.sql": "SELECT 1;",
				"002_empty.sql": " -- comment\n# another\n/* block */\n",
			},
			want: ErrManifest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, contents := range tt.files {
				writeMigration(t, dir, name, contents)
			}

			_, err := LoadMigrations(dir)
			if !errors.Is(err, tt.want) {
				t.Fatalf("LoadMigrations() error = %v, want errors.Is(..., %v)", err, tt.want)
			}
		})
	}
}

func TestLoadMigrationsRejectsUnexpectedEntryTypes(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "non-SQL regular file",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "README.txt"), []byte("not a migration\n"), 0o600); err != nil {
					t.Fatalf("write unexpected regular file: %v", err)
				}
			},
		},
		{
			name: "directory",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(dir, "archive"), 0o700); err != nil {
					t.Fatalf("create unexpected directory: %v", err)
				}
			},
		},
		{
			name: "symbolic link",
			setup: func(t *testing.T, dir string) {
				t.Helper()
				if err := os.Symlink(filepath.Join(dir, "001_first.sql"), filepath.Join(dir, "002_link.sql")); err != nil {
					t.Fatalf("create migration symlink: %v", err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMigration(t, dir, "001_first.sql", "SELECT 1;\n")
			test.setup(t, dir)

			_, err := LoadMigrations(dir)
			if !errors.Is(err, ErrManifest) {
				t.Fatalf("LoadMigrations() error = %v, want ErrManifest", err)
			}
		})
	}
}

func TestLoadMigrationsRejectsSymlinkDirectory(t *testing.T) {
	parent := t.TempDir()
	realDir := filepath.Join(parent, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatalf("create real migration directory: %v", err)
	}
	writeMigration(t, realDir, "001_first.sql", "SELECT 1;\n")
	linkDir := filepath.Join(parent, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatalf("create migration directory symlink: %v", err)
	}

	if _, err := LoadMigrations(linkDir); !errors.Is(err, ErrManifest) {
		t.Fatalf("LoadMigrations() error = %v, want ErrManifest", err)
	}
}

func TestLoadMigrationsRejectsDirectoryReplacementAfterOpen(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "migrations")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("create migration directory: %v", err)
	}
	writeMigration(t, dir, "001_first.sql", "SELECT 1;\n")
	moved := filepath.Join(parent, "original")
	replacementPrevented := false

	_, err := loadMigrationsWithHooks(dir, migrationLoadHooks{afterDirectoryOpen: func() {
		if err := os.Rename(dir, moved); err != nil {
			if runtime.GOOS == "windows" {
				replacementPrevented = true
				return
			}
			t.Fatalf("move opened migration directory: %v", err)
		}
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatalf("create replacement migration directory: %v", err)
		}
		writeMigration(t, dir, "001_first.sql", "SELECT malicious;\n")
	}})
	if replacementPrevented {
		if err != nil {
			t.Fatalf("load after OS prevented directory replacement: %v", err)
		}
		return
	}
	if !errors.Is(err, ErrManifest) {
		t.Fatalf("loadMigrationsWithHooks() error = %v, want ErrManifest", err)
	}
}

func TestLoadMigrationsRejectsFileReplacementAcrossIdentityChecks(t *testing.T) {
	for _, test := range []struct {
		name  string
		hooks func(func(string)) migrationLoadHooks
	}{
		{
			name: "after lstat",
			hooks: func(replace func(string)) migrationLoadHooks {
				return migrationLoadHooks{afterFileLstat: replace}
			},
		},
		{
			name: "after open",
			hooks: func(replace func(string)) migrationLoadHooks {
				return migrationLoadHooks{afterFileOpen: replace}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "001_first.sql")
			writeMigration(t, dir, "001_first.sql", "SELECT 1;\n")
			replaced := false
			replace := func(name string) {
				if replaced || name != "001_first.sql" {
					return
				}
				replaced = true
				if err := os.Rename(path, filepath.Join(dir, "saved.sql")); err != nil {
					t.Fatalf("move inspected migration file: %v", err)
				}
				if err := os.WriteFile(path, []byte("SELECT malicious;\n"), 0o600); err != nil {
					t.Fatalf("write replacement migration file: %v", err)
				}
			}

			_, err := loadMigrationsWithHooks(dir, test.hooks(replace))
			if !errors.Is(err, ErrManifest) {
				t.Fatalf("loadMigrationsWithHooks() error = %v, want ErrManifest", err)
			}
		})
	}
}

func TestLoadMigrationsRejectsEmptyDirectoryAndSemicolonOnlyScript(t *testing.T) {
	for _, setup := range []func(*testing.T, string){
		func(*testing.T, string) {},
		func(t *testing.T, dir string) {
			writeMigration(t, dir, "001_empty.sql", "-- comment\n; /* block */ ;\n")
		},
	} {
		dir := t.TempDir()
		setup(t, dir)
		_, err := LoadMigrations(dir)
		if !errors.Is(err, ErrManifest) {
			t.Fatalf("LoadMigrations() error = %v, want ErrManifest", err)
		}
	}
}

func TestSplitMigrationStatementsPreservesHistoricalSQL(t *testing.T) {
	migrations, err := LoadMigrations(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations"))
	if err != nil {
		t.Fatalf("LoadMigrations() error = %v", err)
	}
	wantCounts := []int{3, 2, 2, 6, 3, 1}
	for i, want := range wantCounts {
		statements, err := splitMigrationStatements(migrations[i].SQL)
		if err != nil {
			t.Fatalf("split migration %03d: %v", i+1, err)
		}
		if len(statements) != want {
			t.Fatalf("migration %03d statement count = %d, want %d", i+1, len(statements), want)
		}
		if strings.Join(statements, "") != migrations[i].SQL {
			t.Fatalf("migration %03d split did not preserve source bytes", i+1)
		}
	}
}

func TestBuildMigrationDSNEnablesMultiStatements(t *testing.T) {
	source := drivermysql.NewConfig()
	source.User = "user"
	source.Passwd = "p@ss"
	source.Net = "tcp"
	source.Addr = "db.example:3307"
	source.DBName = "jxh"
	source.ParseTime = true
	source.Timeout = 3 * time.Second
	source.Params = map[string]string{"application": "manager"}
	cfg := config.DatabaseConfig{
		DSN: source.FormatDSN(),
	}

	dsn, err := BuildMigrationDSN(cfg)
	if err != nil {
		t.Fatalf("BuildMigrationDSN() error = %v", err)
	}
	parsed, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(result) error = %v", err)
	}
	if !parsed.MultiStatements {
		t.Fatal("MultiStatements = false, want true")
	}
	if parsed.User != "user" || parsed.Passwd != "p@ss" || parsed.Addr != "db.example:3307" || parsed.DBName != "jxh" {
		t.Fatalf("connection fields changed: %+v", parsed)
	}
	if parsed.Params["application"] != "manager" || parsed.Timeout.String() != "3s" {
		t.Fatalf("DSN parameters changed: %+v", parsed)
	}
}

func TestBuildMigrationDSNBuildsStructuredConfig(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host:      "127.0.0.1",
		Port:      3307,
		User:      "jxh",
		Password:  "secret",
		Name:      "jxh_bot",
		Charset:   "utf8mb4",
		ParseTime: true,
		Loc:       "Asia/Shanghai",
	}

	dsn, err := BuildMigrationDSN(cfg)
	if err != nil {
		t.Fatalf("BuildMigrationDSN() error = %v", err)
	}
	parsed, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("ParseDSN(result) error = %v", err)
	}
	if !parsed.MultiStatements || parsed.Net != "tcp" || parsed.Addr != "127.0.0.1:3307" || parsed.DBName != "jxh_bot" {
		t.Fatalf("parsed DSN = %+v", parsed)
	}
	if parsed.Loc.String() != "Asia/Shanghai" || !parsed.ParseTime {
		t.Fatalf("time options changed: %+v", parsed)
	}
	if parsed.Timeout != 5*time.Second {
		t.Fatalf("connection timeout = %v, want 5s", parsed.Timeout)
	}
}

func TestBuildMigrationDSNDoesNotLeakInvalidDSN(t *testing.T) {
	secretDSN := "user:hunter2@tcp(localhost:3306)/jxh?timeout=invalid"
	_, err := BuildMigrationDSN(config.DatabaseConfig{DSN: secretDSN})
	if err == nil {
		t.Fatal("BuildMigrationDSN() error = nil, want invalid DSN")
	}
	if err.Error() != "parse database configuration: invalid DSN" {
		t.Fatalf("BuildMigrationDSN() error = %q, want safe summary", err)
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), secretDSN) {
		t.Fatalf("BuildMigrationDSN() leaked DSN: %v", err)
	}
}

func TestBuildMigrationDSNDoesNotLeakConfigOnInvalidLocation(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host: "localhost", Port: 3306, User: "jxh", Password: "hunter2", Name: "jxh",
		Charset: "utf8mb4", Loc: "Invalid/Zone",
	}
	_, err := BuildMigrationDSN(cfg)
	if err == nil {
		t.Fatal("BuildMigrationDSN() error = nil, want invalid location")
	}
	if err.Error() != "load database location: invalid location" {
		t.Fatalf("BuildMigrationDSN() error = %q, want safe summary", err)
	}
	if strings.Contains(err.Error(), cfg.Password) {
		t.Fatalf("BuildMigrationDSN() leaked password: %v", err)
	}
}

func TestBuildMigrationDSNRejectsUnsafeCharset(t *testing.T) {
	cfg := config.DatabaseConfig{
		Host: "localhost", Port: 3306, User: "jxh", Password: "hunter2", Name: "jxh",
		Charset: "utf8mb4; SELECT secret", Loc: "UTC",
	}
	_, err := BuildMigrationDSN(cfg)
	if err == nil || err.Error() != "apply database charset: invalid charset" {
		t.Fatalf("BuildMigrationDSN() error = %v, want safe invalid charset error", err)
	}
	if strings.Contains(err.Error(), "SELECT secret") || strings.Contains(err.Error(), cfg.Password) {
		t.Fatalf("BuildMigrationDSN() leaked unsafe config: %v", err)
	}
}

func TestBuildMigrationDSNRejectsUnsafeCharsetInExplicitDSN(t *testing.T) {
	dsn := "user:hunter2@tcp(localhost:3306)/jxh?charset=utf8mb4%3BSELECT+secret"
	_, err := BuildMigrationDSN(config.DatabaseConfig{DSN: dsn})
	if err == nil || err.Error() != "apply database charset: invalid charset" {
		t.Fatalf("BuildMigrationDSN() error = %v, want safe invalid charset error", err)
	}
	if strings.Contains(err.Error(), "SELECT secret") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("BuildMigrationDSN() leaked unsafe DSN: %v", err)
	}
}

func TestBuildMigrationDSNRejectsUnsafeCollationInExplicitDSN(t *testing.T) {
	dsn := "user:hunter2@tcp(localhost:3306)/jxh?collation=utf8mb4_bin%3BSELECT+secret"
	_, err := BuildMigrationDSN(config.DatabaseConfig{DSN: dsn})
	if err == nil || err.Error() != "apply database collation: invalid collation" {
		t.Fatalf("BuildMigrationDSN() error = %v, want safe invalid collation error", err)
	}
	if strings.Contains(err.Error(), "SELECT secret") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("BuildMigrationDSN() leaked unsafe DSN: %v", err)
	}
}

func TestRepositoryMigrationManifestAndInitMetadata(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "deploy", "mysql", "migrations")
	migrations, err := LoadMigrations(dir)
	if err != nil {
		t.Fatalf("LoadMigrations(repository) error = %v", err)
	}
	if len(migrations) != 9 {
		t.Fatalf("len(migrations) = %d, want 9", len(migrations))
	}
	wantChecksums := map[int]string{
		1: "81f71d4c8db2a412f0f9b0f1d4d61d6d53ecc538b801b2c365d570f789fa66a9",
		2: "df0b7f62b0e0465a64d7c90b9d798cada31159ffa683e22abceab41724d395fa",
		3: "0703d0fe865e6865d0047ae84ba75ab9a9506b72d90c60f74c3b36051ac11306",
		4: "254b502311291b48f7002c041fb6c96cad16f4386aa26e195a6bf373aa41bf17",
		5: "a2239296a829056b33833806a7a064ab6db7ad677f915c723bfe21cd92f9bdae",
		6: "42ad208b9fcbf9990fc295979d17b037bc7050410e9440b2dcffa46fae8e6248",
		7: "94c4e2d5edb46c0c920540684c63585973efa419c321cdcadc9e69e779ada971",
		8: "a52e9d085d265ebb39339e57931d95bbc396f2a4c3b675559b9dec0430a25db9",
		9: "b0ddb67f10af91b6ff7b9b4e94276c5bc8f1f5a3e4205de78cfd48e8712e620e",
	}
	for _, migration := range migrations {
		if want := wantChecksums[migration.Version]; want != "" && migration.Checksum != want {
			t.Errorf("migration %03d checksum = %s, want %s", migration.Version, migration.Checksum, want)
		}
	}

	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "init", "001_schema.sql"))
	if err != nil {
		t.Fatalf("read init schema: %v", err)
	}
	if got := bytes.Count(initSQL, []byte("DELIMITER $$\n")); got != 9 {
		t.Fatalf("init schema compound delimiter count = %d, want 9", got)
	}
	if got := bytes.Count(initSQL, []byte("DELIMITER ;\n")); got != 9 {
		t.Fatalf("init schema delimiter reset count = %d, want 9", got)
	}
	const clientCharsetPrologue = "SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
	if got := bytes.Count(initSQL, []byte(clientCharsetPrologue)); got != 1 {
		t.Fatalf("init schema client charset prologue count = %d, want 1", got)
	}

	wantInitSQL, err := renderRepositoryMySQLCLIInit(migrations)
	if err != nil {
		t.Fatalf("render repository init: %v", err)
	}
	if !bytes.Equal(initSQL, []byte(wantInitSQL)) {
		t.Fatalf("init schema is not the deterministic reversible MySQL CLI rendering of migrations 001..009")
	}

	initText := string(initSQL)
	if got := strings.Count(initText, "CREATE TABLE IF NOT EXISTS `schema_migrations` ("); got != 1 {
		t.Errorf("init schema schema_migrations DDL count = %d, want 1", got)
	}
	if got := strings.Count(initText, "CREATE TABLE IF NOT EXISTS `schema_migration_attempts` ("); got != 1 {
		t.Errorf("init schema schema_migration_attempts DDL count = %d, want 1", got)
	}
	if got := strings.Count(initText, "INSERT INTO `schema_migrations`"); got != 1 {
		t.Errorf("init schema ledger INSERT count = %d, want 1", got)
	}
	ledgerRowPattern := regexp.MustCompile(`(?m)^  \(([0-9]+), '([^']+)', '([0-9a-f]{64})', CURRENT_TIMESTAMP\(3\)\)(?:,|;)$`)
	ledgerRows := ledgerRowPattern.FindAllStringSubmatch(initText, -1)
	if len(ledgerRows) != len(migrations) {
		t.Fatalf("init schema ledger row count = %d, want %d", len(ledgerRows), len(migrations))
	}
	for _, migration := range migrations {
		row := ledgerRows[migration.Version-1]
		if row[1] != fmt.Sprint(migration.Version) || row[2] != migration.Name || row[3] != migration.Checksum {
			t.Errorf("init schema ledger row %d = (%s, %s, %s), want (%d, %s, %s)",
				migration.Version, row[1], row[2], row[3], migration.Version, migration.Name, migration.Checksum)
		}
	}
}

type mysqlCLIRoutine struct {
	kind, name, after string
}

func renderRepositoryMySQLCLIInit(migrations []Migration) (string, error) {
	routines := map[int][]mysqlCLIRoutine{
		7: {
			{kind: "procedure", name: "jxh_guard_007", after: "CALL `jxh_guard_007`();"},
		},
		8: {
			{kind: "procedure", name: "jxh_guard_008", after: "CALL `jxh_guard_008`();"},
			{kind: "procedure", name: "jxh_assert_table_008", after: "DROP PROCEDURE IF EXISTS `jxh_upgrade_core_008`;"},
			{kind: "procedure", name: "jxh_upgrade_core_008", after: "CALL `jxh_upgrade_core_008`();"},
			{kind: "procedure", name: "jxh_create_manager_tables_008", after: "CALL `jxh_create_manager_tables_008`();"},
			{kind: "procedure", name: "jxh_guard_session_triggers_008", after: "CALL `jxh_guard_session_triggers_008`();"},
			{kind: "trigger", name: "trg_admin_sessions_replacement_insert", after: "-- jxh:008-stage session-trigger-insert"},
			{kind: "trigger", name: "trg_admin_sessions_replacement_update", after: "-- jxh:008-stage session-trigger-update"},
		},
		9: {
			{kind: "procedure", name: "jxh_extend_system_operations_009", after: "CALL `jxh_extend_system_operations_009`();"},
		},
	}

	const header = "-- Jxh Manager final MySQL schema.\n" +
		"-- This standalone initializer applies the immutable migration chain to an empty database.\n\n" +
		"SET NAMES utf8mb4 COLLATE utf8mb4_0900_ai_ci;\n\n"
	const ledgerDDL = "CREATE TABLE IF NOT EXISTS `schema_migrations` (\n" +
		"  `version` int unsigned NOT NULL,\n" +
		"  `name` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,\n" +
		"  `checksum` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,\n" +
		"  `applied_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),\n" +
		"  PRIMARY KEY (`version`),\n" +
		"  UNIQUE KEY `uq_schema_migrations_name` (`name`)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;\n\n"
	const attemptLedgerDDL = "CREATE TABLE IF NOT EXISTS `schema_migration_attempts` (\n" +
		"  `version` int unsigned NOT NULL,\n" +
		"  `name` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,\n" +
		"  `checksum` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,\n" +
		"  `stage` int unsigned NOT NULL DEFAULT 0,\n" +
		"  `started_at` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),\n" +
		"  PRIMARY KEY (`version`)\n" +
		") ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;\n\n"

	var rendered strings.Builder
	rendered.WriteString(header)
	compoundCount := 0
	for _, migration := range migrations {
		rendered.WriteString("-- " + migration.Name + "\n")
		migrationSQL, count, err := renderMigrationForMySQLCLI(migration.SQL, routines[migration.Version])
		if err != nil {
			return "", fmt.Errorf("render migration %03d: %w", migration.Version, err)
		}
		compoundCount += count
		rendered.WriteString(migrationSQL)
		rendered.WriteByte('\n')
	}
	if compoundCount != 9 {
		return "", fmt.Errorf("repository compound CREATE count = %d, want 9", compoundCount)
	}

	rendered.WriteString(ledgerDDL)
	rendered.WriteString(attemptLedgerDDL)
	rendered.WriteString("INSERT INTO `schema_migrations` (`version`, `name`, `checksum`, `applied_at`) VALUES\n")
	for i, migration := range migrations {
		terminator := ",\n"
		if i == len(migrations)-1 {
			terminator = ";\n"
		}
		fmt.Fprintf(&rendered, "  (%d, '%s', '%s', CURRENT_TIMESTAMP(3))%s", migration.Version, migration.Name, migration.Checksum, terminator)
	}
	return rendered.String(), nil
}

func renderMigrationForMySQLCLI(migrationSQL string, routines []mysqlCLIRoutine) (string, int, error) {
	if strings.Contains(migrationSQL, "$$") || regexp.MustCompile(`(?im)^\s*delimiter\b`).MatchString(migrationSQL) {
		return "", 0, errors.New("migration SQL conflicts with the init-only MySQL CLI delimiter")
	}
	allCompoundCreates := regexp.MustCompile(`(?im)^[\t ]*create[\t ]+(?:definer[\t ]*=[\t ]*\S+[\t ]+)?(?:procedure|function|trigger|event)[\t ]+`).FindAllStringIndex(migrationSQL, -1)
	if len(allCompoundCreates) != len(routines) {
		return "", 0, fmt.Errorf("described compound CREATE count = %d, found %d", len(routines), len(allCompoundCreates))
	}

	var rendered strings.Builder
	cursor := 0
	outerEnd := regexp.MustCompile(`(?im)^[\t ]*end;[\t ]*(?:\n|$)`)
	for _, routine := range routines {
		createPattern := regexp.MustCompile(`(?im)^[\t ]*create[\t ]+(?:definer[\t ]*=[\t ]*\S+[\t ]+)?` +
			regexp.QuoteMeta(routine.kind) + `[\t ]+` + "`?" + regexp.QuoteMeta(routine.name) + "`?" + `\b`)
		starts := createPattern.FindAllStringIndex(migrationSQL, -1)
		if len(starts) != 1 {
			return "", 0, fmt.Errorf("routine %s %s occurs %d times", routine.kind, routine.name, len(starts))
		}
		start := starts[0][0]
		if start < cursor {
			return "", 0, fmt.Errorf("routine %s is out of descriptor order", routine.name)
		}
		afterOffset := strings.Index(migrationSQL[start:], routine.after)
		if afterOffset < 0 {
			return "", 0, fmt.Errorf("routine %s has no unique following anchor", routine.name)
		}
		after := start + afterOffset
		ends := outerEnd.FindAllStringIndex(migrationSQL[start:after], -1)
		if len(ends) == 0 {
			return "", 0, fmt.Errorf("routine %s has no outer END terminator", routine.name)
		}
		end := ends[len(ends)-1]
		endText := migrationSQL[start+end[0] : start+end[1]]
		semicolonInEnd := strings.LastIndex(endText, ";")
		if semicolonInEnd < 0 {
			return "", 0, fmt.Errorf("routine %s outer END has no semicolon", routine.name)
		}
		semicolon := start + end[0] + semicolonInEnd
		if strings.TrimSpace(migrationSQL[semicolon+1:after]) != "" {
			return "", 0, fmt.Errorf("routine %s has statements between outer END and anchor", routine.name)
		}

		rendered.WriteString(migrationSQL[cursor:start])
		rendered.WriteString("DELIMITER $$\n")
		rendered.WriteString(migrationSQL[start:semicolon])
		rendered.WriteString("$$\nDELIMITER ;")
		cursor = semicolon + 1
	}
	rendered.WriteString(migrationSQL[cursor:])

	restored := strings.ReplaceAll(rendered.String(), "DELIMITER $$\n", "")
	restored = strings.ReplaceAll(restored, "$$\nDELIMITER ;", ";")
	if restored != migrationSQL {
		return "", 0, errors.New("removing init-only delimiter wrappers did not restore migration SQL byte-for-byte")
	}
	return rendered.String(), len(routines), nil
}

func TestMySQLCLIRendererSupportsDescribedRoutineVariantsAndFailsClosed(t *testing.T) {
	script := "  create procedure `nested_proc`()\n" +
		"BEGIN\n  BEGIN\n    SELECT 1;\n  END;\nEND;\n-- after-procedure\n" +
		"CREATE DEFINER=`root`@`localhost` FUNCTION `lower_fn`() RETURNS INT\nBEGIN\n  RETURN 1;\nEND;\n-- after-function\n" +
		"create event `daily_event` ON SCHEDULE EVERY 1 DAY DO\nBEGIN\n  SELECT 2;\nEND;\n-- after-event\n" +
		"  CREATE TRIGGER `audit_trigger` BEFORE INSERT ON `items` FOR EACH ROW\nBEGIN\n  SET @seen = 1;\nEND;\n-- after-trigger\n"
	descriptors := []mysqlCLIRoutine{
		{kind: "procedure", name: "nested_proc", after: "-- after-procedure"},
		{kind: "function", name: "lower_fn", after: "-- after-function"},
		{kind: "event", name: "daily_event", after: "-- after-event"},
		{kind: "trigger", name: "audit_trigger", after: "-- after-trigger"},
	}
	rendered, count, err := renderMigrationForMySQLCLI(script, descriptors)
	if err != nil {
		t.Fatalf("render supported routine variants: %v", err)
	}
	if count != 4 || strings.Count(rendered, "DELIMITER $$\n") != 4 || strings.Count(rendered, "DELIMITER ;\n") != 4 {
		t.Fatalf("rendered compound wrappers = %d/%d/%d, want 4/4/4", count,
			strings.Count(rendered, "DELIMITER $$\n"), strings.Count(rendered, "DELIMITER ;\n"))
	}
	restored := strings.ReplaceAll(rendered, "DELIMITER $$\n", "")
	restored = strings.ReplaceAll(restored, "$$\nDELIMITER ;", ";")
	if restored != script {
		t.Fatal("renderer did not preserve routine bytes")
	}

	for _, test := range []struct {
		name        string
		sql         string
		descriptors []mysqlCLIRoutine
	}{
		{name: "undescribed routine", sql: script, descriptors: descriptors[:3]},
		{name: "delimiter collision", sql: "DELIMITER //\n"},
		{name: "dollar collision", sql: "SELECT '$$';\n"},
		{name: "missing outer end", sql: "CREATE PROCEDURE `broken`()\nBEGIN\nSELECT 1;\n-- after\n", descriptors: []mysqlCLIRoutine{{kind: "procedure", name: "broken", after: "-- after"}}},
		{name: "missing anchor", sql: "CREATE PROCEDURE `broken`()\nBEGIN\nSELECT 1;\nEND;\n", descriptors: []mysqlCLIRoutine{{kind: "procedure", name: "broken", after: "-- absent"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := renderMigrationForMySQLCLI(test.sql, test.descriptors); err == nil {
				t.Fatal("renderer accepted unsupported or ambiguous SQL")
			}
		})
	}
}

func TestRepositorySQLUsesLFAndSingleTrailingNewline(t *testing.T) {
	paths := []string{
		filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "007_remove_group_request_system_request_id.sql"),
		filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"),
		filepath.Join("..", "..", "..", "deploy", "mysql", "init", "001_schema.sql"),
	}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if bytes.Contains(contents, []byte{'\r'}) {
			t.Errorf("%s contains CR bytes", path)
		}
		if !bytes.HasSuffix(contents, []byte("\n")) || bytes.HasSuffix(contents, []byte("\n\n")) {
			t.Errorf("%s must end with exactly one LF", path)
		}
	}
}

func TestMigration007FailsClosedBeforeDroppingLegacyIdentifier(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "007_remove_group_request_system_request_id.sql"))
	if err != nil {
		t.Fatalf("read 007: %v", err)
	}
	script := string(contents)
	for _, required := range []string{
		"CREATE PROCEDURE `jxh_guard_007`()",
		"SIGNAL SQLSTATE '45000'",
		"CALL `jxh_guard_007`()",
		"information_schema.tables",
		"BINARY `table_type` = BINARY 'BASE TABLE'",
		"group_join_requests table is missing or incompatible",
		"`system_request_id` IS NOT NULL",
		"BINARY `request`.`system_request_id` <> BINARY `request`.`flag`",
		"DROP INDEX `idx_group_join_requests_system_request_id`",
		"DROP COLUMN `system_request_id`",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("007 is missing %q", required)
		}
	}
	if strings.Contains(script, "PREPARE") {
		t.Error("007 must not put SIGNAL in a non-preparable dynamic statement")
	}
	if strings.Index(script, "SIGNAL SQLSTATE '45000'") > strings.Index(script, "DROP COLUMN `system_request_id`") {
		t.Error("007 drops system_request_id before its consistency check")
	}
	for _, required := range []string{
		"information_schema.columns",
		"information_schema.statistics",
		"system_request_id state is inconsistent",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("007 is not safely restartable: missing %q", required)
		}
	}
}

func TestManagerMigrationGuardsAllLegacyEnumsBeforeBusinessDDL(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"))
	if err != nil {
		t.Fatalf("read 008: %v", err)
	}
	script := string(contents)
	firstBusinessDDL := strings.Index(script, "ALTER TABLE `group_join_requests`")
	if firstBusinessDDL < 0 {
		t.Fatal("008 lacks its first business DDL")
	}
	guard := script[:firstBusinessDDL]
	for _, required := range []string{
		"CREATE PROCEDURE `jxh_guard_008`()",
		"CALL `jxh_guard_008`()",
		"SIGNAL SQLSTATE '45000'",
		"BINARY `status` NOT IN (BINARY 'pending', BINARY 'processed')",
		"`sub_type` IS NULL OR BINARY `sub_type` NOT IN (BINARY 'add', BINARY 'invite')",
		"BINARY `source` NOT IN (BINARY 'event', BINARY 'system')",
		"BINARY `ai_parse_status` NOT IN (BINARY 'pending', BINARY 'running', BINARY 'completed', BINARY 'succeeded', BINARY 'failed', BINARY 'skipped')",
		"BINARY `type` NOT IN (BINARY '每天', BINARY '单次')",
		"`enabled` NOT IN (FALSE, TRUE)",
	} {
		if !strings.Contains(guard, required) {
			t.Errorf("008 preflight guard is missing %q", required)
		}
	}
	if strings.Contains(guard, "PREPARE") {
		t.Error("008 must not put SIGNAL in a non-preparable dynamic statement")
	}
	for _, forbidden := range []string{"ALTER TABLE", "CREATE TABLE", "UPDATE `group_join_requests`", "UPDATE `scheduled_jobs`"} {
		if strings.Contains(guard, forbidden) {
			t.Errorf("008 executes %q before completing all legacy preflight checks", forbidden)
		}
	}
}

func TestManagerSchemaTablesExistInMigrationAndInit(t *testing.T) {
	migrationSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"))
	if err != nil {
		t.Fatalf("read 008: %v", err)
	}
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "init", "001_schema.sql"))
	if err != nil {
		t.Fatalf("read init schema: %v", err)
	}
	requiredTables := []string{
		"admin_users", "admin_sessions", "admin_audit_logs", "admin_idempotency_keys",
		"managed_groups", "feature_settings", "group_join_policies", "custom_commands", "custom_command_runs",
		"group_join_decisions", "scheduled_job_runs", "bot_operation_events", "bot_operation_daily",
		"system_operations",
	}
	for _, table := range requiredTables {
		needle := "CREATE TABLE `" + table + "`"
		if !strings.Contains(string(migrationSQL), needle) {
			t.Errorf("008 is missing table %s", table)
		}
		if !strings.Contains(string(initSQL), needle) {
			t.Errorf("init schema is missing table %s", table)
		}
		unsafeNeedle := "CREATE TABLE IF NOT EXISTS `" + table + "`"
		if strings.Contains(string(migrationSQL), unsafeNeedle) || strings.Contains(string(initSQL), unsafeNeedle) {
			t.Errorf("manager table %s can silently accept an incompatible existing definition", table)
		}
	}
	for _, required := range []string{
		"`observed_status`", "`decision_status`", "`decision_source`", "`revision`", "`last_decision_id`",
		"`applicant_nickname`", "`ai_error_code`", "`validation_snapshot`", "`last_run_result`", "`updated_by_user_id`",
		"`revoked_reason`", "`replaced_by_session_id`", "`resulting_session_id`", "`request_hash`",
	} {
		if !strings.Contains(string(migrationSQL), required) || !strings.Contains(string(initSQL), required) {
			t.Errorf("manager schema is missing contract field %s", required)
		}
	}
}

func TestManagerMigrationPreciselyChecksEveryRestartableStage(t *testing.T) {
	migrationSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"))
	if err != nil {
		t.Fatalf("read 008: %v", err)
	}
	script := string(migrationSQL)
	for _, required := range []string{
		"CREATE PROCEDURE `jxh_assert_table_008`",
		"CALL `jxh_assert_table_008`('group_join_requests'",
		"CALL `jxh_assert_table_008`('scheduled_jobs'",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("008 is missing restart validation %q", required)
		}
	}
	for _, table := range []string{
		"admin_users", "admin_sessions", "admin_audit_logs", "admin_idempotency_keys",
		"managed_groups", "feature_settings", "group_join_policies", "custom_commands", "custom_command_runs",
		"group_join_decisions", "scheduled_job_runs", "bot_operation_events", "bot_operation_daily", "system_operations",
	} {
		if !strings.Contains(script, "CALL `jxh_assert_table_008`('"+table+"'") {
			t.Errorf("008 does not precisely validate restart state for %s", table)
		}
	}
}

func TestManagerMigrationBackfillsLegacyRowsBeforeConstraints(t *testing.T) {
	contents, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"))
	if err != nil {
		t.Fatalf("read 008: %v", err)
	}
	script := compactSQL(string(contents))
	patterns := []string{
		`UPDATE ` + "`group_join_requests`" + ` SET ` + "`observed_status`" + ` = CASE WHEN ` + "`status`" + ` = 'processed' THEN 'checked' ELSE 'pending' END, ` + "`decision_status`" + ` = CASE WHEN ` + "`status`" + ` = 'processed' THEN 'external_processed' ELSE 'pending' END, ` + "`decision_source`" + ` = CASE WHEN ` + "`status`" + ` = 'processed' THEN 'external' ELSE NULL END`,
		`UPDATE ` + "`scheduled_jobs`" + ` SET ` + "`status`" + ` = CASE WHEN ` + "`enabled`" + ` = TRUE THEN 'active' WHEN BINARY ` + "`type`" + ` = BINARY '单次' AND ` + "`last_run_at`" + ` IS NOT NULL THEN 'completed' ELSE 'archived' END`,
		`UPDATE ` + "`scheduled_jobs`" + ` SET ` + "`created_at`" + ` = COALESCE(` + "`created_at`" + `, ` + "`updated_at`" + `, CURRENT_TIMESTAMP(3)), ` + "`updated_at`" + ` = COALESCE(` + "`updated_at`" + `, ` + "`created_at`" + `, CURRENT_TIMESTAMP(3))`,
	}
	for _, pattern := range patterns {
		if !strings.Contains(script, pattern) {
			t.Errorf("008 is missing exact backfill: %s", pattern)
		}
	}
	groupBackfill := strings.Index(script, patterns[0])
	groupConstraint := strings.Index(script, "ADD CONSTRAINT `chk_group_join_requests_observed_status`")
	if groupBackfill < 0 || groupConstraint < 0 || groupBackfill > groupConstraint {
		t.Error("group request constraints are applied before the legacy status backfill")
	}
	jobTimeBackfill := strings.Index(script, patterns[2])
	jobNotNull := strings.Index(script, "MODIFY COLUMN `created_at` datetime(3) NOT NULL")
	if jobTimeBackfill < 0 || jobNotNull < 0 || jobTimeBackfill > jobNotNull {
		t.Error("scheduled job timestamps are tightened before NULL rows are backfilled")
	}
	if !strings.Contains(script, "MODIFY COLUMN `type` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL") {
		t.Error("008 must preserve Chinese scheduled_jobs.type values with binary comparison semantics")
	}
}

func TestManagerSchemaStoresOnlySafeIdempotencySummary(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		idempotency := tableDefinition(t, script, "admin_idempotency_keys")
		decision := tableDefinition(t, script, "group_join_decisions")
		if strings.Contains(idempotency, "response_envelope") {
			t.Errorf("%s idempotency schema can persist an arbitrary response envelope", label)
		}
		for _, required := range []string{
			"`idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`request_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`response_status` smallint unsigned DEFAULT NULL",
			"`error_code` varchar(100) DEFAULT NULL",
			"`resource_id` varchar(256) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin DEFAULT NULL",
			"`resulting_session_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
			"`trace_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
		} {
			if !strings.Contains(idempotency, required) {
				t.Errorf("%s idempotency schema is missing %q", label, required)
			}
		}
		if !strings.Contains(decision, "`idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL") {
			t.Errorf("%s decision idempotency key is not limited to 128", label)
		}
	}
}

func TestManagerSchemaUsesOneVersionedSettingsRowPerScope(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		settings := tableDefinition(t, script, "feature_settings")
		for _, forbidden := range []string{"`feature_key`", "`override_state`"} {
			if strings.Contains(settings, forbidden) {
				t.Errorf("%s feature_settings still stores per-feature rows via %s", label, forbidden)
			}
		}
		for _, required := range []string{
			"`scope_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`group_id` bigint DEFAULT NULL",
			"`scope_key` bigint AS (COALESCE(`group_id`, 0)) STORED",
			"`settings_json` json NOT NULL",
			"`revision` int unsigned NOT NULL DEFAULT 1",
			"UNIQUE KEY `uq_feature_settings_scope` (`scope_type`, `scope_key`)",
			"CONSTRAINT `chk_feature_settings_scope` CHECK ((`scope_type` = 'global' AND `group_id` IS NULL) OR (`scope_type` = 'group' AND `group_id` IS NOT NULL AND `group_id` > 0))",
		} {
			if !strings.Contains(settings, required) {
				t.Errorf("%s feature_settings is missing %q", label, required)
			}
		}
		if !strings.Contains(script, "INSERT INTO `feature_settings`") || !strings.Contains(script, "'global', NULL, JSON_OBJECT(), 1") {
			t.Errorf("%s schema does not seed the single global settings row", label)
		}

		policy := tableDefinition(t, script, "group_join_policies")
		for _, required := range []string{
			"`group_id` bigint NOT NULL", "`mode` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'ai_fields_complete'",
			"`required_fields` json NOT NULL", "`auto_reject` boolean NOT NULL DEFAULT FALSE",
			"CONSTRAINT `chk_group_join_policies_mode` CHECK (`mode` = 'ai_fields_complete')",
			"CONSTRAINT `chk_group_join_policies_auto_reject` CHECK (`auto_reject` = FALSE)",
		} {
			if !strings.Contains(policy, required) {
				t.Errorf("%s group_join_policies is missing %q", label, required)
			}
		}
	}
}

func TestManagerTelemetryHasExplicitCompositeDimensions(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		events := tableDefinition(t, script, "bot_operation_events")
		if !strings.Contains(events, "`feature_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL") {
			t.Errorf("%s bot_operation_events lacks feature_key", label)
		}
		daily := tableDefinition(t, script, "bot_operation_daily")
		for _, forbidden := range []string{"`dimension_type`", "`dimension_id`", "`metadata`"} {
			if strings.Contains(daily, forbidden) {
				t.Errorf("%s daily telemetry depends on ambiguous %s", label, forbidden)
			}
		}
		for _, required := range []string{
			"`group_id` bigint NOT NULL DEFAULT 0", "`feature_key` varchar(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT ''",
			"`outcome` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT ''",
			"PRIMARY KEY (`bucket_date`, `timezone`, `metric_key`, `group_id`, `feature_key`, `outcome`)",
		} {
			if !strings.Contains(daily, required) {
				t.Errorf("%s daily telemetry is missing %q", label, required)
			}
		}
	}
}

func TestManagerTelemetryEnumsMatchOpenAPIExactly(t *testing.T) {
	featureValues := "('keyword_reply', 'ai_qa', 'quote', 'link_cleaner', 'welcome', 'custom_commands')"
	outcomeValues := "('success', 'failed', 'denied', 'unknown', 'fallback', 'skipped')"
	metricValues := "('keyword_reply_count', 'ai_request_count', 'ai_success_rate', 'ai_duration_ms', 'join_request_count', 'manual_approval_count', 'automatic_approval_count', 'scheduled_job_run_count', 'group_message_count', 'command_run_count', 'active_user_count', 'link_clean_count', 'quote_success_count', 'quote_fallback_count', 'quote_failure_count')"
	for label, script := range repositoryManagerSchemas(t) {
		events := tableDefinition(t, script, "bot_operation_events")
		for _, required := range []string{
			"`outcome` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
			"CHECK (`feature_key` IS NULL OR `feature_key` = '' OR `feature_key` IN " + featureValues + ")",
			"CHECK (`outcome` IS NULL OR `outcome` = '' OR `outcome` IN " + outcomeValues + ")",
		} {
			if !strings.Contains(events, required) {
				t.Errorf("%s events schema is missing %q", label, required)
			}
		}
		daily := tableDefinition(t, script, "bot_operation_daily")
		for _, required := range []string{
			"CHECK (`feature_key` = '' OR `feature_key` IN " + featureValues + ")",
			"CHECK (`outcome` = '' OR `outcome` IN " + outcomeValues + ")",
			"CHECK (`metric_key` IN " + metricValues + ")",
		} {
			if !strings.Contains(daily, required) {
				t.Errorf("%s daily schema is missing %q", label, required)
			}
		}
	}
}

func TestManagerSchemaStoresCompleteUpdateActorSnapshots(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		for _, table := range []string{"scheduled_jobs", "custom_commands"} {
			definition := script
			if table == "custom_commands" {
				definition = tableDefinition(t, script, table)
			}
			for _, required := range []string{
				"`updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
				"`updated_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
				"`updated_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
				"`updated_by_display_name` varchar(100) NOT NULL",
				"CHECK ((`updated_by_type` = 'system' AND `updated_by_user_id` IS NULL AND `updated_by_qq_user_id` IS NULL) OR (`updated_by_type` = 'admin_user' AND `updated_by_user_id` IS NOT NULL) OR (`updated_by_type` = 'qq_user' AND `updated_by_qq_user_id` IS NOT NULL))",
			} {
				if !strings.Contains(definition, required) {
					t.Errorf("%s %s schema is missing %q", label, table, required)
				}
			}
		}
	}

	migration := repositoryManagerSchemas(t)["008"]
	backfill := "UPDATE `scheduled_jobs` SET `updated_by_type` = 'system', `updated_by_user_id` = NULL, `updated_by_qq_user_id` = NULL, `updated_by_display_name` = 'system'"
	tighten := "MODIFY COLUMN `updated_by_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL"
	if strings.Index(compactSQL(migration), backfill) < 0 || strings.Index(compactSQL(migration), backfill) > strings.Index(compactSQL(migration), tighten) {
		t.Error("008 does not backfill the scheduled job actor snapshot before tightening it")
	}
}

func TestAdminSessionReplacementRequiresRevocationAndSameUser(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		sessions := tableDefinition(t, script, "admin_sessions")
		for _, required := range []string{
			"`replacement_depth` int unsigned NOT NULL DEFAULT 0",
			"`replaced_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
			"`replaced_by_depth` int unsigned DEFAULT NULL",
			"UNIQUE KEY `uq_admin_sessions_replacement_target` (`session_id`, `user_id`, `replacement_depth`)",
			"FOREIGN KEY (`replaced_by_session_id`, `replaced_by_user_id`, `replaced_by_depth`) REFERENCES `admin_sessions` (`session_id`, `user_id`, `replacement_depth`) ON DELETE SET NULL",
			"CHECK ((`status` = 'revoked' AND `revoked_at` IS NOT NULL) OR (`status` IN ('active', 'expired') AND `revoked_at` IS NULL))",
		} {
			if !strings.Contains(sessions, required) {
				t.Errorf("%s admin_sessions is missing %q", label, required)
			}
		}
		for _, required := range []string{
			"CREATE TRIGGER `trg_admin_sessions_replacement_insert`",
			"CREATE TRIGGER `trg_admin_sessions_replacement_update`",
			"NEW.replaced_by_user_id = NEW.user_id",
			"NEW.replaced_by_depth > NEW.replacement_depth",
			"NEW.status = 'revoked' AND NEW.revoked_at IS NOT NULL",
			"NEW.replacement_depth <> OLD.replacement_depth",
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s admin_sessions replacement trigger is missing %q", label, required)
			}
		}
	}
}

func TestManagerSchemaHasExecutableRetentionPaths(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		sessions := tableDefinition(t, script, "admin_sessions")
		idempotency := tableDefinition(t, script, "admin_idempotency_keys")
		operations := tableDefinition(t, script, "system_operations")
		for _, required := range []string{
			"KEY `idx_admin_sessions_absolute_expiry` (`absolute_expires_at`, `session_id`)",
			"ON DELETE SET NULL",
		} {
			if !strings.Contains(sessions, required) {
				t.Errorf("%s admin_sessions cleanup path is missing %q", label, required)
			}
		}
		for _, required := range []string{
			"KEY `idx_admin_idempotency_expiry` (`expires_at`, `idempotency_id`)",
			"FOREIGN KEY (`resulting_session_id`) REFERENCES `admin_sessions` (`session_id`) ON DELETE SET NULL",
		} {
			if !strings.Contains(idempotency, required) {
				t.Errorf("%s admin_idempotency_keys cleanup path is missing %q", label, required)
			}
		}
		for _, required := range []string{
			"KEY `idx_system_operations_cleanup` (`completed_at`, `operation_id`)",
			"FOREIGN KEY (`idempotency_id`) REFERENCES `admin_idempotency_keys` (`idempotency_id`) ON DELETE SET NULL",
		} {
			if !strings.Contains(operations, required) {
				t.Errorf("%s system_operations cleanup path is missing %q", label, required)
			}
		}
	}
}

func TestAdminIdempotencyStateVocabularyIsClosedAndCaseSensitive(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		definition := tableDefinition(t, script, "admin_idempotency_keys")
		for _, required := range []string{
			"`state` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`result_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
			"CONSTRAINT `chk_admin_idempotency_state` CHECK (`state` IN ('in_progress', 'completed'))",
			"CONSTRAINT `chk_admin_idempotency_result_status` CHECK (`result_status` IS NULL OR `result_status` IN ('succeeded', 'failed', 'unknown'))",
			"CONSTRAINT `chk_admin_idempotency_completion` CHECK ((`state` = 'in_progress' AND `result_status` IS NULL AND `completed_at` IS NULL) OR (`state` = 'completed' AND `result_status` IS NOT NULL AND `completed_at` IS NOT NULL))",
		} {
			if !strings.Contains(definition, required) {
				t.Errorf("%s admin_idempotency_keys is missing %q", label, required)
			}
		}
	}
}

func TestGroupJoinPolicyRequiresExactTopLevelStringFields(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		policy := tableDefinition(t, script, "group_join_policies")
		for _, required := range []string{
			"JSON_TYPE(`required_fields`) = 'ARRAY'",
			"JSON_LENGTH(`required_fields`) = 3",
			"JSON_TYPE(JSON_EXTRACT(`required_fields`, '$[0]')) = 'STRING'",
			"JSON_TYPE(JSON_EXTRACT(`required_fields`, '$[1]')) = 'STRING'",
			"JSON_TYPE(JSON_EXTRACT(`required_fields`, '$[2]')) = 'STRING'",
			"JSON_CONTAINS(`required_fields`, JSON_QUOTE('student_id'), '$')",
			"JSON_CONTAINS(`required_fields`, JSON_QUOTE('name'), '$')",
			"JSON_CONTAINS(`required_fields`, JSON_QUOTE('major'), '$')",
		} {
			if !strings.Contains(policy, required) {
				t.Errorf("%s group_join_policies is missing %q", label, required)
			}
		}
	}
}

func TestManagerSchemaEnforcesStableEnumsAndReferences(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		for _, required := range []string{
			"`role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"CONSTRAINT `chk_admin_users_role` CHECK (`role` IN ('super_admin', 'maintainer', 'observer'))",
			"CONSTRAINT `chk_admin_sessions_status` CHECK (`status` IN ('active', 'expired', 'revoked'))",
			"CONSTRAINT `chk_admin_audit_actor_type` CHECK (`actor_type` IN ('admin_user', 'qq_user', 'system'))",
			"CONSTRAINT `chk_admin_audit_result` CHECK (`result` IN ('success', 'failed', 'unknown'))",
			"CONSTRAINT `chk_custom_commands_status` CHECK (`status` IN ('draft', 'active', 'disabled', 'archived'))",
			"CONSTRAINT `chk_custom_commands_permission` CHECK (`trigger_permission` IN ('everyone', 'group_admin', 'maintenance_allowlist'))",
			"CONSTRAINT `chk_group_join_decisions_status` CHECK (`status` IN ('started', 'confirmed', 'failed', 'unknown'))",
			"CONSTRAINT `chk_scheduled_jobs_status` CHECK (`status` IN ('active', 'paused', 'completed', 'archived'))",
			"CONSTRAINT `chk_scheduled_job_runs_result` CHECK (`result` IN ('success', 'failed', 'unknown', 'skipped'))",
			"CONSTRAINT `chk_system_operations_type` CHECK (`type` = 'napcat_restart')",
			"CONSTRAINT `chk_system_operations_status` CHECK (`status` IN ('accepted', 'running', 'succeeded', 'failed', 'unknown'))",
			"CONSTRAINT `fk_admin_sessions_replacement` FOREIGN KEY (`replaced_by_session_id`, `replaced_by_user_id`, `replaced_by_depth`) REFERENCES `admin_sessions` (`session_id`, `user_id`, `replacement_depth`) ON DELETE SET NULL",
			"CONSTRAINT `chk_system_operations_completion` CHECK ((`status` IN ('accepted', 'running') AND `completed_at` IS NULL) OR (`status` IN ('succeeded', 'failed', 'unknown') AND `completed_at` IS NOT NULL))",
			"UNIQUE KEY `uq_group_join_decisions_request_ref` (`decision_id`, `request_id`)",
			"FOREIGN KEY (`last_decision_id`, `id`) REFERENCES `group_join_decisions` (`decision_id`, `request_id`)",
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s schema is missing integrity rule %q", label, required)
			}
		}
	}
}

func TestManagerStableEnumsUseCaseSensitiveCollations(t *testing.T) {
	for label, script := range repositoryManagerSchemas(t) {
		for _, required := range []string{
			"`sub_type` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`ai_parse_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending'",
			"`observed_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending'",
			"`decision_status` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT 'pending'",
			"`decision_source` varchar(32) CHARACTER SET ascii COLLATE ascii_bin DEFAULT NULL",
			"`role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`bot_role` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`snapshot_state` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`trigger_permission` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`kind` varchar(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`outcome` varchar(32) CHARACTER SET ascii COLLATE ascii_bin",
			"`type` varchar(16) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL",
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s schema is missing case-sensitive enum declaration %q", label, required)
			}
		}
	}
}

func repositoryManagerSchemas(t *testing.T) map[string]string {
	t.Helper()
	result := make(map[string]string, 2)
	for label, path := range map[string]string{
		"008":  filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"),
		"init": filepath.Join("..", "..", "..", "deploy", "mysql", "init", "001_schema.sql"),
	} {
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s schema: %v", label, err)
		}
		result[label] = string(contents)
	}
	return result
}

func tableDefinition(t *testing.T, script, table string) string {
	t.Helper()
	pattern := regexp.MustCompile(`(?s)CREATE TABLE (?:IF NOT EXISTS )?` + "`" + regexp.QuoteMeta(table) + "`" + ` \(.*?\n\) ENGINE=InnoDB.*?;`)
	definition := pattern.FindString(script)
	if definition == "" {
		t.Fatalf("schema is missing CREATE TABLE for %s", table)
	}
	return definition
}

func compactSQL(script string) string {
	return strings.TrimSpace(regexp.MustCompile(`\s+`).ReplaceAllString(script, " "))
}

func TestManagerSchemaPersistsSafeContractSnapshots(t *testing.T) {
	migrationSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "migrations", "008_create_manager_schema.sql"))
	if err != nil {
		t.Fatalf("read 008: %v", err)
	}
	initSQL, err := os.ReadFile(filepath.Join("..", "..", "..", "deploy", "mysql", "init", "001_schema.sql"))
	if err != nil {
		t.Fatalf("read init schema: %v", err)
	}
	for label, script := range map[string]string{"008": string(migrationSQL), "init": string(initSQL)} {
		for _, required := range []string{
			"`username` varchar(32)",
			"`name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`active_name` varchar(33) CHARACTER SET ascii COLLATE ascii_bin",
			"`actor_role` varchar(32)",
			"`scope_type` varchar(32)",
			"`scope_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin",
			"`reason` varchar(500)",
			"`actor_display_name` varchar(100)",
			"`field_snapshot` json",
			"`trace_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin",
			"`triggered_by_user_id` varchar(64) CHARACTER SET ascii COLLATE ascii_bin",
			"`triggered_by_qq_user_id` varchar(32) CHARACTER SET ascii COLLATE ascii_bin",
			"`triggered_by_display_name` varchar(100)",
			"`token_digest` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`idempotency_key` varchar(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
			"`request_hash` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL",
		} {
			if !strings.Contains(script, required) {
				t.Errorf("%s schema is missing %q", label, required)
			}
		}
		for _, forbidden := range []string{
			"`session_secret`", "`database_password`", "`onebot_token`", "`ai_key`", "`raw_token`",
		} {
			if strings.Contains(script, forbidden) {
				t.Errorf("%s schema persists forbidden secret column %s", label, forbidden)
			}
		}
	}
}

func TestRunnerApplyRejectsTamperedCanonicalHistoricalMigrationBeforeDatabaseAccess(t *testing.T) {
	script := "SELECT 'tampered historical migration';\n"
	sum := sha256.Sum256([]byte(script))
	migration := Migration{
		Version:  1,
		Name:     historicalMigrationIdentities[0].name,
		SQL:      script,
		Checksum: hex.EncodeToString(sum[:]),
	}

	_, err := (Runner{}).Apply(context.Background(), []Migration{migration})
	if !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift before database access", err)
	}
}

func TestRunnerApplyRejectsTamperedRepositoryMigration007BeforeDatabaseAccess(t *testing.T) {
	migrations := repositoryMigrations(t)
	script := "SELECT 'tampered migration 007';\n"
	sum := sha256.Sum256([]byte(script))
	migrations[6].SQL = script
	migrations[6].Checksum = hex.EncodeToString(sum[:])

	_, err := (Runner{}).Apply(context.Background(), migrations)
	if !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift before database access", err)
	}
}

func TestRunnerApplyTreatsMigration007NameAsGenericWithoutRepositoryHistory(t *testing.T) {
	migrations := make([]Migration, 7)
	for i := range migrations {
		version := i + 1
		script := fmt.Sprintf("SELECT %d;", version)
		sum := sha256.Sum256([]byte(script))
		name := fmt.Sprintf("%03d_unrelated", version)
		if version == 7 {
			name = "007_remove_group_request_system_request_id"
		}
		migrations[i] = Migration{Version: version, Name: name, SQL: script, Checksum: hex.EncodeToString(sum[:])}
	}
	steps := []scriptStep{
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), nil),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
	}
	for version := 1; version <= 7; version++ {
		steps = append(steps,
			execStep(fmt.Sprintf("SELECT %d;", version)),
			execStep("INSERT INTO `schema_migrations`"),
		)
	}
	steps = append(steps, queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}))
	db, state := newScriptDB(t, steps...)

	applied, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(applied) != len(migrations) {
		t.Fatalf("applied = %+v, want all generic migrations", applied)
	}
	state.assertComplete(t)
}

func TestRunnerApplyUsesOneConnectionAndRecordsOnlySuccessfulMigrations(t *testing.T) {
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), nil),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
		execStep("SELECT 1;"),
		execStep("INSERT INTO `schema_migrations`"),
		execStep("SELECT 2;"),
		execStep("INSERT INTO `schema_migrations`"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migrations := []Migration{
		{Version: 1, Name: "001_first", SQL: "SELECT 1;", Checksum: strings.Repeat("a", 64)},
		{Version: 2, Name: "002_second", SQL: "SELECT 2;", Checksum: strings.Repeat("b", 64)},
	}

	applied, err := (Runner{DB: db, LockTimeout: time.Second}).Apply(context.Background(), migrations)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(applied) != 2 || applied[0].Name != "001_first" || applied[1].Name != "002_second" {
		t.Fatalf("applied = %+v", applied)
	}
	state.assertComplete(t)
	state.assertSingleConnection(t)
}

func TestRunnerApplyRejectsDriftAndReleasesLock(t *testing.T) {
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, [][]driver.Value{{int64(1), "001_first", strings.Repeat("f", 64)}}),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migrations := []Migration{{Version: 1, Name: "001_first", SQL: "SELECT 1;", Checksum: strings.Repeat("a", 64)}}

	_, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if !errors.Is(err, ErrDrift) {
		t.Fatalf("Apply() error = %v, want ErrDrift", err)
	}
	state.assertComplete(t)
}

func TestRunnerApplyDoesNotRecordFailedSQLOrLeakIt(t *testing.T) {
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), nil),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
		errorExecStep("SELECT secret", errors.New("driver failed near SELECT secret using password=hunter2")),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migrations := []Migration{{Version: 1, Name: "001_sensitive", SQL: "SELECT secret", Checksum: strings.Repeat("a", 64)}}

	_, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if err == nil {
		t.Fatal("Apply() error = nil, want failure")
	}
	if strings.Contains(err.Error(), "SELECT secret") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("Apply() leaked sensitive details: %v", err)
	}
	state.assertComplete(t)
	if state.countCallsContaining("INSERT INTO `schema_migrations`") != 0 {
		t.Fatal("failed migration was recorded")
	}
}

func TestRunnerApplyReturnsFailureWhenRecordingVersionFails(t *testing.T) {
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), nil),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
		execStep("SELECT 1;"),
		errorExecStep("INSERT INTO `schema_migrations`", errors.New("record failed password=hunter2")),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migration := Migration{Version: 1, Name: "001_first", SQL: "SELECT 1;", Checksum: strings.Repeat("a", 64)}

	applied, err := (Runner{DB: db}).Apply(context.Background(), []Migration{migration})
	if err == nil || !strings.Contains(err.Error(), "record migration 001") {
		t.Fatalf("Apply() error = %v, want record operation failure", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("Apply() leaked driver error: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none", applied)
	}
	state.assertComplete(t)
}

func TestRunnerApplyRejectsUnavailableLockWithoutAttemptingRelease(t *testing.T) {
	for _, value := range []driver.Value{int64(0), nil} {
		t.Run(fmt.Sprintf("result_%v", value), func(t *testing.T) {
			db, state := newScriptDB(t,
				queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{value}}),
			)

			_, err := (Runner{DB: db}).Apply(context.Background(), nil)
			if !errors.Is(err, ErrLock) {
				t.Fatalf("Apply() error = %v, want ErrLock", err)
			}
			state.assertComplete(t)
		})
	}
}

func TestRunnerApplyDiscardsConnectionWhenLockOwnershipIsUnknown(t *testing.T) {
	tests := []struct {
		name    string
		acquire scriptStep
		want    error
	}{
		{name: "context canceled", acquire: queryErrorStep("GET_LOCK", context.Canceled), want: context.Canceled},
		{name: "query error", acquire: queryErrorStep("GET_LOCK", errors.New("query failed password=hunter2")), want: ErrLock},
		{name: "scan error", acquire: queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{"not-an-integer"}}), want: ErrLock},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, state := newScriptDB(t, test.acquire, execStep("fresh connection"))
			db.SetMaxOpenConns(1)

			_, err := (Runner{DB: db}).Apply(context.Background(), nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("Apply() error = %v, want errors.Is(..., %v)", err, test.want)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("Apply() leaked acquire error: %v", err)
			}
			if _, err := db.ExecContext(context.Background(), "fresh connection"); err != nil {
				t.Fatalf("execute after unknown lock ownership: %v", err)
			}
			state.assertComplete(t)
			state.assertLastCallUsesNewConnection(t)
		})
	}
}

func TestRunnerApplyReleaseFailureReturnsErrLockAndDiscardsConnection(t *testing.T) {
	tests := []struct {
		name    string
		release scriptStep
	}{
		{name: "zero", release: queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(0)}})},
		{name: "null", release: queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{nil}})},
		{name: "query error", release: queryErrorStep("RELEASE_LOCK", errors.New("release failed password=hunter2"))},
		{name: "scan error", release: queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{"not-an-integer"}})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, state := newScriptDB(t,
				queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
				execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
				queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
				queryStep("information_schema.columns", legacyColumnNames(), nil),
				queryStep("information_schema.statistics", legacyIndexNames(), nil),
				queryStep("information_schema.tables", legacyTableNames(), nil),
				queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
				test.release,
				execStep("fresh connection"),
			)
			db.SetMaxOpenConns(1)

			_, err := (Runner{DB: db}).Apply(context.Background(), nil)
			if !errors.Is(err, ErrLock) {
				t.Fatalf("Apply() error = %v, want ErrLock", err)
			}
			if strings.Contains(err.Error(), "hunter2") {
				t.Fatalf("Apply() leaked release error: %v", err)
			}
			if _, err := db.ExecContext(context.Background(), "fresh connection"); err != nil {
				t.Fatalf("execute after discarded connection: %v", err)
			}
			state.assertComplete(t)
			state.assertLastCallUsesNewConnection(t)
		})
	}
}

func TestRunnerApplyJoinsMigrationAndReleaseFailures(t *testing.T) {
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), nil),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
		errorExecStep("SELECT secret", errors.New("execute failed password=hunter2")),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(0)}}),
	)
	migration := Migration{Version: 1, Name: "001_first", SQL: "SELECT secret", Checksum: strings.Repeat("a", 64)}

	_, err := (Runner{DB: db}).Apply(context.Background(), []Migration{migration})
	if !errors.Is(err, ErrLock) || !strings.Contains(err.Error(), "execute migration 001") {
		t.Fatalf("Apply() error = %v, want joined migration failure and ErrLock", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("Apply() leaked driver error: %v", err)
	}
	state.assertComplete(t)
}

func TestRunnerApplyClosesIndexRowsBeforeReleasingLockAfterScanError(t *testing.T) {
	badIndexRow := []driver.Value{
		"group_join_requests", "idx_group_join_requests_flag", int64(0), int64(1), "flag",
		"not-an-integer", "BTREE", "", "YES", "A",
	}
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStepRequiringClose("information_schema.statistics", legacyIndexNames(), [][]driver.Value{badIndexRow}),
		rowsCloseStep("information_schema.statistics"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)

	_, err := (Runner{DB: db}).Apply(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "inspect legacy schema index") {
		t.Fatalf("Apply() error = %v, want index inspection failure", err)
	}
	state.assertComplete(t)
	state.assertSingleConnection(t)
}

func TestRunnerApplyRejectsExpressionIndexAsUnknownLegacySchema(t *testing.T) {
	snapshot := knownPost007LegacySchema()
	indexRows := legacyIndexRows(snapshot)
	indexRows[0][4] = nil
	indexRows[0][7] = "(`id` + 0)"
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), indexRows),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)

	_, err := (Runner{DB: db}).Apply(context.Background(), nil)
	if !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
	}
	state.assertComplete(t)
	state.assertSingleConnection(t)
}

func TestRunnerApplyClosesRowsBeforeReleasingLockOnInspectionFailures(t *testing.T) {
	type sourceFixture struct {
		name, query, errorFragment string
		columns                    []string
		badRow                     []driver.Value
	}
	sources := []sourceFixture{
		{
			name: "ledger", query: "SELECT `version`, `name`, `checksum`", errorFragment: "applied migration",
			columns: []string{"version", "name", "checksum"},
			badRow:  []driver.Value{"not-an-integer", "001_first", strings.Repeat("a", 64)},
		},
		{
			name: "columns", query: "information_schema.columns", errorFragment: "legacy schema column",
			columns: legacyColumnNames(),
			badRow:  []driver.Value{"group_join_requests", "id", "not-an-integer", "bigint unsigned", "NO", nil, nil, "auto_increment"},
		},
		{
			name: "indexes", query: "information_schema.statistics", errorFragment: "legacy schema index",
			columns: legacyIndexNames(),
			badRow: []driver.Value{
				"group_join_requests", "PRIMARY", int64(0), "not-an-integer", "id",
				int64(0), "BTREE", nil, "YES", "A",
			},
		},
		{
			name: "tables", query: "information_schema.tables", errorFragment: "legacy schema table",
			columns: legacyTableNames(),
			badRow:  []driver.Value{nil, "InnoDB", "utf8mb4_0900_ai_ci"},
		},
		{
			name: "constraints", query: "information_schema.table_constraints", errorFragment: "legacy schema constraint",
			columns: legacyConstraintNames(),
			badRow: []driver.Value{
				"group_join_requests", "PRIMARY", "PRIMARY KEY", "not-an-integer", "id", nil, nil, nil,
			},
		},
	}

	for sourceIndex, source := range sources {
		for _, failure := range []string{"query", "scan", "rows"} {
			t.Run(source.name+"/"+failure, func(t *testing.T) {
				steps := []scriptStep{
					queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
					execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
				}
				if sourceIndex > 0 {
					steps = append(steps, queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil))
					for _, prior := range sources[1:sourceIndex] {
						steps = append(steps, queryStep(prior.query, prior.columns, nil))
					}
				}
				switch failure {
				case "query":
					steps = append(steps, queryErrorStep(source.query, errors.New("driver failure password=hunter2")))
				case "scan":
					steps = append(steps,
						queryStepRequiringClose(source.query, source.columns, [][]driver.Value{source.badRow}),
						rowsCloseStep(source.query),
					)
				case "rows":
					steps = append(steps,
						queryRowsErrorStepRequiringClose(source.query, source.columns, errors.New("rows failure password=hunter2")),
						rowsCloseStep(source.query),
					)
				}
				steps = append(steps, queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}))
				db, state := newScriptDB(t, steps...)

				_, err := (Runner{DB: db}).Apply(context.Background(), nil)
				if err == nil || !strings.Contains(err.Error(), source.errorFragment) {
					t.Fatalf("Apply() error = %v, want %q inspection failure", err, source.errorFragment)
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Fatalf("Apply() leaked driver details: %v", err)
				}
				state.assertComplete(t)
				state.assertSingleConnection(t)
			})
		}
	}
}

func TestRecognizeLegacyBaselineFailsClosed(t *testing.T) {
	snapshot := knownPost007LegacySchema()
	baseline, err := recognizeLegacyBaseline(snapshot)
	if err != nil || baseline != 7 {
		t.Fatalf("recognizeLegacyBaseline(post-007) = %d, %v; want 7, nil", baseline, err)
	}

	tests := []struct {
		name   string
		mutate func(*legacySchema)
	}{
		{
			name: "missing column",
			mutate: func(schema *legacySchema) {
				schema.Columns = schema.Columns[:len(schema.Columns)-1]
			},
		},
		{
			name: "changed column",
			mutate: func(schema *legacySchema) {
				schema.Columns[0].Type = "bigint"
			},
		},
		{
			name: "changed default",
			mutate: func(schema *legacySchema) {
				schema.Columns[0].Default = "0"
			},
		},
		{
			name: "default differs only by case",
			mutate: func(schema *legacySchema) {
				column := slices.IndexFunc(schema.Columns, func(column legacyColumn) bool {
					return column.Table == "group_join_requests" && column.Name == "ai_parse_status"
				})
				if column < 0 {
					t.Fatal("known post-007 schema lacks ai_parse_status")
				}
				schema.Columns[column].Default = "PENDING"
			},
		},
		{
			name: "extra index",
			mutate: func(schema *legacySchema) {
				schema.Indexes = append(schema.Indexes, legacyIndex{Table: "scheduled_jobs", Name: "unknown", Column: "group_id", Sequence: 1, Unique: false})
			},
		},
		{
			name: "changed table engine",
			mutate: func(schema *legacySchema) {
				schema.Tables[0].Engine = "MyISAM"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := knownPost007LegacySchema()
			tt.mutate(&candidate)
			baseline, err := recognizeLegacyBaseline(candidate)
			if baseline != 0 || !errors.Is(err, ErrLegacySchema) {
				t.Fatalf("recognizeLegacyBaseline() = %d, %v; want 0, ErrLegacySchema", baseline, err)
			}
		})
	}
}

func TestRecognizeLegacyBaselineAdoptsPost005AtFive(t *testing.T) {
	snapshot := post005LegacySchemaFixture()
	baseline, err := recognizeLegacyBaseline(snapshot)
	if err != nil || baseline != 5 {
		t.Fatalf("recognizeLegacyBaseline(post-005/006) = %d, %v; want 5, nil", baseline, err)
	}
}

func TestRecognizeLegacyBaselineRejectsIndexDefinitionDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*legacyIndex)
	}{
		{name: "prefix length", mutate: func(index *legacyIndex) { index.SubPart = 191 }},
		{name: "index type", mutate: func(index *legacyIndex) { index.IndexType = "HASH" }},
		{name: "expression", mutate: func(index *legacyIndex) { index.Expression = "lower(`flag`)" }},
		{name: "invisible", mutate: func(index *legacyIndex) { index.Visible = false }},
		{name: "descending", mutate: func(index *legacyIndex) { index.Collation = "D" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := knownPost007LegacySchema()
			index := slices.IndexFunc(snapshot.Indexes, func(index legacyIndex) bool {
				return index.Table == "group_join_requests" && index.Name == "idx_group_join_requests_flag"
			})
			if index < 0 {
				t.Fatal("known post-007 schema lacks the flag index")
			}
			tt.mutate(&snapshot.Indexes[index])

			baseline, err := recognizeLegacyBaseline(snapshot)
			if baseline != 0 || !errors.Is(err, ErrLegacySchema) {
				t.Fatalf("recognizeLegacyBaseline() = %d, %v; want 0, ErrLegacySchema", baseline, err)
			}
		})
	}
}

func TestRecognizeLegacyBaselineRejectsConstraintDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*legacySchema)
	}{
		{
			name: "missing primary key",
			mutate: func(schema *legacySchema) {
				schema.Constraints = slices.DeleteFunc(schema.Constraints, func(constraint legacyConstraint) bool {
					return constraint.Table == "scheduled_jobs" && constraint.Type == "PRIMARY KEY"
				})
			},
		},
		{
			name: "missing unique",
			mutate: func(schema *legacySchema) {
				schema.Constraints = slices.DeleteFunc(schema.Constraints, func(constraint legacyConstraint) bool {
					return constraint.Name == "idx_group_join_requests_flag"
				})
			},
		},
		{
			name: "extra foreign key",
			mutate: func(schema *legacySchema) {
				schema.Constraints = append(schema.Constraints, legacyConstraint{
					Table: "scheduled_jobs", Name: "fk_unknown", Type: "FOREIGN KEY",
					Column: "group_id", Ordinal: 1, ReferencedTable: "group_join_requests", ReferencedColumn: "id",
				})
			},
		},
		{
			name: "extra check",
			mutate: func(schema *legacySchema) {
				schema.Constraints = append(schema.Constraints, legacyConstraint{
					Table: "scheduled_jobs", Name: "chk_unknown", Type: "CHECK", CheckClause: "(`group_id` > 0)",
				})
			},
		},
		{
			name: "extra unique",
			mutate: func(schema *legacySchema) {
				schema.Constraints = append(schema.Constraints, legacyConstraint{
					Table: "scheduled_jobs", Name: "uq_unknown", Type: "UNIQUE", Column: "group_id", Ordinal: 1,
				})
			},
		},
		{
			name: "extra primary key",
			mutate: func(schema *legacySchema) {
				schema.Constraints = append(schema.Constraints, legacyConstraint{
					Table: "scheduled_jobs", Name: "PRIMARY_2", Type: "PRIMARY KEY", Column: "group_id", Ordinal: 1,
				})
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot := knownPost007LegacySchema()
			tt.mutate(&snapshot)
			baseline, err := recognizeLegacyBaseline(snapshot)
			if baseline != 0 || !errors.Is(err, ErrLegacySchema) {
				t.Fatalf("recognizeLegacyBaseline() = %d, %v; want 0, ErrLegacySchema", baseline, err)
			}
		})
	}
}

func TestRecognizeLegacyBaselineRejectsConstraintsWithoutCoreTables(t *testing.T) {
	snapshot := legacySchema{Constraints: []legacyConstraint{{
		Table: "group_join_requests", Name: "PRIMARY", Type: "PRIMARY KEY", Column: "id", Ordinal: 1,
	}}}
	baseline, err := recognizeLegacyBaseline(snapshot)
	if baseline != 0 || !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("recognizeLegacyBaseline() = %d, %v; want 0, ErrLegacySchema", baseline, err)
	}
}

func TestRunnerApplyStartsEmptyDatabaseAtOne(t *testing.T) {
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), nil),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), nil),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), nil),
		execStep("CREATE TABLE core"),
		execStep("INSERT INTO `schema_migrations`"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migration := Migration{Version: 1, Name: "001_test_core", SQL: "CREATE TABLE core", Checksum: strings.Repeat("a", 64)}

	applied, err := (Runner{DB: db}).Apply(context.Background(), []Migration{migration})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(applied) != 1 || applied[0] != migration {
		t.Fatalf("applied = %+v, want 001", applied)
	}
	state.assertComplete(t)
}

func TestRunnerApplyRejectsLegacyAdoptionForUnrelatedManifest(t *testing.T) {
	migrations := make([]Migration, 8)
	for i := range migrations {
		version := i + 1
		migrations[i] = Migration{
			Version:  version,
			Name:     fmt.Sprintf("%03d_unrelated", version),
			SQL:      fmt.Sprintf("SELECT %d;", version),
			Checksum: strings.Repeat(string(rune('a'+i)), 64),
		}
	}
	snapshot := knownPost007LegacySchema()
	steps := []scriptStep{
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		beginStep(),
	}
	for range 7 {
		steps = append(steps, execStep("INSERT INTO `schema_migrations`"))
	}
	steps = append(steps,
		commitStep(),
		execStep("SELECT 8;"),
		execStep("INSERT INTO `schema_migrations`"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	db, state := newScriptDB(t, steps...)

	_, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
	}
	if got := state.countCallsContaining("INSERT INTO `schema_migrations`"); got != 0 {
		t.Fatalf("unrelated manifest recorded %d migration rows, want 0", got)
	}
}

func TestRunnerApplyAdoptsPost007LegacySchema(t *testing.T) {
	migrations := repositoryMigrations(t)
	snapshot := knownPost007LegacySchema()
	steps := []scriptStep{
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("SELECT COUNT(*) FROM `schema_migration_attempts`", []string{"count"}, [][]driver.Value{{int64(0)}}),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("COALESCE(sub_part, 0)", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		beginStep(),
	}
	for i := 0; i < 7; i++ {
		steps = append(steps, execStep("INSERT INTO `schema_migrations`"))
	}
	steps = append(steps,
		commitStep(),
		queryStep("SELECT `version`, `name`, `checksum`, `stage`", []string{"version", "name", "checksum", "stage"}, nil),
		execStep("DROP PROCEDURE IF EXISTS `jxh_guard_008`"),
		execStep("INSERT INTO `schema_migrations`"),
		execStep("DROP PROCEDURE IF EXISTS `jxh_extend_system_operations_009`"),
		execStep("INSERT INTO `schema_migrations`"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	db, state := newScriptDB(t, steps...)

	applied, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(applied) != 2 || applied[0].Version != 8 || applied[1].Version != 9 {
		t.Fatalf("applied = %+v, want 008 through 009", applied)
	}
	state.assertComplete(t)
}

func TestRunnerApplyAdoptsPost005ThenExecutes006Through008(t *testing.T) {
	migrations := repositoryMigrations(t)
	snapshot := post005LegacySchemaFixture()
	post007 := knownPost007LegacySchema()
	steps := []scriptStep{
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("SELECT COUNT(*) FROM `schema_migration_attempts`", []string{"count"}, [][]driver.Value{{int64(0)}}),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		beginStep(),
	}
	for i := 0; i < 5; i++ {
		steps = append(steps, execStep("INSERT INTO `schema_migrations`"))
	}
	steps = append(steps,
		commitStep(),
		queryStep("SELECT `version`, `name`, `checksum`, `stage`", []string{"version", "name", "checksum", "stage"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		queryStep("SELECT `name`, `checksum`, `stage`", []string{"name", "checksum", "stage"}, nil),
		execStep("INSERT INTO `schema_migration_attempts`"),
		beginStep(),
		execStep("UPDATE `group_join_requests`"),
		execStep("UPDATE `schema_migration_attempts` SET `stage`"),
		execStep("INSERT INTO `schema_migrations`"),
		execStep("DELETE FROM `schema_migration_attempts`"),
		commitStep(),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		execStep("DROP PROCEDURE IF EXISTS `jxh_guard_007`"),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(post007)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(post007)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(post007)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(post007)),
		execStep("INSERT INTO `schema_migrations`"),
		execStep("DROP PROCEDURE IF EXISTS `jxh_guard_008`"),
		execStep("INSERT INTO `schema_migrations`"),
		execStep("DROP PROCEDURE IF EXISTS `jxh_extend_system_operations_009`"),
		execStep("INSERT INTO `schema_migrations`"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	db, state := newScriptDB(t, steps...)

	applied, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(applied) != 4 || applied[0].Version != 6 || applied[1].Version != 7 || applied[2].Version != 8 || applied[3].Version != 9 {
		t.Fatalf("applied = %+v, want 006 through 009", applied)
	}
	state.assertComplete(t)
	state.assertSingleConnection(t)
}

func TestRunnerApplyRejectsPost005WhenManifestCannotReach008(t *testing.T) {
	snapshot := post005LegacySchemaFixture()
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("SELECT COUNT(*) FROM `schema_migration_attempts`", []string{"count"}, [][]driver.Value{{int64(0)}}),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migrations := repositoryMigrations(t)[:7]

	_, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
	}
	state.assertComplete(t)
}

func TestRunnerApplyRollsBackIncompleteLegacyBaseline(t *testing.T) {
	migrations := repositoryMigrations(t)
	snapshot := knownPost007LegacySchema()
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("SELECT COUNT(*) FROM `schema_migration_attempts`", []string{"count"}, [][]driver.Value{{int64(0)}}),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(snapshot)),
		queryStep("information_schema.statistics", legacyIndexNames(), legacyIndexRows(snapshot)),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(snapshot)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(snapshot)),
		beginStep(),
		execStep("INSERT INTO `schema_migrations`"),
		errorExecStep("INSERT INTO `schema_migrations`", errors.New("baseline insert failed password=hunter2")),
		rollbackStep(),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)

	applied, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if err == nil || !strings.Contains(err.Error(), "record legacy baseline 002") {
		t.Fatalf("Apply() error = %v, want baseline record failure", err)
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("Apply() leaked driver error: %v", err)
	}
	if len(applied) != 0 {
		t.Fatalf("applied = %+v, want none", applied)
	}
	state.assertComplete(t)
}

func TestRunnerApplyTrustsNonEmptyContinuousLedgerWithoutLegacyGuessing(t *testing.T) {
	migrations := []Migration{
		{Version: 1, Name: "001_first", SQL: "SELECT 1;", Checksum: strings.Repeat("a", 64)},
		{Version: 2, Name: "002_second", SQL: "SELECT 2;", Checksum: strings.Repeat("b", 64)},
	}
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, [][]driver.Value{{int64(1), "001_first", strings.Repeat("a", 64)}}),
		execStep("SELECT 2;"),
		execStep("INSERT INTO `schema_migrations`"),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)

	applied, err := (Runner{DB: db}).Apply(context.Background(), migrations)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(applied) != 1 || applied[0].Version != 2 {
		t.Fatalf("applied = %+v, want only 002", applied)
	}
	if state.countCallsContaining("information_schema.columns") != 0 {
		t.Fatal("non-empty ledger triggered legacy schema guessing")
	}
	state.assertComplete(t)
}

func TestRunnerApplyRejectsUnknownLegacySchemaWithoutBusinessDDL(t *testing.T) {
	partial := knownPost007LegacySchema()
	partial.Columns = partial.Columns[:1]
	db, state := newScriptDB(t,
		queryStep("GET_LOCK", []string{"locked"}, [][]driver.Value{{int64(1)}}),
		execStep("CREATE TABLE IF NOT EXISTS `schema_migrations`"),
		queryStep("SELECT `version`, `name`, `checksum`", []string{"version", "name", "checksum"}, nil),
		queryStep("information_schema.columns", legacyColumnNames(), legacyColumnRows(partial)),
		queryStep("information_schema.statistics", legacyIndexNames(), nil),
		queryStep("information_schema.tables", legacyTableNames(), legacyTableRows(partial)),
		queryStep("information_schema.table_constraints", legacyConstraintNames(), legacyConstraintRows(partial)),
		queryStep("RELEASE_LOCK", []string{"released"}, [][]driver.Value{{int64(1)}}),
	)
	migration := Migration{Version: 1, Name: "001_test_core", SQL: "CREATE TABLE must_not_run", Checksum: strings.Repeat("a", 64)}

	_, err := (Runner{DB: db}).Apply(context.Background(), []Migration{migration})
	if !errors.Is(err, ErrLegacySchema) {
		t.Fatalf("Apply() error = %v, want ErrLegacySchema", err)
	}
	state.assertComplete(t)
	if state.countCallsContaining("must_not_run") != 0 {
		t.Fatal("business migration executed for unknown legacy schema")
	}
}

func legacyColumnNames() []string {
	return []string{"table_name", "column_name", "ordinal_position", "column_type", "is_nullable", "collation_name", "column_default", "extra"}
}

func legacyIndexNames() []string {
	return []string{
		"table_name", "index_name", "non_unique", "seq_in_index", "column_name",
		"sub_part", "index_type", "expression", "is_visible", "collation",
	}
}

func legacyTableNames() []string {
	return []string{"table_name", "engine", "table_collation"}
}

func legacyColumnRows(schema legacySchema) [][]driver.Value {
	rows := make([][]driver.Value, 0, len(schema.Columns))
	for _, column := range schema.Columns {
		var defaultValue driver.Value = column.Default
		if column.Default == legacyNullDefault {
			defaultValue = nil
		}
		rows = append(rows, []driver.Value{
			column.Table, column.Name, int64(column.Ordinal), column.Type,
			map[bool]string{true: "YES", false: "NO"}[column.Nullable], column.Collation, defaultValue, column.Extra,
		})
	}
	return rows
}

func legacyTableRows(schema legacySchema) [][]driver.Value {
	rows := make([][]driver.Value, 0, len(schema.Tables))
	for _, table := range schema.Tables {
		rows = append(rows, []driver.Value{table.Name, table.Engine, table.Collation})
	}
	return rows
}

func legacyIndexRows(schema legacySchema) [][]driver.Value {
	rows := make([][]driver.Value, 0, len(schema.Indexes))
	for _, index := range schema.Indexes {
		nonUnique := int64(1)
		if index.Unique {
			nonUnique = 0
		}
		visibility := "NO"
		if index.Visible {
			visibility = "YES"
		}
		rows = append(rows, []driver.Value{
			index.Table, index.Name, nonUnique, int64(index.Sequence), index.Column,
			int64(index.SubPart), index.IndexType, index.Expression, visibility, index.Collation,
		})
	}
	return rows
}

func legacyConstraintNames() []string {
	return []string{
		"table_name", "constraint_name", "constraint_type", "ordinal_position", "column_name",
		"referenced_table_name", "referenced_column_name", "check_clause",
	}
}

func legacyConstraintRows(schema legacySchema) [][]driver.Value {
	rows := make([][]driver.Value, 0, len(schema.Constraints))
	for _, constraint := range schema.Constraints {
		rows = append(rows, []driver.Value{
			constraint.Table, constraint.Name, constraint.Type, int64(constraint.Ordinal), constraint.Column,
			constraint.ReferencedTable, constraint.ReferencedColumn, constraint.CheckClause,
		})
	}
	return rows
}

func post005LegacySchemaFixture() legacySchema {
	schema := knownPost007LegacySchema()
	for i := range schema.Columns {
		if schema.Columns[i].Table == "group_join_requests" && schema.Columns[i].Ordinal > 2 {
			schema.Columns[i].Ordinal++
		}
	}
	systemRequestID := legacyColumn{
		Table:     "group_join_requests",
		Name:      "system_request_id",
		Ordinal:   3,
		Type:      "varchar(64)",
		Nullable:  true,
		Collation: "utf8mb4_bin",
		Default:   legacyNullDefault,
	}
	schema.Columns = append(schema.Columns, systemRequestID)
	schema.Indexes = append(schema.Indexes, legacyIndex{
		Table: "group_join_requests", Name: "idx_group_join_requests_system_request_id",
		Column: "system_request_id", Sequence: 1, Unique: true,
		IndexType: "BTREE", Visible: true, Collation: "A",
	})
	schema.Constraints = append(schema.Constraints, legacyConstraint{
		Table: "group_join_requests", Name: "idx_group_join_requests_system_request_id", Type: "UNIQUE",
		Column: "system_request_id", Ordinal: 1,
	})
	return schema
}

func writeMigration(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o600); err != nil {
		t.Fatalf("write migration: %v", err)
	}
}

type scriptStep struct {
	kind      string
	contains  string
	columns   []string
	rows      [][]driver.Value
	err       error
	rowsErr   error
	closeRows bool
}

func queryStep(contains string, columns []string, rows [][]driver.Value) scriptStep {
	return scriptStep{kind: "query", contains: contains, columns: columns, rows: rows}
}

func queryErrorStep(contains string, err error) scriptStep {
	return scriptStep{kind: "query", contains: contains, err: err}
}

func queryStepRequiringClose(contains string, columns []string, rows [][]driver.Value) scriptStep {
	return scriptStep{kind: "query", contains: contains, columns: columns, rows: rows, closeRows: true}
}

func queryRowsErrorStepRequiringClose(contains string, columns []string, err error) scriptStep {
	return scriptStep{kind: "query", contains: contains, columns: columns, rowsErr: err, closeRows: true}
}

func rowsCloseStep(contains string) scriptStep {
	return scriptStep{kind: "rows_close", contains: contains}
}

func execStep(contains string) scriptStep {
	return scriptStep{kind: "exec", contains: contains}
}

func errorExecStep(contains string, err error) scriptStep {
	return scriptStep{kind: "exec", contains: contains, err: err}
}

func beginStep() scriptStep    { return scriptStep{kind: "begin"} }
func commitStep() scriptStep   { return scriptStep{kind: "commit"} }
func rollbackStep() scriptStep { return scriptStep{kind: "rollback"} }

type scriptCall struct {
	connID int
	kind   string
	query  string
	args   []driver.NamedValue
}

type scriptState struct {
	mu      sync.Mutex
	steps   []scriptStep
	calls   []scriptCall
	nextID  int
	openIDs map[int]struct{}
}

var scriptDriverID atomic.Uint64

func newScriptDB(t *testing.T, steps ...scriptStep) (*sql.DB, *scriptState) {
	t.Helper()
	state := &scriptState{steps: append([]scriptStep(nil), steps...), openIDs: make(map[int]struct{})}
	name := fmt.Sprintf("migration-script-%d", scriptDriverID.Add(1))
	sql.Register(name, &scriptDriver{state: state})
	db, err := sql.Open(name, "unused")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, state
}

func (s *scriptState) take(connID int, kind, query string, args []driver.NamedValue) (scriptStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, scriptCall{connID: connID, kind: kind, query: query, args: append([]driver.NamedValue(nil), args...)})
	if len(s.steps) == 0 {
		return scriptStep{}, fmt.Errorf("unexpected %s: %s", kind, query)
	}
	step := s.steps[0]
	if step.kind != kind || !strings.Contains(query, step.contains) {
		return scriptStep{}, fmt.Errorf("got %s %q, want %s containing %q", kind, query, step.kind, step.contains)
	}
	s.steps = s.steps[1:]
	return step, nil
}

func (s *scriptState) assertComplete(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) != 0 {
		t.Fatalf("%d scripted database steps were not consumed: %+v", len(s.steps), s.steps)
	}
}

func (s *scriptState) assertSingleConnection(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	var id int
	for i, call := range s.calls {
		if i == 0 {
			id = call.connID
		}
		if call.connID != id {
			t.Fatalf("database calls used connections %d and %d", id, call.connID)
		}
	}
}

func (s *scriptState) assertLastCallUsesNewConnection(t *testing.T) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) < 2 {
		t.Fatalf("database calls = %d, want at least 2", len(s.calls))
	}
	if s.calls[0].connID == s.calls[len(s.calls)-1].connID {
		t.Fatalf("last database call reused discarded connection %d", s.calls[0].connID)
	}
}

func (s *scriptState) countCallsContaining(fragment string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, call := range s.calls {
		if strings.Contains(call.query, fragment) {
			count++
		}
	}
	return count
}

type scriptDriver struct{ state *scriptState }

func (d *scriptDriver) Open(string) (driver.Conn, error) {
	d.state.mu.Lock()
	defer d.state.mu.Unlock()
	d.state.nextID++
	id := d.state.nextID
	d.state.openIDs[id] = struct{}{}
	return &scriptConn{state: d.state, id: id}, nil
}

type scriptConn struct {
	state *scriptState
	id    int
}

func (c *scriptConn) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *scriptConn) Close() error                        { return nil }
func (c *scriptConn) Begin() (driver.Tx, error)           { return c.begin() }

func (c *scriptConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	return c.begin()
}

func (c *scriptConn) begin() (driver.Tx, error) {
	step, err := c.state.take(c.id, "begin", "", nil)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return &scriptTx{conn: c}, nil
}

func (c *scriptConn) ExecContext(_ context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if strings.Contains(query, "CREATE TABLE IF NOT EXISTS `schema_migration_attempts`") {
		return driver.RowsAffected(1), nil
	}
	step, err := c.state.take(c.id, "exec", query, args)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return driver.RowsAffected(1), nil
}

func (c *scriptConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if step, ok := automaticMigrationMetadataQuery(query, args); ok {
		return &scriptRows{columns: step.columns, rows: step.rows}, nil
	}
	step, err := c.state.take(c.id, "query", query, args)
	if err != nil {
		return nil, err
	}
	if step.err != nil {
		return nil, step.err
	}
	return &scriptRows{
		columns: step.columns, rows: step.rows, rowsErr: step.rowsErr,
		state: c.state, connID: c.id, query: query, requireClose: step.closeRows,
	}, nil
}

func automaticMigrationMetadataQuery(query string, args []driver.NamedValue) (scriptStep, bool) {
	if strings.Contains(query, "SELECT table_name, table_type") && len(args) == 0 {
		return queryStep("", []string{"table_name", "table_type"}, [][]driver.Value{
			{"schema_migration_attempts", "BASE TABLE"},
			{"schema_migrations", "BASE TABLE"},
		}), true
	}
	if len(args) != 1 {
		return scriptStep{}, false
	}
	table, ok := args[0].Value.(string)
	if !ok {
		return scriptStep{}, false
	}
	var schema legacySchema
	switch table {
	case "schema_migrations":
		schema = migrationLedgerSchema()
	case "schema_migration_attempts":
		schema = migrationAttemptLedgerSchema()
	default:
		return scriptStep{}, false
	}
	switch {
	case strings.Contains(query, "information_schema.columns"):
		return queryStep("", legacyColumnNames(), legacyColumnRows(schema)), true
	case strings.Contains(query, "information_schema.statistics"):
		return queryStep("", legacyIndexNames(), legacyIndexRows(schema)), true
	case strings.Contains(query, "information_schema.tables"):
		return queryStep("", legacyTableNames(), legacyTableRows(schema)), true
	case strings.Contains(query, "information_schema.table_constraints"):
		return queryStep("", legacyConstraintNames(), legacyConstraintRows(schema)), true
	default:
		return scriptStep{}, false
	}
}

type scriptTx struct{ conn *scriptConn }

func (tx *scriptTx) Commit() error {
	step, err := tx.conn.state.take(tx.conn.id, "commit", "", nil)
	if err != nil {
		return err
	}
	return step.err
}

func (tx *scriptTx) Rollback() error {
	step, err := tx.conn.state.take(tx.conn.id, "rollback", "", nil)
	if err != nil {
		return err
	}
	return step.err
}

type scriptRows struct {
	columns      []string
	rows         [][]driver.Value
	rowsErr      error
	index        int
	state        *scriptState
	connID       int
	query        string
	requireClose bool
	closed       bool
}

func (r *scriptRows) Columns() []string { return r.columns }
func (r *scriptRows) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if !r.requireClose {
		return nil
	}
	step, err := r.state.take(r.connID, "rows_close", r.query, nil)
	if err != nil {
		return err
	}
	return step.err
}
func (r *scriptRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		if r.rowsErr != nil {
			err := r.rowsErr
			r.rowsErr = nil
			return err
		}
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}
