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
	since     *time.Time
	limit     int
}

func (s *memoryTriggerStore) RecordKnowledgeTriggers(ctx context.Context, events []Event) error {
	_ = ctx
	s.events = append(s.events, events...)
	return nil
}

func (s *memoryTriggerStore) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error) {
	_ = ctx
	s.since = since
	s.limit = limit
	if limit > 0 && len(s.summaries) > limit {
		return append([]Summary(nil), s.summaries[:limit]...), nil
	}
	return append([]Summary(nil), s.summaries...), nil
}

func TestServiceRecordsKeywordTrigger(t *testing.T) {
	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local)
	store := &memoryTriggerStore{}
	service := NewService(store, Options{Now: func() time.Time { return now }})

	err := service.RecordKeywordReply(context.Background(), "menu", 1001)

	if err != nil {
		t.Fatalf("RecordKeywordReply returned error: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("events length = %d, want 1", len(store.events))
	}
	event := store.events[0]
	if event.SourceKey != "menu" || event.GroupID != 1001 || event.TriggerType != TriggerTypeKeywordReply || event.TriggeredAt != now {
		t.Fatalf("type/time = %q/%s", event.TriggerType, event.TriggeredAt)
	}
}

func TestServiceDeduplicatesAIRetrievals(t *testing.T) {
	store := &memoryTriggerStore{}
	service := NewService(store, Options{})
	err := service.RecordAIRetrievals(context.Background(), []string{"menu", "menu", "traffic"}, 1001)

	if err != nil {
		t.Fatalf("RecordAIRetrieval returned error: %v", err)
	}
	if len(store.events) != 2 {
		t.Fatalf("events length = %d, want 2", len(store.events))
	}
	if store.events[0].SourceKey != "menu" || store.events[1].SourceKey != "traffic" {
		t.Fatalf("events = %+v", store.events)
	}
}

func TestServiceSummariesForDaysUsesInclusiveNaturalDays(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 7, 10, 20, 30, 0, 0, location)
	store := &memoryTriggerStore{}
	service := NewService(store, Options{Now: func() time.Time { return now }})

	if _, err := service.SummariesForDays(context.Background(), 7, 10); err != nil {
		t.Fatalf("SummariesForDays returned error: %v", err)
	}
	want := time.Date(2026, 7, 4, 0, 0, 0, 0, location)
	if store.since == nil || !store.since.Equal(want) {
		t.Fatalf("since = %v, want %v", store.since, want)
	}
	if _, err := service.SummariesForDays(context.Background(), 30, 10); err != nil {
		t.Fatalf("SummariesForDays returned error: %v", err)
	}
	want = time.Date(2026, 6, 11, 0, 0, 0, 0, location)
	if store.since == nil || !store.since.Equal(want) {
		t.Fatalf("30-day since = %v, want %v", store.since, want)
	}
}

func TestServiceSummariesForDaysZeroUsesAllTime(t *testing.T) {
	store := &memoryTriggerStore{}
	service := NewService(store, Options{})
	if _, err := service.SummariesForDays(context.Background(), 0, 10); err != nil {
		t.Fatalf("SummariesForDays returned error: %v", err)
	}
	if store.since != nil {
		t.Fatalf("since = %v, want nil", store.since)
	}
}

func TestServiceResolvesCurrentKeyword(t *testing.T) {
	store := &memoryTriggerStore{summaries: []Summary{{SourceKey: "menu"}}}
	service := NewService(store, Options{ResolveKeyword: func(sourceKey string) string { return "菜单" }})

	summaries, err := service.Summaries(context.Background(), nil, 10)
	if err != nil || len(summaries) != 1 || summaries[0].Keyword != "菜单" {
		t.Fatalf("summaries = %+v, err %v", summaries, err)
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
