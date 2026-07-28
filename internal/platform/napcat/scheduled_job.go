package napcat

import (
	"context"
	"math"
	"strconv"

	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/message"
)

type ScheduledJobSender struct {
	gateway *Gateway
}

func NewScheduledJobSender(gateway *Gateway) *ScheduledJobSender {
	return &ScheduledJobSender{gateway: gateway}
}

func (s *ScheduledJobSender) Available() bool {
	return s != nil && s.gateway != nil && s.gateway.Snapshot().Connected
}

func (s *ScheduledJobSender) Send(ctx context.Context, groupID, text string) (string, error) {
	group, err := strconv.ParseInt(groupID, 10, 64)
	if err != nil || group <= 0 {
		return "", operationFailure("send_scheduled_job", FailureInvalidResponse)
	}
	if !s.Available() {
		return "", ErrUnavailable
	}
	client, err := s.gateway.client()
	if err != nil {
		return "", err
	}
	encoded, err := api.NewOB11Message(message.ChainOf(message.Text(text)))
	if err != nil {
		return "", operationFailure("send_scheduled_job", FailureInvalidResponse)
	}
	groupText := strconv.FormatInt(group, 10)
	response, err := client.SendGroupMsg(ctx, api.SendGroupMsgRequest{GroupID: &groupText, Message: encoded})
	if err != nil {
		return "", safeOperationError("send_scheduled_job", err)
	}
	if response == nil || response.MessageID <= 0 || response.MessageID > math.MaxInt64 || math.Trunc(response.MessageID) != response.MessageID {
		return "", operationFailure("send_scheduled_job", FailureInvalidResponse)
	}
	return strconv.FormatInt(int64(response.MessageID), 10), nil
}
