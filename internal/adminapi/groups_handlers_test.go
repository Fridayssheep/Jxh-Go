package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/groups"
)

func TestGroupListMapsFiltersAndContractDTO(t *testing.T) {
	service := &fakeGroupOperations{page: groups.Page{Items: []groups.Group{groupHTTPFixture()}, HasMore: true, NextCursor: "cursor_2"}}
	router := newGroupHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/groups?query=Alpha&bot_role=admin&snapshot_state=fresh&feature_key=ai_qa&feature_enabled=true&cursor=cursor_1&limit=25")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.query.Limit != 25 || service.query.FeatureEnabled == nil || !*service.query.FeatureEnabled {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.query, response.Body.String())
	}
	for _, expected := range []string{`"group_id":"00123"`, `"snapshot_state":"fresh"`, `"source":"group_override"`, `"next_cursor":"cursor_2"`} {
		if !strings.Contains(response.Body.String(), expected) {
			t.Fatalf("body missing %s: %s", expected, response.Body.String())
		}
	}
}

func TestGroupQueriesRejectInvalidValuesBeforeService(t *testing.T) {
	service := &fakeGroupOperations{}
	router := newGroupHTTPFixture(t, service)
	for _, target := range []string{
		"/api/admin/v1/groups?bot_role=super",
		"/api/admin/v1/groups?snapshot_state=unknown",
		"/api/admin/v1/groups?feature_key=invalid",
		"/api/admin/v1/groups?feature_enabled=true",
		"/api/admin/v1/groups?feature_key=ai_qa&feature_enabled=1",
		"/api/admin/v1/groups?limit=101",
		"/api/admin/v1/groups?query=a&query=b",
		"/api/admin/v1/groups?unknown=x",
	} {
		request := scheduledReadRequest(http.MethodGet, target)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != 0 {
		t.Fatalf("invalid queries reached service %d times", service.calls)
	}
}

func TestGroupSyncRequiresWritePermissionAndIdempotency(t *testing.T) {
	service := &fakeGroupOperations{syncResult: groups.SyncResult{SyncedAt: time.Unix(100, 0).UTC(), AddedCount: 1, TotalCount: 1}}
	router := newGroupHTTPFixture(t, service)
	request := scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/groups/sync", "")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	if service.calls != 0 {
		t.Fatal("missing idempotency key reached service")
	}

	request = scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/groups/sync", "")
	request.Header.Set("Idempotency-Key", "group-sync-key-1")
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if service.calls != 0 {
		t.Fatal("observer sync reached service")
	}

	request = scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/groups/sync", "")
	request.Header.Set("Idempotency-Key", "group-sync-key-1")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.idempotencyKey != "group-sync-key-1" || !strings.Contains(response.Body.String(), `"added_count":1`) {
		t.Fatalf("status=%d key=%q body=%s", response.Code, service.idempotencyKey, response.Body.String())
	}
}

func TestGroupSyncUnavailableUsesRetryable503(t *testing.T) {
	service := &fakeGroupOperations{err: groups.ErrGatewayUnavailable}
	router := newGroupHTTPFixture(t, service)
	request := scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/groups/sync", "")
	request.Header.Set("Idempotency-Key", "group-sync-key-1")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusServiceUnavailable, "dependency_unavailable")
	if response.Header().Get("Retry-After") != "3" {
		t.Fatalf("headers=%v", response.Header())
	}
}

func TestGroupDetailPreservesOpaqueStringIDAndAllowsObserver(t *testing.T) {
	service := &fakeGroupOperations{group: groupHTTPFixture()}
	router := newGroupHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/groups/00123", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.id != "00123" || service.principal.Role != auth.RoleObserver {
		t.Fatalf("status=%d id=%q principal=%+v body=%s", response.Code, service.id, service.principal, response.Body.String())
	}
}

func newGroupHTTPFixture(t *testing.T, service GroupOperations) *Router {
	t.Helper()
	handlers, err := NewGroupHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router := newHTTPFixture(t)
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

func groupHTTPFixture() groups.Group {
	return groups.Group{
		ID: "00123", Name: "Alpha", MemberCount: 10, MaxMemberCount: 100, BotRole: groups.RoleAdmin,
		SnapshotState: groups.SnapshotFresh, LastSyncedAt: time.Unix(100, 0).UTC(),
		Features: []groups.Feature{{Key: groups.FeatureAIQA, Enabled: true, Source: groups.FeatureGroupOverride}},
	}
}

type fakeGroupOperations struct {
	page           groups.Page
	group          groups.Group
	syncResult     groups.SyncResult
	err            error
	calls          int
	query          groups.ListQuery
	id             string
	idempotencyKey string
	principal      auth.Principal
}

func (s *fakeGroupOperations) List(_ context.Context, principal auth.Principal, query groups.ListQuery) (groups.Page, error) {
	s.calls++
	s.principal, s.query = principal, query
	return s.page, s.err
}

func (s *fakeGroupOperations) Get(_ context.Context, principal auth.Principal, id string) (groups.Group, error) {
	s.calls++
	s.principal, s.id = principal, id
	return s.group, s.err
}

func (s *fakeGroupOperations) Sync(_ context.Context, principal auth.Principal, key string, _ auth.MutationContext) (groups.SyncResult, error) {
	s.calls++
	s.principal, s.idempotencyKey = principal, key
	return s.syncResult, s.err
}
