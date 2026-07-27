package auth

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrAdminForbidden        = errors.New("admin operation forbidden")
	ErrInvalidAdminInput     = errors.New("invalid admin input")
	ErrAdminUserNotFound     = errors.New("admin user not found")
	ErrAdminSessionNotFound  = errors.New("admin session not found")
	ErrAdminRevisionConflict = errors.New("admin user revision conflict")
	ErrLastSuperAdmin        = errors.New("cannot modify the last enabled super admin")
	ErrAdminIdentityConflict = errors.New("admin identity conflict")
	ErrAdminIdempotencyReuse = errors.New("admin idempotency key reused")
)

var adminUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)

type MutationContext struct {
	RequestID string
	IPAddress string
	UserAgent string
}

type CreateUserInput struct {
	Username    string
	DisplayName string
	Role        Role
	QQUserID    *string
	Password    string
}

type UserPatch struct {
	DisplayName Field[string]
	Role        Field[Role]
	QQUserID    Field[*string]
	Enabled     Field[bool]
}

type UserListQuery struct {
	Query   string
	Role    Role
	Enabled *bool
	Cursor  string
	Limit   int
}

type UserPage struct {
	Items      []User
	NextCursor string
	HasMore    bool
}

type SessionListQuery struct {
	UserID  string
	Status  SessionStatus
	Current *bool
	Cursor  string
	Limit   int
}

type SessionPage struct {
	Items      []Session
	NextCursor string
	HasMore    bool
}

type CreateUserMutation struct {
	Actor        Principal
	Context      MutationContext
	Username     string
	DisplayName  string
	Role         Role
	QQUserID     *string
	PasswordHash string
	OccurredAt   time.Time
}

type UpdateUserMutation struct {
	Actor            Principal
	Context          MutationContext
	UserID           string
	ExpectedRevision uint64
	Patch            UserPatch
	OccurredAt       time.Time
}

type ResetPasswordMutation struct {
	Actor            Principal
	Context          MutationContext
	UserID           string
	ExpectedRevision uint64
	PasswordHash     string
	IdempotencyKey   string
	OccurredAt       time.Time
}

type PasswordResetResult struct {
	User                User
	RevokedSessionCount int
	CompletedAt         time.Time
}

type RevokeSessionsMutation struct {
	Actor          Principal
	Context        MutationContext
	UserID         string
	SessionID      string
	IdempotencyKey string
	OccurredAt     time.Time
}

type SessionRevokeResult struct {
	UserID       string
	SessionID    *string
	RevokedCount int
	RevokedAt    time.Time
}

// AdminStore owns the transaction that applies each mutation, writes its audit
// record, and completes an idempotency reservation when one is supplied.
type AdminStore interface {
	CreateAdminUser(ctx context.Context, mutation CreateUserMutation) (User, error)
	GetAdminUser(ctx context.Context, userID string) (User, bool, error)
	ListAdminUsers(ctx context.Context, query UserListQuery) (UserPage, error)
	UpdateAdminUser(ctx context.Context, mutation UpdateUserMutation) (User, error)
	ResetAdminUserPassword(ctx context.Context, mutation ResetPasswordMutation) (PasswordResetResult, error)
	RevokeAdminUserSessions(ctx context.Context, mutation RevokeSessionsMutation) (SessionRevokeResult, error)
	ListAdminSessions(ctx context.Context, query SessionListQuery) (SessionPage, error)
	RevokeAdminSession(ctx context.Context, mutation RevokeSessionsMutation) (SessionRevokeResult, error)
}

type AdminServiceOptions struct {
	Store     AdminStore
	Passwords PasswordEngine
	Now       func() time.Time
}

type AdminService struct {
	store     AdminStore
	passwords PasswordEngine
	now       func() time.Time
}

func NewAdminService(options AdminServiceOptions) (*AdminService, error) {
	if options.Store == nil || options.Passwords == nil || options.Now == nil {
		return nil, ErrInvalidAuthConfig
	}
	return &AdminService{store: options.Store, passwords: options.Passwords, now: options.Now}, nil
}

func (s *AdminService) CreateUser(ctx context.Context, principal Principal, input CreateUserInput, request MutationContext) (User, error) {
	if !principal.Has(PermissionUsersManage) {
		return User{}, ErrAdminForbidden
	}
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	if !validCreateUser(input) || !validMutationContext(request) {
		return User{}, ErrInvalidAdminInput
	}
	password := []byte(input.Password)
	defer clear(password)
	passwordHash, err := s.passwords.Hash(password)
	if err != nil {
		return User{}, fmt.Errorf("hash admin password: %w", err)
	}
	user, err := s.store.CreateAdminUser(ctx, CreateUserMutation{
		Actor: principal, Context: request, Username: input.Username, DisplayName: input.DisplayName,
		Role: input.Role, QQUserID: cloneStringPointer(input.QQUserID), PasswordHash: passwordHash, OccurredAt: s.now(),
	})
	if err != nil {
		return User{}, fmt.Errorf("create admin user: %w", err)
	}
	return cloneUser(user), nil
}

