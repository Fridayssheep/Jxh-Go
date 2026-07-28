package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	managersystem "github.com/zjutjh/jxh-go/internal/management/system"
)

func TestSystemHealthMapsSnapshotWithoutSensitiveConfiguration(t *testing.T) {
	checkedAt := time.Unix(90, 0)
	latency := 25 * time.Millisecond
	service := &fakeSystemOperations{health: managersystem.Health{
		GeneratedAt: time.Unix(100, 0), Live: true, Ready: false,
		Dependencies: []managersystem.DependencyHealth{{
			Key: managersystem.DependencyMySQL, Status: managersystem.DependencyUnavailable, Configured: true, Required: true,
			Latency: &latency, LastCheckedAt: &checkedAt,
		}},
	}}
	router := newSystemHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/system/health", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"readiness":"unavailable"`) || !strings.Contains(response.Body.String(), `"latency_ms":25`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	for _, forbidden := range []string{"password", "dsn", "token"} {
		if strings.Contains(strings.ToLower(response.Body.String()), forbidden) {
			t.Fatalf("health leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestRestartHTTPReturnsAcceptedOperation(t *testing.T) {
	service := &fakeSystemOperations{operation: managersystem.Operation{
		ID: "op_1", Type: "napcat_restart", Status: managersystem.StatusAccepted, RequestedAt: time.Unix(100, 0),
	}}
	router := newSystemHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/system/napcat/restart", `{"confirmation":"restart","reason":"maintenance"}`)
	request.Header.Set("Idempotency-Key", "restart-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"status":"accepted"`) || service.restartCalls != 1 || service.request.RequestID == "" {
		t.Fatalf("status=%d calls=%d context=%+v body=%s", response.Code, service.restartCalls, service.request, response.Body.String())
	}
}

func TestRestartHTTPMapsUnavailableAndRejectsConfirmationBeforeService(t *testing.T) {
	service := &fakeSystemOperations{err: managersystem.ErrNapCatUnavailable}
	router := newSystemHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/system/napcat/restart", `{"confirmation":"wrong"}`)
	request.Header.Set("Idempotency-Key", "restart-key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	if service.restartCalls != 0 {
		t.Fatal("invalid confirmation reached service")
	}

	request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/system/napcat/restart", `{"confirmation":"restart"}`)
	request.Header.Set("Idempotency-Key", "restart-key")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
}

func newSystemHTTPFixture(t *testing.T, service SystemOperations) *Router {
	t.Helper()
	handlers, err := NewSystemHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router := newHTTPFixture(t)
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

type fakeSystemOperations struct {
	health       managersystem.Health
	operation    managersystem.Operation
	err          error
	restartCalls int
	request      auth.MutationContext
}

func (s *fakeSystemOperations) Health(context.Context, auth.Principal) (managersystem.Health, error) {
	return s.health, s.err
}

func (s *fakeSystemOperations) RestartNapCat(_ context.Context, _ auth.Principal, _ managersystem.RestartInput, _ string, request ...auth.MutationContext) (managersystem.Operation, error) {
	s.restartCalls++
	if len(request) > 0 {
		s.request = request[0]
	}
	return s.operation, s.err
}
