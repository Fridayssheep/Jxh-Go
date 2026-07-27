package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
)

type AuditReader interface {
	Get(ctx context.Context, principal auth.Principal, id string) (audit.Log, error)
	List(ctx context.Context, principal auth.Principal, query audit.ListQuery) (audit.Page, error)
}

type AuditHandlers struct {
	service AuditReader
}

func NewAuditHandlers(service AuditReader) (*AuditHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("audit service is required")
	}
	return &AuditHandlers{service: service}, nil
}

func (h *AuditHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	if err := router.HandleFunc(http.MethodGet, "/api/admin/v1/audit-logs", RouteOptions{Permission: auth.PermissionAuditRead}, h.list); err != nil {
		return err
	}
	return router.HandleFunc(http.MethodGet, "/api/admin/v1/audit-logs/{audit_log_id}", RouteOptions{Permission: auth.PermissionAuditRead}, h.get)
}

func (h *AuditHandlers) list(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	query, err := parseAuditListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "审计查询参数无效", nil, false)
		return
	}
	page, err := h.service.List(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]auditSummaryDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapAuditSummary(page.Items[index])
	}
	writeJSON(w, http.StatusOK, auditListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

func (h *AuditHandlers) get(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	id, err := ValidateOpaqueID(r.PathValue("audit_log_id"))
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "审计日志标识无效", nil, false)
		return
	}
	log, err := h.service.Get(r.Context(), principalFromAuth(identity), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapAuditLog(log))
}

func (h *AuditHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, audit.ErrInvalidQuery):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "审计查询参数无效", nil, false)
	case errors.Is(err, audit.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "没有读取审计日志的权限", nil, false)
	case errors.Is(err, audit.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "审计日志不存在", nil, false)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "服务器内部错误", nil, false)
	}
}

func parseAuditListQuery(values url.Values) (audit.ListQuery, error) {
	allowed := map[string]bool{
		"actor_user_id": true, "actor_type": true, "action": true, "target_type": true,
		"target_id": true, "result": true, "from": true, "to": true, "cursor": true, "limit": true,
	}
	for key, entries := range values {
		if !allowed[key] || len(entries) != 1 {
			return audit.ListQuery{}, audit.ErrInvalidQuery
		}
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return audit.ListQuery{}, err
	}
	query := audit.ListQuery{
		ActorUserID: values.Get("actor_user_id"), ActorType: audit.ActorType(values.Get("actor_type")),
		TargetID: values.Get("target_id"), Result: audit.Result(values.Get("result")),
		Cursor: values.Get("cursor"), Limit: limit,
	}
	if action := values.Get("action"); action != "" {
		query.Actions = []string{action}
	}
	if targetType := values.Get("target_type"); targetType != "" {
		query.TargetTypes = []string{targetType}
	}
	if value := values.Get("from"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return audit.ListQuery{}, err
		}
		query.From = &parsed
	}
	if value := values.Get("to"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return audit.ListQuery{}, err
		}
		query.To = &parsed
	}
	return query, nil
}

func principalFromAuth(identity auth.AuthContext) auth.Principal {
	return auth.Principal{UserID: identity.User.ID, SessionID: identity.Session.ID, Role: identity.User.Role}
}

type auditActorDTO struct {
	Type        audit.ActorType `json:"type"`
	UserID      *string         `json:"user_id"`
	QQUserID    *string         `json:"qq_user_id"`
	DisplayName string          `json:"display_name"`
}

type auditTargetDTO struct {
	Type        string  `json:"type"`
	ID          *string `json:"id"`
	DisplayName *string `json:"display_name"`
}

type auditSummaryDTO struct {
	ID         string         `json:"audit_log_id"`
	OccurredAt time.Time      `json:"occurred_at"`
	Actor      auditActorDTO  `json:"actor"`
	Action     string         `json:"action"`
	Target     auditTargetDTO `json:"target"`
	Result     audit.Result   `json:"result"`
	ErrorCode  *string        `json:"error_code"`
	RequestID  string         `json:"request_id"`
}

type auditLogDTO struct {
	auditSummaryDTO
	Source    audit.Source   `json:"source"`
	IPAddress *string        `json:"ip_address"`
	UserAgent *string        `json:"user_agent"`
	Before    map[string]any `json:"before"`
	After     map[string]any `json:"after"`
	Metadata  map[string]any `json:"metadata"`
	Redacted  bool           `json:"redacted"`
}

type auditListDTO struct {
	Items      []auditSummaryDTO `json:"items"`
	NextCursor *string           `json:"next_cursor"`
	HasMore    bool              `json:"has_more"`
}

func mapAuditActor(value audit.Actor) auditActorDTO {
	return auditActorDTO{Type: value.Type, UserID: value.UserID, QQUserID: value.QQUserID, DisplayName: value.DisplayName}
}

func mapAuditTarget(value audit.Target) auditTargetDTO {
	return auditTargetDTO{Type: value.Type, ID: nullableString(value.ID), DisplayName: nullableString(value.DisplayName)}
}

func mapAuditSummary(value audit.Summary) auditSummaryDTO {
	return auditSummaryDTO{
		ID: value.ID, OccurredAt: value.OccurredAt.UTC(), Actor: mapAuditActor(value.Actor), Action: value.Action,
		Target: mapAuditTarget(value.Target), Result: value.Result, ErrorCode: value.ErrorCode, RequestID: value.RequestID,
	}
}

func mapAuditLog(value audit.Log) auditLogDTO {
	metadata := value.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return auditLogDTO{
		auditSummaryDTO: auditSummaryDTO{
			ID: value.ID, OccurredAt: value.OccurredAt.UTC(), Actor: mapAuditActor(value.Actor), Action: value.Action,
			Target: mapAuditTarget(value.Target), Result: value.Result, ErrorCode: value.ErrorCode, RequestID: value.RequestID,
		},
		Source: value.Source, IPAddress: value.IPAddress, UserAgent: value.UserAgent,
		Before: value.Before, After: value.After, Metadata: metadata, Redacted: value.Redacted,
	}
}

func nullableString(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}
