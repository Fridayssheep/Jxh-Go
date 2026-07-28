package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/scheduledjobs"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type ScheduledJobOperations interface {
	Create(context.Context, auth.Principal, scheduledjobs.CreateInput, auth.MutationContext) (scheduledjobs.Job, error)
	Get(context.Context, auth.Principal, string) (scheduledjobs.Job, error)
	List(context.Context, auth.Principal, scheduledjobs.ListQuery) (scheduledjobs.Page[scheduledjobs.Job], error)
	Update(context.Context, auth.Principal, string, uint64, scheduledjobs.Patch, auth.MutationContext) (scheduledjobs.Job, error)
	Archive(context.Context, auth.Principal, string, uint64, auth.MutationContext) error
	TestSend(context.Context, auth.Principal, string, uint64, string, auth.MutationContext) (scheduledjobs.Run, error)
	ListRuns(context.Context, auth.Principal, scheduledjobs.RunListQuery) (scheduledjobs.Page[scheduledjobs.Run], error)
}

type ScheduledJobHandlers struct {
	service ScheduledJobOperations
}

func NewScheduledJobHandlers(service ScheduledJobOperations) (*ScheduledJobHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("scheduled job service is required")
	}
	return &ScheduledJobHandlers{service: service}, nil
}

func (h *ScheduledJobHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/api/admin/v1/scheduled-jobs", RouteOptions{Permission: auth.PermissionScheduledJobsRead}, h.list},
		{http.MethodPost, "/api/admin/v1/scheduled-jobs", mutationRoute(auth.PermissionScheduledJobsWrite), h.create},
		{http.MethodGet, "/api/admin/v1/scheduled-jobs/{job_id}", RouteOptions{Permission: auth.PermissionScheduledJobsRead}, h.get},
		{http.MethodPatch, "/api/admin/v1/scheduled-jobs/{job_id}", mutationRoute(auth.PermissionScheduledJobsWrite), h.update},
		{http.MethodDelete, "/api/admin/v1/scheduled-jobs/{job_id}", mutationRoute(auth.PermissionScheduledJobsWrite), h.archive},
		{http.MethodPost, "/api/admin/v1/scheduled-jobs/{job_id}/test-send", mutationRoute(auth.PermissionScheduledJobsWrite), h.testSend},
		{http.MethodGet, "/api/admin/v1/scheduled-jobs/{job_id}/runs", RouteOptions{Permission: auth.PermissionScheduledJobsRead}, h.listRuns},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

type scheduledJobCreateRequest struct {
	Name     string                 `json:"name"`
	GroupID  string                 `json:"group_id"`
	Message  string                 `json:"message"`
	Schedule scheduledScheduleInput `json:"schedule"`
	Enabled  *bool                  `json:"enabled"`
}

func (h *ScheduledJobHandlers) create(w http.ResponseWriter, r *http.Request) {
	var body scheduledJobCreateRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil || !body.Schedule.set {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "required scheduled job fields are missing", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	job, err := h.service.Create(r.Context(), principalFromAuth(identity), scheduledjobs.CreateInput{
		Name: body.Name, GroupID: body.GroupID, Message: body.Message, Schedule: body.Schedule.value, Enabled: *body.Enabled,
	}, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, job.Version)
	writeJSON(w, http.StatusCreated, mapScheduledJob(job))
}

func (h *ScheduledJobHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "job_id")
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	job, err := h.service.Get(r.Context(), principalFromAuth(identity), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, job.Version)
	writeJSON(w, http.StatusOK, mapScheduledJob(job))
}

func (h *ScheduledJobHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseScheduledJobListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "scheduled job query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.List(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]scheduledJobDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapScheduledJob(page.Items[index])
	}
	writeJSON(w, http.StatusOK, scheduledJobListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

type scheduledPatchField[T any] struct {
	Set   bool
	Value T
}

func (f *scheduledPatchField[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("null is not allowed")
	}
	return json.Unmarshal(data, &f.Value)
}

type scheduledJobPatchRequest struct {
	Name     scheduledPatchField[string]                 `json:"name"`
	GroupID  scheduledPatchField[string]                 `json:"group_id"`
	Message  scheduledPatchField[string]                 `json:"message"`
	Schedule scheduledPatchField[scheduledScheduleInput] `json:"schedule"`
	Status   scheduledPatchField[scheduledjobs.Status]   `json:"status"`
}

func (h *ScheduledJobHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "job_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	var body scheduledJobPatchRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	patch := scheduledjobs.Patch{
		Name:    auth.Field[string]{Set: body.Name.Set, Value: body.Name.Value},
		GroupID: auth.Field[string]{Set: body.GroupID.Set, Value: body.GroupID.Value},
		Message: auth.Field[string]{Set: body.Message.Set, Value: body.Message.Value},
		Status:  auth.Field[scheduledjobs.Status]{Set: body.Status.Set, Value: body.Status.Value},
	}
	if body.Schedule.Set {
		if !body.Schedule.Value.set {
			writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "scheduled job schedule is invalid", nil, false)
			return
		}
		patch.Schedule = auth.Field[scheduledjobs.Schedule]{Set: true, Value: body.Schedule.Value.value}
	}
	identity, _ := AuthFromContext(r.Context())
	job, err := h.service.Update(r.Context(), principalFromAuth(identity), id, revision, patch, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, job.Version)
	writeJSON(w, http.StatusOK, mapScheduledJob(job))
}

