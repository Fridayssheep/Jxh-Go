package main

import (
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/messaging/quote"
	"github.com/zjutjh/jxh-go/internal/platform/config"
	"github.com/zjutjh/jxh-go/internal/platform/health"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
	"gorm.io/gorm"
)

func TestRunWithDependenciesClosesDatabaseAfterInitializationFailure(t *testing.T) {
	closer := &botDatabaseCloser{}
	want := errors.New("initialize later component")
	err := runWithDependencies(t.Context(), config.Default(), runtimeDependencies{
		openDatabase: func(context.Context, config.DatabaseConfig) (databaseResources, error) {
			return databaseResources{ORM: &gorm.DB{}, Pinger: botDatabasePinger{}, Closer: closer}, nil
		},
		buildApplication: func(context.Context, config.Config, databaseResources) (applicationRunner, error) {
			return nil, want
		},
	})
	if !errors.Is(err, want) {
		t.Fatalf("runWithDependencies() error=%v, want %v", err, want)
	}
	if closer.calls != 1 {
		t.Fatalf("database close calls=%d, want 1", closer.calls)
	}
}

func TestRunWithDependenciesClosesDatabaseAfterApplicationStops(t *testing.T) {
	closer := &botDatabaseCloser{}
	runner := &botApplicationRunner{}
	err := runWithDependencies(t.Context(), config.Default(), runtimeDependencies{
		openDatabase: func(context.Context, config.DatabaseConfig) (databaseResources, error) {
			return databaseResources{ORM: &gorm.DB{}, Pinger: botDatabasePinger{}, Closer: closer}, nil
		},
		buildApplication: func(context.Context, config.Config, databaseResources) (applicationRunner, error) {
			if closer.calls != 0 {
				t.Fatal("database closed before application run")
			}
			return runner, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if runner.calls != 1 || closer.calls != 1 {
		t.Fatalf("runner calls=%d database close calls=%d, want 1 and 1", runner.calls, closer.calls)
	}
}

func TestRunWithDependenciesSanitizesDatabaseCloseFailure(t *testing.T) {
	secret := "database-password"
	err := runWithDependencies(t.Context(), config.Default(), runtimeDependencies{
		openDatabase: func(context.Context, config.DatabaseConfig) (databaseResources, error) {
			return databaseResources{
				ORM: &gorm.DB{}, Pinger: botDatabasePinger{},
				Closer: &botDatabaseCloser{err: errors.New(secret)},
			}, nil
		},
		buildApplication: func(context.Context, config.Config, databaseResources) (applicationRunner, error) {
			return &botApplicationRunner{}, nil
		},
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("runWithDependencies() error=%v", err)
	}
}

func TestAdminHTTPRequiresCompleteSecureConfiguration(t *testing.T) {
	configuration := config.Default().Admin
	if adminHTTPConfigured(configuration) {
		t.Fatal("default incomplete admin configuration was enabled")
	}
	configuration.PublicOrigin = "https://manager.example"
	configuration.SessionSecret = strings.Repeat("x", 31)
	if adminHTTPConfigured(configuration) {
		t.Fatal("short admin session secret was accepted")
	}
	configuration.SessionSecret += "x"
	if !adminHTTPConfigured(configuration) {
		t.Fatal("complete secure admin configuration was disabled")
	}
	configuration.PublicOrigin = "  "
	if adminHTTPConfigured(configuration) {
		t.Fatal("blank admin origin was accepted")
	}
}

func TestInitializeKnowledgeAllowsMissingOptionalSourceAndCache(t *testing.T) {
	configuration := config.Default().WPS
	configuration.ShareURL = ""
	configuration.CacheFile = filepath.Join(t.TempDir(), "missing.xlsx")
	healthService := health.NewService()

	index, syncer := initializeKnowledge(t.Context(), configuration, healthService)

	if index == nil || syncer == nil || len(index.Entries()) != 0 {
		t.Fatalf("knowledge runtime index=%v syncer=%v entries=%d", index, syncer, len(index.Entries()))
	}
	status := healthService.Snapshot().WPS
	if status.Available || status.Code != "not_configured" || status.CheckedAt.IsZero() || !status.LastErrorAt.IsZero() {
		t.Fatalf("WPS status=%+v", status)
	}
}

func TestInitializeKnowledgeMarksConfiguredSourceWithoutCacheUnavailable(t *testing.T) {
	configuration := config.Default().WPS
	configuration.ShareURL = "://invalid"
	configuration.CacheFile = filepath.Join(t.TempDir(), "missing.xlsx")
	healthService := health.NewService()

	index, syncer := initializeKnowledge(t.Context(), configuration, healthService)

	if index == nil || syncer == nil || len(index.Entries()) != 0 {
		t.Fatalf("knowledge runtime index=%v syncer=%v entries=%d", index, syncer, len(index.Entries()))
	}
	status := healthService.Snapshot().WPS
	if status.Available || status.Code != "unavailable" || status.CheckedAt.IsZero() || status.LastErrorAt != status.CheckedAt {
		t.Fatalf("WPS status=%+v", status)
	}
}

func TestCheckDatabaseHealthTracksOutageAndRecovery(t *testing.T) {
	service := health.NewService()
	initialSuccess := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	service.SetDatabase(health.ComponentStatus{
		Available: true, Code: "available", CheckedAt: initialSuccess, LastSuccessAt: initialSuccess,
	})
	outageStart := initialSuccess.Add(time.Minute)
	outageChecked := outageStart.Add(25 * time.Millisecond)
	clock := botHealthClock(outageStart, outageChecked)
	if !checkDatabaseHealth(t.Context(), service, botDatabasePinger{err: errors.New("connection lost")}, time.Second, clock) {
		t.Fatal("checkDatabaseHealth()=false")
	}
	status := service.Snapshot().Database
	if status.Available || status.Code != "unavailable" || status.CheckedAt != outageChecked ||
		status.LastSuccessAt != initialSuccess || status.LastErrorAt != outageChecked || status.Latency != 25*time.Millisecond {
		t.Fatalf("outage status=%+v", status)
	}

	recoveryStart := outageChecked.Add(time.Minute)
	recoveryChecked := recoveryStart.Add(10 * time.Millisecond)
	clock = botHealthClock(recoveryStart, recoveryChecked)
	if !checkDatabaseHealth(t.Context(), service, botDatabasePinger{}, time.Second, clock) {
		t.Fatal("checkDatabaseHealth() recovery=false")
	}
	status = service.Snapshot().Database
	if !status.Available || status.Code != "available" || status.LastSuccessAt != recoveryChecked ||
		status.LastErrorAt != outageChecked || status.Latency != 10*time.Millisecond {
		t.Fatalf("recovery status=%+v", status)
	}
}

func TestCheckDatabaseHealthDoesNotPublishShutdownAsOutage(t *testing.T) {
	service := health.NewService()
	want := health.ComponentStatus{Available: true, Code: "available", CheckedAt: time.Unix(1, 0)}
	service.SetDatabase(want)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if checkDatabaseHealth(ctx, service, botDatabasePinger{err: context.Canceled}, time.Second, time.Now) {
		t.Fatal("checkDatabaseHealth()=true after shutdown")
	}
	if got := service.Snapshot().Database; got != want {
		t.Fatalf("database status=%+v, want unchanged %+v", got, want)
	}
}

func TestCheckDatabaseHealthMapsDeadlineToSafeCode(t *testing.T) {
	service := health.NewService()
	checkedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	clock := botHealthClock(checkedAt, checkedAt)
	if !checkDatabaseHealth(
		t.Context(), service, botDatabasePinger{err: context.DeadlineExceeded}, time.Second, clock,
	) {
		t.Fatal("checkDatabaseHealth()=false")
	}
	status := service.Snapshot().Database
	if status.Available || status.Code != "timeout" || status.LastErrorAt != checkedAt {
		t.Fatalf("database status=%+v", status)
	}
}

func TestRuntimeHealthGroupReportsAggregateLifecycle(t *testing.T) {
	service := health.NewService()
	group := newRuntimeHealthGroup(2, service.SetWorkers, time.Now)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{}, 2)
	run := group.Wrap(func(ctx context.Context) error {
		<-ctx.Done()
		return nil
	})
	for range 2 {
		go func() {
			_ = run(ctx)
			done <- struct{}{}
		}()
	}
	waitForBotComponentHealth(t, func() health.ComponentStatus { return service.Snapshot().Workers }, true, "available")
	cancel()
	<-done
	<-done
	waitForBotComponentHealth(t, func() health.ComponentStatus { return service.Snapshot().Workers }, false, "stopped")
}

func TestRecordQuoteHealthMapsFallbackAndFailure(t *testing.T) {
	service := health.NewService()
	fallbackAt := time.Unix(100, 0).UTC()
	recordQuoteHealth(service, quote.Observation{
		Outcome: quote.OutcomePNGFallback, OccurredAt: fallbackAt, Latency: 25 * time.Millisecond,
	})
	status := service.Snapshot().Quote
	if status.Available || status.Code != "degraded_fallback" || status.LastSuccessAt != fallbackAt || status.Latency != 25*time.Millisecond {
		t.Fatalf("fallback health=%+v", status)
	}
	failureAt := fallbackAt.Add(time.Minute)
	recordQuoteHealth(service, quote.Observation{Outcome: quote.OutcomeFailure, OccurredAt: failureAt, Latency: time.Second})
	status = service.Snapshot().Quote
	if status.Available || status.Code != "unavailable" || status.LastSuccessAt != fallbackAt || status.LastErrorAt != failureAt {
		t.Fatalf("failure health=%+v", status)
	}
}

func TestRunTelemetryPublishesLifecycleAndFlushesOnShutdown(t *testing.T) {
	store := &botTelemetryStore{}
	worker := newBotTelemetryWorker(t, store)
	if !worker.Record(telemetry.Observation{
		Kind: telemetry.EventGroupMessage, GroupID: 123, UserID: 456,
		Result: telemetry.ResultSuccess, OccurredAt: time.Now(),
	}) {
		t.Fatal("record telemetry event")
	}
	healthService := health.NewService()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runTelemetry(ctx, healthService, worker) }()

	waitForBotTelemetryHealth(t, healthService, true, "available")
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	waitForBotTelemetryHealth(t, healthService, false, "stopped")
	if got := store.eventCount(); got != 1 {
		t.Fatalf("flushed events=%d, want 1", got)
	}
}

func TestRunTelemetryReportsShutdownFlushFailure(t *testing.T) {
	store := &botTelemetryStore{err: errors.New("database unavailable")}
	worker := newBotTelemetryWorker(t, store)
	if !worker.Record(telemetry.Observation{
		Kind: telemetry.EventGroupMessage, GroupID: 123, UserID: 456,
		Result: telemetry.ResultSuccess, OccurredAt: time.Now(),
	}) {
		t.Fatal("record telemetry event")
	}
	healthService := health.NewService()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runTelemetry(ctx, healthService, worker) }()
	waitForBotTelemetryHealth(t, healthService, true, "available")
	cancel()
	if err := <-done; !errors.Is(err, telemetry.ErrFlushFailed) {
		t.Fatalf("runTelemetry error=%v", err)
	}
	waitForBotTelemetryHealth(t, healthService, false, "failed")
}

func newBotTelemetryWorker(t *testing.T, store telemetry.Store) *telemetry.Service {
	t.Helper()
	worker, err := telemetry.NewService(telemetry.Options{
		Store: store, HMACSecret: []byte("01234567890123456789012345678901"), Capacity: 8, BatchSize: 8,
		FlushInterval: time.Hour, FlushTimeout: time.Second, Now: time.Now, Logger: log.New(io.Discard, "", 0),
	})
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func waitForBotTelemetryHealth(t *testing.T, service *health.Service, available bool, code string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := service.Snapshot().Telemetry
		if status.Available == available && status.Code == code {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("telemetry health=%+v, want available=%t code=%s", service.Snapshot().Telemetry, available, code)
}

func waitForBotComponentHealth(t *testing.T, snapshot func() health.ComponentStatus, available bool, code string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		status := snapshot()
		if status.Available == available && status.Code == code {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("component health=%+v, want available=%t code=%s", snapshot(), available, code)
}

type botTelemetryStore struct {
	mu     sync.Mutex
	events []telemetry.Event
	err    error
}

func (s *botTelemetryStore) AppendTelemetryEvents(_ context.Context, events []telemetry.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, events...)
	return nil
}

func (s *botTelemetryStore) eventCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

type botDatabasePinger struct {
	err error
}

func (p botDatabasePinger) PingContext(context.Context) error { return p.err }

type botDatabaseCloser struct {
	calls int
	err   error
}

func (c *botDatabaseCloser) Close() error {
	c.calls++
	return c.err
}

type botApplicationRunner struct {
	calls int
	err   error
}

func (r *botApplicationRunner) Run(context.Context) error {
	r.calls++
	return r.err
}

func botHealthClock(values ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(values) {
			return values[len(values)-1]
		}
		value := values[index]
		index++
		return value
	}
}
