package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/customcommand"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/settings"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
	"github.com/zjutjh/napcat-sdk/message"
)

type Sender interface {
	SendGroupText(ctx context.Context, groupID int64, text string) error
	SendGroupMessage(ctx context.Context, groupID int64, message message.Chain) error
	SendGroupFlashFile(ctx context.Context, groupID int64, source, name string) error
	GetQuoteMessages(ctx context.Context, groupID, messageID int64, count int) ([]QuotedMessage, error)
	ResolveImage(ctx context.Context, file string) (string, error)
	SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error
	SetRestart(ctx context.Context) error
	GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error)
}

const trackedLinkReplyPrefix = "精小弘觉得这个链接十分甚至九分不对劲，帮你移除了里面的TrackID："

type QuotedMessage struct {
	MessageID  int64
	UserID     int64
	Nickname   string
	RawMessage string
	Message    message.Chain
}

type Options struct {
	Sender               Sender
	Knowledge            *knowledge.IndexRef
	AI                   *ai.Service
	Reloader             *knowledge.Syncer
	Admin                *commands.AdminHandler
	Quote                *quote.Client
	GroupRequests        *grouprequest.Service
	TriggerStats         *triggerstats.Service
	LinkCleaner          *linkcleaner.Service
	Settings             *settings.Runtime
	CustomCommands       CustomCommandExecutor
	MaintenanceAllowlist MaintenanceAllowlist
}

type CustomCommandExecutor interface {
	Probe(message, groupID string) (customcommand.TriggerPermission, bool)
	Execute(context.Context, customcommand.ExecuteInput) (customcommand.Run, bool, error)
}

// MaintenanceAllowlist is intentionally a small persistence boundary. The
// database-backed implementation can be added without coupling the hot path to
// an administrator account repository.
type MaintenanceAllowlist interface {
	Contains(ctx context.Context, groupID, userID int64) (bool, error)
}

type Pipeline struct {
	mu                   sync.RWMutex
	knowledge            *knowledge.IndexRef
	sender               Sender
	groupRequests        *grouprequest.Service
	stats                *triggerstats.Service
	linkCleaner          *linkcleaner.Service
	settings             *settings.Runtime
	customCommands       CustomCommandExecutor
	maintenanceAllowlist MaintenanceAllowlist
	commandRouter        *GroupCommandRouter
}

func (p *Pipeline) SetSender(sender Sender) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sender = sender
}

type GroupMessage struct {
	GroupID        int64
	UserID         int64
	SelfID         int64
	Text           string
	RawMessage     string
	MessageID      int64
	ReplyMessageID int64
	IsSelf         bool
	AtUsers        []int64
	Segments       message.Chain
}

func NewPipeline(opts Options) *Pipeline {
	pipeline := &Pipeline{
		knowledge:            opts.Knowledge,
		groupRequests:        opts.GroupRequests,
		stats:                opts.TriggerStats,
		linkCleaner:          opts.LinkCleaner,
		settings:             opts.Settings,
		customCommands:       opts.CustomCommands,
		maintenanceAllowlist: opts.MaintenanceAllowlist,
		commandRouter:        NewGroupCommandRouter(opts),
	}
	pipeline.SetSender(opts.Sender)
	return pipeline
}

