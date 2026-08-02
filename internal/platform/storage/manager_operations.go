package storage

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/automation/scheduledjobs"
	"github.com/zjutjh/jxh-go/internal/automation/scheduler"
	"github.com/zjutjh/jxh-go/internal/groups/grouprequest"
	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/analytics"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	_ joinrequests.Store         = (*Store)(nil)
	_ scheduler.RuntimeStore     = (*Store)(nil)
	_ scheduledjobs.Store        = (*Store)(nil)
	_ customcommand.Store        = (*Store)(nil)
	_ telemetry.Store            = (*Store)(nil)
	_ telemetry.MaintenanceStore = (*Store)(nil)
	_ analytics.Store            = (*Store)(nil)
)

const (
	databaseDailyJob = "\u6bcf\u5929"
	databaseOnceJob  = "\u5355\u6b21"
)

type OpsActorColumns struct {
	Type        string  `gorm:"column:actor_type"`
	UserID      *string `gorm:"column:actor_user_id"`
	QQUserID    *string `gorm:"column:actor_qq_user_id"`
	DisplayName string  `gorm:"column:actor_display_name"`
	Role        *string `gorm:"column:actor_role"`
}

type ManagerUpdatedByColumns struct {
	Type        string  `gorm:"column:updated_by_type"`
	UserID      *string `gorm:"column:updated_by_user_id"`
	QQUserID    *string `gorm:"column:updated_by_qq_user_id"`
	DisplayName string  `gorm:"column:updated_by_display_name"`
	Role        *string `gorm:"column:updated_by_role"`
}

type managedGroupManagerRow struct {
	GroupID int64  `gorm:"column:group_id"`
	Name    string `gorm:"column:name"`
}

func (managedGroupManagerRow) TableName() string { return "managed_groups" }

type joinPolicyManagerRow struct {
	GroupID        int64  `gorm:"column:group_id;primaryKey"`
	Enabled        bool   `gorm:"column:enabled"`
	Mode           string `gorm:"column:mode"`
	RequiredFields []byte `gorm:"column:required_fields"`
	AutoReject     bool   `gorm:"column:auto_reject"`
	Revision       uint64 `gorm:"column:revision"`
	ManagerUpdatedByColumns
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (joinPolicyManagerRow) TableName() string { return "group_join_policies" }

type joinRequestManagerRow struct {
	InternalID         uint64     `gorm:"column:internal_id"`
	Flag               string     `gorm:"column:flag"`
	GroupID            *int64     `gorm:"column:group_id"`
	GroupName          string     `gorm:"column:group_name"`
	UserID             *int64     `gorm:"column:user_id"`
	ApplicantNickname  *string    `gorm:"column:applicant_nickname"`
	StudentID          *string    `gorm:"column:student_id"`
	StudentName        *string    `gorm:"column:student_name"`
	Major              *string    `gorm:"column:major"`
	SubType            string     `gorm:"column:sub_type"`
	Comment            *string    `gorm:"column:comment"`
	Source             string     `gorm:"column:source"`
	AIParseStatus      string     `gorm:"column:ai_parse_status"`
	AIErrorCode        *string    `gorm:"column:ai_error_code"`
	ValidationSnapshot []byte     `gorm:"column:validation_snapshot"`
	AIParsedAt         *time.Time `gorm:"column:ai_parsed_at"`
	ObservedStatus     string     `gorm:"column:observed_status"`
	DecisionStatus     string     `gorm:"column:decision_status"`
	DecisionSource     *string    `gorm:"column:decision_source"`
	Revision           uint64     `gorm:"column:revision"`
	LastDecisionID     *string    `gorm:"column:last_decision_id"`
	ProcessingExpires  *time.Time `gorm:"column:processing_expires_at"`
	RequestedAt        *time.Time `gorm:"column:requested_at"`
	FirstSeenAt        *time.Time `gorm:"column:first_seen_at"`
	LastSeenAt         *time.Time `gorm:"column:last_seen_at"`
}

type joinDecisionManagerRow struct {
	DecisionID        string  `gorm:"column:decision_id;primaryKey"`
	RequestInternalID uint64  `gorm:"column:request_id"`
	RequestFlag       string  `gorm:"column:request_flag;->"`
	IdempotencyKey    string  `gorm:"column:idempotency_key"`
	Action            string  `gorm:"column:action"`
	Status            string  `gorm:"column:status"`
	Source            string  `gorm:"column:source"`
	Reason            *string `gorm:"column:reason"`
	OpsActorColumns
	FieldSnapshot      []byte     `gorm:"column:field_snapshot"`
	ValidationSnapshot []byte     `gorm:"column:validation_snapshot"`
	RuleVersion        *uint64    `gorm:"column:rule_version"`
	ErrorCode          *string    `gorm:"column:error_code"`
	TraceID            string     `gorm:"column:trace_id"`
	StartedAt          time.Time  `gorm:"column:started_at"`
	CompletedAt        *time.Time `gorm:"column:completed_at"`
}

func (joinDecisionManagerRow) TableName() string { return "group_join_decisions" }

type scheduledJobManagerRow struct {
	ID            uint64     `gorm:"column:id;primaryKey"`
	Name          string     `gorm:"column:name"`
	Type          string     `gorm:"column:type"`
	TimeHHMM      string     `gorm:"column:time_hhmm"`
	RunDate       *time.Time `gorm:"column:run_date"`
	GroupID       int64      `gorm:"column:group_id"`
	GroupName     string     `gorm:"column:group_name"`
	Message       string     `gorm:"column:message"`
	Enabled       bool       `gorm:"column:enabled"`
	Status        string     `gorm:"column:status"`
	Timezone      string     `gorm:"column:timezone"`
	RunAt         *time.Time `gorm:"column:run_at"`
	LastRunAt     *time.Time `gorm:"column:last_run_at"`
	Revision      uint64     `gorm:"column:revision"`
	LastRunResult *string    `gorm:"column:last_run_result"`
	ManagerUpdatedByColumns
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
	ArchivedAt *time.Time `gorm:"column:archived_at"`
}

func (scheduledJobManagerRow) TableName() string { return "scheduled_jobs" }

type scheduledRunManagerRow struct {
	RunID                  string     `gorm:"column:run_id;primaryKey"`
	RunIdentity            string     `gorm:"column:run_identity"`
	JobID                  uint64     `gorm:"column:job_id"`
	Kind                   string     `gorm:"column:kind"`
	Result                 string     `gorm:"column:result"`
	ScheduledFor           *time.Time `gorm:"column:scheduled_for"`
	StartedAt              time.Time  `gorm:"column:started_at"`
	CompletedAt            *time.Time `gorm:"column:completed_at"`
	DurationMS             uint64     `gorm:"column:duration_ms"`
	MessageID              *string    `gorm:"column:message_id"`
	ErrorCode              *string    `gorm:"column:error_code"`
	ErrorMessage           *string    `gorm:"column:error_message"`
	TriggeredByType        *string    `gorm:"column:triggered_by_type"`
	TriggeredByUserID      *string    `gorm:"column:triggered_by_user_id"`
	TriggeredByQQUserID    *string    `gorm:"column:triggered_by_qq_user_id"`
	TriggeredByDisplayName *string    `gorm:"column:triggered_by_display_name"`
	RequestID              *string    `gorm:"column:request_id"`
}

func (scheduledRunManagerRow) TableName() string { return "scheduled_job_runs" }

type customCommandManagerRow struct {
	CommandID         string `gorm:"column:command_id;primaryKey"`
	Name              string `gorm:"column:name"`
	DisplayName       string `gorm:"column:display_name"`
	Description       string `gorm:"column:description"`
	ScopeType         string `gorm:"column:scope_type"`
	ScopeJSON         []byte `gorm:"column:scope_json"`
	TriggerPermission string `gorm:"column:trigger_permission"`
	ParametersJSON    []byte `gorm:"column:parameters_json"`
	ActionsJSON       []byte `gorm:"column:actions_json"`
	Enabled           bool   `gorm:"column:enabled"`
	Status            string `gorm:"column:status"`
	Revision          uint64 `gorm:"column:revision"`
	ManagerUpdatedByColumns
	CreatedAt  time.Time  `gorm:"column:created_at"`
	UpdatedAt  time.Time  `gorm:"column:updated_at"`
	ArchivedAt *time.Time `gorm:"column:archived_at"`
}

func (customCommandManagerRow) TableName() string { return "custom_commands" }

type customRunManagerRow struct {
	RunID         string    `gorm:"column:run_id;primaryKey"`
	RunIdentity   string    `gorm:"column:run_identity"`
	CommandID     string    `gorm:"column:command_id"`
	CommandName   string    `gorm:"column:command_name"`
	GroupID       int64     `gorm:"column:group_id"`
	TriggeredByQQ string    `gorm:"column:triggered_by_qq"`
	Result        string    `gorm:"column:result"`
	ActionSteps   []byte    `gorm:"column:action_steps"`
	DurationMS    uint64    `gorm:"column:duration_ms"`
	ErrorCode     *string   `gorm:"column:error_code"`
	RequestID     *string   `gorm:"column:request_id"`
	OccurredAt    time.Time `gorm:"column:occurred_at"`
}

func (customRunManagerRow) TableName() string { return "custom_command_runs" }

type telemetryEventManagerRow struct {
	EventID    uint64    `gorm:"column:event_id;primaryKey;autoIncrement"`
	EventType  string    `gorm:"column:event_type"`
	GroupID    *int64    `gorm:"column:group_id"`
	FeatureKey *string   `gorm:"column:feature_key"`
	ActorHash  *string   `gorm:"column:actor_hash"`
	CommandID  *string   `gorm:"column:command_id"`
	JobID      *uint64   `gorm:"column:job_id"`
	Outcome    *string   `gorm:"column:outcome"`
	DurationMS *uint64   `gorm:"column:duration_ms"`
	Metadata   []byte    `gorm:"column:metadata"`
	OccurredAt time.Time `gorm:"column:occurred_at"`
}

func (telemetryEventManagerRow) TableName() string { return "bot_operation_events" }

type telemetryDailyManagerRow struct {
	BucketDate  time.Time `gorm:"column:bucket_date;primaryKey"`
	Timezone    string    `gorm:"column:timezone;primaryKey"`
	MetricKey   string    `gorm:"column:metric_key;primaryKey"`
	GroupID     int64     `gorm:"column:group_id;primaryKey"`
	FeatureKey  string    `gorm:"column:feature_key;primaryKey"`
	Outcome     string    `gorm:"column:outcome;primaryKey"`
	ValueCount  uint64    `gorm:"column:value_count"`
	ValueSum    float64   `gorm:"column:value_sum"`
	SampleCount uint64    `gorm:"column:sample_count"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`
}

func (telemetryDailyManagerRow) TableName() string { return "bot_operation_daily" }

type adminAuditManagerRow struct {
	AuditLogID       string    `gorm:"column:audit_log_id;primaryKey"`
	OccurredAt       time.Time `gorm:"column:occurred_at"`
	ActorType        string    `gorm:"column:actor_type"`
	ActorUserID      *string   `gorm:"column:actor_user_id"`
	ActorQQUserID    *string   `gorm:"column:actor_qq_user_id"`
	ActorDisplayName *string   `gorm:"column:actor_display_name"`
	ActorRole        *string   `gorm:"column:actor_role"`
	ScopeType        *string   `gorm:"column:scope_type"`
	ScopeID          *string   `gorm:"column:scope_id"`
	Action           string    `gorm:"column:action"`
	TargetType       *string   `gorm:"column:target_type"`
	TargetID         *string   `gorm:"column:target_id"`
	TargetDisplay    *string   `gorm:"column:target_display_name"`
	Result           string    `gorm:"column:result"`
	ErrorCode        *string   `gorm:"column:error_code"`
	RequestID        string    `gorm:"column:request_id"`
	Source           string    `gorm:"column:source"`
	IPAddress        *string   `gorm:"column:ip_address"`
	UserAgent        *string   `gorm:"column:user_agent"`
	BeforeSnapshot   []byte    `gorm:"column:before_snapshot"`
	AfterSnapshot    []byte    `gorm:"column:after_snapshot"`
	Metadata         []byte    `gorm:"column:metadata"`
	Redacted         bool      `gorm:"column:redacted"`
}

func (adminAuditManagerRow) TableName() string { return "admin_audit_logs" }

type managerCursor struct {
	Millis int64  `json:"t"`
	ID     string `json:"i"`
}

func encodeManagerCursor(at time.Time, id string) string {
	encoded, _ := json.Marshal(managerCursor{Millis: at.UTC().UnixMilli(), ID: id})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func decodeManagerCursor(value string) (managerCursor, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return managerCursor{}, err
	}
	var cursor managerCursor
	if err := json.Unmarshal(decoded, &cursor); err != nil || cursor.ID == "" {
		return managerCursor{}, errors.New("invalid cursor")
	}
	return cursor, nil
}

func utcTime(value time.Time) time.Time { return value.UTC() }

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := value.UTC()
	return &converted
}

func opsNewID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate manager operation id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(value[:]), nil
}

func opsOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func opsMarshalJSON(value any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode manager operation JSON: %w", err)
	}
	return encoded, nil
}

func opsMarshalOptionalJSON(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return opsMarshalJSON(value)
}

func opsUnmarshalJSON(data []byte, target any) error {
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode manager JSON: %w", err)
	}
	return nil
}

