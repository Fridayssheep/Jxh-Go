package triggerstats

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRedisStoreRecordsAndSummarizesTriggers(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local)
	store := NewRedisStore(client, RedisStoreOptions{
		KeyPrefix:      "test:jxh:triggerstats",
		DailyRetention: 180 * 24 * time.Hour,
		Now:            func() time.Time { return now },
	})

	for _, event := range []Event{
		{
			EventKey:    "keyword:1",
			SourceKey:   "menu",
			Keyword:     "菜单",
			TriggerType: TriggerTypeKeywordReply,
			GroupID:     1001,
			UserID:      2002,
			MessageID:   3003,
			TriggerText: "菜单",
			TriggeredAt: now,
		},
		{
			EventKey:    "keyword:2",
			SourceKey:   "menu",
			Keyword:     "菜单",
			TriggerType: TriggerTypeKeywordReply,
			GroupID:     1001,
			UserID:      2003,
			MessageID:   3004,
			TriggerText: "菜单",
			TriggeredAt: now,
		},
		{
			EventKey:    "ai:1",
			SourceKey:   "traffic",
			Keyword:     "交通",
			TriggerType: TriggerTypeAIRetrieval,
			GroupID:     1001,
			UserID:      2004,
			MessageID:   3005,
			TriggerText: "怎么去学校",
			Score:       0.9,
			TriggeredAt: now,
		},
	} {
		if err := store.RecordKnowledgeTrigger(ctx, event); err != nil {
			t.Fatalf("RecordKnowledgeTrigger returned error: %v", err)
		}
	}
	if err := store.RecordKnowledgeTrigger(ctx, Event{
		EventKey:    "keyword:1",
		SourceKey:   "menu",
		Keyword:     "菜单",
		TriggerType: TriggerTypeKeywordReply,
		TriggeredAt: now,
	}); err != nil {
		t.Fatalf("duplicate RecordKnowledgeTrigger returned error: %v", err)
	}

	summaries, err := store.ListKnowledgeTriggerSummaries(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListKnowledgeTriggerSummaries returned error: %v", err)
	}
	if len(summaries) != 2 {
		t.Fatalf("summary count = %d, want 2", len(summaries))
	}
	if summaries[0].SourceKey != "menu" || summaries[0].Keyword != "菜单" || summaries[0].TriggerType != TriggerTypeKeywordReply || summaries[0].Count != 2 {
		t.Fatalf("top summary = %+v", summaries[0])
	}
	if summaries[1].SourceKey != "traffic" || summaries[1].TriggerType != TriggerTypeAIRetrieval || summaries[1].Count != 1 {
		t.Fatalf("second summary = %+v", summaries[1])
	}
}

func TestRedisStoreSummariesHonorSinceWindow(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	now := time.Date(2026, 7, 10, 20, 30, 0, 0, time.Local)
	current := now
	store := NewRedisStore(client, RedisStoreOptions{
		KeyPrefix:      "test:jxh:triggerstats",
		DailyRetention: 180 * 24 * time.Hour,
		Now:            func() time.Time { return current },
	})

	current = now.AddDate(0, 0, -8)
	if err := store.RecordKnowledgeTrigger(ctx, Event{
		EventKey:    "old",
		SourceKey:   "old",
		Keyword:     "旧问题",
		TriggerType: TriggerTypeKeywordReply,
		TriggeredAt: current,
	}); err != nil {
		t.Fatalf("record old trigger: %v", err)
	}
	current = now
	if err := store.RecordKnowledgeTrigger(ctx, Event{
		EventKey:    "new",
		SourceKey:   "new",
		Keyword:     "新问题",
		TriggerType: TriggerTypeKeywordReply,
		TriggeredAt: current,
	}); err != nil {
		t.Fatalf("record new trigger: %v", err)
	}

	since := now.AddDate(0, 0, -7)
	summaries, err := store.ListKnowledgeTriggerSummaries(ctx, &since, 10)
	if err != nil {
		t.Fatalf("ListKnowledgeTriggerSummaries returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].SourceKey != "new" {
		t.Fatalf("summaries = %+v, want only new trigger", summaries)
	}
}

func TestRedisStoreDoesNotClaimEventBeforeCounterWrite(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	store := NewRedisStore(client, RedisStoreOptions{KeyPrefix: "test:atomic"})
	event := Event{
		EventKey: "event-1", SourceKey: "menu", Keyword: "菜单",
		TriggerType: TriggerTypeKeywordReply, TriggeredAt: time.Now(),
	}

	server.Set(store.allKey(), "wrong-type")
	if err := store.RecordKnowledgeTrigger(ctx, event); err == nil {
		t.Fatal("RecordKnowledgeTrigger returned nil error with wrong counter type")
	}
	if server.Exists(store.eventKey(event.EventKey)) {
		t.Fatal("event was claimed even though counters were not written")
	}
	server.Del(store.allKey())
	server.Set(store.dayKey(event.TriggeredAt), "wrong-type")
	if err := store.RecordKnowledgeTrigger(ctx, event); err == nil {
		t.Fatal("RecordKnowledgeTrigger returned nil error with wrong daily counter type")
	}
	if score, err := client.ZScore(ctx, store.allKey(), summaryMember(event)).Result(); err != redis.Nil || score != 0 {
		t.Fatalf("all-time count changed before daily key validation: score %v, err %v", score, err)
	}
	if server.Exists(store.eventKey(event.EventKey)) {
		t.Fatal("event was claimed after daily counter validation failed")
	}
	server.Del(store.dayKey(event.TriggeredAt))
	if err := store.RecordKnowledgeTrigger(ctx, event); err != nil {
		t.Fatalf("retry RecordKnowledgeTrigger returned error: %v", err)
	}
	summaries, err := store.ListKnowledgeTriggerSummaries(ctx, nil, 10)
	if err != nil {
		t.Fatalf("ListKnowledgeTriggerSummaries returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Count != 1 {
		t.Fatalf("summaries = %+v, want one count", summaries)
	}
}
