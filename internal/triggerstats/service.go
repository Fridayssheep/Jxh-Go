package triggerstats

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	TriggerTypeKeywordReply = "keyword_reply"
	TriggerTypeAIRetrieval  = "ai_retrieval"
)

type Event struct {
	SourceKey   string
	TriggerType string
	GroupID     int64
	TriggeredAt time.Time
}

type Summary struct {
	SourceKey     string
	Keyword       string
	TriggerType   string
	Count         int64
	LastTriggered time.Time
}

type Store interface {
	RecordKnowledgeTriggers(ctx context.Context, events []Event) error
	ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error)
}

type Options struct {
	Now            func() time.Time
	ExportDir      string
	ResolveKeyword func(sourceKey string) string
}

type Service struct {
	store          Store
	now            func() time.Time
	exportDir      string
	resolveKeyword func(string) string
}

func NewService(store Store, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	exportDir := strings.TrimSpace(opts.ExportDir)
	if exportDir == "" {
		exportDir = filepath.Join("data", "exports", "trigger_stats")
	}
	return &Service{store: store, now: now, exportDir: exportDir, resolveKeyword: opts.ResolveKeyword}
}

func (s *Service) RecordKeywordReply(ctx context.Context, sourceKey string, groupID int64) error {
	return s.record(ctx, []string{sourceKey}, TriggerTypeKeywordReply, groupID)
}

func (s *Service) RecordAIRetrievals(ctx context.Context, sourceKeys []string, groupID int64) error {
	return s.record(ctx, uniqueSourceKeys(sourceKeys), TriggerTypeAIRetrieval, groupID)
}

func (s *Service) record(ctx context.Context, sourceKeys []string, triggerType string, groupID int64) error {
	if s == nil || s.store == nil || len(sourceKeys) == 0 {
		return nil
	}
	now := s.now()
	events := make([]Event, 0, len(sourceKeys))
	for _, sourceKey := range sourceKeys {
		if sourceKey = strings.TrimSpace(sourceKey); sourceKey != "" {
			events = append(events, Event{SourceKey: sourceKey, TriggerType: triggerType, GroupID: groupID, TriggeredAt: now})
		}
	}
	if len(events) == 0 {
		return nil
	}
	return s.store.RecordKnowledgeTriggers(ctx, events)
}

func (s *Service) Summaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	summaries, err := s.store.ListKnowledgeTriggerSummaries(ctx, since, limit)
	if err != nil {
		return nil, err
	}
	if s.resolveKeyword != nil {
		for i := range summaries {
			summaries[i].Keyword = s.resolveKeyword(summaries[i].SourceKey)
		}
	}
	return summaries, nil
}

func (s *Service) SummariesForDays(ctx context.Context, days, limit int) ([]Summary, error) {
	if days < 0 {
		return nil, fmt.Errorf("days must not be negative")
	}
	if days == 0 {
		return s.Summaries(ctx, nil, limit)
	}
	now := s.now()
	year, month, day := now.Date()
	since := time.Date(year, month, day, 0, 0, 0, 0, now.Location()).AddDate(0, 0, -days+1)
	return s.Summaries(ctx, &since, limit)
}

func uniqueSourceKeys(sourceKeys []string) []string {
	seen := make(map[string]struct{}, len(sourceKeys))
	out := make([]string, 0, len(sourceKeys))
	for _, sourceKey := range sourceKeys {
		sourceKey = strings.TrimSpace(sourceKey)
		if sourceKey == "" {
			continue
		}
		if _, ok := seen[sourceKey]; ok {
			continue
		}
		seen[sourceKey] = struct{}{}
		out = append(out, sourceKey)
	}
	return out
}

func triggerTypeLabel(triggerType string) string {
	switch triggerType {
	case TriggerTypeKeywordReply:
		return "关键词回复"
	case TriggerTypeAIRetrieval:
		return "/ai 检索"
	default:
		return triggerType
	}
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "无"
	}
	return t.Format("2006-01-02 15:04:05")
}
