package adminapi

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

func TestLoginSetsStrictCookieWithoutReturningToken(t *testing.T) {
	service := newFakeAuthOperations()
	router := newAuthHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"root-admin","password":"valid-password-123"}`))
	request.Header.Set("Content-Type", "application/json")
	setManagerOrigin(request)
	request.RemoteAddr = "192.0.2.5:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Path != "/api/admin/v1" {
		t.Fatalf("cookies=%+v", cookies)
	}
	if strings.Contains(response.Body.String(), "session_token") || strings.Contains(response.Body.String(), service.loginResult.SessionToken) {
		t.Fatalf("session token leaked: %s", response.Body.String())
	}
	if service.loginRequest.ClientIP != "192.0.2.5" || service.loginRequest.UserAgent != "" {
		t.Fatalf("request=%+v", service.loginRequest)
	}
}

func TestLoginCookieSecurityFollowsValidatedOrigin(t *testing.T) {
	for _, test := range []struct {
		name   string
		scheme string
		secure bool
	}{
		{name: "HTTP", scheme: "http", secure: false},
		{name: "HTTPS", scheme: "https", secure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := newFakeAuthOperations()
			router := newAuthHTTPFixture(t, service)
			request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"root-admin","password":"valid-password-123"}`))
			request.Host = "manager.example"
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", test.scheme+"://manager.example")
			request.Header.Set("X-Forwarded-Proto", test.scheme)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			cookies := response.Result().Cookies()
			if len(cookies) != 1 || cookies[0].Secure != test.secure {
				t.Fatalf("cookies=%+v want_secure=%t", cookies, test.secure)
			}
		})
	}
}

