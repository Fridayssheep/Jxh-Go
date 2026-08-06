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
	"github.com/zjutjh/jxh-go/internal/automation/scheduler"
	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/bot/commands"
	"github.com/zjutjh/jxh-go/internal/groups/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/knowledge/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/knowledge/triggerstats"
	"github.com/zjutjh/jxh-go/internal/management"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	"github.com/zjutjh/jxh-go/internal/messaging/flashfile"
	"github.com/zjutjh/jxh-go/internal/messaging/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/messaging/quote"
	"github.com/zjutjh/jxh-go/internal/platform/app"
	"github.com/zjutjh/jxh-go/internal/platform/config"
	"github.com/zjutjh/jxh-go/internal/platform/database"
	"github.com/zjutjh/jxh-go/internal/platform/health"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
	"github.com/zjutjh/jxh-go/internal/platform/storage"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
	"gorm.io/gorm"
)

func main() {
	os.Exit(mainResult(os.Args[1:]))
}

func mainResult(arguments []string) int {
	return mainResultWithDependencies(arguments, mainDependencies{
		loadConfig: config.Load,
		signalContext: func() (context.Context, context.CancelFunc) {
			return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		},
		run: run,
	})
}

type mainDependencies struct {
	loadConfig    func(string) (config.Config, error)
	signalContext func() (context.Context, context.CancelFunc)
	run           func(context.Context, config.Config, *app.RestartCoordinator) error
}

