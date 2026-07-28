package database

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/zjutjh/jxh-go/internal/platform/config"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var ErrInvalidRuntimeConfig = errors.New("invalid database runtime configuration")

func BuildRuntimeDSN(cfg config.DatabaseConfig) (string, error) {
	driverConfig, err := buildDriverConfig(cfg)
	if err != nil {
		return "", err
	}
	driverConfig.MultiStatements = false
	return driverConfig.FormatDSN(), nil
}

func OpenGORM(ctx context.Context, cfg config.DatabaseConfig) (*gorm.DB, error) {
	timing, err := validateRuntimeConfiguration(ctx, cfg)
	if err != nil {
		return nil, ErrInvalidRuntimeConfig
	}
	dsn, err := BuildRuntimeDSN(cfg)
	if err != nil {
		return nil, err
	}
	return openGORMWithDialector(ctx, cfg, timing, mysql.Open(dsn))
}

type runtimeTiming struct {
	connectionLifetime time.Duration
	connectionIdleTime time.Duration
	pingTimeout        time.Duration
}

func validateRuntimeConfiguration(ctx context.Context, cfg config.DatabaseConfig) (runtimeTiming, error) {
	if ctx == nil || cfg.MaxOpenConns <= 0 || cfg.MaxIdleConns < 0 || cfg.MaxIdleConns > cfg.MaxOpenConns {
		return runtimeTiming{}, ErrInvalidRuntimeConfig
	}
	lifetime, lifetimeOK := runtimeSeconds(cfg.ConnMaxLifetimeSeconds)
	idleTime, idleOK := runtimeSeconds(cfg.ConnMaxIdleTimeSeconds)
	pingTimeout, pingOK := runtimeSeconds(cfg.PingTimeoutSeconds)
	if !lifetimeOK || !idleOK || !pingOK {
		return runtimeTiming{}, ErrInvalidRuntimeConfig
	}
	return runtimeTiming{
		connectionLifetime: lifetime, connectionIdleTime: idleTime, pingTimeout: pingTimeout,
	}, nil
}

func runtimeSeconds(value int) (time.Duration, bool) {
	if value <= 0 || int64(value) > math.MaxInt64/int64(time.Second) {
		return 0, false
	}
	return time.Duration(value) * time.Second, true
}

func openGORMWithDialector(
	ctx context.Context,
	cfg config.DatabaseConfig,
	timing runtimeTiming,
	dialector gorm.Dialector,
) (*gorm.DB, error) {
	if dialector == nil {
		return nil, ErrInvalidRuntimeConfig
	}
	db, err := gorm.Open(dialector, &gorm.Config{Logger: logger.Discard, DisableAutomaticPing: true})
	if err != nil {
		if db != nil {
			if closer, ok := db.ConnPool.(interface{ Close() error }); ok {
				_ = closer.Close()
			}
		}
		return nil, errors.New("open database: database operation failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		if closer, ok := db.ConnPool.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		return nil, errors.New("access database pool: database operation failed")
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(timing.connectionLifetime)
	sqlDB.SetConnMaxIdleTime(timing.connectionIdleTime)

	pingCtx, cancel := context.WithTimeout(ctx, timing.pingTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("ping database: %w", err)
		}
		return nil, errors.New("ping database: database operation failed")
	}
	return db, nil
}
