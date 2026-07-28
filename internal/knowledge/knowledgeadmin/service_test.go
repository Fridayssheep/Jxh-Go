package knowledgeadmin

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

func TestKnowledgeStatusReturnsIndependentSafeSnapshot(t *testing.T) {
	version := "idx_7"
	lastError := "download_failed"
	lastAttempt := time.Date(2026, 7, 28, 1, 2, 3, 0, time.FixedZone("offset", 8*60*60))
	store := &knowledgeStoreFake{status: Status{
		State: StateDegraded, SourceConfigured: true, ActiveIndexVersion: &version,
		EntryCount: 4, ConflictCount: 1, LastAttemptAt: &lastAttempt, LastErrorCode: &lastError,
	}}
	service := newKnowledgeService(t, store, nil)

	first, err := service.GetStatus(t.Context(), knowledgeObserver())
	if err != nil {
		t.Fatal(err)
	}
	*first.ActiveIndexVersion = "changed"
	*first.LastErrorCode = "changed"
	second, err := service.GetStatus(t.Context(), knowledgeObserver())
	if err != nil {
		t.Fatal(err)
	}
	if *second.ActiveIndexVersion != "idx_7" || *second.LastErrorCode != "download_failed" {
		t.Fatalf("snapshot aliases store state: %+v", second)
	}
	if second.LastAttemptAt == nil || second.LastAttemptAt.Location() != time.UTC {
		t.Fatalf("last_attempt_at=%v", second.LastAttemptAt)
	}
}

func TestKnowledgeReloadLifecycleRejectsConcurrentOperationAndReplaysKey(t *testing.T) {
	reloader := &blockingKnowledgeReloader{
		started: make(chan struct{}), release: make(chan struct{}),
		result: ReloadResult{ActiveIndexVersion: "idx_8", EntryCount: 9, ConflictCount: 2},
	}
	store := &knowledgeStoreFake{status: Status{
		State: StateReady, SourceConfigured: true, EntryCount: 4,
	}}
	service := newKnowledgeService(t, store, reloader)

	accepted, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-key-1")
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Status != OperationAccepted || accepted.CompletedAt != nil || accepted.ErrorCode != nil {
		t.Fatalf("operation=%+v", accepted)
	}
	<-reloader.started

	replayed, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-key-1")
	if err != nil || replayed.ID != accepted.ID {
		t.Fatalf("replay=%+v error=%v", replayed, err)
	}
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-key-2"); !errors.Is(err, ErrReloadInProgress) {
		t.Fatalf("concurrent error=%v", err)
	}
	running := waitForKnowledgeStatus(t, service, OperationRunning)
	if running.State != StateReloading || running.LastAttemptAt == nil {
		t.Fatalf("running status=%+v", running)
	}

	close(reloader.release)
	succeeded := waitForKnowledgeStatus(t, service, OperationSucceeded)
	if succeeded.State != StateReady || succeeded.ActiveIndexVersion == nil || *succeeded.ActiveIndexVersion != "idx_8" ||
		succeeded.EntryCount != 9 || succeeded.ConflictCount != 2 || succeeded.LastSuccessAt == nil || succeeded.LastErrorCode != nil {
		t.Fatalf("succeeded status=%+v", succeeded)
	}
	if reloader.calls.Load() != 1 {
		t.Fatalf("reload calls=%d", reloader.calls.Load())
	}
}

