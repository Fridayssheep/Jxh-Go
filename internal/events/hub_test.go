package events

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestSubscribeResetsExpiredCursorThenContinues(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	hub := newTestHub(t, Options{Capacity: 2, Retention: 15 * time.Minute, SubscriberBuffer: 4, Now: func() time.Time { return now }})
	publishTestEvent(t, hub, EventGroupUpdated, "group-2", now.Add(-2*time.Minute))
	publishTestEvent(t, hub, EventGroupUpdated, "group-3", now.Add(-time.Minute))

	sub, state, err := hub.Subscribe(t.Context(), SubscribeOptions{
		AllowedTopics: []Topic{TopicGroups},
		LastEventID:   "evt_missing",
		SessionID:     "session-1",
	})
	if err != nil || state != ReplayReset {
		t.Fatalf("Subscribe() state=%v err=%v", state, err)
	}
	reset := receiveEvent(t, sub)
	if reset.Type != EventStreamReset || reset.Topic != TopicSystem || reset.Reason != "cursor_unavailable" {
		t.Fatalf("reset event = %+v", reset)
	}

	live := publishTestEvent(t, hub, EventGroupUpdated, "group-4", now)
	if got := receiveEvent(t, sub); got.ID != live.ID {
		t.Fatalf("live event = %+v, want ID %s", got, live.ID)
	}
}

func TestSubscribeReplaysInOrderThenReceivesLiveWithoutGap(t *testing.T) {
	now := time.Unix(200, 0).UTC()
	hub := newTestHub(t, Options{Capacity: 8, Retention: time.Hour, SubscriberBuffer: 2, Now: func() time.Time { return now }})
	first := publishTestEvent(t, hub, EventSettingsUpdated, "settings-global", now.Add(-3*time.Minute))
	second := publishTestEvent(t, hub, EventSettingsUpdated, "settings-group", now.Add(-2*time.Minute))
	third := publishTestEvent(t, hub, EventSettingsUpdated, "settings-last", now.Add(-time.Minute))

	sub, state, err := hub.Subscribe(t.Context(), SubscribeOptions{
		AllowedTopics: []Topic{TopicSettings},
		LastEventID:   first.ID,
		SessionID:     "session-1",
	})
	if err != nil || state != ReplayAvailable {
		t.Fatalf("Subscribe() state=%v err=%v", state, err)
	}
	live := publishTestEvent(t, hub, EventSettingsUpdated, "settings-live", now)
	for index, want := range []Event{second, third, live} {
		if got := receiveEvent(t, sub); got.ID != want.ID {
			t.Fatalf("event %d = %+v, want %+v", index, got, want)
		}
	}
}

func TestSubscribeEnforcesAllowedAndRequestedTopics(t *testing.T) {
	hub := newTestHub(t, Options{Capacity: 8, Retention: time.Hour, SubscriberBuffer: 4})
	if _, _, err := hub.Subscribe(t.Context(), SubscribeOptions{
		AllowedTopics:   []Topic{TopicSystem},
		RequestedTopics: []Topic{TopicGroups},
	}); !errors.Is(err, ErrTopicForbidden) {
		t.Fatalf("Subscribe() error = %v, want ErrTopicForbidden", err)
	}

	sub, _, err := hub.Subscribe(t.Context(), SubscribeOptions{AllowedTopics: []Topic{TopicSystem}})
	if err != nil {
		t.Fatal(err)
	}
	publishTestEvent(t, hub, EventGroupUpdated, "group-1", time.Time{})
	want := publishTestEvent(t, hub, EventSystemHealthChanged, "system", time.Time{})
	if got := receiveEvent(t, sub); got.ID != want.ID {
		t.Fatalf("filtered event = %+v, want %+v", got, want)
	}
}

func TestPublishRejectsUnsafeOrInvalidEnvelopeValues(t *testing.T) {
	hub := newTestHub(t, Options{Capacity: 8, Retention: time.Hour, SubscriberBuffer: 4})
	tests := []Draft{
		{Type: EventType("unknown.event"), Reason: "updated"},
		{Type: EventGroupUpdated, Resource: &Resource{Type: ResourceGroup, ID: "group-1"}, Reason: "contains application text"},
		{Type: EventGroupUpdated, Resource: &Resource{Type: ResourceType("unknown"), ID: "group-1"}, Reason: "updated"},
		{Type: EventGroupUpdated, Resource: &Resource{Type: ResourceGroup, ID: string([]byte{0xff})}, Reason: "updated"},
	}
	for _, draft := range tests {
		if _, err := hub.Publish(draft); !errors.Is(err, ErrInvalidEvent) {
			t.Fatalf("Publish(%+v) error = %v, want ErrInvalidEvent", draft, err)
		}
	}
}

