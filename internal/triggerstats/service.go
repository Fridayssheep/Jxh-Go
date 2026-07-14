package triggerstats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	TriggerTypeKeywordReply = "keyword_reply"
	TriggerTypeAIRetrieval  = "ai_retrieval"

	maxTriggerTextRunes = 500
	maxEventKeyRunes    = 191
)

type Event struct {
	EventKey    string
	SourceKey   string
	Keyword     string
	TriggerType string
	GroupID     int64
	UserID      int64
	MessageID   int64
	TriggerText string
	Score       float64
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
	RecordKnowledgeTrigger(ctx context.Context, event Event) error
	ListKnowledgeTriggerSummaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error)
}

type Options struct {
	Now func() time.Time
}

type Service struct {
	store Store
	now   func() time.Time
}

type KeywordReplyInput struct {
	SourceKey string
	Keyword   string
	GroupID   int64
	UserID    int64
	MessageID int64
	Text      string
}

type AIRetrievalInput struct {
	SourceKey string
	Keyword   string
	GroupID   int64
	UserID    int64
	MessageID int64
	Question  string
	Score     float64
}

func NewService(store Store, opts Options) *Service {
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{store: store, now: now}
}

// RecordKeywordReply stores one exact keyword or alias hit.
func (s *Service) RecordKeywordReply(ctx context.Context, input KeywordReplyInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.RecordKnowledgeTrigger(ctx, Event{
		EventKey:    eventKey(TriggerTypeKeywordReply, input.GroupID, input.MessageID, input.SourceKey, input.Text),
		SourceKey:   input.SourceKey,
		Keyword:     input.Keyword,
		TriggerType: TriggerTypeKeywordReply,
		GroupID:     input.GroupID,
		UserID:      input.UserID,
		MessageID:   input.MessageID,
		TriggerText: trimRunes(input.Text, maxTriggerTextRunes),
		TriggeredAt: s.now(),
	})
}

// RecordAIRetrieval stores one knowledge document returned by /ai retrieval.
func (s *Service) RecordAIRetrieval(ctx context.Context, input AIRetrievalInput) error {
	if s == nil || s.store == nil {
		return nil
	}
	return s.store.RecordKnowledgeTrigger(ctx, Event{
		EventKey:    eventKey(TriggerTypeAIRetrieval, input.GroupID, input.MessageID, input.SourceKey, input.Question),
		SourceKey:   input.SourceKey,
		Keyword:     input.Keyword,
		TriggerType: TriggerTypeAIRetrieval,
		GroupID:     input.GroupID,
		UserID:      input.UserID,
		MessageID:   input.MessageID,
		TriggerText: trimRunes(input.Question, maxTriggerTextRunes),
		Score:       input.Score,
		TriggeredAt: s.now(),
	})
}

func (s *Service) Summaries(ctx context.Context, since *time.Time, limit int) ([]Summary, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	return s.store.ListKnowledgeTriggerSummaries(ctx, since, limit)
}

// SummariesForDays returns all-time summaries when days is zero. Positive
// values include today and the preceding days using the service clock's zone.
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

func FormatSummaries(summaries []Summary) string {
	if len(summaries) == 0 {
		return "暂无统计数据"
	}
	lines := []string{"词条触发 Top 10:"}
	for i, summary := range summaries {
		keyword := strings.TrimSpace(summary.Keyword)
		if keyword == "" {
			keyword = summary.SourceKey
		}
		lines = append(lines, fmt.Sprintf("%d. %s（%s）%d 次，最近：%s", i+1, keyword, triggerTypeLabel(summary.TriggerType), summary.Count, formatTime(summary.LastTriggered)))
	}
	return strings.Join(lines, "\n")
}

func eventKey(triggerType string, groupID, messageID int64, sourceKey, text string) string {
	var key string
	if messageID != 0 {
		key = fmt.Sprintf("%s:%d:%d:%s", triggerType, groupID, messageID, sourceKey)
	} else {
		sum := sha256.Sum256([]byte(text))
		key = fmt.Sprintf("%s:%d:%s:%x", triggerType, groupID, sourceKey, sum[:8])
	}
	if utf8.RuneCountInString(key) <= maxEventKeyRunes {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return triggerType + ":" + hex.EncodeToString(sum[:])
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

func trimRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "无"
	}
	return t.Format("2006-01-02 15:04:05")
}
