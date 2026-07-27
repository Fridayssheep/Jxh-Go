package quote

import (
	"context"
	"net/http"
	"net/http/httptest"
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
	image, err := client.Generate(t.Context(), Payload{{UserID: 1, UserNickname: "User", Message: []MessageSegment{{Type: "text", Text: "hello"}}}})
	if err != nil || image != "png-image" {
		t.Fatalf("image=%q error=%v", image, err)
	}
	observation := <-observed
	if observation.Outcome != OutcomePNGFallback || observation.OccurredAt.IsZero() || observation.Latency < 0 {
		t.Fatalf("observation=%+v", observation)
	}
}

func TestClientObservesCompleteFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	observed := make(chan Observation, 1)
	client := NewClient(server.URL, server.Client(), func(value Observation) { observed <- value })
	if _, err := client.Generate(context.Background(), Payload{}); err == nil {
		t.Fatal("Generate() unexpectedly succeeded")
	}
	if observation := <-observed; observation.Outcome != OutcomeFailure {
		t.Fatalf("observation=%+v", observation)
	}
}
