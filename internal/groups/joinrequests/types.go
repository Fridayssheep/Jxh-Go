package joinrequests

import (
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type ObservedStatus string
type DecisionStatus string
type Action string
type DecisionSource string
type AttemptStatus string
type AIParseStatus string
type SubType string
type RequestSource string
type Sort string
type ExternalOutcome string
type ItemOutcome string

const (
	ObservedPending ObservedStatus = "pending"
	ObservedChecked ObservedStatus = "checked"

	DecisionPending           DecisionStatus = "pending"
	DecisionProcessing        DecisionStatus = "processing"
	DecisionApproved          DecisionStatus = "approved"
	DecisionRejected          DecisionStatus = "rejected"
	DecisionExternalProcessed DecisionStatus = "external_processed"
	DecisionUnknown           DecisionStatus = "unknown"

	ActionApprove Action = "approve"
	ActionReject  Action = "reject"

	SourceManual    DecisionSource = "manual"
	SourceAutomatic DecisionSource = "automatic"
	SourceExternal  DecisionSource = "external"

	AttemptStarted   AttemptStatus = "started"
	AttemptConfirmed AttemptStatus = "confirmed"
	AttemptFailed    AttemptStatus = "failed"
	AttemptUnknown   AttemptStatus = "unknown"

	AIParsePending   AIParseStatus = "pending"
	AIParseRunning   AIParseStatus = "running"
	AIParseSucceeded AIParseStatus = "succeeded"
	AIParseFailed    AIParseStatus = "failed"
	AIParseSkipped   AIParseStatus = "skipped"

	SubTypeAdd    SubType = "add"
	SubTypeInvite SubType = "invite"

	RequestSourceEvent  RequestSource = "event"
	RequestSourceSystem RequestSource = "system"

	SortRequestedDesc Sort = "requested_at_desc"
	SortRequestedAsc  Sort = "requested_at_asc"

	ExternalConfirmed   ExternalOutcome = "confirmed"
	ExternalFailed      ExternalOutcome = "failed"
	ExternalUnknown     ExternalOutcome = "unknown"
	ExternalUnavailable ExternalOutcome = "unavailable"

	ItemConfirmed ItemOutcome = "confirmed"
	ItemFailed    ItemOutcome = "failed"
	ItemUnknown   ItemOutcome = "unknown"
)

const (
	PolicyModeAIFieldsComplete = "ai_fields_complete"
	AutoApprovalRuleVersion    = uint64(1)
)

var requiredPolicyFields = [...]string{"student_id", "name", "major"}

func PolicyRequiredFields() []string {
	return append([]string(nil), requiredPolicyFields[:]...)
}

type GroupReference struct {
	ID   string
	Name string
}

type ApplicantFields struct {
	StudentID        *string
	Name             *string
	Major            *string
	Valid            bool
	ValidationErrors []string
}

type AIParseResult struct {
	Status      AIParseStatus
	Fields      *ApplicantFields
	ErrorCode   *string
	CompletedAt *time.Time
}

type Request struct {
	ID                  string
	Group               GroupReference
	ApplicantQQ         string
	ApplicantNickname   *string
	VerificationMessage string
	SubType             SubType
	Source              RequestSource
	ObservedStatus      ObservedStatus
	DecisionStatus      DecisionStatus
	DecisionSource      *DecisionSource
	AIParse             AIParseResult
	StudentIDAssessment StudentIDAssessment
	RequestedAt         time.Time
	Overdue             bool
	Version             uint64
	LastDecisionID      *string
	Comment             *string
	FirstObservedAt     time.Time
	LastObservedAt      time.Time
}

type Policy struct {
	GroupID        string
	Enabled        bool
	Mode           string
	RequiredFields []string
	AutoReject     bool
	Version        uint64
	UpdatedAt      time.Time
	UpdatedBy      *audit.Actor
}

type PolicyPatch struct {
	Enabled    auth.Field[bool]
	AutoReject auth.Field[bool]
}

type Decision struct {
	ID            string
	RequestID     string
	Action        Action
	Source        DecisionSource
	Status        AttemptStatus
	Actor         *audit.Actor
	Reason        *string
	RuleVersion   *uint64
	FieldSnapshot *ApplicantFields
	StartedAt     time.Time
	CompletedAt   *time.Time
	ErrorCode     *string
	TraceID       string
}

type DecisionInput struct {
	Action Action
	Reason string
}

type DecisionResult struct {
	Request  Request
	Decision Decision
}

type VersionedRequest struct {
	ID      string
	Version uint64
}

type BulkInput struct {
	GroupID string
	Action  Action
	Reason  string
	Items   []VersionedRequest
}

type ItemError struct {
	Code      string
	Message   string
	Retryable bool
}

type BulkItemResult struct {
	RequestID string
	Outcome   ItemOutcome
	Request   Request
	Decision  Decision
	Error     *ItemError
}

type BulkResult struct {
	GroupID        string
	Action         Action
	Items          []BulkItemResult
	ConfirmedCount uint64
	FailedCount    uint64
	UnknownCount   uint64
}

type ListQuery struct {
	GroupID          string
	DecisionStatuses []DecisionStatus
	ObservedStatus   ObservedStatus
	AIParseStatus    AIParseStatus
	SubType          SubType
	Source           RequestSource
	DecisionSource   DecisionSource
	RequestedFrom    *time.Time
	RequestedTo      *time.Time
	Overdue          *bool
	OverdueBefore    *time.Time
	Query            string
	Sort             Sort
	Cursor           string
	Limit            int
}

type DecisionListQuery struct {
	RequestID string
	Cursor    string
	Limit     int
}

type Page[T any] struct {
	Items      []T
	NextCursor string
	HasMore    bool
}

type MutationContext struct {
	Actor      audit.Actor
	Request    auth.MutationContext
	OccurredAt time.Time
}

type BeginMutation struct {
	Context             MutationContext
	GroupID             string
	Items               []VersionedRequest
	Action              Action
	Source              DecisionSource
	Reason              *string
	IdempotencyKey      string
	ProcessingExpiresAt time.Time
	RuleVersion         *uint64
	FieldSnapshots      map[string]ApplicantFields
}

type ReservedItem struct {
	Request  Request
	Decision Decision
}

type Reservation struct {
	Replay bool
	Items  []ReservedItem
}

type CompletionMutation struct {
	DecisionID     string
	RequestID      string
	AttemptStatus  AttemptStatus
	DecisionStatus DecisionStatus
	ErrorCode      *string
	CompletedAt    time.Time
}

type AutoCandidate struct {
	Request Request
	Policy  Policy
}

type ExternalResult struct {
	Outcome   ExternalOutcome
	ErrorCode string
}
