package telemetry

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRecordHashesUserBeforeNonBlockingQueue(t *testing.T) {
	store := &storeFake{}
	service := newTestService(t, store, 1, 1)
	if !service.Record(Observation{Kind: EventGroupMessage, GroupID: 123, UserID: 456, Result: ResultSuccess}) {
		t.Fatal("first Record() = false")
	}
	if service.Record(Observation{Kind: EventGroupMessage, GroupID: 123, UserID: 789, Result: ResultSuccess}) {
		t.Fatal("Record() = true for full queue")
	}
	if service.Dropped() != 1 {
		t.Fatalf("Dropped() = %d, want 1", service.Dropped())
	}
	event := <-service.queue
	if event.UserKey == "" || event.UserKey == "456" || strings.Contains(event.UserKey, "456") {
		t.Fatalf("UserKey = %q", event.UserKey)
	}
	if event.GroupID != "123" || event.Count != 1 {
		t.Fatalf("event = %+v", event)
	}
}

func TestRunFlushesBatchesAndShutdownTail(t *testing.T) {
	store := &storeFake{called: make(chan struct{}, 2)}
	service := newTestService(t, store, 8, 2)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	for _, userID := range []int64{1, 2, 3} {
		if !service.Record(Observation{Kind: EventCommandRun, GroupID: 10, UserID: userID, Result: ResultSuccess}) {
			t.Fatal("Record() = false")
		}
	}
	select {
	case <-store.called:
	case <-time.After(time.Second):
		t.Fatal("batch was not flushed")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.events) != 3 {
		t.Fatalf("stored event count = %d, want 3", len(store.events))
	}
}

func TestFlushFailureRetainsBatchWithoutBlockingProducer(t *testing.T) {
	store := &storeFake{err: errors.New("database password leaked")}
	service := newTestService(t, store, 2, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	if !service.Record(Observation{Kind: EventAIRequest, GroupID: 10, Result: ResultFailed}) {
		t.Fatal("first Record() = false")
	}
	time.Sleep(20 * time.Millisecond)
	for index := 0; index < 20; index++ {
		service.Record(Observation{Kind: EventAIRequest, GroupID: 10, Result: ResultFailed})
	}
	if service.Dropped() == 0 {
		t.Fatal("full queue did not report drops")
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrFlushFailed) {
		t.Fatalf("Run() error = %v, want ErrFlushFailed", err)
	} else if strings.Contains(err.Error(), "database password") {
		t.Fatalf("Run() leaked store error: %v", err)
	}
}

func TestRecordRejectsInvalidOrFreeFormLikeFields(t *testing.T) {
	service := newTestService(t, &storeFake{}, 1, 1)
	if service.Record(Observation{Kind: "arbitrary", GroupID: 1, Result: ResultSuccess}) {
		t.Fatal("Record() accepted unknown kind")
	}
	if service.Record(Observation{Kind: EventCommandRun, GroupID: 1, Result: ResultSuccess, CommandID: strings.Repeat("x", 257)}) {
		t.Fatal("Record() accepted oversized identifier")
	}
	if service.Record(Observation{Kind: EventCommandRun, GroupID: 1, Result: ResultSuccess, CommandID: " secret text "}) {
		t.Fatal("Record() accepted free-form identifier")
	}
	if service.Record(Observation{Kind: EventGroupMessage, GroupID: 1, Result: ResultSuccess, FeatureKey: "unknown_feature"}) {
		t.Fatal("Record() accepted unknown feature")
	}
	if service.Record(Observation{Kind: EventGroupMessage, GroupID: 1, Result: ResultSuccess, KnowledgeKey: "entry_1"}) {
		t.Fatal("Record() accepted incompatible knowledge key")
	}
}

func TestRecordRejectsEventsAfterShutdownAndCountsDiscardedOnce(t *testing.T) {
	store := &storeFake{err: errors.New("unavailable")}
	service := newTestService(t, store, 4, 4)
	if !service.Record(Observation{Kind: EventGroupMessage, GroupID: 1, Result: ResultSuccess}) {
		t.Fatal("initial Record() = false")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := service.Run(ctx); !errors.Is(err, ErrFlushFailed) {
		t.Fatalf("Run() error=%v", err)
	}
	if service.Record(Observation{Kind: EventGroupMessage, GroupID: 1, Result: ResultSuccess}) {
		t.Fatal("Record() accepted event after shutdown")
	}
	if service.Dropped() != 2 || service.Pending() != 0 {
		t.Fatalf("dropped=%d pending=%d", service.Dropped(), service.Pending())
	}
	if err := service.Run(t.Context()); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Run() error=%v", err)
	}
}

func newTestService(t *testing.T, store Store, capacity, batchSize int) *Service {
	t.Helper()
	service, err := NewService(Options{
		Store: store, HMACSecret: []byte("0123456789abcdef0123456789abcdef"), Capacity: capacity,
		BatchSize: batchSize, FlushInterval: time.Hour, FlushTimeout: time.Second,
		Now: func() time.Time { return time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

type storeFake struct {
	mu     sync.Mutex
	events []Event
	err    error
	called chan struct{}
}

func (s *storeFake) AppendTelemetryEvents(_ context.Context, events []Event) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	s.events = append(s.events, events...)
	s.mu.Unlock()
	if s.called != nil {
		select {
		case s.called <- struct{}{}:
		default:
		}
	}
	return nil
}
