package events

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidOptions      = errors.New("invalid event hub options")
	ErrInvalidEvent        = errors.New("invalid admin event")
	ErrInvalidSubscription = errors.New("invalid event subscription")
	ErrTopicForbidden      = errors.New("event topic forbidden")
	ErrIDSource            = errors.New("event ID source unavailable")
)

type Topic string

const (
	TopicOverview      Topic = "overview"
	TopicGroups        Topic = "groups"
	TopicSettings      Topic = "settings"
	TopicJoinRequests  Topic = "join_requests"
	TopicCommands      Topic = "commands"
	TopicScheduledJobs Topic = "scheduled_jobs"
	TopicKnowledge     Topic = "knowledge"
	TopicSystem        Topic = "system"
	TopicAuth          Topic = "auth"
)

type EventType string

const (
	EventOverviewUpdated          EventType = "overview.updated"
	EventGroupUpdated             EventType = "group.updated"
	EventSettingsUpdated          EventType = "settings.updated"
	EventJoinRequestCreated       EventType = "join_request.created"
	EventJoinRequestUpdated       EventType = "join_request.updated"
	EventCommandUpdated           EventType = "command.updated"
	EventCommandRunCompleted      EventType = "command.run_completed"
	EventScheduledJobUpdated      EventType = "scheduled_job.updated"
	EventScheduledJobRunCompleted EventType = "scheduled_job.run_completed"
	EventKnowledgeReloadCompleted EventType = "knowledge.reload_completed"
	EventSystemHealthChanged      EventType = "system.health_changed"
	EventStreamReset              EventType = "stream.reset"
	EventAuthSessionRevoked       EventType = "auth.session_revoked"
)

type ResourceType string

const (
	ResourceOverview     ResourceType = "overview"
	ResourceGroup        ResourceType = "group"
	ResourceSettings     ResourceType = "settings"
	ResourceJoinRequest  ResourceType = "join_request"
	ResourceCommand      ResourceType = "command"
	ResourceScheduledJob ResourceType = "scheduled_job"
	ResourceKnowledge    ResourceType = "knowledge"
	ResourceSystem       ResourceType = "system"
	ResourceSession      ResourceType = "session"
)

type Resource struct {
	Type    ResourceType `json:"type"`
	ID      string       `json:"id"`
	Version uint64       `json:"version"`
}

type Event struct {
	ID         string    `json:"event_id"`
	Type       EventType `json:"event"`
	OccurredAt time.Time `json:"occurred_at"`
	Resource   *Resource `json:"resource"`
	Reason     string    `json:"reason"`
	Topic      Topic     `json:"-"`
}

type Draft struct {
	Type       EventType
	OccurredAt time.Time
	Resource   *Resource
	Reason     string
}

type ReplayState uint8

const (
	ReplayLive ReplayState = iota
	ReplayAvailable
	ReplayReset
)

type Options struct {
	Capacity         int
	Retention        time.Duration
	SubscriberBuffer int
	IDSource         func() (string, error)
	Now              func() time.Time
}

type SubscribeOptions struct {
	AllowedTopics   []Topic
	RequestedTopics []Topic
	LastEventID     string
	SessionID       string
	UserID          string
}

type Hub struct {
	mu               sync.Mutex
	capacity         int
	retention        time.Duration
	subscriberBuffer int
	idSource         func() (string, error)
	now              func() time.Time
	events           []retainedEvent
	subscribers      map[uint64]*Subscription
	nextSubscriberID uint64
}

type retainedEvent struct {
	event       Event
	publishedAt time.Time
}

type Subscription struct {
	hub       *Hub
	id        uint64
	sessionID string
	userID    string
	topics    map[Topic]struct{}
	events    chan Event
	done      chan struct{}
	closeOnce sync.Once
}

var safeMachineValue = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

func NewHub(options Options) (*Hub, error) {
	if options.Capacity <= 0 || options.Retention <= 0 || options.SubscriberBuffer <= 0 || options.IDSource == nil || options.Now == nil {
		return nil, ErrInvalidOptions
	}
	return &Hub{
		capacity:         options.Capacity,
		retention:        options.Retention,
		subscriberBuffer: options.SubscriberBuffer,
		idSource:         options.IDSource,
		now:              options.Now,
		subscribers:      make(map[uint64]*Subscription),
	}, nil
}

