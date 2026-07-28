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

	"github.com/xuri/excelize/v2"
	"github.com/zjutjh/jxh-go/internal/management/analytics"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestAnalyticsSummaryHTTPParsesFiltersAndMapsContract(t *testing.T) {
	value := 7.0
	from := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 7, 8, 0, 0, 0, 0, time.UTC)
	service := &analyticsOperationsFake{summary: analytics.Summary{
		Window: analytics.Window{From: from, To: to, Timezone: "Asia/Shanghai"},
		Metrics: []analytics.Metric{{
			Key: analytics.MetricGroupMessageCount, Label: "Group messages", Unit: analytics.UnitCount, Available: true, Value: &value,
		}, {
			Key: analytics.MetricActiveUserCount, Label: "Active users", Unit: analytics.UnitCount,
		}},
		DataFreshAt: to,
	}}
	router := newAnalyticsHTTPFixture(t, service)
	target := "/api/admin/v1/analytics/summary?from=2026-07-01T00%3A00%3A00Z&to=2026-07-08T00%3A00%3A00Z" +
		"&group_id=00123&group_id=00456&feature_key=ai_qa&result=success&timezone=Asia%2FShanghai"
	request := scheduledReadRequest(http.MethodGet, target)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || service.summaryCalls != 1 || service.principal.Role != auth.RoleSuperAdmin {
		t.Fatalf("status=%d calls=%d principal=%+v body=%s", response.Code, service.summaryCalls, service.principal, response.Body.String())
	}
	query := service.summaryQuery
	if query.From == nil || !query.From.Equal(from) || query.To == nil || !query.To.Equal(to) || query.Timezone != "Asia/Shanghai" ||
		len(query.GroupIDs) != 2 || query.GroupIDs[0] != "00123" || query.FeatureKeys[0] != analytics.FeatureAIQA || query.Results[0] != analytics.ResultSuccess {
		t.Fatalf("query=%+v", query)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"timezone":"Asia/Shanghai"`, `"key":"group_message_count"`, `"value":7`,
		`"key":"active_user_count"`, `"available":false`, `"value":null`, `"previous_value":null`, `"change_percent":null`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("body missing %s: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"actor_hash", "user_key", "qq_user", "message_body", "token"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("summary leaked %q: %s", forbidden, body)
		}
	}
}

func TestAnalyticsTimeseriesAndRankingsHTTPParseLimits(t *testing.T) {
	pointValue := 3.0
	window := analytics.Window{From: analyticsHTTPTime(1), To: analyticsHTTPTime(3), Timezone: "UTC"}
	service := &analyticsOperationsFake{
		timeseries: analytics.Timeseries{
			Window: window, Granularity: analytics.GranularityHour, Series: []analytics.Series{{
				Metric: analytics.MetricAIRequestCount, Label: "AI requests", Unit: analytics.UnitCount,
				Points: []analytics.Point{{BucketStart: analyticsHTTPTime(2), Value: &pointValue}},
			}}, DataFreshAt: analyticsHTTPTime(3),
		},
		rankings: analytics.Rankings{
			Window: window, Dimension: analytics.DimensionGroup, Metric: analytics.MetricGroupMessageCount, Unit: analytics.UnitCount,
			Items: []analytics.RankingItem{{Key: "00123", DisplayName: "Group A", Value: 9, Rank: 1}}, DataFreshAt: analyticsHTTPTime(3),
		},
	}
	router := newAnalyticsHTTPFixture(t, service)

	request := scheduledReadRequest(http.MethodGet,
		"/api/admin/v1/analytics/timeseries?granularity=hour&metric=ai_request_count&metric=ai_success_rate&timezone=UTC")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(service.timeseriesQuery.Metrics) != 2 || service.timeseriesQuery.Granularity != analytics.GranularityHour ||
		!strings.Contains(response.Body.String(), `"bucket_start":"2026-07-28T02:00:00Z"`) {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.timeseriesQuery, response.Body.String())
	}

	request = scheduledReadRequest(http.MethodGet,
		"/api/admin/v1/analytics/rankings?dimension=group&metric=group_message_count&limit=25")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.rankingsQuery.Limit != 25 || service.rankingsQuery.Dimension != analytics.DimensionGroup ||
		!strings.Contains(response.Body.String(), `"rank":1`) {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.rankingsQuery, response.Body.String())
	}
}

func TestAnalyticsHTTPRejectsInvalidQueriesBeforeService(t *testing.T) {
	service := &analyticsOperationsFake{}
	router := newAnalyticsHTTPFixture(t, service)
	targets := []string{
		"/api/admin/v1/analytics/summary?unknown=x",
		"/api/admin/v1/analytics/summary?from=2026-07-01T00%3A00%3A00%2B08%3A00",
		"/api/admin/v1/analytics/summary?timezone=Not%2FAZone",
		"/api/admin/v1/analytics/summary?timezone=Local",
		"/api/admin/v1/analytics/summary?group_id=1&group_id=1",
		"/api/admin/v1/analytics/summary?feature_key=ai_qa&feature_key=ai_qa",
		"/api/admin/v1/analytics/summary?result=timeout",
		"/api/admin/v1/analytics/timeseries?metric=ai_request_count",
		"/api/admin/v1/analytics/timeseries?granularity=hour&metric=ai_request_count&metric=ai_request_count",
		"/api/admin/v1/analytics/rankings?dimension=user&metric=active_user_count",
		"/api/admin/v1/analytics/rankings?dimension=group&metric=group_message_count&limit=101",
		"/api/admin/v1/analytics/export?dataset=timeseries&format=csv&metric=group_message_count",
		"/api/admin/v1/analytics/export?dataset=summary&format=csv&metric=group_message_count",
		"/api/admin/v1/analytics/export?dataset=summary&format=pdf",
	}
	for _, target := range targets {
		request := scheduledReadRequest(http.MethodGet, target)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.totalCalls() != 0 {
		t.Fatalf("invalid queries reached service: %d", service.totalCalls())
	}
}

func TestAnalyticsRoutesEnforceAuthenticationPermissionAndAllowObserver(t *testing.T) {
	service := &analyticsOperationsFake{summary: analytics.Summary{}}
	router := newAnalyticsHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/analytics/summary", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusUnauthorized, CodeUnauthorized)
	if service.totalCalls() != 0 {
		t.Fatal("unauthenticated request reached service")
	}

	request = httptest.NewRequest(http.MethodGet, "/api/admin/v1/analytics/summary", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.principal.Role != auth.RoleObserver {
		t.Fatalf("status=%d principal=%+v body=%s", response.Code, service.principal, response.Body.String())
	}

	deniedRouter, err := NewRouter(MiddlewareOptions{
		PublicOrigin: "https://manager.example", MaxBodyBytes: 64,
		Random: bytes.NewReader(bytes.Repeat([]byte{2}, 128)), Authenticator: analyticsDeniedAuthenticator{},
	})
	if err != nil {
		t.Fatal(err)
	}
	handlers, err := NewAnalyticsHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	if err := handlers.Register(deniedRouter); err != nil {
		t.Fatal(err)
	}
	baseline := service.totalCalls()
	request = httptest.NewRequest(http.MethodGet, "/api/admin/v1/analytics/export?dataset=summary&format=csv", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "denied"})
	response = httptest.NewRecorder()
	deniedRouter.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if service.totalCalls() != baseline {
		t.Fatal("permission-denied request reached service")
	}
}

func TestAnalyticsCSVExportHTTPStreamsHeadersAndSafeBody(t *testing.T) {
	decisionSource := "automatic"
	store := &adminAnalyticsStore{joinRows: &adminJoinRows{rows: []analytics.JoinRequestExportRow{{
		RequestID: "request_1", GroupID: "00123", SubType: "invite", Source: "system", ObservedStatus: "checked",
		DecisionStatus: "approved", DecisionSource: &decisionSource, RequestedAt: analyticsHTTPTime(1), DecidedAt: analyticsHTTPTimePointer(2),
	}}}}
	service, err := analytics.NewService(analytics.Options{Store: store, Now: func() time.Time { return analyticsHTTPTime(12) }})
	if err != nil {
		t.Fatal(err)
	}
	router := newAnalyticsHTTPFixture(t, service)
	request := httptest.NewRequest(http.MethodGet,
		"/api/admin/v1/analytics/export?dataset=join_requests&format=csv&timezone=Asia%2FShanghai", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "observer"})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/csv; charset=utf-8" ||
		response.Header().Get("X-Export-Row-Count") != "1" ||
		response.Header().Get("Content-Disposition") != `attachment; filename="analytics_join_requests_20260728_200000.csv"` {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, "request_id,group_id") || !strings.Contains(body, "request_1,00123") || !strings.Contains(body, "+08:00") {
		t.Fatalf("body=%s", body)
	}
	for _, forbidden := range []string{"applicant_qq", "verification_message", "nickname", "actor_hash", "message_body", "token", "cookie"} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Fatalf("export leaked %q: %s", forbidden, body)
		}
	}
	if store.joinRows.(*adminJoinRows).closeCalls != 1 {
		t.Fatalf("close calls=%d", store.joinRows.(*adminJoinRows).closeCalls)
	}
}

func TestAnalyticsXLSXExportHTTPUsesOpenAPIContentType(t *testing.T) {
	metricValue := 5.0
	store := &adminAnalyticsStore{summary: analytics.SummaryData{
		Values: map[analytics.MetricKey]analytics.MetricValue{
			analytics.MetricCommandRunCount: {Available: true, Value: &metricValue},
		},
		DataFreshAt: analyticsHTTPTime(11),
	}}
	service, err := analytics.NewService(analytics.Options{Store: store, Now: func() time.Time { return analyticsHTTPTime(12) }})
	if err != nil {
		t.Fatal(err)
	}
	router := newAnalyticsHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/analytics/export?dataset=summary&format=xlsx")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" ||
		response.Header().Get("X-Export-Row-Count") != "15" {
		t.Fatalf("status=%d headers=%v body_length=%d", response.Code, response.Header(), response.Body.Len())
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(response.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	rows, err := workbook.GetRows("Analytics")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 16 || rows[0][0] != "metric" {
		t.Fatalf("rows=%d first=%v", len(rows), rows[0])
	}
}

func TestAnalyticsHTTPMapsServiceErrorsWithoutLeakingDetails(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"forbidden", analytics.ErrForbidden, http.StatusForbidden, CodeForbidden},
		{"invalid", analytics.ErrInvalidInput, http.StatusBadRequest, CodeBadRequest},
		{"too large", analytics.ErrExportTooLarge, http.StatusBadRequest, "export_too_large"},
		{"unavailable", analytics.ErrUnavailable, http.StatusServiceUnavailable, "dependency_unavailable"},
		{"internal", errors.New("database password secret"), http.StatusInternalServerError, CodeInternal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := &analyticsOperationsFake{err: test.err}
			router := newAnalyticsHTTPFixture(t, service)
			request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/analytics/export?dataset=summary&format=csv")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			assertErrorCode(t, response, test.status, test.code)
			if strings.Contains(response.Body.String(), "password") || strings.Contains(response.Body.String(), "secret") {
				t.Fatalf("error leaked: %s", response.Body.String())
			}
			if test.status == http.StatusServiceUnavailable && response.Header().Get("Retry-After") != "3" {
				t.Fatalf("Retry-After=%q", response.Header().Get("Retry-After"))
			}
		})
	}
}

func newAnalyticsHTTPFixture(t *testing.T, service AnalyticsOperations) *Router {
	t.Helper()
	handlers, err := NewAnalyticsHandlers(service)
	if err != nil {
		t.Fatal(err)
	}
	router := newHTTPFixture(t)
	if err := handlers.Register(router); err != nil {
		t.Fatal(err)
	}
	return router
}

type analyticsOperationsFake struct {
	summary    analytics.Summary
	timeseries analytics.Timeseries
	rankings   analytics.Rankings
	err        error

	principal       auth.Principal
	summaryQuery    analytics.Query
	timeseriesQuery analytics.TimeseriesQuery
	rankingsQuery   analytics.RankingsQuery
	exportQuery     analytics.ExportQuery
	summaryCalls    int
	timeCalls       int
	rankCalls       int
	exportCalls     int
}

func (s *analyticsOperationsFake) Summary(_ context.Context, principal auth.Principal, query analytics.Query) (analytics.Summary, error) {
	s.summaryCalls++
	s.principal, s.summaryQuery = principal, query
	return s.summary, s.err
}

func (s *analyticsOperationsFake) Timeseries(_ context.Context, principal auth.Principal, query analytics.TimeseriesQuery) (analytics.Timeseries, error) {
	s.timeCalls++
	s.principal, s.timeseriesQuery = principal, query
	return s.timeseries, s.err
}

func (s *analyticsOperationsFake) Rankings(_ context.Context, principal auth.Principal, query analytics.RankingsQuery) (analytics.Rankings, error) {
	s.rankCalls++
	s.principal, s.rankingsQuery = principal, query
	return s.rankings, s.err
}

func (s *analyticsOperationsFake) PrepareExport(_ context.Context, principal auth.Principal, query analytics.ExportQuery) (*analytics.PreparedExport, error) {
	s.exportCalls++
	s.principal, s.exportQuery = principal, query
	return nil, s.err
}

func (s *analyticsOperationsFake) totalCalls() int {
	return s.summaryCalls + s.timeCalls + s.rankCalls + s.exportCalls
}

type analyticsDeniedAuthenticator struct{}

func (analyticsDeniedAuthenticator) Authenticate(context.Context, string) (auth.AuthContext, error) {
	return auth.AuthContext{
		User: auth.User{ID: "usr_denied", Role: "invalid"}, Session: auth.Session{ID: "ses_denied", UserID: "usr_denied"},
	}, nil
}

type adminAnalyticsStore struct {
	summary  analytics.SummaryData
	joinRows analytics.JoinRequestExportRows
}

func (s *adminAnalyticsStore) LoadSummary(context.Context, analytics.Filter) (analytics.SummaryData, error) {
	return s.summary, nil
}

func (s *adminAnalyticsStore) LoadTimeseries(context.Context, analytics.StoreTimeseriesQuery) (analytics.TimeseriesData, error) {
	return analytics.TimeseriesData{}, nil
}

func (s *adminAnalyticsStore) LoadRankings(context.Context, analytics.StoreRankingsQuery) (analytics.RankingsData, error) {
	return analytics.RankingsData{}, nil
}

func (s *adminAnalyticsStore) OpenJoinRequestExport(context.Context, analytics.Filter) (analytics.JoinRequestExportRows, error) {
	return s.joinRows, nil
}

func (s *adminAnalyticsStore) OpenScheduledJobRunExport(context.Context, analytics.Filter) (analytics.ScheduledJobRunExportRows, error) {
	return nil, nil
}

type adminJoinRows struct {
	rows       []analytics.JoinRequestExportRow
	index      int
	closeCalls int
}

func (r *adminJoinRows) RowCount() int { return len(r.rows) }

func (r *adminJoinRows) Next(context.Context) (analytics.JoinRequestExportRow, bool, error) {
	if r.index >= len(r.rows) {
		return analytics.JoinRequestExportRow{}, false, nil
	}
	value := r.rows[r.index]
	r.index++
	return value, true, nil
}

func (r *adminJoinRows) Close() error {
	r.closeCalls++
	return nil
}

func analyticsHTTPTime(hour int) time.Time {
	return time.Date(2026, 7, 28, hour, 0, 0, 0, time.UTC)
}

func analyticsHTTPTimePointer(hour int) *time.Time {
	value := analyticsHTTPTime(hour)
	return &value
}
