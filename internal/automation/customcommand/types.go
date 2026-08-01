package customcommand

import (
	"context"
	"errors"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

var (
	ErrForbidden          = errors.New("custom command operation forbidden")
	ErrInvalidInput       = errors.New("invalid custom command input")
	ErrNotFound           = errors.New("custom command not found")
	ErrConflict           = errors.New("custom command conflict")
	ErrGatewayUnavailable = errors.New("custom command gateway unavailable")
	ErrOutcomeUnknown     = errors.New("custom command action outcome unknown")
)

type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

type ScopeType string

const (
	ScopeGlobal ScopeType = "global"
	ScopeGroups ScopeType = "groups"
)

type TriggerPermission string

const (
	TriggerEveryone             TriggerPermission = "everyone"
	TriggerGroupAdmin           TriggerPermission = "group_admin"
	TriggerMaintenanceAllowlist TriggerPermission = "maintenance_allowlist"
)

type ParameterType string

const (
	ParameterText        ParameterType = "text"
	ParameterInteger     ParameterType = "integer"
	ParameterDuration    ParameterType = "duration"
	ParameterMember      ParameterType = "member"
	ParameterFixedOption ParameterType = "fixed_option"
)

type ActionType string

const (
	ActionReplyText     ActionType = "reply_text"
	ActionMention       ActionType = "mention"
	ActionMuteMember    ActionType = "mute_member"
	ActionSendGroupText ActionType = "send_group_text"
)

type MentionTarget string

const (
	MentionTriggerer MentionTarget = "triggerer"
	MentionParameter MentionTarget = "parameter"
)

type DurationSourceType string

const (
	DurationFixed     DurationSourceType = "fixed"
	DurationParameter DurationSourceType = "parameter"
)

type SenderRole string

const (
	SenderOwner  SenderRole = "owner"
	SenderAdmin  SenderRole = "admin"
	SenderMember SenderRole = "member"
)

type Scope struct {
	Type     ScopeType
	GroupIDs []string
}

type FixedOption struct {
	Value string
	Label string
}

type Parameter struct {
	Name           string
	DisplayName    string
	Type           ParameterType
	Required       bool
	MinLength      int
	MaxLength      int
	Minimum        int64
	Maximum        int64
	MinimumSeconds int64
	MaximumSeconds int64
	AllowTriggerer bool
	Options        []FixedOption
}

type DurationSource struct {
	Type      DurationSourceType
	Seconds   int64
	Parameter string
}

type Action struct {
	Type            ActionType
	Template        string
	Target          MentionTarget
	MemberParameter string
	Duration        DurationSource
	TargetGroupIDs  []string
}

type Definition struct {
	Name              string
	DisplayName       string
	Description       string
	Scope             Scope
	TriggerPermission TriggerPermission
	Parameters        []Parameter
	Actions           []Action
}

type Command struct {
	ID string
	Definition
	Enabled   bool
	Status    Status
	Version   uint64
	CreatedAt time.Time
	UpdatedAt time.Time
	UpdatedBy audit.Actor
}

type Patch struct {
	Name              auth.Field[string]
	DisplayName       auth.Field[string]
	Description       auth.Field[string]
	Scope             auth.Field[Scope]
	TriggerPermission auth.Field[TriggerPermission]
	Parameters        auth.Field[[]Parameter]
	Actions           auth.Field[[]Action]
	Enabled           auth.Field[bool]
}

type ListQuery struct {
	Query             string
	Enabled           *bool
	Status            Status
	ScopeType         ScopeType
	GroupID           string
	ActionType        ActionType
	TriggerPermission TriggerPermission
	Cursor            string
	Limit             int
}

type RunResult string

const (
	RunSuccess    RunResult = "success"
	RunDenied     RunResult = "denied"
	RunParseError RunResult = "parse_error"
	RunFailed     RunResult = "failed"
	RunPartial    RunResult = "partial"
	RunUnknown    RunResult = "unknown"
)

type StepResult string

const (
	StepSuccess StepResult = "success"
	StepFailed  StepResult = "failed"
	StepUnknown StepResult = "unknown"
	StepSkipped StepResult = "skipped"
)

type ActionStep struct {
	Index     int
	Type      ActionType
	Result    StepResult
	Duration  time.Duration
	ErrorCode *string
}

// ArgumentSummary deliberately stores no argument value. Digest is an
// optional keyed HMAC for correlation and is not reversible.
type ArgumentSummary struct {
	Name       string
	Type       ParameterType
	Present    bool
	RuneLength int
	Digest     string
}

type Run struct {
	ID                string
	RunIdentity       string
	CommandID         string
	CommandName       string
	GroupID           string
	TriggeredByQQ     string
	Result            RunResult
	ArgumentSummaries []ArgumentSummary
	ActionSteps       []ActionStep
	Duration          time.Duration
	ErrorCode         *string
	RequestID         string
	OccurredAt        time.Time
}

type Page[T any] struct {
	Items      []T
	NextCursor string
	HasMore    bool
}

type RunListQuery struct {
	CommandID string
	Result    RunResult
	From      *time.Time
	To        *time.Time
	Cursor    string
	Limit     int
}

type ValidationIssue struct {
	Path    string
	Code    string
	Message string
}

type ValidationSample struct {
	GroupID    string
	SenderQQ   string
	SenderRole SenderRole
	Message    string
}

type ParsedArgument struct {
	Name         string
	Type         ParameterType
	DisplayValue string
}

type RenderedAction struct {
	Index   int
	Type    ActionType
	Preview string
}

type ValidationResult struct {
	Valid           bool
	Issues          []ValidationIssue
	Warnings        []ValidationIssue
	ParsedArguments []ParsedArgument
	RenderedActions []RenderedAction
}

type MutationContext struct {
	Actor      auth.Principal
	Request    auth.MutationContext
	OccurredAt time.Time
}

type CreateMutation struct {
	Context    MutationContext
	Definition Definition
	Status     Status
	Enabled    bool
}

type UpdateMutation struct {
	Context          MutationContext
	CommandID        string
	ExpectedRevision uint64
	Patch            Patch
}

type DeleteMutation struct {
	Context          MutationContext
	CommandID        string
	ExpectedRevision uint64
}

// Store implementations must enforce active-name uniqueness and revision
// checks atomically inside create, update, and delete transactions.
type Store interface {
	CommandNameExists(context.Context, string, string) (bool, error)
	CreateCommand(context.Context, CreateMutation) (Command, error)
	GetCommand(context.Context, string) (Command, bool, error)
	ListCommands(context.Context, ListQuery) (Page[Command], error)
	UpdateCommand(context.Context, UpdateMutation) (Command, error)
	DeleteCommand(context.Context, DeleteMutation) error
	ListCommandRuns(context.Context, RunListQuery) (Page[Run], error)
	RecordCommandRun(context.Context, Run) (Run, error)
}

type Gateway interface {
	Available() bool
	ReplyText(context.Context, string, string) error
	Mention(context.Context, string, string) error
	MuteMember(context.Context, string, string, time.Duration) error
	SendGroupText(context.Context, string, string) error
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type ExecuteInput struct {
	RunIdentity            string
	GroupID                string
	SenderQQ               string
	SenderRole             SenderRole
	MaintenanceAllowlisted bool
	Message                string
	RequestID              string
}
