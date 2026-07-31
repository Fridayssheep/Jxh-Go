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

	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

const validCommandDefinitionJSON = `{"name":"/hello","display_name":"Hello","description":"","scope":{"type":"global","group_ids":[]},"trigger_permission":"everyone","parameters":[{"name":"text","display_name":"Text","type":"text","required":true,"min_length":1,"max_length":100}],"actions":[{"type":"reply_text","template":"{{text}}"}]}`

func TestCommandHandlersExerciseAllEightRoutes(t *testing.T) {
	service := &fakeCommandOperations{
		command:    commandFixture(),
		commands:   customcommand.Page[customcommand.Command]{Items: []customcommand.Command{commandFixture()}},
		validation: customcommand.ValidationResult{Valid: true, Issues: []customcommand.ValidationIssue{}, Warnings: []customcommand.ValidationIssue{}},
		runs:       customcommand.Page[customcommand.Run]{Items: []customcommand.Run{commandRunFixture()}},
	}
	router := newCommandHTTPFixture(t, service)

	request := commandMutationRequest(t, http.MethodPost, "/api/admin/v1/commands", validCommandDefinitionJSON)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"4"` || service.created.Name != "/hello" {
		t.Fatalf("create status=%d etag=%q input=%+v body=%s", response.Code, response.Header().Get("ETag"), service.created, response.Body.String())
	}

	request = commandReadRequest(http.MethodGet, "/api/admin/v1/commands?query=hello&enabled=true&status=active&scope_type=groups&group_id=123&action_type=reply_text&trigger_permission=group_admin&cursor=cmd_0&limit=25")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.listQuery.Limit != 25 || service.listQuery.Enabled == nil || !*service.listQuery.Enabled {
		t.Fatalf("list status=%d query=%+v body=%s", response.Code, service.listQuery, response.Body.String())
	}

	request = commandReadRequest(http.MethodGet, "/api/admin/v1/commands/cmd_1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` || !strings.Contains(response.Body.String(), `"member_parameter":null`) {
		t.Fatalf("get status=%d etag=%q body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	request = commandMutationRequest(t, http.MethodPatch, "/api/admin/v1/commands/cmd_1", `{"enabled":false}`)
	request.Header.Set("If-Match", `"4"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.revision != 4 || !service.patch.Enabled.Set || service.patch.Enabled.Value {
		t.Fatalf("patch status=%d revision=%d patch=%+v body=%s", response.Code, service.revision, service.patch, response.Body.String())
	}

	request = commandMutationRequest(t, http.MethodDelete, "/api/admin/v1/commands/cmd_1", "")
	request.Header.Set("If-Match", `"4"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || service.archivedID != "cmd_1" {
		t.Fatalf("archive status=%d id=%q body=%s", response.Code, service.archivedID, response.Body.String())
	}

	request = commandMutationRequest(t, http.MethodPost, "/api/admin/v1/commands/validate", `{"definition":`+validCommandDefinitionJSON+`,"sample":{"group_id":"123","sender_qq":"9988","sender_role":"member","message":"/hello hi"}}`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.draftSample.Message != "/hello hi" || !strings.Contains(response.Body.String(), `"valid":true`) {
		t.Fatalf("draft validation status=%d sample=%+v body=%s", response.Code, service.draftSample, response.Body.String())
	}

	request = commandMutationRequest(t, http.MethodPost, "/api/admin/v1/commands/cmd_1/validate", `{"group_id":"123","sender_qq":"9988","sender_role":"admin","message":"/hello hi"}`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.validatedID != "cmd_1" {
		t.Fatalf("stored validation status=%d id=%q body=%s", response.Code, service.validatedID, response.Body.String())
	}

	request = commandReadRequest(http.MethodGet, "/api/admin/v1/commands/cmd_1/runs?result=partial&from=2026-07-27T00%3A00%3A00Z&to=2026-07-29T00%3A00%3A00%2B00%3A00&cursor=run_0&limit=10")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.runQuery.CommandID != "cmd_1" || service.runQuery.Result != customcommand.RunPartial ||
		!strings.Contains(response.Body.String(), `"duration_ms":1500`) || strings.Contains(response.Body.String(), "ArgumentSummaries") ||
		strings.Contains(response.Body.String(), "argument_summaries") || strings.Contains(response.Body.String(), "request_id") {
		t.Fatalf("runs status=%d query=%+v body=%s", response.Code, service.runQuery, response.Body.String())
	}
}

func TestCommandHandlersRejectInvalidNestedJSONAndMissingFields(t *testing.T) {
	service := &fakeCommandOperations{command: commandFixture()}
	router := newCommandHTTPFixture(t, service)
	invalidBodies := []string{
		`{"name":"/hello","display_name":"Hello","description":"","scope":{"type":"global","group_ids":[]},"trigger_permission":"everyone","parameters":[],"actions":[{"type":"reply_text","template":"hello","script":"bad"}]}`,
		`{"name":"/hello","display_name":"Hello","description":"","scope":{"type":"global"},"trigger_permission":"everyone","parameters":[],"actions":[{"type":"reply_text","template":"hello"}]}`,
		`{"name":"/hello","display_name":"Hello","description":"","scope":{"type":"global","group_ids":[]},"trigger_permission":"everyone","parameters":[{"name":"text","display_name":"Text","type":"text","required":true,"min_length":1}],"actions":[{"type":"reply_text","template":"hello"}]}`,
		`{"name":"/hello","display_name":"Hello","description":"","scope":{"type":"global","group_ids":[]},"trigger_permission":"everyone","parameters":[],"actions":[{"type":"mention","target":"triggerer"}]}`,
	}
	for _, body := range invalidBodies {
		request := commandMutationRequest(t, http.MethodPost, "/api/admin/v1/commands", body)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}

	request := commandMutationRequest(t, http.MethodPatch, "/api/admin/v1/commands/cmd_1", `{"enabled":null}`)
	request.Header.Set("If-Match", `"4"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)

	request = commandMutationRequest(t, http.MethodPatch, "/api/admin/v1/commands/cmd_1", `{"scope":{"type":"global","group_ids":[],"extra":true}}`)
	request.Header.Set("If-Match", `"4"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	if service.calls != 0 {
		t.Fatalf("invalid JSON reached service %d times", service.calls)
	}
}

func TestCommandHandlersEnforceRevisionPermissionsAndCSRF(t *testing.T) {
	service := &fakeCommandOperations{command: commandFixture()}
	router := newCommandHTTPFixture(t, service)

	request := commandMutationRequest(t, http.MethodPatch, "/api/admin/v1/commands/cmd_1", `{"enabled":true}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)

	request = commandMutationRequest(t, http.MethodPost, "/api/admin/v1/commands", validCommandDefinitionJSON)
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)

	request = commandMutationRequest(t, http.MethodPost, "/api/admin/v1/commands", validCommandDefinitionJSON)
	request.Header.Del("X-CSRF-Token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeCSRFInvalid)

	request = commandReadRequest(http.MethodGet, "/api/admin/v1/commands")
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("observer read status=%d body=%s", response.Code, response.Body.String())
	}
	if service.calls != 1 {
		t.Fatalf("blocked writes reached service: calls=%d", service.calls)
	}
}

func TestCommandHandlersRejectInvalidQueriesBeforeService(t *testing.T) {
	service := &fakeCommandOperations{}
	router := newCommandHTTPFixture(t, service)
	for _, target := range []string{
		"/api/admin/v1/commands?enabled=1",
		"/api/admin/v1/commands?status=paused",
		"/api/admin/v1/commands?scope_type=local",
		"/api/admin/v1/commands?action_type=http_request",
		"/api/admin/v1/commands?trigger_permission=owner",
		"/api/admin/v1/commands?status=active&status=draft",
		"/api/admin/v1/commands?limit=101",
		"/api/admin/v1/commands/cmd_1/runs?result=skipped",
		"/api/admin/v1/commands/cmd_1/runs?from=2026-07-29T00%3A00%3A00Z&to=2026-07-28T00%3A00%3A00Z",
		"/api/admin/v1/commands/cmd_1/runs?from=2026-07-28T08%3A00%3A00%2B08%3A00",
	} {
		request := commandReadRequest(http.MethodGet, target)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != 0 {
		t.Fatalf("invalid queries reached service %d times", service.calls)
	}
}

func TestCommandHandlerMapsDomainErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{customcommand.ErrInvalidInput, http.StatusBadRequest, CodeBadRequest},
		{customcommand.ErrForbidden, http.StatusForbidden, CodeForbidden},
		{customcommand.ErrNotFound, http.StatusNotFound, CodeNotFound},
		{customcommand.ErrConflict, http.StatusConflict, "resource_version_conflict"},
		{errors.New("storage failed"), http.StatusInternalServerError, CodeInternal},
	}
	for _, test := range cases {
		service := &fakeCommandOperations{err: test.err}
		router := newCommandHTTPFixture(t, service)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, commandReadRequest(http.MethodGet, "/api/admin/v1/commands/cmd_1"))
		assertErrorCode(t, response, test.status, test.code)
	}
}

func newCommandHTTPFixture(t *testing.T, service CommandOperations) *Router {
	t.Helper()
	handlers, err := NewCommandHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(MiddlewareOptions{
		MaxBodyBytes: 1 << 20,
		Random:       bytes.NewReader(bytes.Repeat([]byte{2}, 8192)), Authenticator: testAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

func commandMutationRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	return userMutationRequest(t, method, target, body)
}

func commandReadRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	return request
}

func commandFixture() customcommand.Command {
	userID := "usr_1"
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	return customcommand.Command{
		ID: "cmd_1",
		Definition: customcommand.Definition{
			Name: "/hello", DisplayName: "Hello", Description: "", Scope: customcommand.Scope{Type: customcommand.ScopeGlobal, GroupIDs: []string{}},
			TriggerPermission: customcommand.TriggerGroupAdmin,
			Parameters:        []customcommand.Parameter{{Name: "member", DisplayName: "Member", Type: customcommand.ParameterMember, Required: true, AllowTriggerer: true}},
			Actions:           []customcommand.Action{{Type: customcommand.ActionMention, Target: customcommand.MentionTriggerer}},
		},
		Enabled: true, Status: customcommand.StatusActive, Version: 4, CreatedAt: now.Add(-time.Hour), UpdatedAt: now,
		UpdatedBy: audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: "Admin"},
	}
}

func commandRunFixture() customcommand.Run {
	code := "mute_rejected"
	return customcommand.Run{
		ID: "run_1", RunIdentity: "sensitive-internal-id", CommandID: "cmd_1", CommandName: "/hello", GroupID: "123", TriggeredByQQ: "9988",
		Result: customcommand.RunPartial, ArgumentSummaries: []customcommand.ArgumentSummary{{Name: "text", Type: customcommand.ParameterText, Present: true, Digest: "secret-digest"}},
		ActionSteps: []customcommand.ActionStep{{Index: 0, Type: customcommand.ActionReplyText, Result: customcommand.StepSuccess, Duration: 500 * time.Millisecond}, {Index: 1, Type: customcommand.ActionMuteMember, Result: customcommand.StepFailed, Duration: time.Second, ErrorCode: &code}},
		Duration:    1500 * time.Millisecond, ErrorCode: &code, RequestID: "req_internal", OccurredAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	}
}

type fakeCommandOperations struct {
	command     customcommand.Command
	commands    customcommand.Page[customcommand.Command]
	validation  customcommand.ValidationResult
	runs        customcommand.Page[customcommand.Run]
	err         error
	calls       int
	created     customcommand.Definition
	patch       customcommand.Patch
	listQuery   customcommand.ListQuery
	runQuery    customcommand.RunListQuery
	draftSample customcommand.ValidationSample
	revision    uint64
	archivedID  string
	validatedID string
}

func (s *fakeCommandOperations) Create(_ context.Context, _ auth.Principal, definition customcommand.Definition, _ auth.MutationContext) (customcommand.Command, error) {
	s.calls++
	s.created = definition
	return s.command, s.err
}

func (s *fakeCommandOperations) Get(context.Context, auth.Principal, string) (customcommand.Command, error) {
	s.calls++
	return s.command, s.err
}

func (s *fakeCommandOperations) List(_ context.Context, _ auth.Principal, query customcommand.ListQuery) (customcommand.Page[customcommand.Command], error) {
	s.calls++
	s.listQuery = query
	return s.commands, s.err
}

func (s *fakeCommandOperations) Update(_ context.Context, _ auth.Principal, _ string, revision uint64, patch customcommand.Patch, _ auth.MutationContext) (customcommand.Command, error) {
	s.calls++
	s.revision, s.patch = revision, patch
	return s.command, s.err
}

func (s *fakeCommandOperations) Archive(_ context.Context, _ auth.Principal, id string, revision uint64, _ auth.MutationContext) error {
	s.calls++
	s.archivedID, s.revision = id, revision
	return s.err
}

func (s *fakeCommandOperations) ValidateDraft(_ context.Context, _ auth.Principal, _ customcommand.Definition, sample customcommand.ValidationSample) (customcommand.ValidationResult, error) {
	s.calls++
	s.draftSample = sample
	return s.validation, s.err
}

func (s *fakeCommandOperations) ValidateStored(_ context.Context, _ auth.Principal, id string, sample customcommand.ValidationSample) (customcommand.ValidationResult, error) {
	s.calls++
	s.validatedID, s.draftSample = id, sample
	return s.validation, s.err
}

func (s *fakeCommandOperations) ListRuns(_ context.Context, _ auth.Principal, query customcommand.RunListQuery) (customcommand.Page[customcommand.Run], error) {
	s.calls++
	s.runQuery = query
	return s.runs, s.err
}
