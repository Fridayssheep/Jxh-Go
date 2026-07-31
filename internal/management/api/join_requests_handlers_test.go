package adminapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestJoinRequestPolicyRoutesMapFixedContract(t *testing.T) {
	service := &joinRequestOperationsFake{policy: joinPolicyHTTPFixture()}
	router := newJoinRequestHTTPFixture(t, service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, joinRequestReadRequest(http.MethodGet, "/api/admin/v1/groups/123/join-request-policy"))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` ||
		!strings.Contains(response.Body.String(), `"mode":"ai_fields_complete"`) ||
		!strings.Contains(response.Body.String(), `"required_fields":["student_id","name","major"]`) ||
		!strings.Contains(response.Body.String(), `"auto_reject":false`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	service.policy.Version = 2
	service.policy.Enabled = true
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/groups/123/join-request-policy", `{"enabled":true}`)
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` ||
		service.policyRevision != 1 || !service.policyPatch.Enabled.Set || !service.policyPatch.Enabled.Value {
		t.Fatalf("status=%d revision=%d patch=%+v body=%s", response.Code, service.policyRevision, service.policyPatch, response.Body.String())
	}

	service.policy.Version = 3
	service.policy.AutoReject = true
	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/groups/123/join-request-policy", `{"auto_reject":true}`)
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.policyRevision != 2 || !service.policyPatch.AutoReject.Set ||
		!service.policyPatch.AutoReject.Value || service.policyPatch.Enabled.Set {
		t.Fatalf("status=%d revision=%d patch=%+v body=%s", response.Code, service.policyRevision, service.policyPatch, response.Body.String())
	}
}

func TestJoinRequestPolicyWriteRequiresVersionAndWritePermission(t *testing.T) {
	service := &joinRequestOperationsFake{policy: joinPolicyHTTPFixture()}
	router := newJoinRequestHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/groups/123/join-request-policy", `{"enabled":true}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)

	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/groups/123/join-request-policy", `{"enabled":true}`)
	request.Header.Set("If-Match", `"1"`)
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if service.calls != 0 {
		t.Fatalf("rejected requests reached service %d times", service.calls)
	}
}

func TestStudentIDRuleRoutesMapVersionedContract(t *testing.T) {
	rule := studentIDRuleHTTPFixture()
	service := &joinRequestOperationsFake{studentIDRule: rule}
	router := newJoinRequestHTTPFixture(t, service)

	response := httptest.NewRecorder()
	router.ServeHTTP(response, joinRequestReadRequest(http.MethodGet, "/api/admin/v1/join-request-rules/student-id"))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` ||
		!strings.Contains(response.Body.String(), `"student_id_length":12`) ||
		!strings.Contains(response.Body.String(), `"major_code":"315"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	service.studentIDRule.Version = 4
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/join-request-rules/student-id",
		`{"enabled":true,"enrollment_year_segment":{"offset":2,"length":4},"major_code_segment":{"offset":6,"length":3},"mappings":[{"enrollment_year":2025,"major_code":"315","major_name":"Computer Science","aliases":["CS"]}]}`)
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` || service.studentIDRuleRevision != 3 ||
		!service.studentIDRulePatch.Enabled.Set || !service.studentIDRulePatch.Enabled.Value ||
		service.studentIDRulePatch.MajorCodeSegment.Value == nil || len(service.studentIDRulePatch.Mappings.Value) != 1 {
		t.Fatalf("status=%d revision=%d patch=%+v body=%s", response.Code, service.studentIDRuleRevision, service.studentIDRulePatch, response.Body.String())
	}

	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/join-request-rules/student-id", `{"major_code_segment":null}`)
	request.Header.Set("If-Match", `"4"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !service.studentIDRulePatch.MajorCodeSegment.Set || service.studentIDRulePatch.MajorCodeSegment.Value != nil {
		t.Fatalf("status=%d patch=%+v body=%s", response.Code, service.studentIDRulePatch, response.Body.String())
	}
}

