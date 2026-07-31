package management

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/automation/scheduledjobs"
	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/knowledge/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/management/analytics"
	"github.com/zjutjh/jxh-go/internal/management/api"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
	"github.com/zjutjh/jxh-go/internal/management/overview"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	"github.com/zjutjh/jxh-go/internal/management/system"
	"github.com/zjutjh/jxh-go/internal/platform/config"
	"github.com/zjutjh/jxh-go/internal/platform/health"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
)

const (
	eventCapacity            = 2048
	eventRetention           = 15 * time.Minute
	eventSubscriberBuffer    = 64
	telemetryCapacity        = 4096
	telemetryBatchSize       = 100
	telemetryFlushInterval   = time.Second
	telemetryFlushTimeout    = 5 * time.Second
	telemetryRetentionDays   = 30
	telemetryMaintenanceTick = time.Hour
)

type Store interface {
	auth.Store
	auth.AdminStore
	audit.Store
	overview.Store
	groups.Store
	settings.Store
	joinrequests.Store
	scheduledjobs.Store
	customcommand.Store
	telemetry.Store
	telemetry.MaintenanceStore
	analytics.Store
	system.Store
	knowledgeadmin.OperationStore
}

type Options struct {
	Context             context.Context
	Config              config.Config
	Store               Store
	Gateway             *napcat.Gateway
	Health              *health.Service
	SettingsRuntime     *settings.Runtime
	KnowledgeStore      knowledgeadmin.Store
	KnowledgeReloader   knowledgeadmin.Reloader
	BotRestartScheduler system.BotRestartScheduler
	Location            *time.Location
	Now                 func() time.Time
	Random              io.Reader
	Logger              *log.Logger
}

type Backend struct {
	AdminServer    *adminapi.Server
	Events         *events.Hub
	Settings       *settings.Service
	JoinRequests   *joinrequests.Service
	ScheduledJobs  *scheduledjobs.Service
	CustomCommands *customcommand.Service
	Telemetry      *telemetry.Service
	Maintenance    *telemetry.Maintenance
	Groups         *groups.Service
	Knowledge      *knowledgeadmin.Service
	System         *system.Service
}

func (b *Backend) Close() {
	if b == nil {
		return
	}
	if b.Knowledge != nil {
		b.Knowledge.Close()
	}
	if b.JoinRequests != nil {
		b.JoinRequests.Close()
	}
	if b.ScheduledJobs != nil {
		b.ScheduledJobs.Close()
	}
	if b.CustomCommands != nil {
		b.CustomCommands.Close()
	}
	if b.Groups != nil {
		b.Groups.Close()
	}
	if b.System != nil {
		b.System.Close()
	}
}

