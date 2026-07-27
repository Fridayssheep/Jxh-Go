package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/events"
	"github.com/zjutjh/jxh-go/internal/telemetry"
)

func TestRuntimePersistsSuccessfulRunBeforeMarkingJob(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := &runtimeStoreFake{
		jobs:         []Job{dueRuntimeJob()},
		reservations: []RunReservation{{RunID: "run_1", Result: RunUnknown, Fresh: true}},
	}
	sends := 0
	publisher := &runtimeEventPublisherFake{}
	recorder := &runtimeTelemetryRecorderFake{}
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Location: time.UTC, Now: runtimeClock(now, now.Add(125*time.Millisecond)),
		Events: publisher, Telemetry: recorder,
		Send: func(context.Context, int64, string) error { sends++; return nil },
	})
	if err := runtime.RunOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || len(store.completions) != 1 || store.completions[0].Result != RunSuccess || store.markCalls != 1 {
		t.Fatalf("sends=%d completions=%+v marks=%d", sends, store.completions, store.markCalls)
	}
	if got := store.order; len(got) != 3 || got[0] != "begin" || got[1] != "complete" || got[2] != "mark" {
		t.Fatalf("operation order=%v", got)
	}
	if store.occurrences[0] != "scheduled-1-20260728" || store.completions[0].Duration != 125*time.Millisecond {
		t.Fatalf("occurrence=%q completion=%+v", store.occurrences[0], store.completions[0])
	}
	if len(publisher.drafts) != 1 || publisher.drafts[0].Type != events.EventScheduledJobRunCompleted ||
		publisher.drafts[0].Resource == nil || publisher.drafts[0].Resource.ID != "1" || publisher.drafts[0].Reason != "success" {
		t.Fatalf("event drafts=%+v", publisher.drafts)
	}
	if len(recorder.observations) != 1 || recorder.observations[0].Kind != telemetry.EventScheduledJobRun ||
		recorder.observations[0].JobID != "1" || recorder.observations[0].GroupID != 123 ||
		recorder.observations[0].Result != telemetry.ResultSuccess || recorder.observations[0].Duration != 125*time.Millisecond {
		t.Fatalf("telemetry=%+v", recorder.observations)
	}
}

func TestRuntimeRetriesExplicitFailureAsNewRun(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := &runtimeStoreFake{
		jobs: []Job{dueRuntimeJob()}, reservations: []RunReservation{
			{RunID: "run_1", Result: RunUnknown, Fresh: true},
			{RunID: "run_2", Result: RunUnknown, Fresh: true},
		},
	}
	sends := 0
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Location: time.UTC,
		Now: runtimeClock(now, now.Add(time.Second), now.Add(2*time.Second), now.Add(3*time.Second)),
		Send: func(context.Context, int64, string) error {
			sends++
			if sends == 1 {
				return runtimeSendError{code: "upstream_rejected"}
			}
			return nil
		},
	})
	if err := runtime.RunOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RunOnce(t.Context(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if sends != 2 || len(store.completions) != 2 || store.completions[0].Result != RunFailed ||
		store.completions[0].ErrorCode != "send_rejected" || store.completions[1].Result != RunSuccess || store.markCalls != 1 {
		t.Fatalf("sends=%d completions=%+v marks=%d", sends, store.completions, store.markCalls)
	}
}

func TestRuntimeDoesNotRepeatUnknownDeliveryWhilePersistenceRetries(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := &runtimeStoreFake{
		jobs: []Job{dueRuntimeJob()}, reservations: []RunReservation{{RunID: "run_1", Result: RunUnknown, Fresh: true}},
		completeErrors: []error{errors.New("database unavailable"), nil},
	}
	sends := 0
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Location: time.UTC, Now: runtimeClock(now, now.Add(time.Second)),
		Send: func(context.Context, int64, string) error { sends++; return context.DeadlineExceeded },
	})
	if err := runtime.RunOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RunOnce(t.Context(), now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if sends != 1 || store.beginCalls != 1 || len(store.completions) != 2 ||
		store.completions[0].Result != RunUnknown || store.markCalls != 1 || len(store.markAts) != 1 || !store.markAts[0].Equal(now) {
		t.Fatalf("sends=%d begins=%d completions=%+v marks=%d", sends, store.beginCalls, store.completions, store.markCalls)
	}
}

