package overview

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/knowledge/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	managersystem "github.com/zjutjh/jxh-go/internal/management/system"
)

var (
	ErrForbidden    = errors.New("overview access forbidden")
	ErrInvalidInput = errors.New("invalid overview input")
	ErrInvalidData  = errors.New("invalid overview data")
)

type Range string

const (
	Range7Days  Range = "7d"
	Range30Days Range = "30d"
)

type MetricKey string

const (
	MetricPendingJoinRequests     MetricKey = "pending_join_requests"
	MetricAutomaticApprovalsToday MetricKey = "automatic_approvals_today"
	MetricCommandRunsToday        MetricKey = "command_runs_today"
	MetricActiveGroups            MetricKey = "active_groups"
	MetricEnabledScheduledJobs    MetricKey = "enabled_scheduled_jobs"
	MetricHealthyDependencies     MetricKey = "healthy_dependencies"
)

type PendingKey string

const (
	PendingJoinRequests         PendingKey = "join_requests"
	PendingFailedJobs           PendingKey = "failed_jobs"
	PendingKnowledgeConflicts   PendingKey = "knowledge_conflicts"
	PendingDegradedDependencies PendingKey = "degraded_dependencies"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

type Query struct {
	Range   Range
	GroupID string
}

type StoreQuery struct {
	Range        Range
	GroupID      string
	From         time.Time
	To           time.Time
	PreviousFrom time.Time
	Timezone     string
}

type MetricValue struct {
	Available     bool
	Value         *float64
	ChangePercent *float64
}

type TrendPoint struct {
	BucketStart time.Time
	Values      map[string]float64
}

type Data struct {
	Metrics map[MetricKey]MetricValue
	Pending map[PendingKey]uint64
	Trend   []TrendPoint
}

type Store interface {
	LoadOverview(ctx context.Context, query StoreQuery) (Data, error)
}

type HealthProvider interface {
	Health(ctx context.Context, principal auth.Principal) (managersystem.Health, error)
}

type KnowledgeProvider interface {
	Status(ctx context.Context) (knowledgeadmin.Status, error)
}

type Metric struct {
	Key           MetricKey
	Label         string
	Available     bool
	Value         *float64
	ChangePercent *float64
}

type PendingItem struct {
	Key      PendingKey
	Label    string
	Count    uint64
	Severity Severity
}

type Dependency struct {
	Key           managersystem.DependencyKey
	Status        managersystem.DependencyStatus
	LastSuccessAt *time.Time
}

type Snapshot struct {
	GeneratedAt  time.Time
	Range        Range
	GroupID      string
	Metrics      []Metric
	PendingItems []PendingItem
	Dependencies []Dependency
	Trend        []TrendPoint
}

type Options struct {
	Store     Store
	Health    HealthProvider
	Knowledge KnowledgeProvider
	Now       func() time.Time
	Location  *time.Location
}

type Service struct {
	store     Store
	health    HealthProvider
	knowledge KnowledgeProvider
	now       func() time.Time
	location  *time.Location
}

var trendKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var metricOrder = []struct {
	key   MetricKey
	label string
}{
	{MetricPendingJoinRequests, "待处理入群申请"},
	{MetricAutomaticApprovalsToday, "今日自动批准"},
	{MetricCommandRunsToday, "今日命令执行"},
	{MetricActiveGroups, "活跃群"},
	{MetricEnabledScheduledJobs, "已启用定时任务"},
	{MetricHealthyDependencies, "健康依赖"},
}

var pendingOrder = []struct {
	key   PendingKey
	label string
}{
	{PendingJoinRequests, "待处理入群申请"},
	{PendingFailedJobs, "失败的定时任务"},
	{PendingKnowledgeConflicts, "知识库冲突"},
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Health == nil || options.Knowledge == nil || options.Now == nil || options.Location == nil {
		return nil, ErrInvalidInput
	}
	return &Service{
		store: options.Store, health: options.Health, knowledge: options.Knowledge,
		now: options.Now, location: options.Location,
	}, nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, query Query) (Snapshot, error) {
	if !principal.Has(auth.PermissionOverviewRead) {
		return Snapshot{}, ErrForbidden
	}
	if query.Range == "" {
		query.Range = Range7Days
	}
	if !validRange(query.Range) || (query.GroupID != "" && !validID(query.GroupID)) {
		return Snapshot{}, ErrInvalidInput
	}
	now := s.now().UTC()
	storeQuery := buildStoreQuery(query, now, s.location)
	data, err := s.store.LoadOverview(ctx, storeQuery)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load overview: %w", err)
	}
	if err := validateData(data, storeQuery); err != nil {
		return Snapshot{}, err
	}
	knowledgeStatus, err := s.knowledge.Status(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load overview knowledge status: %w", err)
	}
	if knowledgeStatus.ConflictCount < 0 {
		return Snapshot{}, ErrInvalidData
	}
	if data.Pending == nil {
		data.Pending = make(map[PendingKey]uint64)
	}
	data.Pending[PendingKnowledgeConflicts] = uint64(knowledgeStatus.ConflictCount)
	health, err := s.health.Health(ctx, principal)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load overview health: %w", err)
	}
	dependencies, healthyCount, degradedCount, criticalDependency := mapDependencies(health.Dependencies)
	metrics := make([]Metric, 0, len(metricOrder))
	for _, definition := range metricOrder {
		if definition.key == MetricHealthyDependencies {
			value := float64(healthyCount)
			metrics = append(metrics, Metric{Key: definition.key, Label: definition.label, Available: true, Value: &value})
			continue
		}
		value, exists := data.Metrics[definition.key]
		if !exists {
			value = MetricValue{}
		}
		metrics = append(metrics, Metric{
			Key: definition.key, Label: definition.label, Available: value.Available,
			Value: cloneFloat(value.Value), ChangePercent: cloneFloat(value.ChangePercent),
		})
	}
	pending := make([]PendingItem, 0, len(pendingOrder)+1)
	for _, definition := range pendingOrder {
		if count, available := data.Pending[definition.key]; available {
			pending = append(pending, PendingItem{
				Key: definition.key, Label: definition.label, Count: count, Severity: pendingSeverity(definition.key, count),
			})
		}
	}
	pending = append(pending, PendingItem{
		Key: PendingDegradedDependencies, Label: "异常依赖", Count: uint64(degradedCount),
		Severity: dependencySeverity(degradedCount, criticalDependency),
	})
	return Snapshot{
		GeneratedAt: now, Range: query.Range, GroupID: query.GroupID, Metrics: metrics,
		PendingItems: pending, Dependencies: dependencies, Trend: cloneTrend(data.Trend),
	}, nil
}

