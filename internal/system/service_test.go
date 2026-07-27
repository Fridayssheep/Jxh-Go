package system

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/health"
	"github.com/zjutjh/jxh-go/internal/napcat"
)

func TestRestartReturnsUnavailableBeforeSideEffect(t *testing.T) {
	service, store, gateway := newSystemFixture(t)
	gateway.connected = false
	_, err := service.RestartNapCat(t.Context(), superPrincipal(), RestartInput{Confirmation: "restart"}, "restart-key", auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrNapCatUnavailable) || store.findCalls != 1 || store.beginCalls != 0 || gateway.calls != 0 {
		t.Fatalf("error=%v find=%d store=%d gateway=%d", err, store.findCalls, store.beginCalls, gateway.calls)
	}
}

func TestRestartReplaysBeforeCheckingCurrentConnection(t *testing.T) {
	service, store, gateway := newSystemFixture(t)
	defer service.Close()
	gateway.connected = false
	completedAt := time.Unix(99, 0).UTC()
	store.found = true
	store.replay = Operation{
		ID: "op_prior", Type: "napcat_restart", Status: StatusSucceeded,
		RequestedAt: time.Unix(90, 0).UTC(), CompletedAt: &completedAt,
	}
	operation, err := service.RestartNapCat(t.Context(), superPrincipal(), RestartInput{Confirmation: "restart"}, "restart-key", auth.MutationContext{RequestID: "req_1"})
	if err != nil || operation.ID != "op_prior" || store.beginCalls != 0 || gateway.calls != 0 {
		t.Fatalf("operation=%+v error=%v begin=%d gateway=%d", operation, err, store.beginCalls, gateway.calls)
	}
}

func TestAcceptedRestartPersistsUnknownOnDisconnect(t *testing.T) {
	service, store, gateway := newSystemFixture(t)
	defer service.Close()
	gateway.err = napcat.ErrUnavailable
	operation, err := service.RestartNapCat(t.Context(), superPrincipal(), RestartInput{Confirmation: "restart"}, "restart-key", auth.MutationContext{RequestID: "req_1"})
	if err != nil || operation.Status != StatusAccepted {
		t.Fatalf("operation=%+v error=%v", operation, err)
	}
	waitForOperation(t, store, operation.ID, StatusUnknown)
	stored := store.operation(operation.ID)
	if stored.ErrorCode == nil || *stored.ErrorCode != "restart_outcome_unknown" || gateway.calls != 1 {
		t.Fatalf("operation=%+v gateway=%d", stored, gateway.calls)
	}
}

func TestAcceptedRestartRetriesTerminalPersistenceWithoutRepeatingSideEffect(t *testing.T) {
	service, store, gateway := newSystemFixture(t)
	defer service.Close()
	store.terminalFailures = 2
	operation, err := service.RestartNapCat(t.Context(), superPrincipal(), RestartInput{Confirmation: "restart"}, "restart-key", auth.MutationContext{RequestID: "req_1"})
	if err != nil {
		t.Fatal(err)
	}
	waitForOperation(t, store, operation.ID, StatusSucceeded)
	if gateway.calls != 1 || store.terminalCalls != 3 {
		t.Fatalf("gateway calls=%d terminal calls=%d", gateway.calls, store.terminalCalls)
	}
}

func TestRestartAuthorizesAndValidatesBeforeStore(t *testing.T) {
	service, store, gateway := newSystemFixture(t)
	defer service.Close()
	_, err := service.RestartNapCat(t.Context(), auth.Principal{Role: auth.RoleMaintainer}, RestartInput{Confirmation: "restart"}, "restart-key", auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
	_, err = service.RestartNapCat(t.Context(), superPrincipal(), RestartInput{Confirmation: "wrong"}, "restart-key", auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrInvalidInput) || store.beginCalls != 0 || gateway.calls != 0 {
		t.Fatalf("error=%v store=%d gateway=%d", err, store.beginCalls, gateway.calls)
	}
}

func TestHealthMapsComponentsWithoutProbingDependencies(t *testing.T) {
	service, _, _ := newSystemFixture(t)
	defer service.Close()
	got, err := service.Health(t.Context(), auth.Principal{Role: auth.RoleObserver})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Live || !got.Ready || len(got.Dependencies) != 8 || got.Dependencies[0].Status != DependencyHealthy ||
		got.Dependencies[1].Status != DependencyUnavailable || got.Dependencies[7].Status != DependencyHealthy {
		t.Fatalf("health=%+v", got)
	}
}

func TestRecoverInterruptedPublishesUnknownOperations(t *testing.T) {
	service, store, _ := newSystemFixture(t)
	defer service.Close()
	requestedAt := time.Unix(50, 0).UTC()
	store.recovered = []Operation{
		{ID: "op_recovered", Type: "napcat_restart", Status: StatusUnknown, RequestedAt: requestedAt,
			CompletedAt: timePointerForSystemTest(time.Unix(100, 0).UTC()), ErrorCode: stringPointerForSystemTest("restart_interrupted")},
	}
	count, err := service.RecoverInterrupted(t.Context())
	if err != nil || count != 1 || !store.recoveredAt.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("recovery count=%d at=%s error=%v", count, store.recoveredAt, err)
	}
}

