package database

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/config"
)

func TestBuildRuntimeDSNDisablesMultiStatements(t *testing.T) {
	cfg := config.Default().Database
	cfg.DSN = "user:secret@tcp(database:3306)/jxh?charset=utf8mb4&parseTime=true&multiStatements=true"
	dsn, err := BuildRuntimeDSN(cfg)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.MultiStatements {
		t.Fatal("runtime DSN retained multiStatements")
	}
	if !parsed.ParseTime {
		t.Fatal("runtime DSN lost caller options")
	}
}

func TestBuildRuntimeDSNReturnsSafeErrors(t *testing.T) {
	secret := "database-password-should-not-leak"
	_, err := BuildRuntimeDSN(config.DatabaseConfig{DSN: "user:" + secret + "@not-a-valid-dsn%%%"})
	if err == nil {
		t.Fatal("BuildRuntimeDSN() error=nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestOpenGORMRejectsInvalidPoolBeforeConnecting(t *testing.T) {
	cfg := config.Default().Database
	cfg.MaxOpenConns = 0
	if _, err := OpenGORM(t.Context(), cfg); err != ErrInvalidRuntimeConfig {
		t.Fatalf("error=%v", err)
	}
}
