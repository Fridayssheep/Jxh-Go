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

func TestSDKSenderGetQuoteMessageHandlesSegmentArray(t *testing.T) {
	var gotPayload map[string]any
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_msg" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotPayload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{
			"status": "ok",
			"retcode": 0,
			"data": {
				"message_id": 99,
				"user_id": "12345",
				"raw_message": "被引用的消息",
				"sender": {
					"nickname": "张三",
					"card": "群名片"
				},
				"message": [
					{"type": "text", "data": {"text": "被引用的消息"}}
				]
			}
		}`))
	}))
	defer apiServer.Close()

	client, err := napcatsdk.NewHTTPClient(apiServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	sender := NewSDKSender(client)
	msg, err := sender.GetQuoteMessage(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}

	if gotPayload["message_id"].(float64) != 99 {
		t.Fatalf("payload = %#v", gotPayload)
	}
	if msg.UserID != 12345 || msg.Nickname != "群名片" || msg.RawMessage != "被引用的消息" {
		t.Fatalf("message = %#v", msg)
	}
	segments, ok := msg.Message.([]any)
	if !ok || len(segments) != 1 {
		t.Fatalf("segments = %#v", msg.Message)
	}
}

func TestSDKSenderGetQuoteMessageRefreshesImageURLFromFile(t *testing.T) {
	var paths []string
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/get_msg":
			_, _ = w.Write([]byte(`{
				"status": "ok",
				"retcode": 0,
				"data": {
					"message_id": 99,
					"user_id": "12345",
					"raw_message": "[CQ:mface,file=internal-name.png]",
					"sender": {"nickname": "张三"},
					"message": [
						{"type": "mface", "data": {"file": "internal-name.png"}}
					]
				}
			}`))
		case "/get_image":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["file"] != "internal-name.png" {
				t.Fatalf("get_image payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{
				"status": "ok",
				"retcode": 0,
				"data": {
					"url": "https://multimedia.nt.qq.com.cn/download?fileid=refreshed"
				}
			}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer apiServer.Close()

	client, err := napcatsdk.NewHTTPClient(apiServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	sender := NewSDKSender(client)
	msg, err := sender.GetQuoteMessage(context.Background(), 99)
	if err != nil {
		t.Fatal(err)
	}

	if !reflect.DeepEqual(paths, []string{"/get_msg", "/get_image"}) {
		t.Fatalf("paths = %#v", paths)
	}
	segments := msg.Message.([]any)
	data := segments[0].(map[string]any)["data"].(map[string]any)
	if data["url"] != "https://multimedia.nt.qq.com.cn/download?fileid=refreshed" {
		t.Fatalf("message = %#v", msg.Message)
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
