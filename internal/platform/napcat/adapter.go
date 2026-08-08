package napcat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/groups/grouprequest"
	"github.com/zjutjh/jxh-go/internal/platform/safego"
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
	Gateway        *Gateway
}

func (s Server) Serve(ctx context.Context) error {
	if strings.TrimSpace(s.WSURL) == "" {
		return fmt.Errorf("napcat websocket URL is required")
	}
	if s.Gateway == nil {
		return fmt.Errorf("napcat gateway is required")
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
			s.Gateway.RecordError(err)
			log.Printf("connect napcat websocket failed: %s", safeErrorSummary(err))
			if !sleepContext(ctx, delay) {
				return nil
			}
			continue
		}
		log.Printf("connected to napcat websocket")
		_ = s.consume(ctx, client)
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

const (
	groupRequestSyncCount    = 100
	groupRequestSyncInterval = 10 * time.Second
)

func (s Server) consume(ctx context.Context, client *napcatsdk.Client) (sessionErr error) {
	session := newSessionWorkers(ctx)
	generation := s.Gateway.Attach(client.API(), time.Now())
	defer func() {
		session.Close()
		s.Gateway.Detach(generation, sessionErr, time.Now())
	}()
	if s.Handler == nil {
		return fmt.Errorf("napcat event handler is required")
	}
	session.Start(func(ctx context.Context) { s.syncGroupJoinRequests(ctx, s.Gateway) })
	slots := make(chan struct{}, maxConcurrentEvents)
	events := client.Events()
	for {
		select {
		case <-session.Context().Done():
			return session.Context().Err()
		case ev, ok := <-events:
			if !ok {
				return fmt.Errorf("napcat event stream closed")
			}
			s.Gateway.RecordEvent(generation, time.Now())
			// Bounded concurrency: acquire a slot before dispatching. If all slots
			// are busy this blocks briefly, applying backpressure instead of
			// spawning unbounded goroutines.
			select {
			case slots <- struct{}{}:
			case <-session.Context().Done():
				return session.Context().Err()
			}
			eventValue := ev
			session.Start(func(sessionCtx context.Context) {
				defer func() { <-slots }()
				// 事件处理链全程处理外部可控输入，未恢复的 panic 会终止整个进程。
				defer safego.Recover("napcat event")
				if err := s.handleEvent(sessionCtx, client, eventValue); err != nil {
					log.Printf("handle napcat event failed: %v", err)
				}
			})
		}
	}
}

type sessionWorkers struct {
	ctx    context.Context
	cancel context.CancelFunc
	wait   sync.WaitGroup
}

func newSessionWorkers(parent context.Context) *sessionWorkers {
	ctx, cancel := context.WithCancel(parent)
	return &sessionWorkers{ctx: ctx, cancel: cancel}
}

func (s *sessionWorkers) Context() context.Context {
	return s.ctx
}

func (s *sessionWorkers) Start(worker func(context.Context)) {
	s.wait.Add(1)
	go func() {
		defer s.wait.Done()
		worker(s.ctx)
	}()
}

func (s *sessionWorkers) Close() {
	s.cancel()
	s.wait.Wait()
}

func (s Server) syncGroupJoinRequests(ctx context.Context, gateway *Gateway) {
	syncOnce := func() {
		// 恢复边界放在每轮工作上，一轮 panic 不会让整个同步循环静默退出。
		defer safego.Recover("group request sync")
		records, err := gateway.FetchGroupJoinRequests(ctx, groupRequestSyncCount)
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("fetch group join requests for automatic sync failed: %v", err)
			}
			if len(records) == 0 {
				return
			}
		}
		if err := s.Handler.ReconcileGroupJoinRequests(ctx, records); err != nil && ctx.Err() == nil {
			log.Printf("reconcile group join requests failed: %v", err)
		}
	}
	syncOnce()
	ticker := time.NewTicker(groupRequestSyncInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			syncOnce()
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
		GroupID:    e.GroupID,
		UserID:     e.UserID,
		SelfID:     e.SelfID(),
		Text:       e.Message.Text(),
		RawMessage: e.RawMessage,
		MessageID:  e.MessageID,
		Reply:      extractReplyRef(e.Message),
		IsSelf:     e.UserID == e.SelfID(),
		AtUsers:    extractAtUsers(e.Message),
		Segments:   e.Message,
	}
}

