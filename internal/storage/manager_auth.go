package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/idempotency"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const (
	managerAuthIdempotencyTTL = 24 * time.Hour

	operationPasswordChange = "auth.password_change"
	operationPasswordReset  = "user.password_reset"
	operationRevokeUser     = "sessions.revoke_user"
	operationRevokeSession  = "sessions.revoke"

	replayPasswordReset = "password_reset"
	replayRevokeUser    = "revoke_user_sessions"
	replayRevokeSession = "revoke_session"
)

var (
	errManagerStorage = errors.New("manager storage operation failed")

	_ auth.Store        = (*Store)(nil)
	_ auth.AdminStore   = (*Store)(nil)
	_ audit.Store       = (*Store)(nil)
	_ idempotency.Store = (*Store)(nil)
)

type managerAdminUser struct {
	UserID       string     `gorm:"column:user_id;primaryKey"`
	Username     string     `gorm:"column:username"`
	DisplayName  string     `gorm:"column:display_name"`
	PasswordHash string     `gorm:"column:password_hash"`
	Role         auth.Role  `gorm:"column:role"`
	QQUserID     *string    `gorm:"column:qq_user_id"`
	Enabled      bool       `gorm:"column:enabled"`
	LastLoginAt  *time.Time `gorm:"column:last_login_at"`
	Revision     uint64     `gorm:"column:revision"`
	CreatedAt    time.Time  `gorm:"column:created_at"`
	UpdatedAt    time.Time  `gorm:"column:updated_at"`
	DeletedAt    *time.Time `gorm:"column:deleted_at"`
}

func (managerAdminUser) TableName() string { return "admin_users" }

type managerAdminSession struct {
	SessionID           string             `gorm:"column:session_id;primaryKey"`
	UserID              string             `gorm:"column:user_id"`
	TokenDigest         string             `gorm:"column:token_digest"`
	CSRFDigest          string             `gorm:"column:csrf_digest"`
	Status              auth.SessionStatus `gorm:"column:status"`
	IPAddress           *string            `gorm:"column:ip_address"`
	UserAgent           *string            `gorm:"column:user_agent"`
	CreatedAt           time.Time          `gorm:"column:created_at"`
	LastSeenAt          time.Time          `gorm:"column:last_seen_at"`
	ExpiresAt           time.Time          `gorm:"column:expires_at"`
	AbsoluteExpiresAt   time.Time          `gorm:"column:absolute_expires_at"`
	RevokedAt           *time.Time         `gorm:"column:revoked_at"`
	RevokedReason       *string            `gorm:"column:revoked_reason"`
	ReplacementDepth    uint64             `gorm:"column:replacement_depth"`
	ReplacedBySessionID *string            `gorm:"column:replaced_by_session_id"`
	ReplacedByUserID    *string            `gorm:"column:replaced_by_user_id"`
	ReplacedByDepth     *uint64            `gorm:"column:replaced_by_depth"`
}

func (managerAdminSession) TableName() string { return "admin_sessions" }

type managerAuditLog struct {
	AuditLogID       string          `gorm:"column:audit_log_id;primaryKey"`
	OccurredAt       time.Time       `gorm:"column:occurred_at"`
	ActorType        audit.ActorType `gorm:"column:actor_type"`
	ActorUserID      *string         `gorm:"column:actor_user_id"`
	ActorQQUserID    *string         `gorm:"column:actor_qq_user_id"`
	ActorDisplayName *string         `gorm:"column:actor_display_name"`
	ActorRole        *auth.Role      `gorm:"column:actor_role"`
	ScopeType        *string         `gorm:"column:scope_type"`
	ScopeID          *string         `gorm:"column:scope_id"`
	Action           string          `gorm:"column:action"`
	TargetType       *string         `gorm:"column:target_type"`
	TargetID         *string         `gorm:"column:target_id"`
	TargetDisplay    *string         `gorm:"column:target_display_name"`
	Result           audit.Result    `gorm:"column:result"`
	ErrorCode        *string         `gorm:"column:error_code"`
	RequestID        string          `gorm:"column:request_id"`
	Source           audit.Source    `gorm:"column:source"`
	IPAddress        *string         `gorm:"column:ip_address"`
	UserAgent        *string         `gorm:"column:user_agent"`
	BeforeSnapshot   json.RawMessage `gorm:"column:before_snapshot;type:json"`
	AfterSnapshot    json.RawMessage `gorm:"column:after_snapshot;type:json"`
	Metadata         json.RawMessage `gorm:"column:metadata;type:json"`
	Redacted         bool            `gorm:"column:redacted"`
}

func (managerAuditLog) TableName() string { return "admin_audit_logs" }

type managerIdempotency struct {
	ID                 uint64                    `gorm:"column:idempotency_id;primaryKey;autoIncrement"`
	ActorType          idempotency.ActorType     `gorm:"column:actor_type"`
	ActorID            string                    `gorm:"column:actor_id"`
	Operation          string                    `gorm:"column:operation"`
	Key                string                    `gorm:"column:idempotency_key"`
	RequestHash        string                    `gorm:"column:request_hash"`
	State              idempotency.State         `gorm:"column:state"`
	ResultStatus       *idempotency.ResultStatus `gorm:"column:result_status"`
	ResponseStatus     *int                      `gorm:"column:response_status"`
	ErrorCode          *string                   `gorm:"column:error_code"`
	ResourceType       *string                   `gorm:"column:resource_type"`
	ResourceID         *string                   `gorm:"column:resource_id"`
	ResultingSessionID *string                   `gorm:"column:resulting_session_id"`
	TraceID            *string                   `gorm:"column:trace_id"`
	CreatedAt          time.Time                 `gorm:"column:created_at"`
	CompletedAt        *time.Time                `gorm:"column:completed_at"`
	ExpiresAt          time.Time                 `gorm:"column:expires_at"`
}

func (managerIdempotency) TableName() string { return "admin_idempotency_keys" }

type managerIdentityRow struct {
	Session       managerAdminSession `gorm:"embedded"`
	AuthUserID    string              `gorm:"column:auth_user_id"`
	Username      string              `gorm:"column:auth_username"`
	DisplayName   string              `gorm:"column:auth_display_name"`
	Role          auth.Role           `gorm:"column:auth_role"`
	QQUserID      *string             `gorm:"column:auth_qq_user_id"`
	Enabled       bool                `gorm:"column:auth_enabled"`
	LastLoginAt   *time.Time          `gorm:"column:auth_last_login_at"`
	UserCreatedAt time.Time           `gorm:"column:auth_created_at"`
	UserUpdatedAt time.Time           `gorm:"column:auth_updated_at"`
	UserRevision  uint64              `gorm:"column:auth_revision"`
}

type managerAuthCursor struct {
	Version     int    `json:"v"`
	Kind        string `json:"k"`
	TimeMillis  int64  `json:"t"`
	ID          string `json:"i"`
	Fingerprint string `json:"f"`
}

type managerMutationReplay struct {
	Kind         string                    `json:"kind"`
	User         *auth.User                `json:"user,omitempty"`
	Revoke       *auth.SessionRevokeResult `json:"revoke,omitempty"`
	RevokedCount int                       `json:"revoked_count,omitempty"`
	CompletedAt  time.Time                 `json:"completed_at,omitempty"`
}

type managerAuthAuditWrite struct {
	ID       string
	Actor    auth.Principal
	Context  auth.MutationContext
	At       time.Time
	Action   string
	Target   audit.Target
	Before   any
	After    any
	Metadata any
}

func (s *Store) LookupUserByUsername(ctx context.Context, normalizedUsername string) (auth.UserCredentials, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.UserCredentials{}, false, err
	}
	var row managerAdminUser
	result := db.Where("username = ? AND deleted_at IS NULL", normalizedUsername).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return auth.UserCredentials{}, false, nil
	}
	if result.Error != nil {
		return auth.UserCredentials{}, false, managerFailure("lookup admin user", result.Error)
	}
	return credentialsFromManagerUser(row), true, nil
}

func (s *Store) LookupUserByID(ctx context.Context, userID string) (auth.UserCredentials, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.UserCredentials{}, false, err
	}
	var row managerAdminUser
	result := db.Where("user_id = ? AND deleted_at IS NULL", userID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return auth.UserCredentials{}, false, nil
	}
	if result.Error != nil {
		return auth.UserCredentials{}, false, managerFailure("lookup admin user", result.Error)
	}
	return credentialsFromManagerUser(row), true, nil
}

