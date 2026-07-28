package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/overview"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	managersystem "github.com/zjutjh/jxh-go/internal/management/system"
	"gorm.io/gorm"
)

const (
	managerActorAdminUser        = "admin_user"
	managerIdempotencyInProgress = "in_progress"
	managerIdempotencyCompleted  = "completed"
	managerIdempotencyTTL        = 24 * time.Hour
)

var (
	_                      overview.Store      = (*Store)(nil)
	_                      groups.Store        = (*Store)(nil)
	_                      settings.Store      = (*Store)(nil)
	_                      managersystem.Store = (*Store)(nil)
	errManagerInvalidState                     = errors.New("invalid manager persistence state")
)

type managerManagedGroup struct {
	GroupID          int64      `gorm:"column:group_id;primaryKey"`
	Name             string     `gorm:"column:name"`
	MemberCount      uint64     `gorm:"column:member_count"`
	MaxMemberCount   uint64     `gorm:"column:max_member_count"`
	BotRole          string     `gorm:"column:bot_role"`
	SnapshotState    string     `gorm:"column:snapshot_state"`
	LastErrorCode    *string    `gorm:"column:last_error_code"`
	LastErrorMessage *string    `gorm:"column:last_error_message"`
	LastSyncedAt     *time.Time `gorm:"column:last_synced_at"`
	Revision         uint64     `gorm:"column:revision"`
	CreatedAt        time.Time  `gorm:"column:created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at"`
	ArchivedAt       *time.Time `gorm:"column:archived_at"`
}

func (managerManagedGroup) TableName() string { return "managed_groups" }

type managerFeatureSetting struct {
	SettingID            string    `gorm:"column:setting_id;primaryKey"`
	ScopeType            string    `gorm:"column:scope_type"`
	GroupID              *int64    `gorm:"column:group_id"`
	SettingsJSON         []byte    `gorm:"column:settings_json"`
	Revision             uint64    `gorm:"column:revision"`
	UpdatedByType        string    `gorm:"column:updated_by_type"`
	UpdatedByUserID      *string   `gorm:"column:updated_by_user_id"`
	UpdatedByQQUserID    *string   `gorm:"column:updated_by_qq_user_id"`
	UpdatedByDisplayName string    `gorm:"column:updated_by_display_name"`
	UpdatedByRole        *string   `gorm:"column:updated_by_role"`
	CreatedAt            time.Time `gorm:"column:created_at"`
	UpdatedAt            time.Time `gorm:"column:updated_at"`
}

func (managerFeatureSetting) TableName() string { return "feature_settings" }

type managerAdminAuditLog struct {
	AuditLogID        string    `gorm:"column:audit_log_id;primaryKey"`
	OccurredAt        time.Time `gorm:"column:occurred_at"`
	ActorType         string    `gorm:"column:actor_type"`
	ActorUserID       *string   `gorm:"column:actor_user_id"`
	ActorQQUserID     *string   `gorm:"column:actor_qq_user_id"`
	ActorDisplayName  *string   `gorm:"column:actor_display_name"`
	ActorRole         *string   `gorm:"column:actor_role"`
	ScopeType         *string   `gorm:"column:scope_type"`
	ScopeID           *string   `gorm:"column:scope_id"`
	Action            string    `gorm:"column:action"`
	TargetType        *string   `gorm:"column:target_type"`
	TargetID          *string   `gorm:"column:target_id"`
	TargetDisplayName *string   `gorm:"column:target_display_name"`
	Result            string    `gorm:"column:result"`
	ErrorCode         *string   `gorm:"column:error_code"`
	RequestID         string    `gorm:"column:request_id"`
	Source            string    `gorm:"column:source"`
	IPAddress         *string   `gorm:"column:ip_address"`
	UserAgent         *string   `gorm:"column:user_agent"`
	BeforeSnapshot    []byte    `gorm:"column:before_snapshot"`
	AfterSnapshot     []byte    `gorm:"column:after_snapshot"`
	Metadata          []byte    `gorm:"column:metadata"`
	Redacted          bool      `gorm:"column:redacted"`
}

func (managerAdminAuditLog) TableName() string { return "admin_audit_logs" }

type managerIdempotencyKey struct {
	ID                 uint64     `gorm:"column:idempotency_id;primaryKey;autoIncrement"`
	ActorType          string     `gorm:"column:actor_type"`
	ActorID            string     `gorm:"column:actor_id"`
	Operation          string     `gorm:"column:operation"`
	IdempotencyKey     string     `gorm:"column:idempotency_key"`
	RequestHash        string     `gorm:"column:request_hash"`
	State              string     `gorm:"column:state"`
	ResultStatus       *string    `gorm:"column:result_status"`
	ResponseStatus     *uint16    `gorm:"column:response_status"`
	ErrorCode          *string    `gorm:"column:error_code"`
	ResourceType       *string    `gorm:"column:resource_type"`
	ResourceID         *string    `gorm:"column:resource_id"`
	ResultingSessionID *string    `gorm:"column:resulting_session_id"`
	TraceID            *string    `gorm:"column:trace_id"`
	CreatedAt          time.Time  `gorm:"column:created_at"`
	CompletedAt        *time.Time `gorm:"column:completed_at"`
	ExpiresAt          time.Time  `gorm:"column:expires_at"`
}

func (managerIdempotencyKey) TableName() string { return "admin_idempotency_keys" }

