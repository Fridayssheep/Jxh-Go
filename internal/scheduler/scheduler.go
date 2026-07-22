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
		if job.RunDate == nil || job.TimeHHMM == "" {
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
		return now.Format("15:04") >= job.TimeHHMM
	case JobTypeDaily:
		return job.TimeHHMM != "" && !alreadyRanToday(job, now) && now.Format("15:04") >= job.TimeHHMM
	default:
		return false
	}
}

func alreadyRanToday(job Job, now time.Time) bool {
	return job.LastRunAt != nil && job.LastRunAt.In(now.Location()).Format("2006-01-02") == now.Format("2006-01-02")
}
