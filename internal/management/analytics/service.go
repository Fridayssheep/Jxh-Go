package analytics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/auth"
)

const (
	defaultRangeDays            = 7
	maximumRangeDays            = 366
	maximumHourDays             = 31
	maximumKnowledgeRankingDays = 30
	defaultRankLimit            = 20
	maximumRankLimit            = 100
)

type metricDefinition struct {
	key   MetricKey
	label string
	unit  Unit
}

var metricDefinitions = []metricDefinition{
	{MetricKeywordReplyCount, "Keyword replies", UnitCount},
	{MetricKnowledgeTriggerCount, "Knowledge triggers", UnitCount},
	{MetricAIRequestCount, "AI requests", UnitCount},
	{MetricAISuccessRate, "AI success rate", UnitPercent},
	{MetricAIDurationMS, "AI duration", UnitMilliseconds},
	{MetricJoinRequestCount, "Join requests", UnitCount},
	{MetricManualApprovalCount, "Manual approvals", UnitCount},
	{MetricAutomaticApprovalCount, "Automatic approvals", UnitCount},
	{MetricScheduledJobRunCount, "Scheduled job runs", UnitCount},
	{MetricGroupMessageCount, "Group messages", UnitCount},
	{MetricCommandRunCount, "Command runs", UnitCount},
	{MetricActiveUserCount, "Active users", UnitCount},
	{MetricLinkCleanCount, "Links cleaned", UnitCount},
	{MetricQuoteSuccessCount, "Quote successes", UnitCount},
	{MetricQuoteFallbackCount, "Quote fallbacks", UnitCount},
	{MetricQuoteFailureCount, "Quote failures", UnitCount},
}

var metricCatalog = func() map[MetricKey]metricDefinition {
	result := make(map[MetricKey]metricDefinition, len(metricDefinitions))
	for _, definition := range metricDefinitions {
		result[definition.key] = definition
	}
	return result
}()

type Options struct {
	Store             Store
	KnowledgeResolver KnowledgeKeyResolver
	Now               func() time.Time
}

type Service struct {
	store             Store
	knowledgeResolver KnowledgeKeyResolver
	now               func() time.Time
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Now == nil {
		return nil, ErrInvalidInput
	}
	return &Service{store: options.Store, knowledgeResolver: options.KnowledgeResolver, now: options.Now}, nil
}

func (s *Service) Summary(ctx context.Context, principal auth.Principal, query Query) (Summary, error) {
	if !principal.Has(auth.PermissionAnalyticsRead) {
		return Summary{}, ErrForbidden
	}
	filter, _, err := s.normalizeFilter(query)
	if err != nil {
		return Summary{}, err
	}
	return s.summary(ctx, filter)
}

func (s *Service) summary(ctx context.Context, filter Filter) (Summary, error) {
	data, err := s.store.LoadSummary(ctx, cloneFilter(filter))
	if err != nil {
		return Summary{}, wrapStoreError("load analytics summary", err)
	}
	if err := validateSummaryData(data); err != nil {
		return Summary{}, err
	}
	metrics := make([]Metric, len(metricDefinitions))
	for index, definition := range metricDefinitions {
		value := data.Values[definition.key]
		metrics[index] = Metric{
			Key: definition.key, Label: definition.label, Unit: definition.unit, Available: value.Available,
			Value: cloneFloat(value.Value), PreviousValue: cloneFloat(value.PreviousValue), ChangePercent: cloneFloat(value.ChangePercent),
		}
	}
	return Summary{Window: windowFromFilter(filter), Metrics: metrics, DataFreshAt: data.DataFreshAt.UTC()}, nil
}

func (s *Service) Timeseries(ctx context.Context, principal auth.Principal, query TimeseriesQuery) (Timeseries, error) {
	if !principal.Has(auth.PermissionAnalyticsRead) {
		return Timeseries{}, ErrForbidden
	}
	filter, location, err := s.normalizeFilter(query.Query)
	if err != nil {
		return Timeseries{}, err
	}
	if !validGranularity(query.Granularity) || !validMetricSelection(query.Metrics) ||
		(query.Granularity == GranularityHour && exceedsLocalDays(filter, location, maximumHourDays)) {
		return Timeseries{}, ErrInvalidInput
	}
	return s.timeseries(ctx, filter, query.Granularity, query.Metrics)
}