func (s *Store) CommitLogin(ctx context.Context, commit auth.LoginCommit) error {
	db, err := s.managerDB(ctx)
	if err != nil {
		return err
	}
	return managerTransaction(db, "commit admin login", func(tx *gorm.DB) error {
		user, found, err := lockManagerUser(tx, commit.Session.UserID)
		if err != nil {
			return err
		}
		if !found || !user.Enabled {
			return auth.ErrInvalidCredentials
		}
		if commit.PasswordHashUpdate != nil && (commit.PasswordHashUpdate.UserID != user.UserID ||
			commit.PasswordHashUpdate.ExpectedVersion != user.Revision) {
			return auth.ErrInvalidCredentials
		}

		var prior *managerAdminSession
		if commit.PriorTokenDigest != nil {
			candidate, ok, err := lockManagerSessionByDigest(tx, digestString(*commit.PriorTokenDigest))
			if err != nil {
				return err
			}
			if ok && candidate.UserID == user.UserID && candidate.Status == auth.SessionStatusActive {
				prior = &candidate
			}
		}
		depth := uint64(0)
		if prior != nil {
			depth = prior.ReplacementDepth + 1
		}
		row := managerSessionFromAuth(commit.Session, commit.TokenDigest, commit.CSRFDigest, depth)
		if err := tx.Create(&row).Error; err != nil {
			return managerFailure("create admin session", err)
		}
		if prior != nil {
			if err := replaceManagerSession(tx, *prior, row, commit.Session.CreatedAt, "login_rotation"); err != nil {
				return err
			}
		}

		updates := map[string]any{
			"last_login_at": normalizedManagerTime(commit.Session.CreatedAt),
			"updated_at":    normalizedManagerTime(commit.Session.CreatedAt),
		}
		if commit.PasswordHashUpdate != nil {
			updates["password_hash"] = commit.PasswordHashUpdate.PasswordHash
			updates["revision"] = gorm.Expr("revision + 1")
		}
		result := tx.Model(&managerAdminUser{}).
			Where("user_id = ? AND deleted_at IS NULL", user.UserID).Updates(updates)
		if result.Error != nil || result.RowsAffected != 1 {
			return managerFailure("update admin login state", result.Error)
		}
		return writeManagerAuthAudit(tx, managerAuthAuditWrite{
			Actor:   auth.Principal{UserID: user.UserID, SessionID: row.SessionID, Role: user.Role},
			Context: auth.MutationContext{IPAddress: commit.Session.IPAddress, UserAgent: commit.Session.UserAgent},
			At:      commit.Session.CreatedAt, Action: "auth.login",
			Target:   audit.Target{Type: "admin_session", ID: row.SessionID},
			Metadata: map[string]any{"rotated_prior": prior != nil, "password_hash_upgraded": commit.PasswordHashUpdate != nil},
		})
	})
}

func (s *Store) LookupSession(ctx context.Context, tokenDigest auth.TokenDigest) (auth.SessionIdentity, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.SessionIdentity{}, false, err
	}
	return loadManagerIdentity(db, "s.token_digest = ?", digestString(tokenDigest), false)
}

func (s *Store) TouchSessionIfStale(ctx context.Context, touch auth.SessionTouch) error {
	db, err := s.managerDB(ctx)
	if err != nil {
		return err
	}
	result := db.Model(&managerAdminSession{}).
		Where("session_id = ? AND status = ? AND revoked_at IS NULL AND last_seen_at <= ?",
			touch.SessionID, auth.SessionStatusActive, normalizedManagerTime(touch.IfLastSeenBefore)).
		Updates(map[string]any{
			"last_seen_at": normalizedManagerTime(touch.LastSeenAt),
			"expires_at":   normalizedManagerTime(touch.ExpiresAt),
		})
	if result.Error != nil {
		return managerFailure("touch admin session", result.Error)
	}
	return nil
}

func (s *Store) LookupReplacedSession(ctx context.Context, tokenDigest auth.TokenDigest) (auth.SessionIdentity, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.SessionIdentity{}, false, err
	}
	return loadManagerIdentity(db, "s.token_digest = ?", digestString(tokenDigest), true)
}

func (s *Store) LookupPasswordChange(ctx context.Context, lookup auth.PasswordChangeLookup) (auth.SessionIdentity, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.SessionIdentity{}, false, err
	}
	var reservation managerIdempotency
	result := db.Where("actor_type = ? AND actor_id = ? AND operation = ? AND idempotency_key = ?",
		idempotency.ActorAdminUser, lookup.UserID, operationPasswordChange, lookup.IdempotencyKey).Take(&reservation)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return auth.SessionIdentity{}, false, nil
	}
	if result.Error != nil {
		return auth.SessionIdentity{}, false, managerFailure("lookup password change", result.Error)
	}
	if !sameManagerHash(reservation.RequestHash, lookup.RequestHash) {
		return auth.SessionIdentity{}, false, auth.ErrAdminIdempotencyReuse
	}
	if reservation.State != idempotency.StateCompleted || reservation.ResultingSessionID == nil {
		return auth.SessionIdentity{}, false, nil
	}
	return loadManagerIdentity(db, "s.session_id = ?", *reservation.ResultingSessionID, false)
}

func (s *Store) CommitPasswordChange(ctx context.Context, commit auth.PasswordChangeCommit) (auth.SessionIdentity, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.SessionIdentity{}, err
	}
	var identity auth.SessionIdentity
	err = managerTransaction(db, "commit password change", func(tx *gorm.DB) error {
		reservation, err := reserveManagerOperation(tx, commit.Actor.UserID, operationPasswordChange,
			commit.IdempotencyKey, commit.RequestHash, commit.OccurredAt)
		if err != nil {
			return err
		}
		if !reservation.Fresh {
			if reservation.State != idempotency.StateCompleted || reservation.Result == nil || reservation.Result.ResultingSessionID == "" {
				return managerFailure("replay password change", nil)
			}
			replayed, found, err := loadManagerIdentity(tx, "s.session_id = ?", reservation.Result.ResultingSessionID, false)
			if err != nil {
				return err
			}
			if !found {
				return managerFailure("replay password change", nil)
			}
			identity = replayed
			return nil
		}

		user, found, err := lockManagerUser(tx, commit.Actor.UserID)
		if err != nil {
			return err
		}
		if !found || !user.Enabled || user.Revision != commit.ExpectedUserVersion {
			return auth.ErrUnauthenticated
		}
		prior, found, err := lockManagerSessionByID(tx, commit.PriorSessionID)
		if err != nil {
			return err
		}
		if !found || prior.UserID != user.UserID || prior.Status != auth.SessionStatusActive ||
			!sameManagerHash(prior.TokenDigest, digestString(commit.PriorTokenDigest)) {
			return auth.ErrUnauthenticated
		}
		var maxDepth uint64
		if err := tx.Model(&managerAdminSession{}).Where("user_id = ?", user.UserID).
			Select("COALESCE(MAX(replacement_depth), 0)").Scan(&maxDepth).Error; err != nil {
			return managerFailure("load admin session replacement depth", err)
		}
		newRow := managerSessionFromAuth(commit.NewSession, commit.NewTokenDigest, commit.NewCSRFDigest, maxDepth+1)
		if err := tx.Create(&newRow).Error; err != nil {
			return managerFailure("create replacement admin session", err)
		}
		revokedAt := normalizedManagerTime(commit.OccurredAt)
		reason := "password_changed"
		result := tx.Model(&managerAdminSession{}).
			Where("user_id = ? AND session_id <> ? AND status = ?", user.UserID, newRow.SessionID, auth.SessionStatusActive).
			Updates(map[string]any{
				"status": auth.SessionStatusRevoked, "revoked_at": revokedAt, "revoked_reason": reason,
				"replaced_by_session_id": newRow.SessionID, "replaced_by_user_id": newRow.UserID,
				"replaced_by_depth": newRow.ReplacementDepth,
			})
		if result.Error != nil {
			return managerFailure("revoke password change sessions", result.Error)
		}
		updatedUser := tx.Model(&managerAdminUser{}).Where("user_id = ? AND revision = ? AND deleted_at IS NULL",
			user.UserID, user.Revision).Updates(map[string]any{
			"password_hash": commit.PasswordHash, "revision": gorm.Expr("revision + 1"), "updated_at": revokedAt,
		})
		if updatedUser.Error != nil {
			return managerFailure("update changed admin password", updatedUser.Error)
		}
		if updatedUser.RowsAffected != 1 {
			return auth.ErrAdminRevisionConflict
		}
		user.PasswordHash = commit.PasswordHash
		user.Revision++
		user.UpdatedAt = revokedAt
		completed := idempotency.Completion{RequestHash: commit.RequestHash, CompletedAt: revokedAt, Result: idempotency.Result{
			Status: idempotency.ResultSucceeded, ResponseStatus: 200,
			Resource:           &idempotency.Resource{Type: "admin_user", ID: user.UserID},
			ResultingSessionID: newRow.SessionID, CompletedAt: revokedAt,
		}}
		if _, err := completeManagerIdempotency(tx, reservation.ID, completed); err != nil {
			return err
		}
		if err := writeManagerAuthAudit(tx, managerAuthAuditWrite{
			ID: managerAuditIDForReservation(reservation.ID), Actor: commit.Actor, Context: commit.Context,
			At: revokedAt, Action: "auth.password_change", Target: audit.Target{Type: "admin_user", ID: user.UserID, DisplayName: user.DisplayName},
			Before: managerUserSnapshot(user, user.Revision-1), After: managerUserSnapshot(user, user.Revision),
			Metadata: map[string]any{"revoked_session_count": result.RowsAffected, "resulting_session_id": newRow.SessionID},
		}); err != nil {
			return err
		}
		csrfDigest, err := parseManagerDigest(newRow.CSRFDigest)
		if err != nil {
			return err
		}
		identity = auth.SessionIdentity{User: userFromManagerRow(user), Session: sessionFromManagerRow(newRow), CSRFDigest: csrfDigest}
		return nil
	})
	return identity, err
}

