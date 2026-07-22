package scheduler

import (
	"context"
	"time"
)

const (
	JobTypeDaily = "每天"
	JobTypeOnce  = "单次"
)

type SendFunc func(context.Context, int64, string) error

type Job struct {
	ID        uint64
	Type      string
	GroupID   int64
	Message   string
	TimeHHMM  string
	RunDate   *time.Time
	Enabled   bool
	LastRunAt *time.Time
}

func IsDue(job Job, now time.Time) bool {
	if !job.Enabled {
		return false
	}
	switch job.Type {
	case JobTypeOnce:
		// Single-run tasks require a run_date and must match that date
		if job.RunDate == nil {
			return false
		}
		// Compare in now's location: RunDate may carry the DB DSN loc after a
		// round-trip, which need not equal the scheduler timezone.
		runDateOnly := job.RunDate.In(now.Location()).Format("2006-01-02")
		nowDateOnly := now.Format("2006-01-02")
		if runDateOnly != nowDateOnly {
			return false
		}
		if alreadyRanToday(job, now) {
			return false
		}
		return job.TimeHHMM != "" && now.Format("15:04") >= job.TimeHHMM
	case JobTypeDaily:
		if job.TimeHHMM == "" {
			return false
		}
		// alreadyRanToday guards same-day repeats. Same-day suppression for a job
		// created after its scheduled time is handled at creation by seeding
		// LastRunAt (see storage.AddScheduledJob), so here a nil LastRunAt simply
		// means "never fired" and should trigger once the time is reached.
		if alreadyRanToday(job, now) {
			return false
		}
		return now.Format("15:04") >= job.TimeHHMM
	default:
		return false
	}
}

func alreadyRanToday(job Job, now time.Time) bool {
	return job.LastRunAt != nil && job.LastRunAt.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02")
}