func (s *AdminService) GetUser(ctx context.Context, principal Principal, userID string) (User, error) {
	if !principal.Has(PermissionUsersManage) {
		return User{}, ErrAdminForbidden
	}
	if !validAdminText(userID, 256) {
		return User{}, ErrInvalidAdminInput
	}
	user, found, err := s.store.GetAdminUser(ctx, userID)
	if err != nil {
		return User{}, fmt.Errorf("get admin user: %w", err)
	}
	if !found {
		return User{}, ErrAdminUserNotFound
	}
	return cloneUser(user), nil
}

func (s *AdminService) ListUsers(ctx context.Context, principal Principal, query UserListQuery) (UserPage, error) {
	if !principal.Has(PermissionUsersManage) {
		return UserPage{}, ErrAdminForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validUserListQuery(query) {
		return UserPage{}, ErrInvalidAdminInput
	}
	page, err := s.store.ListAdminUsers(ctx, query)
	if err != nil {
		return UserPage{}, fmt.Errorf("list admin users: %w", err)
	}
	page.Items = cloneUsers(page.Items)
	return page, nil
}

func (s *AdminService) UpdateUser(ctx context.Context, principal Principal, userID string, expectedRevision uint64, patch UserPatch, request MutationContext) (User, error) {
	if !principal.Has(PermissionUsersManage) {
		return User{}, ErrAdminForbidden
	}
	if !validAdminText(userID, 256) || expectedRevision == 0 || !validUserPatch(patch) || !validMutationContext(request) {
		return User{}, ErrInvalidAdminInput
	}
	user, err := s.store.UpdateAdminUser(ctx, UpdateUserMutation{
		Actor: principal, Context: request, UserID: userID, ExpectedRevision: expectedRevision,
		Patch: cloneUserPatch(patch), OccurredAt: s.now(),
	})
	if err != nil {
		return User{}, fmt.Errorf("update admin user: %w", err)
	}
	return cloneUser(user), nil
}

func (s *AdminService) ResetPassword(ctx context.Context, principal Principal, userID string, expectedRevision uint64, newPassword, idempotencyKey string, request ...MutationContext) (PasswordResetResult, error) {
	if !principal.Has(PermissionUsersManage) {
		return PasswordResetResult{}, ErrAdminForbidden
	}
	mutationContext := firstMutationContext(request)
	if !validAdminText(userID, 256) || expectedRevision == 0 || !validPassword(newPassword) ||
		!validIdempotencyKey(idempotencyKey) || !validMutationContext(mutationContext) {
		return PasswordResetResult{}, ErrInvalidAdminInput
	}
	password := []byte(newPassword)
	defer clear(password)
	passwordHash, err := s.passwords.Hash(password)
	if err != nil {
		return PasswordResetResult{}, fmt.Errorf("hash reset password: %w", err)
	}
	result, err := s.store.ResetAdminUserPassword(ctx, ResetPasswordMutation{
		Actor: principal, Context: mutationContext, UserID: userID, ExpectedRevision: expectedRevision,
		PasswordHash: passwordHash, IdempotencyKey: idempotencyKey, OccurredAt: s.now(),
	})
	if err != nil {
		return PasswordResetResult{}, fmt.Errorf("reset admin password: %w", err)
	}
	result.User = cloneUser(result.User)
	return result, nil
}

func (s *AdminService) RevokeUserSessions(ctx context.Context, principal Principal, userID, idempotencyKey string, request MutationContext) (SessionRevokeResult, error) {
	if !principal.Has(PermissionSessionsManage) {
		return SessionRevokeResult{}, ErrAdminForbidden
	}
	if !validAdminText(userID, 256) || !validIdempotencyKey(idempotencyKey) || !validMutationContext(request) {
		return SessionRevokeResult{}, ErrInvalidAdminInput
	}
	result, err := s.store.RevokeAdminUserSessions(ctx, RevokeSessionsMutation{
		Actor: principal, Context: request, UserID: userID, IdempotencyKey: idempotencyKey, OccurredAt: s.now(),
	})
	if err != nil {
		return SessionRevokeResult{}, fmt.Errorf("revoke admin user sessions: %w", err)
	}
	return cloneSessionRevokeResult(result), nil
}

func (s *AdminService) ListSessions(ctx context.Context, principal Principal, query SessionListQuery) (SessionPage, error) {
	if !principal.Has(PermissionSessionsManage) {
		return SessionPage{}, ErrAdminForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validSessionListQuery(query) {
		return SessionPage{}, ErrInvalidAdminInput
	}
	page, err := s.store.ListAdminSessions(ctx, query)
	if err != nil {
		return SessionPage{}, fmt.Errorf("list admin sessions: %w", err)
	}
	page.Items = append([]Session(nil), page.Items...)
	for index := range page.Items {
		page.Items[index].Current = page.Items[index].ID == principal.SessionID
	}
	return page, nil
}

func (s *AdminService) RevokeSession(ctx context.Context, principal Principal, sessionID, idempotencyKey string, request MutationContext) (SessionRevokeResult, error) {
	if !principal.Has(PermissionSessionsManage) {
		return SessionRevokeResult{}, ErrAdminForbidden
	}
	if !validAdminText(sessionID, 256) || !validIdempotencyKey(idempotencyKey) || !validMutationContext(request) {
		return SessionRevokeResult{}, ErrInvalidAdminInput
	}
	result, err := s.store.RevokeAdminSession(ctx, RevokeSessionsMutation{
		Actor: principal, Context: request, SessionID: sessionID, IdempotencyKey: idempotencyKey, OccurredAt: s.now(),
	})
	if err != nil {
		return SessionRevokeResult{}, fmt.Errorf("revoke admin session: %w", err)
	}
	return cloneSessionRevokeResult(result), nil
}

func validCreateUser(input CreateUserInput) bool {
	return adminUsernamePattern.MatchString(input.Username) && validAdminText(input.DisplayName, 64) &&
		validRole(input.Role) && validOptionalQQ(input.QQUserID) && validPassword(input.Password)
}

func validUserPatch(patch UserPatch) bool {
	if !patch.DisplayName.Set && !patch.Role.Set && !patch.QQUserID.Set && !patch.Enabled.Set {
		return false
	}
	return (!patch.DisplayName.Set || validAdminText(patch.DisplayName.Value, 64)) &&
		(!patch.Role.Set || validRole(patch.Role.Value)) &&
		(!patch.QQUserID.Set || validOptionalQQ(patch.QQUserID.Value))
}

func validUserListQuery(query UserListQuery) bool {
	return query.Limit >= 1 && query.Limit <= 100 &&
		(query.Query == "" || validAdminText(query.Query, 100)) &&
		(query.Role == "" || validRole(query.Role)) &&
		(query.Cursor == "" || validAdminText(query.Cursor, 256))
}

func validSessionListQuery(query SessionListQuery) bool {
	return query.Limit >= 1 && query.Limit <= 100 &&
		(query.UserID == "" || validAdminText(query.UserID, 256)) &&
		(query.Cursor == "" || validAdminText(query.Cursor, 256)) &&
		(query.Status == "" || query.Status == SessionStatusActive || query.Status == SessionStatusExpired || query.Status == SessionStatusRevoked)
}

func validRole(role Role) bool {
	return role == RoleSuperAdmin || role == RoleMaintainer || role == RoleObserver
}

func validOptionalQQ(value *string) bool {
	if value == nil {
		return true
	}
	if !validAdminText(*value, 32) {
		return false
	}
	for _, char := range *value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validPassword(password string) bool {
	return utf8.ValidString(password) && utf8.RuneCountInString(password) >= 12 && utf8.RuneCountInString(password) <= 128
}

func validIdempotencyKey(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || strings.ContainsRune("._:-", char)) {
			return false
		}
	}
	return true
}

func validMutationContext(value MutationContext) bool {
	return (value.RequestID == "" || validAdminText(value.RequestID, 256)) &&
		(value.IPAddress == "" || validAdminText(value.IPAddress, 64)) &&
		(value.UserAgent == "" || validAdminText(value.UserAgent, 300))
}

func validAdminText(value string, maxRunes int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func firstMutationContext(values []MutationContext) MutationContext {
	if len(values) == 0 {
		return MutationContext{}
	}
	return values[0]
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUser(value User) User {
	value.QQUserID = cloneStringPointer(value.QQUserID)
	return value
}

func cloneUsers(values []User) []User {
	result := make([]User, len(values))
	for index := range values {
		result[index] = cloneUser(values[index])
	}
	return result
}

func cloneUserPatch(value UserPatch) UserPatch {
	value.QQUserID.Value = cloneStringPointer(value.QQUserID.Value)
	return value
}

func cloneSessionRevokeResult(value SessionRevokeResult) SessionRevokeResult {
	value.SessionID = cloneStringPointer(value.SessionID)
	return value
}
