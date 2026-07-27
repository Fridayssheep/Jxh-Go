package overview

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/knowledgeadmin"
	managersystem "github.com/zjutjh/jxh-go/internal/system"
)

func TestGetBuildsNaturalDayWindowAndStableMetricSet(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	pending := 4.0
	change := 25.0
	store := &fakeStore{data: Data{
		Metrics: map[MetricKey]MetricValue{
			MetricPendingJoinRequests: {Available: true, Value: &pending, ChangePercent: &change},
		},
		Pending: map[PendingKey]uint64{PendingJoinRequests: 4, PendingFailedJobs: 0},
		Trend: []TrendPoint{{
			BucketStart: time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC),
			Values:      map[string]float64{"join_request_count": 2},
		}},
	}}
	health := &fakeHealth{snapshot: managersystem.Health{Dependencies: []managersystem.DependencyHealth{
		{Key: managersystem.DependencyMySQL, Status: managersystem.DependencyHealthy, Required: true},
		{Key: managersystem.DependencyNapCat, Status: managersystem.DependencyUnavailable, Required: false},
		{Key: managersystem.DependencyWPS, Status: managersystem.DependencyNotConfigured, Required: false},
	}}}
	service := newService(t, store, health, &fakeKnowledge{status: knowledgeadmin.Status{ConflictCount: 2}}, location)
	snapshot, err := service.Get(t.Context(), observer(), Query{Range: Range7Days, GroupID: "00123"})
	if err != nil {
		t.Fatal(err)
	}
	if !store.query.From.Equal(time.Date(2026, 7, 21, 16, 0, 0, 0, time.UTC)) ||
		!store.query.To.Equal(time.Date(2026, 7, 28, 16, 0, 0, 0, time.UTC)) || store.query.Timezone != "Asia/Shanghai" {
		t.Fatalf("query=%+v", store.query)
	}
	if len(snapshot.Metrics) != 6 || snapshot.Metrics[0].Key != MetricPendingJoinRequests ||
		!snapshot.Metrics[0].Available || snapshot.Metrics[1].Available || snapshot.Metrics[5].Value == nil || *snapshot.Metrics[5].Value != 1 {
		t.Fatalf("metrics=%+v", snapshot.Metrics)
	}
	if len(snapshot.PendingItems) != 4 || snapshot.PendingItems[0].Severity != SeverityWarning ||
		snapshot.PendingItems[2].Key != PendingKnowledgeConflicts || snapshot.PendingItems[2].Count != 2 ||
		snapshot.PendingItems[3].Key != PendingDegradedDependencies || snapshot.PendingItems[3].Count != 1 ||
		snapshot.PendingItems[3].Severity != SeverityWarning {
		t.Fatalf("pending=%+v", snapshot.PendingItems)
	}
}

func TestGetMarksRequiredDependencyFailureCritical(t *testing.T) {
	store := &fakeStore{data: Data{Metrics: map[MetricKey]MetricValue{}, Pending: map[PendingKey]uint64{}}}
	health := &fakeHealth{snapshot: managersystem.Health{Dependencies: []managersystem.DependencyHealth{
		{Key: managersystem.DependencyMySQL, Status: managersystem.DependencyUnavailable, Required: true},
	}}}
	service := newService(t, store, health, &fakeKnowledge{}, time.UTC)
	snapshot, err := service.Get(t.Context(), observer(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	item := snapshot.PendingItems[len(snapshot.PendingItems)-1]
	if item.Count != 1 || item.Severity != SeverityCritical || snapshot.Range != Range7Days {
		t.Fatalf("snapshot=%+v", snapshot)
	}
}

func TestGetAuthorizesAndValidatesBeforeProviders(t *testing.T) {
	store := &fakeStore{}
	health := &fakeHealth{}
	service := newService(t, store, health, &fakeKnowledge{}, time.UTC)
	if _, err := service.Get(t.Context(), auth.Principal{Role: "invalid"}, Query{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("error=%v", err)
	}
	if _, err := service.Get(t.Context(), observer(), Query{Range: "90d"}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error=%v", err)
	}
	if store.calls != 0 || health.calls != 0 {
		t.Fatalf("store=%d health=%d", store.calls, health.calls)
	}
}

func TestGetRejectsInvalidAggregateData(t *testing.T) {
	value := math.NaN()
	store := &fakeStore{data: Data{Metrics: map[MetricKey]MetricValue{
		MetricActiveGroups: {Available: true, Value: &value},
	}}}
	service := newService(t, store, &fakeHealth{}, &fakeKnowledge{}, time.UTC)
	if _, err := service.Get(t.Context(), observer(), Query{}); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("error=%v", err)
	}
}

func TestGetReturnsIndependentSnapshot(t *testing.T) {
	value := 1.0
	store := &fakeStore{data: Data{
		Metrics: map[MetricKey]MetricValue{MetricActiveGroups: {Available: true, Value: &value}},
		Trend:   []TrendPoint{{BucketStart: time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC), Values: map[string]float64{"group_message_count": 3}}},
	}}
	service := newService(t, store, &fakeHealth{}, &fakeKnowledge{}, time.UTC)
	snapshot, err := service.Get(t.Context(), observer(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	*snapshot.Metrics[3].Value = 99
	snapshot.Trend[0].Values["group_message_count"] = 99
	if *store.data.Metrics[MetricActiveGroups].Value != 1 || store.data.Trend[0].Values["group_message_count"] != 3 {
		t.Fatal("returned snapshot aliases store data")
	}
}

func newService(t *testing.T, store Store, health HealthProvider, knowledge KnowledgeProvider, location *time.Location) *Service {
	t.Helper()
	service, err := NewService(Options{
		Store: store, Health: health, Knowledge: knowledge, Location: location,
		Now: func() time.Time { return time.Date(2026, 7, 28, 3, 4, 5, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func observer() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleObserver}
}

type fakeStore struct {
	data  Data
	err   error
	calls int
	query StoreQuery
}

func (s *fakeStore) LoadOverview(_ context.Context, query StoreQuery) (Data, error) {
	s.calls++
	s.query = query
	return s.data, s.err
}

type fakeHealth struct {
	snapshot managersystem.Health
	err      error
	calls    int
}

type fakeKnowledge struct {
	status knowledgeadmin.Status
	err    error
}

func (k *fakeKnowledge) Status(context.Context) (knowledgeadmin.Status, error) {
	return k.status, k.err
}

func (h *fakeHealth) Health(context.Context, auth.Principal) (managersystem.Health, error) {
	h.calls++
	return h.snapshot, h.err
}