func TestStudentIDRuleWriteRequiresVersionAndWritePermission(t *testing.T) {
	service := &joinRequestOperationsFake{studentIDRule: studentIDRuleHTTPFixture()}
	router := newJoinRequestHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/join-request-rules/student-id", `{"enabled":false}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)

	request = userMutationRequest(t, http.MethodPatch, "/api/admin/v1/join-request-rules/student-id", `{"enabled":false}`)
	request.Header.Set("If-Match", `"3"`)
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if service.calls != 0 {
		t.Fatalf("rejected requests reached service %d times", service.calls)
	}
}

func TestStudentIDRuleVersionConflictUsesVersionConflictCode(t *testing.T) {
	service := &joinRequestOperationsFake{studentIDRule: studentIDRuleHTTPFixture(), err: joinrequests.ErrConflict}
	router := newJoinRequestHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPatch, "/api/admin/v1/join-request-rules/student-id", `{"enabled":false}`)
	request.Header.Set("If-Match", `"3"`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assertErrorCode(t, response, http.StatusConflict, "resource_version_conflict")
}

func TestJoinRequestListParsesRepeatedStatusesAndReturnsSummary(t *testing.T) {
	service := &joinRequestOperationsFake{requestPage: joinrequests.Page[joinrequests.Request]{Items: []joinrequests.Request{joinRequestHTTPFixture()}}}
	router := newJoinRequestHTTPFixture(t, service)
	target := "/api/admin/v1/join-requests?group_id=123&decision_status=pending&decision_status=unknown&observed_status=pending&ai_parse_status=succeeded&sub_type=add&source=event&decision_source=manual&overdue=true&sort=requested_at_asc&limit=25"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, joinRequestReadRequest(http.MethodGet, target))
	if response.Code != http.StatusOK || service.listQuery.Limit != 25 || len(service.listQuery.DecisionStatuses) != 2 ||
		service.listQuery.Overdue == nil || !*service.listQuery.Overdue {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.listQuery, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"verification_message":"学号123456 姓名张三 专业计算机"`) ||
		!strings.Contains(body, `"student_id_assessment":{"status":"warning","rule_version":3`) ||
		strings.Contains(body, `"comment"`) || strings.Contains(body, "raw_json") {
		t.Fatalf("body=%s", body)
	}
}

func TestJoinRequestDetailAndDecisionHistoryMapContract(t *testing.T) {
	request := joinRequestHTTPFixture()
	decision := joinDecisionHTTPFixture(request.ID, joinrequests.AttemptConfirmed)
	service := &joinRequestOperationsFake{
		request: request, decisionPage: joinrequests.Page[joinrequests.Decision]{Items: []joinrequests.Decision{decision}},
	}
	router := newJoinRequestHTTPFixture(t, service)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, joinRequestReadRequest(http.MethodGet, "/api/admin/v1/join-requests/flag:123"))
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` ||
		!strings.Contains(response.Body.String(), `"comment":"detail"`) || strings.Contains(response.Body.String(), "system_raw_json") {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, joinRequestReadRequest(http.MethodGet, "/api/admin/v1/join-requests/flag:123/decisions?limit=10"))
	if response.Code != http.StatusOK || service.decisionQuery.RequestID != "flag:123" || service.decisionQuery.Limit != 10 ||
		!strings.Contains(response.Body.String(), `"decision_id":"dec_1"`) {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.decisionQuery, response.Body.String())
	}
}

