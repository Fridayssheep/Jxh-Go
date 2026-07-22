package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/api"
	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

type Server struct {
	WSURL          string
	Token          string
	RequestTimeout time.Duration
	ReconnectDelay time.Duration
	Handler        *bot.Pipeline
}

func (s Server) Serve(ctx context.Context) error {
	if strings.TrimSpace(s.WSURL) == "" {
		return fmt.Errorf("napcat websocket URL is required")
	}
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

// maxConcurrentEvents bounds how many events are handled in parallel so a burst
// of group messages/notices cannot spawn unbounded goroutines. Handling stays
// off the read loop so a slow path (e.g. /reload) never blocks event intake.
const maxConcurrentEvents = 32

func (s Server) consume(ctx context.Context, client *napcatsdk.Client) {
	sender := SDKSender{client: client}
	if s.Handler == nil {
		return
	}
	s.Handler.SetSender(sender)
	slots := make(chan struct{}, maxConcurrentEvents)
	events := client.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			// Bounded concurrency: acquire a slot before dispatching. If all slots
			// are busy this blocks briefly, applying backpressure instead of
			// spawning unbounded goroutines.
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return
			}
			go func(evt event.Event) {
				defer func() { <-slots }()
				if err := s.handleEvent(ctx, client, evt); err != nil {
					log.Printf("handle napcat event failed: %v", err)
				}
			}(ev)
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
			return s.Handler.HandleGroupJoinRequest(ctx, record)
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
	groupID := strconv.FormatInt(e.GroupID, 10)
	_, err := client.API().MarkGroupMsgAsRead(ctx, api.MarkGroupMsgAsReadRequest{
		GroupID: &groupID,
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
	encoded, err := api.NewOB11Message(msg)
	if err != nil {
		return fmt.Errorf("encode group message: %w", err)
	}
	groupIDText := strconv.FormatInt(groupID, 10)
	_, err = s.client.API().SendGroupMsg(ctx, api.SendGroupMsgRequest{
		GroupID: &groupIDText,
		Message: encoded,
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
	UserID     oneBotInt64   `json:"user_id"`
	RawMessage string        `json:"raw_message"`
	Sender     quoteSender   `json:"sender"`
	Message    oneBotMessage `json:"message"`
}

func (s SDKSender) GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error) {
	resp, err := s.client.API().GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		UserID:  strconv.FormatInt(userID, 10),
		NoCache: &api.GetGroupMemberInfoRequestNoCacheUnion{Raw: []byte("true")},
	})
	if err != nil {
		return "", err
	}
	if resp.Role == nil {
		return "", fmt.Errorf("NapCat 群成员信息缺少 role")
	}
	return *resp.Role, nil
}

func (s SDKSender) GetQuoteMessages(ctx context.Context, groupID, messageID int64, count int) ([]bot.QuotedMessage, error) {
	var history struct {
		Messages []quoteMessage `json:"messages"`
	}
	messageSeq := strconv.FormatInt(messageID, 10)
	err := s.client.API().Call(ctx, string(api.ActionGetGroupMsgHistory), api.GetGroupMsgHistoryRequest{
		GroupID:      strconv.FormatInt(groupID, 10),
		MessageSeq:   &messageSeq,
		Count:        float64(count),
		ReverseOrder: true,
	}, &history)
	if err != nil {
		return nil, fmt.Errorf("按引用消息获取群历史失败: %w", err)
	}
	targetIndex := slices.IndexFunc(history.Messages, func(message quoteMessage) bool {
		return int64(message.MessageID) == messageID
	})
	if targetIndex < 0 {
		return nil, fmt.Errorf("NapCat 返回的群历史中未找到被引用消息 %d", messageID)
	}
	start := max(0, targetIndex-count+1)
	messages := make([]bot.QuotedMessage, 0, targetIndex-start+1)
	for _, message := range history.Messages[start : targetIndex+1] {
		messages = append(messages, message.quoted())
	}
	s.enrichQuoteAtNames(ctx, groupID, messages)
	return messages, nil
}

func (s SDKSender) enrichQuoteAtNames(ctx context.Context, groupID int64, messages []bot.QuotedMessage) {
	names := map[string]string{"all": "全体成员"}
	for messageIndex := range messages {
		chain := message.ChainOf(messages[messageIndex].Message...)
		for segmentIndex, segment := range chain {
			if segment.Type != "at" || strings.TrimSpace(segment.String("name")) != "" ||
				strings.TrimSpace(segment.String("card")) != "" || strings.TrimSpace(segment.String("nickname")) != "" {
				continue
			}
			qq := strings.TrimSpace(segment.String("qq"))
			name, known := names[qq]
			if !known {
				userID, err := strconv.ParseInt(qq, 10, 64)
				if err != nil {
					continue
				}
				resp, err := s.client.API().GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
					GroupID: strconv.FormatInt(groupID, 10),
					UserID:  strconv.FormatInt(userID, 10),
					NoCache: &api.GetGroupMemberInfoRequestNoCacheUnion{Raw: []byte("true")},
				})
				if err == nil {
					card := ""
					if resp.Card != nil {
						card = strings.TrimSpace(*resp.Card)
					}
					name = senderNickname(quoteSender{Card: card, Nickname: strings.TrimSpace(resp.Nickname)})
				}
				names[qq] = name
			}
			if name == "" {
				continue
			}
			data, ok := segment.Data.(map[string]any)
			if !ok {
				continue
			}
			data = maps.Clone(data)
			data["name"] = name
			chain[segmentIndex].Data = data
		}
		messages[messageIndex].Message = chain
	}
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
	data, err := s.client.API().GetImage(ctx, api.GetImageRequest{File: &file})
	if err != nil {
		return "", err
	}
	if data.URL != nil && *data.URL != "" {
		return *data.URL, nil
	}
	if data.File != nil {
		return *data.File, nil
	}
	return "", nil
}

func (s SDKSender) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	_, err := s.client.API().SetGroupBan(ctx, api.SetGroupBanRequest{
		GroupID:  strconv.FormatInt(groupID, 10),
		UserID:   strconv.FormatInt(userID, 10),
		Duration: api.SetGroupBanRequestDurationUnion{Raw: []byte(strconv.FormatInt(int64(duration.Seconds()), 10))},
	})
	return err
}

func (s SDKSender) SetRestart(ctx context.Context) error {
	_, err := s.client.API().SetRestart(ctx, api.SetRestartRequest{})
	return err
}

func (s SDKSender) FetchGroupJoinRequests(ctx context.Context, count int) ([]grouprequest.Record, error) {
	resp, err := s.client.API().GetGroupSystemMsg(ctx, api.GetGroupSystemMsgRequest{
		Count: api.GetGroupSystemMsgRequestCountUnion{Raw: []byte(strconv.Itoa(count))},
	})
	if err != nil {
		return nil, err
	}
	return grouprequest.RecordsFromSystemMessages(resp.JoinRequests, resp.InvitedRequests, time.Now()), nil
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