func newSystemFixture(t *testing.T) (*Service, *fakeSystemStore, *fakeRestartGateway) {
	t.Helper()
	healthService := health.NewService()
	healthService.SetAdmin(health.ComponentStatus{Available: true, Code: "available", CheckedAt: time.Unix(1, 0)})
	healthService.SetDatabase(health.ComponentStatus{Available: true, Code: "available", CheckedAt: time.Unix(1, 0)})
	healthService.SetNapCat(health.ComponentStatus{Available: false, Code: "napcat_unavailable", CheckedAt: time.Unix(1, 0)})
	healthService.SetTelemetry(health.ComponentStatus{Available: true, Code: "available", CheckedAt: time.Unix(1, 0)})
	store := &fakeSystemStore{operations: make(map[string]Operation)}
	gateway := &fakeRestartGateway{connected: true}
	service, err := NewService(Options{
		Store: store, Health: healthService, Gateway: gateway, IdempotencySecret: []byte("01234567890123456789012345678901"),
		Dependencies: map[DependencyKey]DependencyConfiguration{
			DependencyMySQL: {Configured: true, Required: true}, DependencyNapCat: {Configured: true},
			DependencyTelemetry: {Configured: true},
		},
		Now: func() time.Time { return time.Unix(100, 0) }, WorkerTimeout: time.Second,
		TransitionRetryDelay: time.Millisecond, MaxConcurrentWorkers: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, gateway
}

func superPrincipal() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleSuperAdmin}
}

type fakeRestartGateway struct {
	mu        sync.Mutex
	connected bool
	err       error
	calls     int
}

func (g *fakeRestartGateway) Snapshot() napcat.Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return napcat.Snapshot{Connected: g.connected}
}

func (g *fakeRestartGateway) SetRestart(context.Context) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	return g.err
}

type fakeSystemStore struct {
	mu               sync.Mutex
	operations       map[string]Operation
	beginCalls       int
	sequence         int
	recovered        []Operation
	recoveredAt      time.Time
	findCalls        int
	found            bool
	replay           Operation
	terminalFailures int
	terminalCalls    int
}

func (s *fakeSystemStore) FindNapCatRestart(_ context.Context, _ FindRestart) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.findCalls++
	return cloneOperation(s.replay), s.found, nil
}

func (s *fakeSystemStore) BeginNapCatRestart(_ context.Context, begin BeginRestart) (Operation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.beginCalls++
	s.sequence++
	operation := Operation{ID: "op_1", Type: "napcat_restart", Status: StatusAccepted, RequestedAt: begin.RequestedAt}
	s.operations[operation.ID] = operation
	return operation, true, nil
}

func (s *fakeSystemStore) TransitionNapCatRestart(_ context.Context, transition Transition) (Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if transition.To == StatusSucceeded || transition.To == StatusFailed || transition.To == StatusUnknown {
		s.terminalCalls++
		if s.terminalFailures > 0 {
			s.terminalFailures--
			return Operation{}, errors.New("temporary database failure")
		}
	}
	operation := s.operations[transition.OperationID]
	if operation.Status != transition.From {
		return Operation{}, errors.New("unexpected operation state")
	}
	operation.Status = transition.To
	if transition.To == StatusSucceeded || transition.To == StatusFailed || transition.To == StatusUnknown {
		completedAt := transition.At
		operation.CompletedAt = &completedAt
		if transition.ErrorCode != "" {
			code := transition.ErrorCode
			operation.ErrorCode = &code
		}
	}
	s.operations[operation.ID] = operation
	return operation, nil
}

func (s *fakeSystemStore) RecoverInterruptedNapCatRestarts(_ context.Context, recoveredAt time.Time) ([]Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoveredAt = recoveredAt
	operations := make([]Operation, len(s.recovered))
	for index := range s.recovered {
		operations[index] = cloneOperation(s.recovered[index])
	}
	return operations, nil
}

func (s *fakeSystemStore) operation(id string) Operation {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneOperation(s.operations[id])
}

func waitForOperation(t *testing.T, store *fakeSystemStore, id string, status OperationStatus) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if store.operation(id).Status == status {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %s did not reach %s: %+v", id, status, store.operation(id))
}

func timePointerForSystemTest(value time.Time) *time.Time { return &value }

func stringPointerForSystemTest(value string) *string { return &value }
