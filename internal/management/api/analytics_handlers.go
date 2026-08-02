package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/analytics"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type AnalyticsOperations interface {
	Summary(ctx context.Context, principal auth.Principal, query analytics.Query) (analytics.Summary, error)
	Timeseries(ctx context.Context, principal auth.Principal, query analytics.TimeseriesQuery) (analytics.Timeseries, error)
	Rankings(ctx context.Context, principal auth.Principal, query analytics.RankingsQuery) (analytics.Rankings, error)
	PrepareExport(ctx context.Context, principal auth.Principal, query analytics.ExportQuery) (*analytics.PreparedExport, error)
}

type AnalyticsHandlers struct {
	service AnalyticsOperations
}

func NewAnalyticsHandlers(service AnalyticsOperations) (*AnalyticsHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("analytics service is required")
	}
	return &AnalyticsHandlers{service: service}, nil
}

func (h *AnalyticsHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		pattern    string
		permission auth.Permission
		handler    http.HandlerFunc
	}{
		{"/api/admin/v1/analytics/summary", auth.PermissionAnalyticsRead, h.summary},
		{"/api/admin/v1/analytics/timeseries", auth.PermissionAnalyticsRead, h.timeseries},
		{"/api/admin/v1/analytics/rankings", auth.PermissionAnalyticsRead, h.rankings},
		{"/api/admin/v1/analytics/export", auth.PermissionAnalyticsExport, h.export},
	}
	for _, route := range routes {
		if err := router.HandleFunc(http.MethodGet, route.pattern, RouteOptions{Permission: route.permission}, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (h *AnalyticsHandlers) summary(w http.ResponseWriter, r *http.Request) {
	query, err := parseAnalyticsSummaryQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "analytics query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.Summary(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapAnalyticsSummary(value))
}

func (h *AnalyticsHandlers) timeseries(w http.ResponseWriter, r *http.Request) {
	query, err := parseAnalyticsTimeseriesQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "analytics timeseries query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.Timeseries(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapAnalyticsTimeseries(value))
}

func (h *AnalyticsHandlers) rankings(w http.ResponseWriter, r *http.Request) {
	query, err := parseAnalyticsRankingsQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "analytics rankings query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	value, err := h.service.Rankings(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapAnalyticsRankings(value))
}

func (h *AnalyticsHandlers) export(w http.ResponseWriter, r *http.Request) {
	query, err := parseAnalyticsExportQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "analytics export query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	prepared, err := h.service.PrepareExport(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	defer prepared.Close()
	metadata := prepared.Metadata()
	w.Header().Set("Content-Type", metadata.ContentType)
	w.Header().Set("Content-Disposition", "attachment; filename="+strconv.Quote(metadata.Filename))
	w.Header().Set("X-Export-Row-Count", strconv.Itoa(metadata.RowCount))
	w.WriteHeader(http.StatusOK)
	_ = prepared.WriteTo(r.Context(), w)
}

func (h *AnalyticsHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, analytics.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "analytics operation is forbidden", nil, false)
	case errors.Is(err, analytics.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "analytics input is invalid", nil, false)
	case errors.Is(err, analytics.ErrExportTooLarge):
		writeAPIError(w, r, http.StatusBadRequest, "export_too_large", "analytics export exceeds the row limit", nil, false)
	case errors.Is(err, analytics.ErrUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "analytics data is unavailable", nil, true)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

func parseAnalyticsSummaryQuery(values url.Values) (analytics.Query, error) {
	if !validAnalyticsQueryKeys(values, nil, nil) {
		return analytics.Query{}, analytics.ErrInvalidInput
	}
	return parseAnalyticsBaseQuery(values)
}

func parseAnalyticsTimeseriesQuery(values url.Values) (analytics.TimeseriesQuery, error) {
	if !validAnalyticsQueryKeys(values, map[string]bool{"granularity": true}, map[string]bool{"metric": true}) {
		return analytics.TimeseriesQuery{}, analytics.ErrInvalidInput
	}
	base, err := parseAnalyticsBaseQuery(values)
	if err != nil {
		return analytics.TimeseriesQuery{}, err
	}
	granularity := analytics.Granularity(values.Get("granularity"))
	if !validAnalyticsGranularity(granularity) {
		return analytics.TimeseriesQuery{}, analytics.ErrInvalidInput
	}
	rawMetrics, present := values["metric"]
	if !present || len(rawMetrics) < 1 || len(rawMetrics) > 10 {
		return analytics.TimeseriesQuery{}, analytics.ErrInvalidInput
	}
	metrics := make([]analytics.MetricKey, len(rawMetrics))
	seenMetrics := make(map[analytics.MetricKey]struct{}, len(rawMetrics))
	for index, value := range rawMetrics {
		metrics[index] = analytics.MetricKey(value)
		if !validAnalyticsMetric(metrics[index]) {
			return analytics.TimeseriesQuery{}, analytics.ErrInvalidInput
		}
		if _, duplicate := seenMetrics[metrics[index]]; duplicate {
			return analytics.TimeseriesQuery{}, analytics.ErrInvalidInput
		}
		seenMetrics[metrics[index]] = struct{}{}
	}
	return analytics.TimeseriesQuery{Query: base, Granularity: granularity, Metrics: metrics}, nil
}

func parseAnalyticsRankingsQuery(values url.Values) (analytics.RankingsQuery, error) {
	if !validAnalyticsQueryKeys(values, map[string]bool{"dimension": true, "metric": true, "page": true, "limit": true}, nil) {
		return analytics.RankingsQuery{}, analytics.ErrInvalidInput
	}
	base, err := parseAnalyticsBaseQuery(values)
	if err != nil {
		return analytics.RankingsQuery{}, err
	}
	dimension := analytics.Dimension(values.Get("dimension"))
	metric := analytics.MetricKey(values.Get("metric"))
	if !validAnalyticsDimension(dimension) || !validAnalyticsMetric(metric) {
		return analytics.RankingsQuery{}, analytics.ErrInvalidInput
	}
	limit := 20
	if raw, present := values["limit"]; present {
		parsed, parseErr := strconv.Atoi(raw[0])
		if parseErr != nil || parsed < 1 || parsed > 100 {
			return analytics.RankingsQuery{}, analytics.ErrInvalidInput
		}
		limit = parsed
	}
	page, err := ParsePage(values.Get("page"))
	if err != nil {
		return analytics.RankingsQuery{}, analytics.ErrInvalidInput
	}
	return analytics.RankingsQuery{Query: base, Dimension: dimension, Metric: metric, Page: page, Limit: limit}, nil
}

func parseAnalyticsExportQuery(values url.Values) (analytics.ExportQuery, error) {
	extra := map[string]bool{"dataset": true, "format": true, "granularity": true, "metric": true, "dimension": true}
	if !validAnalyticsQueryKeys(values, extra, nil) {
		return analytics.ExportQuery{}, analytics.ErrInvalidInput
	}
	base, err := parseAnalyticsBaseQuery(values)
	if err != nil {
		return analytics.ExportQuery{}, err
	}
	query := analytics.ExportQuery{
		Query: base, Dataset: analytics.Dataset(values.Get("dataset")), Format: analytics.ExportFormat(values.Get("format")),
		Granularity: analytics.Granularity(values.Get("granularity")), Metric: analytics.MetricKey(values.Get("metric")),
		Dimension: analytics.Dimension(values.Get("dimension")),
	}
	if !validAnalyticsDataset(query.Dataset) || (query.Format != analytics.ExportCSV && query.Format != analytics.ExportXLSX) {
		return analytics.ExportQuery{}, analytics.ErrInvalidInput
	}
	if query.Granularity != "" && !validAnalyticsGranularity(query.Granularity) ||
		query.Metric != "" && !validAnalyticsMetric(query.Metric) || query.Dimension != "" && !validAnalyticsDimension(query.Dimension) {
		return analytics.ExportQuery{}, analytics.ErrInvalidInput
	}
	if !validAnalyticsExportShape(query) {
		return analytics.ExportQuery{}, analytics.ErrInvalidInput
	}
	return query, nil
}

func parseAnalyticsBaseQuery(values url.Values) (analytics.Query, error) {
	query := analytics.Query{Timezone: values.Get("timezone")}
	if raw, present := values["from"]; present {
		parsed, err := ParseUTCTimestamp(raw[0])
		if err != nil {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		query.From = &parsed
	}
	if raw, present := values["to"]; present {
		parsed, err := ParseUTCTimestamp(raw[0])
		if err != nil {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		query.To = &parsed
	}
	if raw, present := values["timezone"]; present && (!utf8.ValidString(raw[0]) || utf8.RuneCountInString(raw[0]) < 1 || utf8.RuneCountInString(raw[0]) > 64) {
		return analytics.Query{}, analytics.ErrInvalidInput
	}
	if query.Timezone != "" {
		if query.Timezone == "Local" {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		if _, err := time.LoadLocation(query.Timezone); err != nil {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
	}
	seenGroupIDs := make(map[string]struct{}, len(values["group_id"]))
	for _, value := range values["group_id"] {
		id, err := ValidateOpaqueID(value)
		if err != nil {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		if _, duplicate := seenGroupIDs[id]; duplicate {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		seenGroupIDs[id] = struct{}{}
		query.GroupIDs = append(query.GroupIDs, id)
	}
	if len(query.GroupIDs) > 100 {
		return analytics.Query{}, analytics.ErrInvalidInput
	}
	seenFeatures := make(map[analytics.FeatureKey]struct{}, len(values["feature_key"]))
	for _, value := range values["feature_key"] {
		feature := analytics.FeatureKey(value)
		if !validAnalyticsFeature(feature) {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		if _, duplicate := seenFeatures[feature]; duplicate {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		seenFeatures[feature] = struct{}{}
		query.FeatureKeys = append(query.FeatureKeys, feature)
	}
	seenResults := make(map[analytics.Result]struct{}, len(values["result"]))
	for _, value := range values["result"] {
		result := analytics.Result(value)
		if !validAnalyticsResult(result) {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		if _, duplicate := seenResults[result]; duplicate {
			return analytics.Query{}, analytics.ErrInvalidInput
		}
		seenResults[result] = struct{}{}
		query.Results = append(query.Results, result)
	}
	return query, nil
}

func validAnalyticsQueryKeys(values url.Values, extraSingletons, extraRepeated map[string]bool) bool {
	commonSingletons := map[string]bool{"from": true, "to": true, "timezone": true}
	commonRepeated := map[string]bool{"group_id": true, "feature_key": true, "result": true}
	for key, entries := range values {
		singleton := commonSingletons[key] || extraSingletons[key]
		repeated := commonRepeated[key] || extraRepeated[key]
		if !singleton && !repeated || singleton && len(entries) != 1 || len(entries) == 0 {
			return false
		}
		for _, entry := range entries {
			if entry == "" {
				return false
			}
		}
	}
	return true
}

func validAnalyticsMetric(value analytics.MetricKey) bool {
	switch value {
	case analytics.MetricKeywordReplyCount, analytics.MetricKnowledgeTriggerCount, analytics.MetricAIRequestCount, analytics.MetricAISuccessRate,
		analytics.MetricAIDurationMS, analytics.MetricJoinRequestCount, analytics.MetricManualApprovalCount,
		analytics.MetricAutomaticApprovalCount, analytics.MetricScheduledJobRunCount, analytics.MetricGroupMessageCount,
		analytics.MetricCommandRunCount, analytics.MetricActiveUserCount, analytics.MetricLinkCleanCount,
		analytics.MetricQuoteSuccessCount, analytics.MetricQuoteFallbackCount, analytics.MetricQuoteFailureCount:
		return true
	default:
		return false
	}
}

func validAnalyticsFeature(value analytics.FeatureKey) bool {
	switch value {
	case analytics.FeatureKeywordReply, analytics.FeatureAIQA, analytics.FeatureQuote, analytics.FeatureLinkCleaner,
		analytics.FeatureWelcome, analytics.FeatureCustomCommand:
		return true
	default:
		return false
	}
}

func validAnalyticsResult(value analytics.Result) bool {
	switch value {
	case analytics.ResultSuccess, analytics.ResultFailed, analytics.ResultDenied, analytics.ResultUnknown,
		analytics.ResultFallback, analytics.ResultSkipped:
		return true
	default:
		return false
	}
}

func validAnalyticsGranularity(value analytics.Granularity) bool {
	return value == analytics.GranularityHour || value == analytics.GranularityDay
}

func validAnalyticsDimension(value analytics.Dimension) bool {
	return value == analytics.DimensionGroup || value == analytics.DimensionCommand || value == analytics.DimensionKnowledgeEntry
}

func validAnalyticsDataset(value analytics.Dataset) bool {
	switch value {
	case analytics.DatasetSummary, analytics.DatasetTimeseries, analytics.DatasetRankings,
		analytics.DatasetJoinRequests, analytics.DatasetScheduledJobRuns:
		return true
	default:
		return false
	}
}

func validAnalyticsExportShape(query analytics.ExportQuery) bool {
	switch query.Dataset {
	case analytics.DatasetSummary:
		return query.Granularity == "" && query.Metric == "" && query.Dimension == ""
	case analytics.DatasetTimeseries:
		return validAnalyticsGranularity(query.Granularity) && validAnalyticsMetric(query.Metric) && query.Dimension == ""
	case analytics.DatasetRankings:
		return query.Granularity == "" && validAnalyticsMetric(query.Metric) && validAnalyticsDimension(query.Dimension)
	case analytics.DatasetJoinRequests, analytics.DatasetScheduledJobRuns:
		return query.Granularity == "" && query.Metric == "" && query.Dimension == ""
	default:
		return false
	}
}

type analyticsWindowDTO struct {
	From     time.Time `json:"from"`
	To       time.Time `json:"to"`
	Timezone string    `json:"timezone"`
}

type analyticsMetricDTO struct {
	Key           analytics.MetricKey `json:"key"`
	Label         string              `json:"label"`
	Unit          analytics.Unit      `json:"unit"`
	Available     bool                `json:"available"`
	Value         *float64            `json:"value"`
	PreviousValue *float64            `json:"previous_value"`
	ChangePercent *float64            `json:"change_percent"`
}

type analyticsSummaryDTO struct {
	Window      analyticsWindowDTO   `json:"window"`
	Metrics     []analyticsMetricDTO `json:"metrics"`
	DataFreshAt time.Time            `json:"data_fresh_at"`
}

type analyticsPointDTO struct {
	BucketStart time.Time `json:"bucket_start"`
	Value       *float64  `json:"value"`
}

type analyticsSeriesDTO struct {
	Metric analytics.MetricKey `json:"metric"`
	Label  string              `json:"label"`
	Unit   analytics.Unit      `json:"unit"`
	Points []analyticsPointDTO `json:"points"`
}

type analyticsTimeseriesDTO struct {
	Window      analyticsWindowDTO    `json:"window"`
	Granularity analytics.Granularity `json:"granularity"`
	Series      []analyticsSeriesDTO  `json:"series"`
	DataFreshAt time.Time             `json:"data_fresh_at"`
}

type analyticsRankingItemDTO struct {
	Key         string  `json:"key"`
	DisplayName string  `json:"display_name"`
	Value       float64 `json:"value"`
	Rank        int     `json:"rank"`
}

type analyticsRankingsDTO struct {
	Window      analyticsWindowDTO        `json:"window"`
	Dimension   analytics.Dimension       `json:"dimension"`
	Metric      analytics.MetricKey       `json:"metric"`
	Unit        analytics.Unit            `json:"unit"`
	Items       []analyticsRankingItemDTO `json:"items"`
	TotalCount  int                       `json:"total_count"`
	DataFreshAt time.Time                 `json:"data_fresh_at"`
}

func mapAnalyticsWindow(value analytics.Window) analyticsWindowDTO {
	return analyticsWindowDTO{From: value.From.UTC(), To: value.To.UTC(), Timezone: value.Timezone}
}

func mapAnalyticsSummary(value analytics.Summary) analyticsSummaryDTO {
	metrics := make([]analyticsMetricDTO, len(value.Metrics))
	for index, metric := range value.Metrics {
		metrics[index] = analyticsMetricDTO{
			Key: metric.Key, Label: metric.Label, Unit: metric.Unit, Available: metric.Available,
			Value: metric.Value, PreviousValue: metric.PreviousValue, ChangePercent: metric.ChangePercent,
		}
	}
	return analyticsSummaryDTO{Window: mapAnalyticsWindow(value.Window), Metrics: metrics, DataFreshAt: value.DataFreshAt.UTC()}
}

func mapAnalyticsTimeseries(value analytics.Timeseries) analyticsTimeseriesDTO {
	series := make([]analyticsSeriesDTO, len(value.Series))
	for seriesIndex, source := range value.Series {
		points := make([]analyticsPointDTO, len(source.Points))
		for pointIndex, point := range source.Points {
			points[pointIndex] = analyticsPointDTO{BucketStart: point.BucketStart.UTC(), Value: point.Value}
		}
		series[seriesIndex] = analyticsSeriesDTO{Metric: source.Metric, Label: source.Label, Unit: source.Unit, Points: points}
	}
	return analyticsTimeseriesDTO{
		Window: mapAnalyticsWindow(value.Window), Granularity: value.Granularity, Series: series, DataFreshAt: value.DataFreshAt.UTC(),
	}
}

func mapAnalyticsRankings(value analytics.Rankings) analyticsRankingsDTO {
	items := make([]analyticsRankingItemDTO, len(value.Items))
	for index, item := range value.Items {
		items[index] = analyticsRankingItemDTO{Key: item.Key, DisplayName: item.DisplayName, Value: item.Value, Rank: item.Rank}
	}
	return analyticsRankingsDTO{
		Window: mapAnalyticsWindow(value.Window), Dimension: value.Dimension, Metric: value.Metric, Unit: value.Unit,
		Items: items, TotalCount: value.TotalCount, DataFreshAt: value.DataFreshAt.UTC(),
	}
}
