package groups

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
)

func TestListUsesEffectiveStalenessAndReturnsIndependentFeatures(t *testing.T) {
	service, store, gateway, _ := newFixture(t)
	group := groupFixture()
	store.page = Page{Items: []Group{group}}
	gateway.snapshot.Connected = false
	page, err := service.List(t.Context(), reader(), ListQuery{SnapshotState: SnapshotStale})
	if err != nil {
		t.Fatal(err)
	}
	if !store.listQuery.ForceStale || page.Items[0].SnapshotState != SnapshotStale || store.listQuery.Limit != 50 {
		t.Fatalf("query=%+v page=%+v", store.listQuery, page)
	}
	page.Items[0].Features[0].Enabled = !page.Items[0].Features[0].Enabled
	if page.Items[0].Features[0].Enabled == store.page.Items[0].Features[0].Enabled {
		t.Fatal("returned features alias store data")
	}
}

func TestSyncPersistsValidatedDirectoryAndDegradesRoleFailures(t *testing.T) {
	service, store, gateway, publisher := newFixture(t)
	gateway.snapshot.Connected = true
	gateway.groups = []napcat.GroupInfo{
		{ID: 123, Name: "Alpha", MemberCount: 10, MaxMemberCount: 100},
		{ID: 456, Name: "Beta", MemberCount: 20, MaxMemberCount: 200},
	}
	gateway.selfID = 789
	gateway.roles = map[int64]string{123: "admin"}
	gateway.roleErrors = map[int64]error{456: errors.New("upstream token=secret")}
	store.reservation = SyncReservation{ExecutionID: "sync_1", Fresh: true}
	store.result = SyncResult{SyncedAt: time.Unix(101, 0).UTC(), AddedCount: 1, UpdatedCount: 1, TotalCount: 2}
	result, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"})
	if err != nil || result.TotalCount != 2 {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if len(store.completion.Groups) != 2 || store.completion.Groups[0].ID != "123" ||
		store.completion.Groups[0].BotRole != RoleAdmin || store.completion.Groups[1].BotRole != RoleUnknown {
		t.Fatalf("completion=%+v", store.completion)
	}
	if len(publisher.drafts) != 2 || publisher.drafts[0].Type != events.EventGroupUpdated || publisher.drafts[1].Type != events.EventOverviewUpdated {
		t.Fatalf("events=%+v", publisher.drafts)
	}
}

func TestSyncUnavailableRecordsFailureWithoutChangingDirectory(t *testing.T) {
	service, store, gateway, _ := newFixture(t)
	gateway.snapshot.Connected = false
	store.reservation = SyncReservation{ExecutionID: "sync_1", Fresh: true}
	_, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrGatewayUnavailable) || store.failure.ErrorCode != "napcat_unavailable" ||
		store.completion.ExecutionID != "" || gateway.listCalls != 0 {
		t.Fatalf("failure=%+v completion=%+v gateway=%d error=%v", store.failure, store.completion, gateway.listCalls, err)
	}
}

func TestSyncReplaysTerminalResultWithoutGatewayCall(t *testing.T) {
	service, store, gateway, _ := newFixture(t)
	prior := SyncResult{SyncedAt: time.Unix(100, 0).UTC(), TotalCount: 2}
	store.reservation = SyncReservation{Fresh: false, Result: &prior}
	result, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"})
	if err != nil || result.TotalCount != 2 || gateway.listCalls != 0 {
		t.Fatalf("result=%+v gateway=%d error=%v", result, gateway.listCalls, err)
	}
}

