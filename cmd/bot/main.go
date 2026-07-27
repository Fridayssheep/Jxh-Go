package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/app"
	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/flashfile"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/health"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/napcat"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/settings"
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

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := openDB(ctx, cfg)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("access db pool: %v", err)
	}
	store := storage.NewStore(db)
	healthService := health.NewService()
	nowUTC := time.Now().UTC()
	healthService.SetDatabase(health.ComponentStatus{
		Available: true, Code: "available", CheckedAt: nowUTC, LastSuccessAt: nowUTC,
	})
	healthService.SetScheduler(health.ComponentStatus{Available: true, Code: "available", CheckedAt: nowUTC, LastSuccessAt: nowUTC})
	healthService.SetWorkers(health.ComponentStatus{Available: true, Code: "available", CheckedAt: nowUTC, LastSuccessAt: nowUTC})

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
	knowledgeLoadedAt := time.Now().UTC()
	if err := knowledgeSync.Sync(ctx); err != nil {
		log.Printf("load knowledge from WPS failed, trying local cache: %v", err)
		if cacheErr := knowledgeSync.LoadCache(); cacheErr != nil {
			log.Fatalf("load knowledge: WPS error: %v; cache error: %v", err, cacheErr)
		}
		log.Printf("loaded knowledge from local cache %s", cfg.WPS.CacheFile)
		healthService.SetWPS(health.ComponentStatus{Available: true, Code: "cache", CheckedAt: knowledgeLoadedAt, LastSuccessAt: knowledgeLoadedAt})
	} else {
		healthService.SetWPS(health.ComponentStatus{Available: true, Code: "available", CheckedAt: knowledgeLoadedAt, LastSuccessAt: knowledgeLoadedAt})
	}

	aiSvc, applicantExtractor, err := newAIServices(ctx, cfg, knowledgeIndex)
	if err != nil {
		log.Printf("ai service not available: %v", err)
		healthService.SetAI(health.ComponentStatus{Available: false, Code: "unavailable", CheckedAt: time.Now().UTC(), LastErrorAt: time.Now().UTC()})
	} else {
		healthService.SetAI(health.ComponentStatus{Available: aiSvc != nil, Code: availabilityCode(aiSvc != nil), CheckedAt: time.Now().UTC(), LastSuccessAt: time.Now().UTC()})
	}
	location := applicationLocation(cfg)
	now := func() time.Time { return time.Now().In(location) }
	knowledgeRuntime, err := knowledgeadmin.NewRuntimeStore(knowledgeadmin.RuntimeStoreOptions{
		Index: knowledgeIndex, Syncer: knowledgeSync, SourceConfigured: strings.TrimSpace(cfg.WPS.ShareURL) != "", Now: now,
	})
	if err != nil {
		log.Fatalf("initialize knowledge runtime: %v", err)
	}
	settingsRuntime := settings.NewDefaultRuntime()
	scheduleLocation := schedulerLocation(cfg)
	triggerStats := triggerstats.NewService(store, triggerstats.Options{
		Now:            now,
		ResolveKeyword: knowledgeIndex.Keyword,
		Location:       location,
	})
	var extractApplicant grouprequest.ExtractApplicantFunc
	if applicantExtractor != nil {
		extractApplicant = func(ctx context.Context, comment string) (grouprequest.ExtractedFields, error) {
			fields, err := applicantExtractor.Extract(ctx, comment)
			return grouprequest.ExtractedFields{
				StudentID: fields.StudentID, StudentName: fields.StudentName, Major: fields.Major,
			}, err
		}
	}
	groupRequests := grouprequest.NewService(store, grouprequest.Options{
		ExportDir:        "./data/exports/group_requests",
		Now:              now,
		Location:         location,
		ExtractApplicant: extractApplicant,
	})
	napcatGateway := napcat.NewGateway(flashfile.NewStager("./data/flash", "/app/data/flash"))
	pipeline := bot.NewPipeline(bot.Options{
		Sender:        napcatGateway,
		Knowledge:     knowledgeIndex,
		AI:            aiSvc,
		Reloader:      knowledgeRuntime,
		Admin:         commands.NewAdminHandler(store, scheduleLocation),
		Quote:         quote.NewClient(cfg.Quote.BaseURL, &http.Client{Timeout: time.Duration(cfg.Quote.TimeoutSec) * time.Second}),
		GroupRequests: groupRequests,
		TriggerStats:  triggerStats,
		LinkCleaner:   linkcleaner.NewService(),
		Settings:      settingsRuntime,
	})
	schedulerRuntime := scheduler.NewRuntime(scheduler.RuntimeOptions{
		Store:    store,
		Send:     pipeline.SendGroupText,
		Location: scheduleLocation,
		Logf:     log.Printf,
	})

	healthAddr := strings.TrimSpace(os.Getenv("JXH_HEALTH_ADDR"))
	if healthAddr == "" {
		healthAddr = ":8080"
	}
	healthMux := http.NewServeMux()
	healthMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	healthServer := &http.Server{
		Addr:    healthAddr,
		Handler: healthMux,
	}
	napcatServer := napcat.Server{
		WSURL:          cfg.OneBot.WSURL,
		Token:          cfg.OneBot.AccessToken,
		RequestTimeout: time.Duration(cfg.OneBot.APITimeoutSec) * time.Second,
		ReconnectDelay: time.Duration(cfg.OneBot.ReconnectIntervalSec) * time.Second,
		Handler:        pipeline,
		Gateway:        napcatGateway,
	}
	healthComponent, err := app.HTTPComponent("health-http", healthServer, true)
	if err != nil {
		log.Fatalf("create health component: %v", err)
	}
	components := []app.Component{
		healthComponent,
		{Name: "napcat", Critical: true, Run: napcatServer.Serve},
		{Name: "scheduler", Run: func(ctx context.Context) error { schedulerRuntime.Run(ctx); return nil }},
		{Name: "group-request-ai", Run: func(ctx context.Context) error { groupRequests.RunAIParser(ctx); return nil }},
		{Name: "health-monitor", Run: func(ctx context.Context) error { runHealthMonitor(ctx, healthService, napcatGateway); return nil }},
	}
	if cfg.Database.TriggerLogRetentionDays > 0 {
		components = append(components, app.Component{
			Name: "trigger-log-purge",
			Run: func(ctx context.Context) error {
				triggerStats.RunPurgeLoop(ctx, cfg.Database.TriggerLogRetentionDays)
				return nil
			},
		})
	}
	application, err := app.New(app.Options{
		Components: components, Closers: []io.Closer{sqlDB},
		ShutdownTimeout: time.Duration(cfg.Admin.ShutdownTimeoutSeconds) * time.Second, Logger: log.Default(),
	})
	if err != nil {
		log.Fatalf("create application: %v", err)
	}
	log.Printf("connecting napcat websocket")
	if err := application.Run(ctx); err != nil {
		log.Fatalf("run application: %v", err)
	}
}

