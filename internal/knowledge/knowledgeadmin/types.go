package knowledgeadmin

import (
	"context"
	"errors"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

var (
	ErrForbidden           = errors.New("knowledge operation forbidden")
	ErrInvalidInput        = errors.New("invalid knowledge input")
	ErrInvalidData         = errors.New("invalid knowledge data")
	ErrNotFound            = errors.New("knowledge entry not found")
	ErrReloadInProgress    = errors.New("knowledge reload already in progress")
	ErrReloaderUnavailable = errors.New("knowledge reloader unavailable")
	ErrIdempotencyConflict = errors.New("knowledge reload idempotency conflict")
)

type State string

const (
	StateReady         State = "ready"
	StateReloading     State = "reloading"
	StateDegraded      State = "degraded"
	StateUnavailable   State = "unavailable"
	StateNotConfigured State = "not_configured"
)

type OperationStatus string

const (
	OperationAccepted  OperationStatus = "accepted"
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
)

type ReloadOperation struct {
	ID          string
	Status      OperationStatus
	StartedAt   time.Time
	CompletedAt *time.Time
	ErrorCode   *string
}

type Status struct {
	State              State
	SourceConfigured   bool
	ActiveIndexVersion *string
	EntryCount         int
	ConflictCount      int
	LastAttemptAt      *time.Time
	LastSuccessAt      *time.Time
	LastErrorCode      *string
	CurrentOperation   *ReloadOperation
}

type EntryType string

const (
	EntryTypeExactReply  EntryType = "exact_reply"
	EntryTypeAIKnowledge EntryType = "ai_knowledge"
	EntryTypeHybrid      EntryType = "hybrid"
)

type EntrySummary struct {
	ID              string
	Title           string
	Category        string
	Type            EntryType
	Keywords        []string
	Aliases         []string
	Enabled         bool
	ExactReply      bool
	AIEnabled       bool
	HasConflict     bool
	SourceUpdatedAt *time.Time
	IndexedAt       time.Time
}

type Entry struct {
	ID              string
	SourceKey       string
	Title           string
	Category        string
	Type            EntryType
	Keywords        []string
	Aliases         []string
	Question        string
	Answer          string
	Enabled         bool
	ExactReply      bool
	AIEnabled       bool
	HasConflict     bool
	SourceUpdatedAt *time.Time
	IndexedAt       time.Time
}

type EntryQuery struct {
	Query       string
	Category    string
	Type        EntryType
	Enabled     *bool
	ExactReply  *bool
	AIEnabled   *bool
	HasConflict *bool
	Cursor      string
	Page        int
	Limit       int
}

type EntryPage struct {
	Items      []EntrySummary
	NextCursor string
	HasMore    bool
	TotalCount int
}

type ConflictType string

const (
	ConflictSourceKey ConflictType = "source_key"
	ConflictKeyword   ConflictType = "keyword"
	ConflictAlias     ConflictType = "alias"
)

type Conflict struct {
	ID         string
	Type       ConflictType
	Key        string
	EntryIDs   []string
	DetectedAt time.Time
}

type ConflictQuery struct {
	Query  string
	Type   ConflictType
	Cursor string
	Page   int
	Limit  int
}

type ConflictPage struct {
	Items      []Conflict
	NextCursor string
	HasMore    bool
	TotalCount int
}

type Store interface {
	Status(ctx context.Context) (Status, error)
	ListEntries(ctx context.Context, query EntryQuery) (EntryPage, error)
	GetEntry(ctx context.Context, id string) (Entry, bool, error)
	ListConflicts(ctx context.Context, query ConflictQuery) (ConflictPage, error)
}

type ReloadResult struct {
	ActiveIndexVersion string
	EntryCount         int
	ConflictCount      int
}

type Reloader interface {
	Reload(ctx context.Context) (ReloadResult, error)
}

type BeginReload struct {
	Actor          auth.Principal
	Context        auth.MutationContext
	OperationID    string
	IdempotencyKey string
	RequestHash    string
	RequestedAt    time.Time
}

type ReloadTransition struct {
	OperationID    string
	From           OperationStatus
	To             OperationStatus
	At             time.Time
	ErrorCode      string
	OutcomeUnknown bool
}

type OperationStore interface {
	BeginKnowledgeReload(ctx context.Context, begin BeginReload) (operation ReloadOperation, fresh bool, err error)
	TransitionKnowledgeReload(ctx context.Context, transition ReloadTransition) (ReloadOperation, error)
	RecoverInterruptedKnowledgeReloads(ctx context.Context, recoveredAt time.Time) ([]ReloadOperation, error)
}

type EventSink interface {
	Publish(draft events.Draft) (events.Event, error)
}

type Options struct {
	Store                 Store
	Operations            OperationStore
	Reloader              Reloader
	Events                EventSink
	IdempotencySecret     []byte
	ReloadTimeout         time.Duration
	WorkerContext         context.Context
	PersistenceRetryDelay time.Duration
	Now                   func() time.Time
	NewOperationID        func() string
}