func (s *Service) timeseries(ctx context.Context, filter Filter, granularity Granularity, metrics []MetricKey) (Timeseries, error) {
	data, err := s.store.LoadTimeseries(ctx, StoreTimeseriesQuery{
		Filter: cloneFilter(filter), Granularity: granularity, Metrics: append([]MetricKey(nil), metrics...),
	})
	if err != nil {
		return Timeseries{}, wrapStoreError("load analytics timeseries", err)
	}
	if err := validateTimeseriesData(data, filter, metrics); err != nil {
		return Timeseries{}, err
	}
	series := make([]Series, len(metrics))
	for index, metric := range metrics {
		definition := metricCatalog[metric]
		series[index] = Series{
			Metric: metric, Label: definition.label, Unit: definition.unit, Points: clonePoints(data.Points[metric]),
		}
	}
	return Timeseries{
		Window: windowFromFilter(filter), Granularity: granularity, Series: series, DataFreshAt: data.DataFreshAt.UTC(),
	}, nil
}

func (s *Service) Rankings(ctx context.Context, principal auth.Principal, query RankingsQuery) (Rankings, error) {
	if !principal.Has(auth.PermissionAnalyticsRead) {
		return Rankings{}, ErrForbidden
	}
	filter, location, err := s.normalizeFilter(query.Query)
	if err != nil {
		return Rankings{}, err
	}
	if query.Limit == 0 {
		query.Limit = defaultRankLimit
	}
	if !validDimension(query.Dimension) || !validMetric(query.Metric) || query.Limit < 1 || query.Limit > maximumRankLimit ||
		(query.Dimension == DimensionKnowledgeEntry && exceedsLocalDays(filter, location, maximumKnowledgeRankingDays)) {
		return Rankings{}, ErrInvalidInput
	}
	return s.rankings(ctx, filter, query.Dimension, query.Metric, query.Limit)
}

func (s *Service) rankings(ctx context.Context, filter Filter, dimension Dimension, metric MetricKey, limit int) (Rankings, error) {
	data, err := s.store.LoadRankings(ctx, StoreRankingsQuery{
		Filter: cloneFilter(filter), Dimension: dimension, Metric: metric, Limit: limit,
		KnowledgeResolver: s.knowledgeResolver,
	})
	if err != nil {
		return Rankings{}, wrapStoreError("load analytics rankings", err)
	}
	if err := validateRankingsData(data, limit); err != nil {
		return Rankings{}, err
	}
	items := make([]RankingItem, len(data.Items))
	for index, item := range data.Items {
		items[index] = RankingItem{Key: item.Key, DisplayName: item.DisplayName, Value: item.Value, Rank: index + 1}
	}
	return Rankings{
		Window: windowFromFilter(filter), Dimension: dimension, Metric: metric, Unit: metricCatalog[metric].unit,
		Items: items, DataFreshAt: data.DataFreshAt.UTC(),
	}, nil
}

func (s *Service) PrepareExport(ctx context.Context, principal auth.Principal, query ExportQuery) (*PreparedExport, error) {
	if !principal.Has(auth.PermissionAnalyticsExport) {
		return nil, ErrForbidden
	}
	filter, location, err := s.normalizeFilter(query.Query)
	if err != nil {
		return nil, err
	}
	if !validDataset(query.Dataset) || !validExportFormat(query.Format) || !validExportShape(query) ||
		(query.Dataset == DatasetTimeseries && query.Granularity == GranularityHour && exceedsLocalDays(filter, location, maximumHourDays)) {
		return nil, ErrInvalidInput
	}

	var source exportRowSource
	switch query.Dataset {
	case DatasetSummary:
		value, loadErr := s.summary(ctx, filter)
		if loadErr != nil {
			return nil, loadErr
		}
		source = summaryExportSource(value)
	case DatasetTimeseries:
		value, loadErr := s.timeseries(ctx, filter, query.Granularity, []MetricKey{query.Metric})
		if loadErr != nil {
			return nil, loadErr
		}
		source = timeseriesExportSource(value)
	case DatasetRankings:
		value, loadErr := s.rankings(ctx, filter, query.Dimension, query.Metric, MaxExportRows)
		if loadErr != nil {
			return nil, loadErr
		}
		source = rankingsExportSource(value)
	case DatasetJoinRequests:
		rows, openErr := s.store.OpenJoinRequestExport(ctx, cloneFilter(filter))
		if openErr != nil {
			return nil, wrapStoreError("open join request analytics export", openErr)
		}
		if rows == nil {
			return nil, ErrInvalidData
		}
		source = &joinRequestExportSource{rows: rows, location: location}
	case DatasetScheduledJobRuns:
		rows, openErr := s.store.OpenScheduledJobRunExport(ctx, cloneFilter(filter))
		if openErr != nil {
			return nil, wrapStoreError("open scheduled job analytics export", openErr)
		}
		if rows == nil {
			return nil, ErrInvalidData
		}
		source = &scheduledJobRunExportSource{rows: rows, location: location}
	}
	rowCount := source.RowCount()
	if rowCount < 0 || rowCount > MaxExportRows {
		_ = source.Close()
		if rowCount > MaxExportRows {
			return nil, ErrExportTooLarge
		}
		return nil, ErrInvalidData
	}
	return prepareExport(source, query.Dataset, query.Format, s.now().In(location), rowCount), nil
}

