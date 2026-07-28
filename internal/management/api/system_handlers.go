package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	managersystem "github.com/zjutjh/jxh-go/internal/management/system"
)

type SystemOperations interface {
	Health(ctx context.Context, principal auth.Principal) (managersystem.Health, error)
	Configuration(ctx context.Context, principal auth.Principal) (managersystem.Configuration, error)
	UpdateConfiguration(ctx context.Context, principal auth.Principal, expectedVersion uint64, yaml string) (managersystem.Configuration, error)
	RestartNapCat(ctx context.Context, principal auth.Principal, input managersystem.RestartInput, idempotencyKey string, request ...auth.MutationContext) (managersystem.Operation, error)
}

type SystemHandlers struct {
	service SystemOperations
}

func NewSystemHandlers(service SystemOperations) (*SystemHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("system service is required")
	}
	return &SystemHandlers{service: service}, nil
}

func (h *SystemHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	if err := router.HandleFunc(http.MethodGet, "/api/admin/v1/system/health", RouteOptions{Permission: auth.PermissionSystemRead}, h.health); err != nil {
		return err
	}
	if err := router.HandleFunc(http.MethodGet, "/api/admin/v1/system/configuration", RouteOptions{Permission: auth.PermissionSystemRead}, h.configuration); err != nil {
		return err
	}
	if err := router.HandleFunc(http.MethodPatch, "/api/admin/v1/system/configuration", mutationRoute(auth.PermissionConfigWrite), h.updateConfiguration); err != nil {
		return err
	}
	return router.HandleFunc(http.MethodPost, "/api/admin/v1/system/napcat/restart", mutationRoute(auth.PermissionNapCatRestart), h.restart)
}

func (h *SystemHandlers) health(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	snapshot, err := h.service.Health(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	dependencies := make([]dependencyHealthDTO, len(snapshot.Dependencies))
	for index := range snapshot.Dependencies {
		dependencies[index] = mapDependencyHealth(snapshot.Dependencies[index])
	}
	writeJSON(w, http.StatusOK, systemHealthDTO{
		GeneratedAt: snapshot.GeneratedAt.UTC(), Liveness: "healthy",
		Readiness: mapReadiness(snapshot), Dependencies: dependencies,
	})
}

func (h *SystemHandlers) configuration(w http.ResponseWriter, r *http.Request) {
	identity, _ := AuthFromContext(r.Context())
	document, err := h.service.Configuration(r.Context(), principalFromAuth(identity))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, document.Version)
	writeJSON(w, http.StatusOK, mapSystemConfiguration(document))
}

type systemConfigurationPatchRequest struct {
	YAML string `json:"yaml"`
}

func (h *SystemHandlers) updateConfiguration(w http.ResponseWriter, r *http.Request) {
	version, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	var body systemConfigurationPatchRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	document, err := h.service.UpdateConfiguration(r.Context(), principalFromAuth(identity), version, body.YAML)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, document.Version)
	writeJSON(w, http.StatusOK, mapSystemConfiguration(document))
}

type napcatRestartRequest struct {
	Confirmation string `json:"confirmation"`
	Reason       string `json:"reason"`
}

func (h *SystemHandlers) restart(w http.ResponseWriter, r *http.Request) {
	idempotencyKey, ok := requiredIdempotencyKey(w, r)
	if !ok {
		return
	}
	var body napcatRestartRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	if body.Confirmation != "restart" {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "confirmation 必须为 restart", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	operation, err := h.service.RestartNapCat(r.Context(), principalFromAuth(identity), managersystem.RestartInput{
		Confirmation: body.Confirmation, Reason: body.Reason,
	}, idempotencyKey, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, mapSystemOperation(operation))
}

func (h *SystemHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, managersystem.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "没有执行系统操作的权限", nil, false)
	case errors.Is(err, managersystem.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "系统操作参数无效", nil, false)
	case errors.Is(err, managersystem.ErrIdempotencyConflict):
		writeAPIError(w, r, http.StatusConflict, CodeConflict, "Idempotency-Key 已用于不同请求", nil, false)
	case errors.Is(err, managersystem.ErrNapCatUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "NapCat 当前不可用", nil, true)
	case errors.Is(err, managersystem.ErrConfigurationVersionConflict):
		writeAPIError(w, r, http.StatusConflict, "resource_version_conflict", "配置文件已被其他操作修改", nil, false)
	case errors.Is(err, managersystem.ErrConfigurationUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "配置文件当前不可用", nil, true)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "服务器内部错误", nil, false)
	}
}