func TestSlowSubscriberIsClosedWithoutBlockingPublish(t *testing.T) {
	hub := newTestHub(t, Options{Capacity: 8, Retention: time.Hour, SubscriberBuffer: 1})
	sub, _, err := hub.Subscribe(t.Context(), SubscribeOptions{AllowedTopics: []Topic{TopicGroups}})
	if err != nil {
		t.Fatal(err)
	}
	publishTestEvent(t, hub, EventGroupUpdated, "group-1", time.Time{})
	publishTestEvent(t, hub, EventGroupUpdated, "group-2", time.Time{})
	select {
	case <-sub.Done():
	case <-time.After(time.Second):
		t.Fatal("slow subscription was not closed")
	}
}

func TestCloseSessionClosesOnlyMatchingSubscriptions(t *testing.T) {
	hub := newTestHub(t, Options{Capacity: 8, Retention: time.Hour, SubscriberBuffer: 4})
	first, _, err := hub.Subscribe(t.Context(), SubscribeOptions{AllowedTopics: []Topic{TopicAuth}, SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := hub.Subscribe(t.Context(), SubscribeOptions{AllowedTopics: []Topic{TopicAuth}, SessionID: "session-2"})
	if err != nil {
		t.Fatal(err)
	}
	hub.CloseSession("session-1")
	select {
	case <-first.Done():
	case <-time.After(time.Second):
		t.Fatal("matching subscription remained open")
	}
	select {
	case <-second.Done():
		t.Fatal("unrelated subscription was closed")
	default:
	}
}

func TestHubSupportsConcurrentPublishSubscribeAndClose(t *testing.T) {
	hub := newTestHub(t, Options{Capacity: 64, Retention: time.Hour, SubscriberBuffer: 64})
	var workers sync.WaitGroup
	for worker := range 12 {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			for iteration := range 100 {
				sessionID := fmt.Sprintf("session-%d-%d", worker, iteration)
				sub, _, err := hub.Subscribe(t.Context(), SubscribeOptions{AllowedTopics: []Topic{TopicGroups}, SessionID: sessionID})
				if err != nil {
					t.Errorf("Subscribe() error = %v", err)
					return
				}
				_, _ = hub.Publish(Draft{Type: EventGroupUpdated, Resource: &Resource{Type: ResourceGroup, ID: "group-1"}, Reason: "updated"})
				hub.CloseSession(sessionID)
				<-sub.Done()
			}
		}(worker)
	}
	workers.Wait()
}

func TestHubSerializesInjectedIDSource(t *testing.T) {
	var sequence uint64
	hub, err := NewHub(Options{
		Capacity:         64,
		Retention:        time.Hour,
		SubscriberBuffer: 4,
		IDSource: func() (string, error) {
			sequence++
			return fmt.Sprintf("evt_%08d", sequence), nil
		},
		Now: func() time.Time { return time.Unix(1000, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 50 {
				if _, err := hub.Publish(Draft{Type: EventGroupUpdated, Reason: "updated"}); err != nil {
					t.Errorf("Publish() error = %v", err)
					return
				}
			}
		}()
	}
	workers.Wait()
}

func TestHubAllowsExactJoinRequestFlagsWithoutRelaxingOtherResources(t *testing.T) {
	hub := newTestHub(t, Options{Capacity: 8, Retention: time.Hour, SubscriberBuffer: 2})
	flag := strings.Repeat("申请/", 100)
	if _, err := hub.Publish(Draft{
		Type: EventJoinRequestUpdated, Resource: &Resource{Type: ResourceJoinRequest, ID: flag, Version: 2}, Reason: "updated",
	}); err != nil {
		t.Fatalf("publish exact join request flag: %v", err)
	}
	if _, err := hub.Publish(Draft{
		Type: EventGroupUpdated, Resource: &Resource{Type: ResourceGroup, ID: flag, Version: 2}, Reason: "updated",
	}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("non-join resource error = %v", err)
	}
	if _, err := hub.Publish(Draft{
		Type: EventJoinRequestUpdated, Resource: &Resource{Type: ResourceJoinRequest, ID: "unsafe\nflag", Version: 2}, Reason: "updated",
	}); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("control character error = %v", err)
	}
}

func newTestHub(t *testing.T, options Options) *Hub {
	t.Helper()
	var sequence atomic.Uint64
	options.IDSource = func() (string, error) {
		return fmt.Sprintf("evt_%08d", sequence.Add(1)), nil
	}
	if options.Now == nil {
		options.Now = func() time.Time { return time.Unix(1000, 0).UTC() }
	}
	hub, err := NewHub(options)
	if err != nil {
		t.Fatal(err)
	}
	return hub
}

func publishTestEvent(t *testing.T, hub *Hub, eventType EventType, id string, occurredAt time.Time) Event {
	t.Helper()
	event, err := hub.Publish(Draft{
		Type:       eventType,
		OccurredAt: occurredAt,
		Resource:   &Resource{Type: resourceTypeForEvent(eventType), ID: id, Version: 1},
		Reason:     "updated",
	})
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func receiveEvent(t *testing.T, subscription *Subscription) Event {
	t.Helper()
	select {
	case event, ok := <-subscription.Events():
		if !ok {
			t.Fatal("subscription closed before event")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return Event{}
	}
}