type managerSystemOperation struct {
	OperationID     string     `gorm:"column:operation_id;primaryKey"`
	Type            string     `gorm:"column:type"`
	Status          string     `gorm:"column:status"`
	RequestedByType string     `gorm:"column:requested_by_type"`
	RequestedBy     string     `gorm:"column:requested_by"`
	IdempotencyID   *uint64    `gorm:"column:idempotency_id"`
	RequestID       string     `gorm:"column:request_id"`
	RequestedAt     time.Time  `gorm:"column:requested_at"`
	CompletedAt     *time.Time `gorm:"column:completed_at"`
	ErrorCode       *string    `gorm:"column:error_code"`
}

func (managerSystemOperation) TableName() string { return "system_operations" }

type managerActorColumns struct {
	Type        string
	UserID      *string
	QQUserID    *string
	DisplayName string
	Role        *string
}

type managerAuditContext struct {
	Actor     managerActorColumns
	RequestID string
	IPAddress *string
	UserAgent *string
	Source    string
}

type managerAuditEntry struct {
	Context           managerAuditContext
	OccurredAt        time.Time
	ScopeType         string
	ScopeID           string
	Action            string
	TargetType        string
	TargetID          string
	TargetDisplayName string
	Result            audit.Result
	ErrorCode         string
	Before            any
	After             any
	Metadata          any
}

func managerActorForPrincipal(_ *gorm.DB, principal auth.Principal) (managerActorColumns, error) {
	role := string(principal.Role)
	return managerActorColumns{
		Type: managerActorAdminUser, UserID: stringPointer(principal.UserID), DisplayName: principal.UserID, Role: stringPointer(role),
	}, nil
}

func managerAuditContextForMutation(tx *gorm.DB, principal auth.Principal, request auth.MutationContext) (managerAuditContext, error) {
	actor, err := managerActorForPrincipal(tx, principal)
	if err != nil {
		return managerAuditContext{}, err
	}
	return managerAuditContext{
		Actor: actor, RequestID: request.RequestID, IPAddress: optionalString(request.IPAddress),
		UserAgent: optionalString(request.UserAgent), Source: string(audit.SourceWeb),
	}, nil
}

func insertManagerAudit(tx *gorm.DB, entry managerAuditEntry) error {
	auditID, err := newManagerID("aud")
	if err != nil {
		return err
	}
	metadata, err := marshalManagerJSON(entry.Metadata)
	if err != nil {
		return err
	}
	before, err := marshalOptionalManagerJSON(entry.Before)
	if err != nil {
		return err
	}
	after, err := marshalOptionalManagerJSON(entry.After)
	if err != nil {
		return err
	}
	model := managerAdminAuditLog{
		AuditLogID: auditID, OccurredAt: entry.OccurredAt.UTC(), ActorType: entry.Context.Actor.Type,
		ActorUserID: entry.Context.Actor.UserID, ActorQQUserID: entry.Context.Actor.QQUserID,
		ActorDisplayName: optionalString(entry.Context.Actor.DisplayName), ActorRole: entry.Context.Actor.Role,
		ScopeType: optionalString(entry.ScopeType), ScopeID: optionalString(entry.ScopeID), Action: entry.Action,
		TargetType: optionalString(entry.TargetType), TargetID: optionalString(entry.TargetID),
		TargetDisplayName: optionalString(entry.TargetDisplayName), Result: string(entry.Result),
		ErrorCode: optionalString(entry.ErrorCode), RequestID: entry.Context.RequestID, Source: entry.Context.Source,
		IPAddress: entry.Context.IPAddress, UserAgent: entry.Context.UserAgent, BeforeSnapshot: before,
		AfterSnapshot: after, Metadata: metadata, Redacted: true,
	}
	return tx.Create(&model).Error
}

func managerAuditContextFromLog(model managerAdminAuditLog) managerAuditContext {
	displayName := ""
	if model.ActorDisplayName != nil {
		displayName = *model.ActorDisplayName
	}
	return managerAuditContext{
		Actor: managerActorColumns{
			Type: model.ActorType, UserID: model.ActorUserID, QQUserID: model.ActorQQUserID,
			DisplayName: displayName, Role: model.ActorRole,
		},
		RequestID: model.RequestID, IPAddress: model.IPAddress, UserAgent: model.UserAgent, Source: model.Source,
	}
}

func findManagerAuditContext(tx *gorm.DB, action, targetID string) (managerAuditContext, error) {
	var model managerAdminAuditLog
	err := tx.Where("action = ? AND target_id = ?", action, targetID).
		Order("occurred_at ASC").Order("audit_log_id ASC").Take(&model).Error
	if err != nil {
		return managerAuditContext{}, err
	}
	return managerAuditContextFromLog(model), nil
}

func newManagerID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate manager id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(random[:]), nil
}

func managerGroupSyncRequestHash() string {
	sum := sha256.Sum256([]byte("jxh-admin/groups-sync/v1"))
	return hex.EncodeToString(sum[:])
}

func marshalManagerJSON(value any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal manager json: %w", err)
	}
	return encoded, nil
}

func marshalOptionalManagerJSON(value any) ([]byte, error) {
	if value == nil {
		return nil, nil
	}
	return marshalManagerJSON(value)
}

func isManagerDuplicateKey(err error) bool {
	var mysqlError *gomysql.MySQLError
	return errors.As(err, &mysqlError) && mysqlError.Number == 1062
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return stringPointer(value)
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
