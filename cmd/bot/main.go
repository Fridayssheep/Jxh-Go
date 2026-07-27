package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/app"
	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/database"
	"github.com/zjutjh/jxh-go/internal/flashfile"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/health"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/management"
	"github.com/zjutjh/jxh-go/internal/napcat"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/settings"
	"github.com/zjutjh/jxh-go/internal/storage"
	"github.com/zjutjh/jxh-go/internal/telemetry"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"gorm.io/gorm"
)

func main() {
	os.Exit(mainResult(os.Args[1:]))
}

func mainResult(arguments []string) int {
	flags := flag.NewFlagSet("jxh-bot", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "path to config file")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("load config: %v", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg); err != nil {
		log.Printf("run bot: %v", err)
		return 1
	}
	return 0
}

type databasePinger interface {
	PingContext(context.Context) error
}

type databaseResources struct {
	ORM    *gorm.DB
	Pinger databasePinger
	Closer io.Closer
}

type applicationRunner interface {
	Run(context.Context) error
}

type runtimeDependencies struct {
	openDatabase     func(context.Context, config.DatabaseConfig) (databaseResources, error)
	buildApplication func(context.Context, config.Config, databaseResources) (applicationRunner, error)
}

func run(ctx context.Context, cfg config.Config) error {
	return runWithDependencies(ctx, cfg, runtimeDependencies{
		openDatabase:     openDatabaseResources,
		buildApplication: buildApplication,
	})
}

func runWithDependencies(ctx context.Context, cfg config.Config, dependencies runtimeDependencies) (err error) {
	if ctx == nil || dependencies.openDatabase == nil || dependencies.buildApplication == nil {
		return errors.New("initialize bot runtime: invalid dependencies")
	}
	resources, err := dependencies.openDatabase(ctx, cfg.Database)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	if resources.ORM == nil || resources.Pinger == nil || resources.Closer == nil {
		if resources.Closer != nil {
			_ = resources.Closer.Close()
		}
		return errors.New("open database: incomplete database resources")
	}
	defer func() {
		if closeErr := resources.Closer.Close(); closeErr != nil {
			err = errors.Join(err, errors.New("close database: database operation failed"))
		}
	}()

	application, err := dependencies.buildApplication(ctx, cfg, resources)
	if err != nil {
		return err
	}
	if application == nil {
		return errors.New("initialize bot runtime: application is not configured")
	}
	return application.Run(ctx)
}

func openDatabaseResources(ctx context.Context, cfg config.DatabaseConfig) (databaseResources, error) {
	db, err := database.OpenGORM(ctx, cfg)
	if err != nil {
		return databaseResources{}, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		if closer, ok := db.ConnPool.(io.Closer); ok {
			_ = closer.Close()
		}
		return databaseResources{}, errors.New("access database pool: database operation failed")
	}
	return databaseResources{ORM: db, Pinger: sqlDB, Closer: sqlDB}, nil
}

