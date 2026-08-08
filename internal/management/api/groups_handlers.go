package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type GroupOperations interface {
	List(ctx context.Context, principal auth.Principal, query groups.ListQuery) (groups.Page, error)
	Get(ctx context.Context, principal auth.Principal, id string) (groups.Group, error)
	Sync(ctx context.Context, principal auth.Principal, idempotencyKey string, request auth.MutationContext) (groups.SyncResult, error)
	PublishNotices(ctx context.Context, principal auth.Principal, input groups.NoticePublishInput, idempotencyKey string, request auth.MutationContext) (groups.NoticePublishResult, error)
}

type GroupHandlers struct {
	service GroupOperations
}

func NewGroupHandlers(service GroupOperations) (*GroupHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("group service is required")
	}
	return &GroupHandlers{service: service}, nil
}

func (h *GroupHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	if err := router.HandleFunc(http.MethodGet, "/api/admin/v1/groups", RouteOptions{Permission: auth.PermissionGroupsRead}, h.list); err != nil {
		return err
	}
	if err := router.HandleFunc(http.MethodPost, "/api/admin/v1/groups/sync", mutationRoute(auth.PermissionGroupsSync), h.sync); err != nil {
		return err
	}
	if err := router.HandleFunc(http.MethodPost, "/api/admin/v1/groups/notices", mutationRoute(auth.PermissionGroupNoticesWrite), h.publishNotices); err != nil {
		return err
	}
	return router.HandleFunc(http.MethodGet, "/api/admin/v1/groups/{group_id}", RouteOptions{Permission: auth.PermissionGroupsRead}, h.get)
}

func (h *GroupHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseGroupListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "group query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.List(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]groupDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapGroup(page.Items[index])
	}
	writeJSON(w, http.StatusOK, groupListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

func (h *GroupHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "group_id")
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	group, err := h.service.Get(r.Context(), principalFromAuth(identity), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapGroup(group))
}

func (h *GroupHandlers) sync(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.Sync(r.Context(), principalFromAuth(identity), idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapGroupSyncResult(result))
}

func (h *GroupHandlers) publishNotices(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	var body groupNoticePublishRequestDTO
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.PublishNotices(r.Context(), principalFromAuth(identity), groups.NoticePublishInput{
		GroupIDs: append([]string(nil), body.GroupIDs...), Content: body.Content,
	}, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapGroupNoticePublishResult(result))
}

func (h *GroupHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, groups.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "group operation is forbidden", nil, false)
	case errors.Is(err, groups.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "group input is invalid", nil, false)
	case errors.Is(err, groups.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "group does not exist", nil, false)
	case errors.Is(err, groups.ErrIdempotencyConflict):
		writeAPIError(w, r, http.StatusConflict, "idempotency_key_reused", "idempotency key was used with different input", nil, false)
	case errors.Is(err, groups.ErrConflict):
		writeAPIError(w, r, http.StatusConflict, CodeConflict, "group synchronization is already in progress", nil, false)
	case errors.Is(err, groups.ErrNoticeInProgress):
		writeAPIError(w, r, http.StatusConflict, CodeConflict, "group notice publication is already in progress", nil, true)
	case errors.Is(err, groups.ErrGatewayUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "NapCat is currently unavailable", nil, true)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

func parseGroupListQuery(values url.Values) (groups.ListQuery, error) {
	if !validSingleQueryKeys(values, "query", "bot_role", "snapshot_state", "feature_key", "feature_enabled", "cursor", "limit") {
		return groups.ListQuery{}, groups.ErrInvalidInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return groups.ListQuery{}, err
	}
	query := groups.ListQuery{
		Query: values.Get("query"), BotRole: groups.Role(values.Get("bot_role")),
		SnapshotState: groups.SnapshotState(values.Get("snapshot_state")), FeatureKey: groups.FeatureKey(values.Get("feature_key")),
		Cursor: values.Get("cursor"), Limit: limit,
	}
	if !utf8.ValidString(query.Query) || utf8.RuneCountInString(query.Query) > 100 {
		return groups.ListQuery{}, groups.ErrInvalidInput
	}
	if query.BotRole != "" && query.BotRole != groups.RoleOwner && query.BotRole != groups.RoleAdmin &&
		query.BotRole != groups.RoleMember && query.BotRole != groups.RoleUnknown {
		return groups.ListQuery{}, groups.ErrInvalidInput
	}
	if query.SnapshotState != "" && query.SnapshotState != groups.SnapshotFresh && query.SnapshotState != groups.SnapshotStale {
		return groups.ListQuery{}, groups.ErrInvalidInput
	}
	if query.FeatureKey != "" && !validGroupFeatureKey(query.FeatureKey) {
		return groups.ListQuery{}, groups.ErrInvalidInput
	}
	if value := values.Get("feature_enabled"); value != "" {
		if query.FeatureKey == "" {
			return groups.ListQuery{}, groups.ErrInvalidInput
		}
		parsed, err := parseStrictBool(value)
		if err != nil {
			return groups.ListQuery{}, err
		}
		query.FeatureEnabled = &parsed
	}
	if query.Cursor != "" {
		if _, err := ValidateOpaqueID(query.Cursor); err != nil {
			return groups.ListQuery{}, err
		}
	}
	return query, nil
}