func (s *Store) RevokeCurrentSession(ctx context.Context, revocation auth.CurrentSessionRevocation) error {
	db, err := s.managerDB(ctx)
	if err != nil {
		return err
	}
	return managerTransaction(db, "revoke current admin session", func(tx *gorm.DB) error {
		row, found, err := lockManagerSessionByID(tx, revocation.SessionID)
		if err != nil {
			return err
		}
		if !found || row.UserID != revocation.Actor.UserID || !sameManagerHash(row.TokenDigest, digestString(revocation.TokenDigest)) {
			return auth.ErrUnauthenticated
		}
		if row.Status != auth.SessionStatusActive {
			return nil
		}
		revokedAt := normalizedManagerTime(revocation.RevokedAt)
		result := tx.Model(&managerAdminSession{}).Where("session_id = ? AND status = ?", row.SessionID, auth.SessionStatusActive).
			Updates(map[string]any{"status": auth.SessionStatusRevoked, "revoked_at": revokedAt, "revoked_reason": "logout"})
		if result.Error != nil {
			return managerFailure("revoke current admin session", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil
		}
		return writeManagerAuthAudit(tx, managerAuthAuditWrite{
			Actor: revocation.Actor, Context: revocation.Context, At: revokedAt, Action: "auth.logout",
			Target:   audit.Target{Type: "admin_session", ID: row.SessionID},
			Metadata: map[string]any{"user_id": row.UserID},
		})
	})
}

func (s *Store) CreateAdminUser(ctx context.Context, mutation auth.CreateUserMutation) (auth.User, error) {
	if !validManagerMutation(mutation.Actor, mutation.Context) {
		return auth.User{}, auth.ErrInvalidAdminInput
	}
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.User{}, err
	}
	var created auth.User
	err = managerTransaction(db, "create admin user", func(tx *gorm.DB) error {
		userID, err := newManagerAuthID("usr_")
		if err != nil {
			return err
		}
		at := normalizedManagerTime(mutation.OccurredAt)
		row := managerAdminUser{
			UserID: userID, Username: mutation.Username, DisplayName: mutation.DisplayName,
			PasswordHash: mutation.PasswordHash, Role: mutation.Role, QQUserID: cloneManagerAuthString(mutation.QQUserID),
			Enabled: true, Revision: 1, CreatedAt: at, UpdatedAt: at,
		}
		if err := tx.Create(&row).Error; err != nil {
			if isManagerDuplicate(err) {
				return auth.ErrAdminIdentityConflict
			}
			return managerFailure("insert admin user", err)
		}
		created = userFromManagerRow(row)
		return writeManagerAuthAudit(tx, managerAuthAuditWrite{
			Actor: mutation.Actor, Context: mutation.Context, At: at, Action: "user.create",
			Target: audit.Target{Type: "admin_user", ID: row.UserID, DisplayName: row.DisplayName},
			After:  managerUserSnapshot(row, row.Revision), Metadata: map[string]any{},
		})
	})
	return created, err
}

func (s *Store) GetAdminUser(ctx context.Context, userID string) (auth.User, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.User{}, false, err
	}
	var row managerAdminUser
	result := db.Select(managerUserPublicColumns()).Where("user_id = ? AND deleted_at IS NULL", userID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return auth.User{}, false, nil
	}
	if result.Error != nil {
		return auth.User{}, false, managerFailure("get admin user", result.Error)
	}
	return userFromManagerRow(row), true, nil
}

func (s *Store) ListAdminUsers(ctx context.Context, query auth.UserListQuery) (auth.UserPage, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.UserPage{}, err
	}
	fingerprint := managerUserFilterFingerprint(query)
	request := db.Model(&managerAdminUser{}).Select(managerUserPublicColumns()).Where("deleted_at IS NULL")
	if value := strings.TrimSpace(query.Query); value != "" {
		pattern := "%" + escapeManagerAuthLike(strings.ToLower(value)) + "%"
		request = request.Where("(LOWER(username) LIKE ? ESCAPE '=' OR LOWER(display_name) LIKE ? ESCAPE '=' OR qq_user_id = ?)",
			pattern, pattern, value)
	}
	if query.Role != "" {
		request = request.Where("role = ?", query.Role)
	}
	if query.Enabled != nil {
		request = request.Where("enabled = ?", *query.Enabled)
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerAuthCursor(query.Cursor, "users", fingerprint)
		if err != nil {
			return auth.UserPage{}, auth.ErrInvalidAdminInput
		}
		request = request.Where("(created_at < ? OR (created_at = ? AND user_id < ?))", cursor.time(), cursor.time(), cursor.ID)
	}
	var rows []managerAdminUser
	if err := request.Order("created_at DESC").Order("user_id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return auth.UserPage{}, managerFailure("list admin users", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	page := auth.UserPage{Items: make([]auth.User, len(rows)), HasMore: hasMore}
	for index := range rows {
		page.Items[index] = userFromManagerRow(rows[index])
	}
	if hasMore && len(rows) > 0 {
		page.NextCursor, err = encodeManagerAuthCursor("users", rows[len(rows)-1].CreatedAt, rows[len(rows)-1].UserID, fingerprint)
		if err != nil {
			return auth.UserPage{}, err
		}
	}
	return page, nil
}

func (s *Store) UpdateAdminUser(ctx context.Context, mutation auth.UpdateUserMutation) (auth.User, error) {
	if !validManagerMutation(mutation.Actor, mutation.Context) {
		return auth.User{}, auth.ErrInvalidAdminInput
	}
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.User{}, err
	}
	var updated auth.User
	err = managerTransaction(db, "update admin user", func(tx *gorm.DB) error {
		superAdminIDs, err := lockEnabledSuperAdmins(tx)
		if err != nil {
			return err
		}
		row, found, err := lockManagerUser(tx, mutation.UserID)
		if err != nil {
			return err
		}
		if !found {
			return auth.ErrAdminUserNotFound
		}
		if row.Revision != mutation.ExpectedRevision {
			return auth.ErrAdminRevisionConflict
		}
		before := row
		proposedRole, proposedEnabled := row.Role, row.Enabled
		if mutation.Patch.Role.Set {
			proposedRole = mutation.Patch.Role.Value
		}
		if mutation.Patch.Enabled.Set {
			proposedEnabled = mutation.Patch.Enabled.Value
		}
		if row.Role == auth.RoleSuperAdmin && row.Enabled &&
			(proposedRole != auth.RoleSuperAdmin || !proposedEnabled) && len(superAdminIDs) == 1 && superAdminIDs[0] == row.UserID {
			return auth.ErrLastSuperAdmin
		}

		updates := map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": normalizedManagerTime(mutation.OccurredAt)}
		if mutation.Patch.DisplayName.Set {
			updates["display_name"] = mutation.Patch.DisplayName.Value
		}
		if mutation.Patch.Role.Set {
			updates["role"] = mutation.Patch.Role.Value
		}
		if mutation.Patch.QQUserID.Set {
			updates["qq_user_id"] = cloneManagerAuthString(mutation.Patch.QQUserID.Value)
		}
		if mutation.Patch.Enabled.Set {
			updates["enabled"] = mutation.Patch.Enabled.Value
		}
		result := tx.Model(&managerAdminUser{}).Where("user_id = ? AND revision = ? AND deleted_at IS NULL",
			row.UserID, row.Revision).Updates(updates)
		if result.Error != nil {
			if isManagerDuplicate(result.Error) {
				return auth.ErrAdminIdentityConflict
			}
			return managerFailure("update admin user", result.Error)
		}
		if result.RowsAffected != 1 {
			return auth.ErrAdminRevisionConflict
		}
		row, found, err = lockManagerUser(tx, row.UserID)
		if err != nil {
			return err
		}
		if !found {
			return managerFailure("reload updated admin user", nil)
		}
		updated = userFromManagerRow(row)
		return writeManagerAuthAudit(tx, managerAuthAuditWrite{
			Actor: mutation.Actor, Context: mutation.Context, At: mutation.OccurredAt, Action: "user.update",
			Target: audit.Target{Type: "admin_user", ID: row.UserID, DisplayName: row.DisplayName},
			Before: managerUserSnapshot(before, before.Revision), After: managerUserSnapshot(row, row.Revision), Metadata: map[string]any{},
		})
	})
	return updated, err
}

