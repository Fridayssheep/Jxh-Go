package scheduler

import (
	"context"
	"time"
)

type RuntimeStore interface {
	ListActiveSchedulerJobs(ctx context.Context) ([]Job, error)
	MarkScheduledJobRan(ctx context.Context, id uint64, at time.Time, disable bool) error
}

type RuntimeOptions struct {
	Store    RuntimeStore
	Send     SendFunc
	Interval time.Duration
	Location *time.Location
	Logf     func(string, ...any)
}

type Runtime struct {
	store    RuntimeStore
	send     SendFunc
	interval time.Duration
	location *time.Location
	logf     func(string, ...any)
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
	return &Runtime{
		store:    opts.Store,
		send:     opts.Send,
		interval: interval,
		location: location,
		logf:     opts.Logf,
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
	for _, job := range jobs {
		if !IsDue(job, now) {
			continue
		}
		if r.send != nil {
			if err := r.send(ctx, job.GroupID, job.Message); err != nil {
				r.log("send scheduled job %d failed: %v", job.ID, err)
				continue
			}
		}
		disable := job.Type == JobTypeOnce
		if err := r.store.MarkScheduledJobRan(ctx, job.ID, now, disable); err != nil {
			r.log("mark scheduled job %d failed: %v", job.ID, err)
		}
	}
	return nil
}

func (r *Runtime) runAndLog(ctx context.Context) {
	if err := r.RunOnce(ctx, time.Now()); err != nil {
		r.log("run scheduled jobs failed: %v", err)
	}
}

func (r *Runtime) log(format string, args ...any) {
	if r != nil && r.logf != nil {
		r.logf(format, args...)
	}
}
