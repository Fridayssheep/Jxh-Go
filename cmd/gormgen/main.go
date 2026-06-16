package main

import (
	"bufio"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/config"
	"gorm.io/driver/mysql"
	"gorm.io/gen"
	"gorm.io/gorm"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	schemaPath := flag.String("schema", "deploy/mysql/init/001_schema.sql", "path to mysql schema sql")
	outPath := flag.String("out", "internal/storage/query", "gorm gen output path")
	applySchema := flag.Bool("apply-schema", true, "apply schema sql before generating")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	db, err := openDB(cfg)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	if *applySchema {
		if err := applySQLFile(context.Background(), db, *schemaPath); err != nil {
			log.Fatalf("apply schema: %v", err)
		}
	}

	g := gen.NewGenerator(gen.Config{
		OutPath:           *outPath,
		Mode:              gen.WithDefaultQuery | gen.WithQueryInterface,
		FieldNullable:     true,
		FieldCoverable:    true,
		FieldSignable:     true,
		FieldWithIndexTag: true,
		FieldWithTypeTag:  true,
	})
	g.UseDB(db)
	models := g.GenerateAllTable()
	g.ApplyBasic(models...)
	g.Execute()
}

func openDB(cfg config.Config) (*gorm.DB, error) {
	dsn := cfg.Database.DSN
	if dsn == "" {
		parseTime := "False"
		if cfg.Database.ParseTime {
			parseTime = "True"
		}
		dsn = fmt.Sprintf(
			"%s:%s@tcp(%s:%d)/%s?charset=%s&parseTime=%s&loc=%s",
			cfg.Database.User,
			cfg.Database.Password,
			cfg.Database.Host,
			cfg.Database.Port,
			cfg.Database.Name,
			cfg.Database.Charset,
			parseTime,
			cfg.Database.Loc,
		)
	}
	return gorm.Open(mysql.Open(dsn), &gorm.Config{})
}

func applySQLFile(ctx context.Context, db *gorm.DB, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	statements, err := splitSQLStatements(data)
	if err != nil {
		return err
	}
	for _, stmt := range statements {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" {
			continue
		}
		execCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		err := db.WithContext(execCtx).Exec(stmt).Error
		cancel()
		if err != nil {
			return fmt.Errorf("exec %q: %w", firstLine(stmt), err)
		}
	}
	return nil
}

func splitSQLStatements(data []byte) ([]string, error) {
	var cleaned bytes.Buffer
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		cleaned.WriteString(line)
		cleaned.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	var statements []string
	var current strings.Builder
	var quote rune
	escaped := false
	for _, r := range cleaned.String() {
		if quote != 0 {
			current.WriteRune(r)
			if escaped {
				escaped = false
				continue
			}
			if r == '\\' {
				escaped = true
				continue
			}
			if r == quote {
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
			current.WriteRune(r)
		case ';':
			statements = append(statements, current.String())
			current.Reset()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote %q in sql", quote)
	}
	if strings.TrimSpace(current.String()) != "" {
		statements = append(statements, current.String())
	}
	return statements, nil
}

func firstLine(stmt string) string {
	line := strings.TrimSpace(stmt)
	if idx := strings.IndexByte(line, '\n'); idx >= 0 {
		line = line[:idx]
	}
	if len(line) > 96 {
		line = line[:96] + "..."
	}
	return line
}
