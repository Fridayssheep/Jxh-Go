package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/zjutjh/jxh-go/internal/platform/config"
	"github.com/zjutjh/jxh-go/internal/platform/database"
	"github.com/zjutjh/jxh-go/internal/platform/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runCommand(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "admin bootstrap failed: %v\n", err)
		os.Exit(1)
	}
}

func runCommand(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	configPath, help, err := bootstrapConfigPath(args)
	if err != nil {
		return fmt.Errorf("parse bootstrap flags: %w", err)
	}
	if help {
		return run(ctx, args, stdin, stdout, nil)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return errors.New("load config: configuration is unavailable or invalid")
	}
	db, err := database.OpenGORM(ctx, cfg.Database)
	if err != nil {
		return errors.New("open database: database operation failed")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.New("access database pool: database operation failed")
	}
	defer sqlDB.Close()
	return run(ctx, args, stdin, stdout, storage.NewStore(db))
}

func bootstrapConfigPath(args []string) (string, bool, error) {
	flags := flag.NewFlagSet("admin-bootstrap", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "config.yaml", "")
	flags.String("username", "", "")
	flags.String("display-name", "", "")
	flags.Bool("password-stdin", false, "")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return *configPath, true, nil
		}
		return "", false, err
	}
	if flags.NArg() != 0 {
		return "", false, errors.New("unexpected positional arguments")
	}
	return *configPath, false, nil
}
