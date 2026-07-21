package storage

import (
	"context"
	"time"

	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db}
}

func (s *Store) RecordKnowledgeTriggers(ctx context.Context, events []triggerstats.Event) error {
	if len(events) == 0 {
		return nil
	}
	models := make([]KnowledgeTriggerLog, 0, len(events))
	for _, event := range events {
		models = append(models, KnowledgeTriggerLog{
			SourceKey: event.SourceKey, TriggerType: event.TriggerType,
			GroupID: event.GroupID, TriggeredAt: event.TriggeredAt,
		})
	}
	return s.db.WithContext(ctx).CreateInBatches(models, len(models)).Error
}

func (s *Store) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]triggerstats.Summary, error) {
	query := s.db.WithContext(ctx).
		Table((KnowledgeTriggerLog{}).TableName()).
		Select(`source_key,
SUM(CASE WHEN trigger_type = ? THEN 1 ELSE 0 END) AS keyword_reply_count,
SUM(CASE WHEN trigger_type = ? THEN 1 ELSE 0 END) AS ai_retrieval_count,
COUNT(*) AS total_count,
MAX(triggered_at) AS last_triggered`, triggerstats.TriggerTypeKeywordReply, triggerstats.TriggerTypeAIRetrieval)
	if since != nil {
		query = query.Where("triggered_at >= ?", *since)
	}
	query = query.Group("source_key").Order("total_count DESC").Order("source_key")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var summaries []triggerstats.Summary
	return summaries, query.Scan(&summaries).Error
}

func (s *Store) ListScheduledJobs(ctx context.Context) ([]commands.ScheduledJobView, error) {
	var jobs []ScheduledJob
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&jobs).Error; err != nil {
		return nil, err
	}
	out := make([]commands.ScheduledJobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, commands.ScheduledJobView{ID: job.ID, Type: job.Type, TimeHHMM: job.TimeHHMM, GroupID: job.GroupID, Message: job.Message})
	}
	return out, nil
}

func (s *Store) AddScheduledJob(ctx context.Context, input commands.ScheduledJobInput) (uint64, error) {
	job := ScheduledJob{Type: input.Type, TimeHHMM: input.TimeHHMM, GroupID: input.GroupID, Message: input.Message, Enabled: true}
	err := s.db.WithContext(ctx).Create(&job).Error
	return job.ID, err
}

func (s *Store) RemoveScheduledJob(ctx context.Context, id uint64) error {
	return s.db.WithContext(ctx).Model(&ScheduledJob{}).Where("id = ?", id).Update("enabled", false).Error
}

func (s *Store) ListActiveSchedulerJobs(ctx context.Context) ([]scheduler.Job, error) {
	var jobs []ScheduledJob
	if err := s.db.WithContext(ctx).Where("enabled = ?", true).Order("id").Find(&jobs).Error; err != nil {
		return nil, err
	}
	out := make([]scheduler.Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, scheduler.Job{
			ID:        job.ID,
			Type:      job.Type,
			GroupID:   job.GroupID,
			Message:   job.Message,
			TimeHHMM:  job.TimeHHMM,
			Enabled:   job.Enabled,
			LastRunAt: job.LastRunAt,
		})
	}
	return out, nil
}

func (s *Store) MarkScheduledJobRan(ctx context.Context, id uint64, at time.Time, disable bool) error {
	updates := map[string]any{"last_run_at": at}
	if disable {
		updates["enabled"] = false
	}
	return s.db.WithContext(ctx).Model(&ScheduledJob{}).Where("id = ?", id).Updates(updates).Error
}

func (s *Store) UpsertGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	model := groupJoinRequestToModel(record)
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "request_key"}},
		DoUpdates: clause.Assignments(map[string]any{
			"flag":         model.Flag,
			"group_id":     model.GroupID,
			"user_id":      model.UserID,
			"student_id":   model.StudentID,
			"student_name": model.StudentName,
			"sub_type":     model.SubType,
			"comment":      model.Comment,
			"status":       model.Status,
			"source":       model.Source,
			"raw_json":     model.RawJSON,
			"requested_at": model.RequestedAt,
			"last_seen_at": model.LastSeenAt,
		}),
	}).Create(&model).Error
}

func (s *Store) ListGroupJoinRequests(ctx context.Context, limit int) ([]grouprequest.Record, error) {
	var models []GroupJoinRequest
	query := s.db.WithContext(ctx).Order("last_seen_at DESC").Order("id DESC")
	if limit > 0 {
		query = query.Limit(limit)
	}
	if err := query.Find(&models).Error; err != nil {
		return nil, err
	}
	records := make([]grouprequest.Record, 0, len(models))
	for _, model := range models {
		records = append(records, groupJoinRequestFromModel(model))
	}
	return records, nil
}

func groupJoinRequestToModel(record grouprequest.Record) GroupJoinRequest {
	return GroupJoinRequest{
		ID:          record.ID,
		RequestKey:  record.RequestKey,
		Flag:        record.Flag,
		GroupID:     record.GroupID,
		UserID:      record.UserID,
		StudentID:   record.StudentID,
		StudentName: record.StudentName,
		SubType:     record.SubType,
		Comment:     record.Comment,
		Status:      record.Status,
		Source:      record.Source,
		RawJSON:     record.RawJSON,
		RequestedAt: record.RequestedAt,
		FirstSeenAt: record.FirstSeenAt,
		LastSeenAt:  record.LastSeenAt,
	}
}

func groupJoinRequestFromModel(model GroupJoinRequest) grouprequest.Record {
	return grouprequest.Record{
		ID:          model.ID,
		RequestKey:  model.RequestKey,
		Flag:        model.Flag,
		GroupID:     model.GroupID,
		UserID:      model.UserID,
		StudentID:   model.StudentID,
		StudentName: model.StudentName,
		SubType:     model.SubType,
		Comment:     model.Comment,
		Status:      model.Status,
		Source:      model.Source,
		RawJSON:     model.RawJSON,
		RequestedAt: model.RequestedAt,
		FirstSeenAt: model.FirstSeenAt,
		LastSeenAt:  model.LastSeenAt,
	}
}