func (h *ScheduledJobHandlers) archive(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "job_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	if err := h.service.Archive(r.Context(), principalFromAuth(identity), id, revision, mutationContextFromRequest(r)); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ScheduledJobHandlers) testSend(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "job_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	run, err := h.service.TestSend(r.Context(), principalFromAuth(identity), id, revision, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	switch run.Result {
	case scheduledjobs.RunFailed:
		writeAPIError(w, r, http.StatusBadGateway, "upstream_failure", "upstream service did not complete the send", nil, true)
	case scheduledjobs.RunUnknown:
		writeJSON(w, http.StatusAccepted, mapScheduledJobRun(run))
	default:
		writeJSON(w, http.StatusOK, mapScheduledJobRun(run))
	}
}

func (h *ScheduledJobHandlers) listRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "job_id")
	if !ok {
		return
	}
	query, err := parseScheduledJobRunListQuery(id, r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "scheduled job run query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.ListRuns(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]scheduledJobRunDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapScheduledJobRun(page.Items[index])
	}
	writeJSON(w, http.StatusOK, scheduledJobRunListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

func (h *ScheduledJobHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, scheduledjobs.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "scheduled job operation is forbidden", nil, false)
	case errors.Is(err, scheduledjobs.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "scheduled job input is invalid", nil, false)
	case errors.Is(err, scheduledjobs.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "scheduled job does not exist", nil, false)
	case errors.Is(err, scheduledjobs.ErrConflict):
		writeAPIError(w, r, http.StatusConflict, "resource_version_conflict", "scheduled job was changed by another operation", nil, false)
	case errors.Is(err, scheduledjobs.ErrIdempotencyConflict):
		writeAPIError(w, r, http.StatusConflict, "idempotency_key_reused", "idempotency key was used with different input", nil, false)
	case errors.Is(err, scheduledjobs.ErrSenderUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "NapCat is currently unavailable", nil, true)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

type scheduledScheduleInput struct {
	set   bool
	value scheduledjobs.Schedule
}

func (s *scheduledScheduleInput) UnmarshalJSON(data []byte) error {
	var discriminator struct {
		Type scheduledjobs.JobType `json:"type"`
	}
	if err := json.Unmarshal(data, &discriminator); err != nil {
		return err
	}
	switch discriminator.Type {
	case scheduledjobs.TypeDaily:
		var value struct {
			Type      scheduledjobs.JobType `json:"type"`
			LocalTime string                `json:"local_time"`
			Timezone  string                `json:"timezone"`
		}
		if err := decodeStrictJSONBytes(data, &value); err != nil {
			return err
		}
		s.value = scheduledjobs.Schedule{Type: value.Type, LocalTime: value.LocalTime, Timezone: value.Timezone}
	case scheduledjobs.TypeOnce:
		var value struct {
			Type  scheduledjobs.JobType `json:"type"`
			RunAt string                `json:"run_at"`
		}
		if err := decodeStrictJSONBytes(data, &value); err != nil {
			return err
		}
		runAt, err := ParseUTCTimestamp(value.RunAt)
		if err != nil {
			return err
		}
		s.value = scheduledjobs.Schedule{Type: value.Type, RunAt: &runAt}
	default:
		return fmt.Errorf("unknown schedule type")
	}
	s.set = true
	return nil
}

func decodeStrictJSONBytes(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func parseScheduledJobListQuery(values url.Values) (scheduledjobs.ListQuery, error) {
	if !validSingleQueryKeys(values, "group_id", "type", "status", "run_result", "cursor", "limit") {
		return scheduledjobs.ListQuery{}, scheduledjobs.ErrInvalidInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return scheduledjobs.ListQuery{}, err
	}
	query := scheduledjobs.ListQuery{
		GroupID: values.Get("group_id"), Type: scheduledjobs.JobType(values.Get("type")),
		Status: scheduledjobs.Status(values.Get("status")), RunResult: scheduledjobs.RunResult(values.Get("run_result")),
		Cursor: values.Get("cursor"), Limit: limit,
	}
	if query.Type != "" && query.Type != scheduledjobs.TypeDaily && query.Type != scheduledjobs.TypeOnce {
		return scheduledjobs.ListQuery{}, scheduledjobs.ErrInvalidInput
	}
	if query.Status != "" && query.Status != scheduledjobs.StatusActive && query.Status != scheduledjobs.StatusPaused &&
		query.Status != scheduledjobs.StatusCompleted && query.Status != scheduledjobs.StatusArchived {
		return scheduledjobs.ListQuery{}, scheduledjobs.ErrInvalidInput
	}
	if query.RunResult != "" && !validScheduledRunResult(query.RunResult) {
		return scheduledjobs.ListQuery{}, scheduledjobs.ErrInvalidInput
	}
	return query, nil
}

func parseScheduledJobRunListQuery(jobID string, values url.Values) (scheduledjobs.RunListQuery, error) {
	if !validSingleQueryKeys(values, "kind", "result", "from", "to", "cursor", "limit") {
		return scheduledjobs.RunListQuery{}, scheduledjobs.ErrInvalidInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return scheduledjobs.RunListQuery{}, err
	}
	query := scheduledjobs.RunListQuery{
		JobID: jobID, Kind: scheduledjobs.RunKind(values.Get("kind")), Result: scheduledjobs.RunResult(values.Get("result")),
		Cursor: values.Get("cursor"), Limit: limit,
	}
	if query.Kind != "" && query.Kind != scheduledjobs.RunScheduled && query.Kind != scheduledjobs.RunTest {
		return scheduledjobs.RunListQuery{}, scheduledjobs.ErrInvalidInput
	}
	if query.Result != "" && !validScheduledRunResult(query.Result) {
		return scheduledjobs.RunListQuery{}, scheduledjobs.ErrInvalidInput
	}
	if value := values.Get("from"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return scheduledjobs.RunListQuery{}, err
		}
		query.From = &parsed
	}
	if value := values.Get("to"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return scheduledjobs.RunListQuery{}, err
		}
		query.To = &parsed
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return scheduledjobs.RunListQuery{}, scheduledjobs.ErrInvalidInput
	}
	return query, nil
}

