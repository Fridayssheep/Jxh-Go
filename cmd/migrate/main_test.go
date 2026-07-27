package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/database"
)

func TestRunWithDepsAppliesRequestedManifestAndPrintsSafeResult(t *testing.T) {
	wantMigrations := []database.Migration{
		{Version: 1, Name: "001_create_core_schema", SQL: "SELECT secret", Checksum: strings.Repeat("a", 64)},
		{Version: 2, Name: "002_next", SQL: "SELECT password", Checksum: strings.Repeat("b", 64)},
	}
	var gotConfigPath, gotDir string
	deps := commandDeps{
		loadConfig: func(path string) (config.Config, error) {
			gotConfigPath = path
			return config.Config{Database: config.DatabaseConfig{Password: "hunter2"}}, nil
		},
		loadMigrations: func(dir string) ([]database.Migration, error) {
			gotDir = dir
			return wantMigrations, nil
		},
		migrate: func(_ context.Context, cfg config.Config, migrations []database.Migration) ([]database.Migration, error) {
			if cfg.Database.Password != "hunter2" {
				t.Fatalf("database config was not passed to migrate")
			}
			if len(migrations) != 2 || migrations[1].Version != 2 {
				t.Fatalf("migrations = %+v", migrations)
			}
			return migrations[1:], nil
		},
	}
	var stdout, stderr bytes.Buffer

	err := runWithDeps(context.Background(), []string{"-config", "custom.yaml", "-dir", "custom-migrations"}, &stdout, &stderr, deps)
	if err != nil {
		t.Fatalf("runWithDeps() error = %v", err)
	}
	if gotConfigPath != "custom.yaml" || gotDir != "custom-migrations" {
		t.Fatalf("paths = %q, %q", gotConfigPath, gotDir)
	}
	if stdout.String() != "applied 002 002_next\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if strings.Contains(stdout.String(), "hunter2") || strings.Contains(stdout.String(), "SELECT") {
		t.Fatalf("stdout leaked sensitive content: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunWithDepsIsSilentWhenSchemaIsCurrent(t *testing.T) {
	deps := commandDeps{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		loadMigrations: func(string) ([]database.Migration, error) {
			return []database.Migration{{Version: 1, Name: "001_first"}}, nil
		},
		migrate: func(context.Context, config.Config, []database.Migration) ([]database.Migration, error) {
			return nil, nil
		},
	}
	var stdout, stderr bytes.Buffer

	if err := runWithDeps(context.Background(), nil, &stdout, &stderr, deps); err != nil {
		t.Fatalf("runWithDeps() error = %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunWithDepsSanitizesMigrationError(t *testing.T) {
	deps := commandDeps{
		loadConfig: func(string) (config.Config, error) {
			return config.Config{Database: config.DatabaseConfig{DSN: "user:hunter2@tcp(db)/jxh"}}, nil
		},
		loadMigrations: func(string) ([]database.Migration, error) {
			return []database.Migration{{Version: 1, Name: "001_secret", SQL: "SELECT secret"}}, nil
		},
		migrate: func(context.Context, config.Config, []database.Migration) ([]database.Migration, error) {
			return nil, errors.New("driver failed for user:hunter2 while running SELECT secret")
		},
	}
	var stdout, stderr bytes.Buffer

	err := runWithDeps(context.Background(), nil, &stdout, &stderr, deps)
	if err == nil {
		t.Fatal("runWithDeps() error = nil")
	}
	if err.Error() != "apply migrations: database operation failed" {
		t.Fatalf("error = %q", err)
	}
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "SELECT secret") {
		t.Fatalf("error leaked sensitive details: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("unexpected output stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunWithDepsPreservesTypedMigrationCategory(t *testing.T) {
	deps := commandDeps{
		loadConfig: func(string) (config.Config, error) { return config.Config{}, nil },
		loadMigrations: func(string) ([]database.Migration, error) {
			return []database.Migration{{Version: 1, Name: "001_first"}}, nil
		},
		migrate: func(context.Context, config.Config, []database.Migration) ([]database.Migration, error) {
			return nil, database.ErrDrift
		},
	}

	err := runWithDeps(context.Background(), nil, &bytes.Buffer{}, &bytes.Buffer{}, deps)
	if !errors.Is(err, database.ErrDrift) {
		t.Fatalf("error = %v, want errors.Is(..., ErrDrift)", err)
	}
	if err.Error() != "apply migrations: migration drift" {
		t.Fatalf("error = %q", err)
	}
}

func TestRunWithDepsTreatsHelpAsSuccess(t *testing.T) {
	called := false
	deps := commandDeps{
		loadConfig: func(string) (config.Config, error) {
			called = true
			return config.Config{}, nil
		},
		loadMigrations: func(string) ([]database.Migration, error) {
			called = true
			return nil, nil
		},
		migrate: func(context.Context, config.Config, []database.Migration) ([]database.Migration, error) {
			called = true
			return nil, nil
		},
	}
	var stdout, stderr bytes.Buffer

	if err := runWithDeps(context.Background(), []string{"-h"}, &stdout, &stderr, deps); err != nil {
		t.Fatalf("runWithDeps(-h) error = %v", err)
	}
	if called {
		t.Fatal("runWithDeps(-h) called migration dependencies")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "Usage of migrate:") || strings.Contains(stderr.String(), "migration failed") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
