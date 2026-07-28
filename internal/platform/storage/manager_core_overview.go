package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/overview"
	"gorm.io/gorm"
)

type managerDailyMetric struct {
	BucketDate  time.Time `gorm:"column:bucket_date"`
	MetricKey   string    `gorm:"column:metric_key"`
	ValueCount  uint64    `gorm:"column:value_count"`
	ValueSum    float64   `gorm:"column:value_sum"`
	SampleCount uint64    `gorm:"column:sample_count"`
}

func (s *Store) LoadOverview(ctx context.Context, query overview.StoreQuery) (data overview.Data, err error) {
	location, err := time.LoadLocation(query.Timezone)
	if err != nil {
		return overview.Data{}, fmt.Errorf("load overview timezone: %w", err)
	}
	var groupID int64
	if query.GroupID != "" {
		groupID, err = parseManagerGroupID(query.GroupID)
		if err != nil {
			return overview.Data{}, err
		}
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		data.Metrics = make(map[overview.MetricKey]overview.MetricValue, 5)
		data.Pending = make(map[overview.PendingKey]uint64, 2)
		todayFrom := query.To.In(location).AddDate(0, 0, -1).UTC()

		pendingQuery := tx.Table("group_join_requests").Where("decision_status = ?", "pending")
		if groupID != 0 {
			pendingQuery = pendingQuery.Where("group_id = ?", groupID)
		}
		pending, countErr := managerCountRows(pendingQuery)
		if countErr != nil {
			return countErr
		}
		managerSetOverviewMetric(&data, overview.MetricPendingJoinRequests, pending)
		data.Pending[overview.PendingJoinRequests] = uint64(pending)

		approvalQuery := tx.Table("group_join_decisions AS decisions").
			Joins("JOIN group_join_requests AS requests ON requests.id = decisions.request_id").
			Where("decisions.source = ? AND decisions.action = ? AND decisions.status = ?", "automatic", "approve", "confirmed").
			Where("decisions.completed_at >= ? AND decisions.completed_at < ?", todayFrom, query.To)
		if groupID != 0 {
			approvalQuery = approvalQuery.Where("requests.group_id = ?", groupID)
		}
		automaticApprovals, countErr := managerCountRows(approvalQuery)
		if countErr != nil {
			return countErr
		}
		managerSetOverviewMetric(&data, overview.MetricAutomaticApprovalsToday, automaticApprovals)

		commandQuery := tx.Table("custom_command_runs").Where("occurred_at >= ? AND occurred_at < ?", todayFrom, query.To)
		if groupID != 0 {
			commandQuery = commandQuery.Where("group_id = ?", groupID)
		}
		commandRuns, countErr := managerCountRows(commandQuery)
		if countErr != nil {
			return countErr
		}
		managerSetOverviewMetric(&data, overview.MetricCommandRunsToday, commandRuns)

		activeGroupsQuery := tx.Table("managed_groups").Where("archived_at IS NULL")
		if groupID != 0 {
			activeGroupsQuery = activeGroupsQuery.Where("group_id = ?", groupID)
		}
		activeGroups, countErr := managerCountRows(activeGroupsQuery)
		if countErr != nil {
			return countErr
		}
		managerSetOverviewMetric(&data, overview.MetricActiveGroups, activeGroups)

		enabledJobsQuery := tx.Table("scheduled_jobs").
			Where("enabled = ? AND status = ? AND archived_at IS NULL", true, "active")
		if groupID != 0 {
			enabledJobsQuery = enabledJobsQuery.Where("group_id = ?", groupID)
		}
		enabledJobs, countErr := managerCountRows(enabledJobsQuery)
		if countErr != nil {
			return countErr
		}
		managerSetOverviewMetric(&data, overview.MetricEnabledScheduledJobs, enabledJobs)

		failedJobsQuery := tx.Table("scheduled_jobs").Where("last_run_result = ? AND archived_at IS NULL", "failed")
		if groupID != 0 {
			failedJobsQuery = failedJobsQuery.Where("group_id = ?", groupID)
		}
		failedJobs, countErr := managerCountRows(failedJobsQuery)
		if countErr != nil {
			return countErr
		}
		data.Pending[overview.PendingFailedJobs] = uint64(failedJobs)

		trend, trendErr := loadManagerOverviewTrend(tx, query, location, groupID)
		if trendErr != nil {
			return trendErr
		}
		data.Trend = trend
		return nil
	})
	return data, err
}

func managerCountRows(query *gorm.DB) (float64, error) {
	var count int64
	if err := query.Count(&count).Error; err != nil {
		return 0, err
	}
	if count < 0 {
		return 0, errManagerInvalidState
	}
	return float64(count), nil
}

func managerSetOverviewMetric(data *overview.Data, key overview.MetricKey, value float64) {
	copy := value
	data.Metrics[key] = overview.MetricValue{Available: true, Value: &copy}
}

func loadManagerOverviewTrend(
	tx *gorm.DB,
	query overview.StoreQuery,
	location *time.Location,
	groupID int64,
) ([]overview.TrendPoint, error) {
	fromDate := query.From.In(location).Format("2006-01-02")
	toDate := query.To.In(location).Format("2006-01-02")
	var rows []managerDailyMetric
	err := tx.Table("bot_operation_daily").
		Select("bucket_date, metric_key, value_count, value_sum, sample_count").
		Where("bucket_date >= ? AND bucket_date < ?", fromDate, toDate).
		Where("timezone = ? AND group_id = ? AND feature_key = '' AND outcome = ''", query.Timezone, groupID).
		Order("bucket_date ASC").Order("metric_key ASC").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	points := make([]overview.TrendPoint, 0, len(rows))
	pointIndex := make(map[string]int)
	for _, row := range rows {
		year, month, day := row.BucketDate.Date()
		dateKey := fmt.Sprintf("%04d-%02d-%02d", year, month, day)
		index, exists := pointIndex[dateKey]
		if !exists {
			bucketStart := time.Date(year, month, day, 0, 0, 0, 0, location).UTC()
			index = len(points)
			pointIndex[dateKey] = index
			points = append(points, overview.TrendPoint{BucketStart: bucketStart, Values: map[string]float64{}})
		}
		points[index].Values[row.MetricKey] = managerDailyMetricValue(row)
	}
	return points, nil
}

func managerDailyMetricValue(row managerDailyMetric) float64 {
	switch row.MetricKey {
	case "ai_success_rate":
		if row.SampleCount == 0 {
			return 0
		}
		return float64(row.ValueCount) / float64(row.SampleCount) * 100
	case "ai_duration_ms":
		if row.SampleCount == 0 {
			return 0
		}
		return row.ValueSum / float64(row.SampleCount)
	default:
		return float64(row.ValueCount)
	}
}