type dependencyHealthDTO struct {
	Key           managersystem.DependencyKey    `json:"key"`
	Status        managersystem.DependencyStatus `json:"status"`
	Configured    bool                           `json:"configured"`
	Required      bool                           `json:"required"`
	LatencyMS     *int64                         `json:"latency_ms"`
	LastCheckedAt *time.Time                     `json:"last_checked_at"`
	LastSuccessAt *time.Time                     `json:"last_success_at"`
	LastErrorAt   *time.Time                     `json:"last_error_at"`
	Message       *string                        `json:"message"`
}

type systemHealthDTO struct {
	GeneratedAt  time.Time             `json:"generated_at"`
	Liveness     string                `json:"liveness"`
	Readiness    string                `json:"readiness"`
	Dependencies []dependencyHealthDTO `json:"dependencies"`
}

type systemOperationDTO struct {
	ID          string                        `json:"operation_id"`
	Type        string                        `json:"type"`
	Status      managersystem.OperationStatus `json:"status"`
	RequestedAt time.Time                     `json:"requested_at"`
	CompletedAt *time.Time                    `json:"completed_at"`
	ErrorCode   *string                       `json:"error_code"`
}

type systemConfigurationDTO struct {
	YAML                 string   `json:"yaml"`
	Version              uint64   `json:"version"`
	MaskedFields         []string `json:"masked_fields"`
	EnvironmentOverrides []string `json:"environment_overrides"`
	RestartRequired      bool     `json:"restart_required"`
}

func mapDependencyHealth(value managersystem.DependencyHealth) dependencyHealthDTO {
	var latencyMS *int64
	if value.Latency != nil {
		milliseconds := value.Latency.Milliseconds()
		if milliseconds < 0 {
			milliseconds = 0
		}
		latencyMS = &milliseconds
	}
	return dependencyHealthDTO{
		Key: value.Key, Status: value.Status, Configured: value.Configured, Required: value.Required,
		LatencyMS: latencyMS, LastCheckedAt: utcTimePointer(value.LastCheckedAt), LastSuccessAt: utcTimePointer(value.LastSuccessAt),
		LastErrorAt: utcTimePointer(value.LastErrorAt), Message: value.Message,
	}
}

func mapReadiness(snapshot managersystem.Health) string {
	if snapshot.Ready {
		return "healthy"
	}
	if !snapshot.Live {
		return "unavailable"
	}
	for _, dependency := range snapshot.Dependencies {
		if dependency.Required && (dependency.Status == managersystem.DependencyUnavailable || dependency.Status == managersystem.DependencyUnknown ||
			dependency.Status == managersystem.DependencyNotConfigured) {
			return "unavailable"
		}
	}
	return "degraded"
}

func mapSystemOperation(value managersystem.Operation) systemOperationDTO {
	return systemOperationDTO{
		ID: value.ID, Type: value.Type, Status: value.Status, RequestedAt: value.RequestedAt.UTC(),
		CompletedAt: utcTimePointer(value.CompletedAt), ErrorCode: value.ErrorCode,
	}
}

func mapSystemConfiguration(value managersystem.Configuration) systemConfigurationDTO {
	return systemConfigurationDTO{
		YAML: value.YAML, Version: value.Version,
		MaskedFields:         append([]string{}, value.MaskedFields...),
		EnvironmentOverrides: append([]string{}, value.EnvironmentOverrides...),
		RestartRequired:      value.RestartRequired,
	}
}