func opsIsDuplicateKey(err error) bool {
	var mysqlError *drivermysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func escapeManagerLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

func principalUpdatedBy(value auth.Principal) ManagerUpdatedByColumns {
	role := string(value.Role)
	userID := value.UserID
	return ManagerUpdatedByColumns{
		Type: string(audit.ActorAdminUser), UserID: &userID, DisplayName: value.UserID, Role: &role,
	}
}

func auditActorUpdatedBy(value audit.Actor) ManagerUpdatedByColumns {
	return ManagerUpdatedByColumns{
		Type: string(value.Type), UserID: value.UserID, QQUserID: value.QQUserID, DisplayName: value.DisplayName,
	}
}

func updatedByActor(value ManagerUpdatedByColumns) audit.Actor {
	return audit.Actor{
		Type: audit.ActorType(value.Type), UserID: value.UserID, QQUserID: value.QQUserID, DisplayName: value.DisplayName,
	}
}

func decisionActor(value OpsActorColumns) *audit.Actor {
	if value.Type == "" {
		return nil
	}
	return &audit.Actor{
		Type: audit.ActorType(value.Type), UserID: value.UserID, QQUserID: value.QQUserID, DisplayName: value.DisplayName,
	}
}

func principalDecisionActor(value auth.Principal) OpsActorColumns {
	role := string(value.Role)
	userID := value.UserID
	return OpsActorColumns{
		Type: string(audit.ActorAdminUser), UserID: &userID, DisplayName: value.UserID, Role: &role,
	}
}

func domainDecisionActor(value audit.Actor) OpsActorColumns {
	return OpsActorColumns{
		Type: string(value.Type), UserID: value.UserID, QQUserID: value.QQUserID, DisplayName: value.DisplayName,
	}
}

type managerAuditWrite struct {
	Actor      OpsActorColumns
	OccurredAt time.Time
	Request    auth.MutationContext
	Source     audit.Source
	Action     string
	TargetType string
	TargetID   string
	TargetName string
	Before     any
	After      any
	Metadata   any
}

func writeManagerAudit(tx *gorm.DB, value managerAuditWrite) error {
	id, err := opsNewID("aud")
	if err != nil {
		return err
	}
	sanitizedBefore, beforeRedacted := audit.SanitizeForWrite(value.Before)
	sanitizedAfter, afterRedacted := audit.SanitizeForWrite(value.After)
	sanitizedMetadata, metadataRedacted := audit.SanitizeForWrite(value.Metadata)
	before, err := opsMarshalOptionalJSON(sanitizedBefore)
	if err != nil {
		return err
	}
	after, err := opsMarshalOptionalJSON(sanitizedAfter)
	if err != nil {
		return err
	}
	metadata, err := opsMarshalJSON(sanitizedMetadata)
	if err != nil {
		return err
	}
	source := value.Source
	if source == "" {
		source = audit.SourceWeb
	}
	row := adminAuditManagerRow{
		AuditLogID: id, OccurredAt: value.OccurredAt.UTC(), ActorType: value.Actor.Type,
		ActorUserID: value.Actor.UserID, ActorQQUserID: value.Actor.QQUserID,
		ActorDisplayName: opsOptionalString(value.Actor.DisplayName), ActorRole: value.Actor.Role,
		Action: value.Action, TargetType: opsOptionalString(value.TargetType), TargetID: opsOptionalString(value.TargetID),
		TargetDisplay: opsOptionalString(value.TargetName), Result: string(audit.ResultSuccess),
		RequestID: value.Request.RequestID, Source: string(source), IPAddress: opsOptionalString(value.Request.IPAddress),
		UserAgent: opsOptionalString(value.Request.UserAgent), BeforeSnapshot: before, AfterSnapshot: after,
		Metadata: metadata, Redacted: beforeRedacted || afterRedacted || metadataRedacted,
	}
	return tx.Create(&row).Error
}

type applicantFieldsPayload struct {
	StudentID        *string  `json:"student_id,omitempty"`
	Name             *string  `json:"name,omitempty"`
	Major            *string  `json:"major,omitempty"`
	Valid            bool     `json:"valid"`
	ValidationErrors []string `json:"validation_errors"`
}

func applicantPayload(value joinrequests.ApplicantFields) applicantFieldsPayload {
	return applicantFieldsPayload{
		StudentID: value.StudentID, Name: value.Name, Major: value.Major, Valid: value.Valid,
		ValidationErrors: append([]string(nil), value.ValidationErrors...),
	}
}

func applicantFromPayload(value applicantFieldsPayload) joinrequests.ApplicantFields {
	return joinrequests.ApplicantFields{
		StudentID: value.StudentID, Name: value.Name, Major: value.Major, Valid: value.Valid,
		ValidationErrors: append([]string(nil), value.ValidationErrors...),
	}
}

func selectJoinRequestManager(db *gorm.DB) *gorm.DB {
	return db.Table("group_join_requests AS request").
		Select(`request.id AS internal_id, request.flag, request.group_id, COALESCE(managed.name, '') AS group_name,
request.user_id, request.applicant_nickname, request.student_id, request.student_name, request.major,
request.sub_type, request.comment, request.source, request.ai_parse_status, request.ai_error_code,
request.validation_snapshot, request.ai_parsed_at, request.observed_status, request.decision_status,
request.decision_source, request.revision, request.last_decision_id, request.processing_expires_at,
request.requested_at, request.first_seen_at, request.last_seen_at`).
		Joins("LEFT JOIN managed_groups AS managed ON managed.group_id = request.group_id")
}

func joinRequestFromManagerRow(row joinRequestManagerRow) (joinrequests.Request, error) {
	var fields *joinrequests.ApplicantFields
	if row.StudentID != nil || row.StudentName != nil || row.Major != nil || len(row.ValidationSnapshot) > 0 {
		payload := applicantFieldsPayload{StudentID: row.StudentID, Name: row.StudentName, Major: row.Major}
		if len(row.ValidationSnapshot) > 0 {
			if err := opsUnmarshalJSON(row.ValidationSnapshot, &payload); err != nil {
				return joinrequests.Request{}, err
			}
			if payload.StudentID == nil {
				payload.StudentID = row.StudentID
			}
			if payload.Name == nil {
				payload.Name = row.StudentName
			}
			if payload.Major == nil {
				payload.Major = row.Major
			}
		}
		converted := applicantFromPayload(payload)
		fields = &converted
	}
	groupID, applicantQQ := "", ""
	if row.GroupID != nil {
		groupID = strconv.FormatInt(*row.GroupID, 10)
	}
	if row.UserID != nil {
		applicantQQ = strconv.FormatInt(*row.UserID, 10)
	}
	verification := ""
	if row.Comment != nil {
		verification = *row.Comment
	}
	requestedAt, firstSeenAt, lastSeenAt := time.Time{}, time.Time{}, time.Time{}
	if row.RequestedAt != nil {
		requestedAt = row.RequestedAt.UTC()
	}
	if row.FirstSeenAt != nil {
		firstSeenAt = row.FirstSeenAt.UTC()
	}
	if row.LastSeenAt != nil {
		lastSeenAt = row.LastSeenAt.UTC()
	}
	var decisionSource *joinrequests.DecisionSource
	if row.DecisionSource != nil {
		converted := joinrequests.DecisionSource(*row.DecisionSource)
		decisionSource = &converted
	}
	return joinrequests.Request{
		ID: row.Flag, Group: joinrequests.GroupReference{ID: groupID, Name: row.GroupName}, ApplicantQQ: applicantQQ,
		ApplicantNickname: row.ApplicantNickname, VerificationMessage: verification,
		SubType: joinrequests.SubType(row.SubType), Source: joinrequests.RequestSource(row.Source),
		ObservedStatus: joinrequests.ObservedStatus(row.ObservedStatus), DecisionStatus: joinrequests.DecisionStatus(row.DecisionStatus),
		DecisionSource: decisionSource, AIParse: joinrequests.AIParseResult{
			Status: joinrequests.AIParseStatus(row.AIParseStatus), Fields: fields, ErrorCode: row.AIErrorCode,
			CompletedAt: utcTimePointer(row.AIParsedAt),
		}, RequestedAt: requestedAt, Version: row.Revision, LastDecisionID: row.LastDecisionID,
		Comment: row.Comment, FirstObservedAt: firstSeenAt, LastObservedAt: lastSeenAt,
	}, nil
}

func decisionFromManagerRow(row joinDecisionManagerRow) (joinrequests.Decision, error) {
	var snapshot *joinrequests.ApplicantFields
	if len(row.FieldSnapshot) > 0 || len(row.ValidationSnapshot) > 0 {
		var payload applicantFieldsPayload
		if err := opsUnmarshalJSON(row.FieldSnapshot, &payload); err != nil {
			return joinrequests.Decision{}, err
		}
		if len(row.ValidationSnapshot) > 0 {
			var validation applicantFieldsPayload
			if err := opsUnmarshalJSON(row.ValidationSnapshot, &validation); err != nil {
				return joinrequests.Decision{}, err
			}
			payload.Valid = validation.Valid
			payload.ValidationErrors = validation.ValidationErrors
		}
		converted := applicantFromPayload(payload)
		snapshot = &converted
	}
	return joinrequests.Decision{
		ID: row.DecisionID, RequestID: row.RequestFlag, Action: joinrequests.Action(row.Action),
		Source: joinrequests.DecisionSource(row.Source), Status: joinrequests.AttemptStatus(row.Status),
		Actor: decisionActor(row.OpsActorColumns), Reason: row.Reason, RuleVersion: row.RuleVersion,
		FieldSnapshot: snapshot, StartedAt: row.StartedAt.UTC(), CompletedAt: utcTimePointer(row.CompletedAt),
		ErrorCode: row.ErrorCode, TraceID: row.TraceID,
	}, nil
}

func (s *Store) GetPolicy(ctx context.Context, groupID string) (joinrequests.Policy, bool, error) {
	var row joinPolicyManagerRow
	err := s.db.WithContext(ctx).Where("group_id = ?", groupID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return joinrequests.Policy{}, false, nil
	}
	if err != nil {
		return joinrequests.Policy{}, false, err
	}
	value, err := policyFromManagerRow(row)
	return value, err == nil, err
}

func policyFromManagerRow(row joinPolicyManagerRow) (joinrequests.Policy, error) {
	var required []string
	if err := opsUnmarshalJSON(row.RequiredFields, &required); err != nil {
		return joinrequests.Policy{}, err
	}
	actor := updatedByActor(row.ManagerUpdatedByColumns)
	return joinrequests.Policy{
		GroupID: strconv.FormatInt(row.GroupID, 10), Enabled: row.Enabled, Mode: row.Mode,
		RequiredFields: required, AutoReject: row.AutoReject, Version: row.Revision,
		UpdatedAt: row.UpdatedAt.UTC(), UpdatedBy: &actor,
	}, nil
}

func (s *Store) UpdatePolicy(ctx context.Context, mutation joinrequests.PolicyMutation) (joinrequests.Policy, error) {
	var result joinrequests.Policy
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before joinPolicyManagerRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id = ?", mutation.GroupID).First(&before).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return joinrequests.ErrConflict
			}
			return err
		}
		if before.Revision != mutation.ExpectedRevision {
			return joinrequests.ErrConflict
		}
		nextEnabled := before.Enabled
		nextAutoReject := before.AutoReject
		if mutation.Patch.Enabled.Set {
			nextEnabled = mutation.Patch.Enabled.Value
		}
		if mutation.Patch.AutoReject.Set {
			nextAutoReject = mutation.Patch.AutoReject.Value
		}
		if nextEnabled == before.Enabled && nextAutoReject == before.AutoReject {
			converted, err := policyFromManagerRow(before)
			if err != nil {
				return err
			}
			result = converted
			return nil
		}
		actor := auditActorUpdatedBy(mutation.Context.Actor)
		updates := map[string]any{
			"revision":        gorm.Expr("revision + 1"),
			"updated_by_type": actor.Type, "updated_by_user_id": actor.UserID,
			"updated_by_qq_user_id": actor.QQUserID, "updated_by_display_name": actor.DisplayName,
			"updated_by_role": actor.Role, "updated_at": mutation.Context.OccurredAt.UTC(),
			"enabled": nextEnabled, "auto_reject": nextAutoReject,
		}
		updated := tx.Model(&joinPolicyManagerRow{}).
			Where("group_id = ? AND revision = ?", mutation.GroupID, mutation.ExpectedRevision).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		var after joinPolicyManagerRow
		if err := tx.Where("group_id = ?", mutation.GroupID).First(&after).Error; err != nil {
			return err
		}
		converted, err := policyFromManagerRow(after)
		if err != nil {
			return err
		}
		result = converted
		// updated_at is the automatic-policy cutoff used by candidate selection.
		// Any effective change to an active policy must retire requests before that
		// cutoff in this same transaction; otherwise they remain pending forever
		// while ListAutoCandidates excludes them.
		if after.Enabled || after.AutoReject {
			if err := retirePendingRequestsBeforePolicyCutoff(tx, after, mutation.Context); err != nil {
				return err
			}
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: domainDecisionActor(mutation.Context.Actor), OccurredAt: mutation.Context.OccurredAt,
			Request: mutation.Context.Request, Source: sourceForManagerActor(mutation.Context.Actor.Type),
			Action: "join_policy.update", TargetType: "group_join_policy", TargetID: mutation.GroupID,
			Before: map[string]any{"enabled": before.Enabled, "auto_reject": before.AutoReject, "revision": before.Revision},
			After: map[string]any{
				"enabled": after.Enabled, "auto_reject": after.AutoReject, "revision": after.Revision,
			}, Metadata: map[string]any{},
		})
	})
	return result, err
}

func (s *Store) RetireStaleAutomaticRequests(ctx context.Context, mutation joinrequests.MutationContext) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var policies []joinPolicyManagerRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("enabled = ? OR auto_reject = ?", true, true).
			Order("group_id ASC").Find(&policies).Error; err != nil {
			return err
		}
		for _, policy := range policies {
			if err := retirePendingRequestsBeforePolicyCutoff(tx, policy, mutation); err != nil {
				return err
			}
		}
		return nil
	})
}

// retirePendingRequestsBeforePolicyCutoff prevents an automatic policy from
// acting on applications that predate its current effective configuration.
// The request history and prior decision attempts remain intact; the audit
// rows document why each old pending item now needs explicit human review.
func retirePendingRequestsBeforePolicyCutoff(tx *gorm.DB, policy joinPolicyManagerRow, context joinrequests.MutationContext) error {
	var requests []struct {
		ID       uint64 `gorm:"column:id"`
		Flag     string `gorm:"column:flag"`
		Revision uint64 `gorm:"column:revision"`
	}
	cutoff := policy.UpdatedAt.UTC()
	if err := tx.Table("group_join_requests").Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("id, flag, revision").
		Where("group_id = ? AND status = ? AND observed_status = ? AND decision_status = ? AND (COALESCE(first_seen_at, requested_at) < ? OR (first_seen_at IS NULL AND requested_at IS NULL))",
			policy.GroupID, grouprequest.StatusPending, joinrequests.ObservedPending, joinrequests.DecisionPending, cutoff).
		Order("id ASC").Scan(&requests).Error; err != nil {
		return err
	}
	for _, request := range requests {
		updated := tx.Model(&GroupJoinRequest{}).
			Where("id = ? AND revision = ? AND decision_status = ?", request.ID, request.Revision, joinrequests.DecisionPending).
			Updates(map[string]any{
				"decision_status":       joinrequests.DecisionUnknown,
				"revision":              gorm.Expr("revision + 1"),
				"processing_expires_at": nil,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		if err := writeManagerAudit(tx, managerAuditWrite{
			Actor: domainDecisionActor(context.Actor), OccurredAt: context.OccurredAt,
			Request: context.Request, Source: sourceForManagerActor(context.Actor.Type),
			Action: "join_request.auto_policy_cutoff", TargetType: "group_join_request", TargetID: request.Flag,
			Before: map[string]any{"decision_status": joinrequests.DecisionPending, "revision": request.Revision},
			After:  map[string]any{"decision_status": joinrequests.DecisionUnknown, "revision": request.Revision + 1},
			Metadata: map[string]any{
				"reason": "predates_automatic_policy_cutoff", "policy_revision": policy.Revision,
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func sourceForManagerActor(actorType audit.ActorType) audit.Source {
	if actorType == audit.ActorSystem {
		return audit.SourceSystem
	}
	if actorType == audit.ActorQQUser {
		return audit.SourceQQ
	}
	return audit.SourceWeb
}

func (s *Store) ListRequests(ctx context.Context, query joinrequests.ListQuery) (joinrequests.Page[joinrequests.Request], error) {
	db := selectJoinRequestManager(s.db.WithContext(ctx))
	if query.GroupID != "" {
		db = db.Where("request.group_id = ?", query.GroupID)
	}
	if len(query.DecisionStatuses) > 0 {
		values := make([]string, len(query.DecisionStatuses))
		for index, value := range query.DecisionStatuses {
			values[index] = string(value)
		}
		db = db.Where("request.decision_status IN ?", values)
	}
	if query.ObservedStatus != "" {
		db = db.Where("request.observed_status = ?", query.ObservedStatus)
	}
	if query.AIParseStatus != "" {
		db = db.Where("request.ai_parse_status = ?", query.AIParseStatus)
	}
	if query.SubType != "" {
		db = db.Where("request.sub_type = ?", query.SubType)
	}
	if query.Source != "" {
		db = db.Where("request.source = ?", query.Source)
	}
	if query.DecisionSource != "" {
		db = db.Where("request.decision_source = ?", query.DecisionSource)
	}
	if query.RequestedFrom != nil {
		db = db.Where("request.requested_at >= ?", query.RequestedFrom.UTC())
	}
	if query.RequestedTo != nil {
		db = db.Where("request.requested_at < ?", query.RequestedTo.UTC())
	}
	if query.Overdue != nil && query.OverdueBefore != nil {
		if *query.Overdue {
			db = db.Where("request.decision_status = ? AND request.requested_at < ?", joinrequests.DecisionPending, query.OverdueBefore.UTC())
		} else {
			db = db.Where("request.decision_status <> ? OR request.requested_at >= ?", joinrequests.DecisionPending, query.OverdueBefore.UTC())
		}
	}
	if query.Query != "" {
		pattern := "%" + escapeManagerLike(query.Query) + "%"
		db = db.Where(`request.flag LIKE ? ESCAPE '\\' OR CAST(request.user_id AS CHAR) LIKE ? ESCAPE '\\'
OR COALESCE(request.applicant_nickname, '') LIKE ? ESCAPE '\\'`, pattern, pattern, pattern)
	}
	var totalCount int64
	if err := db.Distinct("request.id").Count(&totalCount).Error; err != nil {
		return joinrequests.Page[joinrequests.Request]{}, err
	}
	direction, comparator := "DESC", "<"
	if query.Sort == joinrequests.SortRequestedAsc {
		direction, comparator = "ASC", ">"
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerCursor(query.Cursor)
		if err != nil {
			return joinrequests.Page[joinrequests.Request]{}, joinrequests.ErrInvalidInput
		}
		internalID, err := strconv.ParseUint(cursor.ID, 10, 64)
		if err != nil {
			return joinrequests.Page[joinrequests.Request]{}, joinrequests.ErrInvalidInput
		}
		at := time.UnixMilli(cursor.Millis).UTC()
		db = db.Where(fmt.Sprintf("(request.requested_at %s ? OR (request.requested_at = ? AND request.id %s ?))", comparator, comparator), at, at, internalID)
	} else if query.Page > 1 {
		db = db.Offset((query.Page - 1) * query.Limit)
	}
	var rows []joinRequestManagerRow
	if err := db.Order("request.requested_at " + direction).Order("request.id " + direction).Limit(query.Limit + 1).Scan(&rows).Error; err != nil {
		return joinrequests.Page[joinrequests.Request]{}, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]joinrequests.Request, len(rows))
	for index, row := range rows {
		converted, err := joinRequestFromManagerRow(row)
		if err != nil {
			return joinrequests.Page[joinrequests.Request]{}, err
		}
		if query.OverdueBefore != nil {
			converted.Overdue = converted.DecisionStatus == joinrequests.DecisionPending && converted.RequestedAt.Before(query.OverdueBefore.UTC())
		}
		items[index] = converted
	}
	next := ""
	if hasMore && len(rows) > 0 && rows[len(rows)-1].RequestedAt != nil {
		next = encodeManagerCursor(*rows[len(rows)-1].RequestedAt, strconv.FormatUint(rows[len(rows)-1].InternalID, 10))
	}
	return joinrequests.Page[joinrequests.Request]{
		Items: items, NextCursor: next, HasMore: hasMore, TotalCount: int(totalCount),
	}, nil
}

func (s *Store) GetRequest(ctx context.Context, requestID string) (joinrequests.Request, bool, error) {
	var row joinRequestManagerRow
	err := selectJoinRequestManager(s.db.WithContext(ctx)).Where("request.flag = ?", requestID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return joinrequests.Request{}, false, nil
	}
	if err != nil {
		return joinrequests.Request{}, false, err
	}
	value, err := joinRequestFromManagerRow(row)
	return value, err == nil, err
}

func selectJoinDecisionManager(db *gorm.DB) *gorm.DB {
	return db.Table("group_join_decisions AS decision").
		Select(`decision.*, request.flag AS request_flag`).
		Joins("JOIN group_join_requests AS request ON request.id = decision.request_id")
}

func (s *Store) ListDecisions(ctx context.Context, query joinrequests.DecisionListQuery) (joinrequests.Page[joinrequests.Decision], bool, error) {
	var count int64
	if err := s.db.WithContext(ctx).Table("group_join_requests").Where("flag = ?", query.RequestID).Count(&count).Error; err != nil {
		return joinrequests.Page[joinrequests.Decision]{}, false, err
	}
	if count == 0 {
		return joinrequests.Page[joinrequests.Decision]{}, false, nil
	}
	db := selectJoinDecisionManager(s.db.WithContext(ctx)).Where("request.flag = ?", query.RequestID)
	if query.Cursor != "" {
		cursor, err := decodeManagerCursor(query.Cursor)
		if err != nil {
			return joinrequests.Page[joinrequests.Decision]{}, true, joinrequests.ErrInvalidInput
		}
		at := time.UnixMilli(cursor.Millis).UTC()
		db = db.Where("decision.started_at < ? OR (decision.started_at = ? AND decision.decision_id < ?)", at, at, cursor.ID)
	}
	var rows []joinDecisionManagerRow
	if err := db.Order("decision.started_at DESC").Order("decision.decision_id DESC").Limit(query.Limit + 1).Scan(&rows).Error; err != nil {
		return joinrequests.Page[joinrequests.Decision]{}, true, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]joinrequests.Decision, len(rows))
	for index, row := range rows {
		converted, err := decisionFromManagerRow(row)
		if err != nil {
			return joinrequests.Page[joinrequests.Decision]{}, true, err
		}
		items[index] = converted
	}
	next := ""
	if hasMore && len(rows) > 0 {
		next = encodeManagerCursor(rows[len(rows)-1].StartedAt, rows[len(rows)-1].DecisionID)
	}
	return joinrequests.Page[joinrequests.Decision]{Items: items, NextCursor: next, HasMore: hasMore}, true, nil
}

func (s *Store) BeginDecisions(ctx context.Context, mutation joinrequests.BeginMutation) (joinrequests.Reservation, error) {
	var reservation joinrequests.Reservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		items := append([]joinrequests.VersionedRequest(nil), mutation.Items...)
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		if mutation.Source == joinrequests.SourceAutomatic {
			if err := lockAutomaticDecisionPolicy(tx, mutation); err != nil {
				return err
			}
		}
		locked := make(map[string]joinRequestManagerRow, len(items))
		for _, item := range items {
			var row joinRequestManagerRow
			err := selectJoinRequestManager(tx.Clauses(clause.Locking{Strength: "UPDATE"})).Where("request.flag = ?", item.ID).Take(&row).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return joinrequests.ErrNotFound
			}
			if err != nil {
				return err
			}
			locked[item.ID] = row
		}

		replayed, foundReplay, err := replayJoinDecisionReservation(tx, mutation, locked)
		if err != nil {
			return err
		}
		if foundReplay {
			reservation = replayed
			return nil
		}
		for _, item := range mutation.Items {
			row := locked[item.ID]
			if row.Revision != item.Version || row.DecisionStatus != string(joinrequests.DecisionPending) ||
				(mutation.GroupID != "" && (row.GroupID == nil || strconv.FormatInt(*row.GroupID, 10) != mutation.GroupID)) {
				return joinrequests.ErrConflict
			}
		}

		actor := domainDecisionActor(mutation.Context.Actor)
		created := make(map[string]joinDecisionManagerRow, len(mutation.Items))
		for _, item := range mutation.Items {
			requestRow := locked[item.ID]
			decisionID, err := opsNewID("dec")
			if err != nil {
				return err
			}
			traceID, err := opsNewID("trc")
			if err != nil {
				return err
			}
			var fieldsJSON, validationJSON []byte
			if fields, ok := mutation.FieldSnapshots[item.ID]; ok {
				payload := applicantPayload(fields)
				fieldsJSON, err = opsMarshalJSON(applicantFieldsPayload{StudentID: payload.StudentID, Name: payload.Name, Major: payload.Major})
				if err != nil {
					return err
				}
				validationJSON, err = opsMarshalJSON(applicantFieldsPayload{Valid: payload.Valid, ValidationErrors: payload.ValidationErrors})
				if err != nil {
					return err
				}
			}
			decision := joinDecisionManagerRow{
				DecisionID: decisionID, RequestInternalID: requestRow.InternalID,
				IdempotencyKey: mutation.IdempotencyKey, Action: string(mutation.Action), Status: string(joinrequests.AttemptStarted),
				Source: string(mutation.Source), Reason: mutation.Reason, OpsActorColumns: actor,
				FieldSnapshot: fieldsJSON, ValidationSnapshot: validationJSON, RuleVersion: mutation.RuleVersion,
				TraceID: traceID, StartedAt: mutation.Context.OccurredAt.UTC(),
			}
			if err := tx.Create(&decision).Error; err != nil {
				if opsIsDuplicateKey(err) {
					return joinrequests.ErrIdempotencyConflict
				}
				return err
			}
			update := tx.Model(&GroupJoinRequest{}).Where("id = ? AND revision = ? AND decision_status = ?", requestRow.InternalID, item.Version, joinrequests.DecisionPending).
				Updates(map[string]any{
					"decision_status": joinrequests.DecisionProcessing, "decision_source": mutation.Source,
					"revision": gorm.Expr("revision + 1"), "last_decision_id": decisionID,
					"processing_expires_at": mutation.ProcessingExpiresAt.UTC(),
				})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return joinrequests.ErrConflict
			}
			decision.RequestFlag = item.ID
			created[item.ID] = decision
			if err := writeManagerAudit(tx, managerAuditWrite{
				Actor: actor, OccurredAt: mutation.Context.OccurredAt, Request: mutation.Context.Request,
				Source: sourceForManagerActor(mutation.Context.Actor.Type), Action: "join_request.decision",
				TargetType: "group_join_request", TargetID: item.ID,
				Before:   map[string]any{"decision_status": requestRow.DecisionStatus, "revision": requestRow.Revision},
				After:    map[string]any{"decision_status": joinrequests.DecisionProcessing, "revision": requestRow.Revision + 1, "decision_id": decisionID},
				Metadata: map[string]any{"action": mutation.Action, "source": mutation.Source},
			}); err != nil {
				return err
			}
		}
		reservation.Items = make([]joinrequests.ReservedItem, 0, len(mutation.Items))
		for _, item := range mutation.Items {
			requestRow, err := loadJoinRequestManagerRow(tx, item.ID)
			if err != nil {
				return err
			}
			request, err := joinRequestFromManagerRow(requestRow)
			if err != nil {
				return err
			}
			decision, err := decisionFromManagerRow(created[item.ID])
			if err != nil {
				return err
			}
			reservation.Items = append(reservation.Items, joinrequests.ReservedItem{Request: request, Decision: decision})
		}
		return nil
	})
	return reservation, err
}

func lockAutomaticDecisionPolicy(tx *gorm.DB, mutation joinrequests.BeginMutation) error {
	if mutation.GroupID == "" || mutation.PolicyRevision == nil ||
		(mutation.Action != joinrequests.ActionApprove && mutation.Action != joinrequests.ActionReject) {
		return joinrequests.ErrInvalidInput
	}
	var policy joinPolicyManagerRow
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("group_id = ?", mutation.GroupID).Take(&policy).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return joinrequests.ErrConflict
	}
	if err != nil {
		return err
	}
	if policy.Revision != *mutation.PolicyRevision ||
		mutation.Action == joinrequests.ActionApprove && !policy.Enabled ||
		mutation.Action == joinrequests.ActionReject && !policy.AutoReject {
		return joinrequests.ErrConflict
	}
	return nil
}

