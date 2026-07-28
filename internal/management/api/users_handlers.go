package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

type AdminUserService interface {
	CreateUser(ctx context.Context, principal auth.Principal, input auth.CreateUserInput, request auth.MutationContext) (auth.User, error)
	GetUser(ctx context.Context, principal auth.Principal, userID string) (auth.User, error)
	ListUsers(ctx context.Context, principal auth.Principal, query auth.UserListQuery) (auth.UserPage, error)
	UpdateUser(ctx context.Context, principal auth.Principal, userID string, expectedRevision uint64, patch auth.UserPatch, request auth.MutationContext) (auth.User, error)
	ResetPassword(ctx context.Context, principal auth.Principal, userID string, expectedRevision uint64, newPassword, idempotencyKey string, request ...auth.MutationContext) (auth.PasswordResetResult, error)
	RevokeUserSessions(ctx context.Context, principal auth.Principal, userID, idempotencyKey string, request auth.MutationContext) (auth.SessionRevokeResult, error)
	ListSessions(ctx context.Context, principal auth.Principal, query auth.SessionListQuery) (auth.SessionPage, error)
	RevokeSession(ctx context.Context, principal auth.Principal, sessionID, idempotencyKey string, request auth.MutationContext) (auth.SessionRevokeResult, error)
}

type SessionEventSink interface {
	Publish(draft events.Draft) (events.Event, error)
	CloseSession(sessionID string)
	CloseUser(userID string)
}

type UsersHandlers struct {
	service      AdminUserService
	events       SessionEventSink
	cookieSecure bool
}

func NewUsersHandlers(service AdminUserService, eventSink SessionEventSink, cookieSecure bool) (*UsersHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("admin user service is required")
	}
	return &UsersHandlers{service: service, events: eventSink, cookieSecure: cookieSecure}, nil
}

func (h *UsersHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/api/admin/v1/users", RouteOptions{Permission: auth.PermissionUsersManage}, h.listUsers},
		{http.MethodPost, "/api/admin/v1/users", mutationRoute(auth.PermissionUsersManage), h.createUser},
		{http.MethodGet, "/api/admin/v1/users/{user_id}", RouteOptions{Permission: auth.PermissionUsersManage}, h.getUser},
		{http.MethodPatch, "/api/admin/v1/users/{user_id}", mutationRoute(auth.PermissionUsersManage), h.updateUser},
		{http.MethodPost, "/api/admin/v1/users/{user_id}/password-reset", mutationRoute(auth.PermissionUsersManage), h.resetPassword},
		{http.MethodPost, "/api/admin/v1/users/{user_id}/sessions/revoke", mutationRoute(auth.PermissionSessionsManage), h.revokeUserSessions},
		{http.MethodGet, "/api/admin/v1/sessions", RouteOptions{Permission: auth.PermissionSessionsManage}, h.listSessions},
		{http.MethodPost, "/api/admin/v1/sessions/{session_id}/revoke", mutationRoute(auth.PermissionSessionsManage), h.revokeSession},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func mutationRoute(permission auth.Permission) RouteOptions {
	return RouteOptions{Mutation: true, CSRF: true, Permission: permission}
}

type adminUserCreateRequest struct {
	Username    string    `json:"username"`
	DisplayName string    `json:"display_name"`
	Role        auth.Role `json:"role"`
	QQUserID    *string   `json:"qq_user_id"`
	Password    string    `json:"password"`
}

func (h *UsersHandlers) createUser(w http.ResponseWriter, r *http.Request) {
	var body adminUserCreateRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	user, err := h.service.CreateUser(r.Context(), principalFromAuth(identity), auth.CreateUserInput{
		Username: body.Username, DisplayName: body.DisplayName, Role: body.Role, QQUserID: body.QQUserID, Password: body.Password,
	}, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, user.Version)
	writeJSON(w, http.StatusCreated, mapAdminUser(user))
}

func (h *UsersHandlers) getUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathIdentifier(w, r, "user_id")
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	user, err := h.service.GetUser(r.Context(), principalFromAuth(identity), userID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, user.Version)
	writeJSON(w, http.StatusOK, mapAdminUser(user))
}

