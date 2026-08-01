package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/scheduledjobs"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestScheduledJobCreateMapsDailyScheduleAndContractDTO(t *testing.T) {
	service := &fakeScheduledJobOperations{job: scheduledJobFixture()}
	router := newScheduledJobHTTPFixture(t, service)
	request := scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/scheduled-jobs",
		`{"name":"Morning","group_id":"00123","message":"hello","schedule":{"type":"daily","local_time":"09:30","timezone":"Asia/Shanghai"},"enabled":true}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if service.create.GroupID != "00123" || !service.create.Enabled || service.create.Schedule.LocalTime != "09:30" {
		t.Fatalf("create=%+v", service.create)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	schedule, _ := body["schedule"].(map[string]any)
	if schedule["type"] != "daily" || schedule["run_at"] != nil || body["job_id"] != "job_1" {
		t.Fatalf("body=%v", body)
	}
}

func TestScheduledJobUpdateParsesOnceScheduleAndRequiresRevision(t *testing.T) {
	service := &fakeScheduledJobOperations{job: scheduledJobFixture()}
	router := newScheduledJobHTTPFixture(t, service)
	request := scheduledMutationRequest(t, http.MethodPatch, "/api/admin/v1/scheduled-jobs/job_1",
		`{"schedule":{"type":"once","run_at":"2026-07-29T01:00:00Z"},"status":"paused"}`)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusPreconditionRequired, CodePreconditionRequired)
	if service.calls != 0 {
		t.Fatal("missing If-Match reached service")
	}

	request = scheduledMutationRequest(t, http.MethodPatch, "/api/admin/v1/scheduled-jobs/job_1",
		`{"schedule":{"type":"once","run_at":"2026-07-29T01:00:00+00:00"},"status":"paused"}`)
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.revision != 3 || !service.patch.Schedule.Set ||
		service.patch.Schedule.Value.RunAt == nil || service.patch.Schedule.Value.RunAt.Location() != time.UTC {
		t.Fatalf("status=%d revision=%d patch=%+v body=%s", response.Code, service.revision, service.patch, response.Body.String())
	}
}

func TestScheduledJobPayloadRejectsMissingAndUnknownFields(t *testing.T) {
	service := &fakeScheduledJobOperations{job: scheduledJobFixture()}
	router := newScheduledJobHTTPFixture(t, service)
	targets := []string{
		`{"name":"Morning","group_id":"123","message":"hello","schedule":{"type":"daily","local_time":"09:30","timezone":"Asia/Shanghai"}}`,
		`{"name":"Morning","group_id":"123","message":"hello","schedule":{"type":"daily","local_time":"09:30","timezone":"Asia/Shanghai","extra":true},"enabled":true}`,
		`{"name":"Morning","group_id":"123","message":"hello","schedule":{"type":"weekly"},"enabled":true}`,
	}
	for _, body := range targets {
		request := scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/scheduled-jobs", body)
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
	if service.calls != 0 {
		t.Fatalf("invalid payloads reached service %d times", service.calls)
	}
}

func TestScheduledJobQueriesMapFiltersAndRejectInvalidValues(t *testing.T) {
	service := &fakeScheduledJobOperations{jobs: scheduledjobs.Page[scheduledjobs.Job]{Items: []scheduledjobs.Job{scheduledJobFixture()}}}
	router := newScheduledJobHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/scheduled-jobs?group_id=123&type=daily&status=active&run_result=success&cursor=job_0&limit=25")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.listQuery.Limit != 25 || service.listQuery.Type != scheduledjobs.TypeDaily {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.listQuery, response.Body.String())
	}
	baseline := service.calls
	for _, target := range []string{
		"/api/admin/v1/scheduled-jobs?type=weekly",
		"/api/admin/v1/scheduled-jobs?status=running",
		"/api/admin/v1/scheduled-jobs?limit=101",
		"/api/admin/v1/scheduled-jobs?type=daily&type=once",
		"/api/admin/v1/scheduled-jobs/job_1/runs?kind=manual",
		"/api/admin/v1/scheduled-jobs/job_1/runs?from=2026-07-30T00%3A00%3A00Z&to=2026-07-29T00%3A00%3A00Z",
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

func TestScheduledJobTestSendMapsOutcomeAndDependencyErrors(t *testing.T) {
	completedAt := time.Date(2026, 7, 28, 1, 0, 1, 0, time.UTC)
	cases := []struct {
		name       string
		result     scheduledjobs.RunResult
		err        error
		wantStatus int
		wantCode   string
	}{
		{name: "success", result: scheduledjobs.RunSuccess, wantStatus: http.StatusOK},
		{name: "unknown", result: scheduledjobs.RunUnknown, wantStatus: http.StatusAccepted},
		{name: "failed", result: scheduledjobs.RunFailed, wantStatus: http.StatusBadGateway, wantCode: "upstream_failure"},
		{name: "unavailable", err: scheduledjobs.ErrSenderUnavailable, wantStatus: http.StatusServiceUnavailable, wantCode: "dependency_unavailable"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service := &fakeScheduledJobOperations{run: scheduledjobs.Run{
				ID: "run_1", JobID: "job_1", Kind: scheduledjobs.RunTest, Result: test.result,
				StartedAt: completedAt.Add(-time.Second), CompletedAt: &completedAt, Duration: time.Second,
			}, err: test.err}
			router := newScheduledJobHTTPFixture(t, service)
			request := scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/scheduled-jobs/job_1/test-send", "")
			request.Header.Set("If-Match", `"3"`)
			request.Header.Set("Idempotency-Key", "test-send-key-1")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if test.wantCode != "" {
				assertErrorCode(t, response, test.wantStatus, test.wantCode)
			} else if response.Code != test.wantStatus || !strings.Contains(response.Body.String(), `"run_id":"run_1"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if service.idempotencyKey != "test-send-key-1" || service.revision != 3 {
				t.Fatalf("key=%q revision=%d", service.idempotencyKey, service.revision)
			}
		})
	}
}