func replayJoinDecisionReservation(tx *gorm.DB, mutation joinrequests.BeginMutation, locked map[string]joinRequestManagerRow) (joinrequests.Reservation, bool, error) {
	internalIDs := make([]uint64, 0, len(locked))
	for _, row := range locked {
		internalIDs = append(internalIDs, row.InternalID)
	}
	var rows []joinDecisionManagerRow
	if err := tx.Where("request_id IN ? AND idempotency_key = ?", internalIDs, mutation.IdempotencyKey).Find(&rows).Error; err != nil {
		return joinrequests.Reservation{}, false, err
	}
	if len(rows) == 0 {
		return joinrequests.Reservation{}, false, nil
	}
	if len(rows) != len(mutation.Items) {
		return joinrequests.Reservation{}, false, joinrequests.ErrIdempotencyConflict
	}
	byInternalID := make(map[uint64]joinDecisionManagerRow, len(rows))
	for _, row := range rows {
		byInternalID[row.RequestInternalID] = row
	}
	result := joinrequests.Reservation{Replay: true, Items: make([]joinrequests.ReservedItem, 0, len(mutation.Items))}
	expectedActor := domainDecisionActor(mutation.Context.Actor)
	for _, item := range mutation.Items {
		requestRow := locked[item.ID]
		decisionRow, ok := byInternalID[requestRow.InternalID]
		if !ok || decisionRow.Action != string(mutation.Action) || decisionRow.Source != string(mutation.Source) ||
			!sameManagerString(decisionRow.Reason, mutation.Reason) || !sameManagerUint(decisionRow.RuleVersion, mutation.RuleVersion) ||
			!sameOpsActor(decisionRow.OpsActorColumns, expectedActor) {
			return joinrequests.Reservation{}, false, joinrequests.ErrIdempotencyConflict
		}
		if err := validateReplayFieldSnapshot(decisionRow, mutation.FieldSnapshots, item.ID); err != nil {
			return joinrequests.Reservation{}, false, err
		}
		request, err := joinRequestFromManagerRow(requestRow)
		if err != nil {
			return joinrequests.Reservation{}, false, err
		}
		decisionRow.RequestFlag = item.ID
		decision, err := decisionFromManagerRow(decisionRow)
		if err != nil {
			return joinrequests.Reservation{}, false, err
		}
		result.Items = append(result.Items, joinrequests.ReservedItem{Request: request, Decision: decision})
	}
	return result, true, nil
}

func sameOpsActor(left, right OpsActorColumns) bool {
	return left.Type == right.Type && sameManagerString(left.UserID, right.UserID) &&
		sameManagerString(left.QQUserID, right.QQUserID) && left.DisplayName == right.DisplayName
}

func validateReplayFieldSnapshot(row joinDecisionManagerRow, snapshots map[string]joinrequests.ApplicantFields, requestID string) error {
	fields, exists := snapshots[requestID]
	if !exists {
		if len(row.FieldSnapshot) != 0 || len(row.ValidationSnapshot) != 0 {
			return joinrequests.ErrIdempotencyConflict
		}
		return nil
	}
	payload := applicantPayload(fields)
	expectedFields, err := opsMarshalJSON(applicantFieldsPayload{StudentID: payload.StudentID, Name: payload.Name, Major: payload.Major})
	if err != nil {
		return err
	}
	expectedValidation, err := opsMarshalJSON(applicantFieldsPayload{Valid: payload.Valid, ValidationErrors: payload.ValidationErrors})
	if err != nil {
		return err
	}
	if !bytes.Equal(row.FieldSnapshot, expectedFields) || !bytes.Equal(row.ValidationSnapshot, expectedValidation) {
		return joinrequests.ErrIdempotencyConflict
	}
	return nil
}

func sameManagerString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameManagerUint(left, right *uint64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func loadJoinRequestManagerRow(tx *gorm.DB, requestID string) (joinRequestManagerRow, error) {
	var row joinRequestManagerRow
	err := selectJoinRequestManager(tx).Where("request.flag = ?", requestID).Take(&row).Error
	return row, err
}

func (s *Store) CompleteDecision(ctx context.Context, mutation joinrequests.CompletionMutation) (joinrequests.DecisionResult, error) {
	var result joinrequests.DecisionResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		requestRow, err := loadJoinRequestManagerRow(tx.Clauses(clause.Locking{Strength: "UPDATE"}), mutation.RequestID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return joinrequests.ErrNotFound
		}
		if err != nil {
			return err
		}
		var decisionRow joinDecisionManagerRow
		err = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("decision_id = ? AND request_id = ?", mutation.DecisionID, requestRow.InternalID).Take(&decisionRow).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return joinrequests.ErrNotFound
		}
		if err != nil {
			return err
		}
		if decisionRow.Status != string(joinrequests.AttemptStarted) {
			if decisionRow.Status != string(mutation.AttemptStatus) || !sameManagerString(decisionRow.ErrorCode, mutation.ErrorCode) ||
				requestRow.LastDecisionID == nil || *requestRow.LastDecisionID != mutation.DecisionID ||
				requestRow.DecisionStatus != string(mutation.DecisionStatus) {
				return joinrequests.ErrConflict
			}
			return loadCompletedJoinDecisionResult(tx, requestRow, decisionRow, &result)
		}
		if requestRow.DecisionStatus != string(joinrequests.DecisionProcessing) || requestRow.LastDecisionID == nil || *requestRow.LastDecisionID != mutation.DecisionID {
			return joinrequests.ErrConflict
		}
		completedAt := mutation.CompletedAt.UTC()
		updateDecision := tx.Model(&joinDecisionManagerRow{}).Where("decision_id = ? AND status = ?", mutation.DecisionID, joinrequests.AttemptStarted).
			Updates(map[string]any{"status": mutation.AttemptStatus, "error_code": mutation.ErrorCode, "completed_at": completedAt})
		if updateDecision.Error != nil {
			return updateDecision.Error
		}
		if updateDecision.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		updateRequest := tx.Model(&GroupJoinRequest{}).Where("id = ? AND decision_status = ? AND last_decision_id = ?", requestRow.InternalID, joinrequests.DecisionProcessing, mutation.DecisionID).
			Updates(map[string]any{
				"decision_status": mutation.DecisionStatus, "revision": gorm.Expr("revision + 1"), "processing_expires_at": nil,
			})
		if updateRequest.Error != nil {
			return updateRequest.Error
		}
		if updateRequest.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		requestRow, err = loadJoinRequestManagerRow(tx, mutation.RequestID)
		if err != nil {
			return err
		}
		decisionRow.Status, decisionRow.ErrorCode, decisionRow.CompletedAt = string(mutation.AttemptStatus), mutation.ErrorCode, &completedAt
		return loadCompletedJoinDecisionResult(tx, requestRow, decisionRow, &result)
	})
	return result, err
}

func loadCompletedJoinDecisionResult(_ *gorm.DB, requestRow joinRequestManagerRow, decisionRow joinDecisionManagerRow, target *joinrequests.DecisionResult) error {
	request, err := joinRequestFromManagerRow(requestRow)
	if err != nil {
		return err
	}
	decisionRow.RequestFlag = requestRow.Flag
	decision, err := decisionFromManagerRow(decisionRow)
	if err != nil {
		return err
	}
	*target = joinrequests.DecisionResult{Request: request, Decision: decision}
	return nil
}

func (s *Store) ListAutoCandidates(ctx context.Context, limit int) ([]joinrequests.AutoCandidate, error) {
	var rows []struct {
		Request              joinRequestManagerRow `gorm:"embedded"`
		PolicyGroupID        int64                 `gorm:"column:policy_group_id"`
		PolicyEnabled        bool                  `gorm:"column:policy_enabled"`
		PolicyMode           string                `gorm:"column:policy_mode"`
		PolicyRequiredFields []byte                `gorm:"column:policy_required_fields"`
		PolicyAutoReject     bool                  `gorm:"column:policy_auto_reject"`
		PolicyRevision       uint64                `gorm:"column:policy_revision"`
		PolicyUpdatedAt      time.Time             `gorm:"column:policy_updated_at"`
		PolicyUpdatedType    string                `gorm:"column:policy_updated_type"`
		PolicyUpdatedUserID  *string               `gorm:"column:policy_updated_user_id"`
		PolicyUpdatedQQID    *string               `gorm:"column:policy_updated_qq_id"`
		PolicyUpdatedDisplay string                `gorm:"column:policy_updated_display"`
		PolicyUpdatedRole    *string               `gorm:"column:policy_updated_role"`
	}
	db := s.db.WithContext(ctx).Table("group_join_requests AS request").Select(`request.id AS internal_id, request.flag,
request.group_id, COALESCE(managed.name, '') AS group_name, request.user_id, request.applicant_nickname,
request.student_id, request.student_name, request.major, request.sub_type, request.comment, request.source,
request.ai_parse_status, request.ai_error_code, request.validation_snapshot, request.ai_parsed_at,
request.observed_status, request.decision_status, request.decision_source, request.revision,
request.last_decision_id, request.processing_expires_at, request.requested_at, request.first_seen_at, request.last_seen_at,
policy.group_id AS policy_group_id, policy.enabled AS policy_enabled, policy.mode AS policy_mode, policy.required_fields AS policy_required_fields,
policy.auto_reject AS policy_auto_reject, policy.revision AS policy_revision, policy.updated_at AS policy_updated_at,
policy.updated_by_type AS policy_updated_type, policy.updated_by_user_id AS policy_updated_user_id,
policy.updated_by_qq_user_id AS policy_updated_qq_id, policy.updated_by_display_name AS policy_updated_display,
policy.updated_by_role AS policy_updated_role`).
		Joins("JOIN group_join_policies AS policy ON policy.group_id = request.group_id AND (policy.enabled = TRUE OR policy.auto_reject = TRUE)").
		Joins("LEFT JOIN managed_groups AS managed ON managed.group_id = request.group_id").
		Where("request.status = ? AND request.observed_status = ? AND request.decision_status = ? AND request.sub_type = ? AND request.ai_parse_status = ?",
			grouprequest.StatusPending, joinrequests.ObservedPending, joinrequests.DecisionPending, joinrequests.SubTypeAdd, joinrequests.AIParseSucceeded).
		Where("COALESCE(request.first_seen_at, request.requested_at) >= policy.updated_at").
		Where("NOT EXISTS (SELECT 1 FROM group_join_decisions AS prior_decision WHERE prior_decision.request_id = request.id AND prior_decision.source = ? AND prior_decision.status = ?)",
			joinrequests.SourceAutomatic, joinrequests.AttemptFailed).
		Order("request.requested_at ASC").Order("request.id ASC").Limit(limit)
	if err := db.Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := make([]joinrequests.AutoCandidate, len(rows))
	for index, row := range rows {
		request, err := joinRequestFromManagerRow(row.Request)
		if err != nil {
			return nil, err
		}
		policy, err := policyFromManagerRow(joinPolicyManagerRow{
			GroupID: row.PolicyGroupID, Enabled: row.PolicyEnabled, Mode: row.PolicyMode,
			RequiredFields: row.PolicyRequiredFields, AutoReject: row.PolicyAutoReject,
			Revision: row.PolicyRevision, UpdatedAt: row.PolicyUpdatedAt,
			ManagerUpdatedByColumns: ManagerUpdatedByColumns{
				Type: row.PolicyUpdatedType, UserID: row.PolicyUpdatedUserID, QQUserID: row.PolicyUpdatedQQID,
				DisplayName: row.PolicyUpdatedDisplay, Role: row.PolicyUpdatedRole,
			},
		})
		if err != nil {
			return nil, err
		}
		result[index] = joinrequests.AutoCandidate{Request: request, Policy: policy}
	}
	return result, nil
}

