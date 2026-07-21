package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"time"

	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

type Handler interface {
	HandleGroupMessage(ctx context.Context, msg bot.GroupMessage) error
	HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error
}

type groupJoinRequestHandler interface {
	HandleGroupJoinRequest(ctx context.Context, record grouprequest.Record) error
}

type Server struct {
	Addr           string
	WSURL          string
	Token          string
	RequestTimeout time.Duration
	ReconnectDelay time.Duration
	Handler        Handler
}

func (s Server) Serve(ctx context.Context) error {
	if s.WSURL != "" {
		return s.serveForwardWebSocket(ctx)
	}
	return napcatsdk.ServeReverseWebSocket(ctx, s.Addr, func(client *napcatsdk.Client) {
		s.consume(ctx, client)
	}, napcatsdk.WithToken(s.Token), napcatsdk.WithRequestTimeout(s.RequestTimeout))
}

func (s Server) serveForwardWebSocket(ctx context.Context) error {
	delay := s.ReconnectDelay
	if delay <= 0 {
		delay = 5 * time.Second
	}
	for {
		client, err := napcatsdk.DialWebSocket(ctx, s.WSURL, napcatsdk.WithToken(s.Token), napcatsdk.WithRequestTimeout(s.RequestTimeout))
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("connect napcat websocket failed: %v", err)
			if !sleepContext(ctx, delay) {
				return nil
			}
			continue
		}
		log.Printf("connected to napcat websocket: %s", s.WSURL)
		s.consume(ctx, client)
		_ = client.Close()
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("napcat websocket disconnected, reconnecting in %s", delay)
		if !sleepContext(ctx, delay) {
			return nil
		}
	}
}

func (s Server) consume(ctx context.Context, client *napcatsdk.Client) {
	sender := SDKSender{client: client}
	if setter, ok := s.Handler.(interface{ SetSender(bot.Sender) }); ok {
		setter.SetSender(sender)
	}
	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			if err := s.handleEvent(ctx, client, ev); err != nil {
				log.Printf("handle napcat event failed: %v", err)
			}
		}
	}
}

func sleepContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s Server) handleEvent(ctx context.Context, client *napcatsdk.Client, ev event.Event) error {
	if s.Handler == nil {
		return nil
	}
	switch e := ev.(type) {
	case *event.GroupMessage:
		if err := markGroupMessageRead(ctx, client, e); err != nil {
			log.Printf("mark group message as read failed: %v", err)
		}
		return s.Handler.HandleGroupMessage(ctx, toGroupMessage(e))
	case *event.UnknownEvent:
		if record, ok, err := grouprequest.RecordFromEvent(e.Raw()); err != nil {
			return err
		} else if ok {
			if handler, ok := s.Handler.(groupJoinRequestHandler); ok {
				return handler.HandleGroupJoinRequest(ctx, record)
			}
			return nil
		}
		var notice struct {
			PostType   string `json:"post_type"`
			NoticeType string `json:"notice_type"`
			GroupID    int64  `json:"group_id"`
			UserID     int64  `json:"user_id"`
		}
		if err := json.Unmarshal(e.Raw(), &notice); err != nil {
			return nil
		}
		if notice.PostType == "notice" && notice.NoticeType == "group_increase" {
			return s.Handler.HandleGroupIncrease(ctx, notice.GroupID, notice.UserID)
		}
	}
	return nil
}

func toGroupMessage(e *event.GroupMessage) bot.GroupMessage {
	return bot.GroupMessage{
		GroupID:        e.GroupID,
		UserID:         e.UserID,
		SelfID:         e.SelfID(),
		Text:           e.Message.Text(),
		RawMessage:     e.RawMessage,
		MessageID:      e.MessageID,
		ReplyMessageID: extractReplyID(e.Message),
		IsSelf:         e.UserID == e.SelfID(),
		AtUsers:        extractAtUsers(e.Message),
		Segments:       e.Message,
	}
}

func markGroupMessageRead(ctx context.Context, client *napcatsdk.Client, e *event.GroupMessage) error {
	if client == nil || e == nil || e.MessageID == 0 {
		return nil
	}
	_, err := client.API().MarkGroupMsgAsRead(ctx, api.MarkGroupMsgAsReadRequest{
		GroupID:   strconv.FormatInt(e.GroupID, 10),
		MessageID: strconv.FormatInt(e.MessageID, 10),
	})
	return err
}

func extractReplyID(chain message.Chain) int64 {
	for _, seg := range chain.OfType("reply") {
		raw := seg.String("id")
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			return id
		}
	}
	return 0
}

type SDKSender struct {
	client *napcatsdk.Client
}

func (s SDKSender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	return s.SendGroupMessage(ctx, groupID, message.ChainOf(message.Text(text)))
}

func (s SDKSender) SendGroupMessage(ctx context.Context, groupID int64, msg message.Chain) error {
	_, err := s.client.API().SendGroupMsg(ctx, api.SendGroupMsgRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		Message: msg,
	})
	return err
}

type oneBotInt64 int64

func (v *oneBotInt64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		data = data[1 : len(data)-1]
	}
	parsed, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return fmt.Errorf("decode OneBot integer %q: %w", data, err)
	}
	*v = oneBotInt64(parsed)
	return nil
}

