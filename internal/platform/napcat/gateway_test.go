package napcat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
)

type gatewayCall struct {
	action string
	params any
}

func TestClassifyJoinDecisionErrorPreservesUnknownOutcomes(t *testing.T) {
	tests := []struct {
		name    string
		err     error
		outcome joinrequests.ExternalOutcome
		code    string
	}{
		{name: "confirmed", outcome: joinrequests.ExternalConfirmed},
		{name: "unavailable", err: ErrUnavailable, outcome: joinrequests.ExternalUnavailable, code: "dependency_unavailable"},
		{name: "rejected", err: operationFailure("set_group_add_request", FailureUpstreamRejected), outcome: joinrequests.ExternalFailed, code: "upstream_rejected"},
		{name: "timeout", err: operationFailure("set_group_add_request", FailureTimeout), outcome: joinrequests.ExternalUnknown, code: "upstream_timeout"},
		{name: "disconnected", err: operationFailure("set_group_add_request", FailureDisconnected), outcome: joinrequests.ExternalUnknown, code: "upstream_disconnected"},
		{name: "invalid response", err: operationFailure("set_group_add_request", FailureInvalidResponse), outcome: joinrequests.ExternalUnknown, code: "invalid_response"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := classifyJoinDecisionError(test.err)
			if result.Outcome != test.outcome || result.ErrorCode != test.code {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

type fakeGatewayCaller struct {
	mu      sync.Mutex
	calls   []gatewayCall
	handler func(action string, params, result any) error
}

func (f *fakeGatewayCaller) Call(_ context.Context, action string, params, result any) error {
	f.mu.Lock()
	f.calls = append(f.calls, gatewayCall{action: action, params: params})
	handler := f.handler
	f.mu.Unlock()
	if handler == nil {
		return nil
	}
	return handler(action, params, result)
}

func (f *fakeGatewayCaller) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func TestGatewayAttachDetachAndUnavailable(t *testing.T) {
	ctx := context.Background()
	gateway := NewGateway()

	if err := gateway.SendGroupText(ctx, 123, "hello"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SendGroupText() error = %v, want ErrUnavailable", err)
	}
	if err := gateway.SendGroupFlashFile(ctx, 123, "https://example.com/file.pdf", "file.pdf"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SendGroupFlashFile() error = %v, want ErrUnavailable before staging", err)
	}

	firstCaller := &fakeGatewayCaller{}
	firstConnectedAt := time.Unix(10, 0)
	firstGeneration := gateway.Attach(api.NewClient(firstCaller), firstConnectedAt)
	firstSnapshot := gateway.Snapshot()
	if !firstSnapshot.Connected {
		t.Fatal("Snapshot().Connected = false, want true")
	}
	if firstSnapshot.Generation != firstGeneration {
		t.Fatalf("Snapshot().Generation = %d, want %d", firstSnapshot.Generation, firstGeneration)
	}
	if !firstSnapshot.ConnectedAt.Equal(firstConnectedAt) {
		t.Fatalf("Snapshot().ConnectedAt = %v, want %v", firstSnapshot.ConnectedAt, firstConnectedAt)
	}

	secondCaller := &fakeGatewayCaller{}
	secondConnectedAt := time.Unix(15, 0)
	secondGeneration := gateway.Attach(api.NewClient(secondCaller), secondConnectedAt)
	gateway.Detach(firstGeneration, errors.New("old connection closed"), time.Unix(20, 0))
	if snapshot := gateway.Snapshot(); !snapshot.Connected || snapshot.Generation != secondGeneration {
		t.Fatalf("stale Detach changed current state: %+v", snapshot)
	}
	if err := gateway.SendGroupText(ctx, 123, "new connection"); err != nil {
		t.Fatalf("SendGroupText() error = %v", err)
	}
	if firstCaller.callCount() != 0 || secondCaller.callCount() != 1 {
		t.Fatalf("calls after reattach = first %d, second %d; want 0, 1", firstCaller.callCount(), secondCaller.callCount())
	}

	secret := "access_token=do-not-store-this"
	disconnectedAt := time.Unix(25, 0)
	gateway.Detach(secondGeneration, fmt.Errorf("socket closed: %s", secret), disconnectedAt)
	disconnected := gateway.Snapshot()
	if disconnected.Connected {
		t.Fatal("Snapshot().Connected = true, want false")
	}
	if !disconnected.DisconnectedAt.Equal(disconnectedAt) {
		t.Fatalf("Snapshot().DisconnectedAt = %v, want %v", disconnected.DisconnectedAt, disconnectedAt)
	}
	if disconnected.LastError == "" {
		t.Fatal("Snapshot().LastError is empty")
	}
	if strings.Contains(disconnected.LastError, secret) || strings.Contains(disconnected.LastError, "do-not-store-this") {
		t.Fatalf("Snapshot().LastError contains sensitive detail: %q", disconnected.LastError)
	}
	callsBeforeUnavailable := secondCaller.callCount()
	if err := gateway.SetRestart(ctx); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SetRestart() error = %v, want ErrUnavailable", err)
	}
	if secondCaller.callCount() != callsBeforeUnavailable {
		t.Fatalf("offline SetRestart made a network call: got %d calls, want %d", secondCaller.callCount(), callsBeforeUnavailable)
	}
}

func TestGatewayRecordsEventsForCurrentGeneration(t *testing.T) {
	gateway := NewGateway()
	firstGeneration := gateway.Attach(api.NewClient(&fakeGatewayCaller{}), time.Unix(10, 0))
	firstEventAt := time.Unix(11, 0)
	gateway.RecordEvent(firstGeneration, firstEventAt)
	if got := gateway.Snapshot().LastEventAt; !got.Equal(firstEventAt) {
		t.Fatalf("Snapshot().LastEventAt = %v, want %v", got, firstEventAt)
	}

	secondGeneration := gateway.Attach(api.NewClient(&fakeGatewayCaller{}), time.Unix(20, 0))
	gateway.RecordEvent(firstGeneration, time.Unix(30, 0))
	if got := gateway.Snapshot().LastEventAt; !got.Equal(firstEventAt) {
		t.Fatalf("stale RecordEvent changed last event time: got %v, want %v", got, firstEventAt)
	}
	secondEventAt := time.Unix(21, 0)
	gateway.RecordEvent(secondGeneration, secondEventAt)
	if got := gateway.Snapshot().LastEventAt; !got.Equal(secondEventAt) {
		t.Fatalf("Snapshot().LastEventAt = %v, want %v", got, secondEventAt)
	}
}

func TestGatewayGroupDirectoryAndRequestDecision(t *testing.T) {
	memberCount := float64(42)
	maxMemberCount := float64(200)
	caller := &fakeGatewayCaller{}
	caller.handler = func(action string, params, result any) error {
		switch api.Action(action) {
		case api.ActionGetGroupList:
			request, ok := params.(api.GetGroupListRequest)
			if !ok {
				return fmt.Errorf("get_group_list params type = %T", params)
			}
			if request.NoCache == nil || string(request.NoCache.Raw) != "true" {
				return fmt.Errorf("get_group_list no_cache = %#v, want true", request.NoCache)
			}
			response := result.(*api.GetGroupListResponse)
			*response = api.GetGroupListResponse{{
				GroupID:        123456,
				GroupName:      "测试群",
				GroupRemark:    "测试备注",
				MemberCount:    &memberCount,
				MaxMemberCount: &maxMemberCount,
			}}
		case api.ActionSetGroupAddRequest:
			request, ok := params.(api.SetGroupAddRequestRequest)
			if !ok {
				return fmt.Errorf("set_group_add_request params type = %T", params)
			}
			if request.Flag != "opaque-request-flag" {
				return fmt.Errorf("flag = %q", request.Flag)
			}
			if request.Approve == nil || string(request.Approve.Raw) != "false" {
				return fmt.Errorf("approve = %#v, want false", request.Approve)
			}
			if request.Reason == nil || *request.Reason != "信息不完整" {
				return fmt.Errorf("reason = %#v", request.Reason)
			}
		default:
			return fmt.Errorf("unexpected action %q", action)
		}
		return nil
	}

	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))
	groups, err := gateway.GetGroupList(context.Background())
	if err != nil {
		t.Fatalf("GetGroupList() error = %v", err)
	}
	wantGroups := []GroupInfo{{
		ID:             123456,
		Name:           "测试群",
		Remark:         "测试备注",
		MemberCount:    42,
		MaxMemberCount: 200,
	}}
	if !reflect.DeepEqual(groups, wantGroups) {
		t.Fatalf("GetGroupList() = %#v, want %#v", groups, wantGroups)
	}
	if err := gateway.SetGroupAddRequest(context.Background(), "opaque-request-flag", false, "信息不完整"); err != nil {
		t.Fatalf("SetGroupAddRequest() error = %v", err)
	}
}

func TestGatewaySetGroupAddRequestValidatesInputBeforeNetwork(t *testing.T) {
	caller := &fakeGatewayCaller{}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	for _, flag := range []string{"", strings.Repeat("x", 513), string([]byte{0xff})} {
		if err := gateway.SetGroupAddRequest(context.Background(), flag, true, ""); err == nil {
			t.Fatalf("SetGroupAddRequest(%q) error = nil", flag)
		}
	}
	for _, reason := range []string{strings.Repeat("x", 501), string([]byte{0xff})} {
		if err := gateway.SetGroupAddRequest(context.Background(), "valid-flag", false, reason); err == nil {
			t.Fatalf("SetGroupAddRequest() error = nil for invalid reason %q", reason)
		}
	}
	if caller.callCount() != 0 {
		t.Fatalf("invalid flags made %d network calls, want 0", caller.callCount())
	}
}

func TestGatewayRejectsGroupIDsOutsideJSONSafeIntegerRange(t *testing.T) {
	caller := &fakeGatewayCaller{
		handler: func(action string, _, result any) error {
			if api.Action(action) != api.ActionGetGroupList {
				return fmt.Errorf("unexpected action %q", action)
			}
			response := result.(*api.GetGroupListResponse)
			*response = api.GetGroupListResponse{{GroupID: 1 << 53, GroupName: "unsafe"}}
			return nil
		},
	}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	_, err := gateway.GetGroupList(context.Background())
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != FailureInvalidResponse {
		t.Fatalf("GetGroupList() error = %T %v, want invalid-response OperationError", err, err)
	}
}

func TestGatewayReturnsValidatedLoginUserID(t *testing.T) {
	caller := &fakeGatewayCaller{handler: func(action string, _, result any) error {
		if api.Action(action) != api.ActionGetLoginInfo {
			return fmt.Errorf("unexpected action %q", action)
		}
		response := result.(*api.GetLoginInfoResponse)
		response.UserID = 123456789
		return nil
	}}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))
	userID, err := gateway.GetLoginUserID(context.Background())
	if err != nil || userID != 123456789 {
		t.Fatalf("GetLoginUserID() = %d, %v", userID, err)
	}

	caller.handler = func(_ string, _, result any) error {
		result.(*api.GetLoginInfoResponse).UserID = 1 << 53
		return nil
	}
	if _, err := gateway.GetLoginUserID(context.Background()); err == nil || strings.Contains(err.Error(), "9.007") {
		t.Fatalf("GetLoginUserID() error = %v", err)
	}
}

