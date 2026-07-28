package analytics

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestAnalyticsSummaryNormalizesFiltersAndReturnsStableMetricSet(t *testing.T) {
	current := 12.0
	previous := 10.0
	change := 20.0
	store := &analyticsStoreFake{summary: SummaryData{
		Values: map[MetricKey]MetricValue{
			MetricKeywordReplyCount: {Available: true, Value: &current, PreviousValue: &previous, ChangePercent: &change},
		},
		DataFreshAt: analyticsTestTime(11),
	}}
	service := newAnalyticsService(t, store)
	query := Query{
		GroupIDs: []string{"00123"}, FeatureKeys: []FeatureKey{FeatureAIQA}, Results: []Result{ResultSuccess},
	}

	summary, err := service.Summary(t.Context(), analyticsObserver(), query)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Window.Timezone != DefaultTimezone || !summary.Window.To.Equal(analyticsTestTime(12)) ||
		!summary.Window.From.Equal(analyticsTestTime(12).AddDate(0, 0, -7)) {
		t.Fatalf("window=%+v", summary.Window)
	}
	if len(summary.Metrics) != len(metricDefinitions) || summary.Metrics[0].Key != MetricKeywordReplyCount ||
		!summary.Metrics[0].Available || summary.Metrics[1].Available || summary.Metrics[1].Value != nil {
		t.Fatalf("metrics=%+v", summary.Metrics)
	}
	filter := store.summaryFilter
	if filter.GroupIDs[0] != "00123" || filter.FeatureKeys[0] != FeatureAIQA || filter.Results[0] != ResultSuccess {
		t.Fatalf("filter=%+v", filter)
	}
	*summary.Metrics[0].Value = 99
	query.GroupIDs[0] = "changed"
	if *store.summary.Values[MetricKeywordReplyCount].Value != 12 || store.summaryFilter.GroupIDs[0] != "00123" {
		t.Fatal("summary or filter aliases store-owned data")
	}
}