func (s *Store) ResetAdminUserPassword(ctx context.Context, mutation auth.ResetPasswordMutation) (auth.PasswordResetResult, error) {
	if !validManagerMutation(mutation.Actor, mutation.Context) || !validManagerHash(mutation.RequestHash) {
		return auth.PasswordResetResult{}, auth.ErrInvalidAdminInput
	}
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.PasswordResetResult{}, err
	}
	var output auth.PasswordResetResult
	err = managerTransaction(db, "reset admin password", func(tx *gorm.DB) error {
		reservation, err := reserveManagerOperation(tx, mutation.Actor.UserID, operationPasswordReset,
			mutation.IdempotencyKey, mutation.RequestHash, mutation.OccurredAt)
		if err != nil {
			return err
		}
		if !reservation.Fresh {
			replay, err := loadManagerMutationReplay(tx, reservation.ID, replayPasswordReset)
			if err != nil {
				return err
			}
			if replay.User == nil {
				return managerFailure("replay password reset", nil)
			}
			output = auth.PasswordResetResult{User: *replay.User, RevokedSessionCount: replay.RevokedCount, CompletedAt: replay.CompletedAt}
			return nil
		}
		row, found, err := lockManagerUser(tx, mutation.UserID)
		if err != nil {
			return err
		}
		if !found {
			return auth.ErrAdminUserNotFound
		}
		if row.Revision != mutation.ExpectedRevision {
			return auth.ErrAdminRevisionConflict
		}
		before := row
		at := normalizedManagerTime(mutation.OccurredAt)
		result := tx.Model(&managerAdminUser{}).Where("user_id = ? AND revision = ? AND deleted_at IS NULL", row.UserID, row.Revision).
			Updates(map[string]any{"password_hash": mutation.PasswordHash, "revision": gorm.Expr("revision + 1"), "updated_at": at})
		if result.Error != nil {
			return managerFailure("reset admin password", result.Error)
		}
		if result.RowsAffected != 1 {
			return auth.ErrAdminRevisionConflict
		}
		revoked := tx.Model(&managerAdminSession{}).Where("user_id = ? AND status = ?", row.UserID, auth.SessionStatusActive).
			Updates(map[string]any{"status": auth.SessionStatusRevoked, "revoked_at": at, "revoked_reason": "password_reset"})
		if revoked.Error != nil {
			return managerFailure("revoke reset admin sessions", revoked.Error)
		}
		row.PasswordHash = mutation.PasswordHash
		row.Revision++
		row.UpdatedAt = at
		user := userFromManagerRow(row)
		output = auth.PasswordResetResult{User: user, RevokedSessionCount: int(revoked.RowsAffected), CompletedAt: at}
		replay := managerMutationReplay{
			Kind: replayPasswordReset, User: &user, RevokedCount: output.RevokedSessionCount, CompletedAt: at,
		}
		if err := writeManagerAuthAudit(tx, managerAuthAuditWrite{
			ID: managerAuditIDForReservation(reservation.ID), Actor: mutation.Actor, Context: mutation.Context,
			At: at, Action: operationPasswordReset, Target: audit.Target{Type: "admin_user", ID: row.UserID, DisplayName: row.DisplayName},
			Before: managerUserSnapshot(before, before.Revision), After: managerUserSnapshot(row, row.Revision), Metadata: replay,
		}); err != nil {
			return err
		}
		_, err = completeManagerIdempotency(tx, reservation.ID, idempotency.Completion{
			RequestHash: mutation.RequestHash, CompletedAt: at,
			Result: idempotency.Result{Status: idempotency.ResultSucceeded, ResponseStatus: 200,
				Resource: &idempotency.Resource{Type: "admin_user", ID: row.UserID}, CompletedAt: at},
		})
		return err
	})
	return output, err
}

func (s *Store) RevokeAdminUserSessions(ctx context.Context, mutation auth.RevokeSessionsMutation) (auth.SessionRevokeResult, error) {
	return s.revokeManagerSessions(ctx, mutation, true)
}

func (s *Store) RevokeAdminSession(ctx context.Context, mutation auth.RevokeSessionsMutation) (auth.SessionRevokeResult, error) {
	return s.revokeManagerSessions(ctx, mutation, false)
}

func (s *Store) revokeManagerSessions(ctx context.Context, mutation auth.RevokeSessionsMutation, all bool) (auth.SessionRevokeResult, error) {
	if !validManagerMutation(mutation.Actor, mutation.Context) {
		return auth.SessionRevokeResult{}, auth.ErrInvalidAdminInput
	}
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.SessionRevokeResult{}, err
	}
	operation, kind, targetID := operationRevokeSession, replayRevokeSession, mutation.SessionID
	if all {
		operation, kind, targetID = operationRevokeUser, replayRevokeUser, mutation.UserID
	}
	requestHash := managerRequestHash(operation, targetID)
	var output auth.SessionRevokeResult
	err = managerTransaction(db, "revoke admin sessions", func(tx *gorm.DB) error {
		reservation, err := reserveManagerOperation(tx, mutation.Actor.UserID, operation,
			mutation.IdempotencyKey, requestHash, mutation.OccurredAt)
		if err != nil {
			return err
		}
		if !reservation.Fresh {
			replay, err := loadManagerMutationReplay(tx, reservation.ID, kind)
			if err != nil {
				return err
			}
			if replay.Revoke == nil {
				return managerFailure("replay session revocation", nil)
			}
			output = cloneManagerRevoke(*replay.Revoke)
			return nil
		}

		at := normalizedManagerTime(mutation.OccurredAt)
		var target audit.Target
		if all {
			user, found, err := lockManagerUser(tx, mutation.UserID)
			if err != nil {
				return err
			}
			if !found {
				return auth.ErrAdminUserNotFound
			}
			result := tx.Model(&managerAdminSession{}).Where("user_id = ? AND status = ?", user.UserID, auth.SessionStatusActive).
				Updates(map[string]any{"status": auth.SessionStatusRevoked, "revoked_at": at, "revoked_reason": "admin_revoke_user"})
			if result.Error != nil {
				return managerFailure("revoke admin user sessions", result.Error)
			}
			output = auth.SessionRevokeResult{UserID: user.UserID, RevokedCount: int(result.RowsAffected), RevokedAt: at}
			target = audit.Target{Type: "admin_user", ID: user.UserID, DisplayName: user.DisplayName}
		} else {
			session, found, err := lockManagerSessionByID(tx, mutation.SessionID)
			if err != nil {
				return err
			}
			if !found {
				return auth.ErrAdminSessionNotFound
			}
			count := int64(0)
			if session.Status == auth.SessionStatusActive {
				result := tx.Model(&managerAdminSession{}).Where("session_id = ? AND status = ?", session.SessionID, auth.SessionStatusActive).
					Updates(map[string]any{"status": auth.SessionStatusRevoked, "revoked_at": at, "revoked_reason": "admin_revoke"})
				if result.Error != nil {
					return managerFailure("revoke admin session", result.Error)
				}
				count = result.RowsAffected
			}
			sessionID := session.SessionID
			output = auth.SessionRevokeResult{UserID: session.UserID, SessionID: &sessionID, RevokedCount: int(count), RevokedAt: at}
			target = audit.Target{Type: "admin_session", ID: session.SessionID}
		}
		replay := managerMutationReplay{Kind: kind, Revoke: managerRevokePointer(output), CompletedAt: at}
		if err := writeManagerAuthAudit(tx, managerAuthAuditWrite{
			ID: managerAuditIDForReservation(reservation.ID), Actor: mutation.Actor, Context: mutation.Context,
			At: at, Action: operation, Target: target, Metadata: replay,
		}); err != nil {
			return err
		}
		resourceType := target.Type
		_, err = completeManagerIdempotency(tx, reservation.ID, idempotency.Completion{
			RequestHash: requestHash, CompletedAt: at,
			Result: idempotency.Result{Status: idempotency.ResultSucceeded, ResponseStatus: 200,
				Resource: &idempotency.Resource{Type: resourceType, ID: target.ID}, CompletedAt: at},
		})
		return err
	})
	return output, err
}

