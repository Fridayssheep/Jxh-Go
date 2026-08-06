package adminapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type JoinRequestOperations interface {
	GetPolicy(context.Context, auth.Principal, string) (joinrequests.Policy, error)
	UpdatePolicy(context.Context, auth.Principal, string, uint64, joinrequests.PolicyPatch, auth.MutationContext) (joinrequests.Policy, error)
	GetStudentIDRule(context.Context, auth.Principal) (joinrequests.StudentIDRule, error)
	UpdateStudentIDRule(context.Context, auth.Principal, uint64, joinrequests.StudentIDRulePatch, auth.MutationContext) (joinrequests.StudentIDRule, error)
	List(context.Context, auth.Principal, joinrequests.ListQuery) (joinrequests.Page[joinrequests.Request], error)
	Get(context.Context, auth.Principal, string) (joinrequests.Request, error)
	ListDecisions(context.Context, auth.Principal, joinrequests.DecisionListQuery) (joinrequests.Page[joinrequests.Decision], error)
	Decide(context.Context, auth.Principal, string, uint64, joinrequests.DecisionInput, string, auth.MutationContext) (joinrequests.DecisionResult, error)
	BulkDecide(context.Context, auth.Principal, joinrequests.BulkInput, string, auth.MutationContext) (joinrequests.BulkResult, error)
	ListMajorEvidence(context.Context, auth.Principal) ([]joinrequests.EvidenceSummary, joinrequests.RuleState, error)
	GetAutomaticRuleConfiguration(auth.Principal) (joinrequests.AutomaticRuleConfiguration, error)
	ListMajorEvidenceSamples(context.Context, auth.Principal, joinrequests.EvidenceListQuery) (joinrequests.Page[joinrequests.EvidenceSample], error)
	UpdateMajorEvidenceSample(context.Context, auth.Principal, uint64, uint64, joinrequests.EvidenceSamplePatch, auth.MutationContext) (joinrequests.EvidenceSample, error)
	RebuildMajorEvidence(context.Context, auth.Principal, string, auth.MutationContext) (joinrequests.EvidenceRebuildResult, error)
	GetAdmissionRosterStatus(context.Context, auth.Principal) (joinrequests.AdmissionRosterStatus, error)
	ImportAdmissionRoster(context.Context, auth.Principal, string, []byte, string, auth.MutationContext) (joinrequests.AdmissionRosterStatus, error)
}

type JoinRequestHandlers struct {
	service JoinRequestOperations
}

func NewJoinRequestHandlers(service JoinRequestOperations) (*JoinRequestHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("join request service is required")
	}
	return &JoinRequestHandlers{service: service}, nil
}

