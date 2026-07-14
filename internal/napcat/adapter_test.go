package napcat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

func TestSDKSenderGetsLiveGroupMemberRole(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/get_group_member_info" {
			t.Fatalf("request path = %q", r.URL.Path)
		}
		var request struct {
			GroupID string `json:"group_id"`
			UserID  string `json:"user_id"`
			NoCache bool   `json:"no_cache"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request.GroupID != "1001" || request.UserID != "2002" || !request.NoCache {
			t.Fatalf("request = %+v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","retcode":0,"data":{"group_id":1001,"user_id":2002,"role":"admin","nickname":"test"}}`))
	}))
	defer server.Close()

	client, err := napcatsdk.NewHTTPClient(server.URL)
	if err != nil {
		t.Fatalf("NewHTTPClient returned error: %v", err)
	}
	role, err := (SDKSender{client: client}).GetGroupMemberRole(context.Background(), 1001, 2002)
	if err != nil {
		t.Fatalf("GetGroupMemberRole returned error: %v", err)
	}
	if role != commands.GroupRoleAdmin {
		t.Fatalf("role = %q, want %q", role, commands.GroupRoleAdmin)
	}
}

type recordingHandler struct {
	groupRequest grouprequest.Record
	requestCalls int
	requestErr   error
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
	h.requestCalls++
	h.groupRequest = record
	return h.requestErr
}

type lifecycleDedupe struct {
	mu        sync.Mutex
	inFlight  map[string]bool
	completed map[string]bool
}

func (d *lifecycleDedupe) Begin(ctx context.Context, key string) (bool, error) {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completed[key] || d.inFlight[key] {
		return true, nil
	}
	if d.inFlight == nil {
		d.inFlight = make(map[string]bool)
	}
	d.inFlight[key] = true
	return false, nil
}

func (d *lifecycleDedupe) Complete(ctx context.Context, key string) error {
	_ = ctx
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.completed == nil {
		d.completed = make(map[string]bool)
	}
	d.completed[key] = true
	delete(d.inFlight, key)
	return nil
}

func (d *lifecycleDedupe) Abort(key string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.inFlight, key)
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
			message.Segment{Type: "json", Data: map[string]any{"data": `{"jumpUrl":"https://www.bilibili.com/video/BV1test/?vd_source=track"}`}},
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
	if len(msg.Segments) != 3 || msg.Segments[2].Type != "json" {
		t.Fatalf("Segments = %+v, want preserved json segment", msg.Segments)
	}
	data, ok := msg.Segments[2].Data.(map[string]any)
	if !ok || data["data"] == "" {
		t.Fatalf("json segment data = %#v", msg.Segments[2].Data)
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

func TestProcessEventRetriesAfterHandlerFailureThenDeduplicatesSuccess(t *testing.T) {
	raw := []byte(`{"time":1780000000,"post_type":"request","request_type":"group","group_id":1001,"user_id":2002,"flag":"flag-1"}`)
	ev := &event.UnknownEvent{Base: event.Base{
		EventTime: 1780000000, EventPostType: "request", EventSelfID: 999, RawData: raw,
	}}
	handler := &recordingHandler{requestErr: errors.New("database unavailable")}
	server := Server{Handler: handler, Dedupe: &lifecycleDedupe{}}

	if err := server.processEvent(context.Background(), nil, ev); err == nil {
		t.Fatal("first processEvent returned nil error")
	}
	handler.requestErr = nil
	if err := server.processEvent(context.Background(), nil, ev); err != nil {
		t.Fatalf("retry processEvent returned error: %v", err)
	}
	if err := server.processEvent(context.Background(), nil, ev); err != nil {
		t.Fatalf("duplicate processEvent returned error: %v", err)
	}
	if handler.requestCalls != 2 {
		t.Fatalf("handler calls = %d, want 2", handler.requestCalls)
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
