package database

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zjutjh/jxh-go/internal/config"
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
	if ctx == nil || cfg.MaxOpenConns <= 0 || cfg.MaxIdleConns < 0 || cfg.MaxIdleConns > cfg.MaxOpenConns ||
		cfg.ConnMaxLifetimeSeconds <= 0 || cfg.ConnMaxIdleTimeSeconds <= 0 || cfg.PingTimeoutSeconds <= 0 {
		return nil, ErrInvalidRuntimeConfig
	}
	dsn, err := BuildRuntimeDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		return nil, errors.New("open database: database operation failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, errors.New("access database pool: database operation failed")
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetimeSeconds) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(cfg.ConnMaxIdleTimeSeconds) * time.Second)

	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.PingTimeoutSeconds)*time.Second)
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