func (s *Service) normalizeFilter(query Query) (Filter, *time.Location, error) {
	timezone := query.Timezone
	if timezone == "" {
		timezone = DefaultTimezone
	}
	if !validText(timezone, 64, false) || timezone == "Local" {
		return Filter{}, nil, ErrInvalidInput
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return Filter{}, nil, ErrInvalidInput
	}
	if !validOptionalUTCTime(query.From) || !validOptionalUTCTime(query.To) || !validIDs(query.GroupIDs, 100) ||
		!validFeatureKeys(query.FeatureKeys) || !validResults(query.Results) {
		return Filter{}, nil, ErrInvalidInput
	}

	now := s.now().UTC()
	to := now
	if query.To != nil {
		to = query.To.UTC()
	}
	from := to.In(location).AddDate(0, 0, -defaultRangeDays).UTC()
	if query.From != nil {
		from = query.From.UTC()
	}
	if !from.Before(to) {
		return Filter{}, nil, ErrInvalidInput
	}
	filter := Filter{
		From: from, To: to, GroupIDs: append([]string(nil), query.GroupIDs...),
		FeatureKeys: append([]FeatureKey(nil), query.FeatureKeys...), Results: append([]Result(nil), query.Results...), Timezone: timezone,
	}
	if exceedsLocalDays(filter, location, maximumRangeDays) {
		return Filter{}, nil, ErrInvalidInput
	}
	return filter, location, nil
}

func exceedsLocalDays(filter Filter, location *time.Location, days int) bool {
	maximumTo := filter.From.In(location).AddDate(0, 0, days).UTC()
	return filter.To.After(maximumTo)
}

func validateSummaryData(data SummaryData) error {
	if !validDataTime(data.DataFreshAt) {
		return ErrInvalidData
	}
	for key, value := range data.Values {
		if !validMetric(key) || !validMetricValue(key, value) {
			return ErrInvalidData
		}
	}
	return nil
}

func validMetricValue(key MetricKey, value MetricValue) bool {
	if !value.Available {
		return value.Value == nil && value.PreviousValue == nil && value.ChangePercent == nil
	}
	if !validOptionalMetricNumber(key, value.Value) || value.Value == nil || !validOptionalMetricNumber(key, value.PreviousValue) {
		return false
	}
	return value.ChangePercent == nil || finite(*value.ChangePercent)
}

func validateTimeseriesData(data TimeseriesData, filter Filter, requested []MetricKey) error {
	if !validDataTime(data.DataFreshAt) {
		return ErrInvalidData
	}
	wanted := make(map[MetricKey]struct{}, len(requested))
	for _, metric := range requested {
		wanted[metric] = struct{}{}
	}
	total := 0
	for metric, points := range data.Points {
		if _, ok := wanted[metric]; !ok {
			return ErrInvalidData
		}
		total += len(points)
		if total > MaxExportRows {
			return ErrInvalidData
		}
		var previous time.Time
		for index, point := range points {
			if !validDataTime(point.BucketStart) || point.BucketStart.Before(filter.From) || !point.BucketStart.Before(filter.To) ||
				(index > 0 && !point.BucketStart.After(previous)) || !validOptionalMetricNumber(metric, point.Value) {
				return ErrInvalidData
			}
			previous = point.BucketStart
		}
	}
	return nil
}

