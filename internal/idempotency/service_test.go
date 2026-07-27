package idempotency

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

type memoryStore struct {
	mu                 sync.Mutex
	nextID             uint64
	records            map[string]Reservation
	reserveCalls       int
	completeCalls      int
	lastReservation    Reservation
	lastCompletion     Completion
	reserveErr         error
	completeErr        error
	returnWrongHash    bool
	returnInvalidState bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{nextID: 1, records: make(map[string]Reservation)}
}

func (s *memoryStore) ReserveIdempotency(_ context.Context, requested Reservation) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserveCalls++
	s.lastReservation = requested
	if s.reserveErr != nil {
		return Reservation{}, s.reserveErr
	}
	key := reservationMapKey(Actor{Type: requested.ActorType, ID: requested.ActorID}, requested.Operation, requested.Key)
	if existing, ok := s.records[key]; ok {
		if existing.RequestHash != requested.RequestHash {
			return existing, ErrKeyReused
		}
		if s.returnWrongHash {
			existing.RequestHash = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
		}
		if s.returnInvalidState {
			existing.State = State("corrupt")
		}
		return existing, nil
	}
	requested.ID = s.nextID
	s.nextID++
	requested.State = StateInProgress
	requested.Fresh = true
	stored := requested
	stored.Fresh = false
	s.records[key] = stored
	return requested, nil
}

func (s *memoryStore) CompleteIdempotency(_ context.Context, id uint64, completion Completion) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.completeCalls++
	s.lastCompletion = completion
	if s.completeErr != nil {
		return Reservation{}, s.completeErr
	}
	for key, record := range s.records {
		if record.ID != id {
			continue
		}
		if record.RequestHash != completion.RequestHash {
			return record, ErrKeyReused
		}
		if record.State == StateCompleted {
			return record, nil
		}
		result := completion.Result
		record.State = StateCompleted
		record.Result = &result
		record.CompletedAt = &completion.CompletedAt
		s.records[key] = record
		return record, nil
	}
	return Reservation{}, fmt.Errorf("reservation %d not found", id)
}

func (s *memoryStore) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reserveCalls, s.completeCalls
}

func (s *memoryStore) completion() Completion {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastCompletion
}

func reservationMapKey(actor Actor, operation, key string) string {
	return string(actor.Type) + "\x00" + actor.ID + "\x00" + operation + "\x00" + key
}

