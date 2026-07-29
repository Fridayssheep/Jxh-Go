package adminapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/settings"
)

func TestSettingsGlobalRoutesMapContractAndPatchPresence(t *testing.T) {
	service := &settingsOperationsFake{global: globalSettingsFixture()}
	router := newSettingsHTTPFixture(t, service)
	request := settingsReadRequest(http.MethodGet, "/api/admin/v1/settings")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` ||
		!strings.Contains(response.Body.String(), `"message_template":"Welcome {{member_qq}}"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	service.global.Version = 4
	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/settings",
		`{"features":{"ai_qa":{"enabled":false},"welcome":{"enabled":false,"message_template":"Hi {{member_qq}}"}}}`)
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` || service.revision != 3 {
		t.Fatalf("status=%d revision=%d body=%s", response.Code, service.revision, response.Body.String())
	}
	if !service.globalPatch.AIQA.Set || service.globalPatch.AIQA.Value.Enabled.Value ||
		!service.globalPatch.Welcome.Value.MessageTemplate.Set || service.globalPatch.Welcome.Value.MessageTemplate.Value != "Hi {{member_qq}}" {
		t.Fatalf("patch=%+v", service.globalPatch)
	}
}

func TestSettingsGlobalRoutesReadAndPatchJoinRequestSettings(t *testing.T) {
	service := &settingsOperationsFake{global: globalSettingsFixture()}
	router := newSettingsHTTPFixture(t, service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, settingsReadRequest(http.MethodGet, "/api/admin/v1/settings"))
	if response.Code != http.StatusOK || !strings.Contains(
		response.Body.String(),
		`"join_requests":{"auto_reject_reason":"申请信息不完整或格式不符合要求，请完善后重新申请。"}`,
	) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}

	service.global.Version = 4
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/settings",
		`{"join_requests":{"auto_reject_reason":"请补充学号和姓名后重新申请。"}}`)
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !service.globalPatch.JoinRequests.Set ||
		service.globalPatch.JoinRequests.Value.AutoRejectReason.Value != "请补充学号和姓名后重新申请。" {
		t.Fatalf("status=%d patch=%+v body=%s", response.Code, service.globalPatch, response.Body.String())
	}
}

func TestSettingsGroupPatchPreservesFeatureAndNestedNullSemantics(t *testing.T) {
	service := &settingsOperationsFake{group: groupSettingsFixture()}
	router := newSettingsHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/groups/123/settings",
		`{"features":{"keyword_reply":null,"ai_qa":{"enabled":null},"welcome":{"enabled":null,"message_template":"Hi {{group_id}}"}}}`)
	request.Header.Set("If-Match", `"0"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.revision != 0 || service.groupID != "123" {
		t.Fatalf("status=%d revision=%d group=%q body=%s", response.Code, service.revision, service.groupID, response.Body.String())
	}
	patch := service.groupPatch
	if !patch.KeywordReply.Set || !patch.KeywordReply.Clear || patch.AIQA.Enabled.Value != nil ||
		!patch.Welcome.Enabled.Set || patch.Welcome.Enabled.Value != nil ||
		patch.Welcome.MessageTemplate.Value == nil || *patch.Welcome.MessageTemplate.Value != "Hi {{group_id}}" {
		t.Fatalf("patch=%+v", patch)
	}
}

