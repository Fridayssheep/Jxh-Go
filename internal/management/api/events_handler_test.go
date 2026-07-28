package adminapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

func TestSSEReplaysResetFrameWithContractHeaders(t *testing.T) {
	hub := newSSETestHub(t)
	router := newHTTPFixture(t)
	handler, err := NewEventsHandler(hub, testAuthenticator{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Register(router); err != nil {
		t.Fatal(err)
	}

	requestContext, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/events?topic=groups", nil).WithContext(requestContext)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	request.Header.Set("Last-Event-ID", "evt_missing")
	recorder := newCancelAfterFlushRecorder(cancel, 2)
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "text/event-stream; charset=utf-8" ||
		recorder.Header().Get("Cache-Control") != "no-cache" || recorder.Header().Get("X-Accel-Buffering") != "no" {
		t.Fatalf("status=%d headers=%v", recorder.Code, recorder.Header())
	}
	body := recorder.Body.String()
	for _, expected := range []string{"event: stream.reset", "retry: 3000", `"reason":"cursor_unavailable"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("SSE body missing %q: %s", expected, body)
		}
	}
}

func TestSSEWritesHeartbeatAndStopsOnContext(t *testing.T) {
	hub := newSSETestHub(t)
	identityRequest := httptest.NewRequest(http.MethodGet, "/api/admin/v1/events", nil)
	identityRequest.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	requestContext, cancel := context.WithCancel(identityRequest.Context())
	identityRequest = identityRequest.WithContext(requestContext)

	router := newHTTPFixture(t)
	handler, err := NewEventsHandler(hub, testAuthenticator{}, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Register(router); err != nil {
		t.Fatal(err)
	}
	recorder := newCancelAfterFlushRecorder(cancel, 2)
	router.ServeHTTP(recorder, identityRequest)
	if !strings.Contains(recorder.Body.String(), ": heartbeat\n\n") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}

func TestSSEWritesInitialCommentBeforeFirstFlush(t *testing.T) {
	hub := newSSETestHub(t)
	requestContext, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/events", nil).WithContext(requestContext)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	router := newHTTPFixture(t)
	handler, err := NewEventsHandler(hub, testAuthenticator{}, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Register(router); err != nil {
		t.Fatal(err)
	}

	recorder := newCancelAfterFlushRecorder(cancel, 1)
	router.ServeHTTP(recorder, request)

	if body := recorder.Body.String(); !strings.Contains(body, ": connected\n\n") {
		t.Fatalf("first flush body=%q", body)
	}
}

func TestSSERejectsDuplicateOrUnknownTopicsBeforeSubscription(t *testing.T) {
	hub := newSSETestHub(t)
	router := newHTTPFixture(t)
	handler, _ := NewEventsHandler(hub, testAuthenticator{}, time.Second)
	_ = handler.Register(router)
	for _, target := range []string{
		"/api/admin/v1/events?topic=groups&topic=groups",
		"/api/admin/v1/events?topic=unknown",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		assertErrorCode(t, response, http.StatusBadRequest, CodeBadRequest)
	}
}

func TestSSEClosesAfterCurrentSessionRevocation(t *testing.T) {
	hub := newSSETestHub(t)
	router := newHTTPFixture(t)
	handler, _ := NewEventsHandler(hub, testAuthenticator{}, time.Hour)
	_ = handler.Register(router)
	request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/events?topic=auth", nil)
	request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
	recorder := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(recorder, request)
	}()
	time.Sleep(10 * time.Millisecond)
	_, err := hub.Publish(events.Draft{
		Type: events.EventAuthSessionRevoked, Resource: &events.Resource{Type: events.ResourceSession, ID: "ses_1", Version: 1},
		Reason: "revoked",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not close after current session revocation")
	}
	if !strings.Contains(recorder.Body.String(), "event: auth.session_revoked") {
		t.Fatalf("body=%q", recorder.Body.String())
	}
}

func TestSSEClosesWhenPassiveAuthorizationChanges(t *testing.T) {
	initial, err := (testAuthenticator{}).Authenticate(t.Context(), "credential")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*mutableStreamAuthenticator)
	}{
		{name: "role downgrade", mutate: func(value *mutableStreamAuthenticator) {
			value.setRole(auth.RoleObserver)
		}},
		{name: "session revoked or expired", mutate: func(value *mutableStreamAuthenticator) {
			value.setError(auth.ErrUnauthenticated)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := newSSETestHub(t)
			streamAuth := newMutableStreamAuthenticator(initial)
			router := newHTTPFixture(t)
			handler, err := NewEventsHandler(hub, streamAuth, 2*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			if err := handler.Register(router); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "/api/admin/v1/events", nil)
			request.AddCookie(&http.Cookie{Name: SessionCookieName, Value: "credential"})
			recorder := httptest.NewRecorder()
			done := make(chan struct{})
			go func() {
				defer close(done)
				router.ServeHTTP(recorder, request)
			}()
			select {
			case <-streamAuth.calls:
			case <-time.After(time.Second):
				t.Fatal("SSE did not perform initial passive authentication")
			}
			test.mutate(streamAuth)
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("SSE remained open after authorization changed")
			}
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func newSSETestHub(t *testing.T) *events.Hub {
	t.Helper()
	var sequence atomic.Uint64
	hub, err := events.NewHub(events.Options{
		Capacity: 8, Retention: time.Hour, SubscriberBuffer: 4,
		IDSource: func() (string, error) { return fmt.Sprintf("evt_%d", sequence.Add(1)), nil },
		Now:      func() time.Time { return time.Unix(100, 0).UTC() },
	})
	if err != nil {
		t.Fatal(err)
	}
	return hub
}

type cancelAfterFlushRecorder struct {
	*httptest.ResponseRecorder
	cancel     context.CancelFunc
	flushLimit int
	flushes    int
	buffer     bytes.Buffer
}

type mutableStreamAuthenticator struct {
	mu       sync.RWMutex
	identity auth.AuthContext
	err      error
	calls    chan struct{}
}

func newMutableStreamAuthenticator(identity auth.AuthContext) *mutableStreamAuthenticator {
	return &mutableStreamAuthenticator{identity: identity, calls: make(chan struct{}, 8)}
}

func (a *mutableStreamAuthenticator) AuthenticatePassive(context.Context, string) (auth.AuthContext, error) {
	a.mu.RLock()
	identity, err := a.identity, a.err
	a.mu.RUnlock()
	select {
	case a.calls <- struct{}{}:
	default:
	}
	return identity, err
}

func (a *mutableStreamAuthenticator) setRole(role auth.Role) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.identity.User.Role = role
	a.identity.Permissions = auth.PermissionsFor(role)
}

func (a *mutableStreamAuthenticator) setError(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.err = err
}

func newCancelAfterFlushRecorder(cancel context.CancelFunc, limit int) *cancelAfterFlushRecorder {
	return &cancelAfterFlushRecorder{ResponseRecorder: httptest.NewRecorder(), cancel: cancel, flushLimit: limit}
}

func (r *cancelAfterFlushRecorder) Flush() {
	r.ResponseRecorder.Flush()
	r.flushes++
	if r.flushes >= r.flushLimit {
		r.cancel()
	}
}