func buildApplication(ctx context.Context, cfg config.Config, database databaseResources) (_ applicationRunner, err error) {
	db := database.ORM
	sqlDB := database.Pinger
	store := storage.NewStore(db)
	healthService := health.NewService()
	nowUTC := time.Now().UTC()
	healthService.SetDatabase(health.ComponentStatus{
		Available: true, Code: "available", CheckedAt: nowUTC, LastSuccessAt: nowUTC,
	})

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
			return nil, errors.New("load knowledge: WPS and local cache unavailable")
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
		return nil, fmt.Errorf("initialize knowledge runtime: %w", err)
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
	managementBackend, err := management.NewBackend(management.Options{
		Context: ctx, Config: cfg, Store: store, Gateway: napcatGateway, Health: healthService,
		SettingsRuntime: settingsRuntime, KnowledgeStore: knowledgeRuntime, KnowledgeReloader: knowledgeRuntime,
		Location: location, Now: now, Logger: log.Default(),
	})
	if err != nil {
		return nil, fmt.Errorf("initialize management backend: %w", err)
	}
	backendOwned := true
	defer func() {
		if err != nil && backendOwned {
			managementBackend.Close()
		}
	}()
	quoteClient := quote.NewClient(
		cfg.Quote.BaseURL,
		&http.Client{Timeout: time.Duration(cfg.Quote.TimeoutSec) * time.Second},
		func(observation quote.Observation) { recordQuoteHealth(healthService, observation) },
	)
	if strings.TrimSpace(cfg.Quote.BaseURL) == "" {
		checkedAt := time.Now().UTC()
		healthService.SetQuote(health.ComponentStatus{Code: "not_configured", CheckedAt: checkedAt})
	}
	pipeline := bot.NewPipeline(bot.Options{
		Sender:         napcatGateway,
		Knowledge:      knowledgeIndex,
		AI:             aiSvc,
		Reloader:       knowledgeRuntime,
		Admin:          commands.NewAdminHandler(store, scheduleLocation),
		Quote:          quoteClient,
		GroupRequests:  groupRequests,
		TriggerStats:   triggerStats,
		LinkCleaner:    linkcleaner.NewService(),
		Settings:       settingsRuntime,
		CustomCommands: managementBackend.CustomCommands,
		Telemetry:      managementBackend.Telemetry,
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
		return nil, fmt.Errorf("create health component: %w", err)
	}
	adminComponent, err := app.HTTPComponent("admin-http", managementBackend.AdminServer, false)
	if err != nil {
		return nil, fmt.Errorf("create admin component: %w", err)
	}
	adminHealth := newRuntimeHealthGroup(1, healthService.SetAdmin, time.Now)
	adminComponent.Run = adminHealth.Wrap(adminComponent.Run)
	schedulerHealth := newRuntimeHealthGroup(1, healthService.SetScheduler, time.Now)
	workerCount := 3
	if cfg.Database.TriggerLogRetentionDays > 0 {
		workerCount++
	}
	workerHealth := newRuntimeHealthGroup(workerCount, healthService.SetWorkers, time.Now)
	components := []app.Component{
		healthComponent,
		adminComponent,
		{Name: "napcat", Critical: true, Run: napcatServer.Serve},
		{Name: "scheduler", Run: schedulerHealth.Wrap(func(ctx context.Context) error { schedulerRuntime.Run(ctx); return nil })},
		{Name: "group-request-ai", Run: workerHealth.Wrap(func(ctx context.Context) error { groupRequests.RunAIParser(ctx); return nil })},
		{Name: "join-request-auto-approver", Run: workerHealth.Wrap(func(ctx context.Context) error { managementBackend.JoinRequests.RunAutoApprover(ctx); return nil })},
		{Name: "telemetry", Run: func(ctx context.Context) error { return runTelemetry(ctx, healthService, managementBackend.Telemetry) }},
		{Name: "telemetry-maintenance", Run: workerHealth.Wrap(managementBackend.Maintenance.Run)},
		{Name: "health-monitor", Run: func(ctx context.Context) error { runHealthMonitor(ctx, healthService, napcatGateway); return nil }},
		{Name: "database-health-monitor", Run: func(ctx context.Context) error {
			runDatabaseHealthMonitor(ctx, healthService, sqlDB, 5*time.Second, time.Duration(cfg.Database.PingTimeoutSeconds)*time.Second)
			return nil
		}},
	}
	if cfg.Database.TriggerLogRetentionDays > 0 {
		components = append(components, app.Component{
			Name: "trigger-log-purge",
			Run: workerHealth.Wrap(func(ctx context.Context) error {
				triggerStats.RunPurgeLoop(ctx, cfg.Database.TriggerLogRetentionDays)
				return nil
			}),
		})
	}
	application, err := app.New(app.Options{
		Components: components, Closers: []io.Closer{closeFunc(func() error { managementBackend.Close(); return nil })},
		ShutdownTimeout: time.Duration(cfg.Admin.ShutdownTimeoutSeconds) * time.Second, Logger: log.Default(),
	})
	if err != nil {
		return nil, fmt.Errorf("create application: %w", err)
	}
	backendOwned = false
	return application, nil
}

type closeFunc func() error

func (function closeFunc) Close() error { return function() }

type runtimeHealthGroup struct {
	mu          sync.Mutex
	expected    int
	running     int
	failed      bool
	lastSuccess time.Time
	lastError   time.Time
	set         func(health.ComponentStatus)
	now         func() time.Time
}

func newRuntimeHealthGroup(expected int, set func(health.ComponentStatus), now func() time.Time) *runtimeHealthGroup {
	return &runtimeHealthGroup{expected: expected, set: set, now: now}
}

func (g *runtimeHealthGroup) Wrap(run func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) (err error) {
		g.started()
		defer func() { g.stopped(ctx, err) }()
		return run(ctx)
	}
}