type quoteSender struct {
	UserID   oneBotInt64 `json:"user_id"`
	Card     string      `json:"card"`
	Nickname string      `json:"nickname"`
}

type oneBotMessage struct {
	Chain message.Chain
}

func (m *oneBotMessage) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) || (len(data) > 0 && data[0] == '"') {
		return nil
	}
	if len(data) == 0 || data[0] != '[' {
		return fmt.Errorf("decode OneBot message: expected segment array or string")
	}
	return json.Unmarshal(data, &m.Chain)
}

type quoteMessage struct {
	MessageID  oneBotInt64   `json:"message_id"`
	MessageSeq oneBotInt64   `json:"message_seq"`
	UserID     oneBotInt64   `json:"user_id"`
	RawMessage string        `json:"raw_message"`
	Sender     quoteSender   `json:"sender"`
	Message    oneBotMessage `json:"message"`
}

func (s SDKSender) GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error) {
	resp, err := s.client.API().GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		UserID:  strconv.FormatInt(userID, 10),
		NoCache: true,
	})
	if err != nil {
		return "", err
	}
	return resp.Role, nil
}

func (s SDKSender) GetQuoteMessages(ctx context.Context, groupID, messageID int64, count int) ([]bot.QuotedMessage, error) {
	target, err := s.getQuoteMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if count <= 1 {
		return []bot.QuotedMessage{target.quoted()}, nil
	}
	messageSeq := int64(target.MessageSeq)
	if messageSeq <= 0 {
		return nil, fmt.Errorf("被引用消息缺少 message_seq")
	}
	var history struct {
		Messages []quoteMessage `json:"messages"`
	}
	err = s.client.API().Call(ctx, string(api.ActionGetGroupMsgHistory), api.GetGroupMsgHistoryRequest{
		GroupID:    strconv.FormatInt(groupID, 10),
		MessageSeq: strconv.FormatInt(max(1, messageSeq-int64(count-1)), 10),
		Count:      int64(count),
	}, &history)
	if err != nil {
		return nil, err
	}
	messages := make([]bot.QuotedMessage, 0, count)
	targetFound := false
	for _, message := range history.Messages {
		quoted := message.quoted()
		messages = append(messages, quoted)
		if quoted.MessageID == messageID {
			targetFound = true
			break
		}
		if len(messages) == count {
			break
		}
	}
	if !targetFound {
		messages = append(messages, target.quoted())
		if len(messages) > count {
			messages = messages[len(messages)-count:]
		}
	}
	return messages, nil
}

func (s SDKSender) getQuoteMessage(ctx context.Context, messageID int64) (quoteMessage, error) {
	var message quoteMessage
	err := s.client.API().Call(ctx, string(api.ActionGetMsg), api.GetMsgRequest{MessageID: messageID}, &message)
	return message, err
}

func (m quoteMessage) quoted() bot.QuotedMessage {
	userID := int64(m.UserID)
	if userID == 0 {
		userID = int64(m.Sender.UserID)
	}
	return bot.QuotedMessage{
		MessageID: int64(m.MessageID), UserID: userID,
		Nickname: senderNickname(m.Sender), RawMessage: m.RawMessage, Message: m.Message.Chain,
	}
}

func (s SDKSender) ResolveImage(ctx context.Context, file string) (string, error) {
	var data struct {
		URL  string `json:"url"`
		File string `json:"file"`
	}
	if err := s.client.API().Call(ctx, "get_image", map[string]any{"file": file}, &data); err != nil {
		return "", err
	}
	if data.URL != "" {
		return data.URL, nil
	}
	return data.File, nil
}

func (s SDKSender) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	_, err := s.client.API().SetGroupBan(ctx, api.SetGroupBanRequest{
		GroupID:  strconv.FormatInt(groupID, 10),
		UserID:   strconv.FormatInt(userID, 10),
		Duration: int64(duration.Seconds()),
	})
	return err
}

func (s SDKSender) SetRestart(ctx context.Context) error {
	_, err := s.client.API().SetRestart(ctx, api.SetRestartRequest{})
	return err
}

func (s SDKSender) FetchGroupJoinRequests(ctx context.Context, count int) ([]grouprequest.Record, error) {
	if count <= 0 {
		count = 20
	}
	resp, err := s.client.API().GetGroupSystemMsg(ctx, api.GetGroupSystemMsgRequest{Count: count})
	if err != nil {
		return nil, err
	}
	invited := append([]map[string]any{}, resp.InvitedRequest...)
	invited = append(invited, resp.InvitedRequests...)
	return grouprequest.RecordsFromSystemMessages(resp.JoinRequests, invited, time.Now()), nil
}

func extractAtUsers(chain message.Chain) []int64 {
	var out []int64
	for _, seg := range chain.OfType("at") {
		raw := seg.String("qq")
		if raw == "all" || raw == "" {
			continue
		}
		id, err := strconv.ParseInt(raw, 10, 64)
		if err == nil {
			out = append(out, id)
		}
	}
	return out
}

func senderNickname(sender quoteSender) string {
	if sender.Card != "" {
		return sender.Card
	}
	return sender.Nickname
}
