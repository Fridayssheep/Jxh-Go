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
	if !errors.Is(err, ErrNapCatUnavailable) || store.beginCalls != 0 || gateway.calls != 0 {
		t.Fatalf("error=%v store=%d gateway=%d", err, store.beginCalls, gateway.calls)
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

func newSystemFixture(t *testing.T) (*Service, *fakeSystemStore, *fakeRestartGateway) {
	t.Helper()
	healthService := health.NewService()
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
		Now: func() time.Time { return time.Unix(100, 0) }, WorkerTimeout: time.Second, MaxConcurrentWorkers: 1,
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
	mu         sync.Mutex
	operations map[string]Operation
	beginCalls int
	sequence   int
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
