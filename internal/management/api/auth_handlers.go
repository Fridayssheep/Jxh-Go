package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

type AuthOperations interface {
	Authenticator
	ReplacementAuthenticator
	StreamAuthenticator
	Login(ctx context.Context, request auth.LoginRequest) (auth.LoginResult, error)
	Logout(ctx context.Context, credential string, identity auth.AuthContext, request auth.MutationContext) error
	ChangePassword(ctx context.Context, identity auth.AuthContext, input auth.ChangePasswordInput) (auth.LoginResult, error)
}

type AuthHandlers struct {
	service AuthOperations
	events  SessionEventSink
}

var loginUsernamePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{2,31}$`)

func NewAuthHandlers(service AuthOperations, eventSink SessionEventSink) (*AuthHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("auth service is required")
	}
	return &AuthHandlers{service: service, events: eventSink}, nil
}

func (h *AuthHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodPost, "/api/admin/v1/auth/login", RouteOptions{Public: true, Mutation: true}, h.login},
		{http.MethodGet, "/api/admin/v1/auth/me", RouteOptions{}, h.me},
		{http.MethodPost, "/api/admin/v1/auth/logout", RouteOptions{Mutation: true, CSRF: true}, h.logout},
		{http.MethodPost, "/api/admin/v1/auth/change-password", RouteOptions{Mutation: true, CSRF: true, AllowReplacedAuth: true}, h.changePassword},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

type loginRequestDTO struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *AuthHandlers) login(w http.ResponseWriter, r *http.Request) {
	var body loginRequestDTO
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	passwordLength := utf8.RuneCountInString(body.Password)
	if !loginUsernamePattern.MatchString(body.Username) || !utf8.ValidString(body.Password) || passwordLength < 12 || passwordLength > 128 {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "登录参数无效", nil, false)
		return
	}
	priorCredential := ""
	if cookie, err := r.Cookie(SessionCookieName); err == nil {
		priorCredential = cookie.Value
	}
	result, err := h.service.Login(r.Context(), auth.LoginRequest{
		Username: body.Username, Password: body.Password, ClientIP: ClientIPFromContext(r.Context()),
		UserAgent: r.UserAgent(), PriorCredential: priorCredential,
	})
	if err != nil {
		h.writeServiceError(w, r, err, true)
		return
	}
	setSessionCookie(w, r, result.SessionToken)
	writeJSON(w, http.StatusOK, mapAuthContext(result.AuthContext))
}

func (h *AuthHandlers) me(w http.ResponseWriter, r *http.Request) {
	identity, ok := AuthFromContext(r.Context())
	if !ok {
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
		return
	}
	writeJSON(w, http.StatusOK, mapAuthContext(identity))
}

func (h *AuthHandlers) logout(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
		return
	}
	if err := h.service.Logout(r.Context(), cookie.Value, identity, mutationContextFromRequest(r)); err != nil {
		h.writeServiceError(w, r, err, false)
		return
	}
	clearSessionCookie(w, r)
	h.publishRevocation(identity.Session.ID, time.Now())
	w.WriteHeader(http.StatusNoContent)
}

type changePasswordRequestDTO struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *AuthHandlers) changePassword(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	var body changePasswordRequestDTO
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
		return
	}
	result, err := h.service.ChangePassword(r.Context(), identity, auth.ChangePasswordInput{
		Credential: cookie.Value, CurrentPassword: body.CurrentPassword, NewPassword: body.NewPassword,
		IdempotencyKey: idempotencyKey, Context: mutationContextFromRequest(r),
	})
	if err != nil {
		h.writeServiceError(w, r, err, false)
		return
	}
	setSessionCookie(w, r, result.SessionToken)
	h.publishRevocation(identity.Session.ID, result.Session.CreatedAt)
	if h.events != nil {
		h.events.CloseUser(identity.User.ID)
	}
	writeJSON(w, http.StatusOK, mapAuthContext(result.AuthContext))
}

func (h *AuthHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error, login bool) {
	switch {
	case errors.Is(err, auth.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeAPIError(w, r, http.StatusTooManyRequests, CodeRateLimited, "请求过于频繁，请稍后重试", nil, true)
	case errors.Is(err, auth.ErrAdminIdempotencyReuse):
		writeAPIError(w, r, http.StatusConflict, CodeConflict, "Idempotency-Key 已用于不同请求", nil, false)
	case errors.Is(err, auth.ErrInvalidCredentials):
		if login {
			writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "用户名或密码错误", nil, false)
		} else {
			writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "当前密码或新密码无效", nil, false)
		}
	case errors.Is(err, auth.ErrUnauthenticated):
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "登录状态无效或已过期", nil, false)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "服务器内部错误", nil, false)
	}
}

func (h *AuthHandlers) publishRevocation(sessionID string, occurredAt time.Time) {
	if h.events == nil || sessionID == "" {
		return
	}
	_, _ = h.events.Publish(events.Draft{
		Type: events.EventAuthSessionRevoked, OccurredAt: occurredAt,
		Resource: &events.Resource{Type: events.ResourceSession, ID: sessionID}, Reason: "revoked",
	})
	h.events.CloseSession(sessionID)
}

type authContextDTO struct {
	User        adminUserDTO      `json:"user"`
	Session     adminSessionDTO   `json:"session"`
	Permissions []auth.Permission `json:"permissions"`
	CSRFToken   string            `json:"csrf_token"`
}

func mapAuthContext(value auth.AuthContext) authContextDTO {
	return authContextDTO{
		User: mapAdminUser(value.User), Session: mapAdminSession(value.Session),
		Permissions: append([]auth.Permission(nil), value.Permissions...), CSRFToken: value.CSRFToken,
	}
}
