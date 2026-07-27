package quote

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientObservesPNGFallbackWithoutRawFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/gif/base64/" {
			http.Error(w, "sensitive upstream response", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("png-image"))
	}))
	defer server.Close()
	observed := make(chan Observation, 1)
	client := NewClient(server.URL, server.Client(), func(value Observation) { observed <- value })
	image, outcome, err := client.GenerateWithOutcome(t.Context(), Payload{{UserID: 1, UserNickname: "User", Message: []MessageSegment{{Type: "text", Text: "hello"}}}})
	if err != nil || image != "png-image" || outcome != OutcomePNGFallback {
		t.Fatalf("image=%q outcome=%q error=%v", image, outcome, err)
	}
	observation := <-observed
	if observation.Outcome != OutcomePNGFallback || observation.OccurredAt.IsZero() || observation.Latency < 0 {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestClientObservesCompleteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive upstream response", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	observed := make(chan Observation, 1)
	client := NewClient(server.URL, server.Client(), func(value Observation) { observed <- value })
	_, outcome, err := client.GenerateWithOutcome(context.Background(), Payload{})
	if err == nil {
		t.Fatal("Generate() unexpectedly succeeded")
	}
	if outcome != OutcomeFailure {
		t.Fatalf("outcome=%q", outcome)
	}
	if message := err.Error(); strings.Contains(message, "sensitive upstream response") || !strings.Contains(message, "HTTP 503") {
		t.Fatalf("Generate() error = %q", message)
	}
	if observation := <-observed; observation.Outcome != OutcomeFailure {
		t.Fatalf("observation=%+v", observation)
	}
}