func markGroupMessageRead(ctx context.Context, client *napcatsdk.Client, e *event.GroupMessage) error {
	groupID := strconv.FormatInt(e.GroupID, 10)
	_, err := client.API().MarkGroupMsgAsRead(ctx, api.MarkGroupMsgAsReadRequest{
		GroupID: &groupID,
	})
	return safeOperationError("mark_group_message_read", err)
}

func extractReplyRef(chain message.Chain) bot.ReplyRef {
	for _, segment := range chain {
		if segment.Type != "reply" {
			continue
		}
		id, _ := strconv.ParseInt(strings.TrimSpace(segment.String("id")), 10, 64)
		seq, _ := strconv.ParseInt(strings.TrimSpace(segment.String("seq")), 10, 64)
		return bot.ReplyRef{ID: id, Seq: seq}
	}
	return bot.ReplyRef{}
}

func (g *Gateway) SendGroupText(ctx context.Context, groupID int64, text string) error {
	return g.SendGroupMessage(ctx, groupID, message.ChainOf(message.Text(text)))
}

func (g *Gateway) SendGroupMessage(ctx context.Context, groupID int64, msg message.Chain) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	encoded, err := api.NewOB11Message(msg)
	if err != nil {
		return operationFailure("send_group_message", FailureInvalidResponse)
	}
	groupIDText := strconv.FormatInt(groupID, 10)
	_, err = client.SendGroupMsg(ctx, api.SendGroupMsgRequest{
		GroupID: &groupIDText,
		Message: encoded,
	})
	return safeOperationError("send_group_message", err)
}

func (g *Gateway) SendGroupFile(ctx context.Context, groupID int64, source, name string) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	if groupID <= 0 {
		return fmt.Errorf("group ID must be positive")
	}
	filePath := source
	if strings.HasPrefix(strings.ToLower(source), "http://") || strings.HasPrefix(strings.ToLower(source), "https://") {
		if g.groupFiles == nil {
			return fmt.Errorf("group file stager is not initialized")
		}
		staged, err := g.groupFiles.Stage(ctx, source, name)
		if err != nil {
			return safeOperationError("stage_group_file", err)
		}
		filePath = staged
	} else if path.Clean(source) != source || !strings.HasPrefix(source, "/app/jxh-media/") || path.Base(source) != name {
		return fmt.Errorf("invalid local group file source")
	}

	groupIDText := strconv.FormatInt(groupID, 10)
	_, err = client.UploadGroupFile(ctx, api.UploadGroupFileRequest{
		GroupID:    groupIDText,
		File:       filePath,
		Name:       name,
		UploadFile: true,
	})
	return safeOperationError("upload_group_file", err)
}

func decodeDynamicValue(value, target any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, target)
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
	GroupID    oneBotInt64   `json:"group_id"`
	UserID     oneBotInt64   `json:"user_id"`
	RawMessage string        `json:"raw_message"`
	Sender     quoteSender   `json:"sender"`
	Message    oneBotMessage `json:"message"`
}

func (g *Gateway) GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error) {
	client, err := g.client()
	if err != nil {
		return "", err
	}
	resp, err := client.GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
		GroupID: strconv.FormatInt(groupID, 10),
		UserID:  strconv.FormatInt(userID, 10),
		NoCache: &api.GetGroupMemberInfoRequestNoCacheUnion{Raw: []byte("true")},
	})
	if err != nil {
		return "", safeOperationError("get_group_member_role", err)
	}
	if resp.Role == nil {
		return "", operationFailure("get_group_member_role", FailureInvalidResponse)
	}
	return *resp.Role, nil
}

const (
	maxQuoteReplyDepth = 3
	maxQuoteReplyNodes = 30
)

type quoteResolver struct {
	client  *api.Client
	groupID int64
	byID    map[int64]*bot.QuotedMessage
	bySeq   map[int64]*bot.QuotedMessage
	nodes   int
}

