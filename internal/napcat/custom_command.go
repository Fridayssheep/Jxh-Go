package napcat

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/zjutjh/jxh-go/internal/customcommand"
	"github.com/zjutjh/napcat-sdk/message"
)

// CustomCommandGateway narrows the shared NapCat Gateway to the four
// allowlisted custom-command actions and translates uncertain OneBot outcomes.
type CustomCommandGateway struct {
	gateway *Gateway
}

func NewCustomCommandGateway(gateway *Gateway) *CustomCommandGateway {
	return &CustomCommandGateway{gateway: gateway}
}

func (g *CustomCommandGateway) Available() bool {
	return g != nil && g.gateway != nil && g.gateway.Snapshot().Connected
}

func (g *CustomCommandGateway) ReplyText(ctx context.Context, groupID, text string) error {
	group, err := parsePositiveActionID(groupID)
	if err != nil {
		return err
	}
	if !g.Available() {
		return customcommand.ErrGatewayUnavailable
	}
	return mapCustomCommandActionError(g.gateway.SendGroupText(ctx, group, text))
}

func (g *CustomCommandGateway) Mention(ctx context.Context, groupID, memberID string) error {
	group, err := parsePositiveActionID(groupID)
	if err != nil {
		return err
	}
	member, err := parsePositiveActionID(memberID)
	if err != nil {
		return err
	}
	if !g.Available() {
		return customcommand.ErrGatewayUnavailable
	}
	return mapCustomCommandActionError(g.gateway.SendGroupMessage(ctx, group, message.ChainOf(message.At(member))))
}

func (g *CustomCommandGateway) MuteMember(ctx context.Context, groupID, memberID string, duration time.Duration) error {
	group, err := parsePositiveActionID(groupID)
	if err != nil {
		return err
	}
	member, err := parsePositiveActionID(memberID)
	if err != nil {
		return err
	}
	if !g.Available() {
		return customcommand.ErrGatewayUnavailable
	}
	return mapCustomCommandActionError(g.gateway.SetGroupBan(ctx, group, member, duration))
}

func (g *CustomCommandGateway) SendGroupText(ctx context.Context, groupID, text string) error {
	group, err := parsePositiveActionID(groupID)
	if err != nil {
		return err
	}
	if !g.Available() {
		return customcommand.ErrGatewayUnavailable
	}
	return mapCustomCommandActionError(g.gateway.SendGroupText(ctx, group, text))
}

func parsePositiveActionID(value string) (int64, error) {
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, customcommand.ErrInvalidInput
	}
	return parsed, nil
}

func mapCustomCommandActionError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrUnavailable) {
		return customcommand.ErrGatewayUnavailable
	}
	var operationError *OperationError
	if errors.As(err, &operationError) {
		switch operationError.Code {
		case FailureCanceled, FailureTimeout, FailureDisconnected, FailureTransport, FailureUnknown:
			return customcommand.ErrOutcomeUnknown
		}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return customcommand.ErrOutcomeUnknown
	}
	return err
}