func TestKnowledgeReloadFailurePreservesActiveIndexAndRedactsRawError(t *testing.T) {
	version := "idx_stable"
	reloader := &blockingKnowledgeReloader{
		started: make(chan struct{}), release: make(chan struct{}),
		err: errors.New("WPS SID=secret and https://example.invalid/share?token=secret"),
	}
	store := &knowledgeStoreFake{status: Status{
		State: StateReady, SourceConfigured: true, ActiveIndexVersion: &version,
		EntryCount: 7, ConflictCount: 1,
	}}
	service := newKnowledgeService(t, store, reloader)

	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-key-3"); err != nil {
		t.Fatal(err)
	}
	<-reloader.started
	close(reloader.release)
	failed := waitForKnowledgeStatus(t, service, OperationFailed)
	if failed.State != StateDegraded || failed.ActiveIndexVersion == nil || *failed.ActiveIndexVersion != version ||
		failed.EntryCount != 7 || failed.LastSuccessAt != nil || failed.LastErrorCode == nil || *failed.LastErrorCode != "reload_failed" {
		t.Fatalf("failed status=%+v", failed)
	}
	if strings.Contains(strings.ToLower(*failed.LastErrorCode), "sid") || strings.Contains(*failed.LastErrorCode, "secret") {
		t.Fatalf("raw error leaked: %q", *failed.LastErrorCode)
	}
}

func TestKnowledgeReloadUsesValidatedSafeErrorCode(t *testing.T) {
	reloader := &blockingKnowledgeReloader{
		started: make(chan struct{}), release: make(chan struct{}), err: codedReloadError{code: "empty_download"},
	}
	service := newKnowledgeService(t, &knowledgeStoreFake{status: Status{State: StateUnavailable, SourceConfigured: true}}, reloader)
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-key-4"); err != nil {
		t.Fatal(err)
	}
	<-reloader.started
	close(reloader.release)
	status := waitForKnowledgeStatus(t, service, OperationFailed)
	if status.State != StateUnavailable || status.LastErrorCode == nil || *status.LastErrorCode != "empty_download" {
		t.Fatalf("status=%+v", status)
	}
}

