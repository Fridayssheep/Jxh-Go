package bot

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/bot/commands"
	"github.com/zjutjh/jxh-go/internal/groups/grouprequest"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/knowledge/triggerstats"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	"github.com/zjutjh/jxh-go/internal/messaging/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/messaging/quote"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
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
	AI                   AIAnswerer
	Reloader             KnowledgeReloader
	Admin                *commands.AdminHandler
	Quote                *quote.Client
	GroupRequests        *grouprequest.Service
	TriggerStats         *triggerstats.Service
	LinkCleaner          *linkcleaner.Service
	Settings             *settings.Runtime
	CustomCommands       CustomCommandExecutor
	MaintenanceAllowlist MaintenanceAllowlist
	Telemetry            TelemetryRecorder
}

type AIAnswerer interface {
	AnswerWithSources(context.Context, string) (string, []string, error)
}

type KnowledgeReloader interface {
	Sync(context.Context) error
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

type TelemetryRecorder interface {
	Record(telemetry.Observation) bool
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
	telemetry            TelemetryRecorder
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
		telemetry:            opts.Telemetry,
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
	p.recordTelemetry(telemetry.Observation{
		Kind: telemetry.EventGroupMessage, GroupID: msg.GroupID, UserID: msg.UserID, Result: telemetry.ResultSuccess,
	})
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
		startedAt := time.Now()
		cleaned, err := p.linkCleaner.CleanMessage(ctx, msg.Text, msg.Segments)
		if err != nil {
			log.Printf("clean tracked links failed: %v", err)
		}
		if err != nil && len(cleaned) == 0 {
			p.recordTelemetry(telemetry.Observation{
				Kind: telemetry.EventLinkClean, GroupID: msg.GroupID, UserID: msg.UserID,
				FeatureKey: string(settings.FeatureLinkCleaner), Result: telemetry.ResultFailed, Duration: time.Since(startedAt),
			})
		}
		if len(cleaned) > 0 {
			sendErr := sender.SendGroupText(ctx, msg.GroupID, trackedLinkReplyPrefix+"\n"+strings.Join(cleaned, "\n"))
			result := telemetry.ResultSuccess
			if err != nil {
				result = telemetry.ResultPartial
			}
			if sendErr != nil {
				result = telemetry.ResultFailed
			}
			p.recordTelemetry(telemetry.Observation{
				Kind: telemetry.EventLinkClean, GroupID: msg.GroupID, UserID: msg.UserID,
				FeatureKey: string(settings.FeatureLinkCleaner), Result: result, Duration: time.Since(startedAt),
			})
			return sendErr
		}
	}
	if text == "" {
		return nil
	}
	if p.knowledge != nil && p.featureEnabled(msg.GroupID, settings.FeatureKeywordReply) {
		if entry, ok := p.knowledge.Lookup(text); ok {
			startedAt := time.Now()
			if err := sendKeywordReply(ctx, sender, msg.GroupID, entry.SourceKey, entry.Answer); err != nil {
				p.recordTelemetry(telemetry.Observation{
					Kind: telemetry.EventKeywordReply, GroupID: msg.GroupID, UserID: msg.UserID,
					FeatureKey: string(settings.FeatureKeywordReply), Result: telemetry.ResultFailed,
					Duration: time.Since(startedAt), KnowledgeKey: entry.SourceKey,
				})
				return err
			}
			p.recordTelemetry(telemetry.Observation{
				Kind: telemetry.EventKeywordReply, GroupID: msg.GroupID, UserID: msg.UserID,
				FeatureKey: string(settings.FeatureKeywordReply), Result: telemetry.ResultSuccess,
				Duration: time.Since(startedAt), KnowledgeKey: entry.SourceKey,
			})
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
	run, handled, err := p.customCommands.Execute(ctx, input)
	if handled {
		result := customCommandTelemetryResult(run.Result)
		if err != nil {
			result = telemetry.ResultFailed
		}
		p.recordTelemetry(telemetry.Observation{
			Kind: telemetry.EventCommandRun, GroupID: msg.GroupID, UserID: msg.UserID,
			FeatureKey: string(settings.FeatureCustomCommand), Result: result,
			Duration: run.Duration, CommandID: run.CommandID,
		})
	}
	return handled, err
}

func customCommandTelemetryResult(result customcommand.RunResult) telemetry.Result {
	switch result {
	case customcommand.RunSuccess:
		return telemetry.ResultSuccess
	case customcommand.RunDenied:
		return telemetry.ResultDenied
	case customcommand.RunParseError:
		return telemetry.ResultParseFailed
	case customcommand.RunPartial:
		return telemetry.ResultPartial
	case customcommand.RunUnknown:
		return telemetry.ResultUnknown
	default:
		return telemetry.ResultFailed
	}
}

func (p *Pipeline) recordTelemetry(observation telemetry.Observation) {
	if p.telemetry != nil {
		p.telemetry.Record(observation)
	}
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
	err := p.groupRequests.Record(ctx, record)
	result := telemetry.ResultSuccess
	if err != nil {
		result = telemetry.ResultFailed
	}
	p.recordTelemetry(telemetry.Observation{
		Kind: telemetry.EventJoinRequest, GroupID: record.GroupID, UserID: record.UserID, Result: result,
	})
	return err
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
