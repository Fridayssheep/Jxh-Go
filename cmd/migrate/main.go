package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/database"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	migrationsDir := flag.String("dir", "deploy/mysql/migrations", "path to migrations directory")
	flag.Parse()

	if err := run(*configPath, *migrationsDir); err != nil {
		log.Fatalf("migration failed: %v", err)
	}
}

func run(configPath, migrationsDir string) error {
	// Load config
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Load migrations
	migrations, err := database.LoadMigrations(migrationsDir)
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}

	if len(migrations) == 0 {
		log.Println("no migrations found")
		return nil
	}

	// Build migration DSN
	dsn, err := database.MigrationDSN(cfg.Database)
	if err != nil {
		return fmt.Errorf("build DSN: %w", err)
	}

	// Open database connection
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	// Test connection
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping database: %w", err)
	}

	// Run migrations
	runner := database.Runner{
		DB:          db,
		LockTimeout: 30 * time.Second,
	}

	log.Printf("applying %d migrations...", len(migrations))

	executed, err := runner.Apply(ctx, migrations)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	if len(executed) == 0 {
		log.Println("database is up to date")
	} else {
		log.Printf("successfully applied %d migrations:", len(executed))
		for _, m := range executed {
			log.Printf("  %03d %s", m.Version, m.Name)
		}
	}

	return nil
}