func TestJoinRequestDecisionReturnsAcceptedForUnknownOutcome(t *testing.T) {
	request := joinRequestHTTPFixture()
	request.DecisionStatus = joinrequests.DecisionUnknown
	request.Version = 5
	decision := joinDecisionHTTPFixture(request.ID, joinrequests.AttemptUnknown)
	service := &joinRequestOperationsFake{decisionResult: joinrequests.DecisionResult{Request: request, Decision: decision}}
	router := newJoinRequestHTTPFixture(t, service)
	httpRequest := userMutationRequest(t, http.MethodPost, "/api/admin/v1/join-requests/flag:123/decisions", `{"action":"reject","reason":"not eligible"}`)
	httpRequest.Header.Set("If-Match", `"3"`)
	httpRequest.Header.Set("Idempotency-Key", "decision-key-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httpRequest)
	if response.Code != http.StatusAccepted || response.Header().Get("ETag") != `"5"` || service.revision != 3 ||
		service.idempotencyKey != "decision-key-1" || service.decisionInput.Action != joinrequests.ActionReject {
		t.Fatalf("status=%d revision=%d key=%q input=%+v body=%s", response.Code, service.revision, service.idempotencyKey, service.decisionInput, response.Body.String())
	}
}

func TestBulkJoinDecisionMapsPerItemErrors(t *testing.T) {
	confirmedRequest := joinRequestHTTPFixture()
	confirmedRequest.ID = "flag_1"
	unknownRequest := joinRequestHTTPFixture()
	unknownRequest.ID = "flag_2"
	unknownRequest.DecisionStatus = joinrequests.DecisionUnknown
	service := &joinRequestOperationsFake{bulkResult: joinrequests.BulkResult{
		GroupID: "123", Action: joinrequests.ActionApprove, ConfirmedCount: 1, UnknownCount: 1,
		Items: []joinrequests.BulkItemResult{
			{RequestID: "flag_1", Outcome: joinrequests.ItemConfirmed, Request: confirmedRequest, Decision: joinDecisionHTTPFixture("flag_1", joinrequests.AttemptConfirmed)},
			{RequestID: "flag_2", Outcome: joinrequests.ItemUnknown, Request: unknownRequest, Decision: joinDecisionHTTPFixture("flag_2", joinrequests.AttemptUnknown), Error: &joinrequests.ItemError{Code: "upstream_timeout", Message: "outcome unknown", Retryable: false}},
		},
	}}
	router := newJoinRequestHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/join-requests/bulk-decisions",
		`{"group_id":"123","action":"approve","items":[{"request_id":"flag_1","version":1},{"request_id":"flag_2","version":4}]}`)
	request.Header.Set("Idempotency-Key", "bulk-decision-key-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(service.bulkInput.Items) != 2 ||
		!strings.Contains(response.Body.String(), `"unknown_count":1`) ||
		!strings.Contains(response.Body.String(), `"code":"upstream_timeout"`) ||
		!strings.Contains(response.Body.String(), `"fields":{}`) {
		t.Fatalf("status=%d input=%+v body=%s", response.Code, service.bulkInput, response.Body.String())
	}
}

func TestJoinRequestHandlersRejectInvalidInputBeforeService(t *testing.T) {
	service := &joinRequestOperationsFake{}
	router := newJoinRequestHTTPFixture(t, service)
	tests := []struct {
		method  string
		target  string
		body    string
		headers map[string]string
	}{
		{method: http.MethodGet, target: "/api/admin/v1/join-requests?decision_status=invalid"},
		{method: http.MethodGet, target: "/api/admin/v1/join-requests?limit=101"},
		{method: http.MethodGet, target: "/api/admin/v1/join-requests?overdue=yes"},
		{method: http.MethodPatch, target: "/api/admin/v1/groups/123/join-request-policy", body: `{}`, headers: map[string]string{"If-Match": `"1"`}},
		{method: http.MethodPatch, target: "/api/admin/v1/join-request-rules/student-id", body: `{}`, headers: map[string]string{"If-Match": `"1"`}},
		{method: http.MethodPatch, target: "/api/admin/v1/join-request-rules/student-id", body: `{"enrollment_year_segment":{"length":4}}`, headers: map[string]string{"If-Match": `"1"`}},
		{method: http.MethodPatch, target: "/api/admin/v1/join-request-rules/student-id", body: `{"mappings":[{"enrollment_year":2025,"major_code":"315","major_name":"Computer Science"}]}`, headers: map[string]string{"If-Match": `"1"`}},
		{method: http.MethodPost, target: "/api/admin/v1/join-requests/bulk-decisions", body: `{"group_id":"123","action":"approve","items":[{"request_id":"flag_1"}]}`, headers: map[string]string{"Idempotency-Key": "bulk-decision-key-1"}},
	}
	for _, test := range tests {
		var request *http.Request
		if test.method == http.MethodGet {
			request = joinRequestReadRequest(test.method, test.target)
		} else {
			request = userMutationRequest(t, test.method, test.target, test.body)
		}
		for key, value := range test.headers {
			request.Header.Set(key, value)
		}
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != 0 {
		t.Fatalf("invalid input reached service %d times", service.calls)
	}
}

func TestJoinRequestWriteRouteRejectsObserverAndMapsUnavailable(t *testing.T) {
	service := &joinRequestOperationsFake{}
	router := newJoinRequestHTTPFixture(t, service)
	request := userMutationRequest(t, http.MethodPost, "/api/admin/v1/join-requests/flag:123/decisions", `{"action":"approve"}`)
	request.Header.Set("If-Match", `"3"`)
	request.Header.Set("Idempotency-Key", "decision-key-1")
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)

	service.err = joinrequests.ErrDependencyUnavailable
	request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/join-requests/flag:123/decisions", `{"action":"approve"}`)
	request.Header.Set("If-Match", `"3"`)
	request.Header.Set("Idempotency-Key", "decision-key-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
	if response.Header().Get("Retry-After") != "3" {
		t.Fatalf("headers=%v", response.Header())
	}
}

func newJoinRequestHTTPFixture(t *testing.T, service JoinRequestOperations) *Router {
	t.Helper()
	handlers, err := NewJoinRequestHandlers(service)
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

func joinRequestReadRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	return request
}

func joinPolicyHTTPFixture() joinrequests.Policy {
	return joinrequests.Policy{
		GroupID: "123", Mode: joinrequests.PolicyModeAIFieldsComplete,
		RequiredFields: joinrequests.PolicyRequiredFields(), Version: 1,
		UpdatedAt: time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC),
	}
}

func studentIDRuleHTTPFixture() joinrequests.StudentIDRule {
	return joinrequests.StudentIDRule{
		Enabled:               true,
		EnrollmentYearSegment: &joinrequests.StudentIDSegment{Offset: 2, Length: 4},
		MajorCodeSegment:      &joinrequests.StudentIDSegment{Offset: 6, Length: 3},
		Mappings: []joinrequests.StudentMajorMapping{{
			EnrollmentYear: 2025, MajorCode: "315", MajorName: "Computer Science", Aliases: []string{"CS"},
		}},
		Version: 3, UpdatedAt: time.Date(2026, 7, 29, 1, 0, 0, 0, time.UTC),
	}
}

func joinRequestHTTPFixture() joinrequests.Request {
	studentID, name, major := "123456", "张三", "计算机"
	comment := "detail"
	return joinrequests.Request{
		ID: "flag:123", Group: joinrequests.GroupReference{ID: "123", Name: "Test Group"}, ApplicantQQ: "456",
		VerificationMessage: "学号123456 姓名张三 专业计算机", SubType: joinrequests.SubTypeAdd,
		Source: joinrequests.RequestSourceEvent, ObservedStatus: joinrequests.ObservedPending,
		DecisionStatus: joinrequests.DecisionPending,
		AIParse: joinrequests.AIParseResult{Status: joinrequests.AIParseSucceeded, Fields: &joinrequests.ApplicantFields{
			StudentID: &studentID, Name: &name, Major: &major, Valid: true, ValidationErrors: []string{},
		}},
		StudentIDAssessment: joinrequests.StudentIDAssessment{
			Status: joinrequests.StudentIDAssessmentWarning, RuleVersion: 3,
			Warnings: []joinrequests.StudentIDWarning{joinrequests.StudentIDLengthMismatch},
		},
		RequestedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC), Overdue: true, Version: 3, Comment: &comment,
		FirstObservedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC), LastObservedAt: time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC),
	}
}

