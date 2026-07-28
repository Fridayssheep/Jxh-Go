package adminapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestKnowledgeStatusHTTPMatchesContractAndUsesReadPermission(t *testing.T) {
	started := time.Date(2026, 7, 28, 9, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	version := "idx_11"
	service := &knowledgeOperationsFake{status: knowledgeadmin.Status{
		State: knowledgeadmin.StateReloading, SourceConfigured: true, ActiveIndexVersion: &version,
		EntryCount: 11, ConflictCount: 2, LastAttemptAt: &started,
		CurrentOperation: &knowledgeadmin.ReloadOperation{
			ID: "kop_11", Status: knowledgeadmin.OperationRunning, StartedAt: started,
		},
	}}
	router := newKnowledgeHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/knowledge/status", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.statusCalls != 1 || service.principal.Role != auth.RoleObserver {
		t.Fatalf("status=%d calls=%d principal=%+v body=%s", response.Code, service.statusCalls, service.principal, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"state":"reloading"`, `"source_configured":true`, `"active_index_version":"idx_11"`,
		`"last_attempt_at":"2026-07-28T01:00:00Z"`, `"last_success_at":null`, `"error_code":null`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %s: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"sid", "share_url", "token="} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, body)
		}
	}
}

func TestKnowledgeEntriesHTTPParsesFiltersAndMapsSummary(t *testing.T) {
	indexedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	service := &knowledgeOperationsFake{entryPage: knowledgeadmin.EntryPage{
		Items: []knowledgeadmin.EntrySummary{{
			ID: "entry_1", Title: "How to apply", Category: "guide", Type: knowledgeadmin.EntryTypeHybrid,
			Keywords: []string{"apply"}, Enabled: true, ExactReply: true, AIEnabled: true, HasConflict: true,
			IndexedAt: indexedAt,
		}}, NextCursor: "cursor_2", HasMore: true,
	}}
	router := newKnowledgeHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet,
		"/api/admin/v1/knowledge/entries?query=apply&category=guide&entry_type=hybrid&enabled=true&exact_reply=false&ai_enabled=true&has_conflict=true&cursor=cursor_1&limit=25")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.entryListCalls != 1 {
		t.Fatalf("status=%d calls=%d body=%s", response.Code, service.entryListCalls, response.Body.String())
	}
	query := service.entryQuery
	if query.Query != "apply" || query.Category != "guide" || query.Type != knowledgeadmin.EntryTypeHybrid || query.Limit != 25 ||
		query.Cursor != "cursor_1" || query.Enabled == nil || !*query.Enabled || query.ExactReply == nil || *query.ExactReply ||
		query.AIEnabled == nil || !*query.AIEnabled || query.HasConflict == nil || !*query.HasConflict {
		t.Fatalf("query=%+v", query)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"entry_id":"entry_1"`, `"entry_type":"hybrid"`, `"aliases":[]`, `"source_updated_at":null`,
		`"next_cursor":"cursor_2"`, `"has_more":true`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %s: %s", expected, body)
		}
	}
}

func TestKnowledgeEntryAndConflictsHTTPMapFullRecords(t *testing.T) {
	indexedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	service := &knowledgeOperationsFake{
		entry: knowledgeadmin.Entry{
			ID: "entry:001", SourceKey: "WPS-row-001", Title: "Title", Category: "FAQ", Type: knowledgeadmin.EntryTypeAIKnowledge,
			Keywords: []string{"keyword"}, Aliases: []string{}, Question: "Question", Answer: "Answer", AIEnabled: true, IndexedAt: indexedAt,
		},
		conflictPage: knowledgeadmin.ConflictPage{Items: []knowledgeadmin.Conflict{{
			ID: "conflict_1", Type: knowledgeadmin.ConflictAlias, Key: "alias", EntryIDs: []string{"entry:001", "entry:002"}, DetectedAt: indexedAt,
		}}},
	}
	router := newKnowledgeHTTPFixture(t, service)

	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/knowledge/entries/entry:001")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.entryID != "entry:001" || !strings.Contains(response.Body.String(), `"source_key":"WPS-row-001"`) ||
		!strings.Contains(response.Body.String(), `"answer":"Answer"`) {
		t.Fatalf("status=%d id=%q body=%s", response.Code, service.entryID, response.Body.String())
	}

	request = scheduledReadRequest(http.MethodGet, "/api/admin/v1/knowledge/conflicts?query=alias&conflict_type=alias&limit=10")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.conflictQuery.Type != knowledgeadmin.ConflictAlias || service.conflictQuery.Limit != 10 ||
		!strings.Contains(response.Body.String(), `"entry_ids":["entry:001","entry:002"]`) ||
		!strings.Contains(response.Body.String(), `"next_cursor":null`) {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.conflictQuery, response.Body.String())
	}
}