func (h *JoinRequestHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/api/admin/v1/join-request-rules/student-id", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.getStudentIDRule},
		{http.MethodPatch, "/api/admin/v1/join-request-rules/student-id", mutationRoute(auth.PermissionJoinPoliciesWrite), h.updateStudentIDRule},
		{http.MethodGet, "/api/admin/v1/join-request-evidence/major-codes", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.listMajorEvidence},
		{http.MethodGet, "/api/admin/v1/join-request-evidence/samples", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.listMajorEvidenceSamples},
		{http.MethodPatch, "/api/admin/v1/join-request-evidence/samples/{sample_id}", mutationRoute(auth.PermissionJoinPoliciesWrite), h.updateMajorEvidenceSample},
		{http.MethodPost, "/api/admin/v1/join-request-evidence/rebuild", mutationRoute(auth.PermissionJoinPoliciesWrite), h.rebuildMajorEvidence},
		{http.MethodGet, "/api/admin/v1/admission-roster/status", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.getAdmissionRosterStatus},
		{http.MethodPost, "/api/admin/v1/admission-roster/import", mutationRoute(auth.PermissionJoinPoliciesWrite), h.importAdmissionRoster},
		{http.MethodGet, "/api/admin/v1/groups/{group_id}/join-request-policy", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.getPolicy},
		{http.MethodPatch, "/api/admin/v1/groups/{group_id}/join-request-policy", mutationRoute(auth.PermissionJoinPoliciesWrite), h.updatePolicy},
		{http.MethodGet, "/api/admin/v1/join-requests", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.list},
		{http.MethodPost, "/api/admin/v1/join-requests/bulk-decisions", mutationRoute(auth.PermissionJoinRequestsDecide), h.bulkDecide},
		{http.MethodGet, "/api/admin/v1/join-requests/{request_id}", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.get},
		{http.MethodGet, "/api/admin/v1/join-requests/{request_id}/decisions", RouteOptions{Permission: auth.PermissionJoinRequestsRead}, h.listDecisions},
		{http.MethodPost, "/api/admin/v1/join-requests/{request_id}/decisions", mutationRoute(auth.PermissionJoinRequestsDecide), h.decide},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

type ruleStateDTO struct {
	RuleVersion     uint64     `json:"rule_version"`
	Status          string     `json:"status"`
	EvidenceVersion uint64     `json:"evidence_version"`
	ActivatedAt     *time.Time `json:"activated_at"`
	RebuiltAt       *time.Time `json:"rebuilt_at"`
	Version         uint64     `json:"version"`
}

type evidenceSummaryDTO struct {
	EnrollmentYear string                    `json:"enrollment_year"`
	MajorCode      string                    `json:"major_code"`
	TotalSamples   uint64                    `json:"total_samples"`
	MajorCounts    []joinrequests.MajorCount `json:"major_counts"`
}

type automaticRuleConfigurationDTO struct {
	StudentIDLength      int    `json:"student_id_length"`
	EnrollmentYearOffset int    `json:"enrollment_year_offset"`
	EnrollmentYearLength int    `json:"enrollment_year_length"`
	MajorCodeOffset      int    `json:"major_code_offset"`
	MajorCodeLength      int    `json:"major_code_length"`
	CurrentYear          string `json:"current_year"`
	MinimumSamples       int    `json:"minimum_samples"`
}

type evidenceSampleDTO struct {
	SampleID       uint64                      `json:"sample_id"`
	EnrollmentYear string                      `json:"enrollment_year"`
	MajorCode      string                      `json:"major_code"`
	Major          string                      `json:"major"`
	ApprovalSource joinrequests.DecisionSource `json:"approval_source"`
	SourceGroupID  string                      `json:"source_group_id"`
	Active         bool                        `json:"active"`
	Version        uint64                      `json:"version"`
	UpdatedAt      time.Time                   `json:"updated_at"`
}

type admissionRosterStatusDTO struct {
	Configured     bool       `json:"configured"`
	DatasetVersion *string    `json:"dataset_version"`
	FileName       *string    `json:"file_name"`
	RowCount       uint64     `json:"row_count"`
	ActivatedAt    *time.Time `json:"activated_at"`
}

func mapRuleState(value joinrequests.RuleState) ruleStateDTO {
	return ruleStateDTO{RuleVersion: value.RuleVersion, Status: string(value.Status), EvidenceVersion: value.EvidenceVersion, ActivatedAt: value.ActivatedAt, RebuiltAt: value.RebuiltAt, Version: value.Version}
}

func mapEvidenceSample(value joinrequests.EvidenceSample) evidenceSampleDTO {
	return evidenceSampleDTO{SampleID: value.ID, EnrollmentYear: value.EnrollmentYear, MajorCode: value.MajorCode, Major: value.Major, ApprovalSource: value.ApprovalSource, SourceGroupID: value.SourceGroupID, Active: value.Active, Version: value.Version, UpdatedAt: value.UpdatedAt.UTC()}
}

func mapAdmissionRosterStatus(value joinrequests.AdmissionRosterStatus) admissionRosterStatusDTO {
	return admissionRosterStatusDTO{Configured: value.Configured, DatasetVersion: value.DatasetVersion, FileName: value.FileName, RowCount: value.RowCount, ActivatedAt: value.ActivatedAt}
}

func (h *JoinRequestHandlers) listMajorEvidence(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	items, state, err := h.service.ListMajorEvidence(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	rule, err := h.service.GetAutomaticRuleConfiguration(principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	mapped := make([]evidenceSummaryDTO, len(items))
	for index, item := range items {
		mapped[index] = evidenceSummaryDTO{EnrollmentYear: item.EnrollmentYear, MajorCode: item.MajorCode, TotalSamples: item.TotalSamples, MajorCounts: item.MajorCounts}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rule_state": mapRuleState(state),
		"rule": automaticRuleConfigurationDTO{
			StudentIDLength: rule.StudentIDLength, EnrollmentYearOffset: rule.EnrollmentYearOffset,
			EnrollmentYearLength: rule.EnrollmentYearLength, MajorCodeOffset: rule.MajorCodeOffset,
			MajorCodeLength: rule.MajorCodeLength, CurrentYear: rule.CurrentYear, MinimumSamples: rule.MinimumSamples,
		},
		"items": mapped,
	})
}

func (h *JoinRequestHandlers) listMajorEvidenceSamples(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r.URL.Query().Get("page"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "evidence sample query is invalid", nil, false)
		return
	}
	limit, err := ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "evidence sample query is invalid", nil, false)
		return
	}
	query := joinrequests.EvidenceListQuery{EnrollmentYear: r.URL.Query().Get("enrollment_year"), MajorCode: r.URL.Query().Get("major_code"), Page: page, Limit: limit}
	if active := r.URL.Query().Get("active"); active != "" {
		value, parseErr := strconv.ParseBool(active)
		if parseErr != nil {
			writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "evidence sample query is invalid", nil, false)
			return
		}
		query.Active = &value
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.ListMajorEvidenceSamples(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]evidenceSampleDTO, len(result.Items))
	for index, item := range result.Items {
		items[index] = mapEvidenceSample(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": result.HasMore, "total_count": result.TotalCount})
}

type evidenceSamplePatchRequest struct {
	Major  *string `json:"major"`
	Active *bool   `json:"active"`
}

func (h *JoinRequestHandlers) updateMajorEvidenceSample(w http.ResponseWriter, r *http.Request) {
	sampleID, err := strconv.ParseUint(r.PathValue("sample_id"), 10, 64)
	if err != nil || sampleID == 0 {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "evidence sample identifier is invalid", nil, false)
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	var body evidenceSamplePatchRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.UpdateMajorEvidenceSample(r.Context(), principalFromAuth(identity), sampleID, revision,
		joinrequests.EvidenceSamplePatch{Major: body.Major, Active: body.Active}, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, result.Version)
	writeJSON(w, http.StatusOK, mapEvidenceSample(result))
}

func (h *JoinRequestHandlers) rebuildMajorEvidence(w http.ResponseWriter, r *http.Request) {
	key, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.RebuildMajorEvidence(r.Context(), principalFromAuth(identity), key, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rule_state": mapRuleState(result.RuleState), "sample_count": result.SampleCount})
}

func (h *JoinRequestHandlers) getAdmissionRosterStatus(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.GetAdmissionRosterStatus(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapAdmissionRosterStatus(result))
}

func (h *JoinRequestHandlers) importAdmissionRoster(w http.ResponseWriter, r *http.Request) {
	key, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	if err := r.ParseMultipartForm(4 << 20); err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "admission roster upload is invalid", nil, false)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "admission roster file is required", nil, false)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "admission roster file is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.ImportAdmissionRoster(r.Context(), principalFromAuth(identity), header.Filename, data, key, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapAdmissionRosterStatus(result))
}