func newTestService(t *testing.T, store Store, secret []byte, now time.Time) *Service {
	t.Helper()
	service, err := NewService(store, secret, Options{
		TTL: time.Hour,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func validBeginInput(payload any) BeginInput {
	return BeginInput{
		Actor:     Actor{Type: ActorAdminUser, ID: "usr_1"},
		Operation: "sessions.revoke",
		Key:       "retry-key",
		Payload:   payload,
	}
}

func TestServiceCanonicalHMACAndReservationStates(t *testing.T) {
	type requestBA struct {
		B string `json:"b"`
		A int    `json:"a"`
	}
	type requestAB struct {
		A int    `json:"a"`
		B string `json:"b"`
	}

	secret := []byte("0123456789abcdef0123456789abcdef")
	store := newMemoryStore()
	service := newTestService(t, store, secret, time.Unix(100, 0))
	secret[0] = 'x'

	first, err := service.Begin(t.Context(), validBeginInput(requestBA{B: "two", A: 1}))
	if err != nil {
		t.Fatalf("Begin(first) error = %v", err)
	}
	if first.Disposition != DispositionExecute || first.Execution == nil {
		t.Fatalf("Begin(first) = %+v, want execute with execution", first)
	}

	mac := hmac.New(sha256.New, []byte("0123456789abcdef0123456789abcdef"))
	_, _ = mac.Write([]byte("jxh-manager:idempotency-request:v1\x00"))
	_, _ = mac.Write([]byte("sessions.revoke"))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(`{"a":1,"b":"two"}`))
	wantHash := hex.EncodeToString(mac.Sum(nil))
	if first.RequestHash != wantHash {
		t.Fatalf("Begin(first).RequestHash = %q, want %q", first.RequestHash, wantHash)
	}

	second, err := service.Begin(t.Context(), validBeginInput(requestAB{A: 1, B: "two"}))
	if err != nil {
		t.Fatalf("Begin(second) error = %v", err)
	}
	if second.Disposition != DispositionInProgress || second.Execution != nil || second.RequestHash != first.RequestHash {
		t.Fatalf("Begin(second) = %+v, want same-hash in_progress", second)
	}
	if !store.lastReservation.CreatedAt.Equal(time.Unix(100, 0)) || !store.lastReservation.ExpiresAt.Equal(time.Unix(100, 0).Add(time.Hour)) {
		t.Fatalf("reservation times = %v..%v", store.lastReservation.CreatedAt, store.lastReservation.ExpiresAt)
	}
}

func TestServiceRejectsReusedKeyWithDifferentHash(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store, []byte("0123456789abcdef0123456789abcdef"), time.Unix(100, 0))
	if _, err := service.Begin(t.Context(), validBeginInput(map[string]any{"target": "session_1"})); err != nil {
		t.Fatalf("Begin(first) error = %v", err)
	}
	_, err := service.Begin(t.Context(), validBeginInput(map[string]any{"target": "session_2"}))
	if !errors.Is(err, ErrKeyReused) {
		t.Fatalf("Begin(different payload) error = %v, want ErrKeyReused", err)
	}
}

func TestServiceReplaysCompletedResultWithDeepCopies(t *testing.T) {
	store := newMemoryStore()
	now := time.Unix(100, 0)
	service := newTestService(t, store, []byte("0123456789abcdef0123456789abcdef"), now)
	begin, err := service.Begin(t.Context(), validBeginInput(map[string]any{"target": "usr_2"}))
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	resource := &Resource{Type: "admin_user", ID: "usr_2"}
	completed, err := service.Complete(t.Context(), *begin.Execution, Result{
		Status:         ResultSucceeded,
		ResponseStatus: 200,
		Resource:       resource,
		TraceID:        "trace_1",
	})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if completed.Status != ResultSucceeded || !completed.CompletedAt.Equal(now) {
		t.Fatalf("Complete() = %+v", completed)
	}

	resource.ID = "caller-mutated"
	completed.Resource.ID = "result-mutated"

	replayed, err := service.Begin(t.Context(), validBeginInput(map[string]any{"target": "usr_2"}))
	if err != nil {
		t.Fatalf("Begin(replay) error = %v", err)
	}
	if replayed.Disposition != DispositionReplay || replayed.Result == nil {
		t.Fatalf("Begin(replay) = %+v, want replay", replayed)
	}
	if replayed.Result.Resource == nil || replayed.Result.Resource.ID != "usr_2" {
		t.Fatalf("replayed resource was aliased: %+v", replayed.Result.Resource)
	}
	replayed.Result.Resource.ID = "replay-mutated"
	if replayedAgain, err := service.Begin(t.Context(), validBeginInput(map[string]any{"target": "usr_2"})); err != nil {
		t.Fatal(err)
	} else if replayedAgain.Result.Resource.ID != "usr_2" {
		t.Fatal("mutating replay resource changed stored result")
	}
}

func TestServiceMarkUnknownPersistsExternalInterruption(t *testing.T) {
	store := newMemoryStore()
	now := time.Unix(100, 0)
	service := newTestService(t, store, []byte("0123456789abcdef0123456789abcdef"), now)
	begin, err := service.Begin(t.Context(), validBeginInput(map[string]any{"confirmation": "restart"}))
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	result, err := service.MarkUnknown(t.Context(), *begin.Execution, 202, "napcat_outcome_unknown", "trace_1", &Resource{
		Type: "system_operation",
		ID:   "op_1",
	})
	if err != nil {
		t.Fatalf("MarkUnknown() error = %v", err)
	}
	if result.Status != ResultUnknown || result.ErrorCode != "napcat_outcome_unknown" || result.ResponseStatus != 202 {
		t.Fatalf("MarkUnknown() = %+v", result)
	}
	completion := store.completion()
	if completion.Result.Status != ResultUnknown || !completion.CompletedAt.Equal(now) {
		t.Fatalf("stored completion = %+v", completion)
	}

	replayed, err := service.Begin(t.Context(), validBeginInput(map[string]any{"confirmation": "restart"}))
	if err != nil {
		t.Fatalf("Begin(replay) error = %v", err)
	}
	if replayed.Disposition != DispositionReplay || replayed.Result == nil || replayed.Result.Status != ResultUnknown {
		t.Fatalf("Begin(replay) = %+v, want unknown replay", replayed)
	}
}

func TestServiceValidatesInputsBeforeStore(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	if _, err := NewService(nil, secret, Options{}); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("NewService(nil) error = %v, want ErrInvalidStore", err)
	}
	if _, err := NewService(newMemoryStore(), []byte("short"), Options{}); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("NewService(short secret) error = %v, want ErrInvalidSecret", err)
	}

	tests := []struct {
		name   string
		mutate func(*BeginInput)
	}{
		{name: "actor type", mutate: func(input *BeginInput) { input.Actor.Type = ActorType("root") }},
		{name: "actor id", mutate: func(input *BeginInput) { input.Actor.ID = "contains space" }},
		{name: "operation", mutate: func(input *BeginInput) { input.Operation = "" }},
		{name: "key", mutate: func(input *BeginInput) { input.Key = "short" }},
		{name: "payload", mutate: func(input *BeginInput) { input.Payload = make(chan int) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			service := newTestService(t, store, secret, time.Unix(100, 0))
			input := validBeginInput(map[string]any{"target": "session_1"})
			test.mutate(&input)
			if _, err := service.Begin(t.Context(), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Begin() error = %v, want ErrInvalidInput", err)
			}
			if reserve, _ := store.counts(); reserve != 0 {
				t.Fatalf("invalid input made %d Store calls", reserve)
			}
		})
	}
}

