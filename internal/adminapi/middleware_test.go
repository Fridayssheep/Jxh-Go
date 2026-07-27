package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjutjh/jxh-go/internal/auth"
)

func TestMutatingRouteRejectsUntrustedOriginAndCSRF(t *testing.T) {
	router := newHTTPFixture(t)
	router.HandleFunc(http.MethodPost, "/api/admin/v1/test", RouteOptions{
		Mutation: true, CSRF: true, Permission: auth.PermissionSettingsWrite,
	}, func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, http.StatusOK, map[string]bool{"ok": true}) })

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/test", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-CSRF-Token", "valid-csrf-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeOriginForbidden)

	request = httptest.NewRequest(http.MethodPost, "/api/admin/v1/test", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	request.Header.Set("Origin", "https://manager.example")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeCSRFInvalid)
}

func TestAdminMiddlewareIgnoresInboundRequestIDAndSetsSecurityHeaders(t *testing.T) {
	router := newHTTPFixture(t)
	router.HandleFunc(http.MethodGet, "/api/admin/v1/test", RouteOptions{Public: true}, func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"request_id": RequestIDFromContext(r.Context())})
	})
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/test", nil)
	request.Header.Set("X-Request-ID", "attacker-controlled")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("X-Request-ID") == "attacker-controlled" ||
		response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestAdminMiddlewareRejectsOversizedAndWrongContentType(t *testing.T) {
	router := newHTTPFixture(t)
	router.HandleFunc(http.MethodPost, "/api/admin/v1/login", RouteOptions{Public: true, Mutation: true}, func(w http.ResponseWriter, r *http.Request) {
		var value map[string]any
		if err := DecodeJSON(r, &value); errors.Is(err, ErrPayloadTooLarge) {
			WriteError(w, r, http.StatusRequestEntityTooLarge, CodePayloadTooLarge, "请求体过大", nil, false)
			return
		}
		WriteJSON(w, http.StatusOK, value)
	})

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/login", strings.NewReader(strings.Repeat("x", 65)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://manager.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusRequestEntityTooLarge, CodePayloadTooLarge)

	request = httptest.NewRequest(http.MethodPost, "/api/admin/v1/login", strings.NewReader(`{"ok":true}`))
	request.Header.Set("Content-Type", "text/plain")
	request.Header.Set("Origin", "https://manager.example")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType)
}

func TestAdminMiddlewareUsesTrustedProxyChain(t *testing.T) {
	router, err := NewRouter(MiddlewareOptions{
		PublicOrigin: "https://manager.example", TrustedProxies: []string{"10.0.0.0/8"}, MaxBodyBytes: 64,
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 256)), Authenticator: testAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	router.HandleFunc(http.MethodGet, "/ip", RouteOptions{Public: true}, func(w http.ResponseWriter, r *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]string{"ip": ClientIPFromContext(r.Context())})
	})
	request := httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.RemoteAddr = "10.0.0.2:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.7, 10.0.0.1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), "198.51.100.7") {
		t.Fatalf("body=%s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.RemoteAddr = "192.0.2.9:1234"
	request.Header.Set("X-Forwarded-For", "198.51.100.7")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if !strings.Contains(response.Body.String(), "192.0.2.9") {
		t.Fatalf("untrusted peer body=%s", response.Body.String())
	}
}