func TestGatewayOperationErrorHidesUpstreamDetails(t *testing.T) {
	const secret = "access_token=do-not-log"
	caller := &fakeGatewayCaller{
		handler: func(_ string, _, _ any) error {
			return &napcatsdk.APIError{
				Action:  "send_group_msg",
				RetCode: 100,
				Message: secret,
				Raw:     []byte(`{"message":"` + secret + `"}`),
			}
		},
	}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	err := gateway.SendGroupText(context.Background(), 123, "hello")
	if err == nil {
		t.Fatal("SendGroupText() error = nil")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("SendGroupText() error leaked upstream detail: %q", err)
	}
	var operationErr *OperationError
	if !errors.As(err, &operationErr) {
		t.Fatalf("SendGroupText() error type = %T, want *OperationError", err)
	}
	if operationErr.Code != FailureUpstreamRejected {
		t.Fatalf("OperationError.Code = %q, want %q", operationErr.Code, FailureUpstreamRejected)
	}
	var upstreamErr *napcatsdk.APIError
	if errors.As(err, &upstreamErr) {
		t.Fatal("safe Gateway error unwraps to raw SDK APIError")
	}
}

func TestGatewayOperationErrorPreservesSafeTimeoutClassification(t *testing.T) {
	caller := &fakeGatewayCaller{
		handler: func(_ string, _, _ any) error {
			return fmt.Errorf("request with secret query failed: %w", context.DeadlineExceeded)
		},
	}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	err := gateway.SetRestart(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SetRestart() error = %v, want context.DeadlineExceeded classification", err)
	}
	if strings.Contains(err.Error(), "secret query") {
		t.Fatalf("SetRestart() error leaked transport detail: %q", err)
	}
}

