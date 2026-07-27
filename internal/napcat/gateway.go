package napcat

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/flashfile"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
)

var ErrUnavailable = errors.New("napcat sender is not connected")

var ErrOperationFailed = errors.New("napcat operation failed")

type FailureCode string

const (
	FailureCanceled         FailureCode = "canceled"
	FailureTimeout          FailureCode = "timeout"
	FailureDisconnected     FailureCode = "disconnected"
	FailureTransport        FailureCode = "transport_error"
	FailureUpstreamRejected FailureCode = "upstream_rejected"
	FailureInvalidResponse  FailureCode = "invalid_response"
	FailureUnknown          FailureCode = "upstream_error"
)

type OperationError struct {
	Operation string
	Code      FailureCode
	match     error
}

func (e *OperationError) Error() string {
	return fmt.Sprintf("napcat %s failed: %s", e.Operation, e.Code)
}

func (e *OperationError) Is(target error) bool {
	return target == ErrOperationFailed || (e.match != nil && target == e.match)
}

const (
	maxGroupRequestFlagLength   = 512
	maxGroupRequestReasonLength = 500
	maxJSONSafeInteger          = 1<<53 - 1
)

type Snapshot struct {
	Generation     uint64
	Connected      bool
	ConnectedAt    time.Time
	DisconnectedAt time.Time
	LastEventAt    time.Time
	LastError      string
}

type GroupInfo struct {
	ID             int64
	Name           string
	Remark         string
	MemberCount    int
	MaxMemberCount int
}

type gatewayState struct {
	snapshot Snapshot
	client   *api.Client
}

type Gateway struct {
	state      atomic.Pointer[gatewayState]
	flashFiles *flashfile.Stager
}

func NewGateway(flashFiles ...*flashfile.Stager) *Gateway {
	gateway := &Gateway{}
	if len(flashFiles) > 0 {
		gateway.flashFiles = flashFiles[0]
	}
	gateway.state.Store(&gatewayState{})
	return gateway
}

func (g *Gateway) Attach(client *api.Client, connectedAt time.Time) uint64 {
	if client == nil {
		panic("napcat gateway cannot attach a nil client")
	}
	for {
		current := g.load()
		next := &gatewayState{
			snapshot: current.snapshot,
			client:   client,
		}
		next.snapshot.Generation++
		next.snapshot.Connected = true
		next.snapshot.ConnectedAt = connectedAt
		if g.state.CompareAndSwap(current, next) {
			return next.snapshot.Generation
		}
	}
}

func (g *Gateway) Detach(generation uint64, err error, disconnectedAt time.Time) {
	for {
		current := g.load()
		if !current.snapshot.Connected || current.snapshot.Generation != generation {
			return
		}
		next := &gatewayState{snapshot: current.snapshot}
		next.snapshot.Connected = false
		next.snapshot.DisconnectedAt = disconnectedAt
		next.snapshot.LastError = safeErrorSummary(err)
		if g.state.CompareAndSwap(current, next) {
			return
		}
	}
}

func (g *Gateway) RecordEvent(generation uint64, eventAt time.Time) {
	for {
		current := g.load()
		if !current.snapshot.Connected || current.snapshot.Generation != generation ||
			!eventAt.After(current.snapshot.LastEventAt) {
			return
		}
		next := &gatewayState{snapshot: current.snapshot, client: current.client}
		next.snapshot.LastEventAt = eventAt
		if g.state.CompareAndSwap(current, next) {
			return
		}
	}
}

func (g *Gateway) RecordError(err error) {
	if err == nil {
		return
	}
	for {
		current := g.load()
		if current.snapshot.Connected {
			return
		}
		next := &gatewayState{snapshot: current.snapshot}
		next.snapshot.LastError = safeErrorSummary(err)
		if g.state.CompareAndSwap(current, next) {
			return
		}
	}
}

func (g *Gateway) Snapshot() Snapshot {
	return g.load().snapshot
}

func (g *Gateway) client() (*api.Client, error) {
	state := g.load()
	if !state.snapshot.Connected || state.client == nil {
		return nil, ErrUnavailable
	}
	return state.client, nil
}

func (g *Gateway) load() *gatewayState {
	if state := g.state.Load(); state != nil {
		return state
	}
	initial := &gatewayState{}
	if g.state.CompareAndSwap(nil, initial) {
		return initial
	}
	return g.state.Load()
}

func safeErrorSummary(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled):
		return "NapCat connection canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "NapCat connection timed out"
	case errors.Is(err, ErrUnavailable):
		return ErrUnavailable.Error()
	default:
		return "NapCat connection error"
	}
}

func safeOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	result := &OperationError{Operation: operation, Code: FailureUnknown}
	switch {
	case errors.Is(err, context.Canceled):
		result.Code, result.match = FailureCanceled, context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		result.Code, result.match = FailureTimeout, context.DeadlineExceeded
	case errors.Is(err, napcatsdk.ErrTimeout):
		result.Code, result.match = FailureTimeout, napcatsdk.ErrTimeout
	case errors.Is(err, napcatsdk.ErrClosed):
		result.Code, result.match = FailureDisconnected, napcatsdk.ErrClosed
	default:
		var apiErr *napcatsdk.APIError
		var protocolErr *napcatsdk.ProtocolError
		var transportErr *napcatsdk.TransportError
		switch {
		case errors.As(err, &apiErr):
			result.Code = FailureUpstreamRejected
		case errors.As(err, &protocolErr):
			result.Code = FailureInvalidResponse
		case errors.As(err, &transportErr):
			result.Code = FailureTransport
		}
	}
	return result
}

func operationFailure(operation string, code FailureCode) error {
	return &OperationError{Operation: operation, Code: code}
}

func (g *Gateway) GetGroupList(ctx context.Context) ([]GroupInfo, error) {
	client, err := g.client()
	if err != nil {
		return nil, err
	}
	response, err := client.GetGroupList(ctx, api.GetGroupListRequest{
		NoCache: &api.GetGroupListRequestNoCacheUnion{Raw: []byte("true")},
	})
	if err != nil {
		return nil, safeOperationError("get_group_list", err)
	}
	groups := make([]GroupInfo, 0, len(*response))
	for _, group := range *response {
		groupID, err := positiveInteger(group.GroupID, "group_id")
		if err != nil {
			return nil, operationFailure("get_group_list", FailureInvalidResponse)
		}
		memberCount, err := optionalCount(group.MemberCount, "member_count")
		if err != nil {
			return nil, operationFailure("get_group_list", FailureInvalidResponse)
		}
		maxMemberCount, err := optionalCount(group.MaxMemberCount, "max_member_count")
		if err != nil {
			return nil, operationFailure("get_group_list", FailureInvalidResponse)
		}
		groups = append(groups, GroupInfo{
			ID:             groupID,
			Name:           group.GroupName,
			Remark:         group.GroupRemark,
			MemberCount:    memberCount,
			MaxMemberCount: maxMemberCount,
		})
	}
	return groups, nil
}

func (g *Gateway) GetLoginUserID(ctx context.Context) (int64, error) {
	client, err := g.client()
	if err != nil {
		return 0, err
	}
	response, err := client.GetLoginInfo(ctx, api.GetLoginInfoRequest{})
	if err != nil {
		return 0, safeOperationError("get_login_info", err)
	}
	userID, err := positiveInteger(response.UserID, "user_id")
	if err != nil {
		return 0, operationFailure("get_login_info", FailureInvalidResponse)
	}
	return userID, nil
}

func (g *Gateway) SetGroupAddRequest(ctx context.Context, flag string, approve bool, reason string) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	if !utf8.ValidString(flag) {
		return fmt.Errorf("group request flag must be valid UTF-8")
	}
	if strings.TrimSpace(flag) == "" {
		return fmt.Errorf("group request flag is required")
	}
	if utf8.RuneCountInString(flag) > maxGroupRequestFlagLength {
		return fmt.Errorf("group request flag exceeds %d characters", maxGroupRequestFlagLength)
	}
	if !utf8.ValidString(reason) {
		return fmt.Errorf("group request reason must be valid UTF-8")
	}
	if utf8.RuneCountInString(reason) > maxGroupRequestReasonLength {
		return fmt.Errorf("group request reason exceeds %d characters", maxGroupRequestReasonLength)
	}
	request := api.SetGroupAddRequestRequest{
		Flag:    flag,
		Approve: &api.SetGroupAddRequestRequestApproveUnion{Raw: []byte(fmt.Sprintf("%t", approve))},
	}
	if reason != "" {
		request.Reason = &reason
	}
	if _, err := client.SetGroupAddRequest(ctx, request); err != nil {
		return safeOperationError("set_group_add_request", err)
	}
	return nil
}

func positiveInteger(value float64, field string) (int64, error) {
	if value <= 0 || value > maxJSONSafeInteger || math.Trunc(value) != value {
		return 0, fmt.Errorf("%s must be a positive integer, got %v", field, value)
	}
	return int64(value), nil
}

func optionalCount(value *float64, field string) (int, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 || *value > maxJSONSafeInteger || *value > float64(math.MaxInt) || math.Trunc(*value) != *value {
		return 0, fmt.Errorf("%s must be a non-negative integer, got %v", field, *value)
	}
	return int(*value), nil
}
