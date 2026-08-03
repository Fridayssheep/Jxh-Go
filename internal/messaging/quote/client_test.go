package quote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zjutjh/napcat-sdk/message"
)

func TestBuildPayloadExpandsNestedReplyObject(t *testing.T) {
	input := MessageInput{
		UserID: 2, Nickname: "当前用户", Message: message.ChainOf(message.Reply(101), message.Text("514")),
		Reply: &MessageInput{
			UserID: 1, Nickname: "更早用户", Message: message.ChainOf(message.Text("114")),
		},
	}
	payload := BuildPayload(t.Context(), []MessageInput{input}, nil)
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"user_id":2,"user_nickname":"当前用户","message":[{"type":"text","text":"514"}],"reply":{"user_nickname":"更早用户","message":[{"type":"text","text":"114"}]}}]`
	if string(encoded) != want {
		t.Fatalf("payload=%s want=%s", encoded, want)
	}
}

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
