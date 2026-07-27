package telemetry

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidOptions = errors.New("invalid telemetry options")
	ErrAlreadyRunning = errors.New("telemetry worker is already running")
	ErrFlushFailed    = errors.New("telemetry batch flush failed")
)

type EventKind string

const (
	EventGroupMessage      EventKind = "group_message"
	EventKeywordReply      EventKind = "keyword_reply"
	EventAIRequest         EventKind = "ai_request"
	EventJoinRequest       EventKind = "join_request"
	EventManualApproval    EventKind = "manual_approval"
	EventAutomaticApproval EventKind = "automatic_approval"
	EventScheduledJobRun   EventKind = "scheduled_job_run"
	EventCommandRun        EventKind = "command_run"
	EventLinkClean         EventKind = "link_clean"
	EventQuote             EventKind = "quote"
)

type Result string

const (
	ResultSuccess     Result = "success"
	ResultFailed      Result = "failed"
	ResultUnknown     Result = "unknown"
	ResultDenied      Result = "denied"
	ResultTimeout     Result = "timeout"
	ResultFallback    Result = "fallback"
	ResultNoKnowledge Result = "no_knowledge"
	ResultBusy        Result = "busy"
	ResultDisabled    Result = "disabled"
	ResultParseFailed Result = "parse_failed"
	ResultPartial     Result = "partial"
)

// Observation deliberately has no message-body or arbitrary metadata field.
// Raw QQ user IDs are accepted only at this boundary and are never queued or
// passed to Store.
type Observation struct {
	Kind         EventKind
	OccurredAt   time.Time
	GroupID      int64
	UserID       int64
	FeatureKey   string
	Result       Result
	Duration     time.Duration
	CommandID    string
	KnowledgeKey string
	Count        int64
}

type Event struct {
	Kind         EventKind
	OccurredAt   time.Time
	GroupID      string
	UserKey      string
	FeatureKey   string
	Result       Result
	DurationMS   int64
	CommandID    string
	KnowledgeKey string
	Count        int64
}

type Store interface {
	AppendTelemetryEvents(ctx context.Context, events []Event) error
}

type Options struct {
	Store         Store
	HMACSecret    []byte
	Capacity      int
	BatchSize     int
	FlushInterval time.Duration
	FlushTimeout  time.Duration
	Now           func() time.Time
	Logger        *log.Logger
}

type Service struct {
	store         Store
	secret        []byte
	queue         chan Event
	batchSize     int
	flushInterval time.Duration
	flushTimeout  time.Duration
	now           func() time.Time
	logger        *log.Logger
	dropped       atomic.Uint64
	running       atomic.Bool
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil || len(options.HMACSecret) < 32 || options.Capacity < 1 ||
		options.BatchSize < 1 || options.BatchSize > options.Capacity || options.FlushInterval <= 0 ||
		options.FlushTimeout <= 0 || options.Now == nil {
		return nil, ErrInvalidOptions
	}
	if options.Logger == nil {
		options.Logger = log.New(io.Discard, "", 0)
	}
	return &Service{
		store: options.Store, secret: append([]byte(nil), options.HMACSecret...),
		queue: make(chan Event, options.Capacity), batchSize: options.BatchSize,
		flushInterval: options.FlushInterval, flushTimeout: options.FlushTimeout,
		now: options.Now, logger: options.Logger,
	}, nil
}

// Record validates and transforms an observation before attempting a
// non-blocking enqueue. False means invalid input or a full queue.
func (s *Service) Record(observation Observation) bool {
	event, ok := s.eventFromObservation(observation)
	if !ok {
		s.dropped.Add(1)
		return false
	}
	select {
	case s.queue <- event:
		return true
	default:
		s.dropped.Add(1)
		return false
	}
}

func (s *Service) Dropped() uint64 { return s.dropped.Load() }

func (s *Service) Pending() int { return len(s.queue) }

