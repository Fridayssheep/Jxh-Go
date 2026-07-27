package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMigrationsRequiresContiguousImmutableVersions(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "001_first.sql", "SELECT 1;")
	writeMigration(t, dir, "003_gap.sql", "SELECT 3;")
	_, err := LoadMigrations(dir)
	if !errors.Is(err, ErrMigrationSequence) {
		t.Fatalf("got %v, want ErrMigrationSequence", err)
	}
}

func TestLoadMigrationsComputesStableSHA256(t *testing.T) {
	dir := t.TempDir()
	writeMigration(t, dir, "001_first.sql", "SELECT 1;\n")
	migrations, err := LoadMigrations(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrations[0].Version != 1 || len(migrations[0].Checksum) != 64 {
		t.Fatalf("got %+v", migrations[0])
	}
}

func writeMigration(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}