func (s *Store) RecoverExpiredDecisions(ctx context.Context, expiredBefore time.Time, limit int) ([]joinrequests.Request, error) {
	var recovered []joinrequests.Request
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var candidates []struct {
			ID             uint64 `gorm:"column:id"`
			Flag           string `gorm:"column:flag"`
			LastDecisionID string `gorm:"column:last_decision_id"`
		}
		if err := tx.Table("group_join_requests").Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Select("id, flag, last_decision_id").Where("decision_status = ? AND processing_expires_at < ?", joinrequests.DecisionProcessing, expiredBefore.UTC()).
			Order("processing_expires_at ASC").Order("id ASC").Limit(limit).Scan(&candidates).Error; err != nil {
			return err
		}
		for _, candidate := range candidates {
			completed := expiredBefore.UTC()
			decisionUpdate := tx.Model(&joinDecisionManagerRow{}).
				Where("decision_id = ? AND request_id = ? AND status = ?", candidate.LastDecisionID, candidate.ID, joinrequests.AttemptStarted).
				Updates(map[string]any{"status": joinrequests.AttemptUnknown, "error_code": "processing_lease_expired", "completed_at": completed})
			if decisionUpdate.Error != nil {
				return decisionUpdate.Error
			}
			if decisionUpdate.RowsAffected != 1 {
				continue
			}
			requestUpdate := tx.Model(&GroupJoinRequest{}).
				Where("id = ? AND decision_status = ? AND last_decision_id = ?", candidate.ID, joinrequests.DecisionProcessing, candidate.LastDecisionID).
				Updates(map[string]any{"decision_status": joinrequests.DecisionUnknown, "revision": gorm.Expr("revision + 1"), "processing_expires_at": nil})
			if requestUpdate.Error != nil {
				return requestUpdate.Error
			}
			if requestUpdate.RowsAffected != 1 {
				return joinrequests.ErrConflict
			}
			row, err := loadJoinRequestManagerRow(tx, candidate.Flag)
			if err != nil {
				return err
			}
			request, err := joinRequestFromManagerRow(row)
			if err != nil {
				return err
			}
			recovered = append(recovered, request)
		}
		return nil
	})
	return recovered, err
}

func scheduledTypeToDatabase(value scheduledjobs.JobType) (string, error) {
	switch value {
	case scheduledjobs.TypeDaily:
		return databaseDailyJob, nil
	case scheduledjobs.TypeOnce:
		return databaseOnceJob, nil
	default:
		return "", scheduledjobs.ErrInvalidInput
	}
}

func scheduledTypeFromDatabase(value string) (scheduledjobs.JobType, error) {
	switch value {
	case databaseDailyJob:
		return scheduledjobs.TypeDaily, nil
	case databaseOnceJob:
		return scheduledjobs.TypeOnce, nil
	default:
		return "", errors.New("invalid scheduled job type in storage")
	}
}

func scheduleColumns(value scheduledjobs.Schedule) (string, *time.Time, error) {
	if value.Type == scheduledjobs.TypeDaily {
		return value.LocalTime, nil, nil
	}
	if value.Type != scheduledjobs.TypeOnce || value.RunAt == nil {
		return "", nil, scheduledjobs.ErrInvalidInput
	}
	location, err := time.LoadLocation(value.Timezone)
	if err != nil {
		return "", nil, scheduledjobs.ErrInvalidInput
	}
	local := value.RunAt.In(location)
	runDate := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	return local.Format("15:04"), &runDate, nil
}

func scheduledJobFromManagerRow(row scheduledJobManagerRow) (scheduledjobs.Job, error) {
	jobType, err := scheduledTypeFromDatabase(row.Type)
	if err != nil {
		return scheduledjobs.Job{}, err
	}
	schedule := scheduledjobs.Schedule{Type: jobType, LocalTime: row.TimeHHMM, Timezone: row.Timezone}
	if jobType == scheduledjobs.TypeOnce {
		if row.RunDate == nil {
			return scheduledjobs.Job{}, errors.New("once scheduled job has no run date")
		}
		location, err := time.LoadLocation(row.Timezone)
		if err != nil {
			return scheduledjobs.Job{}, err
		}
		clock, err := time.Parse("15:04", row.TimeHHMM)
		if err != nil {
			return scheduledjobs.Job{}, err
		}
		local := time.Date(row.RunDate.Year(), row.RunDate.Month(), row.RunDate.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
		runAt := local.UTC()
		schedule.RunAt = &runAt
	}
	var lastResult *scheduledjobs.RunResult
	if row.LastRunResult != nil {
		value := scheduledjobs.RunResult(*row.LastRunResult)
		lastResult = &value
	}
	return scheduledjobs.Job{
		ID: strconv.FormatUint(row.ID, 10), Name: row.Name,
		Group: scheduledjobs.Group{ID: strconv.FormatInt(row.GroupID, 10), Name: row.GroupName}, Message: row.Message,
		Type: jobType, Schedule: schedule, Status: scheduledjobs.Status(row.Status), NextRunAt: utcTimePointer(row.RunAt),
		LastRunAt: utcTimePointer(row.LastRunAt), LastRunResult: lastResult, Version: row.Revision,
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), UpdatedBy: updatedByActor(row.ManagerUpdatedByColumns),
	}, nil
}

func selectScheduledJobManager(db *gorm.DB) *gorm.DB {
	return db.Table("scheduled_jobs AS job").Select(`job.*, COALESCE(managed.name, '') AS group_name`).
		Joins("LEFT JOIN managed_groups AS managed ON managed.group_id = job.group_id")
}

func loadScheduledJobManagerRow(db *gorm.DB, id string) (scheduledJobManagerRow, error) {
	var row scheduledJobManagerRow
	err := selectScheduledJobManager(db).Where("job.id = ?", id).Take(&row).Error
	return row, err
}

func scheduledAuditActor(principal auth.Principal) OpsActorColumns {
	return principalDecisionActor(principal)
}

func (s *Store) CreateScheduledJob(ctx context.Context, mutation scheduledjobs.CreateMutation) (scheduledjobs.Job, error) {
	var result scheduledjobs.Job
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		jobType, err := scheduledTypeToDatabase(mutation.Input.Schedule.Type)
		if err != nil {
			return err
		}
		timeHHMM, runDate, err := scheduleColumns(mutation.Input.Schedule)
		if err != nil {
			return err
		}
		groupID, err := strconv.ParseInt(mutation.Input.GroupID, 10, 64)
		if err != nil || groupID <= 0 {
			return scheduledjobs.ErrInvalidInput
		}
		actor := principalUpdatedBy(mutation.Context.Actor)
		status := scheduledjobs.StatusPaused
		if mutation.Input.Enabled {
			status = scheduledjobs.StatusActive
		}
		at := mutation.Context.OccurredAt.UTC()
		row := scheduledJobManagerRow{
			Name: mutation.Input.Name, Type: jobType, TimeHHMM: timeHHMM, RunDate: runDate,
			GroupID: groupID, Message: mutation.Input.Message, Enabled: mutation.Input.Enabled,
			Status: string(status), Timezone: mutation.Input.Schedule.Timezone, RunAt: utcTimePointer(mutation.NextRunAt),
			Revision: 1, ManagerUpdatedByColumns: actor, CreatedAt: at, UpdatedAt: at,
		}
		if err := tx.Omit("GroupName").Create(&row).Error; err != nil {
			return err
		}
		loaded, err := loadScheduledJobManagerRow(tx, strconv.FormatUint(row.ID, 10))
		if err != nil {
			return err
		}
		result, err = scheduledJobFromManagerRow(loaded)
		if err != nil {
			return err
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(mutation.Context.Actor), OccurredAt: at, Request: mutation.Context.Request,
			Action: "scheduled_job.create", TargetType: "scheduled_job", TargetID: result.ID, TargetName: result.Name,
			After:    map[string]any{"name": result.Name, "group_id": result.Group.ID, "status": result.Status, "revision": result.Version},
			Metadata: map[string]any{},
		})
	})
	return result, err
}

func (s *Store) GetScheduledJob(ctx context.Context, id string) (scheduledjobs.Job, bool, error) {
	row, err := loadScheduledJobManagerRow(s.db.WithContext(ctx), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return scheduledjobs.Job{}, false, nil
	}
	if err != nil {
		return scheduledjobs.Job{}, false, err
	}
	if row.ArchivedAt != nil {
		return scheduledjobs.Job{}, false, nil
	}
	value, err := scheduledJobFromManagerRow(row)
	return value, err == nil, err
}

func (s *Store) ListScheduledJobs(ctx context.Context, query scheduledjobs.ListQuery) (scheduledjobs.Page[scheduledjobs.Job], error) {
	db := selectScheduledJobManager(s.db.WithContext(ctx))
	if query.GroupID != "" {
		db = db.Where("job.group_id = ?", query.GroupID)
	}
	if query.Type != "" {
		jobType, err := scheduledTypeToDatabase(query.Type)
		if err != nil {
			return scheduledjobs.Page[scheduledjobs.Job]{}, err
		}
		db = db.Where("job.type = ?", jobType)
	}
	if query.Status != "" {
		db = db.Where("job.status = ?", query.Status)
	} else {
		db = db.Where("job.archived_at IS NULL")
	}
	if query.RunResult != "" {
		db = db.Where("job.last_run_result = ?", query.RunResult)
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerCursor(query.Cursor)
		if err != nil {
			return scheduledjobs.Page[scheduledjobs.Job]{}, scheduledjobs.ErrInvalidInput
		}
		id, err := strconv.ParseUint(cursor.ID, 10, 64)
		if err != nil {
			return scheduledjobs.Page[scheduledjobs.Job]{}, scheduledjobs.ErrInvalidInput
		}
		at := time.UnixMilli(cursor.Millis).UTC()
		db = db.Where("job.updated_at < ? OR (job.updated_at = ? AND job.id < ?)", at, at, id)
	}
	var rows []scheduledJobManagerRow
	if err := db.Order("job.updated_at DESC").Order("job.id DESC").Limit(query.Limit + 1).Scan(&rows).Error; err != nil {
		return scheduledjobs.Page[scheduledjobs.Job]{}, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]scheduledjobs.Job, len(rows))
	for index, row := range rows {
		converted, err := scheduledJobFromManagerRow(row)
		if err != nil {
			return scheduledjobs.Page[scheduledjobs.Job]{}, err
		}
		items[index] = converted
	}
	next := ""
	if hasMore && len(rows) > 0 {
		next = encodeManagerCursor(rows[len(rows)-1].UpdatedAt, strconv.FormatUint(rows[len(rows)-1].ID, 10))
	}
	return scheduledjobs.Page[scheduledjobs.Job]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

func (s *Store) UpdateScheduledJob(ctx context.Context, mutation scheduledjobs.UpdateMutation) (scheduledjobs.Job, error) {
	var result scheduledjobs.Job
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := loadScheduledJobManagerRow(tx.Clauses(clause.Locking{Strength: "UPDATE"}), mutation.JobID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return scheduledjobs.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Revision != mutation.ExpectedRevision || before.ArchivedAt != nil {
			return scheduledjobs.ErrConflict
		}
		actor := principalUpdatedBy(mutation.Context.Actor)
		updates := map[string]any{
			"revision": gorm.Expr("revision + 1"), "updated_at": mutation.Context.OccurredAt.UTC(),
			"updated_by_type": actor.Type, "updated_by_user_id": actor.UserID, "updated_by_qq_user_id": actor.QQUserID,
			"updated_by_display_name": actor.DisplayName, "updated_by_role": actor.Role,
		}
		if mutation.Patch.Name.Set {
			updates["name"] = mutation.Patch.Name.Value
		}
		if mutation.Patch.GroupID.Set {
			updates["group_id"] = mutation.Patch.GroupID.Value
		}
		if mutation.Patch.Message.Set {
			updates["message"] = mutation.Patch.Message.Value
		}
		if mutation.Patch.Schedule.Set {
			jobType, err := scheduledTypeToDatabase(mutation.Patch.Schedule.Value.Type)
			if err != nil {
				return err
			}
			timeHHMM, runDate, err := scheduleColumns(mutation.Patch.Schedule.Value)
			if err != nil {
				return err
			}
			updates["type"], updates["time_hhmm"], updates["run_date"] = jobType, timeHHMM, runDate
			updates["timezone"] = mutation.Patch.Schedule.Value.Timezone
		}
		if mutation.Patch.Status.Set {
			updates["status"] = mutation.Patch.Status.Value
			updates["enabled"] = mutation.Patch.Status.Value == scheduledjobs.StatusActive
		}
		if mutation.NextRunAt.Set {
			updates["run_at"] = utcTimePointer(mutation.NextRunAt.Value)
		}
		updated := tx.Model(&scheduledJobManagerRow{}).Where("id = ? AND revision = ? AND archived_at IS NULL", mutation.JobID, mutation.ExpectedRevision).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return scheduledjobs.ErrConflict
		}
		after, err := loadScheduledJobManagerRow(tx, mutation.JobID)
		if err != nil {
			return err
		}
		result, err = scheduledJobFromManagerRow(after)
		if err != nil {
			return err
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(mutation.Context.Actor), OccurredAt: mutation.Context.OccurredAt,
			Request: mutation.Context.Request, Action: "scheduled_job.update", TargetType: "scheduled_job",
			TargetID: mutation.JobID, TargetName: result.Name,
			Before: map[string]any{"name": before.Name, "status": before.Status, "revision": before.Revision},
			After:  map[string]any{"name": after.Name, "status": after.Status, "revision": after.Revision}, Metadata: map[string]any{},
		})
	})
	return result, err
}

func (s *Store) DeleteScheduledJob(ctx context.Context, mutation scheduledjobs.DeleteMutation) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		before, err := loadScheduledJobManagerRow(tx.Clauses(clause.Locking{Strength: "UPDATE"}), mutation.JobID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return scheduledjobs.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Revision != mutation.ExpectedRevision {
			return scheduledjobs.ErrConflict
		}
		var runningCount int64
		if err := tx.Model(&scheduledRunManagerRow{}).
			Where("job_id = ? AND completed_at IS NULL", mutation.JobID).
			Count(&runningCount).Error; err != nil {
			return err
		}
		if runningCount != 0 {
			return scheduledjobs.ErrConflict
		}
		at := mutation.Context.OccurredAt.UTC()
		deletedRuns := tx.Where("job_id = ?", mutation.JobID).Delete(&scheduledRunManagerRow{})
		if deletedRuns.Error != nil {
			return deletedRuns.Error
		}
		deleted := tx.Where("id = ? AND revision = ?", mutation.JobID, mutation.ExpectedRevision).Delete(&scheduledJobManagerRow{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return scheduledjobs.ErrConflict
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(mutation.Context.Actor), OccurredAt: at, Request: mutation.Context.Request,
			Action: "scheduled_job.delete", TargetType: "scheduled_job", TargetID: mutation.JobID, TargetName: before.Name,
			Before: map[string]any{"status": before.Status, "revision": before.Revision},
			After:  map[string]any{"deleted": true}, Metadata: map[string]any{"deleted_run_count": deletedRuns.RowsAffected},
		})
	})
}

func scheduledRunFromManagerRow(row scheduledRunManagerRow) scheduledjobs.Run {
	var actor *audit.Actor
	if row.TriggeredByType != nil {
		display := ""
		if row.TriggeredByDisplayName != nil {
			display = *row.TriggeredByDisplayName
		}
		actor = &audit.Actor{
			Type: audit.ActorType(*row.TriggeredByType), UserID: row.TriggeredByUserID,
			QQUserID: row.TriggeredByQQUserID, DisplayName: display,
		}
	}
	return scheduledjobs.Run{
		ID: row.RunID, JobID: strconv.FormatUint(row.JobID, 10), Kind: scheduledjobs.RunKind(row.Kind),
		Result: scheduledjobs.RunResult(row.Result), ScheduledFor: utcTimePointer(row.ScheduledFor),
		StartedAt: row.StartedAt.UTC(), CompletedAt: utcTimePointer(row.CompletedAt),
		Duration: time.Duration(row.DurationMS) * time.Millisecond, MessageID: row.MessageID,
		ErrorCode: row.ErrorCode, ErrorMessage: row.ErrorMessage, TriggeredBy: actor,
	}
}

