package bot

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
)

type GroupCommandRouter struct {
	ai            *ai.Service
	reloader      Reloader
	admin         *commands.AdminHandler
	quote         QuoteGenerator
	groupRequests *grouprequest.Service
	triggerStats  *triggerstats.Service
}

const maxQuoteMessages = 10

func NewGroupCommandRouter(opts Options) *GroupCommandRouter {
	return &GroupCommandRouter{
		ai:            opts.AI,
		reloader:      opts.Reloader,
		admin:         opts.Admin,
		quote:         opts.Quote,
		groupRequests: opts.GroupRequests,
		triggerStats:  opts.TriggerStats,
	}
}

type GroupRequestFetcher interface {
	FetchGroupJoinRequests(ctx context.Context, count int) ([]grouprequest.Record, error)
}

type GroupFileUploader interface {
	UploadGroupFile(ctx context.Context, groupID int64, path, name string) error
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
	answer, docs, err := r.ai.AnswerWithDocuments(ctx, question)
	if err != nil {
		return err
	}
	if r.triggerStats != nil {
		for _, doc := range docs {
			if err := r.triggerStats.RecordAIRetrieval(ctx, triggerstats.AIRetrievalInput{
				SourceKey: doc.ID,
				Keyword:   doc.Metadata["keyword"],
				GroupID:   msg.GroupID,
				UserID:    msg.UserID,
				MessageID: msg.MessageID,
				Question:  question,
				Score:     doc.Score,
			}); err != nil {
				// 统计失败不影响 /ai 的正常回答，避免新增表异常扩大成问答故障。
				log.Printf("record ai retrieval trigger failed: %v", err)
			}
		}
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
	if strings.HasPrefix(adminText, "群申请 ") {
		return r.handleGroupRequestAdmin(ctx, msg, sender, strings.TrimSpace(strings.TrimPrefix(adminText, "群申请 ")))
	}
	if strings.HasPrefix(adminText, "词条统计") {
		return r.handleTriggerStats(ctx, msg, sender, strings.TrimSpace(strings.TrimPrefix(adminText, "词条统计")))
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

func (r *GroupCommandRouter) handleGroupRequestAdmin(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	if r.groupRequests == nil {
		return sender.SendGroupText(ctx, msg.GroupID, "群申请登记未初始化")
	}
	switch {
	case strings.HasPrefix(text, "导出"):
		limit, err := parseOptionalLimit(strings.TrimSpace(strings.TrimPrefix(text, "导出")))
		if err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "格式：/admin 群申请 导出 [全部|最近N]")
		}
		result, err := r.groupRequests.Export(ctx, limit)
		if err != nil {
			return err
		}
		uploader, ok := sender.(GroupFileUploader)
		if !ok {
			return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已导出群申请 %d 条，但群文件上传接口未初始化。文件保存在：%s", result.Count, result.Path))
		}
		if err := uploader.UploadGroupFile(ctx, msg.GroupID, result.Path, filepath.Base(result.Path)); err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已导出群申请 %d 条，但上传群文件失败：%v。文件保存在：%s", result.Count, err, result.Path))
		}
		return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已导出群申请 %d 条，Excel 已发送到群文件", result.Count))
	case strings.HasPrefix(text, "同步"):
		fetcher, ok := sender.(GroupRequestFetcher)
		if !ok {
			return sender.SendGroupText(ctx, msg.GroupID, "NapCat 群申请接口未初始化")
		}
		limit, err := parseOptionalLimit(strings.TrimSpace(strings.TrimPrefix(text, "同步")))
		if err != nil {
			return sender.SendGroupText(ctx, msg.GroupID, "格式：/admin 群申请 同步 [数量]")
		}
		if limit <= 0 {
			limit = 20
		}
		records, err := fetcher.FetchGroupJoinRequests(ctx, limit)
		if err != nil {
			return err
		}
		for _, record := range records {
			if err := r.groupRequests.Record(ctx, record); err != nil {
				return err
			}
		}
		return sender.SendGroupText(ctx, msg.GroupID, fmt.Sprintf("已同步群申请 %d 条", len(records)))
	default:
		return sender.SendGroupText(ctx, msg.GroupID, "格式：/admin 群申请 <同步|导出>")
	}
}

func (r *GroupCommandRouter) handleTriggerStats(ctx context.Context, msg GroupMessage, sender Sender, text string) error {
	if r.triggerStats == nil {
		return sender.SendGroupText(ctx, msg.GroupID, "词条统计未初始化")
	}
	since, err := parseStatsSince(text, time.Now())
	if err != nil {
		return sender.SendGroupText(ctx, msg.GroupID, "格式：/admin 词条统计 [7d|30d|全部]")
	}
	summaries, err := r.triggerStats.Summaries(ctx, since, 10)
	if err != nil {
		return err
	}
	return sender.SendGroupText(ctx, msg.GroupID, triggerstats.FormatSummaries(summaries))
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

func parseOptionalLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "全部" {
		return 0, nil
	}
	raw = strings.TrimPrefix(raw, "最近")
	limit, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || limit < 0 {
		return 0, fmt.Errorf("invalid limit")
	}
	return limit, nil
}

func parseStatsSince(raw string, now time.Time) (*time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "7d"
	}
	if raw == "全部" {
		return nil, nil
	}
	if !strings.HasSuffix(raw, "d") {
		return nil, fmt.Errorf("invalid range")
	}
	days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
	if err != nil || days <= 0 {
		return nil, fmt.Errorf("invalid range")
	}
	since := now.Add(-time.Duration(days) * 24 * time.Hour)
	return &since, nil
}
