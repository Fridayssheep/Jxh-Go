package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

func TestToGroupMessageMarksOwnerAndExtractsMentions(t *testing.T) {
	ev := &event.GroupMessage{
		GroupID:    1001,
		UserID:     2002,
		MessageID:  3003,
		RawMessage: "@bot /test",
		Sender: event.GroupSender{
			Role: "owner",
		},
		Message: message.ChainOf(
			message.At(9999),
			message.Text("/test"),
		),
	}
	ev.Base.EventSelfID = 9999

	msg := toGroupMessage(ev)

	if !msg.IsOwner {
		t.Fatal("IsOwner = false, want true")
	}
	if msg.SelfID != 9999 {
		t.Fatalf("SelfID = %d, want 9999", msg.SelfID)
	}
	if len(msg.AtUsers) != 1 || msg.AtUsers[0] != 9999 {
		t.Fatalf("AtUsers = %v, want [9999]", msg.AtUsers)
	}
	if msg.Text != "/test" {
		t.Fatalf("Text = %q, want /test", msg.Text)
	}
}

func TestGetQuoteMessagesUsesHistory(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/get_msg":
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"message_id":10,"message_seq":"100","user_id":1,"sender":{"nickname":"one"},"raw_message":"first","message":[{"type":"text","data":{"text":"first"}}]}}`))
		case "/get_group_msg_history":
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			requests <- request
			_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"messages":[{"message_id":10,"user_id":1,"sender":{"nickname":"one"},"raw_message":"first","message":[{"type":"text","data":{"text":"first"}}]},{"message_id":11,"user_id":2,"sender":{"nickname":"forward"},"message":[{"type":"forward","data":{"id":"forward-id"}}]},{"message_id":12,"user_id":3,"sender":{"nickname":"three"},"raw_message":"third","message":[{"type":"text","data":{"text":"third"}}]}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := napcatsdk.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}
	messages, err := (SDKSender{client: client}).GetQuoteMessages(context.Background(), 123, 10, 3)
	if err != nil {
		t.Fatalf("GetQuoteMessages returned error: %v", err)
	}
	request := <-requests
	if request["group_id"] != "123" || request["message_seq"] != "100" || request["count"] != float64(3) {
		t.Fatalf("history request = %#v", request)
	}
	if len(messages) != 3 || messages[0].MessageID != 10 || messages[1].MessageID != 11 || messages[2].MessageID != 12 {
		t.Fatalf("messages = %#v", messages)
	}
}
