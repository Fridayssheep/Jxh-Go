package bot

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/grouprequest"
	"github.com/zjutjh/jxh-go/internal/quote"
	"github.com/zjutjh/jxh-go/internal/triggerstats"
)

type Sender interface {
	SendGroupText(ctx context.Context, groupID int64, text string) error
	SendGroupMessage(ctx context.Context, groupID int64, message any) error
}

type Reloader interface {
	Reload(ctx context.Context) error
}

type Blacklist interface {
	IsBlacklisted(ctx context.Context, userID int64) (bool, error)
}

type QuoteGenerator interface {
	Generate(ctx context.Context, payload quote.Payload) (string, error)
}

type QuoteMessageGetter interface {
	GetQuoteMessages(ctx context.Context, groupID, messageID int64, count int) ([]QuotedMessage, error)
}

type QuotedMessage struct {
	MessageID  int64
	UserID     int64
	Nickname   string
	RawMessage string
	Message    any
}

type Moderator interface {
	SetGroupBan(ctx context.Context, groupID, userID int64, duration time.Duration) error
	SetRestart(ctx context.Context) error
}

type Options struct {
	Knowledge     *cache.Knowledge
	Sender        Sender
	AI            *ai.Service
	Reloader      Reloader
	Blacklist     Blacklist
	Admin         *commands.AdminHandler
	Quote         QuoteGenerator
	GroupRequests *grouprequest.Service
	TriggerStats  *triggerstats.Service
}

type Pipeline struct {
	mu            sync.RWMutex
	knowledge     *cache.Knowledge
	sender        Sender
	blacklist     Blacklist
	groupRequests *grouprequest.Service
	stats         *triggerstats.Service
	commandRouter *GroupCommandRouter
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
	IsOwner        bool
	AtUsers        []int64
}

func NewPipeline(opts Options) *Pipeline {
	return &Pipeline{
		knowledge:     opts.Knowledge,
		sender:        opts.Sender,
		blacklist:     opts.Blacklist,
		groupRequests: opts.GroupRequests,
		stats:         opts.TriggerStats,
		commandRouter: NewGroupCommandRouter(opts),
	}
}

func (p *Pipeline) HandleGroupMessage(ctx context.Context, msg GroupMessage) error {
	sender := p.currentSender()
	if sender == nil || msg.IsSelf {
		return nil
	}
	if p.blacklist != nil {
		blocked, err := p.blacklist.IsBlacklisted(ctx, msg.UserID)
		if err != nil {
			return err
		}
		if blocked {
			return nil
		}
	}
	text := strings.TrimSpace(msg.Text)
	if p.commandRouter != nil {
		handled, err := p.commandRouter.Handle(ctx, msg, sender)
		if handled || err != nil {
			return err
		}
	}
	if text == "" {
		return nil
	}
	if p.knowledge != nil {
		if entry, ok := p.knowledge.Lookup(text); ok {
			if p.stats != nil {
				if err := p.stats.RecordKeywordReply(ctx, triggerstats.KeywordReplyInput{
					SourceKey: entry.SourceKey,
					Keyword:   entry.Keyword,
					GroupID:   msg.GroupID,
					UserID:    msg.UserID,
					MessageID: msg.MessageID,
					Text:      text,
				}); err != nil {
					// 统计是附加能力，失败时不能阻断原本的关键词回复。
					log.Printf("record keyword reply trigger failed: %v", err)
				}
			}
			return sender.SendGroupText(ctx, msg.GroupID, entry.Answer)
		}
	}
	return nil
}

func (p *Pipeline) HandleGroupIncrease(ctx context.Context, groupID int64, userID int64) error {
	sender := p.currentSender()
	if sender == nil {
		return nil
	}
	message := []any{
		map[string]any{"type": "at", "data": map[string]any{"qq": userID}},
		map[string]any{"type": "text", "data": map[string]any{"text": "欢迎来到浙江工业大学，精弘网络欢迎各位的到来！\n输入 菜单 获取精小弘机器人的菜单 哦！\n请及时修改群名片\n格式如下：专业/大类+姓名"}},
	}
	return sender.SendGroupMessage(ctx, groupID, message)
}

func (p *Pipeline) HandleGroupJoinRequest(ctx context.Context, record grouprequest.Record) error {
	if p.groupRequests == nil {
		return nil
	}
	return p.groupRequests.Record(ctx, record)
}

func (p *Pipeline) SendGroupText(ctx context.Context, groupID int64, text string) error {
	sender := p.currentSender()
	if sender == nil {
		return nil
	}
	return sender.SendGroupText(ctx, groupID, text)
}

func (p *Pipeline) currentSender() Sender {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.sender
}
