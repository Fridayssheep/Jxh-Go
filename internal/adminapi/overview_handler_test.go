package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/overview"
	managersystem "github.com/zjutjh/jxh-go/internal/system"
)

func TestOverviewReturnsContractShapeAndPreservesGroupID(t *testing.T) {
	value := 2.0
	service := &fakeOverviewReader{snapshot: overview.Snapshot{
		GeneratedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC), Range: overview.Range30Days, GroupID: "00123",
		Metrics: []overview.Metric{{
			Key: overview.MetricActiveGroups, Label: "Active groups", Available: true, Value: &value,
		}, {
			Key: overview.MetricCommandRunsToday, Label: "Command runs", Available: false,
		}},
		PendingItems: []overview.PendingItem{{
			Key: overview.PendingJoinRequests, Label: "Pending", Count: 3, Severity: overview.SeverityWarning,
		}},
		Dependencies: []overview.Dependency{{
			Key: managersystem.DependencyMySQL, Status: managersystem.DependencyHealthy,
		}},
		Trend: []overview.TrendPoint{{
			BucketStart: time.Date(2026, 7, 27, 0, 0, 0, 0, time.UTC), Values: map[string]float64{"command_run_count": 2},
		}},
	}}
	router := newOverviewHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/overview?range=30d&group_id=00123")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.query.Range != overview.Range30Days || service.query.GroupID != "00123" {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.query, response.Body.String())
	}
	body := response.Body.String()
	for _, expected := range []string{`"group_id":"00123"`, `"available":false`, `"value":null`, `"last_success_at":null`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %s: %s", expected, body)
		}
	}
}

func TestOverviewDefaultsRangeAndRejectsInvalidQueriesBeforeService(t *testing.T) {
	service := &fakeOverviewReader{snapshot: overview.Snapshot{Range: overview.Range7Days}}
	router := newOverviewHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/overview")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.query.Range != overview.Range7Days {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.query, response.Body.String())
	}
	baseline := service.calls
	for _, target := range []string{
		"/api/admin/v1/overview?range=90d",
		"/api/admin/v1/overview?unknown=x",
		"/api/admin/v1/overview?range=7d&range=30d",
		"/api/admin/v1/overview?group_id=" + strings.Repeat("x", 257),
	} {
		request = scheduledReadRequest(http.MethodGet, target)
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != baseline {
		t.Fatalf("invalid queries reached service: before=%d after=%d", baseline, service.calls)
	}
}

func TestOverviewRouteAllowsObserver(t *testing.T) {
	service := &fakeOverviewReader{snapshot: overview.Snapshot{Range: overview.Range7Days}}
	router := newOverviewHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/overview", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.principal.Role != auth.RoleObserver {
		t.Fatalf("status=%d principal=%+v body=%s", response.Code, service.principal, response.Body.String())
	}
}

func newOverviewHTTPFixture(t *testing.T, service OverviewReader) *Router {
	t.Helper()
	handler, err := NewOverviewHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	router := newHTTPFixture(t)
	if err := handler.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

type fakeOverviewReader struct {
	snapshot  overview.Snapshot
	err       error
	calls     int
	query     overview.Query
	principal auth.Principal
}

func (s *fakeOverviewReader) Get(_ context.Context, principal auth.Principal, query overview.Query) (overview.Snapshot, error) {
	s.calls++
	s.principal, s.query = principal, query
	return s.snapshot, s.err
}