func (h *JoinRequestHandlers) getPolicy(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathIdentifier(w, r, "group_id")
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	policy, err := h.service.GetPolicy(r.Context(), principalFromAuth(identity), groupID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, policy.Version)
	writeJSON(w, http.StatusOK, mapJoinPolicy(policy))
}

type joinPolicyPatchRequest struct {
	Enabled    *bool `json:"enabled"`
	AutoReject *bool `json:"auto_reject"`
}

func (h *JoinRequestHandlers) updatePolicy(w http.ResponseWriter, r *http.Request) {
	groupID, ok := pathIdentifier(w, r, "group_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	var body joinPolicyPatchRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	if body.Enabled == nil && body.AutoReject == nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request policy patch is empty", nil, false)
		return
	}
	patch := joinrequests.PolicyPatch{}
	if body.Enabled != nil {
		patch.Enabled = auth.Field[bool]{Set: true, Value: *body.Enabled}
	}
	if body.AutoReject != nil {
		patch.AutoReject = auth.Field[bool]{Set: true, Value: *body.AutoReject}
	}
	identity, _ := AuthFromContext(r.Context())
	policy, err := h.service.UpdatePolicy(r.Context(), principalFromAuth(identity), groupID, revision,
		patch, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, policy.Version)
	writeJSON(w, http.StatusOK, mapJoinPolicy(policy))
}

