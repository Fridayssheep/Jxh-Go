package storage

import (
	"context"
	"time"

	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	storagemodel "github.com/zjutjh/jxh-go/internal/storage/model"
	"github.com/zjutjh/jxh-go/internal/storage/query"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Store struct {
	db *gorm.DB
	q  *query.Query
}

func NewStore(db *gorm.DB) *Store {
	return &Store{db: db, q: query.Use(db)}
}

func (s *Store) DB() *gorm.DB {
	return s.db
}

func (s *Store) RecordKnowledgeTriggers(ctx context.Context, events []triggerstats.Event) error {
	if len(events) == 0 {
		return nil
	}
	models := make([]*storagemodel.KnowledgeTriggerLog, 0, len(events))
	for _, event := range events {
		models = append(models, &storagemodel.KnowledgeTriggerLog{
			SourceKey: event.SourceKey, TriggerType: event.TriggerType,
			GroupID: event.GroupID, TriggeredAt: event.TriggeredAt,
		})
	}
	return s.db.WithContext(ctx).CreateInBatches(models, len(models)).Error
}

func (s *Store) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]triggerstats.Summary, error) {
	query := s.db.WithContext(ctx).
		Table(storagemodel.TableNameKnowledgeTriggerLog).
		Select("source_key, trigger_type, COUNT(*) AS count, MAX(triggered_at) AS last_triggered")
	if since != nil {
		query = query.Where("triggered_at >= ?", *since)
	}
	query = query.Group("source_key, trigger_type").
		Order("count DESC").Order("source_key").Order("trigger_type")
	if limit > 0 {
		query = query.Limit(limit)
	}
	var summaries []triggerstats.Summary
	return summaries, query.Scan(&summaries).Error
}

func (s *Store) ListScheduledJobs(ctx context.Context) ([]commands.ScheduledJobView, error) {
	job := s.q.ScheduledJob
	jobs, err := job.WithContext(ctx).Where(job.Enabled.Is(true)).Order(job.ID).Find()
	if err != nil {
		return nil, err
	}
	out := make([]commands.ScheduledJobView, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, commands.ScheduledJobView{ID: job.ID, Type: job.Type, TimeHHMM: job.TimeHhmm, GroupID: job.GroupID, Message: job.Message, Enabled: job.Enabled})
	}
	return out, nil
}

func (s *Store) AddScheduledJob(ctx context.Context, input commands.ScheduledJobInput) (uint64, error) {
	job := &storagemodel.ScheduledJob{Type: input.Type, TimeHhmm: input.TimeHHMM, GroupID: input.GroupID, Message: input.Message, Enabled: true}
	err := s.q.ScheduledJob.WithContext(ctx).Create(job)
	return job.ID, err
}

func (s *Store) RemoveScheduledJob(ctx context.Context, id uint64) error {
	job := s.q.ScheduledJob
	_, err := job.WithContext(ctx).Where(job.ID.Eq(id)).Update(job.Enabled, false)
	return err
}

func (s *Store) ListActiveSchedulerJobs(ctx context.Context) ([]scheduler.Job, error) {
	job := s.q.ScheduledJob
	jobs, err := job.WithContext(ctx).Where(job.Enabled.Is(true)).Order(job.ID).Find()
	if err != nil {
		return nil, err
	}
	out := make([]scheduler.Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, scheduler.Job{
			ID:        job.ID,
			Type:      job.Type,
			GroupID:   job.GroupID,
			Message:   job.Message,
			TimeHHMM:  job.TimeHhmm,
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
	job := s.q.ScheduledJob
	_, err := job.WithContext(ctx).Where(job.ID.Eq(id)).Updates(updates)
	return err
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
