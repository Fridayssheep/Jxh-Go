package main

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/health"
	"github.com/zjutjh/jxh-go/internal/telemetry"
)

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
