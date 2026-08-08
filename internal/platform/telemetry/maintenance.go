package telemetry

import (
	"context"
	"fmt"
	"io"
	"log"
	"time"
)

type MaintenanceStore interface {
	AggregateTelemetryDaily(ctx context.Context, completedBefore time.Time, timezone string) error
	PurgeTelemetryEvents(ctx context.Context, occurredBefore time.Time) (int64, error)
}

type MaintenanceOptions struct {
	Store         MaintenanceStore
	Location      *time.Location
	RetentionDays int
	Interval      time.Duration
	Now           func() time.Time
	Logger        *log.Logger
}

type Maintenance struct {
	store         MaintenanceStore
	location      *time.Location
	retentionDays int
	interval      time.Duration
	now           func() time.Time
	logger        *log.Logger
}

func NewMaintenance(options MaintenanceOptions) (*Maintenance, error) {
	if options.Store == nil || options.Location == nil || options.RetentionDays < 1 ||
		options.Interval <= 0 || options.Now == nil {
		return nil, fmt.Errorf("invalid telemetry maintenance options")
	}
	if options.Logger == nil {
		options.Logger = log.New(io.Discard, "", 0)
	}
	return &Maintenance{
		store: options.Store, location: options.Location, retentionDays: options.RetentionDays,
		interval: options.Interval, now: options.Now, logger: options.Logger,
	}, nil
}

func (m *Maintenance) Run(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("maintenance context is required")
	}
	m.runOnce(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.runOnce(ctx)
		}
	}
}

func (m *Maintenance) runOnce(ctx context.Context) {
	now := m.now().In(m.location)
	completedBefore := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, m.location).UTC()
	if err := m.store.AggregateTelemetryDaily(ctx, completedBefore, m.location.String()); err != nil {
		m.logger.Printf(
			"telemetry daily aggregation failed completed_before=%s timezone=%q: %v",
			completedBefore.Format(time.RFC3339), m.location.String(), err,
		)
		return
	}
	retentionCutoff := now.AddDate(0, 0, -m.retentionDays).UTC()
	if _, err := m.store.PurgeTelemetryEvents(ctx, retentionCutoff); err != nil {
		m.logger.Printf(
			"telemetry retention purge failed occurred_before=%s retention_days=%d: %v",
			retentionCutoff.Format(time.RFC3339), m.retentionDays, err,
		)
	}
}