func (h *UsersHandlers) listUsers(w http.ResponseWriter, r *http.Request) {
	query, err := parseUserListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "账号查询参数无效", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.ListUsers(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]adminUserDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapAdminUser(page.Items[index])
	}
	writeJSON(w, http.StatusOK, adminUserListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

type nullableStringPatch struct {
	Set   bool
	Value *string
}

func (p *nullableStringPatch) UnmarshalJSON(data []byte) error {
	p.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		p.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	p.Value = &value
	return nil
}

type adminUserPatchRequest struct {
	DisplayName *string             `json:"display_name"`
	Role        *auth.Role          `json:"role"`
	QQUserID    nullableStringPatch `json:"qq_user_id"`
	Enabled     *bool               `json:"enabled"`
}

func (h *UsersHandlers) updateUser(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathIdentifier(w, r, "user_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	var body adminUserPatchRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	patch := auth.UserPatch{QQUserID: auth.Field[*string]{Set: body.QQUserID.Set, Value: body.QQUserID.Value}}
	if body.DisplayName != nil {
		patch.DisplayName = auth.Field[string]{Set: true, Value: *body.DisplayName}
	}
	if body.Role != nil {
		patch.Role = auth.Field[auth.Role]{Set: true, Value: *body.Role}
	}
	if body.Enabled != nil {
		patch.Enabled = auth.Field[bool]{Set: true, Value: *body.Enabled}
	}
	identity, _ := AuthFromContext(r.Context())
	user, err := h.service.UpdateUser(r.Context(), principalFromAuth(identity), userID, revision, patch, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if h.events != nil {
		h.events.CloseUser(user.ID)
	}
	setRevisionETag(w, user.Version)
	writeJSON(w, http.StatusOK, mapAdminUser(user))
}

type passwordResetRequest struct {
	NewPassword string `json:"new_password"`
}

func (h *UsersHandlers) resetPassword(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathIdentifier(w, r, "user_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	var body passwordResetRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.ResetPassword(r.Context(), principalFromAuth(identity), userID, revision, body.NewPassword, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if userID == identity.User.ID {
		h.clearSessionCookie(w)
	}
	if h.events != nil {
		h.events.CloseUser(userID)
	}
	writeJSON(w, http.StatusOK, passwordResetDTO{
		User: mapAdminUser(result.User), RevokedSessionCount: result.RevokedSessionCount, CompletedAt: result.CompletedAt.UTC(),
	})
}

func (h *UsersHandlers) revokeUserSessions(w http.ResponseWriter, r *http.Request) {
	userID, ok := pathIdentifier(w, r, "user_id")
	if !ok {
		return
	}
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.RevokeUserSessions(r.Context(), principalFromAuth(identity), userID, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if userID == identity.User.ID {
		h.clearSessionCookie(w)
	}
	h.publishRevocation(result)
	writeJSON(w, http.StatusOK, mapSessionRevoke(result))
}

func (h *UsersHandlers) listSessions(w http.ResponseWriter, r *http.Request) {
	query, err := parseSessionListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "会话查询参数无效", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	query.CurrentSessionID = identity.Session.ID
	page, err := h.service.ListSessions(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]adminSessionDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapAdminSession(page.Items[index])
	}
	writeJSON(w, http.StatusOK, adminSessionListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

func (h *UsersHandlers) revokeSession(w http.ResponseWriter, r *http.Request) {
	sessionID, ok := pathIdentifier(w, r, "session_id")
	if !ok {
		return
	}
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.RevokeSession(r.Context(), principalFromAuth(identity), sessionID, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	if sessionID == identity.Session.ID {
		h.clearSessionCookie(w)
	}
	h.publishRevocation(result)
	writeJSON(w, http.StatusOK, mapSessionRevoke(result))
}

func (h *UsersHandlers) publishRevocation(result auth.SessionRevokeResult) {
	if h.events == nil || result.RevokedCount == 0 {
		return
	}
	if result.SessionID == nil {
		h.events.CloseUser(result.UserID)
		return
	}
	resourceID := *result.SessionID
	_, _ = h.events.Publish(events.Draft{
		Type: events.EventAuthSessionRevoked, OccurredAt: result.RevokedAt,
		Resource: &events.Resource{Type: events.ResourceSession, ID: resourceID}, Reason: "revoked",
	})
	h.events.CloseSession(resourceID)
}

func (h *UsersHandlers) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: SessionCookieName, Value: "", Path: "/api/admin/v1", MaxAge: -1, Expires: time.Unix(1, 0),
		HttpOnly: true, Secure: h.cookieSecure, SameSite: http.SameSiteStrictMode,
	})
}

func (h *UsersHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrAdminForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "没有管理账号或会话的权限", nil, false)
	case errors.Is(err, auth.ErrInvalidAdminInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "请求参数无效", nil, false)
	case errors.Is(err, auth.ErrAdminUserNotFound), errors.Is(err, auth.ErrAdminSessionNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "账号或会话不存在", nil, false)
	case errors.Is(err, auth.ErrAdminRevisionConflict), errors.Is(err, auth.ErrLastSuperAdmin),
		errors.Is(err, auth.ErrAdminIdentityConflict), errors.Is(err, auth.ErrAdminIdempotencyReuse):
		writeAPIError(w, r, http.StatusConflict, CodeConflict, "资源状态冲突", nil, false)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "服务器内部错误", nil, false)
	}
}

func decodeRequestJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	if err := DecodeJSON(r, target); err != nil {
		if errors.Is(err, ErrPayloadTooLarge) {
			writeAPIError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "请求体过大", nil, false)
		} else {
			writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "JSON 请求体无效", nil, false)
		}
		return false
	}
	return true
}

func pathIdentifier(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	value, err := ValidateOpaqueID(r.PathValue(name))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "资源标识无效", nil, false)
		return "", false
	}
	return value, true
}

func requiredRevision(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	value, err := ParseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		writeAPIError(w, r, http.StatusPreconditionRequired, CodePreconditionRequired, "请求必须携带有效的 If-Match", nil, false)
		return 0, false
	}
	return value, true
}

func requiredIdempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	value, err := ParseIdempotencyKey(r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "Idempotency-Key 无效", nil, false)
		return "", false
	}
	return value, true
}

func mutationContextFromRequest(r *http.Request) auth.MutationContext {
	return auth.MutationContext{
		RequestID: RequestIDFromContext(r.Context()), IPAddress: ClientIPFromContext(r.Context()), UserAgent: r.UserAgent(),
	}
}

func parseUserListQuery(values url.Values) (auth.UserListQuery, error) {
	if !validSingleQueryKeys(values, "query", "role", "enabled", "cursor", "limit") {
		return auth.UserListQuery{}, auth.ErrInvalidAdminInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return auth.UserListQuery{}, err
	}
	query := auth.UserListQuery{Query: values.Get("query"), Role: auth.Role(values.Get("role")), Cursor: values.Get("cursor"), Limit: limit}
	if query.Role != "" {
		if _, err := auth.ParseRole(string(query.Role)); err != nil {
			return auth.UserListQuery{}, err
		}
	}
	if value := values.Get("enabled"); value != "" {
		parsed, err := parseStrictBool(value)
		if err != nil {
			return auth.UserListQuery{}, err
		}
		query.Enabled = &parsed
	}
	return query, nil
}