func validGroupFeatureKey(value groups.FeatureKey) bool {
	switch value {
	case groups.FeatureKeywordReply, groups.FeatureAIQA, groups.FeatureQuote, groups.FeatureLinkCleaner,
		groups.FeatureWelcome, groups.FeatureCustomCommand:
		return true
	default:
		return false
	}
}

type groupFeatureDTO struct {
	Key     groups.FeatureKey    `json:"key"`
	Enabled bool                 `json:"enabled"`
	Source  groups.FeatureSource `json:"source"`
}

type groupJoinRequestPolicyDTO struct {
	Enabled    bool   `json:"enabled"`
	AutoReject bool   `json:"auto_reject"`
	Version    uint64 `json:"version"`
}

type groupDTO struct {
	ID                string                    `json:"group_id"`
	Name              string                    `json:"name"`
	MemberCount       uint64                    `json:"member_count"`
	MaxMemberCount    uint64                    `json:"max_member_count"`
	BotRole           groups.Role               `json:"bot_role"`
	SnapshotState     groups.SnapshotState      `json:"snapshot_state"`
	LastSyncedAt      time.Time                 `json:"last_synced_at"`
	Features          []groupFeatureDTO         `json:"features"`
	JoinRequestPolicy groupJoinRequestPolicyDTO `json:"join_request_policy"`
}

type groupListDTO struct {
	Items      []groupDTO `json:"items"`
	NextCursor *string    `json:"next_cursor"`
	HasMore    bool       `json:"has_more"`
}

type groupSyncResultDTO struct {
	SyncedAt     time.Time `json:"synced_at"`
	AddedCount   uint64    `json:"added_count"`
	UpdatedCount uint64    `json:"updated_count"`
	RemovedCount uint64    `json:"removed_count"`
	TotalCount   uint64    `json:"total_count"`
}

type groupNoticePublishRequestDTO struct {
	GroupIDs []string `json:"group_ids"`
	Content  string   `json:"content"`
}

type groupNoticePublishItemDTO struct {
	Group     groupReferenceDTO       `json:"group"`
	BotRole   groups.Role             `json:"bot_role"`
	Status    groups.NoticeItemStatus `json:"status"`
	ErrorCode string                  `json:"error_code,omitempty"`
}

type groupNoticePublishResultDTO struct {
	PublicationID  string                      `json:"publication_id"`
	Status         groups.NoticePublishStatus  `json:"status"`
	RequestedCount uint64                      `json:"requested_count"`
	PublishedCount uint64                      `json:"published_count"`
	DeniedCount    uint64                      `json:"denied_count"`
	FailedCount    uint64                      `json:"failed_count"`
	UnknownCount   uint64                      `json:"unknown_count"`
	Items          []groupNoticePublishItemDTO `json:"items"`
	CompletedAt    time.Time                   `json:"completed_at"`
}

type groupReferenceDTO struct {
	ID   string `json:"group_id"`
	Name string `json:"name"`
}

func mapGroup(value groups.Group) groupDTO {
	features := make([]groupFeatureDTO, len(value.Features))
	for index, feature := range value.Features {
		features[index] = groupFeatureDTO{Key: feature.Key, Enabled: feature.Enabled, Source: feature.Source}
	}
	return groupDTO{
		ID: value.ID, Name: value.Name, MemberCount: value.MemberCount, MaxMemberCount: value.MaxMemberCount,
		BotRole: value.BotRole, SnapshotState: value.SnapshotState, LastSyncedAt: value.LastSyncedAt.UTC(), Features: features,
		JoinRequestPolicy: groupJoinRequestPolicyDTO{
			Enabled: value.JoinRequestPolicy.Enabled, AutoReject: value.JoinRequestPolicy.AutoReject,
			Version: value.JoinRequestPolicy.Version,
		},
	}
}

func mapGroupSyncResult(value groups.SyncResult) groupSyncResultDTO {
	return groupSyncResultDTO{
		SyncedAt: value.SyncedAt.UTC(), AddedCount: value.AddedCount, UpdatedCount: value.UpdatedCount,
		RemovedCount: value.RemovedCount, TotalCount: value.TotalCount,
	}
}

func mapGroupNoticePublishResult(value groups.NoticePublishResult) groupNoticePublishResultDTO {
	items := make([]groupNoticePublishItemDTO, len(value.Items))
	for index, item := range value.Items {
		items[index] = groupNoticePublishItemDTO{
			Group: groupReferenceDTO{ID: item.Group.GroupID, Name: item.Group.Name}, BotRole: item.BotRole,
			Status: item.Status, ErrorCode: item.ErrorCode,
		}
	}
	return groupNoticePublishResultDTO{
		PublicationID: value.PublicationID, Status: value.Status, RequestedCount: value.RequestedCount,
		PublishedCount: value.PublishedCount, DeniedCount: value.DeniedCount, FailedCount: value.FailedCount,
		UnknownCount: value.UnknownCount, Items: items, CompletedAt: value.CompletedAt.UTC(),
	}
}