func (h *JoinRequestHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseJoinRequestListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.List(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]joinRequestSummaryDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapJoinRequestSummary(page.Items[index])
	}
	writeJSON(w, http.StatusOK, joinRequestListDTO{
		Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore, TotalCount: page.TotalCount,
	})
}

func (h *JoinRequestHandlers) get(w http.ResponseWriter, r *http.Request) {
	requestID, ok := joinRequestPathID(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.Get(r.Context(), principalFromAuth(identity), requestID)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, value.Version)
	writeJSON(w, http.StatusOK, mapJoinRequest(value))
}

func (h *JoinRequestHandlers) listDecisions(w http.ResponseWriter, r *http.Request) {
	requestID, ok := joinRequestPathID(w, r)
	if !ok {
		return
	}
	query, err := parseJoinDecisionListQuery(requestID, r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request decision query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.ListDecisions(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]joinDecisionDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapJoinDecision(page.Items[index])
	}
	writeJSON(w, http.StatusOK, joinDecisionListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

type joinDecisionRequest struct {
	Action joinrequests.Action `json:"action"`
	Reason string              `json:"reason"`
}

func (h *JoinRequestHandlers) decide(w http.ResponseWriter, r *http.Request) {
	requestID, ok := joinRequestPathID(w, r)
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
	var body joinDecisionRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.Decide(r.Context(), principalFromAuth(identity), requestID, revision,
		joinrequests.DecisionInput{Action: body.Action, Reason: body.Reason}, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, result.Request.Version)
	status := http.StatusOK
	if result.Decision.Status == joinrequests.AttemptUnknown {
		status = http.StatusAccepted
	}
	writeJSON(w, status, mapJoinDecisionResult(result))
}

type bulkJoinDecisionItemRequest struct {
	RequestID string  `json:"request_id"`
	Version   *uint64 `json:"version"`
}

type bulkJoinDecisionRequest struct {
	GroupID string                        `json:"group_id"`
	Action  joinrequests.Action           `json:"action"`
	Reason  string                        `json:"reason"`
	Items   []bulkJoinDecisionItemRequest `json:"items"`
}

func (h *JoinRequestHandlers) bulkDecide(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	var body bulkJoinDecisionRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	items := make([]joinrequests.VersionedRequest, len(body.Items))
	for index, item := range body.Items {
		if item.Version == nil {
			writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request item version is required", nil, false)
			return
		}
		items[index] = joinrequests.VersionedRequest{ID: item.RequestID, Version: *item.Version}
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.BulkDecide(r.Context(), principalFromAuth(identity), joinrequests.BulkInput{
		GroupID: body.GroupID, Action: body.Action, Reason: body.Reason, Items: items,
	}, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapBulkJoinDecisionResult(result, RequestIDFromContext(r.Context())))
}

func (h *JoinRequestHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	var rosterValidation *joinrequests.AdmissionRosterValidationError
	switch {
	case errors.As(err, &rosterValidation):
		field := rosterValidation.Field
		if field == "" {
			field = "file"
		}
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "录取名单文件校验失败",
			map[string][]string{field: {rosterValidation.Error()}}, false)
	case errors.Is(err, joinrequests.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "join request operation is forbidden", nil, false)
	case errors.Is(err, joinrequests.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request input is invalid", nil, false)
	case errors.Is(err, joinrequests.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "join request does not exist", nil, false)
	case errors.Is(err, joinrequests.ErrIdempotencyConflict):
		writeAPIError(w, r, http.StatusConflict, "idempotency_key_reused", "idempotency key was used with different input", nil, false)
	case errors.Is(err, joinrequests.ErrConflict):
		writeAPIError(w, r, http.StatusConflict, "resource_state_conflict", "join request was changed or is already processing", nil, false)
	case errors.Is(err, joinrequests.ErrDependencyUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "NapCat is currently unavailable", nil, true)
	case errors.Is(err, joinrequests.ErrExternalFailure):
		writeAPIError(w, r, http.StatusBadGateway, "upstream_failure", "NapCat rejected the join request decision", nil, true)
	default:
		log.Printf("join request operation failed: request_id=%s: %v", RequestIDFromContext(r.Context()), err)
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

func parseJoinRequestListQuery(values url.Values) (joinrequests.ListQuery, error) {
	allowed := map[string]bool{
		"group_id": true, "decision_status": true, "observed_status": true, "ai_parse_status": true,
		"sub_type": true, "source": true, "decision_source": true, "requested_from": true, "requested_to": true,
		"overdue": true, "query": true, "sort": true, "page": true, "cursor": true, "limit": true,
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) == 0 || key != "decision_status" && len(entries) != 1 {
			return joinrequests.ListQuery{}, joinrequests.ErrInvalidInput
		}
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return joinrequests.ListQuery{}, err
	}
	page, err := ParsePage(values.Get("page"))
	if err != nil {
		return joinrequests.ListQuery{}, err
	}
	if page > 1 && values.Get("cursor") != "" {
		return joinrequests.ListQuery{}, joinrequests.ErrInvalidInput
	}
	query := joinrequests.ListQuery{
		GroupID: values.Get("group_id"), ObservedStatus: joinrequests.ObservedStatus(values.Get("observed_status")),
		AIParseStatus: joinrequests.AIParseStatus(values.Get("ai_parse_status")), SubType: joinrequests.SubType(values.Get("sub_type")),
		Source: joinrequests.RequestSource(values.Get("source")), DecisionSource: joinrequests.DecisionSource(values.Get("decision_source")),
		Query: values.Get("query"), Sort: joinrequests.Sort(values.Get("sort")), Cursor: values.Get("cursor"), Page: page, Limit: limit,
	}
	if query.Sort == "" {
		query.Sort = joinrequests.SortRequestedDesc
	}
	for _, value := range values["decision_status"] {
		if value == "" {
			return joinrequests.ListQuery{}, joinrequests.ErrInvalidInput
		}
		query.DecisionStatuses = append(query.DecisionStatuses, joinrequests.DecisionStatus(value))
	}
	if value := values.Get("requested_from"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return joinrequests.ListQuery{}, err
		}
		query.RequestedFrom = &parsed
	}
	if value := values.Get("requested_to"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return joinrequests.ListQuery{}, err
		}
		query.RequestedTo = &parsed
	}
	if value := values.Get("overdue"); value != "" {
		parsed, err := parseStrictBool(value)
		if err != nil {
			return joinrequests.ListQuery{}, err
		}
		query.Overdue = &parsed
	}
	if !validJoinRequestQuery(query) {
		return joinrequests.ListQuery{}, joinrequests.ErrInvalidInput
	}
	return query, nil
}

func validJoinRequestQuery(query joinrequests.ListQuery) bool {
	for _, status := range query.DecisionStatuses {
		switch status {
		case joinrequests.DecisionPending, joinrequests.DecisionProcessing, joinrequests.DecisionApproved,
			joinrequests.DecisionRejected, joinrequests.DecisionExternalProcessed, joinrequests.DecisionUnknown:
		default:
			return false
		}
	}
	if query.ObservedStatus != "" && query.ObservedStatus != joinrequests.ObservedPending && query.ObservedStatus != joinrequests.ObservedChecked {
		return false
	}
	switch query.AIParseStatus {
	case "", joinrequests.AIParsePending, joinrequests.AIParseRunning, joinrequests.AIParseSucceeded,
		joinrequests.AIParseFailed, joinrequests.AIParseSkipped:
	default:
		return false
	}
	if query.SubType != "" && query.SubType != joinrequests.SubTypeAdd && query.SubType != joinrequests.SubTypeInvite ||
		query.Source != "" && query.Source != joinrequests.RequestSourceEvent && query.Source != joinrequests.RequestSourceSystem ||
		query.DecisionSource != "" && query.DecisionSource != joinrequests.SourceManual &&
			query.DecisionSource != joinrequests.SourceAutomatic && query.DecisionSource != joinrequests.SourceExternal ||
		query.Sort != joinrequests.SortRequestedDesc && query.Sort != joinrequests.SortRequestedAsc ||
		!utf8.ValidString(query.Query) || utf8.RuneCountInString(query.Query) > 100 {
		return false
	}
	if query.RequestedFrom != nil && query.RequestedTo != nil && query.RequestedFrom.After(*query.RequestedTo) {
		return false
	}
	if query.Cursor != "" {
		if _, err := ValidateOpaqueID(query.Cursor); err != nil {
			return false
		}
	}
	return true
}

func parseJoinDecisionListQuery(requestID string, values url.Values) (joinrequests.DecisionListQuery, error) {
	if !validSingleQueryKeys(values, "cursor", "limit") {
		return joinrequests.DecisionListQuery{}, joinrequests.ErrInvalidInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return joinrequests.DecisionListQuery{}, err
	}
	return joinrequests.DecisionListQuery{RequestID: requestID, Cursor: values.Get("cursor"), Limit: limit}, nil
}

func joinRequestPathID(w http.ResponseWriter, r *http.Request) (string, bool) {
	value := r.PathValue("request_id")
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request identifier is invalid", nil, false)
		return "", false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "join request identifier is invalid", nil, false)
			return "", false
		}
	}
	return value, true
}