func joinDecisionHTTPFixture(requestID string, status joinrequests.AttemptStatus) joinrequests.Decision {
	completedAt := time.Date(2026, 7, 28, 3, 0, 1, 0, time.UTC)
	decision := joinrequests.Decision{
		ID: "dec_1", RequestID: requestID, Action: joinrequests.ActionApprove, Source: joinrequests.SourceManual,
		Status: status, StartedAt: time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC), TraceID: "req_1",
	}
	if status != joinrequests.AttemptStarted {
		decision.CompletedAt = &completedAt
	}
	if status == joinrequests.AttemptUnknown {
		code := "upstream_timeout"
		decision.ErrorCode = &code
	}
	return decision
}

type joinRequestOperationsFake struct {
	policy                joinrequests.Policy
	requestPage           joinrequests.Page[joinrequests.Request]
	request               joinrequests.Request
	decisionPage          joinrequests.Page[joinrequests.Decision]
	decisionResult        joinrequests.DecisionResult
	bulkResult            joinrequests.BulkResult
	err                   error
	calls                 int
	policyRevision        uint64
	policyPatch           joinrequests.PolicyPatch
	studentIDRule         joinrequests.StudentIDRule
	studentIDRuleRevision uint64
	studentIDRulePatch    joinrequests.StudentIDRulePatch
	listQuery             joinrequests.ListQuery
	decisionQuery         joinrequests.DecisionListQuery
	revision              uint64
	idempotencyKey        string
	decisionInput         joinrequests.DecisionInput
	bulkInput             joinrequests.BulkInput
}

