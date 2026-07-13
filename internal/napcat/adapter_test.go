package napcat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

type recordingHandler struct {
	groupRequest grouprequest.Record
}

func (h *recordingHandler) HandleGroupMessage(ctx context.Context, msg bot.GroupMessage) error {
	_ = ctx
	_ = msg
	return nil
}

func (h *recordingHandler) HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error {
	_ = ctx
	_ = groupID
	_ = userID
	return nil
}

func (h *recordingHandler) HandleGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	_ = ctx
	h.groupRequest = record
	return nil
}

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

func TestHandleEventRecordsGroupJoinRequest(t *testing.T) {
	raw := []byte(`{"time":1780000000,"post_type":"request","request_type":"group","sub_type":"add","self_id":999,"group_id":1001,"user_id":2002,"comment":"申请入群","flag":"flag-1"}`)
	handler := &recordingHandler{}
	server := Server{Handler: handler}

	err := server.handleEvent(context.Background(), nil, &event.UnknownEvent{Base: event.Base{
		EventTime:     1780000000,
		EventPostType: "request",
		EventSelfID:   999,
		RawData:       raw,
	}})

	if err != nil {
		t.Fatalf("handleEvent returned error: %v", err)
	}
	if handler.groupRequest.RequestKey != "flag-1" {
		t.Fatalf("recorded request = %+v", handler.groupRequest)
	}
}

func TestSDKSenderUploadsGroupFile(t *testing.T) {
	var request api.UploadGroupFileRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/upload_group_file" {
			t.Errorf("path = %q, want /upload_group_file", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"file_id":"file-1"}}`))
	}))
	defer server.Close()

	client, err := napcatsdk.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}
	sender := SDKSender{client: client}

	err = sender.UploadGroupFile(context.Background(), 123, "data/exports/group_requests/test.xlsx", "test.xlsx")

	if err != nil {
		t.Fatalf("UploadGroupFile returned error: %v", err)
	}
	if request.GroupID != "123" {
		t.Fatalf("group id = %q, want 123", request.GroupID)
	}
	if request.File != "data/exports/group_requests/test.xlsx" {
		t.Fatalf("file = %q", request.File)
	}
	if request.Name != "test.xlsx" {
		t.Fatalf("name = %q, want test.xlsx", request.Name)
	}
	if !request.UploadFile {
		t.Fatal("upload_file = false, want true")
	}
	if request.Folder != "" || request.FolderID != "" {
		t.Fatalf("folder/folder_id = %q/%q, want root", request.Folder, request.FolderID)
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
