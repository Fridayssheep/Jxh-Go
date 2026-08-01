package grouprequest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/events"
)

func TestJoinRequestEventsFollowSuccessfulWrites(t *testing.T) {
	now := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC)
	store := &groupRequestEventStore{}
	publisher := &groupRequestEventPublisher{}
	service := NewService(store, Options{Now: func() time.Time { return now }, Events: publisher})
	record := Record{Flag: "request-1", GroupID: 10001, UserID: 20001, SubType: "add"}

	if err := service.Record(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if len(publisher.drafts) != 1 || publisher.drafts[0].Type != events.EventJoinRequestCreated ||
		publisher.drafts[0].Resource == nil || publisher.drafts[0].Resource.ID != record.Flag {
		t.Fatalf("record event=%+v", publisher.drafts)
	}

	if err := service.Reconcile(t.Context(), []Record{record}); err != nil {
		t.Fatal(err)
	}
	if len(publisher.drafts) != 2 || publisher.drafts[1].Type != events.EventJoinRequestUpdated ||
		publisher.drafts[1].Reason != "join_request_reconciled" {
		t.Fatalf("reconcile events=%+v", publisher.drafts)
	}

	store.upsertErr = errors.New("database unavailable")
	if err := service.Record(t.Context(), record); err == nil {
		t.Fatal("failed write unexpectedly succeeded")
	}
	if len(publisher.drafts) != 2 {
		t.Fatalf("failed write published event=%+v", publisher.drafts)
	}
}

func TestAICompletionPublishesJoinRequestUpdate(t *testing.T) {
	now := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC)
	record := Record{ID: 1, Flag: "request-ai", GroupID: 10001, UserID: 20001, SubType: "add", Comment: "student details"}
	store := &groupRequestEventStore{pending: []Record{record}}
	publisher := &groupRequestEventPublisher{}
	service := NewService(store, Options{
		Now: func() time.Time { return now }, Events: publisher,
		ExtractApplicant: func(context.Context, string) (ExtractedFields, error) {
			return ExtractedFields{StudentID: "20260001", StudentName: "Student", Major: "Computer Science"}, nil
		},
	})

	service.processPendingAI(t.Context())
	if store.completedID != record.ID || len(publisher.drafts) != 1 ||
		publisher.drafts[0].Type != events.EventJoinRequestUpdated ||
		publisher.drafts[0].Resource == nil || publisher.drafts[0].Resource.ID != record.Flag ||
		publisher.drafts[0].Reason != "join_request_ai_parsed" {
		t.Fatalf("completed=%d events=%+v", store.completedID, publisher.drafts)
	}
}

func TestAIFailurePublishesJoinRequestUpdateAfterStateIsSaved(t *testing.T) {
	now := time.Date(2026, 8, 2, 5, 0, 0, 0, time.UTC)
	record := Record{ID: 2, Flag: "request-ai-failed", GroupID: 10001, UserID: 20001, SubType: "add", Comment: "unparseable"}
	store := &groupRequestEventStore{pending: []Record{record}}
	publisher := &groupRequestEventPublisher{}
	service := NewService(store, Options{
		Now: nowFunc(now), Events: publisher,
		ExtractApplicant: func(context.Context, string) (ExtractedFields, error) {
			return ExtractedFields{}, errors.New("AI unavailable")
		},
	})

	service.processPendingAI(t.Context())
	if store.failedID != record.ID || len(publisher.drafts) != 1 ||
		publisher.drafts[0].Type != events.EventJoinRequestUpdated ||
		publisher.drafts[0].Resource == nil || publisher.drafts[0].Resource.ID != record.Flag ||
		publisher.drafts[0].Reason != "join_request_ai_failed" {
		t.Fatalf("failed=%d events=%+v", store.failedID, publisher.drafts)
	}
}

func nowFunc(now time.Time) func() time.Time {
	return func() time.Time { return now }
}

type groupRequestEventStore struct {
	upsertErr   error
	pending     []Record
	completedID uint64
	failedID    uint64
}

func (s *groupRequestEventStore) UpsertGroupJoinRequest(context.Context, Record) error {
	return s.upsertErr
}

func (*groupRequestEventStore) ListGroupJoinRequests(context.Context, int) ([]Record, error) {
	return nil, nil
}

func (s *groupRequestEventStore) ListPendingGroupJoinRequests(context.Context, int) ([]Record, error) {
	return append([]Record(nil), s.pending...), nil
}

func (s *groupRequestEventStore) CompleteGroupJoinRequestAI(_ context.Context, id uint64, _ ExtractedFields, _ time.Time) error {
	s.completedID = id
	return nil
}

func (s *groupRequestEventStore) FailGroupJoinRequestAI(_ context.Context, id uint64, _ int) error {
	s.failedID = id
	return nil
}

type groupRequestEventPublisher struct {
	drafts []events.Draft
}

func (p *groupRequestEventPublisher) Publish(draft events.Draft) (events.Event, error) {
	p.drafts = append(p.drafts, draft)
	return events.Event{}, nil
}