func (s *Store) ListScheduledJobRuns(ctx context.Context, query scheduledjobs.RunListQuery) (scheduledjobs.Page[scheduledjobs.Run], error) {
	db := s.db.WithContext(ctx).Model(&scheduledRunManagerRow{}).Where("job_id = ?", query.JobID)
	if query.Kind != "" {
		db = db.Where("kind = ?", query.Kind)
	}
	if query.Result != "" {
		db = db.Where("result = ?", query.Result)
	}
	if query.From != nil {
		db = db.Where("started_at >= ?", query.From.UTC())
	}
	if query.To != nil {
		db = db.Where("started_at < ?", query.To.UTC())
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerCursor(query.Cursor)
		if err != nil {
			return scheduledjobs.Page[scheduledjobs.Run]{}, scheduledjobs.ErrInvalidInput
		}
		at := time.UnixMilli(cursor.Millis).UTC()
		db = db.Where("started_at < ? OR (started_at = ? AND run_id < ?)", at, at, cursor.ID)
	}
	var rows []scheduledRunManagerRow
	if err := db.Order("started_at DESC").Order("run_id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return scheduledjobs.Page[scheduledjobs.Run]{}, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]scheduledjobs.Run, len(rows))
	for index, row := range rows {
		items[index] = scheduledRunFromManagerRow(row)
	}
	next := ""
	if hasMore && len(rows) > 0 {
		next = encodeManagerCursor(rows[len(rows)-1].StartedAt, rows[len(rows)-1].RunID)
	}
	return scheduledjobs.Page[scheduledjobs.Run]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

func scheduledTestIdentity(jobID, key string) string {
	digest := sha256.Sum256([]byte(jobID + "\x00" + key))
	return "test-" + hex.EncodeToString(digest[:])
}

func (s *Store) BeginScheduledJobTestSend(ctx context.Context, begin scheduledjobs.TestSendBegin) (scheduledjobs.TestSendReservation, error) {
	var reservation scheduledjobs.TestSendReservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		identity := scheduledTestIdentity(begin.JobID, begin.IdempotencyKey)
		var existing scheduledRunManagerRow
		err := tx.Where("run_identity = ?", identity).Take(&existing).Error
		if err == nil {
			if existing.CompletedAt == nil {
				return scheduledjobs.ErrConflict
			}
			requestID := ""
			if existing.RequestID != nil {
				requestID = *existing.RequestID
			}
			if requestID != begin.Context.Request.RequestID || existing.TriggeredByUserID == nil || *existing.TriggeredByUserID != begin.Context.Actor.UserID {
				return scheduledjobs.ErrIdempotencyConflict
			}
			job, err := loadScheduledJobManagerRow(tx, begin.JobID)
			if err != nil {
				return err
			}
			converted, err := scheduledJobFromManagerRow(job)
			if err != nil {
				return err
			}
			reservation = scheduledjobs.TestSendReservation{ExecutionID: identity, Job: converted, Run: scheduledRunFromManagerRow(existing), Fresh: false}
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		jobRow, err := loadScheduledJobManagerRow(tx.Clauses(clause.Locking{Strength: "UPDATE"}), begin.JobID)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return scheduledjobs.ErrNotFound
		}
		if err != nil {
			return err
		}
		if jobRow.Revision != begin.ExpectedRevision || jobRow.ArchivedAt != nil {
			return scheduledjobs.ErrConflict
		}
		runID, err := opsNewID("run")
		if err != nil {
			return err
		}
		actorType := string(audit.ActorAdminUser)
		actorDisplay := begin.Context.Actor.UserID
		requestID := begin.Context.Request.RequestID
		run := scheduledRunManagerRow{
			RunID: runID, RunIdentity: identity, JobID: jobRow.ID, Kind: string(scheduledjobs.RunTest),
			Result: string(scheduledjobs.RunUnknown), StartedAt: begin.Context.OccurredAt.UTC(),
			TriggeredByType: &actorType, TriggeredByUserID: opsOptionalString(begin.Context.Actor.UserID),
			TriggeredByDisplayName: &actorDisplay, RequestID: &requestID,
		}
		if err := tx.Create(&run).Error; err != nil {
			if opsIsDuplicateKey(err) {
				return scheduledjobs.ErrIdempotencyConflict
			}
			return err
		}
		job, err := scheduledJobFromManagerRow(jobRow)
		if err != nil {
			return err
		}
		reservation = scheduledjobs.TestSendReservation{ExecutionID: identity, Job: job, Run: scheduledRunFromManagerRow(run), Fresh: true}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(begin.Context.Actor), OccurredAt: begin.Context.OccurredAt,
			Request: begin.Context.Request, Action: "scheduled_job.test_send", TargetType: "scheduled_job",
			TargetID: begin.JobID, TargetName: job.Name, After: map[string]any{"run_id": runID, "result": run.Result},
			Metadata: map[string]any{},
		})
	})
	return reservation, err
}

func (s *Store) CompleteScheduledJobTestSend(ctx context.Context, completion scheduledjobs.TestSendCompletion) (scheduledjobs.Run, error) {
	var result scheduledjobs.Run
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row scheduledRunManagerRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ? AND run_identity = ?", completion.RunID, completion.ExecutionID).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return scheduledjobs.ErrNotFound
		}
		if err != nil {
			return err
		}
		if row.CompletedAt != nil {
			if row.Result != string(completion.Result) || row.DurationMS != uint64(max(completion.Duration.Milliseconds(), 0)) ||
				!sameManagerString(row.MessageID, opsOptionalString(completion.MessageID)) ||
				!sameManagerString(row.ErrorCode, opsOptionalString(completion.ErrorCode)) ||
				!sameManagerString(row.ErrorMessage, opsOptionalString(completion.ErrorMessage)) {
				return scheduledjobs.ErrConflict
			}
			result = scheduledRunFromManagerRow(row)
			return nil
		}
		completedAt := completion.CompletedAt.UTC()
		updates := map[string]any{
			"result": completion.Result, "completed_at": completedAt,
			"duration_ms": uint64(max(completion.Duration.Milliseconds(), 0)),
			"message_id":  opsOptionalString(completion.MessageID), "error_code": opsOptionalString(completion.ErrorCode),
			"error_message": opsOptionalString(completion.ErrorMessage),
		}
		updated := tx.Model(&scheduledRunManagerRow{}).Where("run_id = ? AND completed_at IS NULL", completion.RunID).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return scheduledjobs.ErrConflict
		}
		if err := tx.Where("run_id = ?", completion.RunID).Take(&row).Error; err != nil {
			return err
		}
		result = scheduledRunFromManagerRow(row)
		return nil
	})
	return result, err
}

func (s *Store) BeginScheduledJobRun(
	ctx context.Context,
	jobID uint64,
	occurrenceID string,
	scheduledFor time.Time,
	startedAt time.Time,
) (scheduler.RunReservation, error) {
	if jobID == 0 || !strings.HasPrefix(occurrenceID, "scheduled-") || len(occurrenceID) > 110 {
		return scheduler.RunReservation{}, fmt.Errorf("invalid scheduled run identity")
	}
	var reservation scheduler.RunReservation
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var job scheduledJobManagerRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND enabled = ? AND archived_at IS NULL", jobID, true).Take(&job).Error; err != nil {
			return err
		}
		var prior []scheduledRunManagerRow
		if err := tx.Where("job_id = ? AND kind = ? AND run_identity LIKE ?", jobID, scheduledjobs.RunScheduled, occurrenceID+"-%").
			Order("started_at DESC, run_id DESC").Find(&prior).Error; err != nil {
			return err
		}
		if len(prior) > 0 {
			latest := prior[0]
			if latest.CompletedAt == nil {
				completedAt := startedAt.UTC()
				if err := tx.Model(&scheduledRunManagerRow{}).Where("run_id = ? AND completed_at IS NULL", latest.RunID).Updates(map[string]any{
					"result": scheduledjobs.RunUnknown, "completed_at": completedAt,
					"duration_ms": gorm.Expr("GREATEST(TIMESTAMPDIFF(MICROSECOND, started_at, ?) DIV 1000, 0)", completedAt),
					"error_code":  "process_interrupted",
				}).Error; err != nil {
					return err
				}
				latest.Result = string(scheduledjobs.RunUnknown)
				latest.CompletedAt = &completedAt
			}
			result := scheduler.RunResult(latest.Result)
			if result != scheduler.RunFailed {
				reservation = scheduler.RunReservation{RunID: latest.RunID, Result: result, Fresh: false}
				return nil
			}
		}
		runID, err := opsNewID("run")
		if err != nil {
			return err
		}
		scheduledAt := scheduledFor.UTC()
		run := scheduledRunManagerRow{
			RunID: runID, RunIdentity: fmt.Sprintf("%s-%d", occurrenceID, len(prior)+1), JobID: jobID,
			Kind: string(scheduledjobs.RunScheduled), Result: string(scheduler.RunUnknown),
			ScheduledFor: &scheduledAt, StartedAt: startedAt.UTC(),
		}
		if err := tx.Create(&run).Error; err != nil {
			return err
		}
		reservation = scheduler.RunReservation{RunID: runID, Result: scheduler.RunUnknown, Fresh: true}
		return nil
	})
	return reservation, err
}

func (s *Store) CompleteScheduledJobRun(ctx context.Context, completion scheduler.RunCompletion) error {
	if completion.RunID == "" || completion.CompletedAt.IsZero() || completion.Duration < 0 ||
		(completion.Result != scheduler.RunSuccess && completion.Result != scheduler.RunFailed && completion.Result != scheduler.RunUnknown) {
		return fmt.Errorf("invalid scheduled run completion")
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row scheduledRunManagerRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("run_id = ? AND kind = ?", completion.RunID, scheduledjobs.RunScheduled).Take(&row).Error; err != nil {
			return err
		}
		durationMS := uint64(max(completion.Duration.Milliseconds(), 0))
		errorCode := opsOptionalString(completion.ErrorCode)
		if row.CompletedAt != nil {
			if row.Result != string(completion.Result) || row.DurationMS != durationMS || !sameManagerString(row.ErrorCode, errorCode) {
				return fmt.Errorf("scheduled run completion conflict")
			}
			return nil
		}
		result := tx.Model(&scheduledRunManagerRow{}).Where("run_id = ? AND completed_at IS NULL", completion.RunID).Updates(map[string]any{
			"result": completion.Result, "completed_at": completion.CompletedAt.UTC(),
			"duration_ms": durationMS, "error_code": errorCode, "error_message": nil,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("scheduled run completion conflict")
		}
		return nil
	})
}

func (s *Store) RecoverInterruptedScheduledJobRuns(ctx context.Context, recoveredAt time.Time) (int, error) {
	result := s.db.WithContext(ctx).Model(&scheduledRunManagerRow{}).Where("completed_at IS NULL").Updates(map[string]any{
		"result": scheduledjobs.RunUnknown, "completed_at": recoveredAt.UTC(), "error_code": "process_interrupted",
		"duration_ms": gorm.Expr("GREATEST(TIMESTAMPDIFF(MICROSECOND, started_at, ?) DIV 1000, 0)", recoveredAt.UTC()),
	})
	return int(result.RowsAffected), result.Error
}

type commandScopePayload struct {
	Type     customcommand.ScopeType `json:"type"`
	GroupIDs []string                `json:"group_ids"`
}

type commandFixedOptionPayload struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type commandParameterPayload struct {
	Name           string                      `json:"name"`
	DisplayName    string                      `json:"display_name"`
	Type           customcommand.ParameterType `json:"type"`
	Required       bool                        `json:"required"`
	MinLength      int                         `json:"min_length,omitempty"`
	MaxLength      int                         `json:"max_length,omitempty"`
	Minimum        int64                       `json:"minimum,omitempty"`
	Maximum        int64                       `json:"maximum,omitempty"`
	MinimumSeconds int64                       `json:"minimum_seconds,omitempty"`
	MaximumSeconds int64                       `json:"maximum_seconds,omitempty"`
	AllowTriggerer bool                        `json:"allow_triggerer,omitempty"`
	Options        []commandFixedOptionPayload `json:"options,omitempty"`
}

type commandDurationPayload struct {
	Type      customcommand.DurationSourceType `json:"type,omitempty"`
	Seconds   int64                            `json:"seconds,omitempty"`
	Parameter string                           `json:"parameter,omitempty"`
}

type commandActionPayload struct {
	Type            customcommand.ActionType    `json:"type"`
	Template        string                      `json:"template,omitempty"`
	Target          customcommand.MentionTarget `json:"target,omitempty"`
	MemberParameter string                      `json:"member_parameter,omitempty"`
	Duration        commandDurationPayload      `json:"duration,omitempty"`
	TargetGroupIDs  []string                    `json:"target_group_ids,omitempty"`
}

type commandRunPayload struct {
	Version           int                             `json:"version"`
	ArgumentSummaries []customcommand.ArgumentSummary `json:"argument_summaries"`
	ActionSteps       []customcommand.ActionStep      `json:"action_steps"`
}

func sameCommandRunPayload(left, right []byte) bool {
	var leftPayload, rightPayload commandRunPayload
	if opsUnmarshalJSON(left, &leftPayload) != nil || opsUnmarshalJSON(right, &rightPayload) != nil {
		return false
	}
	return reflect.DeepEqual(leftPayload, rightPayload)
}

func commandJSON(definition customcommand.Definition) ([]byte, []byte, []byte, error) {
	scope, err := opsMarshalJSON(commandScopePayload{Type: definition.Scope.Type, GroupIDs: append([]string(nil), definition.Scope.GroupIDs...)})
	if err != nil {
		return nil, nil, nil, err
	}
	parameters := make([]commandParameterPayload, len(definition.Parameters))
	for index, parameter := range definition.Parameters {
		options := make([]commandFixedOptionPayload, len(parameter.Options))
		for optionIndex, option := range parameter.Options {
			options[optionIndex] = commandFixedOptionPayload{Value: option.Value, Label: option.Label}
		}
		parameters[index] = commandParameterPayload{
			Name: parameter.Name, DisplayName: parameter.DisplayName, Type: parameter.Type, Required: parameter.Required,
			MinLength: parameter.MinLength, MaxLength: parameter.MaxLength, Minimum: parameter.Minimum, Maximum: parameter.Maximum,
			MinimumSeconds: parameter.MinimumSeconds, MaximumSeconds: parameter.MaximumSeconds,
			AllowTriggerer: parameter.AllowTriggerer, Options: options,
		}
	}
	parametersJSON, err := opsMarshalJSON(parameters)
	if err != nil {
		return nil, nil, nil, err
	}
	actions := make([]commandActionPayload, len(definition.Actions))
	for index, action := range definition.Actions {
		actions[index] = commandActionPayload{
			Type: action.Type, Template: action.Template, Target: action.Target, MemberParameter: action.MemberParameter,
			Duration:       commandDurationPayload{Type: action.Duration.Type, Seconds: action.Duration.Seconds, Parameter: action.Duration.Parameter},
			TargetGroupIDs: append([]string(nil), action.TargetGroupIDs...),
		}
	}
	actionsJSON, err := opsMarshalJSON(actions)
	return scope, parametersJSON, actionsJSON, err
}

func commandDefinitionFromManagerRow(row customCommandManagerRow) (customcommand.Definition, error) {
	var scope commandScopePayload
	if err := opsUnmarshalJSON(row.ScopeJSON, &scope); err != nil {
		return customcommand.Definition{}, err
	}
	if scope.Type == "" {
		scope.Type = customcommand.ScopeType(row.ScopeType)
	}
	var parametersPayload []commandParameterPayload
	if err := opsUnmarshalJSON(row.ParametersJSON, &parametersPayload); err != nil {
		return customcommand.Definition{}, err
	}
	parameters := make([]customcommand.Parameter, len(parametersPayload))
	for index, parameter := range parametersPayload {
		var options []customcommand.FixedOption
		if len(parameter.Options) > 0 {
			options = make([]customcommand.FixedOption, len(parameter.Options))
		}
		for optionIndex, option := range parameter.Options {
			options[optionIndex] = customcommand.FixedOption{Value: option.Value, Label: option.Label}
		}
		parameters[index] = customcommand.Parameter{
			Name: parameter.Name, DisplayName: parameter.DisplayName, Type: parameter.Type, Required: parameter.Required,
			MinLength: parameter.MinLength, MaxLength: parameter.MaxLength, Minimum: parameter.Minimum, Maximum: parameter.Maximum,
			MinimumSeconds: parameter.MinimumSeconds, MaximumSeconds: parameter.MaximumSeconds,
			AllowTriggerer: parameter.AllowTriggerer, Options: options,
		}
	}
	var actionsPayload []commandActionPayload
	if err := opsUnmarshalJSON(row.ActionsJSON, &actionsPayload); err != nil {
		return customcommand.Definition{}, err
	}
	actions := make([]customcommand.Action, len(actionsPayload))
	for index, action := range actionsPayload {
		actions[index] = customcommand.Action{
			Type: action.Type, Template: action.Template, Target: action.Target, MemberParameter: action.MemberParameter,
			Duration:       customcommand.DurationSource{Type: action.Duration.Type, Seconds: action.Duration.Seconds, Parameter: action.Duration.Parameter},
			TargetGroupIDs: append([]string(nil), action.TargetGroupIDs...),
		}
	}
	return customcommand.Definition{
		Name: row.Name, DisplayName: row.DisplayName, Description: row.Description,
		Scope:             customcommand.Scope{Type: scope.Type, GroupIDs: append([]string(nil), scope.GroupIDs...)},
		TriggerPermission: customcommand.TriggerPermission(row.TriggerPermission), Parameters: parameters, Actions: actions,
	}, nil
}