func NewBackend(options Options) (*Backend, error) {
	if options.Context == nil || options.Store == nil || options.Gateway == nil || options.Health == nil ||
		options.SettingsRuntime == nil || options.KnowledgeStore == nil || options.KnowledgeReloader == nil || options.Location == nil {
		return nil, fmt.Errorf("management backend dependencies are required")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Random == nil {
		options.Random = rand.Reader
	}
	if options.Logger == nil {
		options.Logger = log.Default()
	}
	random := &lockedReader{source: options.Random}
	secrets, err := DeriveSecrets([]byte(options.Config.Admin.SessionSecret))
	if err != nil {
		return nil, fmt.Errorf("derive management secrets: %w", err)
	}

	hub, err := events.NewHub(events.Options{
		Capacity: eventCapacity, Retention: eventRetention, SubscriberBuffer: eventSubscriberBuffer,
		IDSource: opaqueEventIDSource(random), Now: options.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("create management event hub: %w", err)
	}
	passwords := auth.NewPasswordHasher(auth.DefaultPasswordParams(), random)
	limiter, err := auth.NewLoginLimiter(auth.LoginLimiterOptions{
		Window:      time.Duration(options.Config.Admin.LoginWindowSeconds) * time.Second,
		MaxAttempts: options.Config.Admin.LoginMaxAttempts, Capacity: 10_000, Secret: secrets.LoginLimiter,
	})
	if err != nil {
		return nil, fmt.Errorf("create login limiter: %w", err)
	}
	authService, err := auth.NewService(auth.ServiceOptions{
		Store: options.Store, Passwords: passwords, Limiter: limiter, SessionSecret: secrets.SessionToken,
		Random: random, AbsoluteTTL: time.Duration(options.Config.Admin.SessionTTLSeconds) * time.Second,
		IdleTTL: time.Duration(options.Config.Admin.SessionIdleTimeoutSeconds) * time.Second, Now: options.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("create authentication service: %w", err)
	}
	adminService, err := auth.NewAdminService(auth.AdminServiceOptions{
		Store: options.Store, Passwords: passwords, RequestHashSecret: secrets.AdminMutation, Now: options.Now,
	})
	if err != nil {
		return nil, fmt.Errorf("create administrator service: %w", err)
	}
	auditService, err := audit.NewService(options.Store)
	if err != nil {
		return nil, fmt.Errorf("create audit service: %w", err)
	}
	systemService, err := system.NewService(system.Options{
		Store: options.Store, Health: options.Health, Gateway: options.Gateway, Events: hub,
		Configuration:               newConfigurationEditor(options.Config.SourcePath),
		AppliedConfigurationVersion: options.Config.SourceVersion,
		RestartSupported:            options.Config.BotRestartMode == config.BotRestartSupervisedExit && options.BotRestartScheduler != nil,
		BotRestartScheduler:         options.BotRestartScheduler,
		IdempotencySecret:           secrets.SystemOperation, Dependencies: dependencyConfiguration(options.Config),
		Now: options.Now, WorkerContext: options.Context,
	})
	if err != nil {
		return nil, fmt.Errorf("create system service: %w", err)
	}
	var groupService *groups.Service
	var knowledgeService *knowledgeadmin.Service
	var joinRequestService *joinrequests.Service
	var scheduledJobService *scheduledjobs.Service
	var customCommandService *customcommand.Service
	fail := func(err error) (*Backend, error) {
		if knowledgeService != nil {
			knowledgeService.Close()
		}
		if groupService != nil {
			groupService.Close()
		}
		if joinRequestService != nil {
			joinRequestService.Close()
		}
		if scheduledJobService != nil {
			scheduledJobService.Close()
		}
		if customCommandService != nil {
			customCommandService.Close()
		}
		systemService.Close()
		return nil, err
	}

	overviewService, err := overview.NewService(overview.Options{
		Store: options.Store, Health: systemService, Knowledge: options.KnowledgeStore,
		Now: options.Now, Location: options.Location,
	})
	if err != nil {
		return fail(fmt.Errorf("create overview service: %w", err))
	}
	groupService, err = groups.NewService(groups.Options{
		Store: options.Store, Gateway: options.Gateway, Events: hub, Now: options.Now, WorkerContext: options.Context,
	})
	if err != nil {
		return fail(fmt.Errorf("create group service: %w", err))
	}
	settingsService, err := settings.NewService(settings.Options{
		Store: options.Store, Runtime: options.SettingsRuntime, Events: hub, Now: options.Now,
	})
	if err != nil {
		return fail(fmt.Errorf("create settings service: %w", err))
	}
	telemetryService, err := telemetry.NewService(telemetry.Options{
		Store: options.Store, HMACSecret: secrets.TelemetryUser, Capacity: telemetryCapacity,
		BatchSize: telemetryBatchSize, FlushInterval: telemetryFlushInterval,
		FlushTimeout: telemetryFlushTimeout, Now: options.Now, Logger: options.Logger,
	})
	if err != nil {
		return fail(fmt.Errorf("create telemetry service: %w", err))
	}
	joinRequestService, err = joinrequests.NewService(joinrequests.Options{
		Store: options.Store, Approver: options.Gateway, AutoRejectReasons: options.SettingsRuntime,
		Events: hub, Telemetry: telemetryService, Now: options.Now,
		WorkerContext: options.Context,
	})
	if err != nil {
		return fail(fmt.Errorf("create join request service: %w", err))
	}
	scheduledJobService, err = scheduledjobs.NewService(scheduledjobs.Options{
		Store: options.Store, Sender: napcat.NewScheduledJobSender(options.Gateway), Events: hub, Now: options.Now,
		WorkerContext: options.Context,
	})
	if err != nil {
		return fail(fmt.Errorf("create scheduled job service: %w", err))
	}
	knowledgeService, err = knowledgeadmin.NewService(knowledgeadmin.Options{
		Store: options.KnowledgeStore, Operations: options.Store, Reloader: options.KnowledgeReloader,
		Events: hub, IdempotencySecret: secrets.KnowledgeOperation, Now: options.Now, WorkerContext: options.Context,
	})
	if err != nil {
		return fail(fmt.Errorf("create knowledge service: %w", err))
	}
	analyticsService, err := analytics.NewService(analytics.Options{Store: options.Store, Now: options.Now})
	if err != nil {
		return fail(fmt.Errorf("create analytics service: %w", err))
	}
	customCommandService, err = customcommand.NewService(customcommand.Options{
		Store: options.Store, Gateway: napcat.NewCustomCommandGateway(options.Gateway), Now: options.Now,
		ArgumentSummaryKey: secrets.CommandArgument, Events: hub, WorkerContext: options.Context,
	})
	if err != nil {
		return fail(fmt.Errorf("create custom command service: %w", err))
	}
	maintenance, err := telemetry.NewMaintenance(telemetry.MaintenanceOptions{
		Store: options.Store, Location: options.Location, RetentionDays: telemetryRetentionDays,
		Interval: telemetryMaintenanceTick, Now: options.Now, Logger: options.Logger,
	})
	if err != nil {
		return fail(fmt.Errorf("create telemetry maintenance: %w", err))
	}

	if err := loadRuntimeState(options.Context, settingsService, customCommandService, groupService, joinRequestService, scheduledJobService, knowledgeService, systemService); err != nil {
		return fail(err)
	}
	router, err := adminapi.NewManagementRouter(adminapi.ManagementOptions{
		Middleware: adminapi.MiddlewareOptions{
			PublicOrigin: options.Config.Admin.PublicOrigin, TrustedProxies: options.Config.Admin.TrustedProxies,
			MaxBodyBytes:          options.Config.Admin.MaxRequestBodyBytes,
			MaxConcurrentRequests: options.Config.Admin.MaxConcurrentRequests,
			Random:                random, Logger: options.Logger,
		},
		CookieSecure: options.Config.Admin.CookieSecure,
		Auth:         authService, Users: adminService, Audit: auditService, Overview: overviewService,
		Groups: groupService, Settings: settingsService, JoinRequests: joinRequestService,
		ScheduledJobs: scheduledJobService, Knowledge: knowledgeService, Analytics: analyticsService,
		Commands: customCommandService, System: systemService, Events: hub,
	})
	if err != nil {
		return fail(fmt.Errorf("create management router: %w", err))
	}
	server, err := adminapi.NewServer(options.Config.Admin, router)
	if err != nil {
		return fail(fmt.Errorf("create management server: %w", err))
	}
	return &Backend{
		AdminServer: server, Events: hub, Settings: settingsService, JoinRequests: joinRequestService,
		ScheduledJobs: scheduledJobService, CustomCommands: customCommandService,
		Telemetry: telemetryService, Maintenance: maintenance, Groups: groupService,
		Knowledge: knowledgeService, System: systemService,
	}, nil
}

func newConfigurationEditor(sourcePath string) system.ConfigurationEditor {
	if strings.TrimSpace(sourcePath) == "" {
		return nil
	}
	editor, err := config.NewFileEditor(sourcePath)
	if err != nil {
		return nil
	}
	return editor
}

func loadRuntimeState(
	ctx context.Context,
	settingsService *settings.Service,
	commands *customcommand.Service,
	groupsService *groups.Service,
	joinRequestService *joinrequests.Service,
	scheduledJobService *scheduledjobs.Service,
	knowledgeService *knowledgeadmin.Service,
	systemService *system.Service,
) error {
	if err := settingsService.ReloadRuntime(ctx); err != nil {
		return fmt.Errorf("load runtime settings: %w", err)
	}
	if err := commands.LoadRegistry(ctx); err != nil {
		return fmt.Errorf("load custom command registry: %w", err)
	}
	if _, err := groupsService.RecoverInterruptedSyncs(ctx); err != nil {
		return fmt.Errorf("recover group syncs: %w", err)
	}
	if err := joinRequestService.ReloadStudentIDRule(ctx); err != nil {
		return fmt.Errorf("load student ID rule: %w", err)
	}
	if err := joinRequestService.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("recover join request decisions: %w", err)
	}
	if _, err := scheduledJobService.RecoverInterruptedRuns(ctx); err != nil {
		return fmt.Errorf("recover scheduled job runs: %w", err)
	}
	if _, err := knowledgeService.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("recover knowledge reloads: %w", err)
	}
	if _, err := systemService.RecoverInterrupted(ctx); err != nil {
		return fmt.Errorf("recover system operations: %w", err)
	}
	return nil
}

func dependencyConfiguration(cfg config.Config) map[system.DependencyKey]system.DependencyConfiguration {
	return map[system.DependencyKey]system.DependencyConfiguration{
		system.DependencyMySQL:     {Configured: true, Required: true},
		system.DependencyNapCat:    {Configured: strings.TrimSpace(cfg.OneBot.WSURL) != ""},
		system.DependencyWPS:       {Configured: strings.TrimSpace(cfg.WPS.ShareURL) != ""},
		system.DependencyAI:        {Configured: cfg.AI.Enabled && strings.TrimSpace(cfg.AI.APIKey) != "" && strings.TrimSpace(cfg.AI.Model) != ""},
		system.DependencyQuote:     {Configured: strings.TrimSpace(cfg.Quote.BaseURL) != ""},
		system.DependencyWorker:    {Configured: true},
		system.DependencyScheduler: {Configured: true},
		system.DependencyTelemetry: {Configured: true},
	}
}

func opaqueEventIDSource(random io.Reader) func() (string, error) {
	return func() (string, error) {
		var data [18]byte
		if _, err := io.ReadFull(random, data[:]); err != nil {
			return "", err
		}
		return "evt_" + base64.RawURLEncoding.EncodeToString(data[:]), nil
	}
}

type lockedReader struct {
	mu     sync.Mutex
	source io.Reader
}

func (r *lockedReader) Read(buffer []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.source.Read(buffer)
}