func validScheduledRunResult(value scheduledjobs.RunResult) bool {
	return value == scheduledjobs.RunSuccess || value == scheduledjobs.RunFailed || value == scheduledjobs.RunUnknown || value == scheduledjobs.RunSkipped
}

type dailyScheduleDTO struct {
	Type      scheduledjobs.JobType `json:"type"`
	LocalTime string                `json:"local_time"`
	Timezone  string                `json:"timezone"`
}

type onceScheduleDTO struct {
	Type  scheduledjobs.JobType `json:"type"`
	RunAt time.Time             `json:"run_at"`
}

type scheduledGroupDTO struct {
	ID   string `json:"group_id"`
	Name string `json:"name"`
}

type scheduledJobDTO struct {
	ID            string                   `json:"job_id"`
	Name          string                   `json:"name"`
	Group         scheduledGroupDTO        `json:"group"`
	Message       string                   `json:"message"`
	Type          scheduledjobs.JobType    `json:"type"`
	Schedule      any                      `json:"schedule"`
	Status        scheduledjobs.Status     `json:"status"`
	NextRunAt     *time.Time               `json:"next_run_at"`
	LastRunAt     *time.Time               `json:"last_run_at"`
	LastRunResult *scheduledjobs.RunResult `json:"last_run_result"`
	Version       uint64                   `json:"version"`
	CreatedAt     time.Time                `json:"created_at"`
	UpdatedAt     time.Time                `json:"updated_at"`
	UpdatedBy     auditActorDTO            `json:"updated_by"`
}

