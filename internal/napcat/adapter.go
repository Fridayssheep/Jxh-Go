package napcat

import (
	"context"
	"crypto/sha1"
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

type Dedupe interface {
	Begin(ctx context.Context, key string) (duplicate bool, err error)
	Complete(ctx context.Context, key string) error
	Abort(key string)
}

type Server struct {
	Addr           string
	WSURL          string
	Token          string
	RequestTimeout time.Duration
	ReconnectDelay time.Duration
	Handler        Handler
	Dedupe         Dedupe
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
			if err := s.processEvent(ctx, client, ev); err != nil {
				log.Printf("handle napcat event failed: %v", err)
			}
		}
	}
}

func (s Server) processEvent(ctx context.Context, client *napcatsdk.Client, ev event.Event) error {
	key := eventKey(ev)
	if s.Dedupe != nil {
		duplicate, err := s.Dedupe.Begin(ctx, key)
		if err != nil {
			log.Printf("begin napcat event dedupe failed: %v", err)
		}
		if duplicate {
			return nil
		}
	}
	if err := s.handleEvent(ctx, client, ev); err != nil {
		if s.Dedupe != nil {
			s.Dedupe.Abort(key)
		}
		return err
	}
	if s.Dedupe != nil {
		if err := s.Dedupe.Complete(ctx, key); err != nil {
			log.Printf("complete napcat event dedupe failed: %v", err)
		}
	}
	return nil
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
		IsOwner:        e.Sender.Role == "owner",
		AtUsers:        extractAtUsers(e.Message),
		Segments:       toMessageSegments(e.Message),
	}
}

func toMessageSegments(chain message.Chain) []bot.MessageSegment {
	segments := make([]bot.MessageSegment, 0, len(chain))
	for _, segment := range chain {
		segments = append(segments, bot.MessageSegment{Type: segment.Type, Data: segment.Data})
	}
	return segments
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

func eventKey(ev event.Event) string {
	switch e := ev.(type) {
	case *event.GroupMessage:
		return fmt.Sprintf("group-message:%d:%d:%d", e.GroupID, e.MessageID, e.Time())
	case *event.PrivateMessage:
		return fmt.Sprintf("private-message:%d:%d:%d", e.UserID, e.MessageID, e.Time())
	case *event.UnknownEvent:
		sum := sha1.Sum(e.Raw())
		return fmt.Sprintf("unknown:%s:%d:%d:%x", e.PostType(), e.SelfID(), e.Time(), sum[:8])
	default:
		return fmt.Sprintf("%s:%d:%d", ev.PostType(), ev.SelfID(), ev.Time())
	}
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
	return s.SendGroupMessage(ctx, groupID, message.Text(text))
}

func (s SDKSender) SendGroupMessage(ctx context.Context, groupID int64, msg any) error {
	_, err := s.client.API().SendGroupMsg(ctx, api.SendGroupMsgRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		Message: msg,
	})
	return err
}

type quoteMessage struct {
	MessageID  any            `json:"message_id"`
	MessageSeq any            `json:"message_seq"`
	UserID     any            `json:"user_id"`
	RawMessage string         `json:"raw_message"`
	Sender     map[string]any `json:"sender"`
	Message    any            `json:"message"`
}

func (s SDKSender) UploadGroupFile(ctx context.Context, groupID int64, path, name string) error {
	_, err := s.client.API().UploadGroupFile(ctx, api.UploadGroupFileRequest{
		GroupID:    strconv.FormatInt(groupID, 10),
		File:       path,
		Name:       name,
		UploadFile: true,
	})
	return err
}

func (s SDKSender) GetQuoteMessages(ctx context.Context, groupID, messageID int64, count int) ([]bot.QuotedMessage, error) {
	target, err := s.getQuoteMessage(ctx, messageID)
	if err != nil {
		return nil, err
	}
	if count <= 1 {
		return []bot.QuotedMessage{target.quoted()}, nil
	}
	messageSeq := anyString(target.MessageSeq)
	if messageSeq == "" {
		return nil, fmt.Errorf("被引用消息缺少 message_seq")
	}
	history, err := s.client.API().GetGroupMsgHistory(ctx, api.GetGroupMsgHistoryRequest{
		GroupID: strconv.FormatInt(groupID, 10), MessageSeq: messageSeq, Count: int64(count),
	})
	if err != nil {
		return nil, err
	}
	messages := make([]bot.QuotedMessage, 0, count)
	targetFound := false
	for _, item := range history.Messages {
		message, err := decodeQuoteMessage(item)
		if err != nil {
			return nil, err
		}
		quoted := message.quoted()
		targetFound = targetFound || quoted.MessageID == messageID
		messages = append(messages, quoted)
		if len(messages) == count {
			break
		}
	}
	if !targetFound {
		messages = append([]bot.QuotedMessage{target.quoted()}, messages...)
		if len(messages) > count {
			messages = messages[:count]
		}
	}
	return messages, nil
}

func (s SDKSender) getQuoteMessage(ctx context.Context, messageID int64) (quoteMessage, error) {
	var message quoteMessage
	err := s.client.API().Call(ctx, string(api.ActionGetMsg), api.GetMsgRequest{MessageID: messageID}, &message)
	return message, err
}

func decodeQuoteMessage(raw any) (quoteMessage, error) {
	data, err := json.Marshal(raw)
	if err != nil {
		return quoteMessage{}, fmt.Errorf("encode quote history message: %w", err)
	}
	var message quoteMessage
	if err := json.Unmarshal(data, &message); err != nil {
		return quoteMessage{}, fmt.Errorf("decode quote history message: %w", err)
	}
	return message, nil
}

func (m quoteMessage) quoted() bot.QuotedMessage {
	userID := anyInt64(m.UserID)
	if userID == 0 {
		userID = anyInt64(m.Sender["user_id"])
	}
	return bot.QuotedMessage{
		MessageID: anyInt64(m.MessageID), UserID: userID,
		Nickname: senderNickname(m.Sender), RawMessage: m.RawMessage, Message: m.Message,
	}
}

func (s SDKSender) ResolveImage(ctx context.Context, file string) (string, error) {
	var data map[string]any
	if err := s.client.API().Call(ctx, "get_image", map[string]any{"file": file}, &data); err != nil {
		return "", err
	}
	if url := anyString(data["url"]); url != "" {
		return url, nil
	}
	return anyString(data["file"]), nil
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

func senderNickname(sender map[string]any) string {
	if card, ok := sender["card"].(string); ok && card != "" {
		return card
	}
	if nickname, ok := sender["nickname"].(string); ok {
		return nickname
	}
	return ""
}

func anyString(v any) string {
	switch value := v.(type) {
	case string:
		return value
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return ""
	}
}

func anyInt64(v any) int64 {
	switch value := v.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case string:
		id, _ := strconv.ParseInt(value, 10, 64)
		return id
	default:
		return 0
	}
}