func TestAnalyticsAuthorizationAndInvalidFiltersPrecedeStore(t *testing.T) {
	store := &analyticsStoreFake{}
	service := newAnalyticsService(t, store)
	invalidPrincipal := auth.Principal{Role: "invalid"}

	if _, err := service.Summary(t.Context(), invalidPrincipal, Query{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("summary error=%v", err)
	}
	if _, err := service.PrepareExport(t.Context(), invalidPrincipal, ExportQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("export error=%v", err)
	}
	from := analyticsTestTime(12)
	to := analyticsTestTime(11)
	queries := []Query{
		{From: &from, To: &to},
		{Timezone: "../secret"},
		{Timezone: "Local"},
		{GroupIDs: []string{"1", "1"}},
		{FeatureKeys: []FeatureKey{"invalid"}},
		{Results: []Result{"timeout"}},
	}
	for _, query := range queries {
		if _, err := service.Summary(t.Context(), analyticsObserver(), query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("query=%+v error=%v", query, err)
		}
	}
	if store.totalCalls() != 0 {
		t.Fatalf("invalid requests reached store: %d", store.totalCalls())
	}
}

func TestAnalyticsTimeseriesValidatesGranularityMetricsAndRange(t *testing.T) {
	one := 1.0
	two := 2.0
	store := &analyticsStoreFake{timeseries: TimeseriesData{
		Points: map[MetricKey][]Point{
			MetricGroupMessageCount: {{BucketStart: analyticsTestTime(10), Value: &one}},
			MetricAISuccessRate:     {{BucketStart: analyticsTestTime(10), Value: &two}},
		},
		DataFreshAt: analyticsTestTime(11),
	}}
	service := newAnalyticsService(t, store)
	from := analyticsTestTime(9)
	to := analyticsTestTime(12)
	value, err := service.Timeseries(t.Context(), analyticsObserver(), TimeseriesQuery{
		Query: Query{From: &from, To: &to, Timezone: "UTC"}, Granularity: GranularityHour,
		Metrics: []MetricKey{MetricGroupMessageCount, MetricAISuccessRate},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(value.Series) != 2 || value.Series[0].Metric != MetricGroupMessageCount || value.Series[1].Unit != UnitPercent ||
		store.timeseriesQuery.Granularity != GranularityHour {
		t.Fatalf("value=%+v query=%+v", value, store.timeseriesQuery)
	}
	*value.Series[0].Points[0].Value = 99
	if *store.timeseries.Points[MetricGroupMessageCount][0].Value != 1 {
		t.Fatal("timeseries aliases store data")
	}

	tooOld := analyticsTestTime(12).AddDate(0, 0, -32)
	for _, query := range []TimeseriesQuery{
		{Granularity: GranularityDay},
		{Granularity: "minute", Metrics: []MetricKey{MetricGroupMessageCount}},
		{Granularity: GranularityDay, Metrics: []MetricKey{MetricGroupMessageCount, MetricGroupMessageCount}},
		{Query: Query{From: &tooOld, To: &to, Timezone: "UTC"}, Granularity: GranularityHour, Metrics: []MetricKey{MetricGroupMessageCount}},
	} {
		if _, err := service.Timeseries(t.Context(), analyticsObserver(), query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("query=%+v error=%v", query, err)
		}
	}
}

func TestAnalyticsRankingsApplyDefaultLimitAndStableRanks(t *testing.T) {
	store := &analyticsStoreFake{rankings: RankingsData{
		Items:       []RankingValue{{Key: "00123", DisplayName: "Group A", Value: 9}, {Key: "00456", DisplayName: "Group B", Value: 7}},
		DataFreshAt: analyticsTestTime(11),
	}}
	service := newAnalyticsService(t, store)
	value, err := service.Rankings(t.Context(), analyticsObserver(), RankingsQuery{
		Dimension: DimensionGroup, Metric: MetricGroupMessageCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.rankingsQuery.Limit != 20 || len(value.Items) != 2 || value.Items[0].Rank != 1 || value.Items[1].Rank != 2 || value.Unit != UnitCount {
		t.Fatalf("value=%+v query=%+v", value, store.rankingsQuery)
	}
	if _, err := service.Rankings(t.Context(), analyticsObserver(), RankingsQuery{
		Dimension: DimensionGroup, Metric: MetricGroupMessageCount, Limit: 101,
	}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("limit error=%v", err)
	}
	store.rankings = RankingsData{
		Items: []RankingValue{{Key: "b", Value: 1}, {Key: "a", Value: 2}}, DataFreshAt: analyticsTestTime(11),
	}
	if _, err := service.Rankings(t.Context(), analyticsObserver(), RankingsQuery{
		Dimension: DimensionGroup, Metric: MetricGroupMessageCount,
	}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("unsorted rankings error=%v", err)
	}
}

func TestAnalyticsStoreUnavailableRemainsClassifiable(t *testing.T) {
	store := &analyticsStoreFake{summaryErr: errors.Join(errors.New("database DSN secret"), ErrUnavailable)}
	service := newAnalyticsService(t, store)
	if _, err := service.Summary(t.Context(), analyticsObserver(), Query{}); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("error=%v", err)
	}
}

func TestAnalyticsCSVExportStreamsSafeJoinRequestFieldsAndTimezone(t *testing.T) {
	decisionSource := "manual"
	rows := &joinRequestRowsFake{rows: []JoinRequestExportRow{{
		RequestID: "request_1", GroupID: " =2+3", SubType: "add", Source: "event", ObservedStatus: "checked",
		DecisionStatus: "approved", DecisionSource: &decisionSource, RequestedAt: analyticsTestTime(1), DecidedAt: timePointer(analyticsTestTime(2)),
	}}}
	store := &analyticsStoreFake{joinRows: rows}
	service := newAnalyticsService(t, store)
	prepared, err := service.PrepareExport(t.Context(), analyticsObserver(), ExportQuery{
		Query: Query{Timezone: "Asia/Shanghai"}, Dataset: DatasetJoinRequests, Format: ExportCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	metadata := prepared.Metadata()
	if metadata.RowCount != 1 || metadata.ContentType != csvContentType || !strings.HasSuffix(metadata.Filename, ".csv") {
		t.Fatalf("metadata=%+v", metadata)
	}
	var output bytes.Buffer
	if err := prepared.WriteTo(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(bytes.NewReader(output.Bytes())).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1][1] != "' =2+3" || !strings.HasSuffix(records[1][7], "+08:00") {
		t.Fatalf("records=%v", records)
	}
	lower := strings.ToLower(output.String())
	for _, forbidden := range []string{"applicant_qq", "verification_message", "student_id", "actor_hash", "token", "cookie", "raw_error"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("export leaked %q: %s", forbidden, output.String())
		}
	}
	if rows.nextCalls != 2 {
		t.Fatalf("next calls=%d", rows.nextCalls)
	}
}

func TestAnalyticsXLSXExportUsesStreamingWriterAndEscapesFormulaCells(t *testing.T) {
	store := &analyticsStoreFake{rankings: RankingsData{
		Items:       []RankingValue{{Key: "command_1", DisplayName: "=HYPERLINK(\"bad\")", Value: 3}},
		DataFreshAt: analyticsTestTime(11),
	}}
	service := newAnalyticsService(t, store)
	prepared, err := service.PrepareExport(t.Context(), analyticsObserver(), ExportQuery{
		Dataset: DatasetRankings, Format: ExportXLSX, Dimension: DimensionCommand, Metric: MetricCommandRunCount,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if prepared.Metadata().RowCount != 1 || prepared.Metadata().ContentType != xlsxContentType {
		t.Fatalf("metadata=%+v", prepared.Metadata())
	}
	if store.rankingsQuery.Limit != MaxExportRows {
		t.Fatalf("export ranking limit=%d, want %d", store.rankingsQuery.Limit, MaxExportRows)
	}
	var output bytes.Buffer
	if err := prepared.WriteTo(t.Context(), &output); err != nil {
		t.Fatal(err)
	}
	workbook, err := excelize.OpenReader(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer workbook.Close()
	values, err := workbook.GetRows("Analytics")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 || len(values[1]) < 6 || values[1][5] != "'=HYPERLINK(\"bad\")" {
		t.Fatalf("rows=%v", values)
	}
}

func TestAnalyticsExportEnforcesDatasetShapeRowLimitAndSafeErrors(t *testing.T) {
	service := newAnalyticsService(t, &analyticsStoreFake{})
	for _, query := range []ExportQuery{
		{Dataset: DatasetTimeseries, Format: ExportCSV, Metric: MetricGroupMessageCount},
		{Dataset: DatasetRankings, Format: ExportCSV, Metric: MetricGroupMessageCount},
		{Dataset: DatasetSummary, Format: ExportCSV, Metric: MetricGroupMessageCount},
		{Dataset: DatasetJoinRequests, Format: "pdf"},
	} {
		if _, err := service.PrepareExport(t.Context(), analyticsObserver(), query); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("query=%+v error=%v", query, err)
		}
	}

	tooMany := &joinRequestRowsFake{count: MaxExportRows + 1}
	service = newAnalyticsService(t, &analyticsStoreFake{joinRows: tooMany})
	if _, err := service.PrepareExport(t.Context(), analyticsObserver(), ExportQuery{
		Dataset: DatasetJoinRequests, Format: ExportCSV,
	}); !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("row limit error=%v", err)
	}
	if !tooMany.closed {
		t.Fatal("oversized cursor was not closed")
	}

	rawError := "upstream_token=secret"
	runRows := &scheduledJobRowsFake{rows: []ScheduledJobRunExportRow{{
		RunID: "run_1", JobID: "job_1", GroupID: "group_1", Kind: "scheduled", Result: ResultFailed,
		StartedAt: analyticsTestTime(1), CompletedAt: timePointer(analyticsTestTime(2)), ErrorCode: &rawError,
	}}}
	service = newAnalyticsService(t, &analyticsStoreFake{runRows: runRows})
	prepared, err := service.PrepareExport(t.Context(), analyticsObserver(), ExportQuery{
		Dataset: DatasetScheduledJobRuns, Format: ExportCSV,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	var output bytes.Buffer
	if err := prepared.WriteTo(t.Context(), &output); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("unsafe row error=%v", err)
	}
	if strings.Contains(output.String(), "secret") {
		t.Fatalf("unsafe error reached export: %s", output.String())
	}
}

func newAnalyticsService(t *testing.T, store Store) *Service {
	t.Helper()
	service, err := NewService(Options{Store: store, Now: func() time.Time { return analyticsTestTime(12) }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func analyticsObserver() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleObserver}
}

func analyticsTestTime(hour int) time.Time {
	return time.Date(2026, 7, 28, hour, 0, 0, 0, time.UTC)
}

func timePointer(value time.Time) *time.Time {
	copy := value
	return &copy
}

type analyticsStoreFake struct {
	summary    SummaryData
	summaryErr error
	timeseries TimeseriesData
	timeErr    error
	rankings   RankingsData
	rankErr    error
	joinRows   JoinRequestExportRows
	joinErr    error
	runRows    ScheduledJobRunExportRows
	runErr     error

	summaryCalls    int
	timeCalls       int
	rankCalls       int
	joinCalls       int
	runCalls        int
	summaryFilter   Filter
	timeseriesQuery StoreTimeseriesQuery
	rankingsQuery   StoreRankingsQuery
}

func (s *analyticsStoreFake) LoadSummary(_ context.Context, filter Filter) (SummaryData, error) {
	s.summaryCalls++
	s.summaryFilter = filter
	return s.summary, s.summaryErr
}

func (s *analyticsStoreFake) LoadTimeseries(_ context.Context, query StoreTimeseriesQuery) (TimeseriesData, error) {
	s.timeCalls++
	s.timeseriesQuery = query
	return s.timeseries, s.timeErr
}

func (s *analyticsStoreFake) LoadRankings(_ context.Context, query StoreRankingsQuery) (RankingsData, error) {
	s.rankCalls++
	s.rankingsQuery = query
	return s.rankings, s.rankErr
}

func (s *analyticsStoreFake) OpenJoinRequestExport(_ context.Context, filter Filter) (JoinRequestExportRows, error) {
	s.joinCalls++
	s.summaryFilter = filter
	return s.joinRows, s.joinErr
}

func (s *analyticsStoreFake) OpenScheduledJobRunExport(_ context.Context, filter Filter) (ScheduledJobRunExportRows, error) {
	s.runCalls++
	s.summaryFilter = filter
	return s.runRows, s.runErr
}

func (s *analyticsStoreFake) totalCalls() int {
	return s.summaryCalls + s.timeCalls + s.rankCalls + s.joinCalls + s.runCalls
}

type joinRequestRowsFake struct {
	rows      []JoinRequestExportRow
	index     int
	count     int
	nextCalls int
	closed    bool
}

func (r *joinRequestRowsFake) RowCount() int {
	if r.count != 0 {
		return r.count
	}
	return len(r.rows)
}

func (r *joinRequestRowsFake) Next(context.Context) (JoinRequestExportRow, bool, error) {
	r.nextCalls++
	if r.index >= len(r.rows) {
		return JoinRequestExportRow{}, false, nil
	}
	value := r.rows[r.index]
	r.index++
	return value, true, nil
}

func (r *joinRequestRowsFake) Close() error {
	r.closed = true
	return nil
}

type scheduledJobRowsFake struct {
	rows   []ScheduledJobRunExportRow
	index  int
	closed bool
}

func (r *scheduledJobRowsFake) RowCount() int { return len(r.rows) }

func (r *scheduledJobRowsFake) Next(context.Context) (ScheduledJobRunExportRow, bool, error) {
	if r.index >= len(r.rows) {
		return ScheduledJobRunExportRow{}, false, nil
	}
	value := r.rows[r.index]
	r.index++
	return value, true, nil
}

func (r *scheduledJobRowsFake) Close() error {
	r.closed = true
	return nil
}