func TestGatewayCapabilitiesHideSDKErrorDetails(t *testing.T) {
	const secret = "wording=private-upstream-payload"
	caller := &fakeGatewayCaller{
		handler: func(action string, _, _ any) error {
			return &napcatsdk.APIError{Action: action, RetCode: 100, Wording: secret}
		},
	}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	tests := []struct {
		name string
		call func() error
	}{
		{name: "group list", call: func() error { _, err := gateway.GetGroupList(context.Background()); return err }},
		{name: "group request decision", call: func() error {
			return gateway.SetGroupAddRequest(context.Background(), "valid-flag", true, "")
		}},
		{name: "flash file", call: func() error {
			return gateway.SendGroupFlashFile(context.Background(), 123, "/app/jxh-media/file.pdf", "file.pdf")
		}},
		{name: "member role", call: func() error {
			_, err := gateway.GetGroupMemberRole(context.Background(), 123, 456)
			return err
		}},
		{name: "quote history", call: func() error {
			_, err := gateway.GetQuoteMessages(context.Background(), 123, 456, 1)
			return err
		}},
		{name: "image resolution", call: func() error {
			_, err := gateway.ResolveImage(context.Background(), "image-id")
			return err
		}},
		{name: "group ban", call: func() error {
			return gateway.SetGroupBan(context.Background(), 123, 456, time.Minute)
		}},
		{name: "group request fetch", call: func() error {
			_, err := gateway.FetchGroupJoinRequests(context.Background(), 10)
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if err == nil {
				t.Fatal("capability error = nil")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("capability error leaked upstream detail: %q", err)
			}
			var operationErr *OperationError
			if !errors.As(err, &operationErr) || operationErr.Code != FailureUpstreamRejected {
				t.Fatalf("capability error = %T %v, want upstream-rejected OperationError", err, err)
			}
		})
	}
}