func TestScheduledJobDeleteAndRunHistoryRoutes(t *testing.T) {
	completedAt := time.Date(2026, 7, 28, 1, 0, 1, 0, time.UTC)
	service := &fakeScheduledJobOperations{runs: scheduledjobs.Page[scheduledjobs.Run]{
		Items: []scheduledjobs.Run{{
			ID: "run_1", JobID: "job_1", Kind: scheduledjobs.RunScheduled, Result: scheduledjobs.RunSuccess,
			StartedAt: completedAt.Add(-1500 * time.Millisecond), CompletedAt: &completedAt, Duration: 1500 * time.Millisecond,
		}}, HasMore: false,
	}}
	router := newScheduledJobHTTPFixture(t, service)
	request := scheduledReadRequest(http.MethodGet, "/api/admin/v1/scheduled-jobs/job_1/runs?kind=scheduled&result=success&limit=10")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || service.runQuery.JobID != "job_1" || !strings.Contains(response.Body.String(), `"duration_ms":1500`) {
		t.Fatalf("status=%d query=%+v body=%s", response.Code, service.runQuery, response.Body.String())
	}

	request = scheduledMutationRequest(t, http.MethodDelete, "/api/admin/v1/scheduled-jobs/job_1", "")
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Body.Len() != 0 || service.deleteID != "job_1" {
		t.Fatalf("status=%d delete=%q body=%s", response.Code, service.deleteID, response.Body.String())
	}
}