func (g *Gateway) GetQuoteMessages(ctx context.Context, groupID int64, ref bot.ReplyRef, count int) ([]bot.QuotedMessage, error) {
	client, err := g.client()
	if err != nil {
		return nil, err
	}
	resolver := quoteResolver{
		client: client, groupID: groupID,
		byID: make(map[int64]*bot.QuotedMessage), bySeq: make(map[int64]*bot.QuotedMessage),
	}
	messageSeq := ref.Seq
	var history []quoteMessage
	if messageSeq != 0 {
		history, err = resolver.history(ctx, messageSeq, count)
	}
	if messageSeq == 0 || err != nil || quoteMessageIndex(history, messageSeq) < 0 {
		if ref.ID == 0 {
			return nil, safeOperationError("get_quote_messages", err)
		}
		target, idErr := resolver.messageByID(ctx, ref.ID)
		if idErr != nil {
			return nil, safeOperationError("get_quote_messages", errors.Join(err, idErr))
		}
		messageSeq = target.MessageSeq
		if messageSeq == 0 {
			return nil, operationFailure("get_quote_messages", FailureInvalidResponse)
		}
		history, err = resolver.history(ctx, messageSeq, count)
		if err != nil {
			return nil, safeOperationError("get_quote_messages", err)
		}
	}
	targetIndex := quoteMessageIndex(history, messageSeq)
	if targetIndex < 0 {
		return nil, operationFailure("get_quote_messages", FailureInvalidResponse)
	}
	start := max(0, targetIndex-count+1)
	messages := make([]bot.QuotedMessage, 0, targetIndex-start+1)
	for _, wire := range history[start : targetIndex+1] {
		base := resolver.remember(wire)
		quoted := *base
		quoted.Reply = nil
		resolver.expand(ctx, &quoted, base, 1, map[*bot.QuotedMessage]struct{}{base: {}})
		messages = append(messages, quoted)
	}
	g.enrichQuoteAtNames(ctx, client, groupID, messages)
	return messages, nil
}

func quoteMessageIndex(messages []quoteMessage, seq int64) int {
	return slices.IndexFunc(messages, func(message quoteMessage) bool {
		return int64(message.MessageSeq) == seq
	})
}

func (r *quoteResolver) history(ctx context.Context, seq int64, count int) ([]quoteMessage, error) {
	var history struct {
		Messages []quoteMessage `json:"messages"`
	}
	messageSeq := strconv.FormatInt(seq, 10)
	err := r.client.Call(ctx, string(api.ActionGetGroupMsgHistory), api.GetGroupMsgHistoryRequest{
		GroupID: strconv.FormatInt(r.groupID, 10), MessageSeq: &messageSeq,
		Count: float64(count), ReverseOrder: true,
	}, &history)
	if err != nil {
		return nil, err
	}
	for _, message := range history.Messages {
		r.remember(message)
	}
	return history.Messages, nil
}

func (r *quoteResolver) remember(message quoteMessage) *bot.QuotedMessage {
	id, seq := int64(message.MessageID), int64(message.MessageSeq)
	cached := r.bySeq[seq]
	if cached == nil {
		cached = r.byID[id]
	}
	quoted := message.quoted()
	if cached == nil {
		cached = &quoted
	} else {
		*cached = quoted
	}
	if id != 0 {
		r.byID[id] = cached
	}
	if seq != 0 {
		r.bySeq[seq] = cached
	}
	return cached
}

func (r *quoteResolver) resolve(ctx context.Context, ref bot.ReplyRef) (*bot.QuotedMessage, error) {
	if ref.Seq != 0 {
		if cached := r.bySeq[ref.Seq]; cached != nil {
			return cached, nil
		}
		history, err := r.history(ctx, ref.Seq, 1)
		if err == nil {
			if index := quoteMessageIndex(history, ref.Seq); index >= 0 {
				return r.remember(history[index]), nil
			}
		}
	}
	if ref.ID != 0 {
		return r.messageByID(ctx, ref.ID)
	}
	return nil, fmt.Errorf("reply reference has no id or seq")
}

func (r *quoteResolver) messageByID(ctx context.Context, id int64) (*bot.QuotedMessage, error) {
	if cached := r.byID[id]; cached != nil {
		return cached, nil
	}
	var message quoteMessage
	err := r.client.Call(ctx, string(api.ActionGetMsg), api.GetMsgRequest{
		MessageID: api.GetMsgRequestMessageIDUnion{Raw: []byte(strconv.FormatInt(id, 10))},
	}, &message)
	if err != nil {
		return nil, err
	}
	if int64(message.MessageID) != id || (message.GroupID != 0 && int64(message.GroupID) != r.groupID) {
		return nil, fmt.Errorf("NapCat get_msg returned an invalid message")
	}
	return r.remember(message), nil
}