func TestGatewayHidesSensitiveDynamicResponseMessages(t *testing.T) {
	const secret = "signed-url=https://private.example/file?token=secret"
	caller := &fakeGatewayCaller{
		handler: func(action string, _, result any) error {
			if api.Action(action) != api.ActionCreateFlashTask {
				return fmt.Errorf("unexpected action %q", action)
			}
			response := result.(*api.CreateFlashTaskResponse)
			*response = map[string]any{"result": 1, "errMsg": secret}
			return nil
		},
	}
	gateway := NewGateway()
	gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	err := gateway.SendGroupFlashFile(context.Background(), 123, "/app/jxh-media/file.pdf", "file.pdf")
	if err == nil {
		t.Fatal("SendGroupFlashFile() error = nil")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "token=secret") {
		t.Fatalf("SendGroupFlashFile() error leaked dynamic response detail: %q", err)
	}
	var operationErr *OperationError
	if !errors.As(err, &operationErr) || operationErr.Code != FailureInvalidResponse {
		t.Fatalf("SendGroupFlashFile() error = %T %v, want invalid-response OperationError", err, err)
	}
}

func TestGatewayConcurrentStateAccess(t *testing.T) {
	gateway := NewGateway()
	caller := &fakeGatewayCaller{}
	generation := gateway.Attach(api.NewClient(caller), time.Unix(10, 0))

	const iterations = 200
	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for i := 0; i < iterations; i++ {
				_ = gateway.Snapshot()
				err := gateway.SendGroupText(context.Background(), 123, "concurrent")
				if err != nil && !errors.Is(err, ErrUnavailable) {
					t.Errorf("SendGroupText() error = %v", err)
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for i := 0; i < iterations; i++ {
			gateway.RecordEvent(generation, time.Unix(int64(11+i), 0))
			gateway.Detach(generation, errors.New("closed"), time.Unix(int64(1000+i), 0))
			generation = gateway.Attach(api.NewClient(caller), time.Unix(int64(2000+i), 0))
		}
	}()
	close(start)
	workers.Wait()
}