func TestSyncRetriesSuccessfulTerminalPersistenceWithoutRepeatingGatewayCalls(t *testing.T) {
	base := &fakeStore{
		reservation: SyncReservation{ExecutionID: "sync_1", Fresh: true},
		result:      SyncResult{SyncedAt: time.Unix(101, 0).UTC(), AddedCount: 1, TotalCount: 1},
	}
	store := newFlakyTerminalStore(base)
	store.completionFailures = 2
	gateway := &fakeGateway{
		snapshot: napcat.Snapshot{Connected: true}, groups: []napcat.GroupInfo{{ID: 123, Name: "Alpha"}},
	}
	service, err := NewService(Options{
		Store: store, Gateway: gateway, Now: func() time.Time { return time.Unix(101, 0) },
		SyncTimeout: time.Second, PersistenceRetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"}); err == nil {
		t.Fatal("initial completion persistence unexpectedly succeeded")
	}
	waitForTerminalRetry(t, store.completionDone)
	if store.completeCallCount() != 3 || gateway.listCalls != 1 {
		t.Fatalf("completion calls=%d gateway calls=%d", store.completeCallCount(), gateway.listCalls)
	}
}

func TestSyncRetriesFailedTerminalPersistenceWithoutRepeatingGatewayCalls(t *testing.T) {
	base := &fakeStore{reservation: SyncReservation{ExecutionID: "sync_1", Fresh: true}}
	store := newFlakyTerminalStore(base)
	store.failureFailures = 2
	gateway := &fakeGateway{snapshot: napcat.Snapshot{Connected: false}}
	service, err := NewService(Options{
		Store: store, Gateway: gateway, Now: func() time.Time { return time.Unix(101, 0) },
		SyncTimeout: time.Second, PersistenceRetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"}); err == nil {
		t.Fatal("initial failure persistence unexpectedly succeeded")
	}
	waitForTerminalRetry(t, store.failureDone)
	if store.failureCallCount() != 3 || gateway.listCalls != 0 {
		t.Fatalf("failure calls=%d gateway calls=%d", store.failureCallCount(), gateway.listCalls)
	}
}

func TestCloseStopsPendingTerminalPersistenceRetry(t *testing.T) {
	base := &fakeStore{
		reservation: SyncReservation{ExecutionID: "sync_1", Fresh: true},
		result:      SyncResult{SyncedAt: time.Unix(101, 0).UTC(), AddedCount: 1, TotalCount: 1},
	}
	store := newFlakyTerminalStore(base)
	store.completionFailures = 1000
	gateway := &fakeGateway{
		snapshot: napcat.Snapshot{Connected: true}, groups: []napcat.GroupInfo{{ID: 123, Name: "Alpha"}},
	}
	service, err := NewService(Options{
		Store: store, Gateway: gateway, Now: func() time.Time { return time.Unix(101, 0) },
		SyncTimeout: time.Second, PersistenceRetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"}); err == nil {
		t.Fatal("initial completion persistence unexpectedly succeeded")
	}
	waitForCompletionCalls(t, store, 2)
	service.Close()
	calls := store.completeCallCount()
	time.Sleep(5 * time.Millisecond)
	if store.completeCallCount() != calls {
		t.Fatalf("persistence calls continued after close: before=%d after=%d", calls, store.completeCallCount())
	}
}

func TestSyncRejectsDuplicateUpstreamGroupIDs(t *testing.T) {
	service, store, gateway, _ := newFixture(t)
	gateway.snapshot.Connected = true
	gateway.groups = []napcat.GroupInfo{{ID: 123, Name: "Alpha"}, {ID: 123, Name: "Duplicate"}}
	store.reservation = SyncReservation{ExecutionID: "sync_1", Fresh: true}
	_, err := service.Sync(t.Context(), writer(), "sync-key-1", auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrGatewayUnavailable) || store.failure.ErrorCode != "group_list_unavailable" || store.completion.ExecutionID != "" {
		t.Fatalf("failure=%+v completion=%+v error=%v", store.failure, store.completion, err)
	}
}

func TestOperationsAuthorizeAndValidateBeforeStore(t *testing.T) {
	service, store, _, _ := newFixture(t)
	if _, err := service.List(t.Context(), auth.Principal{Role: "invalid"}, ListQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("list error=%v", err)
	}
	if _, err := service.Get(t.Context(), reader(), "not-a-group"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("get error=%v", err)
	}
	if _, err := service.Sync(t.Context(), writer(), "short", auth.MutationContext{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("sync error=%v", err)
	}
	if store.calls != 0 {
		t.Fatalf("invalid operations made %d store calls", store.calls)
	}
}

func TestRecoverInterruptedSyncsUsesCurrentUTC(t *testing.T) {
	service, store, _, _ := newFixture(t)
	store.recovered = 2
	count, err := service.RecoverInterruptedSyncs(t.Context())
	if err != nil || count != 2 || !store.recoveredAt.Equal(time.Unix(101, 0).UTC()) {
		t.Fatalf("count=%d at=%v error=%v", count, store.recoveredAt, err)
	}
}

func newFixture(t *testing.T) (*Service, *fakeStore, *fakeGateway, *fakePublisher) {
	t.Helper()
	store := &fakeStore{}
	gateway := &fakeGateway{}
	publisher := &fakePublisher{}
	service, err := NewService(Options{
		Store: store, Gateway: gateway, Events: publisher, Now: func() time.Time { return time.Unix(101, 0) },
		StaleAfter: time.Minute, SyncTimeout: time.Second, MaxRoleWorkers: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, store, gateway, publisher
}

func reader() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleObserver}
}

func writer() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleMaintainer}
}

func groupFixture() Group {
	features := make([]Feature, len(featureOrder))
	for index, key := range featureOrder {
		features[index] = Feature{Key: key, Enabled: true, Source: FeatureGlobal}
	}
	return Group{
		ID: "123", Name: "Alpha", MemberCount: 10, MaxMemberCount: 100, BotRole: RoleAdmin,
		SnapshotState: SnapshotFresh, LastSyncedAt: time.Unix(100, 0).UTC(), Features: features,
	}
}

type fakeStore struct {
	calls       int
	page        Page
	group       Group
	found       bool
	reservation SyncReservation
	result      SyncResult
	err         error
	listQuery   StoreListQuery
	begin       BeginSync
	completion  CompleteSync
	failure     FailSync
	recovered   int
	recoveredAt time.Time
}

type flakyTerminalStore struct {
	*fakeStore
	mu                 sync.Mutex
	completionFailures int
	failureFailures    int
	completionCalls    int
	failureCalls       int
	completionDone     chan struct{}
	failureDone        chan struct{}
	completionOnce     sync.Once
	failureOnce        sync.Once
}

func newFlakyTerminalStore(store *fakeStore) *flakyTerminalStore {
	return &flakyTerminalStore{
		fakeStore: store, completionDone: make(chan struct{}), failureDone: make(chan struct{}),
	}
}

func (s *flakyTerminalStore) CompleteGroupSync(ctx context.Context, completion CompleteSync) (SyncResult, error) {
	s.mu.Lock()
	s.completionCalls++
	fail := s.completionFailures > 0
	if fail {
		s.completionFailures--
	}
	s.mu.Unlock()
	if fail {
		return SyncResult{}, errors.New("temporary database failure")
	}
	result, err := s.fakeStore.CompleteGroupSync(ctx, completion)
	if err == nil {
		s.completionOnce.Do(func() { close(s.completionDone) })
	}
	return result, err
}

func (s *flakyTerminalStore) FailGroupSync(ctx context.Context, failure FailSync) error {
	s.mu.Lock()
	s.failureCalls++
	fail := s.failureFailures > 0
	if fail {
		s.failureFailures--
	}
	s.mu.Unlock()
	if fail {
		return errors.New("temporary database failure")
	}
	err := s.fakeStore.FailGroupSync(ctx, failure)
	if err == nil {
		s.failureOnce.Do(func() { close(s.failureDone) })
	}
	return err
}

func (s *flakyTerminalStore) completeCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completionCalls
}

func (s *flakyTerminalStore) failureCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failureCalls
}