func (g *runtimeHealthGroup) started() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.running++
	checkedAt := g.now().UTC()
	status := health.ComponentStatus{
		Code: "starting", CheckedAt: checkedAt, LastSuccessAt: g.lastSuccess, LastErrorAt: g.lastError,
	}
	if g.running == g.expected && !g.failed {
		status.Available = true
		status.Code = "available"
		status.LastSuccessAt = checkedAt
		g.lastSuccess = checkedAt
	}
	g.set(status)
}

func (g *runtimeHealthGroup) stopped(ctx context.Context, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.running > 0 {
		g.running--
	}
	checkedAt := g.now().UTC()
	failure := err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)
	if failure {
		g.failed = true
		g.lastError = checkedAt
	}
	code := "stopped"
	if g.failed {
		code = "failed"
	} else if ctx.Err() == nil {
		code = "stopped_unexpectedly"
		g.lastError = checkedAt
	}
	g.set(health.ComponentStatus{
		Code: code, CheckedAt: checkedAt, LastSuccessAt: g.lastSuccess, LastErrorAt: g.lastError,
	})
}

func recordQuoteHealth(service *health.Service, observation quote.Observation) {
	if service == nil || observation.OccurredAt.IsZero() || observation.Latency < 0 {
		return
	}
	previous := service.Snapshot().Quote
	status := health.ComponentStatus{
		CheckedAt: observation.OccurredAt.UTC(), Latency: observation.Latency,
		LastSuccessAt: previous.LastSuccessAt, LastErrorAt: previous.LastErrorAt,
	}
	switch observation.Outcome {
	case quote.OutcomeGIFSuccess:
		status.Available = true
		status.Code = "available"
		status.LastSuccessAt = status.CheckedAt
	case quote.OutcomePNGFallback:
		status.Code = "degraded_fallback"
		status.LastSuccessAt = status.CheckedAt
	case quote.OutcomeFailure:
		status.Code = "unavailable"
		status.LastErrorAt = status.CheckedAt
	default:
		return
	}
	service.SetQuote(status)
}

func runTelemetry(ctx context.Context, service *health.Service, worker *telemetry.Service) error {
	now := time.Now().UTC()
	service.SetTelemetry(health.ComponentStatus{Available: true, Code: "available", CheckedAt: now, LastSuccessAt: now})
	err := worker.Run(ctx)
	stoppedAt := time.Now().UTC()
	status := health.ComponentStatus{Available: false, Code: "stopped", CheckedAt: stoppedAt, LastSuccessAt: now}
	if err != nil {
		status.Code = "failed"
		status.LastErrorAt = stoppedAt
	}
	service.SetTelemetry(status)
	return err
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

func runDatabaseHealthMonitor(
	ctx context.Context,
	service *health.Service,
	pinger databasePinger,
	interval time.Duration,
	timeout time.Duration,
) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	checkDatabaseHealth(ctx, service, pinger, timeout, time.Now)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !checkDatabaseHealth(ctx, service, pinger, timeout, time.Now) {
				return
			}
		}
	}
}

func checkDatabaseHealth(
	ctx context.Context,
	service *health.Service,
	pinger databasePinger,
	timeout time.Duration,
	now func() time.Time,
) bool {
	if ctx == nil || service == nil || pinger == nil || now == nil {
		return false
	}
	startedAt := now()
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	err := pinger.PingContext(pingCtx)
	cancel()
	if ctx.Err() != nil {
		return false
	}
	checkedAt := now()
	latency := checkedAt.Sub(startedAt)
	if latency < 0 {
		latency = 0
	}
	previous := service.Snapshot().Database
	status := health.ComponentStatus{
		CheckedAt: checkedAt.UTC(), Latency: latency,
		LastSuccessAt: previous.LastSuccessAt, LastErrorAt: previous.LastErrorAt,
	}
	if err == nil {
		status.Available = true
		status.Code = "available"
		status.LastSuccessAt = status.CheckedAt
	} else {
		status.Code = "unavailable"
		if errors.Is(err, context.DeadlineExceeded) {
			status.Code = "timeout"
		}
		status.LastErrorAt = status.CheckedAt
	}
	service.SetDatabase(status)
	return true
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
