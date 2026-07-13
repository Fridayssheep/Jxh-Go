package triggerstats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultRedisKeyPrefix = "jxh:triggerstats"

type RedisStoreOptions struct {
	KeyPrefix      string
	DailyRetention time.Duration
	Now            func() time.Time
}

type RedisStore struct {
	client         *redis.Client
	keyPrefix      string
	dailyRetention time.Duration
	now            func() time.Time
}

func NewRedisStore(client *redis.Client, opts RedisStoreOptions) *RedisStore {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	keyPrefix := opts.KeyPrefix
	if keyPrefix == "" {
		keyPrefix = defaultRedisKeyPrefix
	}
	retention := opts.DailyRetention
	if retention <= 0 {
		retention = 180 * 24 * time.Hour
	}
	return &RedisStore{client: client, keyPrefix: keyPrefix, dailyRetention: retention, now: now}
}

func (s *RedisStore) RecordKnowledgeTrigger(ctx context.Context, event Event) error {
	if s == nil || s.client == nil {
		return nil
	}
	if event.TriggeredAt.IsZero() {
		event.TriggeredAt = s.now()
	}
	if event.EventKey == "" {
		event.EventKey = eventKey(event.TriggerType, event.GroupID, event.MessageID, event.SourceKey, event.TriggerText)
	}
	created, err := s.client.SetNX(ctx, s.eventKey(event.EventKey), "1", s.dailyRetention).Result()
	if err != nil || !created {
		return err
	}
	member := summaryMember(event)
	dayKey := s.dayKey(event.TriggeredAt)
	pipe := s.client.Pipeline()
	pipe.ZIncrBy(ctx, s.allKey(), 1, member)
	pipe.ZIncrBy(ctx, dayKey, 1, member)
	pipe.Expire(ctx, dayKey, s.dailyRetention)
	pipe.HSet(ctx, s.metaKey(member), map[string]any{
		"source_key":     event.SourceKey,
		"keyword":        event.Keyword,
		"trigger_type":   event.TriggerType,
		"group_id":       event.GroupID,
		"user_id":        event.UserID,
		"message_id":     event.MessageID,
		"trigger_text":   event.TriggerText,
		"score":          event.Score,
		"last_triggered": event.TriggeredAt.UnixNano(),
	})
	_, err = pipe.Exec(ctx)
	return err
}

func (s *RedisStore) ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error) {
	if s == nil || s.client == nil {
		return nil, nil
	}
	var counts map[string]float64
	var err error
	if since == nil {
		counts, err = s.allCounts(ctx, limit)
	} else {
		counts, err = s.windowCounts(ctx, *since)
	}
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(counts))
	for member, count := range counts {
		meta, err := s.client.HGetAll(ctx, s.metaKey(member)).Result()
		if err != nil {
			return nil, err
		}
		if len(meta) == 0 {
			continue
		}
		summaries = append(summaries, Summary{
			SourceKey:     meta["source_key"],
			Keyword:       meta["keyword"],
			TriggerType:   meta["trigger_type"],
			Count:         int64(count),
			LastTriggered: unixNanoTime(meta["last_triggered"]),
		})
	}
	sort.SliceStable(summaries, func(i, j int) bool {
		if summaries[i].Count == summaries[j].Count {
			return summaries[i].LastTriggered.After(summaries[j].LastTriggered)
		}
		return summaries[i].Count > summaries[j].Count
	})
	if limit > 0 && len(summaries) > limit {
		summaries = summaries[:limit]
	}
	return summaries, nil
}

func (s *RedisStore) allCounts(ctx context.Context, limit int) (map[string]float64, error) {
	stop := int64(-1)
	if limit > 0 {
		stop = int64(limit - 1)
	}
	items, err := s.client.ZRevRangeWithScores(ctx, s.allKey(), 0, stop).Result()
	if err != nil {
		return nil, err
	}
	counts := make(map[string]float64, len(items))
	for _, item := range items {
		member, ok := item.Member.(string)
		if !ok {
			continue
		}
		counts[member] = item.Score
	}
	return counts, nil
}

func (s *RedisStore) windowCounts(ctx context.Context, since time.Time) (map[string]float64, error) {
	counts := map[string]float64{}
	for day := dayStart(since); !day.After(dayStart(s.now())); day = day.AddDate(0, 0, 1) {
		items, err := s.client.ZRangeWithScores(ctx, s.dayKey(day), 0, -1).Result()
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			member, ok := item.Member.(string)
			if !ok {
				continue
			}
			counts[member] += item.Score
		}
	}
	return counts, nil
}

func (s *RedisStore) allKey() string {
	return s.keyPrefix + ":all"
}

func (s *RedisStore) dayKey(t time.Time) string {
	return s.keyPrefix + ":day:" + dayStart(t).Format("20060102")
}

func (s *RedisStore) eventKey(key string) string {
	return s.keyPrefix + ":event:" + key
}

func (s *RedisStore) metaKey(member string) string {
	return s.keyPrefix + ":meta:" + member
}

func summaryMember(event Event) string {
	sum := sha256.Sum256([]byte(event.SourceKey))
	return fmt.Sprintf("%s:%s", event.TriggerType, hex.EncodeToString(sum[:8]))
}

func dayStart(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

func unixNanoTime(value string) time.Time {
	nano, err := strconv.ParseInt(value, 10, 64)
	if err != nil || nano <= 0 {
		return time.Time{}
	}
	return time.Unix(0, nano)
}
