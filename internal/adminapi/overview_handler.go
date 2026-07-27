package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/overview"
	managersystem "github.com/zjutjh/jxh-go/internal/system"
)

type OverviewReader interface {
	Get(ctx context.Context, principal auth.Principal, query overview.Query) (overview.Snapshot, error)
}

type OverviewHandler struct {
	service OverviewReader
}

func NewOverviewHandler(service OverviewReader) (*OverviewHandler, error) {
	if service == nil {
		return nil, fmt.Errorf("overview service is required")
	}
	return &OverviewHandler{service: service}, nil
}

func (h *OverviewHandler) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	return router.HandleFunc(http.MethodGet, "/api/admin/v1/overview", RouteOptions{Permission: auth.PermissionOverviewRead}, h.get)
}

func (h *OverviewHandler) get(w http.ResponseWriter, r *http.Request) {
	query, err := parseOverviewQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "overview query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	snapshot, err := h.service.Get(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapOverview(snapshot))
}

func (h *OverviewHandler) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, overview.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "overview access is forbidden", nil, false)
	case errors.Is(err, overview.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "overview query is invalid", nil, false)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

func parseOverviewQuery(values url.Values) (overview.Query, error) {
	if !validSingleQueryKeys(values, "range", "group_id") {
		return overview.Query{}, overview.ErrInvalidInput
	}
	query := overview.Query{Range: overview.Range(values.Get("range")), GroupID: values.Get("group_id")}
	if query.Range == "" {
		query.Range = overview.Range7Days
	}
	if query.Range != overview.Range7Days && query.Range != overview.Range30Days {
		return overview.Query{}, overview.ErrInvalidInput
	}
	if query.GroupID != "" {
		if _, err := ValidateOpaqueID(query.GroupID); err != nil {
			return overview.Query{}, err
		}
	}
	return query, nil
}

type overviewMetricDTO struct {
	Key           overview.MetricKey `json:"key"`
	Label         string             `json:"label"`
	Available     bool               `json:"available"`
	Value         *float64           `json:"value"`
	ChangePercent *float64           `json:"change_percent"`
}

type overviewPendingDTO struct {
	Key      overview.PendingKey `json:"key"`
	Label    string              `json:"label"`
	Count    uint64              `json:"count"`
	Severity overview.Severity   `json:"severity"`
}

type overviewDependencyDTO struct {
	Key           managersystem.DependencyKey    `json:"key"`
	Status        managersystem.DependencyStatus `json:"status"`
	LastSuccessAt *time.Time                     `json:"last_success_at"`
}

type overviewTrendDTO struct {
	BucketStart time.Time          `json:"bucket_start"`
	Values      map[string]float64 `json:"values"`
}

type overviewDTO struct {
	GeneratedAt  time.Time               `json:"generated_at"`
	Range        overview.Range          `json:"range"`
	GroupID      *string                 `json:"group_id"`
	Metrics      []overviewMetricDTO     `json:"metrics"`
	PendingItems []overviewPendingDTO    `json:"pending_items"`
	Dependencies []overviewDependencyDTO `json:"dependencies"`
	Trend        []overviewTrendDTO      `json:"trend"`
}

func mapOverview(value overview.Snapshot) overviewDTO {
	metrics := make([]overviewMetricDTO, len(value.Metrics))
	for index, metric := range value.Metrics {
		metrics[index] = overviewMetricDTO{
			Key: metric.Key, Label: metric.Label, Available: metric.Available,
			Value: metric.Value, ChangePercent: metric.ChangePercent,
		}
	}
	pending := make([]overviewPendingDTO, len(value.PendingItems))
	for index, item := range value.PendingItems {
		pending[index] = overviewPendingDTO{Key: item.Key, Label: item.Label, Count: item.Count, Severity: item.Severity}
	}
	dependencies := make([]overviewDependencyDTO, len(value.Dependencies))
	for index, dependency := range value.Dependencies {
		dependencies[index] = overviewDependencyDTO{
			Key: dependency.Key, Status: dependency.Status, LastSuccessAt: utcTimePointer(dependency.LastSuccessAt),
		}
	}
	trend := make([]overviewTrendDTO, len(value.Trend))
	for index, point := range value.Trend {
		trend[index] = overviewTrendDTO{BucketStart: point.BucketStart.UTC(), Values: point.Values}
	}
	return overviewDTO{
		GeneratedAt: value.GeneratedAt.UTC(), Range: value.Range, GroupID: nullableString(value.GroupID),
		Metrics: metrics, PendingItems: pending, Dependencies: dependencies, Trend: trend,
	}
}
