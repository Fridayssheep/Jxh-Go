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
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestMutatingRouteRejectsUntrustedOriginAndCSRF(t *testing.T) {
	router := newHTTPFixture(t)
	router.HandleFunc(http.MethodPost, "/api/admin/v1/test", RouteOptions{
		Mutation: true, CSRF: true, Permission: auth.PermissionSettingsWrite,
	}, func(w http.ResponseWriter, _ *http.Request) { WriteJSON(w, http.StatusOK, map[string]bool{"ok": true}) })

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/test", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	setManagerOrigin(request)
	request.Header.Set("Origin", "https://attacker.example")
	request.Header.Set("X-CSRF-Token", "valid-csrf-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeOriginForbidden)

	request = httptest.NewRequest(http.MethodPost, "/api/admin/v1/test", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	setManagerOrigin(request)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeCSRFInvalid)
}

func TestMutatingRouteDerivesAllowedOriginFromRequest(t *testing.T) {
	router, err := NewRouter(MiddlewareOptions{
		MaxBodyBytes: 64, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 512)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.HandleFunc(http.MethodPost, "/api/admin/v1/test", RouteOptions{
		Public: true, Mutation: true,
	}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name           string
		host           string
		forwardedProto string
		forwardedHost  string
		origin         string
		wantStatus     int
	}{
		{name: "LAN HTTP", host: "192.168.2.6:8080", forwardedProto: "http", origin: "http://192.168.2.6:8080", wantStatus: http.StatusNoContent},
		{name: "HTTPS hostname", host: "manager.example", forwardedProto: "https", origin: "https://manager.example", wantStatus: http.StatusNoContent},
		{name: "default HTTPS port", host: "manager.example:443", forwardedProto: "https", origin: "https://manager.example", wantStatus: http.StatusNoContent},
		{name: "IPv6 LAN HTTP", host: "[fd00::1]:8080", forwardedProto: "http", origin: "http://[fd00::1]:8080", wantStatus: http.StatusNoContent},
		{name: "forwarded host ignored", host: "manager.example", forwardedProto: "https", forwardedHost: "attacker.example", origin: "https://manager.example", wantStatus: http.StatusNoContent},
		{name: "different host", host: "192.168.2.6:8080", forwardedProto: "http", origin: "http://attacker.example", wantStatus: http.StatusForbidden},
		{name: "different port", host: "192.168.2.6:8080", forwardedProto: "http", origin: "http://192.168.2.6:8081", wantStatus: http.StatusForbidden},
		{name: "different scheme", host: "manager.example", forwardedProto: "https", origin: "http://manager.example", wantStatus: http.StatusForbidden},
		{name: "missing forwarded scheme", host: "manager.example", origin: "https://manager.example", wantStatus: http.StatusForbidden},
		{name: "uppercase forwarded scheme", host: "manager.example", forwardedProto: "HTTPS", origin: "https://manager.example", wantStatus: http.StatusForbidden},
		{name: "forwarded scheme chain", host: "manager.example", forwardedProto: "https,http", origin: "https://manager.example", wantStatus: http.StatusForbidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/test", nil)
			request.Host = test.host
			request.Header.Set("Origin", test.origin)
			if test.forwardedProto != "" {
				request.Header.Set("X-Forwarded-Proto", test.forwardedProto)
			}
			if test.forwardedHost != "" {
				request.Header.Set("X-Forwarded-Host", test.forwardedHost)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
}

func TestMutatingRouteRejectsOriginBeforeAuthentication(t *testing.T) {
	authenticator := &countingAuthenticator{}
	router, err := NewRouter(MiddlewareOptions{
		MaxBodyBytes: 64, Random: bytes.NewReader(bytes.Repeat([]byte{1}, 512)), Authenticator: authenticator,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.HandleFunc(http.MethodPost, "/api/admin/v1/test", RouteOptions{
		Mutation: true,
	}, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/test", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	setManagerOrigin(request)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertErrorCode(t, response, http.StatusForbidden, CodeOriginForbidden)
	if authenticator.calls != 0 {
		t.Fatalf("authentication calls=%d want=0", authenticator.calls)
	}
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
	setManagerOrigin(request)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusRequestEntityTooLarge, CodePayloadTooLarge)

	request = httptest.NewRequest(http.MethodPost, "/api/admin/v1/login", strings.NewReader(`{"ok":true}`))
	request.Header.Set("Content-Type", "text/plain")
	setManagerOrigin(request)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusUnsupportedMediaType, CodeUnsupportedMediaType)
}

func TestAdminMiddlewareRejectsExcessConcurrencyWithoutBlocking(t *testing.T) {
	router, err := NewRouter(MiddlewareOptions{
		MaxBodyBytes: 64, MaxConcurrentRequests: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	if err := router.HandleFunc(http.MethodGet, "/limited", RouteOptions{Public: true}, func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusNoContent)
	}); err != nil {
		t.Fatal(err)
	}

	firstDone := make(chan int, 1)
	go func() {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/limited", nil))
		firstDone <- response.Code
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach handler")
	}

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/limited", nil))
	assertErrorCode(t, response, http.StatusServiceUnavailable, CodeServerBusy)
	if response.Header().Get("Retry-After") != "1" || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("headers=%v", response.Header())
	}
	var envelope ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil || !envelope.Error.Retryable {
		t.Fatalf("busy response=%+v error=%v", envelope, err)
	}

	close(release)
	select {
	case status := <-firstDone:
		if status != http.StatusNoContent {
			t.Fatalf("first request status=%d", status)
		}
	case <-time.After(time.Second):
		t.Fatal("first request did not complete after release")
	}
}

func TestAdminMiddlewareUsesTrustedProxyChain(t *testing.T) {
	router, err := NewRouter(MiddlewareOptions{
		TrustedProxies: []string{"10.0.0.0/8"}, MaxBodyBytes: 64,
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
		MaxBodyBytes: 64,
		Random:       bytes.NewReader(bytes.Repeat([]byte{1}, 256)), Logger: log.New(&logs, "", 0), Authenticator: authenticator,
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
		MaxBodyBytes: 64,
		Random:       bytes.NewReader(bytes.Repeat([]byte{1}, 4096)), Logger: logger, Authenticator: testAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func setManagerOrigin(request *http.Request) {
	request.Host = "manager.example"
	request.Header.Set("Origin", "https://manager.example")
	request.Header.Set("X-Forwarded-Proto", "https")
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
		User:        auth.User{ID: "usr_1", Role: role, Enabled: true},
		Session:     auth.Session{ID: "ses_1", UserID: "usr_1", Status: auth.SessionStatusActive},
		Permissions: auth.PermissionsFor(role), CSRFToken: "valid-csrf-token",
	}, nil
}

type countingAuthenticator struct {
	calls int
}

func (a *countingAuthenticator) Authenticate(context.Context, string) (auth.AuthContext, error) {
	a.calls++
	return auth.AuthContext{}, auth.ErrUnauthenticated
}

func (a testAuthenticator) AuthenticatePassive(ctx context.Context, credential string) (auth.AuthContext, error) {
	return a.Authenticate(ctx, credential)
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
