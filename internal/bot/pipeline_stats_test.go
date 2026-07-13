package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
)

type recordingTriggerStats struct {
	events    []triggerstats.Event
	summaries []triggerstats.Summary
	err       error
}

func (r *recordingTriggerStats) RecordKnowledgeTrigger(ctx context.Context, event triggerstats.Event) error {
	_ = ctx
	if r.err != nil {
		return r.err
	}
	r.events = append(r.events, event)
	return nil
}

func (r *recordingTriggerStats) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]triggerstats.Summary, error) {
	_ = ctx
	_ = since
	_ = limit
	if limit > 0 && len(r.summaries) > limit {
		return append([]triggerstats.Summary(nil), r.summaries[:limit]...), nil
	}
	return append([]triggerstats.Summary(nil), r.summaries...), nil
}

func TestPipelineRecordsKeywordReplyTrigger(t *testing.T) {
	knowledgeCache := cache.NewKnowledge()
	knowledgeCache.Replace(knowledge.NewKeywordIndex([]knowledge.Entry{{
		SourceKey:  "menu",
		Keyword:    "菜单",
		Answer:     "菜单内容",
		Enabled:    true,
		ExactReply: true,
	}}))
	stats := &recordingTriggerStats{}
	sender := &recordingSender{}
	pipeline := NewPipeline(Options{
		Knowledge:    knowledgeCache,
		Sender:       sender,
		TriggerStats: triggerstats.NewService(stats, triggerstats.Options{Now: func() time.Time { return time.Unix(1, 0) }}),
	})

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{
		GroupID:   1001,
		UserID:    2002,
		MessageID: 3003,
		Text:      "菜单",
	})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if sender.text != "菜单内容" {
		t.Fatalf("sent text = %q", sender.text)
	}
	if len(stats.events) != 1 {
		t.Fatalf("stats events = %d, want 1", len(stats.events))
	}
	if stats.events[0].TriggerType != triggerstats.TriggerTypeKeywordReply {
		t.Fatalf("trigger type = %q", stats.events[0].TriggerType)
	}
}

func TestPipelineKeepsKeywordReplyWhenStatsFails(t *testing.T) {
	knowledgeCache := cache.NewKnowledge()
	knowledgeCache.Replace(knowledge.NewKeywordIndex([]knowledge.Entry{{
		SourceKey:  "menu",
		Keyword:    "菜单",
		Answer:     "菜单内容",
		Enabled:    true,
		ExactReply: true,
	}}))
	sender := &recordingSender{}
	pipeline := NewPipeline(Options{
		Knowledge:    knowledgeCache,
		Sender:       sender,
		TriggerStats: triggerstats.NewService(&recordingTriggerStats{err: errors.New("stats unavailable")}, triggerstats.Options{}),
	})

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{
		GroupID:   1001,
		UserID:    2002,
		MessageID: 3003,
		Text:      "菜单",
	})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if sender.text != "菜单内容" {
		t.Fatalf("sent text = %q", sender.text)
	}
}
