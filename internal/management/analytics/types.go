package analytics

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

var (
	ErrForbidden      = errors.New("analytics operation forbidden")
	ErrInvalidInput   = errors.New("invalid analytics input")
	ErrInvalidData    = errors.New("invalid analytics data")
	ErrUnavailable    = errors.New("analytics store unavailable")
	ErrExportTooLarge = errors.New("analytics export exceeds row limit")
)

const (
	DefaultTimezone = "Asia/Shanghai"
	MaxExportRows   = 100_000
)

type MetricKey string

const (
	MetricKeywordReplyCount      MetricKey = "keyword_reply_count"
	MetricKnowledgeTriggerCount  MetricKey = "knowledge_trigger_count"
	MetricAIRequestCount         MetricKey = "ai_request_count"
	MetricAISuccessRate          MetricKey = "ai_success_rate"
	MetricAIDurationMS           MetricKey = "ai_duration_ms"
	MetricJoinRequestCount       MetricKey = "join_request_count"
	MetricManualApprovalCount    MetricKey = "manual_approval_count"
	MetricAutomaticApprovalCount MetricKey = "automatic_approval_count"
	MetricScheduledJobRunCount   MetricKey = "scheduled_job_run_count"
	MetricGroupMessageCount      MetricKey = "group_message_count"
	MetricCommandRunCount        MetricKey = "command_run_count"
	MetricActiveUserCount        MetricKey = "active_user_count"
	MetricLinkCleanCount         MetricKey = "link_clean_count"
	MetricQuoteSuccessCount      MetricKey = "quote_success_count"
	MetricQuoteFallbackCount     MetricKey = "quote_fallback_count"
	MetricQuoteFailureCount      MetricKey = "quote_failure_count"
)

type Unit string

const (
	UnitCount        Unit = "count"
	UnitPercent      Unit = "percent"
	UnitMilliseconds Unit = "milliseconds"
)

type FeatureKey string

const (
	FeatureKeywordReply  FeatureKey = "keyword_reply"
	FeatureAIQA          FeatureKey = "ai_qa"
	FeatureQuote         FeatureKey = "quote"
	FeatureLinkCleaner   FeatureKey = "link_cleaner"
	FeatureWelcome       FeatureKey = "welcome"
	FeatureCustomCommand FeatureKey = "custom_commands"
)

type Result string

const (
	ResultSuccess  Result = "success"
	ResultFailed   Result = "failed"
	ResultDenied   Result = "denied"
	ResultUnknown  Result = "unknown"
	ResultFallback Result = "fallback"
	ResultSkipped  Result = "skipped"
)

type Granularity string

const (
	GranularityHour Granularity = "hour"
	GranularityDay  Granularity = "day"
)

type Dimension string

const (
	DimensionGroup          Dimension = "group"
	DimensionCommand        Dimension = "command"
	DimensionKnowledgeEntry Dimension = "knowledge_entry"
)

type Dataset string

const (
	DatasetSummary          Dataset = "summary"
	DatasetTimeseries       Dataset = "timeseries"
	DatasetRankings         Dataset = "rankings"
	DatasetJoinRequests     Dataset = "join_requests"
	DatasetScheduledJobRuns Dataset = "scheduled_job_runs"
)

type ExportFormat string

const (
	ExportCSV  ExportFormat = "csv"
	ExportXLSX ExportFormat = "xlsx"
)

type Query struct {
	From        *time.Time
	To          *time.Time
	GroupIDs    []string
	FeatureKeys []FeatureKey
	Results     []Result
	Timezone    string
}

type Filter struct {
	From        time.Time
	To          time.Time
	GroupIDs    []string
	FeatureKeys []FeatureKey
	Results     []Result
	Timezone    string
}

type Window struct {
	From     time.Time
	To       time.Time
	Timezone string
}

type MetricValue struct {
	Available     bool
	Value         *float64
	PreviousValue *float64
	ChangePercent *float64
}

type SummaryData struct {
	Values      map[MetricKey]MetricValue
	DataFreshAt time.Time
}

type Metric struct {
	Key           MetricKey
	Label         string
	Unit          Unit
	Available     bool
	Value         *float64
	PreviousValue *float64
	ChangePercent *float64
}

type Summary struct {
	Window      Window
	Metrics     []Metric
	DataFreshAt time.Time
}

