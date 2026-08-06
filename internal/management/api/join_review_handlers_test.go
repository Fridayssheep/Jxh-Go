package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type joinReviewAuthenticator struct{ role auth.Role }

func (a joinReviewAuthenticator) Authenticate(context.Context, string) (auth.AuthContext, error) {
	now := time.Now().UTC()
	return auth.AuthContext{
		User:        auth.User{ID: "user-test", Username: "tester", DisplayName: "Tester", Role: a.role, Enabled: true, CreatedAt: now, UpdatedAt: now, Version: 1},
		Session:     auth.Session{ID: "session-test", UserID: "user-test", Status: auth.SessionStatusActive, Current: true, CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)},
		Permissions: auth.PermissionsFor(a.role), CSRFToken: "csrf-test",
	}, nil
}

type joinReviewOperationsStub struct {
	JoinRequestOperations
	rebuildKey string
	importKey  string
	importName string
}

func (s *joinReviewOperationsStub) ListMajorEvidence(context.Context, auth.Principal) ([]joinrequests.EvidenceSummary, joinrequests.RuleState, error) {
	now := time.Now().UTC()
	return nil, joinrequests.RuleState{RuleVersion: 2, Status: joinrequests.RuleStateReady, EvidenceVersion: 1, ActivatedAt: &now, RebuiltAt: &now, Version: 1}, nil
}

func (s *joinReviewOperationsStub) GetAutomaticRuleConfiguration(auth.Principal) (joinrequests.AutomaticRuleConfiguration, error) {
	return joinrequests.AutomaticRuleConfiguration{
		StudentIDLength: 12, EnrollmentYearOffset: 2, EnrollmentYearLength: 4,
		MajorCodeOffset: 6, MajorCodeLength: 3, CurrentYear: "2026", MinimumSamples: 3,
	}, nil
}

func (s *joinReviewOperationsStub) RebuildMajorEvidence(_ context.Context, _ auth.Principal, key string, _ auth.MutationContext) (joinrequests.EvidenceRebuildResult, error) {
	s.rebuildKey = key
	return joinrequests.EvidenceRebuildResult{RuleState: joinrequests.RuleState{RuleVersion: 2, Status: joinrequests.RuleStateReady, EvidenceVersion: 2, Version: 2}}, nil
}

func (s *joinReviewOperationsStub) ImportAdmissionRoster(_ context.Context, _ auth.Principal, name string, _ []byte, key string, _ auth.MutationContext) (joinrequests.AdmissionRosterStatus, error) {
	s.importKey, s.importName = key, name
	return joinrequests.AdmissionRosterStatus{Configured: true, RowCount: 1}, nil
}

