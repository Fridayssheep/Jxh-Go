package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/config"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
)

type fakeProcessedEventStore struct {
	mu      sync.Mutex
	seen    map[string]bool
	hasErr  error
	markErr error
}

func (s *fakeProcessedEventStore) HasProcessedEvent(ctx context.Context, key string) (bool, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[key], s.hasErr
}

func (s *fakeProcessedEventStore) MarkProcessedEvent(ctx context.Context, key string, at time.Time) error {
	_ = ctx
	_ = at
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	s.seen[key] = true
	return nil
}

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

func TestInitTriggerStatsServiceDegradesWhenRedisUnavailable(t *testing.T) {
	cfg := config.Default()
	cfg.Redis.Addr = "127.0.0.1:1"
	service, closeFn := initTriggerStatsService(context.Background(), cfg)
	defer closeFn()
	if service != nil {
		t.Fatal("service was initialized with unavailable Redis")
	}
}

func TestApplicationLocationUsesConfiguredTimezone(t *testing.T) {
	cfg := config.Default()
	cfg.App.Timezone = "Asia/Shanghai"
	_, offset := time.Now().In(applicationLocation(cfg)).Zone()
	if offset != 8*60*60 {
		t.Fatalf("timezone offset = %d, want %d", offset, 8*60*60)
	}
}

func TestPersistentDedupeAllowsOnlyOneConcurrentBegin(t *testing.T) {
	dedupe := &persistentDedupe{memory: cache.NewEventDedupe(time.Hour)}
	const workers = 20
	start := make(chan struct{})
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			duplicate, err := dedupe.Begin(context.Background(), "event")
			if err != nil {
				t.Errorf("Begin returned error: %v", err)
			}
			results <- duplicate
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	var accepted int
	for duplicate := range results {
		if !duplicate {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted begins = %d, want 1", accepted)
	}
	dedupe.Abort("event")
	duplicate, err := dedupe.Begin(context.Background(), "event")
	if err != nil || duplicate {
		t.Fatalf("Begin after Abort = duplicate %v, err %v", duplicate, err)
	}
}

func TestPersistentDedupeKeepsMemoryCompletionWhenStoreMarkFails(t *testing.T) {
	storeErr := errors.New("store unavailable")
	dedupe := &persistentDedupe{
		memory: cache.NewEventDedupe(time.Hour),
		store:  &fakeProcessedEventStore{seen: map[string]bool{}, markErr: storeErr},
	}
	duplicate, err := dedupe.Begin(context.Background(), "event")
	if err != nil || duplicate {
		t.Fatalf("Begin = duplicate %v, err %v", duplicate, err)
	}
	if err := dedupe.Complete(context.Background(), "event"); !errors.Is(err, storeErr) {
		t.Fatalf("Complete error = %v, want %v", err, storeErr)
	}
	duplicate, err = dedupe.Begin(context.Background(), "event")
	if err != nil || !duplicate {
		t.Fatalf("Begin after failed persistence = duplicate %v, err %v", duplicate, err)
	}
}
