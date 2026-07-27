package audit

import "time"

type ActorType string

const (
	ActorAdminUser ActorType = "admin_user"
	ActorQQUser    ActorType = "qq_user"
	ActorSystem    ActorType = "system"
)

type Source string

const (
	SourceWeb    Source = "web"
	SourceQQ     Source = "qq"
	SourceSystem Source = "system"
)

type Result string

const (
	ResultSuccess Result = "success"
	ResultFailed  Result = "failed"
	ResultUnknown Result = "unknown"
)

type Actor struct {
	Type        ActorType
	UserID      *string
	QQUserID    *string
	DisplayName string
}

type Target struct {
	Type        string
	ID          string
	DisplayName string
}

type Context struct {
	Actor     Actor
	Source    Source
	IPAddress *string
	UserAgent *string
	RequestID string
}

type Log struct {
	ID         string
	OccurredAt time.Time
	Actor      Actor
	Action     string
	Target     Target
	Result     Result
	ErrorCode  *string
	RequestID  string
	Source     Source
	IPAddress  *string
	UserAgent  *string
	Before     map[string]any
	After      map[string]any
	Metadata   map[string]any
	Redacted   bool
}

type Summary struct {
	ID         string
	OccurredAt time.Time
	Actor      Actor
	Action     string
	Target     Target
	Result     Result
	ErrorCode  *string
	RequestID  string
}

type ListQuery struct {
	ActorUserID string
	ActorType   ActorType
	Actions     []string
	TargetTypes []string
	Result      Result
	From        *time.Time
	To          *time.Time
	Cursor      string
	Limit       int
}

type Page struct {
	Items      []Summary
	NextCursor string
	HasMore    bool
}
