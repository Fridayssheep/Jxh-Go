package bot

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/quote"
)

type GroupCommandRouter struct {
	ai       *ai.Service
	reloader Reloader
	admin    *commands.AdminHandler
	quote    QuoteGenerator
}

const maxQuoteMessages = 10

func NewGroupCommandRouter(opts Options) *GroupCommandRouter {
	return &GroupCommandRouter{
		ai:       opts.AI,
		reloader: opts.Reloader,
		admin:    opts.Admin,
		quote:    opts.Quote,
	}
}

func (r *GroupCommandRouter) Handle(ctx context.Context, msg GroupMessage, sender Sender) (bool, error) {
	if r == nil {
		return false, nil
	}
	text := strings.TrimSpace(msg.Text)
	if isSlashCommand(text) && !mentionsSelf(msg) {
		return true, nil
	}
	switch {
	case text == "/test":
		return true, sender.SendGroupText(ctx, msg.GroupID, "精小弘正常")
	case text == "/reload":
		return true, r.handleReload(ctx, msg, sender)
	case text == "/q" || strings.HasPrefix(text, "/q "):
		return true, r.handleQuote(ctx, msg, sender, text)
	case strings.HasPrefix(text, "/ai"):
		return true, r.handleAI(ctx, msg, sender, text)
	case strings.HasPrefix(text, "/admin"):
		return true, r.handleAdmin(ctx, msg, sender, text)
	default:
		return false, nil
	}
}

func isSlashCommand(text string) bool {
	return strings.HasPrefix(text, "/")
}

func mentionsSelf(msg GroupMessage) bool {
	if msg.SelfID == 0 {
		return false
	}
	for _, user := range msg.AtUsers {
		if user == msg.SelfID {
			return true
		}
	}
	return false
}

func (r *GroupCommandRouter) handleReload(ctx context.Context, msg GroupMessage, sender Sender) error {
	if r.reloader != nil {
		if err := r.reloader.Reload(ctx); err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "重载失败："+err.Error())
		}
	}
	return sender.SendGroupText(ctx, msg.GroupID, "重载成功")
}

func (r *GroupCommandRouter) handleQuote(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	if r.quote == nil {
		return sender.SendGroupText(ctx, msg.GroupID, "引用图服务未初始化")
	}
	count, err := parseQuoteCount(text)
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "用法：回复一条消息后发送 /q [1-10]")
	}
	if msg.ReplyMessageID == 0 {
		return sender.SendGroupText(ctx, msg.GroupID, "请回复一条消息后使用 /q")
	}
	getter, ok := sender.(QuoteMessageGetter)
	if !ok {
		return sender.SendGroupText(ctx, msg.GroupID, "NapCat 消息接口未初始化")
	}
	quoted, err := getter.GetQuoteMessages(ctx, msg.GroupID, msg.ReplyMessageID, count)
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "获取被引用消息失败："+err.Error())
	}
	resolver, _ := sender.(quote.ImageResolver)
	inputs := make([]quote.MessageInput, 0, len(quoted))
	for _, message := range quoted {
		if message.MessageID == msg.MessageID {
			continue
		}
		inputs = append(inputs, quote.MessageInput{
			UserID: message.UserID, Nickname: message.Nickname,
			RawMessage: message.RawMessage, Message: message.Message,
		})
	}
	payload := quote.BuildPayload(ctx, inputs, resolver)
	if len(payload) == 0 {
		return sender.SendGroupText(ctx, msg.GroupID, "被引用消息内容为空")
	}
	image, err := r.quote.Generate(ctx, payload)
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "引用图生成失败："+err.Error())
	}
	return sender.SendGroupMessage(ctx, msg.GroupID, map[string]any{"type": "image", "data": map[string]any{"file": "base64://" + image}})
}

func parseQuoteCount(text string) (int, error) {
	args := strings.Fields(strings.TrimSpace(strings.TrimPrefix(text, "/q")))
	if len(args) == 0 {
		return 1, nil
	}
	if len(args) != 1 {
		return 0, fmt.Errorf("invalid quote arguments")
	}
	count, err := strconv.Atoi(args[0])
	if err != nil || count < 1 || count > maxQuoteMessages {
		return 0, fmt.Errorf("quote count must be between 1 and %d", maxQuoteMessages)
	}
	return count, nil
}

func (r *GroupCommandRouter) handleAI(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	question := strings.TrimSpace(strings.TrimPrefix(text, "/ai"))
	if r.ai == nil {
		return sender.SendGroupText(ctx, msg.GroupID, ai.DisabledAnswer)
	}
	answer, err := r.ai.Answer(ctx, question)
	if err != nil {
		return err
	}
	return sender.SendGroupText(ctx, msg.GroupID, answer)
}

func (r *GroupCommandRouter) handleAdmin(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	if r.admin == nil {
		return sender.SendGroupText(ctx, msg.GroupID, "管理命令未初始化")
	}
	adminText := strings.TrimSpace(strings.TrimPrefix(text, "/admin"))
	adminInput := commands.AdminInput{
		ActorID: msg.UserID,
		Text:    adminText,
		AtUsers: targetAtUsers(msg),
		IsOwner: msg.IsOwner,
	}
	if resp, err := r.admin.PermissionMessage(ctx, adminInput); resp != "" || err != nil {
		if err != nil {
			return err
		}
		return sender.SendGroupText(ctx, msg.GroupID, resp)
	}
	if adminText == "restart" {
		moderator, ok := sender.(Moderator)
		if !ok {
			return sender.SendGroupText(ctx, msg.GroupID, "NapCat 管理接口未初始化")
		}
		if err := moderator.SetRestart(ctx); err != nil {
			return err
		}
		return sender.SendGroupText(ctx, msg.GroupID, "已请求重启 NapCat")
	}
	if strings.HasPrefix(adminText, "ban ") {
		moderator, ok := sender.(Moderator)
		if !ok {
			return sender.SendGroupText(ctx, msg.GroupID, "NapCat 管理接口未初始化")
		}
		atUsers := targetAtUsers(msg)
		if len(atUsers) == 0 {
			return sender.SendGroupText(ctx, msg.GroupID, "请 @ 要禁言的用户")
		}
		duration, err := parseBanDuration(strings.TrimSpace(strings.TrimPrefix(adminText, "ban ")))
		if err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "禁言时间格式不正确")
		}
		if err := moderator.SetGroupBan(ctx, msg.GroupID, atUsers[0], duration); err != nil {
			return err
		}
		return sender.SendGroupText(ctx, msg.GroupID, "已禁言")
	}
	resp, err := r.admin.ExecuteAuthorized(ctx, adminInput)
	if err != nil {
		return err
	}
	return sender.SendGroupText(ctx, msg.GroupID, resp)
}

func targetAtUsers(msg GroupMessage) []int64 {
	if msg.SelfID == 0 {
		return msg.AtUsers
	}
	out := make([]int64, 0, len(msg.AtUsers))
	for _, user := range msg.AtUsers {
		if user != msg.SelfID {
			out = append(out, user)
		}
	}
	return out
}

func parseBanDuration(raw string) (time.Duration, error) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(fields[0]); err == nil {
		return d, nil
	}
	seconds, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, err
	}
	return time.Duration(seconds) * time.Second, nil
}