func customCommandFromManagerRow(row customCommandManagerRow) (customcommand.Command, error) {
	definition, err := commandDefinitionFromManagerRow(row)
	if err != nil {
		return customcommand.Command{}, err
	}
	return customcommand.Command{
		ID: row.CommandID, Definition: definition, Enabled: row.Enabled, Status: customcommand.Status(row.Status),
		Version: row.Revision, CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(),
		UpdatedBy: updatedByActor(row.ManagerUpdatedByColumns),
	}, nil
}

func customRunFromManagerRow(row customRunManagerRow) (customcommand.Run, error) {
	var payload commandRunPayload
	if err := opsUnmarshalJSON(row.ActionSteps, &payload); err != nil {
		var legacy []customcommand.ActionStep
		if legacyErr := opsUnmarshalJSON(row.ActionSteps, &legacy); legacyErr != nil {
			return customcommand.Run{}, err
		}
		payload.ActionSteps = legacy
	}
	requestID := ""
	if row.RequestID != nil {
		requestID = *row.RequestID
	}
	return customcommand.Run{
		ID: row.RunID, RunIdentity: row.RunIdentity, CommandID: row.CommandID, CommandName: row.CommandName,
		GroupID: strconv.FormatInt(row.GroupID, 10), TriggeredByQQ: row.TriggeredByQQ,
		Result: customcommand.RunResult(row.Result), ArgumentSummaries: payload.ArgumentSummaries,
		ActionSteps: payload.ActionSteps, Duration: time.Duration(row.DurationMS) * time.Millisecond,
		ErrorCode: row.ErrorCode, RequestID: requestID, OccurredAt: row.OccurredAt.UTC(),
	}, nil
}

func (s *Store) CommandNameExists(ctx context.Context, name, exceptID string) (bool, error) {
	db := s.db.WithContext(ctx).Model(&customCommandManagerRow{}).Where("name = ? AND archived_at IS NULL", name)
	if exceptID != "" {
		db = db.Where("command_id <> ?", exceptID)
	}
	var count int64
	err := db.Count(&count).Error
	return count > 0, err
}

func (s *Store) CreateCommand(ctx context.Context, mutation customcommand.CreateMutation) (customcommand.Command, error) {
	var result customcommand.Command
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		scopeJSON, parametersJSON, actionsJSON, err := commandJSON(mutation.Definition)
		if err != nil {
			return err
		}
		id, err := opsNewID("cmd")
		if err != nil {
			return err
		}
		actor := principalUpdatedBy(mutation.Context.Actor)
		at := mutation.Context.OccurredAt.UTC()
		row := customCommandManagerRow{
			CommandID: id, Name: mutation.Definition.Name, DisplayName: mutation.Definition.DisplayName,
			Description: mutation.Definition.Description, ScopeType: string(mutation.Definition.Scope.Type), ScopeJSON: scopeJSON,
			TriggerPermission: string(mutation.Definition.TriggerPermission), ParametersJSON: parametersJSON, ActionsJSON: actionsJSON,
			Enabled: mutation.Enabled, Status: string(mutation.Status), Revision: 1,
			ManagerUpdatedByColumns: actor, CreatedAt: at, UpdatedAt: at,
		}
		if err := tx.Create(&row).Error; err != nil {
			if opsIsDuplicateKey(err) {
				return customcommand.ErrConflict
			}
			return err
		}
		result, err = customCommandFromManagerRow(row)
		if err != nil {
			return err
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(mutation.Context.Actor), OccurredAt: at, Request: mutation.Context.Request,
			Action: "custom_command.create", TargetType: "custom_command", TargetID: id, TargetName: row.DisplayName,
			After: commandAuditSnapshot(row), Metadata: map[string]any{},
		})
	})
	return result, err
}

func commandAuditSnapshot(row customCommandManagerRow) map[string]any {
	return map[string]any{
		"name": row.Name, "display_name": row.DisplayName, "scope_type": row.ScopeType,
		"trigger_permission": row.TriggerPermission, "enabled": row.Enabled, "status": row.Status, "revision": row.Revision,
	}
}

func (s *Store) GetCommand(ctx context.Context, id string) (customcommand.Command, bool, error) {
	var row customCommandManagerRow
	err := s.db.WithContext(ctx).Where("command_id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return customcommand.Command{}, false, nil
	}
	if err != nil {
		return customcommand.Command{}, false, err
	}
	if row.ArchivedAt != nil {
		return customcommand.Command{}, false, nil
	}
	value, err := customCommandFromManagerRow(row)
	return value, err == nil, err
}

func (s *Store) ListCommands(ctx context.Context, query customcommand.ListQuery) (customcommand.Page[customcommand.Command], error) {
	db := s.db.WithContext(ctx).Model(&customCommandManagerRow{})
	if query.Query != "" {
		pattern := "%" + escapeManagerLike(query.Query) + "%"
		db = db.Where("name LIKE ? ESCAPE '\\\\' OR display_name LIKE ? ESCAPE '\\\\' OR description LIKE ? ESCAPE '\\\\'", pattern, pattern, pattern)
	}
	if query.Enabled != nil {
		db = db.Where("enabled = ?", *query.Enabled)
	}
	if query.Status != "" {
		db = db.Where("status = ?", query.Status)
	} else {
		db = db.Where("archived_at IS NULL")
	}
	if query.ScopeType != "" {
		db = db.Where("scope_type = ?", query.ScopeType)
	}
	if query.GroupID != "" {
		db = db.Where("scope_type = ? OR JSON_CONTAINS(scope_json, JSON_QUOTE(?), '$.group_ids')", customcommand.ScopeGlobal, query.GroupID)
	}
	if query.ActionType != "" {
		db = db.Where("JSON_CONTAINS(actions_json, JSON_OBJECT('type', ?))", query.ActionType)
	}
	if query.TriggerPermission != "" {
		db = db.Where("trigger_permission = ?", query.TriggerPermission)
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerCursor(query.Cursor)
		if err != nil {
			return customcommand.Page[customcommand.Command]{}, customcommand.ErrInvalidInput
		}
		at := time.UnixMilli(cursor.Millis).UTC()
		db = db.Where("updated_at < ? OR (updated_at = ? AND command_id < ?)", at, at, cursor.ID)
	}
	var rows []customCommandManagerRow
	if err := db.Order("updated_at DESC").Order("command_id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return customcommand.Page[customcommand.Command]{}, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]customcommand.Command, len(rows))
	for index, row := range rows {
		converted, err := customCommandFromManagerRow(row)
		if err != nil {
			return customcommand.Page[customcommand.Command]{}, err
		}
		items[index] = converted
	}
	next := ""
	if hasMore && len(rows) > 0 {
		next = encodeManagerCursor(rows[len(rows)-1].UpdatedAt, rows[len(rows)-1].CommandID)
	}
	return customcommand.Page[customcommand.Command]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

func (s *Store) UpdateCommand(ctx context.Context, mutation customcommand.UpdateMutation) (customcommand.Command, error) {
	var result customcommand.Command
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before customCommandManagerRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("command_id = ?", mutation.CommandID).Take(&before).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return customcommand.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Revision != mutation.ExpectedRevision || before.ArchivedAt != nil {
			return customcommand.ErrConflict
		}
		definition, err := commandDefinitionFromManagerRow(before)
		if err != nil {
			return err
		}
		if mutation.Patch.Name.Set {
			definition.Name = mutation.Patch.Name.Value
		}
		if mutation.Patch.DisplayName.Set {
			definition.DisplayName = mutation.Patch.DisplayName.Value
		}
		if mutation.Patch.Description.Set {
			definition.Description = mutation.Patch.Description.Value
		}
		if mutation.Patch.Scope.Set {
			definition.Scope = mutation.Patch.Scope.Value
		}
		if mutation.Patch.TriggerPermission.Set {
			definition.TriggerPermission = mutation.Patch.TriggerPermission.Value
		}
		if mutation.Patch.Parameters.Set {
			definition.Parameters = mutation.Patch.Parameters.Value
		}
		if mutation.Patch.Actions.Set {
			definition.Actions = mutation.Patch.Actions.Value
		}
		enabled, status := before.Enabled, customcommand.Status(before.Status)
		if mutation.Patch.Enabled.Set {
			enabled = mutation.Patch.Enabled.Value
			if enabled {
				status = customcommand.StatusActive
			} else if status != customcommand.StatusDraft {
				status = customcommand.StatusDisabled
			}
		}
		scopeJSON, parametersJSON, actionsJSON, err := commandJSON(definition)
		if err != nil {
			return err
		}
		actor := principalUpdatedBy(mutation.Context.Actor)
		updates := map[string]any{
			"name": definition.Name, "display_name": definition.DisplayName, "description": definition.Description,
			"scope_type": definition.Scope.Type, "scope_json": scopeJSON, "trigger_permission": definition.TriggerPermission,
			"parameters_json": parametersJSON, "actions_json": actionsJSON, "enabled": enabled, "status": status,
			"revision": gorm.Expr("revision + 1"), "updated_at": mutation.Context.OccurredAt.UTC(),
			"updated_by_type": actor.Type, "updated_by_user_id": actor.UserID, "updated_by_qq_user_id": actor.QQUserID,
			"updated_by_display_name": actor.DisplayName, "updated_by_role": actor.Role,
		}
		updated := tx.Model(&customCommandManagerRow{}).Where("command_id = ? AND revision = ? AND archived_at IS NULL", mutation.CommandID, mutation.ExpectedRevision).Updates(updates)
		if updated.Error != nil {
			if opsIsDuplicateKey(updated.Error) {
				return customcommand.ErrConflict
			}
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return customcommand.ErrConflict
		}
		var after customCommandManagerRow
		if err := tx.Where("command_id = ?", mutation.CommandID).Take(&after).Error; err != nil {
			return err
		}
		result, err = customCommandFromManagerRow(after)
		if err != nil {
			return err
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(mutation.Context.Actor), OccurredAt: mutation.Context.OccurredAt,
			Request: mutation.Context.Request, Action: "custom_command.update", TargetType: "custom_command",
			TargetID: mutation.CommandID, TargetName: after.DisplayName, Before: commandAuditSnapshot(before),
			After: commandAuditSnapshot(after), Metadata: map[string]any{},
		})
	})
	return result, err
}

func (s *Store) DeleteCommand(ctx context.Context, mutation customcommand.DeleteMutation) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before customCommandManagerRow
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("command_id = ?", mutation.CommandID).Take(&before).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return customcommand.ErrNotFound
		}
		if err != nil {
			return err
		}
		if before.Revision != mutation.ExpectedRevision {
			return customcommand.ErrConflict
		}
		at := mutation.Context.OccurredAt.UTC()
		deletedRuns := tx.Where("command_id = ?", mutation.CommandID).Delete(&customRunManagerRow{})
		if deletedRuns.Error != nil {
			return deletedRuns.Error
		}
		deleted := tx.Where("command_id = ? AND revision = ?", mutation.CommandID, mutation.ExpectedRevision).Delete(&customCommandManagerRow{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return customcommand.ErrConflict
		}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: scheduledAuditActor(mutation.Context.Actor), OccurredAt: at, Request: mutation.Context.Request,
			Action: "custom_command.delete", TargetType: "custom_command", TargetID: mutation.CommandID,
			TargetName: before.DisplayName, Before: commandAuditSnapshot(before), After: map[string]any{"deleted": true},
			Metadata: map[string]any{"deleted_run_count": deletedRuns.RowsAffected},
		})
	})
}

func (s *Store) ListCommandRuns(ctx context.Context, query customcommand.RunListQuery) (customcommand.Page[customcommand.Run], error) {
	db := s.db.WithContext(ctx).Model(&customRunManagerRow{}).Where("command_id = ?", query.CommandID)
	if query.Result != "" {
		db = db.Where("result = ?", query.Result)
	}
	if query.From != nil {
		db = db.Where("occurred_at >= ?", query.From.UTC())
	}
	if query.To != nil {
		db = db.Where("occurred_at < ?", query.To.UTC())
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerCursor(query.Cursor)
		if err != nil {
			return customcommand.Page[customcommand.Run]{}, customcommand.ErrInvalidInput
		}
		at := time.UnixMilli(cursor.Millis).UTC()
		db = db.Where("occurred_at < ? OR (occurred_at = ? AND run_id < ?)", at, at, cursor.ID)
	}
	var rows []customRunManagerRow
	if err := db.Order("occurred_at DESC").Order("run_id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return customcommand.Page[customcommand.Run]{}, err
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	items := make([]customcommand.Run, len(rows))
	for index, row := range rows {
		converted, err := customRunFromManagerRow(row)
		if err != nil {
			return customcommand.Page[customcommand.Run]{}, err
		}
		items[index] = converted
	}
	next := ""
	if hasMore && len(rows) > 0 {
		next = encodeManagerCursor(rows[len(rows)-1].OccurredAt, rows[len(rows)-1].RunID)
	}
	return customcommand.Page[customcommand.Run]{Items: items, NextCursor: next, HasMore: hasMore}, nil
}

func (s *Store) RecordCommandRun(ctx context.Context, run customcommand.Run) (customcommand.Run, error) {
	payload, err := opsMarshalJSON(commandRunPayload{
		Version: 1, ArgumentSummaries: append([]customcommand.ArgumentSummary(nil), run.ArgumentSummaries...),
		ActionSteps: append([]customcommand.ActionStep(nil), run.ActionSteps...),
	})
	if err != nil {
		return customcommand.Run{}, err
	}
	if run.ID == "" {
		run.ID, err = opsNewID("crun")
		if err != nil {
			return customcommand.Run{}, err
		}
	}
	if run.RunIdentity == "" {
		run.RunIdentity = run.ID
	}
	groupID, err := strconv.ParseInt(run.GroupID, 10, 64)
	if err != nil || groupID <= 0 {
		return customcommand.Run{}, customcommand.ErrInvalidInput
	}
	row := customRunManagerRow{
		RunID: run.ID, RunIdentity: run.RunIdentity, CommandID: run.CommandID, CommandName: run.CommandName,
		GroupID: groupID, TriggeredByQQ: run.TriggeredByQQ, Result: string(run.Result), ActionSteps: payload,
		DurationMS: uint64(max(run.Duration.Milliseconds(), 0)), ErrorCode: run.ErrorCode,
		RequestID: opsOptionalString(run.RequestID), OccurredAt: run.OccurredAt.UTC(),
	}
	err = s.db.WithContext(ctx).Create(&row).Error
	if err == nil {
		return customRunFromManagerRow(row)
	}
	if !opsIsDuplicateKey(err) {
		return customcommand.Run{}, err
	}
	var existing customRunManagerRow
	if loadErr := s.db.WithContext(ctx).Where("run_identity = ?", run.RunIdentity).Take(&existing).Error; loadErr != nil {
		return customcommand.Run{}, loadErr
	}
	if existing.CommandID != run.CommandID || existing.CommandName != run.CommandName || existing.GroupID != groupID ||
		existing.TriggeredByQQ != run.TriggeredByQQ || existing.Result != string(run.Result) ||
		existing.DurationMS != uint64(max(run.Duration.Milliseconds(), 0)) || !sameManagerString(existing.ErrorCode, run.ErrorCode) ||
		!sameManagerString(existing.RequestID, opsOptionalString(run.RequestID)) || !sameCommandRunPayload(existing.ActionSteps, payload) {
		return customcommand.Run{}, customcommand.ErrConflict
	}
	return customRunFromManagerRow(existing)
}

type telemetryMetadataPayload struct {
	Count        int64  `json:"count,omitempty"`
	KnowledgeKey string `json:"knowledge_key,omitempty"`
}

func telemetryOutcome(value telemetry.Result) string {
	switch value {
	case telemetry.ResultSuccess:
		return string(analytics.ResultSuccess)
	case telemetry.ResultDenied:
		return string(analytics.ResultDenied)
	case telemetry.ResultFallback, telemetry.ResultNoKnowledge:
		return string(analytics.ResultFallback)
	case telemetry.ResultDisabled:
		return string(analytics.ResultSkipped)
	case telemetry.ResultUnknown, telemetry.ResultTimeout, telemetry.ResultBusy, telemetry.ResultPartial:
		return string(analytics.ResultUnknown)
	default:
		return string(analytics.ResultFailed)
	}
}

func (s *Store) AppendTelemetryEvents(ctx context.Context, events []telemetry.Event) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([]telemetryEventManagerRow, len(events))
	for index, event := range events {
		groupID, err := strconv.ParseInt(event.GroupID, 10, 64)
		if err != nil || groupID <= 0 {
			return telemetry.ErrFlushFailed
		}
		metadata, err := opsMarshalJSON(telemetryMetadataPayload{Count: event.Count, KnowledgeKey: event.KnowledgeKey})
		if err != nil {
			return err
		}
		duration := uint64(max(event.DurationMS, 0))
		row := telemetryEventManagerRow{
			EventType: string(event.Kind), GroupID: &groupID, FeatureKey: opsOptionalString(event.FeatureKey),
			ActorHash: opsOptionalString(event.UserKey), Outcome: opsOptionalString(telemetryOutcome(event.Result)),
			DurationMS: &duration, Metadata: metadata, OccurredAt: event.OccurredAt.UTC(),
		}
		if event.Kind == telemetry.EventScheduledJobRun && event.JobID != "" {
			jobID, parseErr := strconv.ParseUint(event.JobID, 10, 64)
			if parseErr != nil {
				return telemetry.ErrFlushFailed
			}
			row.JobID = &jobID
		} else {
			row.CommandID = opsOptionalString(event.CommandID)
		}
		rows[index] = row
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.CreateInBatches(rows, 500).Error
	})
}

