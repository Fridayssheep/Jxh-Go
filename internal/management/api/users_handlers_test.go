package adminapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

func TestCreateAdminUserReturnsContractDTOWithoutPassword(t *testing.T) {
	service := &fakeAdminUserService{user: testAdminUser()}
	router := newUsersHTTPFixture(t, service, nil)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/users", `{"username":"alice","display_name":"Alice","role":"maintainer","password":"valid-password-123"}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"2"` || strings.Contains(response.Body.String(), "password") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if service.create.Username != "alice" || service.mutation.RequestID == "" {
		t.Fatalf("input=%+v mutation=%+v", service.create, service.mutation)
	}
}

func TestUpdateUserRequiresSuperAdminAndIfMatch(t *testing.T) {
	service := &fakeAdminUserService{user: testAdminUser()}
	router := newUsersHTTPFixture(t, service, nil)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/users/usr_2", `{"enabled":false}`)
	request.Header.Del("If-Match")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)
	if service.calls != 0 {
		t.Fatal("missing If-Match reached service")
	}

	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/users/usr_2", `{"qq_user_id":null,"enabled":false}`)
	request.Header.Set("If-Match", `"4"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !service.patch.QQUserID.Set || service.patch.QQUserID.Value != nil || service.revision != 4 {
		t.Fatalf("status=%d patch=%+v revision=%d body=%s", response.Code, service.patch, service.revision, response.Body.String())
	}
}

func TestResetOwnPasswordClearsStrictCookie(t *testing.T) {
	service := &fakeAdminUserService{reset: auth.PasswordResetResult{
		User: testAdminUser(), RevokedSessionCount: 2, CompletedAt: time.Unix(100, 0),
	}}
	router := newUsersHTTPFixture(t, service, nil)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/users/usr_1/password-reset", `{"new_password":"new-valid-password"}`)
	request.Header.Set("If-Match", `"2"`)
	request.Header.Set("Idempotency-Key", "idem-reset-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName || cookies[0].MaxAge >= 0 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookies=%+v", cookies)
	}
}

func TestRevokeSessionPublishesAndClosesSubscription(t *testing.T) {
	hub := newSSETestHub(t)
	sessionID := "ses_1"
	service := &fakeAdminUserService{revoke: auth.SessionRevokeResult{
		UserID: "usr_1", SessionID: &sessionID, RevokedCount: 1, RevokedAt: time.Unix(100, 0),
	}}
	subscription, _, err := hub.Subscribe(t.Context(), events.SubscribeOptions{AllowedTopics: []events.Topic{events.TopicAuth}, SessionID: sessionID})
	if err != nil {
		t.Fatal(err)
	}
	router := newUsersHTTPFixture(t, service, hub)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/sessions/ses_1/revoke", "")
	request.Header.Set("Idempotency-Key", "idem-revoke-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	select {
	case event := <-subscription.Events():
		if event.Type != events.EventAuthSessionRevoked {
			t.Fatalf("event=%+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("revocation event not published")
	}
	select {
	case <-subscription.Done():
	case <-time.After(time.Second):
		t.Fatal("revoked session subscription remained open")
	}
}

func TestUserSecurityMutationsCloseAllUserSubscriptions(t *testing.T) {
	for _, operation := range []string{"update", "password_reset", "bulk_revoke"} {
		t.Run(operation, func(t *testing.T) {
			hub := newSSETestHub(t)
			subscription, _, err := hub.Subscribe(t.Context(), events.SubscribeOptions{
				AllowedTopics: []events.Topic{events.TopicGroups}, SessionID: "ses_target", UserID: "usr_1",
			})
			if err != nil {
				t.Fatal(err)
			}
			service := &fakeAdminUserService{user: testAdminUser()}
			var request *http.Request
			switch operation {
			case "update":
				request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/users/usr_1", `{"role":"observer"}`)
				request.Header.Set("If-Match", `"2"`)
			case "password_reset":
				service.reset = auth.PasswordResetResult{User: testAdminUser(), RevokedSessionCount: 2, CompletedAt: time.Unix(100, 0)}
				request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/users/usr_1/password-reset", `{"new_password":"new-valid-password"}`)
				request.Header.Set("If-Match", `"2"`)
				request.Header.Set("Idempotency-Key", "idem-reset-1")
			case "bulk_revoke":
				service.revoke = auth.SessionRevokeResult{UserID: "usr_1", RevokedCount: 2, RevokedAt: time.Unix(100, 0)}
				request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/users/usr_1/sessions/revoke", "")
				request.Header.Set("Idempotency-Key", "idem-revoke-1")
			}
			router := newUsersHTTPFixture(t, service, hub)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			select {
			case <-subscription.Done():
			case <-time.After(time.Second):
				t.Fatal("user subscription remained open after security mutation")
			}
		})
	}
}

func TestUserAndSessionQueriesRejectInvalidValuesBeforeService(t *testing.T) {
	service := &fakeAdminUserService{}
	router := newUsersHTTPFixture(t, service, nil)
	for _, target := range []string{
		"/api/admin/v1/users?enabled=1", "/api/admin/v1/users?role=unknown", "/api/admin/v1/users?unknown=x",
		"/api/admin/v1/sessions?current=yes", "/api/admin/v1/sessions?status=unknown", "/api/admin/v1/sessions?limit=101",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != 0 {
		t.Fatalf("invalid queries reached service %d times", service.calls)
	}
}

func TestSessionCurrentFilterUsesAuthenticatedSession(t *testing.T) {
	current := true
	service := &fakeAdminUserService{}
	router := newUsersHTTPFixture(t, service, nil)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/sessions?current=true", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.sessionQuery.Current == nil || *service.sessionQuery.Current != current ||
		service.sessionQuery.CurrentSessionID != "ses_1" {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.sessionQuery, response.Body.String())
	}
}

func newUsersHTTPFixture(t *testing.T, service AdminUserService, sink SessionEventSink) *Router {
	t.Helper()
	handlers, err := NewUsersHandlers(service, sink)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(MiddlewareOptions{
		MaxBodyBytes: 1 << 20,
		Random:       bytes.NewReader(bytes.Repeat([]byte{1}, 4096)), Authenticator: testAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

func userMutationRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	setManagerOrigin(request)
	request.Header.Set("X-CSRF-Token", "valid-csrf-token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func testAdminUser() auth.User {
	return auth.User{
		ID: "usr_1", Username: "alice", DisplayName: "Alice", Role: auth.RoleMaintainer, Enabled: true,
		CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(2, 0), Version: 2,
	}
}

type fakeAdminUserService struct {
	user         auth.User
	userPage     auth.UserPage
	sessions     auth.SessionPage
	reset        auth.PasswordResetResult
	revoke       auth.SessionRevokeResult
	err          error
	calls        int
	create       auth.CreateUserInput
	patch        auth.UserPatch
	revision     uint64
	mutation     auth.MutationContext
	sessionQuery auth.SessionListQuery
}

func (s *fakeAdminUserService) CreateUser(_ context.Context, _ auth.Principal, input auth.CreateUserInput, request auth.MutationContext) (auth.User, error) {
	s.calls++
	s.create, s.mutation = input, request
	return s.user, s.err
}

func (s *fakeAdminUserService) GetUser(context.Context, auth.Principal, string) (auth.User, error) {
	s.calls++
	return s.user, s.err
}

func (s *fakeAdminUserService) ListUsers(context.Context, auth.Principal, auth.UserListQuery) (auth.UserPage, error) {
	s.calls++
	return s.userPage, s.err
}

func (s *fakeAdminUserService) UpdateUser(_ context.Context, _ auth.Principal, _ string, revision uint64, patch auth.UserPatch, request auth.MutationContext) (auth.User, error) {
	s.calls++
	s.revision, s.patch, s.mutation = revision, patch, request
	return s.user, s.err
}

func (s *fakeAdminUserService) ResetPassword(_ context.Context, _ auth.Principal, _ string, revision uint64, _, _ string, request ...auth.MutationContext) (auth.PasswordResetResult, error) {
	s.calls++
	s.revision = revision
	if len(request) > 0 {
		s.mutation = request[0]
	}
	return s.reset, s.err
}

func (s *fakeAdminUserService) RevokeUserSessions(context.Context, auth.Principal, string, string, auth.MutationContext) (auth.SessionRevokeResult, error) {
	s.calls++
	return s.revoke, s.err
}

func (s *fakeAdminUserService) ListSessions(_ context.Context, _ auth.Principal, query auth.SessionListQuery) (auth.SessionPage, error) {
	s.calls++
	s.sessionQuery = query
	return s.sessions, s.err
}

func (s *fakeAdminUserService) RevokeSession(context.Context, auth.Principal, string, string, auth.MutationContext) (auth.SessionRevokeResult, error) {
	s.calls++
	return s.revoke, s.err
}