func TestCurrentAdminUsesAuthenticatedContext(t *testing.T) {
	service := newFakeAuthOperations()
	router := newAuthHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/auth/me", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"csrf_token":"valid-csrf-token"`) || !strings.Contains(response.Body.String(), `"username":"root-admin"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestLogoutRequiresCSRFAndClearsCookie(t *testing.T) {
	service := newFakeAuthOperations()
	router := newAuthHTTPFixture(t, service)
	request := authMutationRequest(http.MethodPost, "/api/admin/v1/auth/logout", "")
	request.Header.Del("X-CSRF-Token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeCSRFInvalid)
	if service.logoutCalls != 0 {
		t.Fatal("invalid CSRF reached logout service")
	}

	request = authMutationRequest(http.MethodPost, "/api/admin/v1/auth/logout", "")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || service.logoutCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.logoutCalls, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge >= 0 {
		t.Fatalf("cookies=%+v", cookies)
	}
}

func TestChangePasswordAllowsReplacedCookieReplayAndRotatesCookie(t *testing.T) {
	service := newFakeAuthOperations()
	service.normalAuthErr = auth.ErrUnauthenticated
	router := newAuthHTTPFixture(t, service)
	request := authMutationRequest(http.MethodPost, "/api/admin/v1/auth/change-password", `{"current_password":"current-password","new_password":"new-password-123"}`)
	request.Header.Set("Idempotency-Key", "idem-change-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.replacedAuthCalls != 1 || service.changeCalls != 1 {
		t.Fatalf("status=%d replaced=%d change=%d body=%s", response.Code, service.replacedAuthCalls, service.changeCalls, response.Body.String())
	}
	if got := response.Result().Cookies(); len(got) != 1 || got[0].Value != service.loginResult.SessionToken {
		t.Fatalf("cookies=%+v", got)
	}
}

func TestChangePasswordClosesEverySubscriptionForUser(t *testing.T) {
	service := newFakeAuthOperations()
	hub := newSSETestHub(t)
	subscription, _, err := hub.Subscribe(t.Context(), events.SubscribeOptions{
		AllowedTopics: []events.Topic{events.TopicGroups}, SessionID: "ses_other", UserID: "usr_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	router := newAuthHTTPFixtureWithEvents(t, service, hub)
	request := authMutationRequest(http.MethodPost, "/api/admin/v1/auth/change-password", `{"current_password":"current-password","new_password":"new-password-123"}`)
	request.Header.Set("Idempotency-Key", "idem-change-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case <-subscription.Done():
	case <-time.After(time.Second):
		t.Fatal("user subscription remained open after password change")
	}
}

func TestLoginUsesUniformSafeErrors(t *testing.T) {
	for _, failure := range []error{auth.ErrInvalidCredentials, errors.New("database leaked secret")} {
		service := newFakeAuthOperations()
		service.err = failure
		router := newAuthHTTPFixture(t, service)
		request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(`{"username":"root-admin","password":"valid-password-123"}`))
		request.Header.Set("Content-Type", "application/json")
		setManagerOrigin(request)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if strings.Contains(response.Body.String(), "database leaked secret") || strings.Contains(response.Body.String(), "valid-password-123") {
			t.Fatalf("sensitive error leaked: %s", response.Body.String())
		}
	}
}

func TestLoginValidatesContractBeforeService(t *testing.T) {
	for _, body := range []string{
		`{"username":"Bad Name","password":"valid-password-123"}`,
		`{"username":"root-admin","password":"short"}`,
		`{"username":"root-admin","password":"valid-password-123","unknown":true}`,
	} {
		service := newFakeAuthOperations()
		router := newAuthHTTPFixture(t, service)
		request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/auth/login", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		setManagerOrigin(request)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
		if service.loginRequest.Username != "" {
			t.Fatalf("invalid input reached login service: %+v", service.loginRequest)
		}
	}
}

func newAuthHTTPFixture(t *testing.T, service *fakeAuthOperations) *Router {
	return newAuthHTTPFixtureWithEvents(t, service, nil)
}

func newAuthHTTPFixtureWithEvents(t *testing.T, service *fakeAuthOperations, eventSink SessionEventSink) *Router {
	t.Helper()
	router, err := NewRouter(MiddlewareOptions{
		MaxBodyBytes: 1 << 20,
		Random:       bytes.NewReader(bytes.Repeat([]byte{1}, 4096)), Authenticator: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewAuthHandlers(service, eventSink)
	if err != nil {
		t.Fatal(err)
	}
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

func authMutationRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	setManagerOrigin(request)
	request.Header.Set("X-CSRF-Token", "valid-csrf-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

type fakeAuthOperations struct {
	identity          auth.AuthContext
	loginResult       auth.LoginResult
	loginRequest      auth.LoginRequest
	err               error
	normalAuthErr     error
	logoutCalls       int
	changeCalls       int
	replacedAuthCalls int
}

func newFakeAuthOperations() *fakeAuthOperations {
	user := auth.User{
		ID: "usr_1", Username: "root-admin", DisplayName: "Root", Role: auth.RoleSuperAdmin, Enabled: true,
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0), Version: 1,
	}
	session := auth.Session{
		ID: "ses_1", UserID: user.ID, Status: auth.SessionStatusActive, Current: true,
		CreatedAt: time.Unix(1, 0), LastSeenAt: time.Unix(1, 0), ExpiresAt: time.Unix(1000, 0), AbsoluteExpiresAt: time.Unix(2000, 0),
	}
	identity := auth.AuthContext{User: user, Session: session, Permissions: auth.PermissionsFor(user.Role), CSRFToken: "valid-csrf-token"}
	return &fakeAuthOperations{identity: identity, loginResult: auth.LoginResult{AuthContext: identity, SessionToken: "new-session-credential"}}
}

func (s *fakeAuthOperations) Authenticate(context.Context, string) (auth.AuthContext, error) {
	if s.normalAuthErr != nil {
		return auth.AuthContext{}, s.normalAuthErr
	}
	return s.identity, nil
}

func (s *fakeAuthOperations) AuthenticatePassive(context.Context, string) (auth.AuthContext, error) {
	if s.normalAuthErr != nil {
		return auth.AuthContext{}, s.normalAuthErr
	}
	return s.identity, nil
}

func (s *fakeAuthOperations) AuthenticateForRotation(context.Context, string) (auth.AuthContext, error) {
	s.replacedAuthCalls++
	return s.identity, nil
}

func (s *fakeAuthOperations) Login(_ context.Context, request auth.LoginRequest) (auth.LoginResult, error) {
	s.loginRequest = request
	return s.loginResult, s.err
}

func (s *fakeAuthOperations) Logout(context.Context, string, auth.AuthContext, auth.MutationContext) error {
	s.logoutCalls++
	return s.err
}

func (s *fakeAuthOperations) ChangePassword(context.Context, auth.AuthContext, auth.ChangePasswordInput) (auth.LoginResult, error) {
	s.changeCalls++
	return s.loginResult, s.err
}
