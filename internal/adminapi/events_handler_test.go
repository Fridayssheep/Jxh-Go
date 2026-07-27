package adminapi

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/events"
)

func TestSSEReplaysResetFrameWithContractHeaders(t *testing.T) {
	hub := newSSETestHub(t)
	router := newHTTPFixture(t)
	handler, err := NewEventsHandler(hub, time.Hour)
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
	handler, err := NewEventsHandler(hub, time.Millisecond)
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

func TestSSERejectsDuplicateOrUnknownTopicsBeforeSubscription(t *testing.T) {
	hub := newSSETestHub(t)
	router := newHTTPFixture(t)
	handler, _ := NewEventsHandler(hub, time.Second)
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
	handler, _ := NewEventsHandler(hub, time.Hour)
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
