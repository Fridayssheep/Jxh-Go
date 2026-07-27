package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/config"
)

var (
	ErrManifest        = errors.New("invalid migration manifest")
	ErrMigrationSequence = errors.New("invalid migration sequence")
	ErrLock            = errors.New("migration lock unavailable")
	ErrDrift           = errors.New("migration drift")
	ErrLegacySchema    = errors.New("unrecognized legacy schema")
)

var (
	migrationFilename = regexp.MustCompile(`^([0-9]{3})_([a-z0-9_]+)\.sql$`)
)

const migrationLockName = "jxh_manager_migrations"

type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

type Runner struct {
	DB          *sql.DB
	LockTimeout time.Duration
}

// LoadMigrations reads all migration files from dir and returns them sorted by version.
func LoadMigrations(dir string) ([]Migration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: cannot read migration directory: %v", ErrManifest, err)
	}

	var migrations []Migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		matches := migrationFilename.FindStringSubmatch(entry.Name())
		if matches == nil {
			continue
		}

		version, _ := strconv.Atoi(matches[1])
		name := matches[1] + "_" + matches[2]

		content, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: cannot read %s: %v", ErrManifest, entry.Name(), err)
		}

		if len(content) == 0 {
			return nil, fmt.Errorf("%w: %s is empty", ErrManifest, entry.Name())
		}

		hash := sha256.Sum256(content)
		checksum := hex.EncodeToString(hash[:])

		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			SQL:      string(content),
			Checksum: checksum,
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})

	// Validate sequence: must start at 1 and be contiguous
	for i, m := range migrations {
		expected := i + 1
		if m.Version != expected {
			return nil, fmt.Errorf("%w: expected version %d, got %d", ErrMigrationSequence, expected, m.Version)
		}
	}

	return migrations, nil
}

// Apply executes pending migrations under a database lock.
func (r Runner) Apply(ctx context.Context, migrations []Migration) ([]Migration, error) {
	if r.DB == nil {
		return nil, fmt.Errorf("database connection is nil")
	}

	conn, err := r.DB.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Close()

	// Acquire migration lock
	lockTimeout := r.LockTimeout
	if lockTimeout == 0 {
		lockTimeout = 30 * time.Second
	}
	lockSeconds := int(lockTimeout.Seconds())

	var locked sql.NullInt64
	err = conn.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", migrationLockName, lockSeconds).Scan(&locked)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("acquire migration lock: %w", err)
		}
		return nil, fmt.Errorf("%w: query GET_LOCK failed: %v", ErrLock, err)
	}
	if !locked.Valid || locked.Int64 != 1 {
		return nil, fmt.Errorf("%w: lock not granted", ErrLock)
	}
	defer releaseMigrationLock(conn)

	// Create schema_migrations table if not exists
	_, err = conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+"`schema_migrations`"+` (
  `+"`version`"+` int unsigned NOT NULL,
  `+"`name`"+` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
  `+"`checksum`"+` char(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  `+"`applied_at`"+` datetime(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (`+"`version`"+`),
  UNIQUE KEY `+"`uq_schema_migrations_name`"+` (`+"`name`"+`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`)
	if err != nil {
		return nil, fmt.Errorf("create schema_migrations table: %w", err)
	}

	// Load already applied migrations
	rows, err := conn.QueryContext(ctx, "SELECT `version`, `name`, `checksum` FROM `schema_migrations` ORDER BY `version`")
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}

	var applied []Migration
	for rows.Next() {
		var m Migration
		if err := rows.Scan(&m.Version, &m.Name, &m.Checksum); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied = append(applied, m)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}

	// Validate applied migrations match manifest (detect drift)
	for i, appliedMig := range applied {
		if i >= len(migrations) {
			return nil, fmt.Errorf("%w: applied version %d not in manifest", ErrDrift, appliedMig.Version)
		}
		manifestMig := migrations[i]
		if appliedMig.Version != manifestMig.Version || appliedMig.Name != manifestMig.Name {
			return nil, fmt.Errorf("%w: version %d name mismatch: applied=%s manifest=%s", ErrDrift, appliedMig.Version, appliedMig.Name, manifestMig.Name)
		}
		if appliedMig.Checksum != manifestMig.Checksum {
			return nil, fmt.Errorf("%w: version %d checksum mismatch", ErrDrift, appliedMig.Version)
		}
	}

	// Execute pending migrations
	var executed []Migration
	for i := len(applied); i < len(migrations); i++ {
		m := migrations[i]
		if err := executeMigration(ctx, conn, m); err != nil {
			return executed, err
		}
		executed = append(executed, m)
	}

	return executed, nil
}

func executeMigration(ctx context.Context, conn *sql.Conn, m Migration) error {
	// Execute the migration SQL
	_, err := conn.ExecContext(ctx, m.SQL)
	if err != nil {
		return fmt.Errorf("execute migration %d (%s): %w", m.Version, m.Name, err)
	}

	// Record in ledger
	_, err = conn.ExecContext(ctx, "INSERT INTO `schema_migrations` (`version`, `name`, `checksum`) VALUES (?, ?, ?)", m.Version, m.Name, m.Checksum)
	if err != nil {
		return fmt.Errorf("record migration %d in ledger: %w", m.Version, err)
	}

	return nil
}

func releaseMigrationLock(conn *sql.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn.QueryRowContext(ctx, "SELECT RELEASE_LOCK(?)", migrationLockName).Scan(new(sql.NullInt64))
}

// MigrationDSN builds a DSN with multiStatements=true for migration runner
func MigrationDSN(cfg config.DatabaseConfig) (string, error) {
	if cfg.DSN != "" {
		parsed, err := drivermysql.ParseDSN(cfg.DSN)
		if err != nil {
			return "", fmt.Errorf("parse DSN: %w", err)
		}
		parsed.MultiStatements = true
		return parsed.FormatDSN(), nil
	}

	dsn := drivermysql.NewConfig()
	dsn.User = cfg.User
	dsn.Passwd = cfg.Password
	dsn.Net = "tcp"
	dsn.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	dsn.DBName = cfg.Name
	dsn.Params = map[string]string{
		"charset":   cfg.Charset,
		"parseTime": "True",
		"loc":       cfg.Loc,
	}
	dsn.MultiStatements = true

	return dsn.FormatDSN(), nil
}
