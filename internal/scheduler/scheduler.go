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
		runDateOnly := job.RunDate.Format("2006-01-02")
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
		// For newly created daily jobs, skip first trigger if created after schedule time today
		if job.LastRunAt == nil {
			// Never run before - only trigger if we're past the scheduled time
			if now.Format("15:04") >= job.TimeHHMM {
				// Check if this is the first poll after creation and still within the same day
				// We want to skip same-day execution on creation, so mark as "already ran today"
				return false
			}
			return false
		}
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