func (s *Service) Run(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidOptions
	}
	if !s.running.CompareAndSwap(false, true) {
		return ErrAlreadyRunning
	}
	defer s.running.Store(false)

	ticker := time.NewTicker(s.flushInterval)
	defer ticker.Stop()
	batch := make([]Event, 0, s.batchSize)
	for {
		var input <-chan Event
		if len(batch) < s.batchSize {
			input = s.queue
		}
		select {
		case event := <-input:
			batch = append(batch, event)
			if len(batch) == s.batchSize {
				if err := s.flush(ctx, batch); err == nil {
					batch = batch[:0]
				}
			}
		case <-ticker.C:
			if len(batch) > 0 {
				if err := s.flush(ctx, batch); err == nil {
					batch = batch[:0]
				}
			}
		case <-ctx.Done():
			return s.flushOnShutdown(batch)
		}
	}
}

func (s *Service) eventFromObservation(value Observation) (Event, bool) {
	if !validKind(value.Kind) || !validResult(value.Result) || value.GroupID <= 0 || value.UserID < 0 ||
		value.Duration < 0 || value.Duration > time.Hour || value.Count < 0 ||
		!validBounded(value.FeatureKey, 64) || !validBounded(value.CommandID, 256) ||
		!validBounded(value.KnowledgeKey, 256) {
		return Event{}, false
	}
	if value.OccurredAt.IsZero() {
		value.OccurredAt = s.now()
	}
	userKey := ""
	if value.UserID > 0 {
		mac := hmac.New(sha256.New, s.secret)
		_, _ = mac.Write([]byte(strconv.FormatInt(value.UserID, 10)))
		userKey = hex.EncodeToString(mac.Sum(nil))
	}
	count := value.Count
	if count == 0 {
		count = 1
	}
	return Event{
		Kind: value.Kind, OccurredAt: value.OccurredAt.UTC(), GroupID: strconv.FormatInt(value.GroupID, 10),
		UserKey: userKey, FeatureKey: value.FeatureKey, Result: value.Result,
		DurationMS: value.Duration.Milliseconds(), CommandID: value.CommandID,
		KnowledgeKey: value.KnowledgeKey, Count: count,
	}, true
}

func (s *Service) flush(ctx context.Context, batch []Event) error {
	if len(batch) == 0 {
		return nil
	}
	copyOfBatch := append([]Event(nil), batch...)
	if err := s.store.AppendTelemetryEvents(ctx, copyOfBatch); err != nil {
		s.logger.Printf("telemetry batch flush failed count=%d", len(batch))
		return ErrFlushFailed
	}
	return nil
}

func (s *Service) flushOnShutdown(batch []Event) error {
	remaining := append([]Event(nil), batch...)
	for {
		for len(remaining) < s.batchSize {
			select {
			case event := <-s.queue:
				remaining = append(remaining, event)
			default:
				goto flush
			}
		}
	flush:
		if len(remaining) == 0 {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), s.flushTimeout)
		err := s.flush(ctx, remaining)
		cancel()
		if err != nil {
			s.dropped.Add(uint64(len(remaining) + len(s.queue)))
			return err
		}
		remaining = remaining[:0]
		if len(s.queue) == 0 {
			return nil
		}
	}
}

func validBounded(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(strings.TrimSpace(value)) <= maximum
}

func validKind(value EventKind) bool {
	switch value {
	case EventGroupMessage, EventKeywordReply, EventAIRequest, EventJoinRequest, EventManualApproval,
		EventAutomaticApproval, EventScheduledJobRun, EventCommandRun, EventLinkClean, EventQuote:
		return true
	default:
		return false
	}
}

func validResult(value Result) bool {
	switch value {
	case ResultSuccess, ResultFailed, ResultUnknown, ResultDenied, ResultTimeout, ResultFallback,
		ResultNoKnowledge, ResultBusy, ResultDisabled, ResultParseFailed, ResultPartial:
		return true
	default:
		return false
	}
}

func (e Event) String() string {
	return fmt.Sprintf("%s/%s", e.Kind, e.Result)
}
