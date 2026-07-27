package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/database"
)

type commandDeps struct {
	loadConfig     func(string) (config.Config, error)
	loadMigrations func(string) ([]database.Migration, error)
	migrate        func(context.Context, config.Config, []database.Migration) ([]database.Migration, error)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "migration failed: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	return runWithDeps(ctx, args, stdout, stderr, commandDeps{
		loadConfig:     config.Load,
		loadMigrations: database.LoadMigrations,
		migrate:        migrateDatabase,
	})
}

func runWithDeps(ctx context.Context, args []string, stdout, stderr io.Writer, deps commandDeps) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "config.yaml", "path to config file")
	migrationDir := flags.String("dir", "deploy/mysql/migrations", "path to migration directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return fmt.Errorf("parse flags: %w", err)
	}
	if flags.NArg() != 0 {
		return errors.New("parse flags: unexpected positional arguments")
	}

	cfg, err := deps.loadConfig(*configPath)
	if err != nil {
		return errors.New("load config: configuration is unavailable or invalid")
	}
	migrations, err := deps.loadMigrations(*migrationDir)
	if err != nil {
		return safeManifestError(err)
	}
	applied, err := deps.migrate(ctx, cfg, migrations)
	if err != nil {
		return safeMigrationError(err)
	}
	if len(applied) == 0 {
		return nil
	}
	for _, migration := range applied {
		if _, err := fmt.Fprintf(stdout, "applied %03d %s\n", migration.Version, migration.Name); err != nil {
			return fmt.Errorf("write result: %w", err)
		}
	}
	return nil
}

func migrateDatabase(ctx context.Context, cfg config.Config, migrations []database.Migration) ([]database.Migration, error) {
	dsn, err := database.BuildMigrationDSN(cfg.Database)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, errors.New("open database: database operation failed")
	}
	defer db.Close()
	db.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	db.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	db.SetConnMaxLifetime(time.Duration(cfg.Database.ConnMaxLifetimeSeconds) * time.Second)
	db.SetConnMaxIdleTime(time.Duration(cfg.Database.ConnMaxIdleTimeSeconds) * time.Second)

	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.PingTimeoutSeconds)*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("ping database: %w", err)
		}
		return nil, errors.New("ping database: database operation failed")
	}
	return (database.Runner{DB: db, LockTimeout: 10 * time.Second}).Apply(ctx, migrations)
}

func safeManifestError(err error) error {
	switch {
	case errors.Is(err, database.ErrSequence):
		return fmt.Errorf("load migrations: %w", database.ErrSequence)
	case errors.Is(err, database.ErrManifest):
		return fmt.Errorf("load migrations: %w", database.ErrManifest)
	default:
		return errors.New("load migrations: manifest unavailable")
	}
}

func safeMigrationError(err error) error {
	switch {
	case errors.Is(err, database.ErrLock):
		return fmt.Errorf("apply migrations: %w", database.ErrLock)
	case errors.Is(err, database.ErrDrift):
		return fmt.Errorf("apply migrations: %w", database.ErrDrift)
	case errors.Is(err, database.ErrLegacySchema):
		return fmt.Errorf("apply migrations: %w", database.ErrLegacySchema)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("apply migrations: %w", context.Canceled)
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("apply migrations: %w", context.DeadlineExceeded)
	default:
		return errors.New("apply migrations: database operation failed")
	}
}
