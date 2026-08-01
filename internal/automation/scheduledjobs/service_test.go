package scheduledjobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
)

func TestServiceAuthorizesAndValidatesBeforeStore(t *testing.T) {
	service, store, _ := newFixture(t)
	_, err := service.Create(t.Context(), auth.Principal{Role: auth.RoleObserver}, CreateInput{}, auth.MutationContext{})
	if !errors.Is(err, ErrForbidden) || store.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, store.calls)
	}
	_, err = service.Create(t.Context(), writer(), CreateInput{
		Name: "Daily", GroupID: "123", Message: "hello", Schedule: Schedule{Type: TypeDaily, LocalTime: "9:00", Timezone: "Asia/Shanghai"}, Enabled: true,
	}, auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrInvalidInput) || store.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, store.calls)
	}
}

func TestCreatePassesCanonicalScheduleToStore(t *testing.T) {
	service, store, _ := newFixture(t)
	store.job = testJob()
	job, err := service.Create(t.Context(), writer(), CreateInput{
		Name: "Daily", GroupID: "123", Message: "hello", Schedule: Schedule{Type: TypeDaily, LocalTime: "09:00", Timezone: "Asia/Shanghai"}, Enabled: true,
	}, auth.MutationContext{RequestID: "req_1"})
	if err != nil || job.ID != "job_1" || store.create.Input.Schedule.LocalTime != "09:00" || store.create.NextRunAt == nil {
		t.Fatalf("job=%+v create=%+v error=%v", job, store.create, err)
	}
}

func TestCreateRejectsPastOnceScheduleBeforeStore(t *testing.T) {
	service, store, _ := newFixture(t)
	runAt := time.Unix(100, 0).UTC()
	_, err := service.Create(t.Context(), writer(), CreateInput{
		Name: "Once", GroupID: "123", Message: "hello", Schedule: Schedule{Type: TypeOnce, RunAt: &runAt}, Enabled: true,
	}, auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrInvalidInput) || store.calls != 0 {
		t.Fatalf("error=%v calls=%d", err, store.calls)
	}
}

func TestUpdateRecalculatesNextRunWithRevisionSnapshot(t *testing.T) {
	service, store, _ := newFixture(t)
	store.job = testJob()
	patch := Patch{Status: auth.Field[Status]{Set: true, Value: StatusPaused}}
	_, err := service.Update(t.Context(), writer(), "job_1", 2, patch, auth.MutationContext{RequestID: "req_1"})
	if err != nil || !store.update.NextRunAt.Set || store.update.NextRunAt.Value != nil || store.calls != 2 {
		t.Fatalf("update=%+v calls=%d error=%v", store.update, store.calls, err)
	}
}

func TestUpdateRejectsStaleRevisionBeforeMutation(t *testing.T) {
	service, store, _ := newFixture(t)
	store.job = testJob()
	_, err := service.Update(t.Context(), writer(), "job_1", 1,
		Patch{Status: auth.Field[Status]{Set: true, Value: StatusPaused}}, auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrConflict) || store.calls != 1 || store.update.JobID != "" {
		t.Fatalf("update=%+v calls=%d error=%v", store.update, store.calls, err)
	}
}

func TestDeletePassesLoadedRevisionToStore(t *testing.T) {
	service, store, _ := newFixture(t)
	if err := service.Delete(t.Context(), writer(), "job_1", 2, auth.MutationContext{RequestID: "req_1"}); err != nil {
		t.Fatal(err)
	}
	if store.deleted.JobID != "job_1" || store.deleted.ExpectedRevision != 2 {
		t.Fatalf("delete mutation=%+v", store.deleted)
	}
}

func TestRecoverInterruptedRunsDelegatesSafeStartupTransition(t *testing.T) {
	service, store, _ := newFixture(t)
	store.recovered = 3
	count, err := service.RecoverInterruptedRuns(t.Context())
	if err != nil || count != 3 || !store.recoveredAt.Equal(time.Unix(101, 0).UTC()) {
		t.Fatalf("count=%d recoveredAt=%v error=%v", count, store.recoveredAt, err)
	}
}