type dailyAccumulator struct {
	row   telemetryDailyManagerRow
	users map[string]struct{}
}

func telemetryMetricForEvent(row telemetryEventManagerRow) []analytics.MetricKey {
	switch telemetry.EventKind(row.EventType) {
	case telemetry.EventKeywordReply:
		metrics := []analytics.MetricKey{analytics.MetricKeywordReplyCount}
		if row.Outcome != nil && *row.Outcome == string(analytics.ResultSuccess) {
			metrics = append(metrics, analytics.MetricKnowledgeTriggerCount)
		}
		return metrics
	case telemetry.EventKnowledgeRetrieval:
		if row.Outcome != nil && *row.Outcome == string(analytics.ResultSuccess) {
			return []analytics.MetricKey{analytics.MetricKnowledgeTriggerCount}
		}
		return nil
	case telemetry.EventAIRequest:
		return []analytics.MetricKey{analytics.MetricAIRequestCount, analytics.MetricAISuccessRate, analytics.MetricAIDurationMS}
	case telemetry.EventJoinRequest:
		return []analytics.MetricKey{analytics.MetricJoinRequestCount}
	case telemetry.EventManualApproval:
		return []analytics.MetricKey{analytics.MetricManualApprovalCount}
	case telemetry.EventAutomaticApproval:
		if row.Outcome == nil || *row.Outcome != string(analytics.ResultSuccess) {
			return nil
		}
		return []analytics.MetricKey{analytics.MetricAutomaticApprovalCount}
	case telemetry.EventScheduledJobRun:
		return []analytics.MetricKey{analytics.MetricScheduledJobRunCount}
	case telemetry.EventGroupMessage:
		return []analytics.MetricKey{analytics.MetricGroupMessageCount, analytics.MetricActiveUserCount}
	case telemetry.EventCommandRun:
		return []analytics.MetricKey{analytics.MetricCommandRunCount}
	case telemetry.EventLinkClean:
		return []analytics.MetricKey{analytics.MetricLinkCleanCount}
	case telemetry.EventQuote:
		if row.Outcome != nil && *row.Outcome == string(analytics.ResultSuccess) {
			return []analytics.MetricKey{analytics.MetricQuoteSuccessCount}
		}
		if row.Outcome != nil && *row.Outcome == string(analytics.ResultFallback) {
			return []analytics.MetricKey{analytics.MetricQuoteFallbackCount}
		}
		return []analytics.MetricKey{analytics.MetricQuoteFailureCount}
	default:
		return nil
	}
}

func telemetryEventCount(row telemetryEventManagerRow) uint64 {
	var metadata telemetryMetadataPayload
	if opsUnmarshalJSON(row.Metadata, &metadata) == nil && metadata.Count > 0 {
		return uint64(metadata.Count)
	}
	return 1
}

func dailyAccumulatorKey(row telemetryDailyManagerRow) string {
	return row.BucketDate.Format("2006-01-02") + "\x00" + row.MetricKey + "\x00" + strconv.FormatInt(row.GroupID, 10) + "\x00" + row.FeatureKey + "\x00" + row.Outcome
}

func (s *Store) AggregateTelemetryDaily(ctx context.Context, completedBefore time.Time, timezone string) error {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return err
	}
	var events []telemetryEventManagerRow
	if err := s.db.WithContext(ctx).Where("occurred_at < ?", completedBefore.UTC()).Order("occurred_at ASC").Find(&events).Error; err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	accumulators := make(map[string]*dailyAccumulator)
	dates := make(map[string]time.Time)
	for _, event := range events {
		local := event.OccurredAt.In(location)
		date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
		dates[date.Format("2006-01-02")] = date
		groups := []int64{0}
		if event.GroupID != nil && *event.GroupID != 0 {
			groups = append(groups, *event.GroupID)
		}
		features := []string{""}
		if event.FeatureKey != nil && *event.FeatureKey != "" {
			features = append(features, *event.FeatureKey)
		}
		outcomes := []string{""}
		if event.Outcome != nil && *event.Outcome != "" {
			outcomes = append(outcomes, *event.Outcome)
		}
		for _, metric := range telemetryMetricForEvent(event) {
			for _, groupID := range groups {
				for _, feature := range features {
					for _, outcome := range outcomes {
						prototype := telemetryDailyManagerRow{
							BucketDate: date, Timezone: timezone, MetricKey: string(metric),
							GroupID: groupID, FeatureKey: feature, Outcome: outcome,
						}
						key := dailyAccumulatorKey(prototype)
						accumulator := accumulators[key]
						if accumulator == nil {
							accumulator = &dailyAccumulator{row: prototype}
							accumulators[key] = accumulator
						}
						count := telemetryEventCount(event)
						switch metric {
						case analytics.MetricAISuccessRate:
							accumulator.row.SampleCount += count
							if event.Outcome != nil && *event.Outcome == string(analytics.ResultSuccess) {
								accumulator.row.ValueCount += count
							}
						case analytics.MetricAIDurationMS:
							if event.DurationMS != nil {
								accumulator.row.ValueSum += float64(*event.DurationMS) * float64(count)
								accumulator.row.SampleCount += count
							}
						case analytics.MetricActiveUserCount:
							if event.ActorHash != nil && *event.ActorHash != "" {
								if accumulator.users == nil {
									accumulator.users = make(map[string]struct{})
								}
								accumulator.users[*event.ActorHash] = struct{}{}
							}
						default:
							accumulator.row.ValueCount += count
							accumulator.row.SampleCount += count
						}
					}
				}
			}
		}
	}
	rows := make([]telemetryDailyManagerRow, 0, len(accumulators))
	for _, accumulator := range accumulators {
		if accumulator.users != nil {
			accumulator.row.ValueCount = uint64(len(accumulator.users))
			accumulator.row.SampleCount = accumulator.row.ValueCount
		}
		rows = append(rows, accumulator.row)
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		dateValues := make([]time.Time, 0, len(dates))
		for _, date := range dates {
			dateValues = append(dateValues, date)
		}
		if err := tx.Where("timezone = ? AND bucket_date IN ?", timezone, dateValues).Delete(&telemetryDailyManagerRow{}).Error; err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return tx.Omit("UpdatedAt").CreateInBatches(rows, 500).Error
	})
}

func (s *Store) PurgeTelemetryEvents(ctx context.Context, occurredBefore time.Time) (int64, error) {
	result := s.db.WithContext(ctx).Where("occurred_at < ?", occurredBefore.UTC()).Delete(&telemetryEventManagerRow{})
	return result.RowsAffected, result.Error
}

func analyticsEventQuery(db *gorm.DB, filter analytics.Filter) *gorm.DB {
	db = db.Model(&telemetryEventManagerRow{}).
		Where("occurred_at >= ? AND occurred_at < ?", filter.From.UTC(), filter.To.UTC())
	if len(filter.GroupIDs) > 0 {
		db = db.Where("group_id IN ?", filter.GroupIDs)
	}
	if len(filter.FeatureKeys) > 0 {
		values := make([]string, len(filter.FeatureKeys))
		for index, value := range filter.FeatureKeys {
			values[index] = string(value)
		}
		db = db.Where("feature_key IN ?", values)
	}
	if len(filter.Results) > 0 {
		values := make([]string, len(filter.Results))
		for index, value := range filter.Results {
			values[index] = string(value)
		}
		db = db.Where("outcome IN ?", values)
	}
	return db
}

func (s *Store) loadAnalyticsEvents(ctx context.Context, filter analytics.Filter) ([]telemetryEventManagerRow, error) {
	var rows []telemetryEventManagerRow
	if err := analyticsEventQuery(s.db.WithContext(ctx), filter).Order("occurred_at ASC").Order("event_id ASC").Find(&rows).Error; err != nil {
		return nil, analytics.ErrUnavailable
	}
	return rows, nil
}

func fullAnalyticsDayRange(filter analytics.Filter) (time.Time, time.Time, bool) {
	location, err := time.LoadLocation(filter.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	from := filter.From.In(location)
	start := time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, location)
	if from.After(start) {
		start = start.AddDate(0, 0, 1)
	}
	to := filter.To.In(location)
	end := time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, location)
	if !start.Before(end) {
		return time.Time{}, time.Time{}, false
	}
	return start, end, true
}

func (s *Store) loadAnalyticsDaily(ctx context.Context, filter analytics.Filter, metrics []analytics.MetricKey) ([]telemetryDailyManagerRow, map[string]struct{}, error) {
	start, end, ok := fullAnalyticsDayRange(filter)
	if !ok {
		return nil, nil, nil
	}
	startDate, endDate := start.Format("2006-01-02"), end.Format("2006-01-02")
	var dateRows []struct {
		BucketDate time.Time `gorm:"column:bucket_date"`
	}
	if err := s.db.WithContext(ctx).Model(&telemetryDailyManagerRow{}).Select("bucket_date").
		Where("timezone = ? AND bucket_date >= ? AND bucket_date < ?", filter.Timezone, startDate, endDate).
		Group("bucket_date").Scan(&dateRows).Error; err != nil {
		return nil, nil, analytics.ErrUnavailable
	}
	if len(dateRows) == 0 {
		return nil, nil, nil
	}
	dates := make(map[string]struct{}, len(dateRows))
	for _, row := range dateRows {
		dates[row.BucketDate.Format("2006-01-02")] = struct{}{}
	}
	db := s.db.WithContext(ctx).Model(&telemetryDailyManagerRow{}).
		Where("timezone = ? AND bucket_date >= ? AND bucket_date < ?", filter.Timezone, startDate, endDate)
	if len(metrics) > 0 {
		values := make([]string, len(metrics))
		for index, metric := range metrics {
			values[index] = string(metric)
		}
		db = db.Where("metric_key IN ?", values)
	}
	if len(filter.GroupIDs) == 0 {
		db = db.Where("group_id = 0")
	} else {
		db = db.Where("group_id IN ?", filter.GroupIDs)
	}
	if len(filter.FeatureKeys) == 0 {
		db = db.Where("feature_key = ''")
	} else {
		values := make([]string, len(filter.FeatureKeys))
		for index, value := range filter.FeatureKeys {
			values[index] = string(value)
		}
		db = db.Where("feature_key IN ?", values)
	}
	if len(filter.Results) == 0 {
		db = db.Where("outcome = ''")
	} else {
		values := make([]string, len(filter.Results))
		for index, value := range filter.Results {
			values[index] = string(value)
		}
		db = db.Where("outcome IN ?", values)
	}
	var rows []telemetryDailyManagerRow
	if err := db.Order("bucket_date ASC").Find(&rows).Error; err != nil {
		return nil, nil, analytics.ErrUnavailable
	}
	return rows, dates, nil
}

func excludeRolledUpEvents(events []telemetryEventManagerRow, timezone string, dates map[string]struct{}) []telemetryEventManagerRow {
	if len(dates) == 0 {
		return events
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return events
	}
	result := make([]telemetryEventManagerRow, 0, len(events))
	for _, event := range events {
		if _, rolledUp := dates[event.OccurredAt.In(location).Format("2006-01-02")]; !rolledUp {
			result = append(result, event)
		}
	}
	return result
}

func analyticsMetricNumberCombined(events []telemetryEventManagerRow, daily []telemetryDailyManagerRow, metric analytics.MetricKey) (float64, bool) {
	var dailyCount, dailySamples uint64
	var dailySum float64
	for _, row := range daily {
		if row.MetricKey != string(metric) {
			continue
		}
		dailyCount += row.ValueCount
		dailySum += row.ValueSum
		dailySamples += row.SampleCount
	}
	switch metric {
	case analytics.MetricAISuccessRate:
		var eventSuccesses, eventTotal uint64
		for _, event := range events {
			if telemetry.EventKind(event.EventType) != telemetry.EventAIRequest {
				continue
			}
			count := telemetryEventCount(event)
			eventTotal += count
			if event.Outcome != nil && *event.Outcome == string(analytics.ResultSuccess) {
				eventSuccesses += count
			}
		}
		total := dailySamples + eventTotal
		if total == 0 {
			return 0, false
		}
		return float64(dailyCount+eventSuccesses) * 100 / float64(total), true
	case analytics.MetricAIDurationMS:
		var eventSum float64
		var eventSamples uint64
		for _, event := range events {
			if telemetry.EventKind(event.EventType) != telemetry.EventAIRequest || event.DurationMS == nil {
				continue
			}
			count := telemetryEventCount(event)
			eventSum += float64(*event.DurationMS) * float64(count)
			eventSamples += count
		}
		total := dailySamples + eventSamples
		if total == 0 {
			return 0, false
		}
		return (dailySum + eventSum) / float64(total), true
	case analytics.MetricActiveUserCount:
		value, _ := analyticsMetricNumber(events, metric)
		return float64(dailyCount) + value, true
	default:
		value, _ := analyticsMetricNumber(events, metric)
		return float64(dailyCount) + value, true
	}
}

func analyticsCombinedFreshAt(events []telemetryEventManagerRow, daily []telemetryDailyManagerRow) time.Time {
	var fresh time.Time
	for _, event := range events {
		if event.OccurredAt.After(fresh) {
			fresh = event.OccurredAt.UTC()
		}
	}
	for _, row := range daily {
		if !row.UpdatedAt.IsZero() && row.UpdatedAt.After(fresh) {
			fresh = row.UpdatedAt.UTC()
		}
	}
	if fresh.IsZero() {
		fresh = time.Now().UTC()
	}
	return fresh.UTC()
}

func analyticsFreshAt(events []telemetryEventManagerRow) time.Time {
	if len(events) == 0 {
		return time.Now().UTC()
	}
	latest := events[0].OccurredAt
	for _, event := range events[1:] {
		if event.OccurredAt.After(latest) {
			latest = event.OccurredAt
		}
	}
	return latest.UTC()
}

func eventHasMetric(event telemetryEventManagerRow, metric analytics.MetricKey) bool {
	for _, candidate := range telemetryMetricForEvent(event) {
		if candidate == metric {
			return true
		}
	}
	return false
}

func analyticsMetricNumber(events []telemetryEventManagerRow, metric analytics.MetricKey) (float64, bool) {
	switch metric {
	case analytics.MetricAISuccessRate:
		var successes, total uint64
		for _, event := range events {
			if telemetry.EventKind(event.EventType) != telemetry.EventAIRequest {
				continue
			}
			count := telemetryEventCount(event)
			total += count
			if event.Outcome != nil && *event.Outcome == string(analytics.ResultSuccess) {
				successes += count
			}
		}
		if total == 0 {
			return 0, false
		}
		return float64(successes) * 100 / float64(total), true
	case analytics.MetricAIDurationMS:
		var sum float64
		var samples uint64
		for _, event := range events {
			if telemetry.EventKind(event.EventType) != telemetry.EventAIRequest || event.DurationMS == nil {
				continue
			}
			count := telemetryEventCount(event)
			sum += float64(*event.DurationMS) * float64(count)
			samples += count
		}
		if samples == 0 {
			return 0, false
		}
		return sum / float64(samples), true
	case analytics.MetricActiveUserCount:
		users := make(map[string]struct{})
		for _, event := range events {
			if !eventHasMetric(event, metric) || event.ActorHash == nil || *event.ActorHash == "" {
				continue
			}
			users[*event.ActorHash] = struct{}{}
		}
		return float64(len(users)), true
	default:
		var total uint64
		for _, event := range events {
			if eventHasMetric(event, metric) {
				total += telemetryEventCount(event)
			}
		}
		return float64(total), true
	}
}

func managerFloatPointer(value float64) *float64 {
	copy := value
	return &copy
}

func (s *Store) LoadSummary(ctx context.Context, filter analytics.Filter) (analytics.SummaryData, error) {
	current, err := s.loadAnalyticsEvents(ctx, filter)
	if err != nil {
		return analytics.SummaryData{}, err
	}
	duration := filter.To.Sub(filter.From)
	previousFilter := filter
	previousFilter.From = filter.From.Add(-duration)
	previousFilter.To = filter.From
	previous, err := s.loadAnalyticsEvents(ctx, previousFilter)
	if err != nil {
		return analytics.SummaryData{}, err
	}
	metrics := allAnalyticsMetrics()
	currentDaily, currentDates, err := s.loadAnalyticsDaily(ctx, filter, metrics)
	if err != nil {
		return analytics.SummaryData{}, err
	}
	current = excludeRolledUpEvents(current, filter.Timezone, currentDates)
	previousDaily, previousDates, err := s.loadAnalyticsDaily(ctx, previousFilter, metrics)
	if err != nil {
		return analytics.SummaryData{}, err
	}
	previous = excludeRolledUpEvents(previous, previousFilter.Timezone, previousDates)
	values := make(map[analytics.MetricKey]analytics.MetricValue, len(metrics))
	for _, metric := range metrics {
		currentValue, available := analyticsMetricNumberCombined(current, currentDaily, metric)
		if !available {
			values[metric] = analytics.MetricValue{}
			continue
		}
		value := analytics.MetricValue{Available: true, Value: managerFloatPointer(currentValue)}
		if previousValue, previousAvailable := analyticsMetricNumberCombined(previous, previousDaily, metric); previousAvailable {
			value.PreviousValue = managerFloatPointer(previousValue)
			if previousValue != 0 {
				value.ChangePercent = managerFloatPointer((currentValue - previousValue) * 100 / previousValue)
			}
		}
		values[metric] = value
	}
	allEvents := append(append([]telemetryEventManagerRow(nil), previous...), current...)
	allDaily := append(append([]telemetryDailyManagerRow(nil), previousDaily...), currentDaily...)
	return analytics.SummaryData{Values: values, DataFreshAt: analyticsCombinedFreshAt(allEvents, allDaily)}, nil
}