type scheduledJobListDTO struct {
	Items      []scheduledJobDTO `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

type scheduledJobRunDTO struct {
	ID           string                  `json:"run_id"`
	JobID        string                  `json:"job_id"`
	Kind         scheduledjobs.RunKind   `json:"kind"`
	Result       scheduledjobs.RunResult `json:"result"`
	ScheduledFor *time.Time              `json:"scheduled_for"`
	StartedAt    time.Time               `json:"started_at"`
	CompletedAt  *time.Time              `json:"completed_at"`
	DurationMS   int64                   `json:"duration_ms"`
	MessageID    *string                 `json:"message_id"`
	ErrorCode    *string                 `json:"error_code"`
	ErrorMessage *string                 `json:"error_message"`
	TriggeredBy  *auditActorDTO          `json:"triggered_by"`
}

type scheduledJobRunListDTO struct {
	Items      []scheduledJobRunDTO `json:"items"`
	NextCursor *string              `json:"next_cursor"`
	HasMore    bool                 `json:"has_more"`
}

func mapScheduledJob(value scheduledjobs.Job) scheduledJobDTO {
	var schedule any
	if value.Schedule.Type == scheduledjobs.TypeOnce && value.Schedule.RunAt != nil {
		schedule = onceScheduleDTO{Type: value.Schedule.Type, RunAt: value.Schedule.RunAt.UTC()}
	} else {
		schedule = dailyScheduleDTO{Type: value.Schedule.Type, LocalTime: value.Schedule.LocalTime, Timezone: value.Schedule.Timezone}
	}
	return scheduledJobDTO{
		ID: value.ID, Name: value.Name, Group: scheduledGroupDTO{ID: value.Group.ID, Name: value.Group.Name}, Message: value.Message,
		Type: value.Type, Schedule: schedule, Status: value.Status, NextRunAt: utcTimePointer(value.NextRunAt),
		LastRunAt: utcTimePointer(value.LastRunAt), LastRunResult: value.LastRunResult, Version: value.Version,
		CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC(), UpdatedBy: mapAuditActor(value.UpdatedBy),
	}
}

func mapScheduledJobRun(value scheduledjobs.Run) scheduledJobRunDTO {
	duration := value.Duration.Milliseconds()
	if duration < 0 {
		duration = 0
	}
	var triggeredBy *auditActorDTO
	if value.TriggeredBy != nil {
		actor := mapAuditActor(*value.TriggeredBy)
		triggeredBy = &actor
	}
	return scheduledJobRunDTO{
		ID: value.ID, JobID: value.JobID, Kind: value.Kind, Result: value.Result,
		ScheduledFor: utcTimePointer(value.ScheduledFor), StartedAt: value.StartedAt.UTC(), CompletedAt: utcTimePointer(value.CompletedAt),
		DurationMS: duration, MessageID: value.MessageID, ErrorCode: value.ErrorCode, ErrorMessage: value.ErrorMessage, TriggeredBy: triggeredBy,
	}
}