func TestKnowledgeHTTPRejectsInvalidQueriesBeforeService(t *testing.T) {
	service := &knowledgeOperationsFake{}
	router := newKnowledgeHTTPFixture(t, service)
	targets := []string{
		"/api/admin/v1/knowledge/entries?unknown=x",
		"/api/admin/v1/knowledge/entries?query=a&query=b",
		"/api/admin/v1/knowledge/entries?entry_type=invalid",
		"/api/admin/v1/knowledge/entries?enabled=1",
		"/api/admin/v1/knowledge/entries?limit=",
		"/api/admin/v1/knowledge/entries?limit=101",
		"/api/admin/v1/knowledge/entries?query=" + strings.Repeat("x", 201),
		"/api/admin/v1/knowledge/conflicts?conflict_type=invalid",
		"/api/admin/v1/knowledge/conflicts?cursor=" + strings.Repeat("x", 2049),
	}
	for _, target := range targets {
		request := scheduledReadRequest(http.MethodGet, target)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/knowledge/entries/"+strings.Repeat("x", 257))
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	if service.entryListCalls != 0 || service.conflictListCalls != 0 || service.entryGetCalls != 0 {
		t.Fatalf("invalid input reached service: entries=%d conflicts=%d get=%d", service.entryListCalls, service.conflictListCalls, service.entryGetCalls)
	}
}

func TestKnowledgeReloadHTTPRequiresPermissionCSRFAndIdempotencyKey(t *testing.T) {
	service := &knowledgeOperationsFake{operation: knowledgeadmin.ReloadOperation{
		ID: "kop_1", Status: knowledgeadmin.OperationAccepted, StartedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
	}}
	router := newKnowledgeHTTPFixture(t, service)

	request := httptest.NewRequest(http.MethodPost, "/api/admin/v1/knowledge/reload", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusUnauthorized, CodeUnauthorized)

	request = httptest.NewRequest(http.MethodPost, "/api/admin/v1/knowledge/reload", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	request.Header.Set("Origin", "https://manager.example")
	request.Header.Set("X-CSRF-Token", "valid-csrf-token")
	request.Header.Set("Idempotency-Key", "reload-key-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)

	request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/knowledge/reload", "")
	request.Header.Set("Idempotency-Key", "reload-key-1")
	request.Header.Del("X-CSRF-Token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeCSRFInvalid)

	request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/knowledge/reload", "")
	request.Header.Set("Idempotency-Key", "short")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)

	request = userMutationRequest(t, http.MethodPost, "/api/admin/v1/knowledge/reload", "")
	request.Header.Set("Idempotency-Key", "reload-key-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || service.reloadCalls != 1 || service.idempotencyKey != "reload-key-1" ||
		service.mutation.RequestID == "" || !strings.Contains(response.Body.String(), `"completed_at":null`) {
		t.Fatalf("status=%d calls=%d key=%q body=%s", response.Code, service.reloadCalls, service.idempotencyKey, response.Body.String())
	}
}

func TestKnowledgeHTTPMapsServiceErrors(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		method     string
		serviceErr error
		status     int
		code       string
	}{
		{"not found", "/api/admin/v1/knowledge/entries/missing", http.MethodGet, knowledgeadmin.ErrNotFound, http.StatusNotFound, CodeNotFound},
		{"conflict", "/api/admin/v1/knowledge/reload", http.MethodPost, knowledgeadmin.ErrReloadInProgress, http.StatusConflict, CodeConflict},
		{"idempotency conflict", "/api/admin/v1/knowledge/reload", http.MethodPost, knowledgeadmin.ErrIdempotencyConflict, http.StatusConflict, "idempotency_key_reused"},
		{"unavailable", "/api/admin/v1/knowledge/reload", http.MethodPost, knowledgeadmin.ErrReloaderUnavailable, http.StatusServiceUnavailable, "dependency_unavailable"},
		{"internal", "/api/admin/v1/knowledge/status", http.MethodGet, errors.New("WPS SID secret"), http.StatusInternalServerError, CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &knowledgeOperationsFake{err: test.serviceErr}
			router := newKnowledgeHTTPFixture(t, service)
			var request *http.Request
			if test.method == http.MethodPost {
				request = userMutationRequest(t, test.method, test.target, "")
				request.Header.Set("Idempotency-Key", "reload-key-1")
			} else {
				request = scheduledReadRequest(test.method, test.target)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertErrorCode(t, response, test.status, test.code)
			if strings.Contains(strings.ToLower(response.Body.String()), "sid") || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("raw error leaked: %s", response.Body.String())
			}
			if test.status == http.StatusServiceUnavailable && response.Header().Get("Retry-After") != "3" {
				t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
			}
		})
	}
}

func newKnowledgeHTTPFixture(t *testing.T, service KnowledgeOperations) *Router {
	t.Helper()
	handlers, err := NewKnowledgeHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router := newHTTPFixture(t)
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

type knowledgeOperationsFake struct {
	status       knowledgeadmin.Status
	operation    knowledgeadmin.ReloadOperation
	entryPage    knowledgeadmin.EntryPage
	entry        knowledgeadmin.Entry
	conflictPage knowledgeadmin.ConflictPage
	err          error

	statusCalls       int
	reloadCalls       int
	entryListCalls    int
	entryGetCalls     int
	conflictListCalls int
	principal         auth.Principal
	idempotencyKey    string
	mutation          auth.MutationContext
	entryQuery        knowledgeadmin.EntryQuery
	entryID           string
	conflictQuery     knowledgeadmin.ConflictQuery
}

func (s *knowledgeOperationsFake) GetStatus(_ context.Context, principal auth.Principal) (knowledgeadmin.Status, error) {
	s.statusCalls++
	s.principal = principal
	return s.status, s.err
}

func (s *knowledgeOperationsFake) StartReload(_ context.Context, principal auth.Principal, key string, request ...auth.MutationContext) (knowledgeadmin.ReloadOperation, error) {
	s.reloadCalls++
	s.principal = principal
	s.idempotencyKey = key
	if len(request) > 0 {
		s.mutation = request[0]
	}
	return s.operation, s.err
}

func (s *knowledgeOperationsFake) ListEntries(_ context.Context, principal auth.Principal, query knowledgeadmin.EntryQuery) (knowledgeadmin.EntryPage, error) {
	s.entryListCalls++
	s.principal = principal
	s.entryQuery = query
	return s.entryPage, s.err
}

func (s *knowledgeOperationsFake) GetEntry(_ context.Context, principal auth.Principal, id string) (knowledgeadmin.Entry, error) {
	s.entryGetCalls++
	s.principal = principal
	s.entryID = id
	return s.entry, s.err
}

func (s *knowledgeOperationsFake) ListConflicts(_ context.Context, principal auth.Principal, query knowledgeadmin.ConflictQuery) (knowledgeadmin.ConflictPage, error) {
	s.conflictListCalls++
	s.principal = principal
	s.conflictQuery = query
	return s.conflictPage, s.err
}