func (s *joinRequestOperationsFake) GetStudentIDRule(context.Context, auth.Principal) (joinrequests.StudentIDRule, error) {
	s.calls++
	return s.studentIDRule, s.err
}

func (s *joinRequestOperationsFake) UpdateStudentIDRule(_ context.Context, _ auth.Principal, revision uint64, patch joinrequests.StudentIDRulePatch, _ auth.MutationContext) (joinrequests.StudentIDRule, error) {
	s.calls++
	s.studentIDRuleRevision, s.studentIDRulePatch = revision, patch
	return s.studentIDRule, s.err
}

func (s *joinRequestOperationsFake) GetPolicy(context.Context, auth.Principal, string) (joinrequests.Policy, error) {
	s.calls++
	return s.policy, s.err
}

func (s *joinRequestOperationsFake) UpdatePolicy(_ context.Context, _ auth.Principal, _ string, revision uint64, patch joinrequests.PolicyPatch, _ auth.MutationContext) (joinrequests.Policy, error) {
	s.calls++
	s.policyRevision, s.policyPatch = revision, patch
	return s.policy, s.err
}

func (s *joinRequestOperationsFake) List(_ context.Context, _ auth.Principal, query joinrequests.ListQuery) (joinrequests.Page[joinrequests.Request], error) {
	s.calls++
	s.listQuery = query
	return s.requestPage, s.err
}

func (s *joinRequestOperationsFake) Get(context.Context, auth.Principal, string) (joinrequests.Request, error) {
	s.calls++
	return s.request, s.err
}

func (s *joinRequestOperationsFake) ListDecisions(_ context.Context, _ auth.Principal, query joinrequests.DecisionListQuery) (joinrequests.Page[joinrequests.Decision], error) {
	s.calls++
	s.decisionQuery = query
	return s.decisionPage, s.err
}

func (s *joinRequestOperationsFake) Decide(_ context.Context, _ auth.Principal, _ string, revision uint64, input joinrequests.DecisionInput, key string, _ auth.MutationContext) (joinrequests.DecisionResult, error) {
	s.calls++
	s.revision, s.decisionInput, s.idempotencyKey = revision, input, key
	return s.decisionResult, s.err
}

func (s *joinRequestOperationsFake) BulkDecide(_ context.Context, _ auth.Principal, input joinrequests.BulkInput, key string, _ auth.MutationContext) (joinrequests.BulkResult, error) {
	s.calls++
	s.bulkInput, s.idempotencyKey = input, key
	return s.bulkResult, s.err
}
