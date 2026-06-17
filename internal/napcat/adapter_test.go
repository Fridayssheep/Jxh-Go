package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/zjutjh/jxh-go/internal/bot"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

func TestHandleEventMarksGroupMessageReadBeforeHandling(t *testing.T) {
	var calls []string
	var markPayload map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, "mark")
		if r.URL.Path != "/mark_group_msg_as_read" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&markPayload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{}}`))
	}))
	defer apiServer.Close()

	client, err := napcatsdk.NewHTTPClient(apiServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	handler := &recordingHandler{calls: &calls}
	server := Server{Handler: handler}

	err = server.handleEvent(context.Background(), client, &event.GroupMessage{
		Base: event.Base{
			EventPostType: "message",
			EventSelfID:   999,
		},
		GroupID:    123,
		UserID:     456,
		MessageID:  789,
		Message:    message.ChainOf(message.Text("精小弘")),
		RawMessage: "精小弘",
	})
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(calls, []string{"mark", "handle"}) {
		t.Fatalf("calls = %#v", calls)
	}
	if markPayload["group_id"] != "123" || markPayload["message_id"] != "789" {
		t.Fatalf("mark payload = %#v", markPayload)
	}
	if handler.lastMessage.Text != "精小弘" {
		t.Fatalf("handler message = %#v", handler.lastMessage)
	}
}

type recordingHandler struct {
	calls       *[]string
	lastMessage bot.GroupMessage
}

func (h *recordingHandler) HandleGroupMessage(ctx context.Context, msg bot.GroupMessage) error {
	_ = ctx
	*h.calls = append(*h.calls, "handle")
	h.lastMessage = msg
	return nil
}

func (h *recordingHandler) HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error {
	_ = ctx
	_ = groupID
	_ = userID
	return nil
}
