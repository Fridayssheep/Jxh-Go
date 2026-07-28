package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/platform/config"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
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

func TestOpenGORMUsesOneBoundedPingAndAppliesPoolLimit(t *testing.T) {
	cfg := config.Default().Database
	timing, err := validateRuntimeConfiguration(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := &runtimeDriverState{}
	dialector := newRuntimeDialector(t, state)
	db, err := openGORMWithDialector(t.Context(), cfg, timing, dialector)
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if got := state.pings.Load(); got != 1 {
		t.Fatalf("database pings=%d, want 1", got)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != cfg.MaxOpenConns {
		t.Fatalf("max open connections=%d, want %d", got, cfg.MaxOpenConns)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOpenGORMClosesPoolAndSanitizesPingFailure(t *testing.T) {
	cfg := config.Default().Database
	timing, err := validateRuntimeConfiguration(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	secret := "database-password"
	state := &runtimeDriverState{ping: func(context.Context) error { return errors.New(secret) }}
	_, err = openGORMWithDialector(t.Context(), cfg, timing, newRuntimeDialector(t, state))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("openGORMWithDialector() error=%v", err)
	}
	if state.pings.Load() != 1 || state.closes.Load() != 1 {
		t.Fatalf("pings=%d closes=%d, want 1 and 1", state.pings.Load(), state.closes.Load())
	}
}

func TestOpenGORMPropagatesPingTimeoutAndClosesPool(t *testing.T) {
	cfg := config.Default().Database
	cfg.PingTimeoutSeconds = 1
	timing, err := validateRuntimeConfiguration(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := &runtimeDriverState{ping: func(ctx context.Context) error {
		<-ctx.Done()
		return context.Cause(ctx)
	}}
	startedAt := time.Now()
	_, err = openGORMWithDialector(t.Context(), cfg, timing, newRuntimeDialector(t, state))
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(startedAt) > 2*time.Second {
		t.Fatalf("openGORMWithDialector() error=%v duration=%s", err, time.Since(startedAt))
	}
	if state.pings.Load() != 1 || state.closes.Load() != 1 {
		t.Fatalf("pings=%d closes=%d, want 1 and 1", state.pings.Load(), state.closes.Load())
	}
}

func TestOpenGORMPropagatesParentCancellationBeforeConnecting(t *testing.T) {
	cfg := config.Default().Database
	timing, err := validateRuntimeConfiguration(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	state := &runtimeDriverState{ping: func(ctx context.Context) error {
		<-ctx.Done()
		return context.Cause(ctx)
	}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = openGORMWithDialector(ctx, cfg, timing, newRuntimeDialector(t, state))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("openGORMWithDialector() error=%v", err)
	}
	if state.opens.Load() != 0 || state.pings.Load() != 0 || state.closes.Load() != 0 {
		t.Fatalf("opens=%d pings=%d closes=%d, want all zero", state.opens.Load(), state.pings.Load(), state.closes.Load())
	}
}

func TestOpenGORMRejectsDurationOverflowBeforeConnecting(t *testing.T) {
	cfg := config.Default().Database
	cfg.ConnMaxLifetimeSeconds = int((int64(^uint64(0)>>1) / int64(time.Second)) + 1)
	if _, err := OpenGORM(t.Context(), cfg); !errors.Is(err, ErrInvalidRuntimeConfig) {
		t.Fatalf("OpenGORM() error=%v", err)
	}
}

var runtimeDriverID atomic.Uint64

type runtimeDriverState struct {
	opens  atomic.Int64
	closes atomic.Int64
	pings  atomic.Int64
	ping   func(context.Context) error
}

type runtimeDriver struct {
	state *runtimeDriverState
}

func (d *runtimeDriver) Open(string) (driver.Conn, error) {
	d.state.opens.Add(1)
	return &runtimeConnection{state: d.state}, nil
}

type runtimeConnection struct {
	state *runtimeDriverState
}

func (c *runtimeConnection) Prepare(string) (driver.Stmt, error) { return nil, driver.ErrSkip }
func (c *runtimeConnection) Begin() (driver.Tx, error)           { return nil, driver.ErrSkip }

func (c *runtimeConnection) Close() error {
	c.state.closes.Add(1)
	return nil
}

func (c *runtimeConnection) Ping(ctx context.Context) error {
	c.state.pings.Add(1)
	if c.state.ping != nil {
		return c.state.ping(ctx)
	}
	return nil
}

func newRuntimeDialector(t *testing.T, state *runtimeDriverState) gorm.Dialector {
	t.Helper()
	driverName := fmt.Sprintf("database-runtime-%d", runtimeDriverID.Add(1))
	sql.Register(driverName, &runtimeDriver{state: state})
	sqlDB, err := sql.Open(driverName, "unused")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true})
}