type joinPolicyDTO struct {
	GroupID        string         `json:"group_id"`
	Enabled        bool           `json:"enabled"`
	Mode           string         `json:"mode"`
	RequiredFields []string       `json:"required_fields"`
	AutoReject     bool           `json:"auto_reject"`
	Version        uint64         `json:"version"`
	UpdatedAt      time.Time      `json:"updated_at"`
	UpdatedBy      *auditActorDTO `json:"updated_by"`
}

type aiApplicantFieldsDTO struct {
	StudentID        *string  `json:"student_id"`
	Name             *string  `json:"name"`
	Major            *string  `json:"major"`
	Valid            bool     `json:"valid"`
	ValidationErrors []string `json:"validation_errors"`
}

type aiParseResultDTO struct {
	Status      joinrequests.AIParseStatus `json:"status"`
	Fields      *aiApplicantFieldsDTO      `json:"fields"`
	ErrorCode   *string                    `json:"error_code"`
	CompletedAt *time.Time                 `json:"completed_at"`
}

type joinGroupReferenceDTO struct {
	ID   string `json:"group_id"`
	Name string `json:"name"`
}

type joinRequestSummaryDTO struct {
	RequestID           string                        `json:"request_id"`
	Group               joinGroupReferenceDTO         `json:"group"`
	ApplicantQQ         string                        `json:"applicant_qq"`
	ApplicantNickname   *string                       `json:"applicant_nickname"`
	VerificationMessage string                        `json:"verification_message"`
	SubType             joinrequests.SubType          `json:"sub_type"`
	Source              joinrequests.RequestSource    `json:"source"`
	ObservedStatus      joinrequests.ObservedStatus   `json:"observed_status"`
	DecisionStatus      joinrequests.DecisionStatus   `json:"decision_status"`
	DecisionSource      *joinrequests.DecisionSource  `json:"decision_source"`
	AIParse             aiParseResultDTO              `json:"ai_parse"`
	StudentIDAssessment studentIDAssessmentDTO        `json:"student_id_assessment"`
	AutomaticReview     *joinrequests.AutomaticReview `json:"automatic_review"`
	RequestedAt         time.Time                     `json:"requested_at"`
	Overdue             bool                          `json:"overdue"`
	Version             uint64                        `json:"version"`
	LastDecisionID      *string                       `json:"last_decision_id"`
}