func TestScheduledJobWriteRouteRejectsObserverBeforeService(t *testing.T) {
	service := &fakeScheduledJobOperations{job: scheduledJobFixture()}
	router := newScheduledJobHTTPFixture(t, service)
	request := scheduledMutationRequest(t, http.MethodPost, "/api/admin/v1/scheduled-jobs",
		`{"name":"Morning","group_id":"123","message":"hello","schedule":{"type":"daily","local_time":"09:30","timezone":"Asia/Shanghai"},"enabled":true}`)
	request.Header.Set("Cookie", SessionCookieName+"=observer")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	assertErrorCode(t, response, http.StatusForbidden, CodeForbidden)
	if service.calls != 0 {
		t.Fatal("observer request reached service")
	}
}

func newScheduledJobHTTPFixture(t *testing.T, service ScheduledJobOperations) *Router {
	t.Helper()
	handlers, err := NewScheduledJobHandlers(service)
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

func scheduledMutationRequest(t *testing.T, method, target, body string) *http.Request {
	t.Helper()
	return userMutationRequest(t, method, target, body)
}

func scheduledReadRequest(method, target string) *http.Request {
	request := httptest.NewRequest(method, target, nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	return request
}

func scheduledJobFixture() scheduledjobs.Job {
	userID := "usr_1"
	return scheduledjobs.Job{
		ID: "job_1", Name: "Morning", Group: scheduledjobs.Group{ID: "123", Name: "Test Group"}, Message: "hello",
		Type: scheduledjobs.TypeDaily, Schedule: scheduledjobs.Schedule{Type: scheduledjobs.TypeDaily, LocalTime: "09:30", Timezone: "Asia/Shanghai"},
		Status: scheduledjobs.StatusActive, Version: 3,
		CreatedAt: time.Date(2026, 7, 27, 1, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC),
		UpdatedBy: audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: "Admin"},
	}
}

type fakeScheduledJobOperations struct {
	job            scheduledjobs.Job
	jobs           scheduledjobs.Page[scheduledjobs.Job]
	run            scheduledjobs.Run
	runs           scheduledjobs.Page[scheduledjobs.Run]
	err            error
	calls          int
	create         scheduledjobs.CreateInput
	patch          scheduledjobs.Patch
	listQuery      scheduledjobs.ListQuery
	runQuery       scheduledjobs.RunListQuery
	revision       uint64
	idempotencyKey string
	deleteID       string
}

func (s *fakeScheduledJobOperations) Create(_ context.Context, _ auth.Principal, input scheduledjobs.CreateInput, _ auth.MutationContext) (scheduledjobs.Job, error) {
	s.calls++
	s.create = input
	return s.job, s.err
}

func (s *fakeScheduledJobOperations) Get(context.Context, auth.Principal, string) (scheduledjobs.Job, error) {
	s.calls++
	return s.job, s.err
}

func (s *fakeScheduledJobOperations) List(_ context.Context, _ auth.Principal, query scheduledjobs.ListQuery) (scheduledjobs.Page[scheduledjobs.Job], error) {
	s.calls++
	s.listQuery = query
	return s.jobs, s.err
}

func (s *fakeScheduledJobOperations) Update(_ context.Context, _ auth.Principal, _ string, revision uint64, patch scheduledjobs.Patch, _ auth.MutationContext) (scheduledjobs.Job, error) {
	s.calls++
	s.revision, s.patch = revision, patch
	return s.job, s.err
}

func (s *fakeScheduledJobOperations) Delete(_ context.Context, _ auth.Principal, id string, revision uint64, _ auth.MutationContext) error {
	s.calls++
	s.deleteID, s.revision = id, revision
	return s.err
}

func (s *fakeScheduledJobOperations) TestSend(_ context.Context, _ auth.Principal, _ string, revision uint64, key string, _ auth.MutationContext) (scheduledjobs.Run, error) {
	s.calls++
	s.revision, s.idempotencyKey = revision, key
	return s.run, s.err
}

func (s *fakeScheduledJobOperations) ListRuns(_ context.Context, _ auth.Principal, query scheduledjobs.RunListQuery) (scheduledjobs.Page[scheduledjobs.Run], error) {
	s.calls++
	s.runQuery = query
	return s.runs, s.err
}