func TestKnowledgeReloadIsBoundedAndPublishesSafeCompletionEvent(t *testing.T) {
	reloader := contextKnowledgeReloader{started: make(chan struct{})}
	sink := &knowledgeEventSink{}
	service, err := NewService(Options{
		Store:      &knowledgeStoreFake{status: Status{State: StateUnavailable, SourceConfigured: true}},
		Operations: newKnowledgeOperationStoreFake(), Reloader: reloader, Events: sink,
		IdempotencySecret: []byte("01234567890123456789012345678901"), ReloadTimeout: 10 * time.Millisecond,
		Now:            func() time.Time { return time.Now().UTC() },
		NewOperationID: func() string { return "kop_timeout_1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-timeout-key"); err != nil {
		t.Fatal(err)
	}
	<-reloader.started
	status := waitForKnowledgeStatus(t, service, OperationFailed)
	if status.LastErrorCode == nil || *status.LastErrorCode != "reload_timeout" {
		t.Fatalf("status=%+v", status)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.drafts) != 1 || sink.drafts[0].Type != events.EventKnowledgeReloadCompleted ||
		sink.drafts[0].Resource == nil || sink.drafts[0].Resource.ID != "kop_timeout_1" || sink.drafts[0].Reason != "failed" {
		t.Fatalf("drafts=%+v", sink.drafts)
	}
}

func TestKnowledgeReloadRetriesTerminalPersistence(t *testing.T) {
	reloader := &blockingKnowledgeReloader{
		started: make(chan struct{}), release: make(chan struct{}),
		result: ReloadResult{ActiveIndexVersion: "idx_retry", EntryCount: 3, ConflictCount: 1},
	}
	operations := &flakyKnowledgeOperationStore{
		knowledgeOperationStoreFake: newKnowledgeOperationStoreFake(), terminalFailures: 2,
	}
	sink := &knowledgeEventSink{}
	service, err := NewService(Options{
		Store:      &knowledgeStoreFake{status: Status{State: StateReady, SourceConfigured: true}},
		Operations: operations, Reloader: reloader, Events: sink,
		IdempotencySecret:     []byte("01234567890123456789012345678901"),
		PersistenceRetryDelay: time.Millisecond, Now: func() time.Time { return time.Now().UTC() },
		NewOperationID: func() string { return "kop_retry_1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-retry-key"); err != nil {
		t.Fatal(err)
	}
	<-reloader.started
	close(reloader.release)
	status := waitForKnowledgeStatus(t, service, OperationSucceeded)
	if status.ActiveIndexVersion == nil || *status.ActiveIndexVersion != "idx_retry" {
		t.Fatalf("status=%+v", status)
	}
	if got := operations.terminalAttempts.Load(); got != 3 {
		t.Fatalf("terminal persistence attempts=%d, want 3", got)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if len(sink.drafts) != 1 || sink.drafts[0].Reason != "succeeded" {
		t.Fatalf("drafts=%+v", sink.drafts)
	}
}

func TestKnowledgeCloseCancelsAndWaitsForReloadWorker(t *testing.T) {
	reloader := contextKnowledgeReloader{started: make(chan struct{})}
	operations := newKnowledgeOperationStoreFake()
	service, err := NewService(Options{
		Store:      &knowledgeStoreFake{status: Status{State: StateReady, SourceConfigured: true}},
		Operations: operations, Reloader: reloader,
		IdempotencySecret: []byte("01234567890123456789012345678901"), ReloadTimeout: time.Minute,
		PersistenceRetryDelay: time.Millisecond, Now: func() time.Time { return time.Now().UTC() },
		NewOperationID: func() string { return "kop_close_1" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-close-key"); err != nil {
		t.Fatal(err)
	}
	<-reloader.started
	done := make(chan struct{})
	go func() {
		service.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for and cancel the reload worker")
	}
	service.Close()
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-after-close"); !errors.Is(err, ErrReloaderUnavailable) {
		t.Fatalf("StartReload after Close error=%v", err)
	}
}

func TestKnowledgeReloaderUnavailableAndAuthorizationPrecedeDependencies(t *testing.T) {
	store := &knowledgeStoreFake{status: Status{State: StateReady}}
	service := newKnowledgeService(t, store, nil)
	unauthorized := auth.Principal{Role: "invalid"}

	if _, err := service.GetStatus(t.Context(), unauthorized); !errors.Is(err, ErrForbidden) {
		t.Fatalf("status error=%v", err)
	}
	if _, err := service.ListEntries(t.Context(), unauthorized, EntryQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("entries error=%v", err)
	}
	if _, err := service.GetEntry(t.Context(), unauthorized, "entry_1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("entry error=%v", err)
	}
	if _, err := service.ListConflicts(t.Context(), unauthorized, ConflictQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("conflicts error=%v", err)
	}
	if _, err := service.StartReload(t.Context(), knowledgeObserver(), "reload-key-5"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("observer reload error=%v", err)
	}
	if _, err := service.StartReload(t.Context(), auth.Principal{Role: auth.RoleMaintainer}, "reload-key-5"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty actor reload error=%v", err)
	}
	if store.totalCalls() != 0 {
		t.Fatalf("unauthorized requests reached store: %d", store.totalCalls())
	}
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-key-5"); !errors.Is(err, ErrReloaderUnavailable) {
		t.Fatalf("unavailable error=%v", err)
	}
}

func TestKnowledgeReadQueriesValidateAndReturnIndependentPages(t *testing.T) {
	updated := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	store := &knowledgeStoreFake{
		status: Status{State: StateReady},
		entryPage: EntryPage{Items: []EntrySummary{{
			ID: "entry_1", Title: "Title", Category: "FAQ", Type: EntryTypeHybrid,
			Keywords: []string{"one"}, Aliases: []string{"alias"}, SourceUpdatedAt: &updated, IndexedAt: updated,
		}}, NextCursor: "cursor_2", HasMore: true},
		entry: Entry{
			ID: "entry_1", SourceKey: "source-1", Title: "Title", Category: "FAQ", Type: EntryTypeHybrid,
			Keywords: []string{"one"}, Aliases: []string{"alias"}, Question: "Q", Answer: "A", IndexedAt: updated,
		},
		conflictPage: ConflictPage{Items: []Conflict{{
			ID: "conflict_1", Type: ConflictKeyword, Key: "one", EntryIDs: []string{"entry_1", "entry_2"}, DetectedAt: updated,
		}}},
	}
	service := newKnowledgeService(t, store, nil)
	principal := knowledgeObserver()
	flag := true

	entries, err := service.ListEntries(t.Context(), principal, EntryQuery{Query: "Title", Enabled: &flag})
	if err != nil {
		t.Fatal(err)
	}
	if store.lastEntryQuery().Limit != 50 {
		t.Fatalf("query=%+v", store.lastEntryQuery())
	}
	entries.Items[0].Keywords[0] = "changed"
	if store.entryPage.Items[0].Keywords[0] != "one" {
		t.Fatal("entry page aliases store data")
	}
	entry, err := service.GetEntry(t.Context(), principal, "entry_1")
	if err != nil {
		t.Fatal(err)
	}
	entry.Aliases[0] = "changed"
	if store.entry.Aliases[0] != "alias" {
		t.Fatal("entry aliases store data")
	}
	conflicts, err := service.ListConflicts(t.Context(), principal, ConflictQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if store.lastConflictQuery().Limit != 50 {
		t.Fatalf("query=%+v", store.lastConflictQuery())
	}
	conflicts.Items[0].EntryIDs[0] = "changed"
	if store.conflictPage.Items[0].EntryIDs[0] != "entry_1" {
		t.Fatal("conflict page aliases store data")
	}
}

func TestKnowledgeInvalidQueriesAndMissingEntryDoNotReturnStoreData(t *testing.T) {
	store := &knowledgeStoreFake{status: Status{State: StateReady}}
	service := newKnowledgeService(t, store, nil)
	principal := knowledgeObserver()

	for _, query := range []EntryQuery{
		{Query: strings.Repeat("x", 201)}, {Type: "unknown"}, {Limit: 101}, {Cursor: strings.Repeat("x", 2049)},
	} {
		if _, err := service.ListEntries(t.Context(), principal, query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("query=%+v error=%v", query, err)
		}
	}
	for _, query := range []ConflictQuery{{Query: strings.Repeat("x", 201)}, {Type: "unknown"}, {Limit: -1}} {
		if _, err := service.ListConflicts(t.Context(), principal, query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("query=%+v error=%v", query, err)
		}
	}
	if _, err := service.GetEntry(t.Context(), principal, strings.Repeat("x", 257)); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid id error=%v", err)
	}
	if _, err := service.GetEntry(t.Context(), principal, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error=%v", err)
	}
	if store.entryListCalls.Load() != 0 || store.conflictListCalls.Load() != 0 || store.entryGetCalls.Load() != 1 {
		t.Fatalf("calls list=%d conflicts=%d get=%d", store.entryListCalls.Load(), store.conflictListCalls.Load(), store.entryGetCalls.Load())
	}
}

func TestKnowledgeStatusIsRaceSafeDuringReload(t *testing.T) {
	reloader := &blockingKnowledgeReloader{
		started: make(chan struct{}), release: make(chan struct{}),
		result: ReloadResult{ActiveIndexVersion: "idx_race", EntryCount: 2},
	}
	service := newKnowledgeService(t, &knowledgeStoreFake{status: Status{State: StateReady, SourceConfigured: true}}, reloader)
	if _, err := service.StartReload(t.Context(), knowledgeMaintainer(), "reload-race-key"); err != nil {
		t.Fatal(err)
	}
	<-reloader.started

	var readers sync.WaitGroup
	for range 16 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for range 100 {
				if _, err := service.GetStatus(t.Context(), knowledgeObserver()); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	close(reloader.release)
	readers.Wait()
	status := waitForKnowledgeStatus(t, service, OperationSucceeded)
	if status.ActiveIndexVersion == nil || *status.ActiveIndexVersion != "idx_race" {
		t.Fatalf("status=%+v", status)
	}
}

func TestKnowledgeRecoveryMarksInterruptedReloadFailed(t *testing.T) {
	store := newKnowledgeOperationStoreFake()
	completedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	code := "reload_interrupted"
	store.recovered = []ReloadOperation{{
		ID: "kop_recovered", Status: OperationFailed, StartedAt: completedAt.Add(-time.Minute),
		CompletedAt: &completedAt, ErrorCode: &code,
	}}
	service, err := NewService(Options{
		Store:      &knowledgeStoreFake{status: Status{State: StateReady, SourceConfigured: true}},
		Operations: store, Now: func() time.Time { return completedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	count, err := service.RecoverInterrupted(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("recovery count=%d error=%v", count, err)
	}
	status, err := service.GetStatus(t.Context(), knowledgeObserver())
	if err != nil || status.CurrentOperation == nil || status.CurrentOperation.Status != OperationFailed ||
		status.LastErrorCode == nil || *status.LastErrorCode != code {
		t.Fatalf("recovered status=%+v error=%v", status, err)
	}
}

func newKnowledgeService(t *testing.T, store Store, reloader Reloader) *Service {
	t.Helper()
	var clock atomic.Int64
	options := Options{
		Store: store, Reloader: reloader,
		Now: func() time.Time {
			return time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC).Add(time.Duration(clock.Add(1)) * time.Second)
		},
		NewOperationID: func() string { return "kop_test_1" },
	}
	if reloader != nil {
		options.Operations = newKnowledgeOperationStoreFake()
		options.IdempotencySecret = []byte("01234567890123456789012345678901")
	}
	service, err := NewService(options)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type knowledgeOperationStoreFake struct {
	mu         sync.Mutex
	operations map[string]ReloadOperation
	keys       map[string]string
	recovered  []ReloadOperation
}

type flakyKnowledgeOperationStore struct {
	*knowledgeOperationStoreFake
	terminalFailures int64
	terminalAttempts atomic.Int64
}

func (s *flakyKnowledgeOperationStore) TransitionKnowledgeReload(
	ctx context.Context,
	transition ReloadTransition,
) (ReloadOperation, error) {
	if managerKnowledgeTestTerminal(transition.To) {
		attempt := s.terminalAttempts.Add(1)
		if attempt <= s.terminalFailures {
			return ReloadOperation{}, errors.New("transient persistence failure")
		}
	}
	return s.knowledgeOperationStoreFake.TransitionKnowledgeReload(ctx, transition)
}

func newKnowledgeOperationStoreFake() *knowledgeOperationStoreFake {
	return &knowledgeOperationStoreFake{operations: make(map[string]ReloadOperation), keys: make(map[string]string)}
}

func (s *knowledgeOperationStoreFake) BeginKnowledgeReload(_ context.Context, begin BeginReload) (ReloadOperation, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := begin.Actor.UserID + "\x00" + begin.IdempotencyKey
	if id, exists := s.keys[key]; exists {
		return cloneOperation(s.operations[id]), false, nil
	}
	for _, operation := range s.operations {
		if operationInProgress(operation.Status) {
			return ReloadOperation{}, false, ErrReloadInProgress
		}
	}
	operation := ReloadOperation{ID: begin.OperationID, Status: OperationAccepted, StartedAt: begin.RequestedAt}
	s.keys[key] = operation.ID
	s.operations[operation.ID] = operation
	return cloneOperation(operation), true, nil
}

func (s *knowledgeOperationStoreFake) TransitionKnowledgeReload(_ context.Context, transition ReloadTransition) (ReloadOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operation := s.operations[transition.OperationID]
	if operation.Status != transition.From {
		return ReloadOperation{}, errors.New("unexpected knowledge operation state")
	}
	operation.Status = transition.To
	if managerKnowledgeTestTerminal(transition.To) {
		completedAt := transition.At
		operation.CompletedAt = &completedAt
		if transition.ErrorCode != "" {
			code := transition.ErrorCode
			operation.ErrorCode = &code
		}
	}
	s.operations[operation.ID] = operation
	return cloneOperation(operation), nil
}

func (s *knowledgeOperationStoreFake) RecoverInterruptedKnowledgeReloads(context.Context, time.Time) ([]ReloadOperation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	operations := make([]ReloadOperation, len(s.recovered))
	for index := range s.recovered {
		operations[index] = cloneOperation(s.recovered[index])
	}
	s.recovered = nil
	return operations, nil
}

func managerKnowledgeTestTerminal(status OperationStatus) bool {
	return status == OperationSucceeded || status == OperationFailed
}

func knowledgeObserver() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleObserver}
}

func knowledgeMaintainer() auth.Principal {
	return auth.Principal{UserID: "usr_2", SessionID: "ses_2", Role: auth.RoleMaintainer}
}

func waitForKnowledgeStatus(t *testing.T, service *Service, wanted OperationStatus) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := service.GetStatus(t.Context(), knowledgeObserver())
		if err != nil {
			t.Fatal(err)
		}
		if status.CurrentOperation != nil && status.CurrentOperation.Status == wanted {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation did not reach %s", wanted)
	return Status{}
}

type knowledgeStoreFake struct {
	mu sync.Mutex

	status       Status
	statusErr    error
	entryPage    EntryPage
	entryListErr error
	entry        Entry
	entryFound   bool
	entryGetErr  error
	conflictPage ConflictPage
	conflictErr  error

	statusCalls       atomic.Int64
	entryListCalls    atomic.Int64
	entryGetCalls     atomic.Int64
	conflictListCalls atomic.Int64
	entryQuery        EntryQuery
	conflictQuery     ConflictQuery
}

func (s *knowledgeStoreFake) Status(context.Context) (Status, error) {
	s.statusCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStatus(s.status), s.statusErr
}

func (s *knowledgeStoreFake) ListEntries(_ context.Context, query EntryQuery) (EntryPage, error) {
	s.entryListCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entryQuery = query
	return s.entryPage, s.entryListErr
}

func (s *knowledgeStoreFake) GetEntry(_ context.Context, _ string) (Entry, bool, error) {
	s.entryGetCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	found := s.entryFound
	if s.entry.ID != "" {
		found = true
	}
	return s.entry, found, s.entryGetErr
}

func (s *knowledgeStoreFake) ListConflicts(_ context.Context, query ConflictQuery) (ConflictPage, error) {
	s.conflictListCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conflictQuery = query
	return s.conflictPage, s.conflictErr
}

func (s *knowledgeStoreFake) lastEntryQuery() EntryQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entryQuery
}

func (s *knowledgeStoreFake) lastConflictQuery() ConflictQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conflictQuery
}

func (s *knowledgeStoreFake) totalCalls() int64 {
	return s.statusCalls.Load() + s.entryListCalls.Load() + s.entryGetCalls.Load() + s.conflictListCalls.Load()
}

type blockingKnowledgeReloader struct {
	started chan struct{}
	release chan struct{}
	result  ReloadResult
	err     error
	calls   atomic.Int64
	once    sync.Once
}

func (r *blockingKnowledgeReloader) Reload(context.Context) (ReloadResult, error) {
	r.calls.Add(1)
	r.once.Do(func() { close(r.started) })
	<-r.release
	return r.result, r.err
}

type codedReloadError struct {
	code string
}

type contextKnowledgeReloader struct {
	started chan struct{}
}

func (r contextKnowledgeReloader) Reload(ctx context.Context) (ReloadResult, error) {
	close(r.started)
	<-ctx.Done()
	return ReloadResult{}, ctx.Err()
}

type knowledgeEventSink struct {
	mu     sync.Mutex
	drafts []events.Draft
}

func (s *knowledgeEventSink) Publish(draft events.Draft) (events.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.drafts = append(s.drafts, draft)
	return events.Event{}, nil
}

func (e codedReloadError) Error() string {
	return "upstream details that must not be exposed"
}

func (e codedReloadError) SafeErrorCode() string {
	return e.code
}
