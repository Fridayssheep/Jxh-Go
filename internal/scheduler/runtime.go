package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/events"
	"github.com/zjutjh/jxh-go/internal/safego"
	"github.com/zjutjh/jxh-go/internal/telemetry"
)

type RunResult string

const (
	RunSuccess RunResult = "success"
	RunFailed  RunResult = "failed"
	RunUnknown RunResult = "unknown"
	RunSkipped RunResult = "skipped"
)

type RuntimeStore interface {
	ListActiveSchedulerJobs(ctx context.Context) ([]Job, error)
	BeginScheduledJobRun(ctx context.Context, jobID uint64, occurrenceID string, scheduledFor, startedAt time.Time) (RunReservation, error)
	CompleteScheduledJobRun(ctx context.Context, completion RunCompletion) error
	MarkScheduledJobRan(ctx context.Context, id uint64, at time.Time, disable bool) error
}

type RunReservation struct {
	RunID  string
	Result RunResult
	Fresh  bool
}

type RunCompletion struct {
	RunID       string
	Result      RunResult
	CompletedAt time.Time
	Duration    time.Duration
	ErrorCode   string
}

type RuntimeOptions struct {
	Store     RuntimeStore
	Send      SendFunc
	Events    EventPublisher
	Telemetry TelemetryRecorder
	Interval  time.Duration
	Location  *time.Location
	Logf      func(string, ...any)
	Now       func() time.Time
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type TelemetryRecorder interface {
	Record(telemetry.Observation) bool
}

type Runtime struct {
	store     RuntimeStore
	send      SendFunc
	events    EventPublisher
	telemetry TelemetryRecorder
	interval  time.Duration
	location  *time.Location
	logf      func(string, ...any)
	now       func() time.Time

	// pending keeps a send in memory until its run completion is durable. A
	// terminal delivery then retries only last_run_at; an explicit failure may
	// create a new attempt on the next tick.
	mu      sync.Mutex
	pending map[uint64]pendingRun
}

type pendingRun struct {
	day        string
	ranAt      time.Time
	completion *RunCompletion
	markRan    bool
}

func NewRuntime(opts RuntimeOptions) *Runtime {
	interval := opts.Interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	location := opts.Location
	if location == nil {
		location = time.Local
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Runtime{
		store: opts.Store, send: opts.Send, events: opts.Events, telemetry: opts.Telemetry,
		interval: interval, location: location, logf: opts.Logf, now: now,
		pending: make(map[uint64]pendingRun),
	}
}

func (r *Runtime) Run(ctx context.Context) {
	r.runAndLog(ctx)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runAndLog(ctx)
		}
	}
}

func (r *Runtime) RunOnce(ctx context.Context, now time.Time) error {
	if r == nil || r.store == nil {
		return nil
	}
	if r.location != nil {
		now = now.In(r.location)
	}
	jobs, err := r.store.ListActiveSchedulerJobs(ctx)
	if err != nil {
		return err
	}
	today := now.Format("2006-01-02")
	for _, job := range jobs {
		if r.hasPending(job.ID) {
			r.finishPending(ctx, job)
			continue
		}
		if !IsDue(job, now) {
			continue
		}
		startedAt := r.now().UTC()
		reservation, err := r.store.BeginScheduledJobRun(
			ctx, job.ID, scheduledOccurrenceIdentity(job, now), now.UTC(), startedAt,
		)
		if err != nil {
			r.log("begin scheduled job %d run failed: %v", job.ID, err)
			continue
		}
		if !reservation.Fresh {
			if reservation.Result == RunSuccess || reservation.Result == RunUnknown || reservation.Result == RunSkipped {
				r.rememberPending(job.ID, today, now, nil, true)
				r.finishPending(ctx, job)
			}
			continue
		}
		if r.send == nil {
			completedAt := r.now().UTC()
			r.rememberPending(job.ID, today, now, &RunCompletion{
				RunID: reservation.RunID, Result: RunFailed, CompletedAt: completedAt,
				Duration: nonNegativeDuration(completedAt.Sub(startedAt)), ErrorCode: "dependency_unavailable",
			}, false)
			r.finishPending(ctx, job)
			continue
		}
		sendErr := r.send(ctx, job.GroupID, job.Message)
		completedAt := r.now().UTC()
		result, code, _ := classifyScheduledSend(sendErr)
		completion := RunCompletion{
			RunID: reservation.RunID, Result: result, CompletedAt: completedAt,
			Duration: nonNegativeDuration(completedAt.Sub(startedAt)), ErrorCode: code,
		}
		r.rememberPending(job.ID, today, now, &completion, result != RunFailed)
		r.finishPending(ctx, job)
	}
	return nil
}

