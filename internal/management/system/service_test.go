package system

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	platformconfig "github.com/zjutjh/jxh-go/internal/platform/config"
	"github.com/zjutjh/jxh-go/internal/platform/health"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
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

func TestConfigurationReadAndUpdateUseSeparatePermissions(t *testing.T) {
	service, _, _ := newSystemFixture(t)
	defer service.Close()

	document, err := service.Configuration(t.Context(), auth.Principal{Role: auth.RoleObserver})
	if err != nil || document.Version != 7 || document.AppliedVersion != 6 || !document.RestartRequired || !document.RestartSupported ||
		document.WPS.Sheet != "release" {
		t.Fatalf("configuration=%+v error=%v", document, err)
	}
	patch := platformconfig.SettingsPatch{WPS: &platformconfig.WPSSettingsPatch{Sheet: stringPointerForSystemTest("next")}}
	request := auth.MutationContext{RequestID: "req_configuration", IPAddress: "127.0.0.1", UserAgent: "test"}
	if _, err := service.UpdateConfiguration(t.Context(), auth.Principal{Role: auth.RoleMaintainer}, 7, patch, request); !errors.Is(err, ErrForbidden) {
		t.Fatalf("maintainer update error = %v, want ErrForbidden", err)
	}
	updated, err := service.UpdateConfiguration(t.Context(), superPrincipal(), 7, patch, request)
	if err != nil || updated.Version != 8 || updated.WPS.Sheet != "next" || !updated.RestartRequired {
		t.Fatalf("updated configuration=%+v error=%v", updated, err)
	}
	editor := service.configuration.(*fakeConfigurationEditor)
	if editor.expectedVersion != 7 || editor.patch.WPS == nil || editor.patch.WPS.Sheet == nil || *editor.patch.WPS.Sheet != "next" {
		t.Fatalf("editor input = version %d patch %#v", editor.expectedVersion, editor.patch)
	}
}

func TestConfigurationMapsEditorFailuresToDomainErrors(t *testing.T) {
	service, _, _ := newSystemFixture(t)
	defer service.Close()
	editor := service.configuration.(*fakeConfigurationEditor)

	editor.readErr = platformconfig.ErrInvalidDocument
	if _, err := service.Configuration(t.Context(), auth.Principal{Role: auth.RoleObserver}); !errors.Is(err, ErrConfigurationUnavailable) {
		t.Fatalf("read error = %v, want ErrConfigurationUnavailable", err)
	}

	editor.readErr = nil
	editor.updateErr = platformconfig.ErrInvalidDocument
	request := auth.MutationContext{RequestID: "req_configuration"}
	patch := platformconfig.SettingsPatch{WPS: &platformconfig.WPSSettingsPatch{Sheet: stringPointerForSystemTest("next")}}
	if _, err := service.UpdateConfiguration(t.Context(), superPrincipal(), 7, patch, request); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid update error = %v, want ErrInvalidInput", err)
	}
	editor.updateErr = platformconfig.ErrVersionConflict
	if _, err := service.UpdateConfiguration(t.Context(), superPrincipal(), 7, patch, request); !errors.Is(err, ErrConfigurationVersionConflict) {
		t.Fatalf("stale update error = %v, want ErrConfigurationVersionConflict", err)
	}
	editor.updateErr = errors.New("disk unavailable")
	if _, err := service.UpdateConfiguration(t.Context(), superPrincipal(), 7, patch, request); !errors.Is(err, ErrConfigurationUnavailable) {
		t.Fatalf("storage error = %v, want ErrConfigurationUnavailable", err)
	}
}

func TestUpdateConfigurationRecordsOnlyChangedPathsInAudit(t *testing.T) {
	service, store, _ := newSystemFixture(t)
	defer service.Close()
	patch := platformconfig.SettingsPatch{
		WPS: &platformconfig.WPSSettingsPatch{SID: &platformconfig.SecretUpdate{Operation: platformconfig.SecretReplace, Value: "never-audit-this"}},
		AI:  &platformconfig.AISettingsPatch{TimeoutSec: intPointerForSystemTest(45)},
	}
	request := auth.MutationContext{RequestID: "req_configuration", IPAddress: "127.0.0.1", UserAgent: "test"}
	if _, err := service.UpdateConfiguration(t.Context(), superPrincipal(), 7, patch, request); err != nil {
		t.Fatal(err)
	}
	if store.configurationAudit.Actor.UserID != "usr_1" || store.configurationAudit.Context != request ||
		!reflect.DeepEqual(store.configurationAudit.Fields, []string{"ai.timeout_sec", "wps.sid"}) || store.configurationAudit.ExpectedVersion != 7 {
		t.Fatalf("audit request = %#v", store.configurationAudit)
	}
	if !reflect.DeepEqual(store.configurationCompletion.Fields, []string{"ai.timeout_sec", "wps.sid"}) ||
		store.configurationCompletion.Version != 8 || store.configurationCompletion.Result != ConfigurationAuditSuccess {
		t.Fatalf("audit completion = %#v", store.configurationCompletion)
	}
	if strings.Contains(fmt.Sprintf("%#v %#v", store.configurationAudit, store.configurationCompletion), "never-audit-this") {
		t.Fatal("audit captured a secret value")
	}
}

