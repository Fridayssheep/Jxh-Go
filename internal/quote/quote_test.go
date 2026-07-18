package quote

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordingResolver struct {
	sources []string
}

func (r *recordingResolver) ResolveImage(_ context.Context, source string) (string, error) {
	r.sources = append(r.sources, source)
	return "https://example.com/resolved.gif", nil
}

func TestLatestQuoteContract(t *testing.T) {
	resolver := &recordingResolver{}
	payload := BuildPayload(context.Background(), []MessageInput{
		{
			UserID:   123,
			Nickname: " tester ",
			Message: []map[string]any{
				{"type": "face", "data": map[string]any{"id": 178}},
				{"type": "image", "data": map[string]any{"url": "https://example.com/animated.gif"}},
				{"type": "image", "data": map[string]any{"file": "napcat-image-token"}},
			},
		},
		{UserID: 456, Nickname: "second", RawMessage: "second message"},
		{UserID: 789, Nickname: "forward", Message: []map[string]any{{"type": "forward", "data": map[string]any{"id": "forward-id"}}}},
	}, resolver)

	if len(payload) != 2 {
		t.Fatalf("payload length = %d, want 2 non-empty messages", len(payload))
	}
	segments, ok := payload[0].Message.([]MessageSegment)
	if !ok || len(segments) != 3 {
		t.Fatalf("message = %#v, want three segments", payload[0].Message)
	}
	if segments[0].Type != "face" || segments[0].ID != "178" {
		t.Fatalf("face segment = %#v, want face ID 178", segments[0])
	}
	if segments[1].URL != "https://example.com/animated.gif" {
		t.Fatalf("remote image URL = %q, want unchanged URL", segments[1].URL)
	}
	if segments[2].URL != "https://example.com/resolved.gif" {
		t.Fatalf("resolved image URL = %q", segments[2].URL)
	}
	if len(resolver.sources) != 1 || resolver.sources[0] != "napcat-image-token" {
		t.Fatalf("resolved sources = %v, want only NapCat token", resolver.sources)
	}

	type request struct {
		path    string
		payload Payload
	}
	requests := make(chan request, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var received Payload
		_ = json.NewDecoder(r.Body).Decode(&received)
		requests <- request{path: r.URL.Path, payload: received}
		if r.URL.Path == "/gif/base64/" {
			http.Error(w, "GIF unavailable", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte("cG5n"))
	}))
	defer server.Close()

	result, err := NewClient(server.URL, server.Client()).Generate(context.Background(), payload)
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	gifRequest, pngRequest := <-requests, <-requests
	if gifRequest.path != "/gif/base64/" || pngRequest.path != "/png/base64/" {
		t.Fatalf("request paths = %q, %q", gifRequest.path, pngRequest.path)
	}
	if result != "cG5n" || len(pngRequest.payload) != 2 || pngRequest.payload[0].Avatar != "" {
		t.Fatalf("result = %q, payload = %#v", result, pngRequest.payload)
	}
}