func (r *quoteResolver) expand(ctx context.Context, target, base *bot.QuotedMessage, depth int, visiting map[*bot.QuotedMessage]struct{}) {
	ref := extractReplyRef(base.Message)
	if ref == (bot.ReplyRef{}) {
		return
	}
	if depth > maxQuoteReplyDepth || r.nodes >= maxQuoteReplyNodes {
		target.Reply = quoteFallback("[更早的回复已省略]")
		return
	}
	r.nodes++
	next, err := r.resolve(ctx, ref)
	if err != nil {
		log.Printf("get replied quote message failed: group=%d reply_id=%d reply_seq=%d: %v", r.groupID, ref.ID, ref.Seq, err)
		target.Reply = quoteFallback("[引用消息不可用]")
		return
	}
	if _, cycle := visiting[next]; cycle {
		target.Reply = quoteFallback("[循环回复已省略]")
		return
	}
	reply := *next
	reply.Reply = nil
	target.Reply = &reply
	visiting[next] = struct{}{}
	r.expand(ctx, &reply, next, depth+1, visiting)
	delete(visiting, next)
}

func quoteFallback(text string) *bot.QuotedMessage {
	return &bot.QuotedMessage{Nickname: "匿名", RawMessage: text}
}

func (g *Gateway) enrichQuoteAtNames(ctx context.Context, client *api.Client, groupID int64, messages []bot.QuotedMessage) {
	names := map[string]string{"all": "全体成员"}
	var enrich func(*bot.QuotedMessage)
	enrich = func(quoted *bot.QuotedMessage) {
		if quoted == nil {
			return
		}
		chain := message.ChainOf(quoted.Message...)
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
				resp, err := client.GetGroupMemberInfo(ctx, api.GetGroupMemberInfoRequest{
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
		quoted.Message = chain
		enrich(quoted.Reply)
	}
	for i := range messages {
		enrich(&messages[i])
	}
}

func (m quoteMessage) quoted() bot.QuotedMessage {
	userID := int64(m.UserID)
	if userID == 0 {
		userID = int64(m.Sender.UserID)
	}
	return bot.QuotedMessage{
		MessageID: int64(m.MessageID), MessageSeq: int64(m.MessageSeq), UserID: userID,
		Nickname: senderNickname(m.Sender), RawMessage: m.RawMessage, Message: m.Message.Chain,
	}
}

func (g *Gateway) ResolveImage(ctx context.Context, file string) (string, error) {
	client, err := g.client()
	if err != nil {
		return "", err
	}
	data, err := client.GetImage(ctx, api.GetImageRequest{File: &file})
	if err != nil {
		return "", safeOperationError("resolve_image", err)
	}
	if data.URL != nil && *data.URL != "" {
		return *data.URL, nil
	}
	if data.File != nil {
		return *data.File, nil
	}
	return "", nil
}

func (g *Gateway) SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	_, err = client.SetGroupBan(ctx, api.SetGroupBanRequest{
		GroupID:  strconv.FormatInt(groupID, 10),
		UserID:   strconv.FormatInt(userID, 10),
		Duration: api.SetGroupBanRequestDurationUnion{Raw: []byte(strconv.FormatInt(int64(duration.Seconds()), 10))},
	})
	return safeOperationError("set_group_ban", err)
}

func (g *Gateway) SetRestart(ctx context.Context) error {
	client, err := g.client()
	if err != nil {
		return err
	}
	_, err = client.SetRestart(ctx, api.SetRestartRequest{})
	return safeOperationError("restart", err)
}

func (g *Gateway) FetchGroupJoinRequests(ctx context.Context, count int) ([]grouprequest.Record, error) {
	client, err := g.client()
	if err != nil {
		return nil, err
	}
	var resp struct {
		InvitedRequests []json.RawMessage `json:"invited_requests"`
		InvitedRequest  []json.RawMessage `json:"InvitedRequest"`
		JoinRequests    []json.RawMessage `json:"join_requests"`
	}
	err = client.Call(ctx, string(api.ActionGetGroupSystemMsg), api.GetGroupSystemMsgRequest{
		Count: api.GetGroupSystemMsgRequestCountUnion{Raw: []byte(strconv.Itoa(count))},
	}, &resp)
	if err != nil {
		return nil, safeOperationError("fetch_group_join_requests", err)
	}
	joinRequests, err := decodeGroupSystemMessages(resp.JoinRequests, false)
	var decodeErrors []error
	if err != nil {
		decodeErrors = append(decodeErrors, fmt.Errorf("decode join requests: %w", err))
	}
	invitedRaw := resp.InvitedRequests
	if len(invitedRaw) == 0 {
		invitedRaw = resp.InvitedRequest
	}
	invitedRequests, err := decodeGroupSystemMessages(invitedRaw, true)
	if err != nil {
		decodeErrors = append(decodeErrors, fmt.Errorf("decode invited requests: %w", err))
	}
	records := grouprequest.RecordsFromSystemMessages(joinRequests, invitedRequests)
	if err := errors.Join(decodeErrors...); err != nil {
		return records, operationFailure("fetch_group_join_requests", FailureInvalidResponse)
	}
	return records, nil
}

type groupSystemMessageWire struct {
	RequestID    json.RawMessage `json:"request_id"`
	RequesterUin json.RawMessage `json:"requester_uin"`
	RequesterID  json.RawMessage `json:"requester_id"`
	UserID       json.RawMessage `json:"user_id"`
	Uin          json.RawMessage `json:"uin"`
	InvitorUin   json.RawMessage `json:"invitor_uin"`
	GroupID      json.RawMessage `json:"group_id"`
	Message      string          `json:"message"`
	Checked      bool            `json:"checked"`
}

func decodeGroupSystemMessages(rawMessages []json.RawMessage, invited bool) ([]grouprequest.SystemMessage, error) {
	messages := make([]grouprequest.SystemMessage, 0, len(rawMessages))
	var decodeErrors []error
	for i, raw := range rawMessages {
		message, err := decodeGroupSystemMessage(raw, invited)
		if err != nil {
			decodeErrors = append(decodeErrors, fmt.Errorf("item %d: %w", i, err))
			continue
		}
		messages = append(messages, message)
	}
	return messages, errors.Join(decodeErrors...)
}

func decodeGroupSystemMessage(raw json.RawMessage, invited bool) (grouprequest.SystemMessage, error) {
	var wire groupSystemMessageWire
	if err := json.Unmarshal(raw, &wire); err != nil {
		return grouprequest.SystemMessage{}, fmt.Errorf("decode group system message: %w", err)
	}
	requestID, err := decimalJSONValue(wire.RequestID, "request_id", true)
	if err != nil {
		return grouprequest.SystemMessage{}, err
	}
	if strings.TrimLeft(requestID, "0") == "" {
		return grouprequest.SystemMessage{}, fmt.Errorf("group system message request_id must be positive")
	}
	groupID, err := firstInt64JSONValue("group_id", wire.GroupID)
	if err != nil {
		return grouprequest.SystemMessage{}, err
	}
	var userID int64
	if invited {
		userID, err = firstInt64JSONValue("invitor_uin", wire.InvitorUin)
	} else {
		// Current NapCat versions expose the join applicant as invitor_uin.
		// Prefer explicit requester fields if a future response provides them.
		userID, err = firstInt64JSONValue("requester", wire.RequesterUin, wire.RequesterID, wire.UserID, wire.Uin, wire.InvitorUin)
	}
	if err != nil {
		return grouprequest.SystemMessage{}, err
	}
	return grouprequest.SystemMessage{
		RequestID: requestID,
		GroupID:   groupID,
		UserID:    userID,
		Message:   wire.Message,
		Checked:   wire.Checked,
		RawJSON:   string(raw),
	}, nil
}

func firstInt64JSONValue(field string, values ...json.RawMessage) (int64, error) {
	// 调用方按优先级传入多个候选字段。某个候选格式不对不代表整条记录无效，
	// 继续尝试后面的；全部失败时返回第一个错误，保留具体原因。
	var firstErr error
	for _, raw := range values {
		value, err := decimalJSONValue(raw, field, false)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if value == "" || value == "0" {
			continue
		}
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("decode %s %q: %w", field, value, err)
			}
			continue
		}
		if parsed == 0 {
			continue
		}
		return parsed, nil
	}
	if firstErr != nil {
		return 0, firstErr
	}
	return 0, fmt.Errorf("group system message %s is missing or zero", field)
}

func decimalJSONValue(raw json.RawMessage, field string, required bool) (string, error) {
	value := strings.TrimSpace(string(raw))
	if value == "" || value == "null" {
		if required {
			return "", fmt.Errorf("group system message %s is missing", field)
		}
		return "", nil
	}
	if value[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("decode group system message %s: %w", field, err)
		}
		value = strings.TrimSpace(value)
	}
	if value == "" {
		if required {
			return "", fmt.Errorf("group system message %s is empty", field)
		}
		return "", nil
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return "", fmt.Errorf("group system message %s %q is not a decimal integer", field, value)
		}
	}
	return value, nil
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