func validateRankingsData(data RankingsData, limit int) error {
	if !validDataTime(data.DataFreshAt) || len(data.Items) > limit {
		return ErrInvalidData
	}
	seen := make(map[string]struct{}, len(data.Items))
	var previous RankingValue
	for index, item := range data.Items {
		if !validText(item.Key, 256, false) || !validText(item.DisplayName, 200, true) || !finite(item.Value) || item.Value < 0 {
			return ErrInvalidData
		}
		if index > 0 && (item.Value > previous.Value || item.Value == previous.Value && item.Key <= previous.Key) {
			return ErrInvalidData
		}
		if _, duplicate := seen[item.Key]; duplicate {
			return ErrInvalidData
		}
		seen[item.Key] = struct{}{}
		previous = item
	}
	return nil
}

func validMetricSelection(values []MetricKey) bool {
	if len(values) < 1 || len(values) > 10 {
		return false
	}
	seen := make(map[MetricKey]struct{}, len(values))
	for _, value := range values {
		if !validMetric(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validIDs(values []string, maximum int) bool {
	if len(values) > maximum {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validText(value, 256, false) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validFeatureKeys(values []FeatureKey) bool {
	if len(values) > 6 {
		return false
	}
	seen := make(map[FeatureKey]struct{}, len(values))
	for _, value := range values {
		if !validFeatureKey(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validResults(values []Result) bool {
	if len(values) > 6 {
		return false
	}
	seen := make(map[Result]struct{}, len(values))
	for _, value := range values {
		if !validResult(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func validExportShape(query ExportQuery) bool {
	switch query.Dataset {
	case DatasetSummary:
		return query.Granularity == "" && query.Metric == "" && query.Dimension == ""
	case DatasetTimeseries:
		return validGranularity(query.Granularity) && validMetric(query.Metric) && query.Dimension == ""
	case DatasetRankings:
		return query.Granularity == "" && validMetric(query.Metric) && validDimension(query.Dimension)
	case DatasetJoinRequests, DatasetScheduledJobRuns:
		return query.Granularity == "" && query.Metric == "" && query.Dimension == ""
	default:
		return false
	}
}

func validMetric(value MetricKey) bool {
	_, ok := metricCatalog[value]
	return ok
}

func validFeatureKey(value FeatureKey) bool {
	switch value {
	case FeatureKeywordReply, FeatureAIQA, FeatureQuote, FeatureLinkCleaner, FeatureWelcome, FeatureCustomCommand:
		return true
	default:
		return false
	}
}

func validResult(value Result) bool {
	switch value {
	case ResultSuccess, ResultFailed, ResultDenied, ResultUnknown, ResultFallback, ResultSkipped:
		return true
	default:
		return false
	}
}

func validGranularity(value Granularity) bool {
	return value == GranularityHour || value == GranularityDay
}

func validDimension(value Dimension) bool {
	return value == DimensionGroup || value == DimensionCommand || value == DimensionKnowledgeEntry
}

func validDataset(value Dataset) bool {
	switch value {
	case DatasetSummary, DatasetTimeseries, DatasetRankings, DatasetJoinRequests, DatasetScheduledJobRuns:
		return true
	default:
		return false
	}
}

func validExportFormat(value ExportFormat) bool {
	return value == ExportCSV || value == ExportXLSX
}

func validOptionalMetricNumber(metric MetricKey, value *float64) bool {
	if value == nil {
		return true
	}
	if !finite(*value) || *value < 0 {
		return false
	}
	return metric != MetricAISuccessRate || *value <= 100
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validDataTime(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC
}

func validOptionalUTCTime(value *time.Time) bool {
	return value == nil || validDataTime(*value)
}

func validText(value string, maximum int, emptyAllowed bool) bool {
	return utf8.ValidString(value) && (emptyAllowed || strings.TrimSpace(value) != "") && utf8.RuneCountInString(value) <= maximum
}

func wrapStoreError(action string, err error) error {
	if errors.Is(err, ErrUnavailable) {
		return ErrUnavailable
	}
	return fmt.Errorf("%s: %w", action, err)
}

func cloneFilter(value Filter) Filter {
	value.GroupIDs = append([]string(nil), value.GroupIDs...)
	value.FeatureKeys = append([]FeatureKey(nil), value.FeatureKeys...)
	value.Results = append([]Result(nil), value.Results...)
	return value
}

func windowFromFilter(value Filter) Window {
	return Window{From: value.From.UTC(), To: value.To.UTC(), Timezone: value.Timezone}
}

func clonePoints(values []Point) []Point {
	result := make([]Point, len(values))
	for index, value := range values {
		result[index] = Point{BucketStart: value.BucketStart.UTC(), Value: cloneFloat(value.Value)}
	}
	return result
}

func cloneFloat(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
