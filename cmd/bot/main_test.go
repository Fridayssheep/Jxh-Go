package main

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
)

func TestNewAIServiceReturnsNilWhenDisabled(t *testing.T) {
	cfg := config.Default()
	cfg.AI.Enabled = false

	service, err := newAIService(context.Background(), cfg, ai.StaticRetriever{})
	if err != nil {
		t.Fatalf("newAIService returned error: %v", err)
	}
	if service != nil {
		t.Fatal("newAIService returned service, want nil")
	}
}

func TestNewTriggerStatsServiceUsesRedis(t *testing.T) {
	ctx := context.Background()
	server := miniredis.RunT(t)
	cfg := config.Default()
	cfg.Redis.Addr = server.Addr()
	cfg.Redis.DailyRetentionDays = 30

	service, closeFn, err := newTriggerStatsService(ctx, cfg)
	if err != nil {
		t.Fatalf("newTriggerStatsService returned error: %v", err)
	}
	defer closeFn()
	if service == nil {
		t.Fatal("newTriggerStatsService returned nil service")
	}

	err = service.RecordKeywordReply(ctx, triggerstats.KeywordReplyInput{
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
	since := time.Now().Add(-24 * time.Hour)
	summaries, err := service.Summaries(ctx, &since, 10)
	if err != nil {
		t.Fatalf("Summaries returned error: %v", err)
	}
	if len(summaries) != 1 || summaries[0].SourceKey != "menu" || summaries[0].Count != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
}