type joinRequestDTO struct {
	joinRequestSummaryDTO
	Comment         *string   `json:"comment"`
	FirstObservedAt time.Time `json:"first_observed_at"`
	LastObservedAt  time.Time `json:"last_observed_at"`
}

type joinRequestListDTO struct {
	Items      []joinRequestSummaryDTO `json:"items"`
	NextCursor *string                 `json:"next_cursor"`
	HasMore    bool                    `json:"has_more"`
	TotalCount int                     `json:"total_count"`
}

type joinDecisionDTO struct {
	DecisionID     string                        `json:"decision_id"`
	RequestID      string                        `json:"request_id"`
	Action         joinrequests.Action           `json:"action"`
	Source         joinrequests.DecisionSource   `json:"source"`
	Status         joinrequests.AttemptStatus    `json:"status"`
	Actor          *auditActorDTO                `json:"actor"`
	Reason         *string                       `json:"reason"`
	RuleVersion    *uint64                       `json:"rule_version"`
	FieldSnapshot  *aiApplicantFieldsDTO         `json:"field_snapshot"`
	ReviewSnapshot *joinrequests.AutomaticReview `json:"review_snapshot"`
	StartedAt      time.Time                     `json:"started_at"`
	CompletedAt    *time.Time                    `json:"completed_at"`
	ErrorCode      *string                       `json:"error_code"`
	TraceID        string                        `json:"trace_id"`
}