func (s *Store) ListAdminSessions(ctx context.Context, query auth.SessionListQuery) (auth.SessionPage, error) {
	if (query.CurrentSessionID != "" && len(query.CurrentSessionID) > 64) || query.AsOf.IsZero() {
		return auth.SessionPage{}, auth.ErrInvalidAdminInput
	}
	db, err := s.managerDB(ctx)
	if err != nil {
		return auth.SessionPage{}, err
	}
	asOf := normalizedManagerTime(query.AsOf)
	expired := db.Model(&managerAdminSession{}).
		Where("status = ? AND (expires_at <= ? OR absolute_expires_at <= ?)", auth.SessionStatusActive, asOf, asOf).
		Update("status", auth.SessionStatusExpired)
	if expired.Error != nil {
		return auth.SessionPage{}, managerFailure("expire admin sessions", expired.Error)
	}
	fingerprint := managerSessionFilterFingerprint(query)
	request := db.Model(&managerAdminSession{}).Select(managerSessionPublicColumns())
	if query.UserID != "" {
		request = request.Where("user_id = ?", query.UserID)
	}
	if query.Status != "" {
		request = request.Where("status = ?", query.Status)
	}
	if query.Current != nil {
		if query.CurrentSessionID == "" {
			return auth.SessionPage{}, auth.ErrInvalidAdminInput
		}
		if *query.Current {
			request = request.Where("session_id = ?", query.CurrentSessionID)
		} else {
			request = request.Where("session_id <> ?", query.CurrentSessionID)
		}
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerAuthCursor(query.Cursor, "sessions", fingerprint)
		if err != nil {
			return auth.SessionPage{}, auth.ErrInvalidAdminInput
		}
		request = request.Where("(last_seen_at < ? OR (last_seen_at = ? AND session_id < ?))", cursor.time(), cursor.time(), cursor.ID)
	}
	var rows []managerAdminSession
	if err := request.Order("last_seen_at DESC").Order("session_id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return auth.SessionPage{}, managerFailure("list admin sessions", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	page := auth.SessionPage{Items: make([]auth.Session, len(rows)), HasMore: hasMore}
	for index := range rows {
		page.Items[index] = sessionFromManagerRow(rows[index])
		page.Items[index].Current = rows[index].SessionID == query.CurrentSessionID
	}
	if hasMore && len(rows) > 0 {
		page.NextCursor, err = encodeManagerAuthCursor("sessions", rows[len(rows)-1].LastSeenAt, rows[len(rows)-1].SessionID, fingerprint)
		if err != nil {
			return auth.SessionPage{}, err
		}
	}
	return page, nil
}

func (s *Store) GetAuditLog(ctx context.Context, id string) (audit.Log, bool, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return audit.Log{}, false, err
	}
	var row managerAuditLog
	result := db.Where("audit_log_id = ?", id).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return audit.Log{}, false, nil
	}
	if result.Error != nil {
		return audit.Log{}, false, managerFailure("get audit log", result.Error)
	}
	log, err := auditLogFromManagerRow(row)
	if err != nil {
		return audit.Log{}, false, err
	}
	return log, true, nil
}

func (s *Store) ListAuditLogs(ctx context.Context, query audit.ListQuery) (audit.Page, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return audit.Page{}, err
	}
	fingerprint := managerAuditFilterFingerprint(query)
	request := db.Model(&managerAuditLog{}).Select(managerAuditSummaryColumns())
	if query.ActorUserID != "" {
		request = request.Where("actor_user_id = ?", query.ActorUserID)
	}
	if query.ActorType != "" {
		request = request.Where("actor_type = ?", query.ActorType)
	}
	if len(query.Actions) > 0 {
		request = request.Where("action IN ?", normalizedManagerStrings(query.Actions))
	}
	if len(query.TargetTypes) > 0 {
		request = request.Where("target_type IN ?", normalizedManagerStrings(query.TargetTypes))
	}
	if query.TargetID != "" {
		request = request.Where("target_id = ?", query.TargetID)
	}
	if query.Result != "" {
		request = request.Where("result = ?", query.Result)
	}
	if query.From != nil {
		request = request.Where("occurred_at >= ?", normalizedManagerTime(*query.From))
	}
	if query.To != nil {
		request = request.Where("occurred_at <= ?", normalizedManagerTime(*query.To))
	}
	if query.Cursor != "" {
		cursor, err := decodeManagerAuthCursor(query.Cursor, "audit", fingerprint)
		if err != nil {
			return audit.Page{}, audit.ErrInvalidQuery
		}
		request = request.Where("(occurred_at < ? OR (occurred_at = ? AND audit_log_id < ?))", cursor.time(), cursor.time(), cursor.ID)
	}
	var rows []managerAuditLog
	if err := request.Order("occurred_at DESC").Order("audit_log_id DESC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return audit.Page{}, managerFailure("list audit logs", err)
	}
	hasMore := len(rows) > query.Limit
	if hasMore {
		rows = rows[:query.Limit]
	}
	page := audit.Page{Items: make([]audit.Summary, len(rows)), HasMore: hasMore}
	for index := range rows {
		page.Items[index] = auditSummaryFromManagerRow(rows[index])
	}
	if hasMore && len(rows) > 0 {
		page.NextCursor, err = encodeManagerAuthCursor("audit", rows[len(rows)-1].OccurredAt, rows[len(rows)-1].AuditLogID, fingerprint)
		if err != nil {
			return audit.Page{}, err
		}
	}
	return page, nil
}

func (s *Store) ReserveIdempotency(ctx context.Context, reservation idempotency.Reservation) (idempotency.Reservation, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return idempotency.Reservation{}, err
	}
	var output idempotency.Reservation
	err = managerTransaction(db, "reserve idempotency key", func(tx *gorm.DB) error {
		var err error
		output, err = reserveManagerIdempotency(tx, reservation)
		return err
	})
	return output, err
}

func (s *Store) CompleteIdempotency(ctx context.Context, id uint64, completion idempotency.Completion) (idempotency.Reservation, error) {
	db, err := s.managerDB(ctx)
	if err != nil {
		return idempotency.Reservation{}, err
	}
	var output idempotency.Reservation
	err = managerTransaction(db, "complete idempotency key", func(tx *gorm.DB) error {
		var err error
		output, err = completeManagerIdempotency(tx, id, completion)
		return err
	})
	return output, err
}

func (s *Store) managerDB(ctx context.Context) (*gorm.DB, error) {
	if s == nil || s.db == nil || ctx == nil {
		return nil, managerFailure("open manager storage", nil)
	}
	return s.db.WithContext(ctx).Session(&gorm.Session{Logger: logger.Discard}), nil
}

func managerTransaction(db *gorm.DB, operation string, fn func(*gorm.DB) error) error {
	err := db.Transaction(fn)
	if err == nil || publicManagerError(err) {
		return err
	}
	return managerFailure(operation, err)
}

func publicManagerError(err error) bool {
	for _, target := range []error{
		context.Canceled, context.DeadlineExceeded, errManagerStorage,
		auth.ErrInvalidCredentials, auth.ErrUnauthenticated, auth.ErrInvalidAdminInput,
		auth.ErrAdminUserNotFound, auth.ErrAdminSessionNotFound, auth.ErrAdminRevisionConflict,
		auth.ErrLastSuperAdmin, auth.ErrAdminIdentityConflict, auth.ErrAdminIdempotencyReuse,
		idempotency.ErrKeyReused,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func managerFailure(operation string, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, errManagerStorage)
}

func isManagerDuplicate(err error) bool {
	var mysqlErr *drivermysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1062
}

func normalizedManagerTime(value time.Time) time.Time { return value.UTC().Truncate(time.Millisecond) }

func newManagerAuthID(prefix string) (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", managerFailure("generate manager identifier", err)
	}
	return prefix + hex.EncodeToString(random[:]), nil
}