func TestSettingsGroupReadUsesVersionZeroContract(t *testing.T) {
	service := &settingsOperationsFake{group: settings.Group{
		GroupID: "123", Effective: settings.DefaultFeatures(), GlobalVersion: 7, Version: 0,
	}}
	router := newSettingsHTTPFixture(t, service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, settingsReadRequest(http.MethodGet, "/api/admin/v1/groups/123/settings"))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"0"` ||
		!strings.Contains(response.Body.String(), `"overrides":{}`) ||
		!strings.Contains(response.Body.String(), `"updated_at":null`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestSettingsPayloadsRejectUnknownMissingAndInvalidNullFields(t *testing.T) {
	service := &settingsOperationsFake{global: globalSettingsFixture(), group: groupSettingsFixture()}
	router := newSettingsHTTPFixture(t, service)
	tests := []struct {
		target string
		body   string
	}{
		{target: "/api/admin/v1/settings", body: `{}`},
		{target: "/api/admin/v1/settings", body: `{"features":{}}`},
		{target: "/api/admin/v1/settings", body: `{"features":{"ai_qa":null}}`},
		{target: "/api/admin/v1/settings", body: `{"features":{"welcome":{}}}`},
		{target: "/api/admin/v1/settings", body: `{"features":{"keyword_reply":{"enabled":true,"extra":1}}}`},
		{target: "/api/admin/v1/groups/123/settings", body: `{"features":{"keyword_reply":{}}}`},
		{target: "/api/admin/v1/groups/123/settings", body: `{"features":{"welcome":{"extra":true}}}`},
	}
	for _, test := range tests {
		request := userMutationRequest(t, http.MethodPatch, test.target, test.body)
		request.Header.Set("If-Match", `"1"`)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != 0 {
		t.Fatalf("invalid settings payload reached service %d times", service.calls)
	}
}

func TestSettingsMutationsRequireRevisionAndMapConflicts(t *testing.T) {
	service := &settingsOperationsFake{global: globalSettingsFixture()}
	router := newSettingsHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/settings", `{"features":{"ai_qa":{"enabled":false}}}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)

	request = userMutationRequest(t, http.MethodDelete, "/api/admin/v1/groups/123/settings", "")
	request.Header.Set("If-Match", `"0"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)

	service.err = settings.ErrConflict
	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/settings", `{"features":{"ai_qa":{"enabled":false}}}`)
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusConflict, "resource_version_conflict")
}

func TestSettingsWriteRouteRejectsObserverBeforeService(t *testing.T) {
	service := &settingsOperationsFake{global: globalSettingsFixture()}
	router := newSettingsHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/settings", `{"features":{"ai_qa":{"enabled":false}}}`)
	request.Header.Set("If-Match", `"3"`)
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if service.calls != 0 {
		t.Fatal("observer request reached settings service")
	}
}

func newSettingsHTTPFixture(t *testing.T, service SettingsOperations) *Router {
	t.Helper()
	handlers, err := NewSettingsHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(MiddlewareOptions{
		PublicOrigin: "https://manager.example", MaxBodyBytes: 1 << 20,
		Random: bytes.NewReader(bytes.Repeat([]byte{1}, 4096)), Authenticator: testAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

func settingsReadRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	return request
}

func globalSettingsFixture() settings.Global {
	userID := "usr_1"
	features := settings.DefaultFeatures()
	features.Welcome.MessageTemplate = "Welcome {{member_qq}}"
	return settings.Global{
		Features: features, JoinRequests: settings.DefaultJoinRequestSettings(),
		Version: 3, UpdatedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
		UpdatedBy: &audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: "Admin"},
	}
}

func groupSettingsFixture() settings.Group {
	updatedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	overrides := settings.Overrides{KeywordReply: &settings.BasicOverride{Enabled: false}}
	effective := settings.DefaultFeatures()
	effective.KeywordReply.Enabled = false
	return settings.Group{
		GroupID: "123", Effective: effective, Overrides: overrides, GlobalVersion: 3,
		Version: 1, UpdatedAt: &updatedAt,
	}
}

type settingsOperationsFake struct {
	global      settings.Global
	group       settings.Group
	err         error
	calls       int
	revision    uint64
	groupID     string
	globalPatch settings.GlobalPatch
	groupPatch  settings.GroupPatch
}

func (s *settingsOperationsFake) GetGlobal(context.Context, auth.Principal) (settings.Global, error) {
	s.calls++
	return s.global, s.err
}

func (s *settingsOperationsFake) UpdateGlobal(_ context.Context, _ auth.Principal, revision uint64, patch settings.GlobalPatch, _ auth.MutationContext) (settings.Global, error) {
	s.calls++
	s.revision, s.globalPatch = revision, patch
	return s.global, s.err
}

func (s *settingsOperationsFake) GetGroup(_ context.Context, _ auth.Principal, groupID string) (settings.Group, error) {
	s.calls++
	s.groupID = groupID
	return s.group, s.err
}

func (s *settingsOperationsFake) UpdateGroup(_ context.Context, _ auth.Principal, groupID string, revision uint64, patch settings.GroupPatch, _ auth.MutationContext) (settings.Group, error) {
	s.calls++
	s.groupID, s.revision, s.groupPatch = groupID, revision, patch
	return s.group, s.err
}

func (s *settingsOperationsFake) DeleteGroup(_ context.Context, _ auth.Principal, groupID string, revision uint64, _ auth.MutationContext) error {
	s.calls++
	s.groupID, s.revision = groupID, revision
	return s.err
}
