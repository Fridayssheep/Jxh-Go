package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
)

const defaultSSEHeartbeat = 15 * time.Second

type EventSubscriber interface {
	Subscribe(ctx context.Context, options events.SubscribeOptions) (*events.Subscription, events.ReplayState, error)
}

type StreamAuthenticator interface {
	AuthenticatePassive(ctx context.Context, credential string) (auth.AuthContext, error)
}

type EventsHandler struct {
	hub           EventSubscriber
	authenticator StreamAuthenticator
	heartbeat     time.Duration
}

func NewEventsHandler(hub EventSubscriber, authenticator StreamAuthenticator, heartbeat time.Duration) (*EventsHandler, error) {
	if hub == nil || authenticator == nil {
		return nil, fmt.Errorf("event hub and stream authenticator are required")
	}
	if heartbeat == 0 {
		heartbeat = defaultSSEHeartbeat
	}
	if heartbeat < 0 {
		return nil, fmt.Errorf("event heartbeat must be positive")
	}
	return &EventsHandler{hub: hub, authenticator: authenticator, heartbeat: heartbeat}, nil
}

func (h *EventsHandler) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	return router.HandleFunc(http.MethodGet, "/api/admin/v1/events", RouteOptions{Permission: auth.PermissionEventsRead}, h.ServeHTTP)
}

func (h *EventsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	identity, ok := AuthFromContext(r.Context())
	if !ok {
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
		return
	}
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil || cookie.Value == "" {
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "需要登录", nil, false)
		return
	}
	validated, err := h.authenticator.AuthenticatePassive(r.Context(), cookie.Value)
	if err != nil || !sameStreamAuthorization(identity, validated) {
		if err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
			w.Header().Set("Retry-After", "3")
			writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "authentication service is unavailable", nil, true)
			return
		}
		writeAPIError(w, r, http.StatusUnauthorized, CodeUnauthorized, "登录状态无效或已过期", nil, false)
		return
	}
	identity = validated
	requested, err := parseEventTopics(r.URL.Query()["topic"])
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "事件主题无效", map[string][]string{"topic": {"主题必须唯一且属于支持的枚举"}}, false)
		return
	}
	subscription, _, err := h.hub.Subscribe(r.Context(), events.SubscribeOptions{
		AllowedTopics: allowedEventTopics(identity.User.Role), RequestedTopics: requested,
		LastEventID: r.Header.Get("Last-Event-ID"), SessionID: identity.Session.ID, UserID: identity.User.ID,
	})
	if err != nil {
		if errors.Is(err, events.ErrTopicForbidden) {
			writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "没有订阅该主题的权限", nil, false)
			return
		}
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "事件订阅参数无效", nil, false)
		return
	}
	defer subscription.Close()

	header := w.Header()
	header.Set("Content-Type", "text/event-stream; charset=utf-8")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})
	if err := controller.Flush(); err != nil {
		return
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-subscription.Done():
			return
		case event, open := <-subscription.Events():
			if !open || writeSSEEvent(w, event) != nil || controller.Flush() != nil {
				return
			}
			if event.Type == events.EventAuthSessionRevoked && event.Resource != nil && event.Resource.ID == identity.Session.ID {
				return
			}
		case <-ticker.C:
			validated, err := h.authenticator.AuthenticatePassive(r.Context(), cookie.Value)
			if err != nil || !sameStreamAuthorization(identity, validated) {
				return
			}
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil || controller.Flush() != nil {
				return
			}
		}
	}
}

func sameStreamAuthorization(initial, current auth.AuthContext) bool {
	return current.User.Enabled && current.User.ID == initial.User.ID && current.User.Role == initial.User.Role &&
		current.Session.ID == initial.Session.ID && current.Session.UserID == current.User.ID &&
		current.Session.Status == auth.SessionStatusActive && current.Session.RevokedAt == nil
}

func parseEventTopics(values []string) ([]events.Topic, error) {
	result := make([]events.Topic, 0, len(values))
	seen := make(map[events.Topic]struct{}, len(values))
	for _, value := range values {
		topic := events.Topic(value)
		if !knownEventTopic(topic) {
			return nil, events.ErrInvalidSubscription
		}
		if _, exists := seen[topic]; exists {
			return nil, events.ErrInvalidSubscription
		}
		seen[topic] = struct{}{}
		result = append(result, topic)
	}
	return result, nil
}

func knownEventTopic(topic events.Topic) bool {
	switch topic {
	case events.TopicOverview, events.TopicGroups, events.TopicSettings, events.TopicJoinRequests,
		events.TopicCommands, events.TopicScheduledJobs, events.TopicKnowledge, events.TopicSystem, events.TopicAuth:
		return true
	default:
		return false
	}
}

func allowedEventTopics(role auth.Role) []events.Topic {
	checks := []struct {
		permission auth.Permission
		topic      events.Topic
	}{
		{auth.PermissionOverviewRead, events.TopicOverview},
		{auth.PermissionGroupsRead, events.TopicGroups},
		{auth.PermissionSettingsRead, events.TopicSettings},
		{auth.PermissionJoinRequestsRead, events.TopicJoinRequests},
		{auth.PermissionCommandsRead, events.TopicCommands},
		{auth.PermissionScheduledJobsRead, events.TopicScheduledJobs},
		{auth.PermissionKnowledgeRead, events.TopicKnowledge},
		{auth.PermissionSystemRead, events.TopicSystem},
		{auth.PermissionEventsRead, events.TopicAuth},
	}
	result := make([]events.Topic, 0, len(checks))
	for _, check := range checks {
		if auth.Allowed(role, check.permission) {
			result = append(result, check.topic)
		}
	}
	return result
}

func writeSSEEvent(w http.ResponseWriter, event events.Event) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %s\nevent: %s\nretry: 3000\ndata: %s\n\n", event.ID, event.Type, payload)
	return err
}