func parseSessionListQuery(values url.Values) (auth.SessionListQuery, error) {
	if !validSingleQueryKeys(values, "user_id", "status", "current", "cursor", "limit") {
		return auth.SessionListQuery{}, auth.ErrInvalidAdminInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return auth.SessionListQuery{}, err
	}
	query := auth.SessionListQuery{
		UserID: values.Get("user_id"), Status: auth.SessionStatus(values.Get("status")), Cursor: values.Get("cursor"), Limit: limit,
	}
	if query.Status != "" && query.Status != auth.SessionStatusActive && query.Status != auth.SessionStatusExpired && query.Status != auth.SessionStatusRevoked {
		return auth.SessionListQuery{}, auth.ErrInvalidAdminInput
	}
	if value := values.Get("current"); value != "" {
		parsed, err := parseStrictBool(value)
		if err != nil {
			return auth.SessionListQuery{}, err
		}
		query.Current = &parsed
	}
	return query, nil
}

func validSingleQueryKeys(values url.Values, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		set[key] = struct{}{}
	}
	for key, entries := range values {
		if _, ok := set[key]; !ok || len(entries) != 1 {
			return false
		}
	}
	return true
}

func parseStrictBool(value string) (bool, error) {
	if value != "true" && value != "false" {
		return false, auth.ErrInvalidAdminInput
	}
	return strconv.ParseBool(value)
}

func setRevisionETag(w http.ResponseWriter, revision uint64) {
	w.Header().Set("ETag", strconv.Quote(strconv.FormatUint(revision, 10)))
}

type adminUserDTO struct {
	ID          string     `json:"user_id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name"`
	Role        auth.Role  `json:"role"`
	QQUserID    *string    `json:"qq_user_id"`
	Enabled     bool       `json:"enabled"`
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Version     uint64     `json:"version"`
}

type adminSessionDTO struct {
	ID         string             `json:"session_id"`
	UserID     string             `json:"user_id"`
	Status     auth.SessionStatus `json:"status"`
	Current    bool               `json:"current"`
	IPAddress  string             `json:"ip_address"`
	UserAgent  string             `json:"user_agent"`
	CreatedAt  time.Time          `json:"created_at"`
	LastSeenAt time.Time          `json:"last_seen_at"`
	ExpiresAt  time.Time          `json:"expires_at"`
	RevokedAt  *time.Time         `json:"revoked_at"`
}

type adminUserListDTO struct {
	Items      []adminUserDTO `json:"items"`
	NextCursor *string        `json:"next_cursor"`
	HasMore    bool           `json:"has_more"`
}

type adminSessionListDTO struct {
	Items      []adminSessionDTO `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

type passwordResetDTO struct {
	User                adminUserDTO `json:"user"`
	RevokedSessionCount int          `json:"revoked_session_count"`
	CompletedAt         time.Time    `json:"completed_at"`
}

type sessionRevokeDTO struct {
	UserID       string    `json:"user_id"`
	SessionID    *string   `json:"session_id"`
	RevokedCount int       `json:"revoked_count"`
	RevokedAt    time.Time `json:"revoked_at"`
}

func mapAdminUser(value auth.User) adminUserDTO {
	return adminUserDTO{
		ID: value.ID, Username: value.Username, DisplayName: value.DisplayName, Role: value.Role, QQUserID: value.QQUserID,
		Enabled: value.Enabled, LastLoginAt: utcTimePointer(value.LastLoginAt), CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC(), Version: value.Version,
	}
}

func mapAdminSession(value auth.Session) adminSessionDTO {
	return adminSessionDTO{
		ID: value.ID, UserID: value.UserID, Status: value.Status, Current: value.Current,
		IPAddress: value.IPAddress, UserAgent: value.UserAgent, CreatedAt: value.CreatedAt.UTC(), LastSeenAt: value.LastSeenAt.UTC(),
		ExpiresAt: value.ExpiresAt.UTC(), RevokedAt: utcTimePointer(value.RevokedAt),
	}
}

func mapSessionRevoke(value auth.SessionRevokeResult) sessionRevokeDTO {
	return sessionRevokeDTO{
		UserID: value.UserID, SessionID: value.SessionID, RevokedCount: value.RevokedCount, RevokedAt: value.RevokedAt.UTC(),
	}
}

func utcTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}