type TimeseriesQuery struct {
	Query
	Granularity Granularity
	Metrics     []MetricKey
}

type StoreTimeseriesQuery struct {
	Filter
	Granularity Granularity
	Metrics     []MetricKey
}

type Point struct {
	BucketStart time.Time
	Value       *float64
}

type TimeseriesData struct {
	Points      map[MetricKey][]Point
	DataFreshAt time.Time
}

type Series struct {
	Metric MetricKey
	Label  string
	Unit   Unit
	Points []Point
}

type Timeseries struct {
	Window      Window
	Granularity Granularity
	Series      []Series
	DataFreshAt time.Time
}

type RankingsQuery struct {
	Query
	Dimension Dimension
	Metric    MetricKey
	Page      int
	Limit     int
}

type StoreRankingsQuery struct {
	Filter
	Dimension         Dimension
	Metric            MetricKey
	Page              int
	Limit             int
	KnowledgeResolver KnowledgeKeyResolver
}

type KnowledgeKeyResolver interface {
	ResolveKnowledgeKey(value string) (sourceKey string, displayName string, ok bool)
}

type RankingValue struct {
	Key         string
	DisplayName string
	Value       float64
}

type RankingsData struct {
	Items       []RankingValue
	TotalCount  int
	DataFreshAt time.Time
}

type RankingItem struct {
	Key         string
	DisplayName string
	Value       float64
	Rank        int
}

type Rankings struct {
	Window      Window
	Dimension   Dimension
	Metric      MetricKey
	Unit        Unit
	Items       []RankingItem
	TotalCount  int
	DataFreshAt time.Time
}

type ExportQuery struct {
	Query
	Dataset     Dataset
	Format      ExportFormat
	Granularity Granularity
	Metric      MetricKey
	Dimension   Dimension
}

type JoinRequestExportRow struct {
	RequestID      string
	GroupID        string
	SubType        string
	Source         string
	ObservedStatus string
	DecisionStatus string
	DecisionSource *string
	RequestedAt    time.Time
	DecidedAt      *time.Time
}

// JoinRequestExportRows deliberately excludes applicant identity, verification
// messages, parsed applicant fields, comments, and arbitrary metadata.
type JoinRequestExportRows interface {
	RowCount() int
	Next(ctx context.Context) (JoinRequestExportRow, bool, error)
	Close() error
}

type ScheduledJobRunExportRow struct {
	RunID        string
	JobID        string
	GroupID      string
	Kind         string
	Result       Result
	ScheduledFor *time.Time
	StartedAt    time.Time
	CompletedAt  *time.Time
	DurationMS   int64
	ErrorCode    *string
}

// ScheduledJobRunExportRows deliberately excludes message bodies, message IDs,
// actor identity, request metadata, and raw error messages.
type ScheduledJobRunExportRows interface {
	RowCount() int
	Next(ctx context.Context) (ScheduledJobRunExportRow, bool, error)
	Close() error
}

type Store interface {
	LoadSummary(ctx context.Context, filter Filter) (SummaryData, error)
	LoadTimeseries(ctx context.Context, query StoreTimeseriesQuery) (TimeseriesData, error)
	LoadRankings(ctx context.Context, query StoreRankingsQuery) (RankingsData, error)
	OpenJoinRequestExport(ctx context.Context, filter Filter) (JoinRequestExportRows, error)
	OpenScheduledJobRunExport(ctx context.Context, filter Filter) (ScheduledJobRunExportRows, error)
}

type ExportMetadata struct {
	Filename    string
	ContentType string
	RowCount    int
}

type PreparedExport struct {
	metadata ExportMetadata
	write    func(context.Context, io.Writer) error
	close    func() error
	mu       sync.Mutex
	used     bool
	closed   bool
}

func (e *PreparedExport) Metadata() ExportMetadata {
	if e == nil {
		return ExportMetadata{}
	}
	return e.metadata
}

func (e *PreparedExport) WriteTo(ctx context.Context, writer io.Writer) error {
	if e == nil || e.write == nil || writer == nil {
		return ErrInvalidInput
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.used || e.closed {
		return ErrInvalidInput
	}
	e.used = true
	return e.write(ctx, writer)
}

func (e *PreparedExport) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	closeExport := e.close
	e.mu.Unlock()
	if closeExport == nil {
		return nil
	}
	return closeExport()
}
