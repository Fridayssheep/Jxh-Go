package idempotency

import (
	"context"
	"errors"
	"time"
)

var (
	ErrKeyReused     = errors.New("idempotency key reused with different request")
	ErrInvalidStore  = errors.New("invalid idempotency store")
	ErrInvalidSecret = errors.New("invalid idempotency HMAC secret")
	ErrInvalidInput  = errors.New("invalid idempotency input")
	ErrInvalidRecord = errors.New("invalid idempotency store record")
)

type ActorType string

const (
	ActorAdminUser ActorType = "admin_user"
	ActorQQUser    ActorType = "qq_user"
	ActorSystem    ActorType = "system"
)

type Actor struct {
	Type ActorType
	ID   string
}

type State string

const (
	StateInProgress State = "in_progress"
	StateCompleted  State = "completed"
)

type ResultStatus string

const (
	ResultSucceeded ResultStatus = "succeeded"
	ResultFailed    ResultStatus = "failed"
	ResultUnknown   ResultStatus = "unknown"
)

type Resource struct {
	Type string
	ID   string
}

type Result struct {
	Status             ResultStatus
	ResponseStatus     int
	ErrorCode          string
	Resource           *Resource
	ResultingSessionID string
	TraceID            string
	CompletedAt        time.Time
}

// Reservation mirrors the allowlisted admin_idempotency_keys columns. Fresh is
// transient and is true only for the caller that won the insert race.
type Reservation struct {
	ID          uint64
	ActorType   ActorType
	ActorID     string
	Operation   string
	Key         string
	RequestHash string
	State       State
	Fresh       bool
	Result      *Result
	CreatedAt   time.Time
	CompletedAt *time.Time
	ExpiresAt   time.Time
}

type Completion struct {
	RequestHash string
	Result      Result
	CompletedAt time.Time
}

type Store interface {
	// ReserveIdempotency atomically inserts or reads the winner for
	// actor_type + actor_id + operation + key.
	ReserveIdempotency(ctx context.Context, reservation Reservation) (Reservation, error)
	// CompleteIdempotency conditionally transitions in_progress to completed and
	// returns the original completed result when another caller won the race.
	CompleteIdempotency(ctx context.Context, id uint64, completion Completion) (Reservation, error)
}

type Disposition string

const (
	DispositionExecute    Disposition = "execute"
	DispositionInProgress Disposition = "in_progress"
	DispositionReplay     Disposition = "replay"
)

type Execution struct {
	ReservationID uint64
	Actor         Actor
	Operation     string
	Key           string
	RequestHash   string
}

type BeginInput struct {
	Actor     Actor
	Operation string
	Key       string
	Payload   any
}

type BeginResult struct {
	Disposition Disposition
	RequestHash string
	Execution   *Execution
	Result      *Result
}

type Options struct {
	TTL time.Duration
	Now func() time.Time
}