func managerAuditIDForReservation(id uint64) string { return "aud_i_" + strconv.FormatUint(id, 10) }

func digestString(value auth.TokenDigest) string { return hex.EncodeToString(value[:]) }

func parseManagerDigest(value string) (auth.TokenDigest, error) {
	var digest auth.TokenDigest
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != len(digest) {
		return digest, managerFailure("decode stored token digest", nil)
	}
	copy(digest[:], decoded)
	return digest, nil
}

func sameManagerHash(first, second string) bool {
	return len(first) == len(second) && hmac.Equal([]byte(first), []byte(second))
}

func validManagerHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func managerStringPointer(value string) *string {
	if value == "" {
		return nil
	}
	return cloneManagerAuthString(&value)
}

func cloneManagerAuthString(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func credentialsFromManagerUser(row managerAdminUser) auth.UserCredentials {
	return auth.UserCredentials{User: userFromManagerRow(row), PasswordHash: row.PasswordHash}
}

func userFromManagerRow(row managerAdminUser) auth.User {
	return auth.User{
		ID: row.UserID, Username: row.Username, DisplayName: row.DisplayName, Role: row.Role,
		QQUserID: cloneManagerAuthString(row.QQUserID), Enabled: row.Enabled, LastLoginAt: cloneManagerTime(row.LastLoginAt),
		CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC(), Version: row.Revision,
	}
}

func cloneManagerTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := value.UTC()
	return &cloned
}

func managerSessionFromAuth(session auth.Session, tokenDigest, csrfDigest auth.TokenDigest, depth uint64) managerAdminSession {
	return managerAdminSession{
		SessionID: session.ID, UserID: session.UserID, TokenDigest: digestString(tokenDigest), CSRFDigest: digestString(csrfDigest),
		Status: session.Status, IPAddress: managerStringPointer(session.IPAddress), UserAgent: managerStringPointer(session.UserAgent),
		CreatedAt: normalizedManagerTime(session.CreatedAt), LastSeenAt: normalizedManagerTime(session.LastSeenAt),
		ExpiresAt: normalizedManagerTime(session.ExpiresAt), AbsoluteExpiresAt: normalizedManagerTime(session.AbsoluteExpiresAt),
		RevokedAt: cloneManagerTime(session.RevokedAt), ReplacementDepth: depth,
	}
}

func sessionFromManagerRow(row managerAdminSession) auth.Session {
	return auth.Session{
		ID: row.SessionID, UserID: row.UserID, Status: row.Status,
		IPAddress: managerStringValue(row.IPAddress), UserAgent: managerStringValue(row.UserAgent),
		CreatedAt: row.CreatedAt.UTC(), LastSeenAt: row.LastSeenAt.UTC(), ExpiresAt: row.ExpiresAt.UTC(),
		AbsoluteExpiresAt: row.AbsoluteExpiresAt.UTC(), RevokedAt: cloneManagerTime(row.RevokedAt),
	}
}

func managerStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func managerUserPublicColumns() []string {
	return []string{"user_id", "username", "display_name", "role", "qq_user_id", "enabled", "last_login_at", "revision", "created_at", "updated_at"}
}

func managerSessionPublicColumns() []string {
	return []string{"session_id", "user_id", "status", "ip_address", "user_agent", "created_at", "last_seen_at", "expires_at", "absolute_expires_at", "revoked_at"}
}

func lockManagerUser(tx *gorm.DB, userID string) (managerAdminUser, bool, error) {
	var row managerAdminUser
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("user_id = ? AND deleted_at IS NULL", userID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return managerAdminUser{}, false, nil
	}
	if result.Error != nil {
		return managerAdminUser{}, false, managerFailure("lock admin user", result.Error)
	}
	return row, true, nil
}

func lockEnabledSuperAdmins(tx *gorm.DB) ([]string, error) {
	var ids []string
	if err := tx.Model(&managerAdminUser{}).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("role = ? AND enabled = ? AND deleted_at IS NULL", auth.RoleSuperAdmin, true).
		Order("user_id").Pluck("user_id", &ids).Error; err != nil {
		return nil, managerFailure("lock enabled super administrators", err)
	}
	return ids, nil
}

func lockManagerSessionByID(tx *gorm.DB, sessionID string) (managerAdminSession, bool, error) {
	var row managerAdminSession
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("session_id = ?", sessionID).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return managerAdminSession{}, false, nil
	}
	if result.Error != nil {
		return managerAdminSession{}, false, managerFailure("lock admin session", result.Error)
	}
	return row, true, nil
}

func lockManagerSessionByDigest(tx *gorm.DB, digest string) (managerAdminSession, bool, error) {
	var row managerAdminSession
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("token_digest = ?", digest).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return managerAdminSession{}, false, nil
	}
	if result.Error != nil {
		return managerAdminSession{}, false, managerFailure("lock admin session", result.Error)
	}
	return row, true, nil
}

func replaceManagerSession(tx *gorm.DB, prior, replacement managerAdminSession, at time.Time, reason string) error {
	revokedAt := normalizedManagerTime(at)
	result := tx.Model(&managerAdminSession{}).Where("session_id = ? AND user_id = ? AND status = ?",
		prior.SessionID, prior.UserID, auth.SessionStatusActive).Updates(map[string]any{
		"status": auth.SessionStatusRevoked, "revoked_at": revokedAt, "revoked_reason": reason,
		"replaced_by_session_id": replacement.SessionID, "replaced_by_user_id": replacement.UserID,
		"replaced_by_depth": replacement.ReplacementDepth,
	})
	if result.Error != nil {
		return managerFailure("replace admin session", result.Error)
	}
	return nil
}

func loadManagerIdentity(db *gorm.DB, predicate string, value any, requireReplacement bool) (auth.SessionIdentity, bool, error) {
	selectColumns := `s.session_id, s.user_id, s.token_digest, s.csrf_digest, s.status, s.ip_address, s.user_agent,
s.created_at, s.last_seen_at, s.expires_at, s.absolute_expires_at, s.revoked_at, s.revoked_reason,
s.replacement_depth, s.replaced_by_session_id, s.replaced_by_user_id, s.replaced_by_depth,
u.user_id AS auth_user_id, u.username AS auth_username, u.display_name AS auth_display_name,
u.role AS auth_role, u.qq_user_id AS auth_qq_user_id, u.enabled AS auth_enabled,
u.last_login_at AS auth_last_login_at, u.created_at AS auth_created_at,
u.updated_at AS auth_updated_at, u.revision AS auth_revision`
	request := db.Table("admin_sessions AS s").Select(selectColumns).
		Joins("JOIN admin_users AS u ON u.user_id = s.user_id AND u.deleted_at IS NULL").Where(predicate, value)
	if requireReplacement {
		request = request.Where("s.status = ? AND s.replaced_by_session_id IS NOT NULL", auth.SessionStatusRevoked)
	}
	var row managerIdentityRow
	result := request.Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return auth.SessionIdentity{}, false, nil
	}
	if result.Error != nil {
		return auth.SessionIdentity{}, false, managerFailure("load admin session identity", result.Error)
	}
	csrfDigest, err := parseManagerDigest(row.Session.CSRFDigest)
	if err != nil {
		return auth.SessionIdentity{}, false, err
	}
	user := auth.User{
		ID: row.AuthUserID, Username: row.Username, DisplayName: row.DisplayName, Role: row.Role,
		QQUserID: cloneManagerAuthString(row.QQUserID), Enabled: row.Enabled, LastLoginAt: cloneManagerTime(row.LastLoginAt),
		CreatedAt: row.UserCreatedAt.UTC(), UpdatedAt: row.UserUpdatedAt.UTC(), Version: row.UserRevision,
	}
	return auth.SessionIdentity{User: user, Session: sessionFromManagerRow(row.Session), CSRFDigest: csrfDigest}, true, nil
}

func managerUserSnapshot(row managerAdminUser, revision uint64) map[string]any {
	return map[string]any{
		"user_id": row.UserID, "username": row.Username, "display_name": row.DisplayName,
		"role": row.Role, "qq_user_id": row.QQUserID, "enabled": row.Enabled, "revision": revision,
	}
}

func validManagerMutation(actor auth.Principal, request auth.MutationContext) bool {
	return actor.UserID != "" && len(actor.UserID) <= 64 && len(actor.SessionID) <= 64 &&
		len(request.RequestID) <= 64 && len(request.IPAddress) <= 64 && len(request.UserAgent) <= 300
}

