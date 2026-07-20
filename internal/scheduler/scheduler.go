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
	Enabled   bool
	LastRunAt *time.Time
}

func IsDue(job Job, now time.Time) bool {
	if !job.Enabled {
		return false
	}
	switch job.Type {
	case JobTypeOnce:
		if alreadyRanToday(job, now) {
			return false
		}
		return job.TimeHHMM != "" && now.Format("15:04") >= job.TimeHHMM
	case JobTypeDaily:
		if job.TimeHHMM == "" || alreadyRanToday(job, now) {
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