func newJoinReviewRouter(t *testing.T, role auth.Role, operations *joinReviewOperationsStub) http.Handler {
	t.Helper()
	router, err := NewRouter(MiddlewareOptions{MaxBodyBytes: 2 << 20, MaxConcurrentRequests: 8, Authenticator: joinReviewAuthenticator{role: role}})
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewJoinRequestHandlers(operations)
	if err != nil {
		t.Fatal(err)
	}
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

func joinReviewRequest(method, target string, body *bytes.Reader) *http.Request {
	request := httptest.NewRequest(method, "http://manager.test"+target, body)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session-token"})
	request.RemoteAddr = "127.0.0.1:12345"
	return request
}

func authorizeJoinReviewMutation(request *http.Request) {
	request.Header.Set("Origin", "http://manager.test")
	request.Header.Set("X-Forwarded-Proto", "http")
	request.Header.Set("X-CSRF-Token", "csrf-test")
}

func TestJoinReviewRoutesEnforcePermissionsAndMutationHeaders(t *testing.T) {
	operations := &joinReviewOperationsStub{}
	observer := newJoinReviewRouter(t, auth.RoleObserver, operations)

	readRequest := joinReviewRequest(http.MethodGet, "/api/admin/v1/join-request-evidence/major-codes", bytes.NewReader(nil))
	readResponse := httptest.NewRecorder()
	observer.ServeHTTP(readResponse, readRequest)
	if readResponse.Code != http.StatusOK {
		t.Fatalf("observer read status = %d, body = %s", readResponse.Code, readResponse.Body.String())
	}

	forbiddenRequest := joinReviewRequest(http.MethodPost, "/api/admin/v1/join-request-evidence/rebuild", bytes.NewReader(nil))
	authorizeJoinReviewMutation(forbiddenRequest)
	forbiddenRequest.Header.Set("Idempotency-Key", "rebuild-test-1")
	forbiddenResponse := httptest.NewRecorder()
	observer.ServeHTTP(forbiddenResponse, forbiddenRequest)
	if forbiddenResponse.Code != http.StatusForbidden || operations.rebuildKey != "" {
		t.Fatalf("observer mutation status = %d, key = %q", forbiddenResponse.Code, operations.rebuildKey)
	}

	superAdmin := newJoinReviewRouter(t, auth.RoleSuperAdmin, operations)
	missingKeyRequest := joinReviewRequest(http.MethodPost, "/api/admin/v1/join-request-evidence/rebuild", bytes.NewReader(nil))
	authorizeJoinReviewMutation(missingKeyRequest)
	missingKeyResponse := httptest.NewRecorder()
	superAdmin.ServeHTTP(missingKeyResponse, missingKeyRequest)
	if missingKeyResponse.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency status = %d", missingKeyResponse.Code)
	}

	validRequest := joinReviewRequest(http.MethodPost, "/api/admin/v1/join-request-evidence/rebuild", bytes.NewReader(nil))
	authorizeJoinReviewMutation(validRequest)
	validRequest.Header.Set("Idempotency-Key", "rebuild-test-2")
	validResponse := httptest.NewRecorder()
	superAdmin.ServeHTTP(validResponse, validRequest)
	if validResponse.Code != http.StatusOK || operations.rebuildKey != "rebuild-test-2" {
		t.Fatalf("valid rebuild status = %d, key = %q, body = %s", validResponse.Code, operations.rebuildKey, validResponse.Body.String())
	}

	patchRequest := joinReviewRequest(http.MethodPatch, "/api/admin/v1/join-request-evidence/samples/1", bytes.NewReader([]byte(`{"active":false}`)))
	authorizeJoinReviewMutation(patchRequest)
	patchRequest.Header.Set("Content-Type", "application/json")
	patchResponse := httptest.NewRecorder()
	superAdmin.ServeHTTP(patchResponse, patchRequest)
	if patchResponse.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing If-Match status = %d, body = %s", patchResponse.Code, patchResponse.Body.String())
	}
}

func TestAdmissionRosterImportAcceptsMultipartAndIdempotencyKey(t *testing.T) {
	operations := &joinReviewOperationsStub{}
	router := newJoinReviewRouter(t, auth.RoleSuperAdmin, operations)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	file, err := writer.CreateFormFile("file", "roster.csv")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("学号,专业\n302026315326,计算机类\n")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://manager.test/api/admin/v1/admission-roster/import", &body)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "session-token"})
	request.RemoteAddr = "127.0.0.1:12345"
	authorizeJoinReviewMutation(request)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Idempotency-Key", "roster-import-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || operations.importKey != "roster-import-1" || !strings.EqualFold(operations.importName, "roster.csv") {
		t.Fatalf("import status = %d, key = %q, name = %q, body = %s", response.Code, operations.importKey, operations.importName, response.Body.String())
	}
}

func TestAdmissionRosterValidationErrorProducesFieldReport(t *testing.T) {
	handlers, err := NewJoinRequestHandlers(&joinReviewOperationsStub{})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://manager.test/api/admin/v1/admission-roster/import", nil)
	response := httptest.NewRecorder()
	handlers.writeServiceError(response, request, &joinrequests.AdmissionRosterValidationError{
		Row: 3, Field: "student_id", Message: "学号在文件中重复",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("validation status = %d", response.Code)
	}
	var payload ErrorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	report := payload.Error.Fields["student_id"]
	if len(report) != 1 || report[0] != "第 3 行：学号在文件中重复" {
		t.Fatalf("unexpected validation report: %+v", payload.Error.Fields)
	}
}