func (r *Runtime) finishPending(ctx context.Context, job Job) {
	pending, ok := r.loadPending(job.ID)
	if !ok {
		return
	}
	if pending.completion != nil {
		if err := r.store.CompleteScheduledJobRun(ctx, *pending.completion); err != nil {
			r.log("complete scheduled job %d run failed: %v", job.ID, err)
			return
		}
		r.publishCompletion(job, *pending.completion)
		r.clearPendingCompletion(job.ID, pending.day)
	}
	if !pending.markRan {
		r.forgetPending(job.ID)
		return
	}
	if err := r.store.MarkScheduledJobRan(ctx, job.ID, pending.ranAt, job.Type == JobTypeOnce); err != nil {
		r.log("mark scheduled job %d failed: %v", job.ID, err)
		return
	}
	r.forgetPending(job.ID)
}

type safeFailureCoder interface {
	SafeFailureCode() string
}

func classifyScheduledSend(err error) (RunResult, string, string) {
	if err == nil {
		return RunSuccess, "", ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return RunUnknown, "send_outcome_unknown", ""
	}
	var coded safeFailureCoder
	if errors.As(err, &coded) {
		switch coded.SafeFailureCode() {
		case "unavailable":
			return RunFailed, "dependency_unavailable", ""
		case "upstream_rejected", "invalid_response":
			return RunFailed, "send_rejected", ""
		default:
			return RunUnknown, "send_outcome_unknown", ""
		}
	}
	return RunUnknown, "send_outcome_unknown", ""
}

func scheduledOccurrenceIdentity(job Job, now time.Time) string {
	return fmt.Sprintf("scheduled-%d-%s", job.ID, now.Format("20060102"))
}

func (r *Runtime) hasPending(id uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.pending[id]
	return ok
}

func (r *Runtime) rememberPending(id uint64, day string, ranAt time.Time, completion *RunCompletion, markRan bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending[id] = pendingRun{day: day, ranAt: ranAt, completion: completion, markRan: markRan}
}

func (r *Runtime) loadPending(id uint64) (pendingRun, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[id]
	return pending, ok
}

func (r *Runtime) clearPendingCompletion(id uint64, day string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending, ok := r.pending[id]
	if ok && pending.day == day {
		pending.completion = nil
		r.pending[id] = pending
	}
}

func (r *Runtime) publishCompletion(job Job, completion RunCompletion) {
	if r.events != nil {
		_, _ = r.events.Publish(events.Draft{
			Type: events.EventScheduledJobRunCompleted, OccurredAt: completion.CompletedAt.UTC(),
			Resource: &events.Resource{Type: events.ResourceScheduledJob, ID: strconv.FormatUint(job.ID, 10)},
			Reason:   string(completion.Result),
		})
	}
	if r.telemetry != nil {
		r.telemetry.Record(telemetry.Observation{
			Kind: telemetry.EventScheduledJobRun, OccurredAt: completion.CompletedAt.UTC(),
			GroupID: job.GroupID, Result: telemetryResult(completion.Result), Duration: completion.Duration,
			JobID: strconv.FormatUint(job.ID, 10),
		})
	}
}

func telemetryResult(result RunResult) telemetry.Result {
	switch result {
	case RunSuccess:
		return telemetry.ResultSuccess
	case RunFailed:
		return telemetry.ResultFailed
	case RunSkipped:
		return telemetry.ResultDisabled
	default:
		return telemetry.ResultUnknown
	}
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

func (r *Runtime) forgetPending(id uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.pending, id)
}

func (r *Runtime) runAndLog(ctx context.Context) {
	// 恢复边界放在每轮 tick 上，一轮 panic 不会让整个调度循环静默退出。
	defer safego.Recover("scheduler tick")
	if err := r.RunOnce(ctx, time.Now()); err != nil {
		r.log("run scheduled jobs failed: %v", err)
	}
}

func (r *Runtime) log(format string, args ...any) {
	if r != nil && r.logf != nil {
		r.logf(format, args...)
	}
}