func waitForTerminalRetry(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("terminal persistence retry did not complete")
	}
}

func waitForCompletionCalls(t *testing.T, store *flakyTerminalStore, minimum int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if store.completeCallCount() >= minimum {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("completion calls=%d, expected at least %d", store.completeCallCount(), minimum)
}

func (s *fakeStore) ListGroups(_ context.Context, query StoreListQuery) (Page, error) {
	s.calls++
	s.listQuery = query
	return s.page, s.err
}

func (s *fakeStore) GetGroup(context.Context, string) (Group, bool, error) {
	s.calls++
	return s.group, s.found, s.err
}

func (s *fakeStore) BeginGroupSync(_ context.Context, begin BeginSync) (SyncReservation, error) {
	s.calls++
	s.begin = begin
	return s.reservation, s.err
}

func (s *fakeStore) CompleteGroupSync(_ context.Context, completion CompleteSync) (SyncResult, error) {
	s.calls++
	s.completion = completion
	return s.result, s.err
}

func (s *fakeStore) FailGroupSync(_ context.Context, failure FailSync) error {
	s.calls++
	s.failure = failure
	return s.err
}

func (s *fakeStore) RecoverInterruptedGroupSyncs(_ context.Context, recoveredAt time.Time) (int, error) {
	s.calls++
	s.recoveredAt = recoveredAt
	return s.recovered, s.err
}

type fakeGateway struct {
	mu         sync.Mutex
	snapshot   napcat.Snapshot
	groups     []napcat.GroupInfo
	selfID     int64
	roles      map[int64]string
	roleErrors map[int64]error
	listErr    error
	loginErr   error
	listCalls  int
}

func (g *fakeGateway) Snapshot() napcat.Snapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.snapshot
}

func (g *fakeGateway) GetGroupList(context.Context) ([]napcat.GroupInfo, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.listCalls++
	return append([]napcat.GroupInfo(nil), g.groups...), g.listErr
}

func (g *fakeGateway) GetLoginUserID(context.Context) (int64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.selfID, g.loginErr
}

func (g *fakeGateway) GetGroupMemberRole(_ context.Context, groupID, _ int64) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.roles[groupID], g.roleErrors[groupID]
}

type fakePublisher struct {
	drafts []events.Draft
}

func (p *fakePublisher) Publish(draft events.Draft) (events.Event, error) {
	p.drafts = append(p.drafts, draft)
	return events.Event{}, nil
}