func TestServiceValidatesCompletionBeforeStore(t *testing.T) {
	tests := []struct {
		name   string
		result Result
	}{
		{name: "status", result: Result{ResponseStatus: 200}},
		{name: "response status", result: Result{Status: ResultSucceeded, ResponseStatus: 99}},
		{name: "success error code", result: Result{Status: ResultSucceeded, ResponseStatus: 200, ErrorCode: "unexpected"}},
		{name: "missing failure error code", result: Result{Status: ResultFailed, ResponseStatus: 500}},
		{name: "raw error", result: Result{Status: ResultUnknown, ResponseStatus: 202, ErrorCode: "socket closed: token=secret"}},
		{name: "resource type", result: Result{Status: ResultSucceeded, ResponseStatus: 200, Resource: &Resource{Type: "Admin User", ID: "usr_1"}}},
		{name: "resource id", result: Result{Status: ResultSucceeded, ResponseStatus: 200, Resource: &Resource{Type: "admin_user"}}},
		{name: "session id", result: Result{Status: ResultSucceeded, ResponseStatus: 200, ResultingSessionID: "contains space"}},
		{name: "trace id", result: Result{Status: ResultSucceeded, ResponseStatus: 200, TraceID: "contains space"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newMemoryStore()
			service := newTestService(t, store, []byte("0123456789abcdef0123456789abcdef"), time.Unix(100, 0))
			begin, err := service.Begin(t.Context(), validBeginInput(map[string]any{"target": "session_1"}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Complete(t.Context(), *begin.Execution, test.result); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("Complete() error = %v, want ErrInvalidInput", err)
			}
			if _, complete := store.counts(); complete != 0 {
				t.Fatalf("invalid completion made %d Store calls", complete)
			}
		})
	}
}

func TestServiceRejectsCorruptStoreRecords(t *testing.T) {
	for name, configure := range map[string]func(*memoryStore){
		"wrong hash":    func(store *memoryStore) { store.returnWrongHash = true },
		"invalid state": func(store *memoryStore) { store.returnInvalidState = true },
	} {
		t.Run(name, func(t *testing.T) {
			store := newMemoryStore()
			service := newTestService(t, store, []byte("0123456789abcdef0123456789abcdef"), time.Unix(100, 0))
			input := validBeginInput(map[string]any{"target": "session_1"})
			if _, err := service.Begin(t.Context(), input); err != nil {
				t.Fatal(err)
			}
			configure(store)
			_, err := service.Begin(t.Context(), input)
			if name == "wrong hash" {
				if !errors.Is(err, ErrKeyReused) {
					t.Fatalf("Begin() error = %v, want ErrKeyReused", err)
				}
			} else if !errors.Is(err, ErrInvalidRecord) {
				t.Fatalf("Begin() error = %v, want ErrInvalidRecord", err)
			}
		})
	}
}

func TestServiceConcurrentReservationHasOneExecutor(t *testing.T) {
	store := newMemoryStore()
	service := newTestService(t, store, []byte("0123456789abcdef0123456789abcdef"), time.Unix(100, 0))
	input := validBeginInput(map[string]any{"target": "session_1"})

	const workers = 32
	start := make(chan struct{})
	results := make(chan BeginResult, workers)
	errorsSeen := make(chan error, workers)
	var wait sync.WaitGroup
	for i := 0; i < workers; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			result, err := service.Begin(context.Background(), input)
			if err != nil {
				errorsSeen <- err
				return
			}
			results <- result
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("Begin() error = %v", err)
	}

	executeCount := 0
	inProgressCount := 0
	for result := range results {
		switch result.Disposition {
		case DispositionExecute:
			executeCount++
		case DispositionInProgress:
			inProgressCount++
		default:
			t.Errorf("unexpected disposition %q", result.Disposition)
		}
	}
	if executeCount != 1 || inProgressCount != workers-1 {
		t.Fatalf("execute=%d in_progress=%d, want 1 and %d", executeCount, inProgressCount, workers-1)
	}
}
