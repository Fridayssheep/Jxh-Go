package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/knowledge/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type KnowledgeOperations interface {
	GetStatus(ctx context.Context, principal auth.Principal) (knowledgeadmin.Status, error)
	StartReload(ctx context.Context, principal auth.Principal, idempotencyKey string, request ...auth.MutationContext) (knowledgeadmin.ReloadOperation, error)
	ListEntries(ctx context.Context, principal auth.Principal, query knowledgeadmin.EntryQuery) (knowledgeadmin.EntryPage, error)
	GetEntry(ctx context.Context, principal auth.Principal, id string) (knowledgeadmin.Entry, error)
	ListConflicts(ctx context.Context, principal auth.Principal, query knowledgeadmin.ConflictQuery) (knowledgeadmin.ConflictPage, error)
}

type KnowledgeHandlers struct {
	service KnowledgeOperations
}

func NewKnowledgeHandlers(service KnowledgeOperations) (*KnowledgeHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("knowledge service is required")
	}
	return &KnowledgeHandlers{service: service}, nil
}

func (h *KnowledgeHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/api/admin/v1/knowledge/status", RouteOptions{Permission: auth.PermissionKnowledgeRead}, h.status},
		{http.MethodPost, "/api/admin/v1/knowledge/reload", mutationRoute(auth.PermissionKnowledgeReload), h.reload},
		{http.MethodGet, "/api/admin/v1/knowledge/entries", RouteOptions{Permission: auth.PermissionKnowledgeRead}, h.listEntries},
		{http.MethodGet, "/api/admin/v1/knowledge/entries/{entry_id}", RouteOptions{Permission: auth.PermissionKnowledgeRead}, h.getEntry},
		{http.MethodGet, "/api/admin/v1/knowledge/conflicts", RouteOptions{Permission: auth.PermissionKnowledgeRead}, h.listConflicts},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (h *KnowledgeHandlers) status(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	status, err := h.service.GetStatus(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapKnowledgeStatus(status))
}

func (h *KnowledgeHandlers) reload(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	operation, err := h.service.StartReload(r.Context(), principalFromAuth(identity), idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, mapKnowledgeOperation(operation))
}

func (h *KnowledgeHandlers) listEntries(w http.ResponseWriter, r *http.Request) {
	query, err := parseKnowledgeEntryQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "knowledge entry query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.ListEntries(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]knowledgeEntrySummaryDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapKnowledgeEntrySummary(page.Items[index])
	}
	writeJSON(w, http.StatusOK, knowledgeEntryListDTO{
		Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore,
	})
}