func allAnalyticsMetrics() []analytics.MetricKey {
	return []analytics.MetricKey{
		analytics.MetricKeywordReplyCount, analytics.MetricKnowledgeTriggerCount,
		analytics.MetricAIRequestCount, analytics.MetricAISuccessRate,
		analytics.MetricAIDurationMS, analytics.MetricJoinRequestCount, analytics.MetricManualApprovalCount,
		analytics.MetricAutomaticApprovalCount, analytics.MetricScheduledJobRunCount, analytics.MetricGroupMessageCount,
		analytics.MetricCommandRunCount, analytics.MetricActiveUserCount, analytics.MetricLinkCleanCount,
		analytics.MetricQuoteSuccessCount, analytics.MetricQuoteFallbackCount, analytics.MetricQuoteFailureCount,
	}
}

func analyticsBucket(value time.Time, granularity analytics.Granularity, location *time.Location) time.Time {
	local := value.In(location)
	if granularity == analytics.GranularityHour {
		return time.Date(local.Year(), local.Month(), local.Day(), local.Hour(), 0, 0, 0, location).UTC()
	}
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).UTC()
}

func (s *Store) LoadTimeseries(ctx context.Context, query analytics.StoreTimeseriesQuery) (analytics.TimeseriesData, error) {
	events, err := s.loadAnalyticsEvents(ctx, query.Filter)
	if err != nil {
		return analytics.TimeseriesData{}, err
	}
	location, err := time.LoadLocation(query.Timezone)
	if err != nil {
		return analytics.TimeseriesData{}, analytics.ErrUnavailable
	}
	var daily []telemetryDailyManagerRow
	if query.Granularity == analytics.GranularityDay {
		var dates map[string]struct{}
		daily, dates, err = s.loadAnalyticsDaily(ctx, query.Filter, query.Metrics)
		if err != nil {
			return analytics.TimeseriesData{}, err
		}
		events = excludeRolledUpEvents(events, query.Timezone, dates)
	}
	buckets := make(map[time.Time][]telemetryEventManagerRow)
	for _, event := range events {
		bucket := analyticsBucket(event.OccurredAt, query.Granularity, location)
		if bucket.Before(query.From) {
			bucket = query.From.UTC()
		}
		buckets[bucket] = append(buckets[bucket], event)
	}
	dailyBuckets := make(map[time.Time][]telemetryDailyManagerRow)
	for _, row := range daily {
		bucket := time.Date(row.BucketDate.Year(), row.BucketDate.Month(), row.BucketDate.Day(), 0, 0, 0, 0, location).UTC()
		dailyBuckets[bucket] = append(dailyBuckets[bucket], row)
	}
	starts := make([]time.Time, 0, len(buckets)+len(dailyBuckets))
	for start := range buckets {
		starts = append(starts, start)
	}
	for start := range dailyBuckets {
		if _, exists := buckets[start]; !exists {
			starts = append(starts, start)
		}
	}
	sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
	points := make(map[analytics.MetricKey][]analytics.Point, len(query.Metrics))
	for _, metric := range query.Metrics {
		for _, start := range starts {
			value, available := analyticsMetricNumberCombined(buckets[start], dailyBuckets[start], metric)
			if !available {
				continue
			}
			points[metric] = append(points[metric], analytics.Point{BucketStart: start.UTC(), Value: managerFloatPointer(value)})
		}
	}
	return analytics.TimeseriesData{Points: points, DataFreshAt: analyticsCombinedFreshAt(events, daily)}, nil
}

func analyticsDimensionValue(event telemetryEventManagerRow, dimension analytics.Dimension) (string, bool) {
	switch dimension {
	case analytics.DimensionGroup:
		if event.GroupID == nil || *event.GroupID <= 0 {
			return "", false
		}
		return strconv.FormatInt(*event.GroupID, 10), true
	case analytics.DimensionCommand:
		if event.CommandID == nil || *event.CommandID == "" {
			return "", false
		}
		return *event.CommandID, true
	case analytics.DimensionKnowledgeEntry:
		var metadata telemetryMetadataPayload
		if opsUnmarshalJSON(event.Metadata, &metadata) != nil || metadata.KnowledgeKey == "" {
			return "", false
		}
		return metadata.KnowledgeKey, true
	default:
		return "", false
	}
}

func analyticsKnowledgeIdentity(value string, resolver analytics.KnowledgeKeyResolver) (string, string) {
	if resolver == nil {
		return value, value
	}
	sourceKey, displayName, ok := resolver.ResolveKnowledgeKey(value)
	if !ok || sourceKey == "" {
		return value, value
	}
	if displayName == "" {
		displayName = sourceKey
	}
	return sourceKey, displayName
}

func (s *Store) LoadRankings(ctx context.Context, query analytics.StoreRankingsQuery) (analytics.RankingsData, error) {
	events, err := s.loadAnalyticsEvents(ctx, query.Filter)
	if err != nil {
		return analytics.RankingsData{}, err
	}
	var daily []telemetryDailyManagerRow
	if query.Dimension == analytics.DimensionGroup {
		var dates map[string]struct{}
		daily, dates, err = s.loadAnalyticsDailyGroupRankings(ctx, query)
		if err != nil {
			return analytics.RankingsData{}, err
		}
		events = excludeRolledUpEvents(events, query.Timezone, dates)
	}
	groups := make(map[string][]telemetryEventManagerRow)
	knowledgeNames := make(map[string]string)
	for _, event := range events {
		if !eventHasMetric(event, query.Metric) {
			continue
		}
		key, ok := analyticsDimensionValue(event, query.Dimension)
		if ok {
			if query.Dimension == analytics.DimensionKnowledgeEntry {
				var displayName string
				key, displayName = analyticsKnowledgeIdentity(key, query.KnowledgeResolver)
				knowledgeNames[key] = displayName
			}
			groups[key] = append(groups[key], event)
		}
	}
	dailyGroups := make(map[string][]telemetryDailyManagerRow)
	for _, row := range daily {
		key := strconv.FormatInt(row.GroupID, 10)
		dailyGroups[key] = append(dailyGroups[key], row)
	}
	items := make([]analytics.RankingValue, 0, len(groups)+len(dailyGroups))
	seen := make(map[string]struct{}, len(groups)+len(dailyGroups))
	for key, groupEvents := range groups {
		value, available := analyticsMetricNumberCombined(groupEvents, dailyGroups[key], query.Metric)
		if available {
			displayName := key
			if knowledgeNames[key] != "" {
				displayName = knowledgeNames[key]
			}
			items = append(items, analytics.RankingValue{Key: key, DisplayName: displayName, Value: value})
			seen[key] = struct{}{}
		}
	}
	for key, groupDaily := range dailyGroups {
		if _, exists := seen[key]; exists {
			continue
		}
		value, available := analyticsMetricNumberCombined(nil, groupDaily, query.Metric)
		if available {
			items = append(items, analytics.RankingValue{Key: key, DisplayName: key, Value: value})
		}
	}
	displayNames, err := s.analyticsDisplayNames(ctx, query.Dimension, items)
	if err != nil {
		return analytics.RankingsData{}, err
	}
	for index := range items {
		if name := displayNames[items[index].Key]; name != "" {
			items[index].DisplayName = name
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Value != items[j].Value {
			return items[i].Value > items[j].Value
		}
		return items[i].Key < items[j].Key
	})
	totalCount := len(items)
	pageNumber := query.Page
	if pageNumber < 1 {
		pageNumber = 1
	}
	start := (pageNumber - 1) * query.Limit
	if start > len(items) {
		start = len(items)
	}
	end := start + query.Limit
	if end > len(items) {
		end = len(items)
	}
	return analytics.RankingsData{
		Items: items[start:end], TotalCount: totalCount, DataFreshAt: analyticsCombinedFreshAt(events, daily),
	}, nil
}

func (s *Store) loadAnalyticsDailyGroupRankings(ctx context.Context, query analytics.StoreRankingsQuery) ([]telemetryDailyManagerRow, map[string]struct{}, error) {
	start, end, ok := fullAnalyticsDayRange(query.Filter)
	if !ok {
		return nil, nil, nil
	}
	startDate, endDate := start.Format("2006-01-02"), end.Format("2006-01-02")
	var dateRows []struct {
		BucketDate time.Time `gorm:"column:bucket_date"`
	}
	base := s.db.WithContext(ctx).Model(&telemetryDailyManagerRow{}).
		Where("timezone = ? AND bucket_date >= ? AND bucket_date < ?", query.Timezone, startDate, endDate)
	if err := base.Select("bucket_date").Group("bucket_date").Scan(&dateRows).Error; err != nil {
		return nil, nil, analytics.ErrUnavailable
	}
	if len(dateRows) == 0 {
		return nil, nil, nil
	}
	dates := make(map[string]struct{}, len(dateRows))
	for _, row := range dateRows {
		dates[row.BucketDate.Format("2006-01-02")] = struct{}{}
	}
	db := base.Where("metric_key = ? AND group_id > 0", query.Metric)
	if len(query.GroupIDs) > 0 {
		db = db.Where("group_id IN ?", query.GroupIDs)
	}
	if len(query.FeatureKeys) == 0 {
		db = db.Where("feature_key = ''")
	} else {
		values := make([]string, len(query.FeatureKeys))
		for index, value := range query.FeatureKeys {
			values[index] = string(value)
		}
		db = db.Where("feature_key IN ?", values)
	}
	if len(query.Results) == 0 {
		db = db.Where("outcome = ''")
	} else {
		values := make([]string, len(query.Results))
		for index, value := range query.Results {
			values[index] = string(value)
		}
		db = db.Where("outcome IN ?", values)
	}
	var rows []telemetryDailyManagerRow
	if err := db.Find(&rows).Error; err != nil {
		return nil, nil, analytics.ErrUnavailable
	}
	return rows, dates, nil
}

func (s *Store) analyticsDisplayNames(ctx context.Context, dimension analytics.Dimension, items []analytics.RankingValue) (map[string]string, error) {
	result := make(map[string]string)
	if len(items) == 0 || dimension == analytics.DimensionKnowledgeEntry {
		return result, nil
	}
	ids := make([]string, len(items))
	for index, item := range items {
		ids[index] = item.Key
	}
	if dimension == analytics.DimensionGroup {
		var rows []managedGroupManagerRow
		if err := s.db.WithContext(ctx).Where("group_id IN ?", ids).Find(&rows).Error; err != nil {
			return nil, analytics.ErrUnavailable
		}
		for _, row := range rows {
			result[strconv.FormatInt(row.GroupID, 10)] = row.Name
		}
		return result, nil
	}
	var rows []customCommandManagerRow
	if err := s.db.WithContext(ctx).Select("command_id, display_name").Where("command_id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, analytics.ErrUnavailable
	}
	for _, row := range rows {
		result[row.CommandID] = row.DisplayName
	}
	return result, nil
}

type joinRequestExportRows struct {
	rows  []analytics.JoinRequestExportRow
	index int
}

func (r *joinRequestExportRows) RowCount() int { return len(r.rows) }

func (r *joinRequestExportRows) Next(ctx context.Context) (analytics.JoinRequestExportRow, bool, error) {
	if err := ctx.Err(); err != nil {
		return analytics.JoinRequestExportRow{}, false, err
	}
	if r.index >= len(r.rows) {
		return analytics.JoinRequestExportRow{}, false, nil
	}
	value := r.rows[r.index]
	r.index++
	return value, true, nil
}

func (r *joinRequestExportRows) Close() error {
	r.rows = nil
	return nil
}

type scheduledRunExportRows struct {
	rows  []analytics.ScheduledJobRunExportRow
	index int
}

func (r *scheduledRunExportRows) RowCount() int { return len(r.rows) }

func (r *scheduledRunExportRows) Next(ctx context.Context) (analytics.ScheduledJobRunExportRow, bool, error) {
	if err := ctx.Err(); err != nil {
		return analytics.ScheduledJobRunExportRow{}, false, err
	}
	if r.index >= len(r.rows) {
		return analytics.ScheduledJobRunExportRow{}, false, nil
	}
	value := r.rows[r.index]
	r.index++
	return value, true, nil
}

func (r *scheduledRunExportRows) Close() error {
	r.rows = nil
	return nil
}

func (s *Store) OpenJoinRequestExport(ctx context.Context, filter analytics.Filter) (analytics.JoinRequestExportRows, error) {
	type exportRow struct {
		RequestID      string     `gorm:"column:request_id"`
		GroupID        int64      `gorm:"column:group_id"`
		SubType        string     `gorm:"column:sub_type"`
		Source         string     `gorm:"column:source"`
		ObservedStatus string     `gorm:"column:observed_status"`
		DecisionStatus string     `gorm:"column:decision_status"`
		DecisionSource *string    `gorm:"column:decision_source"`
		RequestedAt    time.Time  `gorm:"column:requested_at"`
		DecidedAt      *time.Time `gorm:"column:decided_at"`
	}
	db := s.db.WithContext(ctx).Table("group_join_requests AS request").
		Select(`request.flag AS request_id, request.group_id, request.sub_type, request.source,
request.observed_status, request.decision_status, request.decision_source, request.requested_at,
decision.completed_at AS decided_at`).
		Joins("LEFT JOIN group_join_decisions AS decision ON decision.decision_id = request.last_decision_id AND decision.request_id = request.id").
		Where("request.requested_at >= ? AND request.requested_at < ?", filter.From.UTC(), filter.To.UTC())
	if len(filter.GroupIDs) > 0 {
		db = db.Where("request.group_id IN ?", filter.GroupIDs)
	}
	if len(filter.Results) > 0 {
		statuses := make([]string, 0, len(filter.Results)*2)
		for _, result := range filter.Results {
			switch result {
			case analytics.ResultSuccess:
				statuses = append(statuses, string(joinrequests.DecisionApproved), string(joinrequests.DecisionRejected), string(joinrequests.DecisionExternalProcessed))
			case analytics.ResultUnknown:
				statuses = append(statuses, string(joinrequests.DecisionUnknown), string(joinrequests.DecisionProcessing))
			case analytics.ResultFailed:
				statuses = append(statuses, string(joinrequests.DecisionPending))
			}
		}
		if len(statuses) == 0 {
			return &joinRequestExportRows{}, nil
		}
		db = db.Where("request.decision_status IN ?", statuses)
	}
	var rows []exportRow
	if err := db.Order("request.requested_at ASC").Order("request.id ASC").Limit(analytics.MaxExportRows + 1).Scan(&rows).Error; err != nil {
		return nil, analytics.ErrUnavailable
	}
	result := make([]analytics.JoinRequestExportRow, len(rows))
	for index, row := range rows {
		result[index] = analytics.JoinRequestExportRow{
			RequestID: row.RequestID, GroupID: strconv.FormatInt(row.GroupID, 10), SubType: row.SubType,
			Source: row.Source, ObservedStatus: row.ObservedStatus, DecisionStatus: row.DecisionStatus,
			DecisionSource: row.DecisionSource, RequestedAt: row.RequestedAt.UTC(), DecidedAt: utcTimePointer(row.DecidedAt),
		}
	}
	return &joinRequestExportRows{rows: result}, nil
}

func (s *Store) OpenScheduledJobRunExport(ctx context.Context, filter analytics.Filter) (analytics.ScheduledJobRunExportRows, error) {
	type exportRow struct {
		RunID        string     `gorm:"column:run_id"`
		JobID        uint64     `gorm:"column:job_id"`
		GroupID      int64      `gorm:"column:group_id"`
		Kind         string     `gorm:"column:kind"`
		Result       string     `gorm:"column:result"`
		ScheduledFor *time.Time `gorm:"column:scheduled_for"`
		StartedAt    time.Time  `gorm:"column:started_at"`
		CompletedAt  *time.Time `gorm:"column:completed_at"`
		DurationMS   int64      `gorm:"column:duration_ms"`
		ErrorCode    *string    `gorm:"column:error_code"`
	}
	db := s.db.WithContext(ctx).Table("scheduled_job_runs AS run").
		Select(`run.run_id, run.job_id, job.group_id, run.kind, run.result, run.scheduled_for,
run.started_at, run.completed_at, run.duration_ms, run.error_code`).
		Joins("JOIN scheduled_jobs AS job ON job.id = run.job_id").
		Where("run.started_at >= ? AND run.started_at < ?", filter.From.UTC(), filter.To.UTC())
	if len(filter.GroupIDs) > 0 {
		db = db.Where("job.group_id IN ?", filter.GroupIDs)
	}
	if len(filter.Results) > 0 {
		values := make([]string, len(filter.Results))
		for index, value := range filter.Results {
			values[index] = string(value)
		}
		db = db.Where("run.result IN ?", values)
	}
	var rows []exportRow
	if err := db.Order("run.started_at ASC").Order("run.run_id ASC").Limit(analytics.MaxExportRows + 1).Scan(&rows).Error; err != nil {
		return nil, analytics.ErrUnavailable
	}
	result := make([]analytics.ScheduledJobRunExportRow, len(rows))
	for index, row := range rows {
		result[index] = analytics.ScheduledJobRunExportRow{
			RunID: row.RunID, JobID: strconv.FormatUint(row.JobID, 10), GroupID: strconv.FormatInt(row.GroupID, 10),
			Kind: row.Kind, Result: analytics.Result(row.Result), ScheduledFor: utcTimePointer(row.ScheduledFor),
			StartedAt: row.StartedAt.UTC(), CompletedAt: utcTimePointer(row.CompletedAt), DurationMS: row.DurationMS, ErrorCode: row.ErrorCode,
		}
	}
	return &scheduledRunExportRows{rows: result}, nil
}