type joinDecisionListDTO struct {
	Items      []joinDecisionDTO `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

type joinDecisionResultDTO struct {
	JoinRequest joinRequestDTO  `json:"join_request"`
	Decision    joinDecisionDTO `json:"decision"`
}

type bulkJoinDecisionItemResultDTO struct {
	RequestID   string                   `json:"request_id"`
	Outcome     joinrequests.ItemOutcome `json:"outcome"`
	JoinRequest joinRequestSummaryDTO    `json:"join_request"`
	Decision    joinDecisionDTO          `json:"decision"`
	Error       *Error                   `json:"error"`
}

type bulkJoinDecisionResultDTO struct {
	GroupID        string                          `json:"group_id"`
	Action         joinrequests.Action             `json:"action"`
	Items          []bulkJoinDecisionItemResultDTO `json:"items"`
	ConfirmedCount uint64                          `json:"confirmed_count"`
	FailedCount    uint64                          `json:"failed_count"`
	UnknownCount   uint64                          `json:"unknown_count"`
}

func mapJoinPolicy(value joinrequests.Policy) joinPolicyDTO {
	return joinPolicyDTO{
		GroupID: value.GroupID, Enabled: value.Enabled, Mode: value.Mode,
		RequiredFields: append([]string(nil), value.RequiredFields...), AutoReject: value.AutoReject,
		Version: value.Version, UpdatedAt: value.UpdatedAt.UTC(), UpdatedBy: mapOptionalAuditActor(value.UpdatedBy),
	}
}

func mapApplicantFields(value *joinrequests.ApplicantFields) *aiApplicantFieldsDTO {
	if value == nil {
		return nil
	}
	return &aiApplicantFieldsDTO{
		StudentID: value.StudentID, Name: value.Name, Major: value.Major, Valid: value.Valid,
		ValidationErrors: append([]string{}, value.ValidationErrors...),
	}
}

func mapAIParse(value joinrequests.AIParseResult) aiParseResultDTO {
	return aiParseResultDTO{
		Status: value.Status, Fields: mapApplicantFields(value.Fields), ErrorCode: value.ErrorCode,
		CompletedAt: utcTimePointer(value.CompletedAt),
	}
}

func mapJoinRequestSummary(value joinrequests.Request) joinRequestSummaryDTO {
	return joinRequestSummaryDTO{
		RequestID: value.ID, Group: joinGroupReferenceDTO{ID: value.Group.ID, Name: value.Group.Name},
		ApplicantQQ: value.ApplicantQQ, ApplicantNickname: value.ApplicantNickname,
		VerificationMessage: value.VerificationMessage, SubType: value.SubType, Source: value.Source,
		ObservedStatus: value.ObservedStatus, DecisionStatus: value.DecisionStatus, DecisionSource: value.DecisionSource,
		AIParse: mapAIParse(value.AIParse), StudentIDAssessment: mapStudentIDAssessment(value.StudentIDAssessment),
		AutomaticReview: value.AutomaticReview,
		RequestedAt:     value.RequestedAt.UTC(), Overdue: value.Overdue,
		Version: value.Version, LastDecisionID: value.LastDecisionID,
	}
}

func mapJoinRequest(value joinrequests.Request) joinRequestDTO {
	return joinRequestDTO{
		joinRequestSummaryDTO: mapJoinRequestSummary(value), Comment: value.Comment,
		FirstObservedAt: value.FirstObservedAt.UTC(), LastObservedAt: value.LastObservedAt.UTC(),
	}
}

func mapJoinDecision(value joinrequests.Decision) joinDecisionDTO {
	return joinDecisionDTO{
		DecisionID: value.ID, RequestID: value.RequestID, Action: value.Action, Source: value.Source, Status: value.Status,
		Actor: mapOptionalAuditActor(value.Actor), Reason: value.Reason, RuleVersion: value.RuleVersion,
		FieldSnapshot: mapApplicantFields(value.FieldSnapshot), StartedAt: value.StartedAt.UTC(),
		ReviewSnapshot: value.ReviewSnapshot,
		CompletedAt:    utcTimePointer(value.CompletedAt), ErrorCode: value.ErrorCode, TraceID: value.TraceID,
	}
}

func mapJoinDecisionResult(value joinrequests.DecisionResult) joinDecisionResultDTO {
	return joinDecisionResultDTO{JoinRequest: mapJoinRequest(value.Request), Decision: mapJoinDecision(value.Decision)}
}

func mapBulkJoinDecisionResult(value joinrequests.BulkResult, traceID string) bulkJoinDecisionResultDTO {
	items := make([]bulkJoinDecisionItemResultDTO, len(value.Items))
	for index, item := range value.Items {
		items[index] = bulkJoinDecisionItemResultDTO{
			RequestID: item.RequestID, Outcome: item.Outcome, JoinRequest: mapJoinRequestSummary(item.Request),
			Decision: mapJoinDecision(item.Decision),
		}
		if item.Error != nil {
			items[index].Error = &Error{
				Code: item.Error.Code, Message: item.Error.Message, RequestID: traceID,
				Fields: map[string][]string{}, Retryable: item.Error.Retryable,
			}
		}
	}
	return bulkJoinDecisionResultDTO{
		GroupID: value.GroupID, Action: value.Action, Items: items,
		ConfirmedCount: value.ConfirmedCount, FailedCount: value.FailedCount, UnknownCount: value.UnknownCount,
	}
}