func TestTestSendReturnsUnknownWithoutLeakingSenderError(t *testing.T) {
	service, store, sender := newFixture(t)
	sender.err = errors.New("upstream token=secret")
	store.reservation = TestSendReservation{
		ExecutionID: "exec_1", Job: testJob(), Run: Run{ID: "run_1", JobID: "job_1", Kind: RunTest, StartedAt: time.Unix(100, 0)}, Fresh: true,
	}
	store.completed = Run{ID: "run_1", JobID: "job_1", Kind: RunTest, Result: RunUnknown, StartedAt: time.Unix(100, 0), CompletedAt: timePointer(time.Unix(101, 0))}
	run, err := service.TestSend(t.Context(), writer(), "job_1", 2, "idem-send-1", auth.MutationContext{RequestID: "req_1"})
	if err != nil || run.Result != RunUnknown || store.completion.ErrorCode != "send_outcome_unknown" ||
		strings.Contains(store.completion.ErrorMessage, "secret") {
		t.Fatalf("run=%+v completion=%+v error=%v", run, store.completion, err)
	}
}

func TestTestSendUnavailableDoesNotReserveRun(t *testing.T) {
	service, store, sender := newFixture(t)
	sender.available = false
	_, err := service.TestSend(t.Context(), writer(), "job_1", 2, "idem-send-1", auth.MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrSenderUnavailable) || store.calls != 0 || sender.calls != 0 {
		t.Fatalf("error=%v store=%d sender=%d", err, store.calls, sender.calls)
	}
}

func TestTestSendClassifiesExplicitRejectionAsFailed(t *testing.T) {
	service, store, sender := newFixture(t)
	sender.err = &napcat.OperationError{Operation: "send", Code: napcat.FailureUpstreamRejected}
	store.reservation = TestSendReservation{
		ExecutionID: "exec_1", Job: testJob(), Run: Run{ID: "run_1", JobID: "job_1", Kind: RunTest, StartedAt: time.Unix(100, 0)}, Fresh: true,
	}
	store.completed = Run{ID: "run_1", JobID: "job_1", Kind: RunTest, Result: RunFailed, StartedAt: time.Unix(100, 0), CompletedAt: timePointer(time.Unix(101, 0))}
	_, err := service.TestSend(t.Context(), writer(), "job_1", 2, "idem-send-1", auth.MutationContext{RequestID: "req_1"})
	if err != nil || store.completion.Result != RunFailed || store.completion.ErrorCode != "send_rejected" {
		t.Fatalf("completion=%+v error=%v", store.completion, err)
	}
}