func (h *Hub) Publish(draft Draft) (Event, error) {
	if h == nil {
		return Event{}, ErrInvalidOptions
	}
	topic, ok := topicForEvent(draft.Type)
	resource := copyResource(draft.Resource)
	if !ok || !validReason(draft.Reason) || !validResource(resource) {
		return Event{}, ErrInvalidEvent
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	publishedAt := h.now().UTC()
	id, err := h.idSource()
	if err != nil || !validOpaqueValue(id, 256) {
		return Event{}, ErrIDSource
	}
	occurredAt := draft.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = publishedAt
	}
	event := Event{
		ID:         id,
		Type:       draft.Type,
		OccurredAt: occurredAt.UTC(),
		Resource:   resource,
		Reason:     draft.Reason,
		Topic:      topic,
	}
	h.pruneLocked(publishedAt)
	h.events = append(h.events, retainedEvent{event: event, publishedAt: publishedAt})
	if overflow := len(h.events) - h.capacity; overflow > 0 {
		copy(h.events, h.events[overflow:])
		h.events = h.events[:h.capacity]
	}
	for id, subscription := range h.subscribers {
		if _, allowed := subscription.topics[event.Topic]; !allowed {
			continue
		}
		select {
		case subscription.events <- event:
		default:
			h.closeSubscriptionLocked(id, subscription)
		}
	}
	return event, nil
}

func (h *Hub) Subscribe(ctx context.Context, options SubscribeOptions) (*Subscription, ReplayState, error) {
	if h == nil || ctx == nil || !utf8.ValidString(options.SessionID) || len(options.SessionID) > 256 ||
		!utf8.ValidString(options.UserID) || len(options.UserID) > 256 {
		return nil, ReplayLive, ErrInvalidSubscription
	}
	topics, err := subscriptionTopics(options.AllowedTopics, options.RequestedTopics)
	if err != nil {
		return nil, ReplayLive, err
	}

	h.mu.Lock()
	h.pruneLocked(h.now())
	replay, state, err := h.replayLocked(options.LastEventID, topics)
	if err != nil {
		h.mu.Unlock()
		return nil, ReplayLive, err
	}
	h.nextSubscriberID++
	buffer := h.subscriberBuffer + len(replay)
	subscription := &Subscription{
		hub:       h,
		id:        h.nextSubscriberID,
		sessionID: options.SessionID,
		userID:    options.UserID,
		topics:    topics,
		events:    make(chan Event, buffer),
		done:      make(chan struct{}),
	}
	for _, event := range replay {
		subscription.events <- event
	}
	h.subscribers[subscription.id] = subscription
	h.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
			subscription.Close()
		case <-subscription.done:
		}
	}()
	return subscription, state, nil
}

func (h *Hub) CloseSession(sessionID string) {
	if h == nil || sessionID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, subscription := range h.subscribers {
		if subscription.sessionID == sessionID {
			h.closeSubscriptionLocked(id, subscription)
		}
	}
}

func (h *Hub) CloseUser(userID string) {
	if h == nil || userID == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, subscription := range h.subscribers {
		if subscription.userID == userID {
			h.closeSubscriptionLocked(id, subscription)
		}
	}
}

func (s *Subscription) Events() <-chan Event {
	if s == nil {
		return nil
	}
	return s.events
}

func (s *Subscription) Done() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.done
}

func (s *Subscription) Close() {
	if s == nil || s.hub == nil {
		return
	}
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if current, ok := s.hub.subscribers[s.id]; ok && current == s {
		s.hub.closeSubscriptionLocked(s.id, s)
	}
}

func (h *Hub) replayLocked(lastEventID string, topics map[Topic]struct{}) ([]Event, ReplayState, error) {
	if lastEventID == "" {
		return nil, ReplayLive, nil
	}
	if !validOpaqueValue(lastEventID, 256) {
		return nil, ReplayLive, ErrInvalidSubscription
	}
	cursor := -1
	for index := range h.events {
		if h.events[index].event.ID == lastEventID {
			cursor = index
			break
		}
	}
	if cursor < 0 {
		reset, err := h.resetEventLocked()
		if err != nil {
			return nil, ReplayLive, err
		}
		return []Event{reset}, ReplayReset, nil
	}
	replay := make([]Event, 0, len(h.events)-cursor-1)
	for _, retained := range h.events[cursor+1:] {
		event := retained.event
		if _, allowed := topics[event.Topic]; allowed {
			replay = append(replay, event)
		}
	}
	return replay, ReplayAvailable, nil
}

func (h *Hub) resetEventLocked() (Event, error) {
	id, err := h.idSource()
	if err != nil || !validOpaqueValue(id, 256) {
		return Event{}, ErrIDSource
	}
	return Event{
		ID:         id,
		Type:       EventStreamReset,
		OccurredAt: h.now().UTC(),
		Reason:     "cursor_unavailable",
		Topic:      TopicSystem,
	}, nil
}