func TestAdminRouterUsesJSONForNotFoundMethodAndPanic(t *testing.T) {
	var logs strings.Builder
	router := newHTTPFixtureWithLogger(t, log.New(&logs, "", 0))
	router.HandleFunc(http.MethodGet, "/panic", RouteOptions{Public: true}, func(http.ResponseWriter, *http.Request) {
		panic("sensitive panic value")
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/missing", nil))
	assertErrorCode(t, response, http.StatusNotFound, CodeNotFound)

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/panic", nil))
	assertErrorCode(t, response, http.StatusMethodNotAllowed, CodeMethodNotAllowed)

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic", nil))
	assertErrorCode(t, response, http.StatusInternalServerError, CodeInternal)
	if strings.Contains(logs.String(), "sensitive panic value") || strings.Contains(response.Body.String(), "sensitive panic value") {
		t.Fatalf("panic leaked logs=%q body=%q", logs.String(), response.Body.String())
	}
}

func TestAdminPermissionStopsHandler(t *testing.T) {
	router := newHTTPFixture(t)
	called := false
	router.HandleFunc(http.MethodGet, "/super", RouteOptions{Permission: auth.PermissionUsersManage}, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodGet, "/super", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if called {
		t.Fatal("forbidden request reached handler")
	}
}

func TestAdminAuthenticationInfrastructureFailureReturns503WithoutRotationFallback(t *testing.T) {
	var logs strings.Builder
	authenticator := &failingReplacementAuthenticator{}
	router, err := NewRouter(MiddlewareOptions{
		PublicOrigin: "https://manager.example", MaxBodyBytes: 64,
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 256)), Logger: log.New(&logs, "", 0), Authenticator: authenticator,
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	if err := router.HandleFunc(http.MethodGet, "/protected", RouteOptions{AllowReplacedAuth: true}, func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/protected", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertErrorCode(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
	if called || authenticator.replacementCalls != 0 || response.Header().Get("Retry-After") != "3" {
		t.Fatalf("called=%t replacement_calls=%d headers=%v", called, authenticator.replacementCalls, response.Header())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || !envelope.Error.Retryable {
		t.Fatalf("authentication outage response = %+v, error=%v", envelope, err)
	}
	if strings.Contains(response.Body.String(), "database password") || strings.Contains(logs.String(), "database password") {
		t.Fatalf("authentication failure leaked details: logs=%q body=%q", logs.String(), response.Body.String())
	}
}

func TestDecodeJSONRejectsUnknownFieldsAndTrailingValues(t *testing.T) {
	type requestBody struct {
		Name string `json:"name"`
	}
	for _, body := range []string{`{"name":"ok","extra":true}`, `{"name":"ok"} {"name":"again"}`, ``} {
		request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var value requestBody
		if err := DecodeJSON(request, &value); !errors.Is(err, ErrInvalidJSON) {
			t.Fatalf("DecodeJSON(%q) error=%v", body, err)
		}
	}
}

func newHTTPFixture(t *testing.T) *Router {
	t.Helper()
	return newHTTPFixtureWithLogger(t, nil)
}

func newHTTPFixtureWithLogger(t *testing.T, logger *log.Logger) *Router {
	t.Helper()
	router, err := NewRouter(MiddlewareOptions{
		PublicOrigin: "https://manager.example", MaxBodyBytes: 64,
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 4096)), Logger: logger, Authenticator: testAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

type testAuthenticator struct{}

func (testAuthenticator) Authenticate(_ context.Context, credential string) (auth.AuthContext, error) {
	role := auth.RoleSuperAdmin
	if credential == "observer" {
		role = auth.RoleObserver
	} else if credential != "credential" {
		return auth.AuthContext{}, auth.ErrUnauthenticated
	}
	return auth.AuthContext{
		User: auth.User{ID: "usr_1", Role: role}, Session: auth.Session{ID: "ses_1", UserID: "usr_1"},
		Permissions: auth.PermissionsFor(role), CSRFToken: "valid-csrf-token",
	}, nil
}

type failingReplacementAuthenticator struct {
	replacementCalls int
}

func (*failingReplacementAuthenticator) Authenticate(context.Context, string) (auth.AuthContext, error) {
	return auth.AuthContext{}, errors.New("database password must not escape")
}

func (a *failingReplacementAuthenticator) AuthenticateForRotation(context.Context, string) (auth.AuthContext, error) {
	a.replacementCalls++
	return auth.AuthContext{}, auth.ErrUnauthenticated
}

func assertErrorCode(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body: %v body=%s", err, response.Body.String())
	}
	if envelope.Error.Code != code || envelope.Error.RequestID == "" {
		t.Fatalf("error=%+v", envelope.Error)
	}
}