func TestRuntimePersistsFailedCompletionBeforeCreatingRetry(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := &runtimeStoreFake{
		jobs: []Job{dueRuntimeJob()}, reservations: []RunReservation{
			{RunID: "run_1", Result: RunUnknown, Fresh: true},
			{RunID: "run_2", Result: RunUnknown, Fresh: true},
		},
		completeErrors: []error{errors.New("database unavailable"), nil, nil},
	}
	sends := 0
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Location: time.UTC,
		Now: runtimeClock(now, now.Add(time.Second), now.Add(2*time.Second), now.Add(3*time.Second)),
		Send: func(context.Context, int64, string) error {
			sends++
			if sends == 1 {
				return runtimeSendError{code: "upstream_rejected"}
			}
			return nil
		},
	})
	for attempt := 0; attempt < 3; attempt++ {
		if err := runtime.RunOnce(t.Context(), now.Add(time.Duration(attempt)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if sends != 2 || store.beginCalls != 2 || len(store.completions) != 3 || store.markCalls != 1 ||
		store.completions[0].RunID != "run_1" || store.completions[1].RunID != "run_1" ||
		store.completions[2].RunID != "run_2" {
		t.Fatalf("sends=%d begins=%d completions=%+v marks=%d", sends, store.beginCalls, store.completions, store.markCalls)
	}
}

func TestRuntimeReplaysPriorUnknownOccurrenceWithoutSending(t *testing.T) {
	now := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	store := &runtimeStoreFake{
		jobs: []Job{dueRuntimeJob()}, reservations: []RunReservation{{RunID: "run_1", Result: RunUnknown, Fresh: false}},
	}
	sends := 0
	runtime := NewRuntime(RuntimeOptions{
		Store: store, Location: time.UTC, Now: runtimeClock(now),
		Send: func(context.Context, int64, string) error { sends++; return nil },
	})
	if err := runtime.RunOnce(t.Context(), now); err != nil {
		t.Fatal(err)
	}
	if sends != 0 || len(store.completions) != 0 || store.markCalls != 1 {
		t.Fatalf("sends=%d completions=%+v marks=%d", sends, store.completions, store.markCalls)
	}
}

func dueRuntimeJob() Job {
	return Job{ID: 1, Type: JobTypeDaily, GroupID: 123, Message: "notice", TimeHHMM: "09:00", Enabled: true}
}

func runtimeClock(values ...time.Time) func() time.Time {
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

type runtimeSendError struct{ code string }

func (e runtimeSendError) Error() string           { return "scheduled send failed" }
func (e runtimeSendError) SafeFailureCode() string { return e.code }

type runtimeEventPublisherFake struct{ drafts []events.Draft }

func (p *runtimeEventPublisherFake) Publish(draft events.Draft) (events.Event, error) {
	p.drafts = append(p.drafts, draft)
	return events.Event{}, nil
}

type runtimeTelemetryRecorderFake struct{ observations []telemetry.Observation }

func (r *runtimeTelemetryRecorderFake) Record(observation telemetry.Observation) bool {
	r.observations = append(r.observations, observation)
	return true
}

type runtimeStoreFake struct {
	jobs           []Job
	reservations   []RunReservation
	completeErrors []error
	completions    []RunCompletion
	occurrences    []string
	order          []string
	beginCalls     int
	markCalls      int
	markAts        []time.Time
}

func (s *runtimeStoreFake) ListActiveSchedulerJobs(context.Context) ([]Job, error) {
	return append([]Job(nil), s.jobs...), nil
}

func (s *runtimeStoreFake) BeginScheduledJobRun(
	_ context.Context,
	_ uint64,
	occurrenceID string,
	_, _ time.Time,
) (RunReservation, error) {
	s.order = append(s.order, "begin")
	s.occurrences = append(s.occurrences, occurrenceID)
	index := s.beginCalls
	s.beginCalls++
	if index >= len(s.reservations) {
		return RunReservation{}, errors.New("unexpected begin")
	}
	return s.reservations[index], nil
}

func (s *runtimeStoreFake) CompleteScheduledJobRun(_ context.Context, completion RunCompletion) error {
	s.order = append(s.order, "complete")
	s.completions = append(s.completions, completion)
	index := len(s.completions) - 1
	if index < len(s.completeErrors) {
		return s.completeErrors[index]
	}
	return nil
}

func (s *runtimeStoreFake) MarkScheduledJobRan(_ context.Context, id uint64, at time.Time, disable bool) error {
	s.order = append(s.order, "mark")
	s.markCalls++
	s.markAts = append(s.markAts, at)
	for index := range s.jobs {
		if s.jobs[index].ID == id {
			s.jobs[index].LastRunAt = &at
			if disable {
				s.jobs[index].Enabled = false
			}
		}
	}
	return nil
}
