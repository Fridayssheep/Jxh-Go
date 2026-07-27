package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
)

func TestObserverGetsRedactedAuditDetail(t *testing.T) {
	store := &fakeAuditStore{detail: audit.Log{
		ID: "aud_1", OccurredAt: time.Unix(100, 0), Actor: audit.Actor{Type: audit.ActorAdminUser, DisplayName: "Root"},
		Action: "sessions.revoke", Target: audit.Target{Type: "admin_session", ID: "ses_1"}, Result: audit.ResultSuccess,
		RequestID: "req_original", Source: audit.SourceWeb,
		Before: map[string]any{"token_digest": "never-return", "enabled": true}, Metadata: map[string]any{},
	}}
	router := newAuditHTTPFixture(t, store)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/audit-logs/aud_1", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if strings.Contains(body, "never-return") || !strings.Contains(body, audit.RedactedValue) || !strings.Contains(body, `"redacted":true`) {
		t.Fatalf("body=%s", body)
	}
	var object map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["audit_log_id"]; !ok {
		t.Fatalf("missing snake-case DTO: %#v", object)
	}
}

func TestAuditListMapsAllContractFilters(t *testing.T) {
	store := &fakeAuditStore{page: audit.Page{Items: []audit.Summary{{
		ID: "aud_1", OccurredAt: time.Unix(100, 0), Actor: audit.Actor{Type: audit.ActorSystem},
		Action: "system.restart", Target: audit.Target{Type: "system_operation"}, Result: audit.ResultUnknown, RequestID: "req_1",
	}}, NextCursor: "cursor_2", HasMore: true}}
	router := newAuditHTTPFixture(t, store)
	target := "/api/admin/v1/audit-logs?actor_user_id=usr_1&actor_type=admin_user&action=user.update&target_type=admin_user&target_id=usr_2&result=success&from=2026-07-27T00%3A00%3A00Z&to=2026-07-28T00%3A00%3A00Z&cursor=cursor_1&limit=25"
	request := httptest.NewRequest(http.MethodGet, target, nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	query := store.query
	if query.ActorUserID != "usr_1" || query.ActorType != audit.ActorAdminUser || query.TargetID != "usr_2" ||
		len(query.Actions) != 1 || query.Actions[0] != "user.update" || query.Limit != 25 || query.From == nil || query.To == nil {
		t.Fatalf("query=%+v", query)
	}
	if !strings.Contains(response.Body.String(), `"next_cursor":"cursor_2"`) {
		t.Fatalf("body=%s", response.Body.String())
	}
}

func TestAuditHTTPRejectsInvalidQueryBeforeStore(t *testing.T) {
	store := &fakeAuditStore{}
	router := newAuditHTTPFixture(t, store)
	for _, target := range []string{
		"/api/admin/v1/audit-logs?from=2026-07-28T00%3A00%3A00Z&to=2026-07-27T00%3A00%3A00Z",
		"/api/admin/v1/audit-logs?actor_type=unknown",
		"/api/admin/v1/audit-logs?limit=101",
		"/api/admin/v1/audit-logs?unknown=value",
		"/api/admin/v1/audit-logs?action=one&action=two",
		"/api/admin/v1/audit-logs?target_type=" + strings.Repeat("x", 65),
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if store.listCalls != 0 {
		t.Fatalf("invalid requests reached store %d times", store.listCalls)
	}
}

func newAuditHTTPFixture(t *testing.T, store audit.Store) *Router {
	t.Helper()
	service, err := audit.NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewAuditHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router := newHTTPFixture(t)
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

type fakeAuditStore struct {
	detail    audit.Log
	page      audit.Page
	query     audit.ListQuery
	getCalls  int
	listCalls int
}

func (s *fakeAuditStore) GetAuditLog(_ context.Context, id string) (audit.Log, bool, error) {
	s.getCalls++
	return s.detail, s.detail.ID == id, nil
}

func (s *fakeAuditStore) ListAuditLogs(_ context.Context, query audit.ListQuery) (audit.Page, error) {
	s.listCalls++
	s.query = query
	return s.page, nil
}
