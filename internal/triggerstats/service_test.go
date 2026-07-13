package triggerstats

import (
	"context"
	"strings"
	"testing"
	"time"
)

type memoryTriggerStore struct {
	events    []Event
	summaries []Summary
}

func (s *memoryTriggerStore) RecordKnowledgeTrigger(ctx context.Context, event Event) error {
	_ = ctx
	s.events = append(s.events, event)
	return nil
}

func (s *memoryTriggerStore) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error) {
	_ = ctx
	_ = since
	if limit > 0 && len(s.summaries) > limit {
		return append([]Summary(nil), s.summaries[:limit]...), nil
	}
	return append([]Summary(nil), s.summaries...), nil
}

func TestServiceRecordsKeywordTrigger(t *testing.T) {
	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local)
	store := &memoryTriggerStore{}
	service := NewService(store, Options{Now: func() time.Time { return now }})

	err := service.RecordKeywordReply(context.Background(), KeywordReplyInput{
		SourceKey: "menu",
		Keyword:   "菜单",
		GroupID:   1001,
		UserID:    2002,
		MessageID: 3003,
		Text:      "菜单",
	})

	if err != nil {
		t.Fatalf("RecordKeywordReply returned error: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("events length = %d, want 1", len(store.events))
	}
	event := store.events[0]
	if event.EventKey != "keyword_reply:1001:3003:menu" {
		t.Fatalf("event key = %q", event.EventKey)
	}
	if event.TriggerType != TriggerTypeKeywordReply || event.TriggeredAt != now {
		t.Fatalf("type/time = %q/%s", event.TriggerType, event.TriggeredAt)
	}
}

func TestServiceBoundsLongEventKey(t *testing.T) {
	store := &memoryTriggerStore{}
	service := NewService(store, Options{})
	sourceKey := strings.Repeat("source", 50)

	err := service.RecordAIRetrieval(context.Background(), AIRetrievalInput{
		SourceKey: sourceKey,
		Keyword:   "长词条",
		GroupID:   1001,
		UserID:    2002,
		MessageID: 3003,
		Question:  "问题",
		Score:     0.8,
	})

	if err != nil {
		t.Fatalf("RecordAIRetrieval returned error: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("events length = %d, want 1", len(store.events))
	}
	event := store.events[0]
	if event.SourceKey != sourceKey {
		t.Fatal("SourceKey was not preserved")
	}
	if len(event.EventKey) > 191 {
		t.Fatalf("EventKey length = %d, want <= 191", len(event.EventKey))
	}
	if !strings.HasPrefix(event.EventKey, TriggerTypeAIRetrieval+":") {
		t.Fatalf("EventKey = %q, want trigger type prefix", event.EventKey)
	}
}

func TestFormatSummariesShowsTopTriggers(t *testing.T) {
	lines := FormatSummaries([]Summary{{
		SourceKey:     "menu",
		Keyword:       "菜单",
		TriggerType:   TriggerTypeKeywordReply,
		Count:         12,
		LastTriggered: time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local),
	}})

	if !strings.Contains(lines, "1. 菜单") {
		t.Fatalf("summary output missing keyword: %q", lines)
	}
	if !strings.Contains(lines, "关键词回复") || !strings.Contains(lines, "12 次") {
		t.Fatalf("summary output missing type/count: %q", lines)
	}
}