func buildStoreQuery(query Query, now time.Time, location *time.Location) StoreQuery {
	localNow := now.In(location)
	year, month, day := localNow.Date()
	toLocal := time.Date(year, month, day, 0, 0, 0, 0, location).AddDate(0, 0, 1)
	days := 7
	if query.Range == Range30Days {
		days = 30
	}
	fromLocal := toLocal.AddDate(0, 0, -days)
	return StoreQuery{
		Range: query.Range, GroupID: query.GroupID, From: fromLocal.UTC(), To: toLocal.UTC(),
		PreviousFrom: fromLocal.AddDate(0, 0, -days).UTC(), Timezone: location.String(),
	}
}

func mapDependencies(values []managersystem.DependencyHealth) ([]Dependency, int, int, bool) {
	dependencies := make([]Dependency, len(values))
	healthyCount := 0
	degradedCount := 0
	critical := false
	for index, value := range values {
		dependencies[index] = Dependency{
			Key: value.Key, Status: value.Status, LastSuccessAt: cloneTime(value.LastSuccessAt),
		}
		if value.Status == managersystem.DependencyHealthy {
			healthyCount++
			continue
		}
		if value.Status == managersystem.DependencyNotConfigured && !value.Required {
			continue
		}
		degradedCount++
		if value.Required && (value.Status == managersystem.DependencyUnavailable || value.Status == managersystem.DependencyUnknown ||
			value.Status == managersystem.DependencyNotConfigured) {
			critical = true
		}
	}
	return dependencies, healthyCount, degradedCount, critical
}

func validateData(data Data, query StoreQuery) error {
	for key, value := range data.Metrics {
		if !validStoredMetricKey(key) || !validMetricValue(value) {
			return ErrInvalidData
		}
	}
	for key := range data.Pending {
		if key != PendingJoinRequests && key != PendingFailedJobs && key != PendingKnowledgeConflicts {
			return ErrInvalidData
		}
	}
	if len(data.Trend) > 31 {
		return ErrInvalidData
	}
	var previous time.Time
	for index, point := range data.Trend {
		bucket := point.BucketStart.UTC()
		if point.BucketStart.Location() != time.UTC || bucket.Before(query.From) || !bucket.Before(query.To) ||
			(index > 0 && !bucket.After(previous)) {
			return ErrInvalidData
		}
		previous = bucket
		for key, value := range point.Values {
			if !trendKeyPattern.MatchString(key) || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
				return ErrInvalidData
			}
		}
	}
	return nil
}

func validMetricValue(value MetricValue) bool {
	if !value.Available {
		return value.Value == nil && value.ChangePercent == nil
	}
	if value.Value == nil || math.IsNaN(*value.Value) || math.IsInf(*value.Value, 0) || *value.Value < 0 {
		return false
	}
	return value.ChangePercent == nil || (!math.IsNaN(*value.ChangePercent) && !math.IsInf(*value.ChangePercent, 0))
}

func validStoredMetricKey(value MetricKey) bool {
	return value == MetricPendingJoinRequests || value == MetricAutomaticApprovalsToday || value == MetricCommandRunsToday ||
		value == MetricActiveGroups || value == MetricEnabledScheduledJobs
}

func validRange(value Range) bool {
	return value == Range7Days || value == Range30Days
}

func validID(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 1 && utf8.RuneCountInString(value) <= 256
}

func pendingSeverity(key PendingKey, count uint64) Severity {
	if count == 0 {
		return SeverityInfo
	}
	if key == PendingFailedJobs {
		return SeverityCritical
	}
	return SeverityWarning
}

func dependencySeverity(count int, critical bool) Severity {
	if count == 0 {
		return SeverityInfo
	}
	if critical {
		return SeverityCritical
	}
	return SeverityWarning
}

func cloneTrend(values []TrendPoint) []TrendPoint {
	result := make([]TrendPoint, len(values))
	for index, value := range values {
		result[index] = TrendPoint{BucketStart: value.BucketStart.UTC(), Values: make(map[string]float64, len(value.Values))}
		for key, metric := range value.Values {
			result[index].Values[key] = metric
		}
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

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}
