package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/napcat"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/storage"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	db, err := openDB(cfg)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	store := storage.NewStore(db)

	knowledgeIndex := knowledge.NewIndexRef(nil)
	knowledgeSync := knowledge.NewSyncer(knowledge.SyncerOptions{
		Source: knowledge.WPSClient{
			ShareURL: cfg.WPS.ShareURL,
			SID:      cfg.WPS.SID,
			Timeout:  time.Duration(cfg.WPS.TimeoutSec) * time.Second,
		},
		Sheet:     cfg.WPS.Sheet,
		CacheFile: cfg.WPS.CacheFile,
		Index:     knowledgeIndex,
	})
	if _, err := knowledgeSync.Sync(ctx); err != nil {
		log.Printf("load knowledge from WPS failed, trying local cache: %v", err)
		if _, cacheErr := knowledgeSync.LoadCache(); cacheErr != nil {
			log.Fatalf("load knowledge: WPS error: %v; cache error: %v", err, cacheErr)
		}
		log.Printf("loaded knowledge from local cache %s", cfg.WPS.CacheFile)
	}

	aiSvc, err := newAIService(ctx, cfg, knowledgeIndex)
	if err != nil {
		log.Fatalf("create ai service: %v", err)
	}
	location := applicationLocation(cfg)
	triggerStats := triggerstats.NewService(store, triggerstats.Options{
		Now:            func() time.Time { return time.Now().In(location) },
		ResolveKeyword: knowledgeIndex.Keyword,
	})
	groupRequests := grouprequest.NewService(store, grouprequest.Options{ExportDir: "./data/exports/group_requests"})
	pipeline := bot.NewPipeline(bot.Options{
		Knowledge:     knowledgeIndex,
		AI:            aiSvc,
		Reloader:      knowledgeSync,
		Admin:         commands.NewAdminHandler(store),
		Quote:         quote.NewClient(cfg.Quote.BaseURL, &http.Client{Timeout: time.Duration(cfg.Quote.TimeoutSec) * time.Second}),
		GroupRequests: groupRequests,
		TriggerStats:  triggerStats,
		LinkCleaner:   linkcleaner.NewService(linkcleaner.Options{}),
	})
	go scheduler.NewRuntime(scheduler.RuntimeOptions{
		Store:    store,
		Send:     pipeline.SendGroupText,
		Location: schedulerLocation(cfg),
		Logf:     log.Printf,
	}).Run(ctx)

	server := napcat.Server{
		Addr:           cfg.Server.Addr,
		WSURL:          cfg.OneBot.WSURL,
		Token:          cfg.OneBot.AccessToken,
		RequestTimeout: cfg.OneBot.APITimeout,
		ReconnectDelay: cfg.OneBot.ReconnectInterval,
		Handler:        pipeline,
	}
	if cfg.OneBot.WSURL != "" {
		log.Printf("connecting napcat websocket %s", cfg.OneBot.WSURL)
	} else {
		log.Printf("starting reverse websocket server on %s", cfg.Server.Addr)
	}
	if err := server.Serve(ctx); err != nil {
		log.Fatalf("serve napcat websocket: %v", err)
	}
}

func hasAIModelConfig(cfg config.AIConfig) bool {
	if cfg.APIKey == "" || cfg.Model == "" {
		return false
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	switch provider {
	case "", "openai":
		return cfg.BaseURL != ""
	case "ark":
		return true
	default:
		return true
	}
}

func newAIService(ctx context.Context, cfg config.Config, index *knowledge.IndexRef) (*ai.Service, error) {
	if !cfg.AI.Enabled || !hasAIModelConfig(cfg.AI) {
		return nil, nil
	}
	chatModel, err := ai.NewEinoModel(ctx, ai.EinoModelConfig{
		Provider: cfg.AI.Provider,
		BaseURL:  cfg.AI.BaseURL,
		APIKey:   cfg.AI.APIKey,
		Model:    cfg.AI.Model,
	})
	if err != nil {
		return nil, err
	}
	return ai.NewService(ctx, ai.Options{
		Model:            chatModel,
		Knowledge:        index,
		Timeout:          time.Duration(cfg.AI.TimeoutSec) * time.Second,
		MaxQuestionChars: cfg.AI.MaxQuestionChars,
	})
}

func applicationLocation(cfg config.Config) *time.Location {
	if cfg.App.Timezone != "" {
		if location, err := time.LoadLocation(cfg.App.Timezone); err == nil {
			return location
		} else {
			log.Printf("load app timezone failed: %v", err)
		}
	}
	return time.Local
}

func schedulerLocation(cfg config.Config) *time.Location {
	loc := time.Local
	if cfg.Scheduler.Timezone != "" {
		if loaded, err := time.LoadLocation(cfg.Scheduler.Timezone); err == nil {
			loc = loaded
		} else {
			log.Printf("load scheduler timezone failed: %v", err)
		}
	}
	return loc
}

func openDB(cfg config.Config) (*gorm.DB, error) {
	dsn := cfg.Database.DSN
	if dsn == "" {
		location, err := time.LoadLocation(cfg.Database.Loc)
		if err != nil {
			return nil, err
		}
		driverConfig := drivermysql.NewConfig()
		driverConfig.User = cfg.Database.User
		driverConfig.Passwd = cfg.Database.Password
		driverConfig.Net = "tcp"
		driverConfig.Addr = net.JoinHostPort(cfg.Database.Host, strconv.Itoa(cfg.Database.Port))
		driverConfig.DBName = cfg.Database.Name
		driverConfig.Params = map[string]string{"charset": cfg.Database.Charset}
		driverConfig.ParseTime = cfg.Database.ParseTime
		driverConfig.Loc = location
		dsn = driverConfig.FormatDSN()
	}
	return gorm.Open(mysql.Open(dsn), &gorm.Config{})
}
