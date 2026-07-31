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
	platformconfig "github.com/zjutjh/jxh-go/internal/platform/config"
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

func TestSystemConfigurationHTTPReturnsStructuredEffectiveValuesAndAppliedVersion(t *testing.T) {
	service := &fakeSystemOperations{configuration: managersystem.Configuration{
		WPS: platformconfig.WPSSettings{
			ShareURL: platformconfig.SecretState{Configured: true, Source: platformconfig.SourceFile},
			SID:      platformconfig.SecretState{Configured: true, Source: platformconfig.SourceEnvironment}, Sheet: "release", TimeoutSec: 120,
		},
		AI: platformconfig.AISettings{Provider: "openai", BaseURL: "https://api.example.test/v1",
			APIKey: platformconfig.SecretState{Configured: true, Source: platformconfig.SourceFile}, Model: "model", TimeoutSec: 30, MaxQuestionChars: 500},
		Quote:                platformconfig.QuoteSettings{BaseURL: "http://quote:5000", TimeoutSec: 10},
		Time:                 platformconfig.TimeSettings{AppTimezone: "Asia/Shanghai", SchedulerTimezone: "Asia/Shanghai"},
		Retention:            platformconfig.RetentionSettings{TriggerLogRetentionDays: 180},
		EnvironmentOverrides: []string{"wps.sid"}, Version: 7, AppliedVersion: 6, RestartRequired: true, RestartSupported: true,
	}}
	router := newSystemHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/system/configuration", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"7"` ||
		!strings.Contains(response.Body.String(), `"applied_version":6`) ||
		!strings.Contains(response.Body.String(), `"share_url":{"configured":true,"source":"file"}`) ||
		strings.Contains(response.Body.String(), `"yaml"`) || strings.Contains(response.Body.String(), "api-key-secret") {
		t.Fatalf("status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
}

func TestUpdateConfigurationPassesPatchVersionAndMutationContext(t *testing.T) {
	service := &fakeSystemOperations{configuration: managersystem.Configuration{Version: 8, AppliedVersion: 7}}
	router := newSystemHTTPFixture(t, service)

	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/system/configuration", `{"wps":{"sheet":"next"},"ai":{"timeout_sec":45}}`)
	request.Header.Set("If-Match", `"7"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.configurationUpdates != 1 || service.expectedVersion != 7 || service.request.RequestID == "" ||
		service.patch.WPS == nil || service.patch.WPS.Sheet == nil || *service.patch.WPS.Sheet != "next" ||
		service.patch.AI == nil || service.patch.AI.TimeoutSec == nil || *service.patch.AI.TimeoutSec != 45 {
		t.Fatalf("status=%d calls=%d version=%d patch=%#v context=%#v body=%s", response.Code, service.configurationUpdates, service.expectedVersion, service.patch, service.request, response.Body.String())
	}
}

func TestConfigurationHTTPNeverAcceptsRawYAMLOrDeploymentFields(t *testing.T) {
	service := &fakeSystemOperations{}
	router := newSystemHTTPFixture(t, service)
	for _, body := range []string{
		`{"yaml":"admin:\n  addr: 0.0.0.0:8090\n"}`,
		`{"admin":{"addr":"0.0.0.0:8090"}}`,
		`{"wps":{"cache_file":"/tmp/override"}}`,
		`{"ai":{"timeout_sec":45,"unknown":true}}`,
	} {
		request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/system/configuration", body)
		request.Header.Set("If-Match", `"7"`)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.configurationUpdates != 0 {
		t.Fatal("invalid configuration request reached service")
	}
}

func TestSystemConfigurationHTTPRequiresVersionAndMapsEditorErrors(t *testing.T) {
	service := &fakeSystemOperations{}
	router := newSystemHTTPFixture(t, service)

	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/system/configuration", `{"time":{"app_timezone":"Asia/Shanghai"}}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)
	if service.configurationUpdates != 0 {
		t.Fatal("missing If-Match reached the service")
	}

	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{managersystem.ErrInvalidInput, http.StatusBadRequest, CodeBadRequest},
		{&managersystem.ConfigurationManagedFieldsError{Fields: []string{"ai.model"}}, http.StatusConflict, "configuration_field_managed_externally"},
		{managersystem.ErrConfigurationVersionConflict, http.StatusConflict, "resource_version_conflict"},
		{managersystem.ErrConfigurationUnavailable, http.StatusServiceUnavailable, "dependency_unavailable"},
	} {
		service.err = test.err
		request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/system/configuration", `{"time":{"app_timezone":"Asia/Shanghai"}}`)
		request.Header.Set("If-Match", `"7"`)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, test.status, test.code)
	}
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
	health               managersystem.Health
	operation            managersystem.Operation
	err                  error
	restartCalls         int
	request              auth.MutationContext
	configuration        managersystem.Configuration
	configurationUpdates int
	expectedVersion      uint64
	patch                platformconfig.SettingsPatch
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

func (s *fakeSystemOperations) Configuration(context.Context, auth.Principal) (managersystem.Configuration, error) {
	return s.configuration, s.err
}

func (s *fakeSystemOperations) UpdateConfiguration(_ context.Context, _ auth.Principal, version uint64, patch platformconfig.SettingsPatch, request auth.MutationContext) (managersystem.Configuration, error) {
	s.configurationUpdates++
	s.expectedVersion = version
	s.patch = patch
	s.request = request
	return s.configuration, s.err
}