func (h *Hub) pruneLocked(now time.Time) {
	cutoff := now.Add(-h.retention)
	retained := h.events[:0]
	for _, event := range h.events {
		if !event.publishedAt.Before(cutoff) {
			retained = append(retained, event)
		}
	}
	for index := len(retained); index < len(h.events); index++ {
		h.events[index] = retainedEvent{}
	}
	h.events = retained
}

func (h *Hub) closeSubscriptionLocked(id uint64, subscription *Subscription) {
	delete(h.subscribers, id)
	subscription.closeOnce.Do(func() {
		close(subscription.done)
		close(subscription.events)
	})
}

func subscriptionTopics(allowed, requested []Topic) (map[Topic]struct{}, error) {
	allowedSet := make(map[Topic]struct{}, len(allowed))
	for _, topic := range allowed {
		if !validTopic(topic) {
			return nil, ErrInvalidSubscription
		}
		allowedSet[topic] = struct{}{}
	}
	if len(requested) == 0 {
		if len(allowedSet) == 0 {
			return nil, ErrTopicForbidden
		}
		return allowedSet, nil
	}
	selected := make(map[Topic]struct{}, len(requested))
	for _, topic := range requested {
		if !validTopic(topic) {
			return nil, ErrInvalidSubscription
		}
		if _, ok := allowedSet[topic]; !ok {
			return nil, ErrTopicForbidden
		}
		selected[topic] = struct{}{}
	}
	return selected, nil
}

func topicForEvent(eventType EventType) (Topic, bool) {
	switch eventType {
	case EventOverviewUpdated:
		return TopicOverview, true
	case EventGroupUpdated:
		return TopicGroups, true
	case EventSettingsUpdated:
		return TopicSettings, true
	case EventJoinRequestCreated, EventJoinRequestUpdated:
		return TopicJoinRequests, true
	case EventCommandUpdated, EventCommandRunCompleted:
		return TopicCommands, true
	case EventScheduledJobUpdated, EventScheduledJobRunCompleted:
		return TopicScheduledJobs, true
	case EventKnowledgeReloadCompleted:
		return TopicKnowledge, true
	case EventSystemHealthChanged, EventStreamReset:
		return TopicSystem, true
	case EventAuthSessionRevoked:
		return TopicAuth, true
	default:
		return "", false
	}
}

func resourceTypeForEvent(eventType EventType) ResourceType {
	switch eventType {
	case EventOverviewUpdated:
		return ResourceOverview
	case EventGroupUpdated:
		return ResourceGroup
	case EventSettingsUpdated:
		return ResourceSettings
	case EventJoinRequestCreated, EventJoinRequestUpdated:
		return ResourceJoinRequest
	case EventCommandUpdated, EventCommandRunCompleted:
		return ResourceCommand
	case EventScheduledJobUpdated, EventScheduledJobRunCompleted:
		return ResourceScheduledJob
	case EventKnowledgeReloadCompleted:
		return ResourceKnowledge
	case EventSystemHealthChanged:
		return ResourceSystem
	case EventAuthSessionRevoked:
		return ResourceSession
	default:
		return ""
	}
}

func validTopic(topic Topic) bool {
	switch topic {
	case TopicOverview, TopicGroups, TopicSettings, TopicJoinRequests, TopicCommands,
		TopicScheduledJobs, TopicKnowledge, TopicSystem, TopicAuth:
		return true
	default:
		return false
	}
}

func validResource(resource *Resource) bool {
	if resource == nil {
		return true
	}
	switch resource.Type {
	case ResourceOverview, ResourceGroup, ResourceSettings, ResourceJoinRequest, ResourceCommand,
		ResourceScheduledJob, ResourceKnowledge, ResourceSystem, ResourceSession:
	default:
		return false
	}
	if resource.ID == "" {
		return true
	}
	if resource.Type == ResourceJoinRequest {
		return validJoinRequestID(resource.ID)
	}
	return validOpaqueValue(resource.ID, 256)
}

func validJoinRequestID(value string) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 512 {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return value != ""
}

func validReason(reason string) bool {
	return len(reason) >= 1 && len(reason) <= 100 && utf8.ValidString(reason) && safeMachineValue.MatchString(reason)
}

func validOpaqueValue(value string, maxLength int) bool {
	return len(value) >= 1 && len(value) <= maxLength && utf8.ValidString(value) && safeMachineValue.MatchString(value)
}

func copyResource(resource *Resource) *Resource {
	if resource == nil {
		return nil
	}
	copy := *resource
	return &copy
}
