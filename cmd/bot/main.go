package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

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
	knowledgeRetrieverOptions := ai.KnowledgeRetrieverOptions{
		ScoreThreshold: cfg.AI.ScoreThreshold,
		CacheTTL:       time.Duration(cfg.Cache.AIRetrievalTTLSec) * time.Second,
	}
	aiRetriever := ai.NewRetrieverRef(nil)
	knowledgeSync := knowledge.NewSyncer(knowledge.SyncerOptions{
		Source: knowledge.WPSClient{
			ShareURL: cfg.WPS.ShareURL,
			SID:      cfg.WPS.SID,
			Timeout:  time.Duration(cfg.WPS.TimeoutSec) * time.Second,
		},
		Sheet:     cfg.WPS.Sheet,
		CacheFile: cfg.WPS.CacheFile,
		Index:     knowledgeIndex,
		OnSynced: func(entries []knowledge.Entry) {
			aiRetriever.Set(ai.NewKnowledgeRetriever(entries, knowledgeRetrieverOptions))
		},
	})
	if _, err := knowledgeSync.Sync(ctx); err != nil {
		log.Printf("load knowledge from WPS failed, trying local cache: %v", err)
		if _, cacheErr := knowledgeSync.LoadCache(); cacheErr != nil {
			log.Fatalf("load knowledge: WPS error: %v; cache error: %v", err, cacheErr)
		}
		log.Printf("loaded knowledge from local cache %s", cfg.WPS.CacheFile)
	}

	aiSvc, err := newAIService(ctx, cfg, aiRetriever)
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

func shouldCreateEinoChat(cfg config.AIConfig) bool {
	if cfg.Model == "" {
		return false
	}
	if cfg.APIKey != "" {
		return true
	}
	return false
}

func newAIService(ctx context.Context, cfg config.Config, retriever ai.Retriever) (*ai.Service, error) {
	if !cfg.AI.Enabled {
		return nil, nil
	}
	chat := ai.Chat(ai.ExtractiveChat{})
	if shouldCreateEinoChat(cfg.AI) {
		einoChat, err := ai.NewEinoChat(ctx, ai.EinoChatConfig{
			Provider: cfg.AI.Provider,
			BaseURL:  cfg.AI.BaseURL,
			APIKey:   cfg.AI.APIKey,
			Model:    cfg.AI.Model,
		})
		if err != nil {
			return nil, err
		}
		chat = einoChat
	}
	return ai.NewService(ai.Options{
		Retriever:        retriever,
		Chat:             chat,
		TopK:             cfg.AI.TopK,
		MaxQuestionChars: cfg.AI.MaxQuestionChars,
	}), nil
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
		parseTime := "False"
		if cfg.Database.ParseTime {
			parseTime = "True"
		}
		dsn = cfg.Database.User + ":" + cfg.Database.Password + "@tcp(" + cfg.Database.Host + ":" + itoa(cfg.Database.Port) + ")/" + cfg.Database.Name + "?charset=" + cfg.Database.Charset + "&parseTime=" + parseTime + "&loc=" + cfg.Database.Loc
	}
	return gorm.Open(mysql.Open(dsn), &gorm.Config{})
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