func writeManagerAuthAudit(tx *gorm.DB, input managerAuthAuditWrite) error {
	id := input.ID
	if id == "" {
		var err error
		id, err = newManagerAuthID("aud_")
		if err != nil {
			return err
		}
	}
	sanitizedBefore, beforeRedacted := audit.SanitizeForWrite(input.Before)
	sanitizedAfter, afterRedacted := audit.SanitizeForWrite(input.After)
	sanitizedMetadata, metadataRedacted := audit.SanitizeForWrite(input.Metadata)
	before, err := managerJSON(sanitizedBefore, true)
	if err != nil {
		return err
	}
	after, err := managerJSON(sanitizedAfter, true)
	if err != nil {
		return err
	}
	metadata, err := managerJSON(sanitizedMetadata, false)
	if err != nil {
		return err
	}
	actorID := cloneManagerAuthString(&input.Actor.UserID)
	role := input.Actor.Role
	actorName := ""
	var actor managerAdminUser
	if err := tx.Select("display_name").Where("user_id = ?", input.Actor.UserID).Take(&actor).Error; err == nil {
		actorName = actor.DisplayName
	}
	row := managerAuditLog{
		AuditLogID: id, OccurredAt: normalizedManagerTime(input.At), ActorType: audit.ActorAdminUser,
		ActorUserID: actorID, ActorDisplayName: managerStringPointer(actorName), ActorRole: &role,
		Action: input.Action, TargetType: managerStringPointer(input.Target.Type), TargetID: managerStringPointer(input.Target.ID),
		TargetDisplay: managerStringPointer(input.Target.DisplayName), Result: audit.ResultSuccess,
		RequestID: input.Context.RequestID, Source: audit.SourceWeb,
		IPAddress: managerStringPointer(input.Context.IPAddress), UserAgent: managerStringPointer(input.Context.UserAgent),
		BeforeSnapshot: before, AfterSnapshot: after, Metadata: metadata,
		Redacted: beforeRedacted || afterRedacted || metadataRedacted,
	}
	if err := tx.Create(&row).Error; err != nil {
		return managerFailure("write manager audit log", err)
	}
	return nil
}

func managerJSON(value any, nullable bool) (json.RawMessage, error) {
	if value == nil {
		if nullable {
			return nil, nil
		}
		return json.RawMessage(`{}`), nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, managerFailure("encode manager audit snapshot", err)
	}
	return encoded, nil
}

func auditLogFromManagerRow(row managerAuditLog) (audit.Log, error) {
	before, err := decodeManagerJSONObject(row.BeforeSnapshot)
	if err != nil {
		return audit.Log{}, err
	}
	after, err := decodeManagerJSONObject(row.AfterSnapshot)
	if err != nil {
		return audit.Log{}, err
	}
	metadata, err := decodeManagerJSONObject(row.Metadata)
	if err != nil {
		return audit.Log{}, err
	}
	summary := auditSummaryFromManagerRow(row)
	return audit.Log{
		ID: summary.ID, OccurredAt: summary.OccurredAt, Actor: summary.Actor, Action: summary.Action,
		Target: summary.Target, Result: summary.Result, ErrorCode: summary.ErrorCode, RequestID: summary.RequestID,
		Source: row.Source, IPAddress: cloneManagerAuthString(row.IPAddress), UserAgent: cloneManagerAuthString(row.UserAgent),
		Before: before, After: after, Metadata: metadata, Redacted: row.Redacted,
	}, nil
}

func auditSummaryFromManagerRow(row managerAuditLog) audit.Summary {
	return audit.Summary{
		ID: row.AuditLogID, OccurredAt: row.OccurredAt.UTC(),
		Actor:  audit.Actor{Type: row.ActorType, UserID: cloneManagerAuthString(row.ActorUserID), QQUserID: cloneManagerAuthString(row.ActorQQUserID), DisplayName: managerStringValue(row.ActorDisplayName)},
		Action: row.Action,
		Target: audit.Target{Type: managerStringValue(row.TargetType), ID: managerStringValue(row.TargetID), DisplayName: managerStringValue(row.TargetDisplay)},
		Result: row.Result, ErrorCode: cloneManagerAuthString(row.ErrorCode), RequestID: row.RequestID,
	}
}

func decodeManagerJSONObject(encoded json.RawMessage) (map[string]any, error) {
	if len(encoded) == 0 || bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
		return nil, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, managerFailure("decode manager audit snapshot", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, managerFailure("decode manager audit snapshot", nil)
	}
	return value, nil
}

func managerAuditSummaryColumns() []string {
	return []string{
		"audit_log_id", "occurred_at", "actor_type", "actor_user_id", "actor_qq_user_id", "actor_display_name",
		"action", "target_type", "target_id", "target_display_name", "result", "error_code", "request_id",
	}
}

func reserveManagerOperation(tx *gorm.DB, actorID, operation, key, requestHash string, at time.Time) (idempotency.Reservation, error) {
	reservation, err := reserveManagerIdempotency(tx, idempotency.Reservation{
		ActorType: idempotency.ActorAdminUser, ActorID: actorID, Operation: operation, Key: key,
		RequestHash: requestHash, State: idempotency.StateInProgress,
		CreatedAt: normalizedManagerTime(at), ExpiresAt: normalizedManagerTime(at.Add(managerAuthIdempotencyTTL)),
	})
	if errors.Is(err, idempotency.ErrKeyReused) {
		return idempotency.Reservation{}, auth.ErrAdminIdempotencyReuse
	}
	return reservation, err
}

func reserveManagerIdempotency(tx *gorm.DB, requested idempotency.Reservation) (idempotency.Reservation, error) {
	row := managerIdempotency{
		ActorType: requested.ActorType, ActorID: requested.ActorID, Operation: requested.Operation, Key: requested.Key,
		RequestHash: requested.RequestHash, State: idempotency.StateInProgress,
		CreatedAt: normalizedManagerTime(requested.CreatedAt), ExpiresAt: normalizedManagerTime(requested.ExpiresAt),
	}
	result := tx.Exec(`INSERT INTO admin_idempotency_keys
(actor_type, actor_id, operation, idempotency_key, request_hash, state, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE idempotency_id = idempotency_id + LAST_INSERT_ID(0)`,
		row.ActorType, row.ActorID, row.Operation, row.Key, row.RequestHash, row.State, row.CreatedAt, row.ExpiresAt)
	if result.Error != nil {
		return idempotency.Reservation{}, managerFailure("insert idempotency key", result.Error)
	}
	var insertedID uint64
	if err := tx.Raw("SELECT LAST_INSERT_ID()").Scan(&insertedID).Error; err != nil {
		return idempotency.Reservation{}, managerFailure("classify idempotency key reservation", err)
	}
	fresh := insertedID != 0
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("actor_type = ? AND actor_id = ? AND operation = ? AND idempotency_key = ?",
			requested.ActorType, requested.ActorID, requested.Operation, requested.Key).
		Take(&row).Error; err != nil {
		return idempotency.Reservation{}, managerFailure("load winning idempotency key", err)
	}
	if fresh && row.ID != insertedID {
		return idempotency.Reservation{}, managerFailure("classify idempotency key reservation", nil)
	}
	if !fresh && !row.ExpiresAt.After(normalizedManagerTime(requested.CreatedAt)) {
		if err := tx.Delete(&row).Error; err != nil {
			return idempotency.Reservation{}, managerFailure("expire idempotency key", err)
		}
		row = managerIdempotency{
			ActorType: requested.ActorType, ActorID: requested.ActorID, Operation: requested.Operation, Key: requested.Key,
			RequestHash: requested.RequestHash, State: idempotency.StateInProgress,
			CreatedAt: normalizedManagerTime(requested.CreatedAt), ExpiresAt: normalizedManagerTime(requested.ExpiresAt),
		}
		if err := tx.Create(&row).Error; err != nil {
			return idempotency.Reservation{}, managerFailure("replace expired idempotency key", err)
		}
		fresh = true
	}
	if fresh {
		return reservationFromManagerRow(row, true)
	}
	return existingManagerReservation(row, requested.RequestHash)
}

func existingManagerReservation(row managerIdempotency, requestHash string) (idempotency.Reservation, error) {
	reservation, err := reservationFromManagerRow(row, false)
	if err != nil {
		return idempotency.Reservation{}, err
	}
	if !sameManagerHash(reservation.RequestHash, requestHash) {
		return reservation, idempotency.ErrKeyReused
	}
	return reservation, nil
}