func TestUpdateConfigurationRetriesFinalAuditWithoutWritingFileAgain(t *testing.T) {
	service, store, _ := newSystemFixture(t)
	defer service.Close()
	store.configurationCompletionFailures = 1
	patch := platformconfig.SettingsPatch{WPS: &platformconfig.WPSSettingsPatch{Sheet: stringPointerForSystemTest("next")}}
	if _, err := service.UpdateConfiguration(t.Context(), superPrincipal(), 7, patch, auth.MutationContext{RequestID: "req_configuration"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		store.mu.Lock()
		calls := store.configurationCompletionCalls
		store.mu.Unlock()
		if calls >= 2 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	editor := service.configuration.(*fakeConfigurationEditor)
	if editor.updateCalls != 1 {
		t.Fatalf("editor update calls = %d", editor.updateCalls)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.configurationCompletionCalls != 2 {
		t.Fatalf("audit completion calls = %d", store.configurationCompletionCalls)
	}
}

func TestConfigurationIsUnavailableWithoutAnEditor(t *testing.T) {
	service, _, _ := newSystemFixture(t)
	defer service.Close()
	service.configuration = nil

	if _, err := service.Configuration(t.Context(), auth.Principal{Role: auth.RoleObserver}); !errors.Is(err, ErrConfigurationUnavailable) {
		t.Fatalf("configuration error = %v, want ErrConfigurationUnavailable", err)
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
		Configuration: &fakeConfigurationEditor{document: platformconfig.Settings{
			WPS:       platformconfig.WPSSettings{Sheet: "release", TimeoutSec: 120},
			AI:        platformconfig.AISettings{Provider: "openai", TimeoutSec: 30, MaxQuestionChars: 500},
			Quote:     platformconfig.QuoteSettings{TimeoutSec: 10},
			Time:      platformconfig.TimeSettings{AppTimezone: "Asia/Shanghai", SchedulerTimezone: "Asia/Shanghai"},
			Retention: platformconfig.RetentionSettings{TriggerLogRetentionDays: 180}, Version: 7,
		}},
		AppliedConfigurationVersion: 6, RestartSupported: true,
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

type fakeConfigurationEditor struct {
	document        platformconfig.Settings
	readErr         error
	updateErr       error
	expectedVersion uint64
	patch           platformconfig.SettingsPatch
	updateCalls     int
}

func (e *fakeConfigurationEditor) Read(context.Context) (platformconfig.Settings, error) {
	return e.document, e.readErr
}

func (e *fakeConfigurationEditor) Update(_ context.Context, version uint64, patch platformconfig.SettingsPatch) (platformconfig.Settings, []string, error) {
	e.updateCalls++
	if e.updateErr != nil {
		return platformconfig.Settings{}, nil, e.updateErr
	}
	e.expectedVersion = version
	e.patch = patch
	if patch.WPS != nil && patch.WPS.Sheet != nil {
		e.document.WPS.Sheet = *patch.WPS.Sheet
	}
	e.document.Version++
	return e.document, patch.Paths(), nil
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
	mu                              sync.Mutex
	operations                      map[string]Operation
	beginCalls                      int
	sequence                        int
	recovered                       []Operation
	recoveredAt                     time.Time
	findCalls                       int
	found                           bool
	replay                          Operation
	terminalFailures                int
	terminalCalls                   int
	configurationAudit              ConfigurationAuditRequest
	configurationCompletion         ConfigurationAuditCompletion
	configurationCompletionCalls    int
	configurationCompletionFailures int
}

func (s *fakeSystemStore) BeginConfigurationUpdate(_ context.Context, request ConfigurationAuditRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configurationAudit = request
	return "aud_configuration", nil
}

func (s *fakeSystemStore) CompleteConfigurationUpdate(_ context.Context, completion ConfigurationAuditCompletion) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.configurationCompletionCalls++
	if s.configurationCompletionFailures > 0 {
		s.configurationCompletionFailures--
		return errors.New("temporary audit failure")
	}
	s.configurationCompletion = completion
	return nil
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
func intPointerForSystemTest(value int) *int          { return &value }