func mainResultWithDependencies(arguments []string, dependencies mainDependencies) int {
	flags := flag.NewFlagSet("jxh-bot", flag.ContinueOnError)
	configPath := flags.String("config", "config.yaml", "path to config file")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if dependencies.loadConfig == nil || dependencies.signalContext == nil || dependencies.run == nil {
		log.Print("initialize bot runtime: invalid main dependencies")
		return 1
	}

	cfg, err := dependencies.loadConfig(*configPath)
	if err != nil {
		log.Printf("load config: %v", err)
		return 1
	}

	signalCtx, stop := dependencies.signalContext()
	if signalCtx == nil || stop == nil {
		log.Print("initialize bot runtime: invalid signal context")
		return 1
	}
	defer stop()
	ctx, cancel := context.WithCancelCause(signalCtx)
	defer cancel(nil)
	restartCoordinator := app.NewRestartCoordinator(cancel)
	err = dependencies.run(ctx, cfg, restartCoordinator)
	if errors.Is(context.Cause(ctx), app.ErrRestartRequested) {
		if err == nil || errors.Is(err, app.ErrRestartRequested) {
			return 75
		}
	}
	if err != nil {
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
	schemaAutomation database.SchemaAutomation
	buildApplication func(context.Context, config.Config, databaseResources) (applicationRunner, error)
}

func run(ctx context.Context, cfg config.Config, restartCoordinator *app.RestartCoordinator) error {
	return runWithDependencies(ctx, cfg, runtimeDependencies{
		openDatabase: openDatabaseResources, schemaAutomation: database.NewMigrator(),
		buildApplication: func(ctx context.Context, cfg config.Config, database databaseResources) (applicationRunner, error) {
			return buildApplication(ctx, cfg, database, restartCoordinator)
		},
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
	if dependencies.schemaAutomation != nil {
		if err := dependencies.schemaAutomation.Apply(ctx, resources.ORM); err != nil {
			return errors.New("apply schema automation: database operation failed")
		}
	}

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

func buildApplication(
	ctx context.Context,
	cfg config.Config,
	database databaseResources,
	restartCoordinator *app.RestartCoordinator,
) (_ applicationRunner, err error) {
	db := database.ORM
	sqlDB := database.Pinger
	store := storage.NewStore(db)
	healthService := health.NewService()
	nowUTC := time.Now().UTC()
	healthService.SetDatabase(health.ComponentStatus{
		Available: true, Code: "available", CheckedAt: nowUTC, LastSuccessAt: nowUTC,
	})

	knowledgeIndex, knowledgeSync := initializeKnowledge(ctx, cfg.WPS, healthService)

	aiSvc, applicantExtractor, majorCodeJudge, err := newAIServices(ctx, cfg, knowledgeIndex)
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
	var managementBackend *management.Backend
	if adminHTTPConfigured(cfg.Admin) {
		managementBackend, err = management.NewBackend(management.Options{
			Context: ctx, Config: cfg, Store: store, Gateway: napcatGateway, Health: healthService,
			SettingsRuntime: settingsRuntime, KnowledgeStore: knowledgeRuntime, KnowledgeReloader: knowledgeRuntime,
			BotRestartScheduler: restartCoordinator, Location: location, Now: now, Logger: log.Default(),
			MajorCodeJudge: majorCodeJudge,
		})
		if err != nil {
			return nil, fmt.Errorf("initialize management backend: %w", err)
		}
	} else {
		checkedAt := time.Now().UTC()
		healthService.SetAdmin(health.ComponentStatus{Code: "misconfigured", CheckedAt: checkedAt, LastErrorAt: checkedAt})
		healthService.SetTelemetry(health.ComponentStatus{Code: "not_configured", CheckedAt: checkedAt})
		log.Print("admin API disabled: secure configuration is incomplete")
	}
	backendOwned := managementBackend != nil
	defer func() {
		if err != nil && backendOwned {
			managementBackend.Close()
		}
	}()
	var customCommands bot.CustomCommandExecutor
	var telemetryRecorder bot.TelemetryRecorder
	if managementBackend != nil {
		customCommands = managementBackend.CustomCommands
		telemetryRecorder = managementBackend.Telemetry
		groupRequests.SetEventPublisher(managementBackend.Events)
	}
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
		CustomCommands: customCommands,
		Telemetry:      telemetryRecorder,
	})
	schedulerOptions := scheduler.RuntimeOptions{
		Store: store, Send: pipeline.SendGroupText, Location: scheduleLocation, Logf: log.Printf,
	}
	if managementBackend != nil {
		schedulerOptions.Events = managementBackend.Events
		schedulerOptions.Telemetry = managementBackend.Telemetry
	}
	schedulerRuntime := scheduler.NewRuntime(schedulerOptions)

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
	schedulerHealth := newRuntimeHealthGroup(1, healthService.SetScheduler, time.Now)
	workerCount := 1
	if managementBackend != nil {
		workerCount += 2
	}
	if cfg.Database.TriggerLogRetentionDays > 0 {
		workerCount++
	}
	workerHealth := newRuntimeHealthGroup(workerCount, healthService.SetWorkers, time.Now)
	components := []app.Component{healthComponent}
	if managementBackend != nil {
		adminComponent, err := app.HTTPComponent("admin-http", managementBackend.AdminServer, false)
		if err != nil {
			return nil, fmt.Errorf("create admin component: %w", err)
		}
		adminHealth := newRuntimeHealthGroup(1, healthService.SetAdmin, time.Now)
		adminComponent.Run = adminHealth.Wrap(adminComponent.Run)
		components = append(components, adminComponent)
	}
	components = append(components,
		app.Component{Name: "napcat", Critical: true, Run: napcatServer.Serve},
		app.Component{Name: "scheduler", Run: schedulerHealth.Wrap(func(ctx context.Context) error { schedulerRuntime.Run(ctx); return nil })},
		app.Component{Name: "group-request-ai", Run: workerHealth.Wrap(func(ctx context.Context) error { groupRequests.RunAIParser(ctx); return nil })},
		app.Component{Name: "health-monitor", Run: func(ctx context.Context) error { runHealthMonitor(ctx, healthService, napcatGateway); return nil }},
		app.Component{Name: "database-health-monitor", Run: func(ctx context.Context) error {
			runDatabaseHealthMonitor(ctx, healthService, sqlDB, 5*time.Second, time.Duration(cfg.Database.PingTimeoutSeconds)*time.Second)
			return nil
		}},
	)
	if managementBackend != nil {
		components = append(components,
			app.Component{Name: "join-request-auto-approver", Run: workerHealth.Wrap(func(ctx context.Context) error {
				managementBackend.JoinRequests.RunAutoApprover(ctx)
				return nil
			})},
			app.Component{Name: "telemetry", Run: func(ctx context.Context) error {
				return runTelemetry(ctx, healthService, managementBackend.Telemetry)
			}},
			app.Component{Name: "telemetry-maintenance", Run: workerHealth.Wrap(managementBackend.Maintenance.Run)},
		)
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
	var closers []io.Closer
	if managementBackend != nil {
		closers = append(closers, closeFunc(func() error { managementBackend.Close(); return nil }))
	}
	application, err := app.New(app.Options{
		Components: components, Closers: closers,
		ShutdownTimeout: time.Duration(cfg.Admin.ShutdownTimeoutSeconds) * time.Second, Logger: log.Default(),
	})
	if err != nil {
		return nil, fmt.Errorf("create application: %w", err)
	}
	backendOwned = false
	return application, nil
}

func initializeKnowledge(ctx context.Context, configuration config.WPSConfig, healthService *health.Service) (*knowledge.IndexRef, *knowledge.Syncer) {
	index := knowledge.NewIndexRef(nil)
	syncer := knowledge.NewSyncer(knowledge.SyncerOptions{
		Source: knowledge.WPSClient{
			ShareURL: configuration.ShareURL,
			SID:      configuration.SID,
			Timeout:  time.Duration(configuration.TimeoutSec) * time.Second,
		},
		Sheet:     configuration.Sheet,
		CacheFile: configuration.CacheFile,
		Index:     index,
	})
	sourceConfigured := strings.TrimSpace(configuration.ShareURL) != ""
	if sourceConfigured {
		if err := syncer.Sync(ctx); err == nil {
			checkedAt := time.Now().UTC()
			healthService.SetWPS(health.ComponentStatus{
				Available: true, Code: "available", CheckedAt: checkedAt, LastSuccessAt: checkedAt,
			})
			return index, syncer
		}
		log.Print("load knowledge from WPS failed, trying local cache")
	}
	if err := syncer.LoadCache(); err == nil {
		checkedAt := time.Now().UTC()
		log.Printf("loaded knowledge from local cache %s", configuration.CacheFile)
		healthService.SetWPS(health.ComponentStatus{
			Available: true, Code: "cache", CheckedAt: checkedAt, LastSuccessAt: checkedAt,
		})
		return index, syncer
	}

	checkedAt := time.Now().UTC()
	if sourceConfigured {
		log.Print("knowledge unavailable: WPS sync and local cache load failed")
		healthService.SetWPS(health.ComponentStatus{
			Code: "unavailable", CheckedAt: checkedAt, LastErrorAt: checkedAt,
		})
	} else {
		log.Print("knowledge source is not configured and no local cache is available")
		healthService.SetWPS(health.ComponentStatus{Code: "not_configured", CheckedAt: checkedAt})
	}
	return index, syncer
}

func adminHTTPConfigured(configuration config.AdminConfig) bool {
	return len([]byte(configuration.SessionSecret)) >= 32
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

func newAIServices(ctx context.Context, cfg config.Config, index *knowledge.IndexRef) (*ai.Service, *ai.ApplicantExtractor, *ai.MajorCodeJudge, error) {
	if !cfg.AI.Enabled || !hasAIModelConfig(cfg.AI) {
		return nil, nil, nil, nil
	}
	chatModel, err := ai.NewEinoModel(ctx, ai.EinoModelConfig{
		Provider: cfg.AI.Provider,
		BaseURL:  cfg.AI.BaseURL,
		APIKey:   cfg.AI.APIKey,
		Model:    cfg.AI.Model,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	reviewModel, err := ai.NewEinoModel(ctx, ai.EinoModelConfig{
		Provider: cfg.AI.Provider,
		BaseURL:  cfg.AI.BaseURL,
		APIKey:   cfg.AI.APIKey,
		Model:    cfg.AI.Model,
		JSONOnly: true,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	service, err := ai.NewService(ctx, ai.Options{
		Model:            chatModel,
		Reviewer:         reviewModel,
		Knowledge:        index,
		Timeout:          time.Duration(cfg.AI.TimeoutSec) * time.Second,
		MaxQuestionChars: cfg.AI.MaxQuestionChars,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	timeout := time.Duration(cfg.AI.TimeoutSec) * time.Second
	return service, ai.NewApplicantExtractor(chatModel, timeout), ai.NewMajorCodeJudge(reviewModel, timeout), nil
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
