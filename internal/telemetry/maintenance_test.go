package telemetry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMaintenanceUsesCompletedLocalDayAndRetentionCutoff(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*60*60)
	now := time.Date(2026, 7, 28, 10, 30, 0, 0, location)
	store := &maintenanceStoreFake{}
	maintenance, err := NewMaintenance(MaintenanceOptions{
		Store: store, Location: location, RetentionDays: 30, Interval: time.Hour,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewMaintenance() error = %v", err)
	}
	maintenance.runOnce(t.Context())
	wantCompletedBefore := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	if !store.completedBefore.Equal(wantCompletedBefore) {
		t.Fatalf("completedBefore = %v, want %v", store.completedBefore, wantCompletedBefore)
	}
	wantRetention := now.AddDate(0, 0, -30).UTC()
	if !store.occurredBefore.Equal(wantRetention) {
		t.Fatalf("occurredBefore = %v, want %v", store.occurredBefore, wantRetention)
	}
}

func TestMaintenanceStoreFailuresDoNotStopWorker(t *testing.T) {
	store := &maintenanceStoreFake{err: errors.New("unavailable")}
	maintenance, err := NewMaintenance(MaintenanceOptions{
		Store: store, Location: time.UTC, RetentionDays: 7, Interval: time.Millisecond,
		Now: func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		t.Fatalf("NewMaintenance() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := maintenance.Run(ctx); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if store.aggregateCalls < 2 || store.purgeCalls < 2 {
		t.Fatalf("calls = aggregate %d purge %d", store.aggregateCalls, store.purgeCalls)
	}
}

type maintenanceStoreFake struct {
	completedBefore time.Time
	occurredBefore  time.Time
	aggregateCalls  int
	purgeCalls      int
	err             error
}

func (s *maintenanceStoreFake) AggregateTelemetryDaily(_ context.Context, before time.Time) error {
	s.aggregateCalls++
	s.completedBefore = before
	return s.err
}

func (s *maintenanceStoreFake) PurgeTelemetryEvents(_ context.Context, before time.Time) (int64, error) {
	s.purgeCalls++
	s.occurredBefore = before
	return 0, s.err
}