func availabilityCode(available bool) string {
	if available {
		return "available"
	}
	return "not_configured"
}

func runHealthMonitor(ctx context.Context, service *health.Service, gateway *napcat.Gateway) {
	update := func() {
		now := time.Now().UTC()
		snapshot := gateway.Snapshot()
		code := "unavailable"
		if snapshot.Connected {
			code = "available"
		}
		status := health.ComponentStatus{Available: snapshot.Connected, Code: code, CheckedAt: now}
		if snapshot.Connected {
			status.LastSuccessAt = now
		} else if !snapshot.DisconnectedAt.IsZero() {
			status.LastErrorAt = snapshot.DisconnectedAt.UTC()
		}
		service.SetNapCat(status)
	}
	update()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			update()
		}
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

func newAIServices(ctx context.Context, cfg config.Config, index *knowledge.IndexRef) (*ai.Service, *ai.ApplicantExtractor, error) {
	if !cfg.AI.Enabled || !hasAIModelConfig(cfg.AI) {
		return nil, nil, nil
	}
	chatModel, err := ai.NewEinoModel(ctx, ai.EinoModelConfig{
		Provider: cfg.AI.Provider,
		BaseURL:  cfg.AI.BaseURL,
		APIKey:   cfg.AI.APIKey,
		Model:    cfg.AI.Model,
	})
	if err != nil {
		return nil, nil, err
	}
	reviewModel, err := ai.NewEinoModel(ctx, ai.EinoModelConfig{
		Provider: cfg.AI.Provider,
		BaseURL:  cfg.AI.BaseURL,
		APIKey:   cfg.AI.APIKey,
		Model:    cfg.AI.Model,
		JSONOnly: true,
	})
	if err != nil {
		return nil, nil, err
	}
	service, err := ai.NewService(ctx, ai.Options{
		Model:            chatModel,
		Reviewer:         reviewModel,
		Knowledge:        index,
		Timeout:          time.Duration(cfg.AI.TimeoutSec) * time.Second,
		MaxQuestionChars: cfg.AI.MaxQuestionChars,
	})
	if err != nil {
		return nil, nil, err
	}
	return service, ai.NewApplicantExtractor(chatModel, time.Duration(cfg.AI.TimeoutSec)*time.Second), nil
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

func openDB(ctx context.Context, cfg config.Config) (*gorm.DB, error) {
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
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.Database.ConnMaxLifetimeSeconds) * time.Second)
	sqlDB.SetConnMaxIdleTime(time.Duration(cfg.Database.ConnMaxIdleTimeSeconds) * time.Second)
	pingCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Database.PingTimeoutSeconds)*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("ping database")
	}
	return db, nil
}