func TestTestSendRetriesCompletionWithoutSendingAgain(t *testing.T) {
	base := &fakeStore{
		reservation: TestSendReservation{
			ExecutionID: "exec_retry", Job: testJob(),
			Run: Run{ID: "run_retry", JobID: "job_1", Kind: RunTest, StartedAt: time.Unix(100, 0)}, Fresh: true,
		},
		completed: Run{
			ID: "run_retry", JobID: "job_1", Kind: RunTest, Result: RunSuccess,
			StartedAt: time.Unix(100, 0), CompletedAt: timePointer(time.Unix(101, 0)),
		},
	}
	store := &retryTestSendStore{fakeStore: base, completed: make(chan struct{})}
	sender := &fakeSender{available: true}
	service, err := NewService(Options{
		Store: store, Sender: sender, Now: func() time.Time { return time.Unix(101, 0) },
		SendTimeout: time.Second, PersistenceRetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if _, err := service.TestSend(t.Context(), writer(), "job_1", 2, "idem-retry-send",
		auth.MutationContext{RequestID: "req_retry"}); err == nil {
		t.Fatal("initial persistence failure did not reach the caller")
	}
	select {
	case <-store.completed:
	case <-time.After(time.Second):
		t.Fatal("test-send completion retry did not finish")
	}
	if store.attempts.Load() != 2 {
		t.Fatalf("completion attempts=%d, want 2", store.attempts.Load())
	}
	sender.mu.Lock()
	sendCalls := sender.calls
	sender.mu.Unlock()
	if sendCalls != 1 {
		t.Fatalf("send calls=%d, want 1", sendCalls)
	}
}

func newFixture(t *testing.T) (*Service, *fakeStore, *fakeSender) {
	t.Helper()
	store := &fakeStore{}
	sender := &fakeSender{available: true}
	service, err := NewService(Options{Store: store, Sender: sender, Now: func() time.Time { return time.Unix(101, 0) }, SendTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service, store, sender
}

func writer() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleMaintainer}
}

func testJob() Job {
	return Job{
		ID: "job_1", Name: "Daily", Group: Group{ID: "123", Name: "Group"}, Message: "hello", Type: TypeDaily,
		Schedule: Schedule{Type: TypeDaily, LocalTime: "09:00", Timezone: "Asia/Shanghai"}, Status: StatusActive,
		Version: 2, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0),
	}
}

type fakeSender struct {
	mu        sync.Mutex
	available bool
	err       error
	calls     int
}

func (s *fakeSender) Available() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.available
}

func (s *fakeSender) Send(context.Context, string, string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return "message_1", s.err
}

type fakeStore struct {
	mu          sync.Mutex
	calls       int
	job         Job
	page        Page[Job]
	runs        Page[Run]
	reservation TestSendReservation
	completed   Run
	create      CreateMutation
	update      UpdateMutation
	deleted     DeleteMutation
	completion  TestSendCompletion
	recovered   int
	recoveredAt time.Time
}

type retryTestSendStore struct {
	*fakeStore
	attempts  atomic.Int64
	completed chan struct{}
}

func (s *retryTestSendStore) CompleteScheduledJobTestSend(
	ctx context.Context,
	completion TestSendCompletion,
) (Run, error) {
	if s.attempts.Add(1) == 1 {
		return Run{}, errors.New("database unavailable")
	}
	run, err := s.fakeStore.CompleteScheduledJobTestSend(ctx, completion)
	if err == nil {
		close(s.completed)
	}
	return run, err
}

func (s *fakeStore) CreateScheduledJob(_ context.Context, mutation CreateMutation) (Job, error) {
	s.calls++
	s.create = mutation
	return s.job, nil
}

func (s *fakeStore) GetScheduledJob(context.Context, string) (Job, bool, error) {
	s.calls++
	return s.job, s.job.ID != "", nil
}

func (s *fakeStore) ListScheduledJobs(context.Context, ListQuery) (Page[Job], error) {
	s.calls++
	return s.page, nil
}

func (s *fakeStore) UpdateScheduledJob(_ context.Context, mutation UpdateMutation) (Job, error) {
	s.calls++
	s.update = mutation
	return s.job, nil
}

func (s *fakeStore) DeleteScheduledJob(_ context.Context, mutation DeleteMutation) error {
	s.calls++
	s.deleted = mutation
	return nil
}

func (s *fakeStore) ListScheduledJobRuns(context.Context, RunListQuery) (Page[Run], error) {
	s.calls++
	return s.runs, nil
}

func (s *fakeStore) BeginScheduledJobTestSend(_ context.Context, _ TestSendBegin) (TestSendReservation, error) {
	s.calls++
	return s.reservation, nil
}

func (s *fakeStore) CompleteScheduledJobTestSend(_ context.Context, completion TestSendCompletion) (Run, error) {
	s.calls++
	s.completion = completion
	return s.completed, nil
}

func (s *fakeStore) RecoverInterruptedScheduledJobRuns(_ context.Context, recoveredAt time.Time) (int, error) {
	s.calls++
	s.recoveredAt = recoveredAt
	return s.recovered, nil
}

func timePointer(value time.Time) *time.Time { return &value }