func completeManagerIdempotency(tx *gorm.DB, id uint64, completion idempotency.Completion) (idempotency.Reservation, error) {
	var row managerIdempotency
	result := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("idempotency_id = ?", id).Take(&row)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return idempotency.Reservation{}, managerFailure("complete missing idempotency key", nil)
	}
	if result.Error != nil {
		return idempotency.Reservation{}, managerFailure("lock idempotency key", result.Error)
	}
	if !sameManagerHash(row.RequestHash, completion.RequestHash) {
		reservation, conversionErr := reservationFromManagerRow(row, false)
		if conversionErr != nil {
			return idempotency.Reservation{}, conversionErr
		}
		return reservation, idempotency.ErrKeyReused
	}
	if row.State == idempotency.StateCompleted {
		return reservationFromManagerRow(row, false)
	}
	updates := managerCompletionUpdates(completion)
	result = tx.Model(&managerIdempotency{}).
		Where("idempotency_id = ? AND state = ? AND request_hash = ?", id, idempotency.StateInProgress, completion.RequestHash).
		Updates(updates)
	if result.Error != nil {
		return idempotency.Reservation{}, managerFailure("complete idempotency key", result.Error)
	}
	if result.RowsAffected != 1 {
		return idempotency.Reservation{}, managerFailure("complete idempotency key", nil)
	}
	if err := tx.Where("idempotency_id = ?", id).Take(&row).Error; err != nil {
		return idempotency.Reservation{}, managerFailure("reload completed idempotency key", err)
	}
	return reservationFromManagerRow(row, false)
}

func managerCompletionUpdates(completion idempotency.Completion) map[string]any {
	result := completion.Result
	updates := map[string]any{
		"state": idempotency.StateCompleted, "result_status": result.Status,
		"response_status": result.ResponseStatus, "completed_at": normalizedManagerTime(completion.CompletedAt),
		"error_code": nil, "resource_type": nil, "resource_id": nil,
		"resulting_session_id": nil, "trace_id": nil,
	}
	if result.ErrorCode != "" {
		updates["error_code"] = result.ErrorCode
	}
	if result.Resource != nil {
		updates["resource_type"] = result.Resource.Type
		updates["resource_id"] = result.Resource.ID
	}
	if result.ResultingSessionID != "" {
		updates["resulting_session_id"] = result.ResultingSessionID
	}
	if result.TraceID != "" {
		updates["trace_id"] = result.TraceID
	}
	return updates
}

func reservationFromManagerRow(row managerIdempotency, fresh bool) (idempotency.Reservation, error) {
	reservation := idempotency.Reservation{
		ID: row.ID, ActorType: row.ActorType, ActorID: row.ActorID, Operation: row.Operation, Key: row.Key,
		RequestHash: row.RequestHash, State: row.State, Fresh: fresh,
		CreatedAt: row.CreatedAt.UTC(), CompletedAt: cloneManagerTime(row.CompletedAt), ExpiresAt: row.ExpiresAt.UTC(),
	}
	if row.State == idempotency.StateInProgress {
		if row.ResultStatus != nil || row.CompletedAt != nil {
			return idempotency.Reservation{}, managerFailure("decode idempotency key", nil)
		}
		return reservation, nil
	}
	if row.State != idempotency.StateCompleted || row.ResultStatus == nil || row.ResponseStatus == nil || row.CompletedAt == nil {
		return idempotency.Reservation{}, managerFailure("decode idempotency key", nil)
	}
	result := idempotency.Result{
		Status: *row.ResultStatus, ResponseStatus: *row.ResponseStatus,
		ErrorCode: managerStringValue(row.ErrorCode), ResultingSessionID: managerStringValue(row.ResultingSessionID),
		TraceID: managerStringValue(row.TraceID), CompletedAt: row.CompletedAt.UTC(),
	}
	if row.ResourceType != nil || row.ResourceID != nil {
		if row.ResourceType == nil || row.ResourceID == nil {
			return idempotency.Reservation{}, managerFailure("decode idempotency key", nil)
		}
		result.Resource = &idempotency.Resource{Type: *row.ResourceType, ID: *row.ResourceID}
	}
	reservation.Result = &result
	return reservation, nil
}

func loadManagerMutationReplay(tx *gorm.DB, reservationID uint64, expectedKind string) (managerMutationReplay, error) {
	var row managerAuditLog
	if err := tx.Select("metadata").Where("audit_log_id = ?", managerAuditIDForReservation(reservationID)).Take(&row).Error; err != nil {
		return managerMutationReplay{}, managerFailure("load idempotent manager result", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(row.Metadata))
	decoder.DisallowUnknownFields()
	var replay managerMutationReplay
	if err := decoder.Decode(&replay); err != nil || replay.Kind != expectedKind {
		return managerMutationReplay{}, managerFailure("decode idempotent manager result", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return managerMutationReplay{}, managerFailure("decode idempotent manager result", err)
	}
	return replay, nil
}

func managerRequestHash(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte{byte(len(part) >> 24), byte(len(part) >> 16), byte(len(part) >> 8), byte(len(part))})
		_, _ = digest.Write([]byte(part))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func cloneManagerRevoke(value auth.SessionRevokeResult) auth.SessionRevokeResult {
	value.SessionID = cloneManagerAuthString(value.SessionID)
	return value
}

func managerRevokePointer(value auth.SessionRevokeResult) *auth.SessionRevokeResult {
	cloned := cloneManagerRevoke(value)
	return &cloned
}

func escapeManagerAuthLike(value string) string {
	value = strings.ReplaceAll(value, "=", "==")
	value = strings.ReplaceAll(value, "%", "=%")
	return strings.ReplaceAll(value, "_", "=_")
}

func managerUserFilterFingerprint(query auth.UserListQuery) string {
	enabled := ""
	if query.Enabled != nil {
		enabled = strconv.FormatBool(*query.Enabled)
	}
	return managerRequestHash("users", strings.ToLower(strings.TrimSpace(query.Query)), string(query.Role), enabled)
}

func managerSessionFilterFingerprint(query auth.SessionListQuery) string {
	current := ""
	if query.Current != nil {
		current = strconv.FormatBool(*query.Current)
	}
	return managerRequestHash("sessions", query.UserID, string(query.Status), current, query.CurrentSessionID)
}

func managerAuditFilterFingerprint(query audit.ListQuery) string {
	actions := normalizedManagerStrings(query.Actions)
	targetTypes := normalizedManagerStrings(query.TargetTypes)
	from, to := "", ""
	if query.From != nil {
		from = normalizedManagerTime(*query.From).Format(time.RFC3339Nano)
	}
	if query.To != nil {
		to = normalizedManagerTime(*query.To).Format(time.RFC3339Nano)
	}
	parts := []string{"audit", query.ActorUserID, string(query.ActorType), query.TargetID, string(query.Result), from, to}
	parts = append(parts, actions...)
	parts = append(parts, "\x00targets")
	parts = append(parts, targetTypes...)
	return managerRequestHash(parts...)
}

func normalizedManagerStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func encodeManagerAuthCursor(kind string, at time.Time, id, fingerprint string) (string, error) {
	cursor := managerAuthCursor{Version: 1, Kind: kind, TimeMillis: normalizedManagerTime(at).UnixMilli(), ID: id, Fingerprint: fingerprint}
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", managerFailure("encode manager cursor", err)
	}
	value := base64.RawURLEncoding.EncodeToString(encoded)
	if len(value) > 256 {
		return "", managerFailure("encode manager cursor", nil)
	}
	return value, nil
}

func decodeManagerAuthCursor(value, kind, fingerprint string) (managerAuthCursor, error) {
	if value == "" || len(value) > 256 {
		return managerAuthCursor{}, errors.New("invalid manager cursor")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return managerAuthCursor{}, errors.New("invalid manager cursor")
	}
	decoder := json.NewDecoder(bytes.NewReader(decoded))
	decoder.DisallowUnknownFields()
	var cursor managerAuthCursor
	if err := decoder.Decode(&cursor); err != nil || cursor.Version != 1 || cursor.Kind != kind ||
		!sameManagerHash(cursor.Fingerprint, fingerprint) || cursor.ID == "" || len(cursor.ID) > 256 || cursor.TimeMillis <= 0 {
		return managerAuthCursor{}, errors.New("invalid manager cursor")
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return managerAuthCursor{}, errors.New("invalid manager cursor")
	}
	return cursor, nil
}

func (cursor managerAuthCursor) time() time.Time { return time.UnixMilli(cursor.TimeMillis).UTC() }