func (h *KnowledgeHandlers) getEntry(w http.ResponseWriter, r *http.Request) {
	id, err := ValidateOpaqueID(r.PathValue("entry_id"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "knowledge entry identifier is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	entry, err := h.service.GetEntry(r.Context(), principalFromAuth(identity), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapKnowledgeEntry(entry))
}

func (h *KnowledgeHandlers) listConflicts(w http.ResponseWriter, r *http.Request) {
	query, err := parseKnowledgeConflictQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "knowledge conflict query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.ListConflicts(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]knowledgeConflictDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapKnowledgeConflict(page.Items[index])
	}
	writeJSON(w, http.StatusOK, knowledgeConflictListDTO{
		Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore,
	})
}

func (h *KnowledgeHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, knowledgeadmin.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "knowledge operation is forbidden", nil, false)
	case errors.Is(err, knowledgeadmin.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "knowledge input is invalid", nil, false)
	case errors.Is(err, knowledgeadmin.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "knowledge entry does not exist", nil, false)
	case errors.Is(err, knowledgeadmin.ErrReloadInProgress):
		writeAPIError(w, r, http.StatusConflict, CodeConflict, "knowledge reload is already in progress", nil, false)
	case errors.Is(err, knowledgeadmin.ErrIdempotencyConflict):
		writeAPIError(w, r, http.StatusConflict, "idempotency_key_reused", "idempotency key was used with different input", nil, false)
	case errors.Is(err, knowledgeadmin.ErrReloaderUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "knowledge reloader is unavailable", nil, true)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

func parseKnowledgeEntryQuery(values url.Values) (knowledgeadmin.EntryQuery, error) {
	if !validSingleQueryKeys(values, "query", "category", "entry_type", "enabled", "exact_reply", "ai_enabled", "has_conflict", "cursor", "limit") {
		return knowledgeadmin.EntryQuery{}, knowledgeadmin.ErrInvalidInput
	}
	if !validQueryText(values.Get("query"), 200) || !validQueryText(values.Get("category"), 100) || !validQueryText(values.Get("cursor"), 2048) {
		return knowledgeadmin.EntryQuery{}, knowledgeadmin.ErrInvalidInput
	}
	entryType := knowledgeadmin.EntryType(values.Get("entry_type"))
	if _, present := values["entry_type"]; present && !validKnowledgeEntryType(entryType) {
		return knowledgeadmin.EntryQuery{}, knowledgeadmin.ErrInvalidInput
	}
	limit, err := parseKnowledgeLimit(values)
	if err != nil {
		return knowledgeadmin.EntryQuery{}, err
	}
	enabled, err := parseOptionalKnowledgeBool(values, "enabled")
	if err != nil {
		return knowledgeadmin.EntryQuery{}, err
	}
	exactReply, err := parseOptionalKnowledgeBool(values, "exact_reply")
	if err != nil {
		return knowledgeadmin.EntryQuery{}, err
	}
	aiEnabled, err := parseOptionalKnowledgeBool(values, "ai_enabled")
	if err != nil {
		return knowledgeadmin.EntryQuery{}, err
	}
	hasConflict, err := parseOptionalKnowledgeBool(values, "has_conflict")
	if err != nil {
		return knowledgeadmin.EntryQuery{}, err
	}
	return knowledgeadmin.EntryQuery{
		Query: values.Get("query"), Category: values.Get("category"), Type: entryType,
		Enabled: enabled, ExactReply: exactReply, AIEnabled: aiEnabled, HasConflict: hasConflict,
		Cursor: values.Get("cursor"), Limit: limit,
	}, nil
}

func parseKnowledgeConflictQuery(values url.Values) (knowledgeadmin.ConflictQuery, error) {
	if !validSingleQueryKeys(values, "query", "conflict_type", "cursor", "limit") ||
		!validQueryText(values.Get("query"), 200) || !validQueryText(values.Get("cursor"), 2048) {
		return knowledgeadmin.ConflictQuery{}, knowledgeadmin.ErrInvalidInput
	}
	conflictType := knowledgeadmin.ConflictType(values.Get("conflict_type"))
	if _, present := values["conflict_type"]; present && !validKnowledgeConflictType(conflictType) {
		return knowledgeadmin.ConflictQuery{}, knowledgeadmin.ErrInvalidInput
	}
	limit, err := parseKnowledgeLimit(values)
	if err != nil {
		return knowledgeadmin.ConflictQuery{}, err
	}
	return knowledgeadmin.ConflictQuery{
		Query: values.Get("query"), Type: conflictType, Cursor: values.Get("cursor"), Limit: limit,
	}, nil
}

func parseKnowledgeLimit(values url.Values) (int, error) {
	if entries, present := values["limit"]; present && entries[0] == "" {
		return 0, knowledgeadmin.ErrInvalidInput
	}
	return ParseLimit(values.Get("limit"))
}

func parseOptionalKnowledgeBool(values url.Values, key string) (*bool, error) {
	entries, present := values[key]
	if !present {
		return nil, nil
	}
	value, err := parseStrictBool(entries[0])
	if err != nil {
		return nil, knowledgeadmin.ErrInvalidInput
	}
	return &value, nil
}

func validKnowledgeEntryType(value knowledgeadmin.EntryType) bool {
	return value == knowledgeadmin.EntryTypeExactReply || value == knowledgeadmin.EntryTypeAIKnowledge || value == knowledgeadmin.EntryTypeHybrid
}

func validKnowledgeConflictType(value knowledgeadmin.ConflictType) bool {
	return value == knowledgeadmin.ConflictSourceKey || value == knowledgeadmin.ConflictKeyword || value == knowledgeadmin.ConflictAlias
}

func validQueryText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

type knowledgeReloadOperationDTO struct {
	ID          string                         `json:"operation_id"`
	Status      knowledgeadmin.OperationStatus `json:"status"`
	StartedAt   time.Time                      `json:"started_at"`
	CompletedAt *time.Time                     `json:"completed_at"`
	ErrorCode   *string                        `json:"error_code"`
}

type knowledgeStatusDTO struct {
	State              knowledgeadmin.State         `json:"state"`
	SourceConfigured   bool                         `json:"source_configured"`
	ActiveIndexVersion *string                      `json:"active_index_version"`
	EntryCount         int                          `json:"entry_count"`
	ConflictCount      int                          `json:"conflict_count"`
	LastAttemptAt      *time.Time                   `json:"last_attempt_at"`
	LastSuccessAt      *time.Time                   `json:"last_success_at"`
	LastErrorCode      *string                      `json:"last_error_code"`
	CurrentOperation   *knowledgeReloadOperationDTO `json:"current_operation"`
}

type knowledgeEntrySummaryDTO struct {
	ID              string                   `json:"entry_id"`
	Title           string                   `json:"title"`
	Category        string                   `json:"category"`
	Type            knowledgeadmin.EntryType `json:"entry_type"`
	Keywords        []string                 `json:"keywords"`
	Aliases         []string                 `json:"aliases"`
	Enabled         bool                     `json:"enabled"`
	ExactReply      bool                     `json:"exact_reply"`
	AIEnabled       bool                     `json:"ai_enabled"`
	HasConflict     bool                     `json:"has_conflict"`
	SourceUpdatedAt *time.Time               `json:"source_updated_at"`
	IndexedAt       time.Time                `json:"indexed_at"`
}

type knowledgeEntryDTO struct {
	ID              string                   `json:"entry_id"`
	SourceKey       string                   `json:"source_key"`
	Title           string                   `json:"title"`
	Category        string                   `json:"category"`
	Type            knowledgeadmin.EntryType `json:"entry_type"`
	Keywords        []string                 `json:"keywords"`
	Aliases         []string                 `json:"aliases"`
	Question        string                   `json:"question"`
	Answer          string                   `json:"answer"`
	Enabled         bool                     `json:"enabled"`
	ExactReply      bool                     `json:"exact_reply"`
	AIEnabled       bool                     `json:"ai_enabled"`
	HasConflict     bool                     `json:"has_conflict"`
	SourceUpdatedAt *time.Time               `json:"source_updated_at"`
	IndexedAt       time.Time                `json:"indexed_at"`
}

type knowledgeEntryListDTO struct {
	Items      []knowledgeEntrySummaryDTO `json:"items"`
	NextCursor *string                    `json:"next_cursor"`
	HasMore    bool                       `json:"has_more"`
}

type knowledgeConflictDTO struct {
	ID         string                      `json:"conflict_id"`
	Type       knowledgeadmin.ConflictType `json:"type"`
	Key        string                      `json:"key"`
	EntryIDs   []string                    `json:"entry_ids"`
	DetectedAt time.Time                   `json:"detected_at"`
}

type knowledgeConflictListDTO struct {
	Items      []knowledgeConflictDTO `json:"items"`
	NextCursor *string                `json:"next_cursor"`
	HasMore    bool                   `json:"has_more"`
}

func mapKnowledgeOperation(value knowledgeadmin.ReloadOperation) knowledgeReloadOperationDTO {
	return knowledgeReloadOperationDTO{
		ID: value.ID, Status: value.Status, StartedAt: value.StartedAt.UTC(),
		CompletedAt: utcTimePointer(value.CompletedAt), ErrorCode: value.ErrorCode,
	}
}

func mapKnowledgeStatus(value knowledgeadmin.Status) knowledgeStatusDTO {
	var operation *knowledgeReloadOperationDTO
	if value.CurrentOperation != nil {
		mapped := mapKnowledgeOperation(*value.CurrentOperation)
		operation = &mapped
	}
	return knowledgeStatusDTO{
		State: value.State, SourceConfigured: value.SourceConfigured, ActiveIndexVersion: value.ActiveIndexVersion,
		EntryCount: value.EntryCount, ConflictCount: value.ConflictCount,
		LastAttemptAt: utcTimePointer(value.LastAttemptAt), LastSuccessAt: utcTimePointer(value.LastSuccessAt),
		LastErrorCode: value.LastErrorCode, CurrentOperation: operation,
	}
}

func mapKnowledgeEntrySummary(value knowledgeadmin.EntrySummary) knowledgeEntrySummaryDTO {
	return knowledgeEntrySummaryDTO{
		ID: value.ID, Title: value.Title, Category: value.Category, Type: value.Type,
		Keywords: nonNilStrings(value.Keywords), Aliases: nonNilStrings(value.Aliases), Enabled: value.Enabled,
		ExactReply: value.ExactReply, AIEnabled: value.AIEnabled, HasConflict: value.HasConflict,
		SourceUpdatedAt: utcTimePointer(value.SourceUpdatedAt), IndexedAt: value.IndexedAt.UTC(),
	}
}

func mapKnowledgeEntry(value knowledgeadmin.Entry) knowledgeEntryDTO {
	return knowledgeEntryDTO{
		ID: value.ID, SourceKey: value.SourceKey, Title: value.Title, Category: value.Category, Type: value.Type,
		Keywords: nonNilStrings(value.Keywords), Aliases: nonNilStrings(value.Aliases), Question: value.Question, Answer: value.Answer,
		Enabled: value.Enabled, ExactReply: value.ExactReply, AIEnabled: value.AIEnabled, HasConflict: value.HasConflict,
		SourceUpdatedAt: utcTimePointer(value.SourceUpdatedAt), IndexedAt: value.IndexedAt.UTC(),
	}
}

func mapKnowledgeConflict(value knowledgeadmin.Conflict) knowledgeConflictDTO {
	return knowledgeConflictDTO{
		ID: value.ID, Type: value.Type, Key: value.Key, EntryIDs: nonNilStrings(value.EntryIDs), DetectedAt: value.DetectedAt.UTC(),
	}
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