func (p *Pipeline) HandleGroupMessage(ctx context.Context, msg GroupMessage) error {
	sender := p.currentSender()
	if sender == nil || msg.IsSelf {
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	handled, err := p.commandRouter.Handle(ctx, msg, sender)
	if handled || err != nil {
		return err
	}
	if p.customCommands != nil && p.featureEnabled(msg.GroupID, settings.FeatureCustomCommand) {
		handled, err := p.handleCustomCommand(ctx, msg, text, sender)
		if handled || err != nil {
			return err
		}
	}
	if p.linkCleaner != nil && p.featureEnabled(msg.GroupID, settings.FeatureLinkCleaner) {
		cleaned, err := p.linkCleaner.CleanMessage(ctx, msg.Text, msg.Segments)
		if err != nil {
			log.Printf("clean tracked links failed: %v", err)
		}
		if len(cleaned) > 0 {
			return sender.SendGroupText(ctx, msg.GroupID, trackedLinkReplyPrefix+"\n"+strings.Join(cleaned, "\n"))
		}
	}
	if text == "" {
		return nil
	}
	if p.knowledge != nil && p.featureEnabled(msg.GroupID, settings.FeatureKeywordReply) {
		if entry, ok := p.knowledge.Lookup(text); ok {
			if err := sendKeywordReply(ctx, sender, msg.GroupID, entry.SourceKey, entry.Answer); err != nil {
				return err
			}
			if p.stats != nil {
				if err := p.stats.RecordKeywordReply(ctx, entry.SourceKey, msg.GroupID); err != nil {
					// 统计是附加能力，失败时不能阻断原本的关键词回复。
					log.Printf("record keyword reply trigger failed: %v", err)
				}
			}
			return nil
		}
	}
	return nil
}

func (p *Pipeline) handleCustomCommand(ctx context.Context, msg GroupMessage, text string, sender Sender) (bool, error) {
	groupID := strconv.FormatInt(msg.GroupID, 10)
	permission, matched := p.customCommands.Probe(text, groupID)
	if !matched {
		return false, nil
	}
	input := customcommand.ExecuteInput{
		RunIdentity: customCommandRunIdentity(msg), GroupID: groupID,
		SenderQQ: strconv.FormatInt(msg.UserID, 10), SenderRole: customcommand.SenderMember,
		Message: text,
	}
	switch permission {
	case customcommand.TriggerGroupAdmin:
		role, err := sender.GetGroupMemberRole(ctx, msg.GroupID, msg.UserID)
		if err != nil {
			log.Printf("query custom command actor role failed: group=%d user=%d", msg.GroupID, msg.UserID)
			return true, err
		}
		mapped, ok := customCommandSenderRole(role)
		if !ok {
			log.Printf("query custom command actor role returned invalid role: group=%d user=%d", msg.GroupID, msg.UserID)
			return true, fmt.Errorf("invalid group member role")
		}
		input.SenderRole = mapped
	case customcommand.TriggerMaintenanceAllowlist:
		if p.maintenanceAllowlist != nil {
			allowed, err := p.maintenanceAllowlist.Contains(ctx, msg.GroupID, msg.UserID)
			if err != nil {
				log.Printf("query custom command maintenance allowlist failed: group=%d user=%d", msg.GroupID, msg.UserID)
				return true, err
			}
			input.MaintenanceAllowlisted = allowed
		}
	}
	_, handled, err := p.customCommands.Execute(ctx, input)
	return handled, err
}

func customCommandSenderRole(value string) (customcommand.SenderRole, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "owner":
		return customcommand.SenderOwner, true
	case "admin":
		return customcommand.SenderAdmin, true
	case "member":
		return customcommand.SenderMember, true
	default:
		return "", false
	}
}

func customCommandRunIdentity(msg GroupMessage) string {
	if msg.MessageID <= 0 {
		return ""
	}
	return fmt.Sprintf("qqmsg:%d:%d", msg.GroupID, msg.MessageID)
}

func (p *Pipeline) HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error {
	sender := p.currentSender()
	if sender == nil {
		return nil
	}
	features := settings.DefaultFeatures()
	if p.settings != nil {
		features = p.settings.EffectiveForGroup(groupID)
	}
	if !features.Welcome.Enabled {
		return nil
	}
	return sender.SendGroupMessage(ctx, groupID, message.ChainOf(
		message.At(userID),
		message.Text(settings.RenderWelcome(features.Welcome.MessageTemplate, groupID, userID)),
	))
}

func (p *Pipeline) HandleGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	if p.groupRequests == nil {
		return fmt.Errorf("group request service is not initialized")
	}
	return p.groupRequests.Record(ctx, record)
}

func (p *Pipeline) ReconcileGroupJoinRequests(ctx context.Context, records []grouprequest.Record) error {
	if p.groupRequests == nil {
		return fmt.Errorf("group request service is not initialized")
	}
	return p.groupRequests.Reconcile(ctx, records)
}

func (p *Pipeline) SendGroupText(ctx context.Context, groupID int64, text string) error {
	sender := p.currentSender()
	if sender == nil {
		return fmt.Errorf("napcat sender is not connected")
	}
	return sender.SendGroupText(ctx, groupID, text)
}

func (p *Pipeline) currentSender() Sender {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sender
}

func (p *Pipeline) featureEnabled(groupID int64, key settings.FeatureKey) bool {
	return p.settings == nil || p.settings.Enabled(groupID, key)
}
