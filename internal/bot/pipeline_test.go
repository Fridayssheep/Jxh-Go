package bot_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/zjutjh/jxh-go/internal/ai"
	"github.com/zjutjh/jxh-go/internal/bot"
	"github.com/zjutjh/jxh-go/internal/cache"
	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/quote"
)

func TestPipelineFallsBackToKeywordReply(t *testing.T) {
	sender := &fakeSender{}
	kc := cache.NewKnowledge()
	kc.Replace(knowledge.NewKeywordIndex([]knowledge.Entry{{Keyword: "精小弘", Answer: "我在！", Enabled: true, ExactReply: true}}))
	p := bot.NewPipeline(bot.Options{Knowledge: kc, Sender: sender})
	if err := p.HandleGroupMessage(context.Background(), bot.GroupMessage{GroupID: 1, UserID: 2, Text: "精小弘"}); err != nil {
		t.Fatal(err)
	}
	if sender.lastText != "我在！" {
		t.Fatalf("lastText = %q", sender.lastText)
	}
}

func TestPipelineAdminCommandUsesHandler(t *testing.T) {
	sender := &fakeSender{}
	admins := commands.NewMemoryAdminStore()
	p := bot.NewPipeline(bot.Options{
		Sender: sender,
		Admin:  commands.NewAdminHandler(admins),
	})
	if err := p.HandleGroupMessage(context.Background(), bot.GroupMessage{
		GroupID: 1,
		UserID:  1,
		Text:    "/admin 添加管理员",
		AtUsers: []int64{2},
		IsOwner: true,
	}); err != nil {
		t.Fatal(err)
	}
	if sender.lastText != "已添加管理员" {
		t.Fatalf("lastText = %q", sender.lastText)
	}
}

func TestPipelineQCommandGeneratesQuoteImage(t *testing.T) {
	sender := &fakeSender{
		quoted: bot.QuotedMessage{
			UserID:     12345,
			Nickname:   "张三",
			RawMessage: "被引用的消息",
		},
	}
	quoteGen := &capturingQuote{result: "base64-image"}
	p := bot.NewPipeline(bot.Options{
		Sender: sender,
		Quote:  quoteGen,
	})
	if err := p.HandleGroupMessage(context.Background(), bot.GroupMessage{
		GroupID:        1,
		UserID:         2,
		Text:           "/q",
		ReplyMessageID: 99,
		RawMessage:     "/q",
	}); err != nil {
		t.Fatal(err)
	}
	if sender.requestedQuoteMessageID != 99 {
		t.Fatalf("requestedQuoteMessageID = %d", sender.requestedQuoteMessageID)
	}
	data, err := json.Marshal(quoteGen.payload)
	if err != nil {
		t.Fatal(err)
	}
	wantPayload := `[{"user_id":12345,"user_nickname":"张三","message":"被引用的消息"}]`
	if string(data) != wantPayload {
		t.Fatalf("payload = %s, want %s", data, wantPayload)
	}
	msg, ok := sender.lastMessage.(map[string]any)
	if !ok || msg["type"] != "image" {
		t.Fatalf("message = %#v", sender.lastMessage)
	}
	file := msg["data"].(map[string]any)["file"]
	if file != "base64://base64-image" {
		t.Fatalf("image file = %q", file)
	}
}

func TestPipelineAICommandUsesService(t *testing.T) {
	sender := &fakeSender{}
	p := bot.NewPipeline(bot.Options{
		Sender: sender,
		AI: ai.NewService(ai.Options{
			Retriever: ai.StaticRetriever{Documents: []ai.Document{{ID: "1", Content: "选课说明"}}},
			Chat:      &ai.StaticChat{Response: "这是 AI 回答"},
		}),
	})
	if err := p.HandleGroupMessage(context.Background(), bot.GroupMessage{GroupID: 1, UserID: 2, Text: "/ai 怎么选课"}); err != nil {
		t.Fatal(err)
	}
	if sender.lastText != "这是 AI 回答" {
		t.Fatalf("lastText = %q", sender.lastText)
	}
}

type fakeSender struct {
	lastGroupID             int64
	lastText                string
	lastMessage             any
	quoted                  bot.QuotedMessage
	requestedQuoteMessageID int64
}

func (f *fakeSender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	_ = ctx
	f.lastGroupID = groupID
	f.lastText = text
	return nil
}

func (f *fakeSender) SendGroupMessage(ctx context.Context, groupID int64, message any) error {
	_ = ctx
	f.lastGroupID = groupID
	f.lastMessage = message
	f.lastText = ""
	if text, ok := message.(string); ok {
		f.lastText = text
	}
	return nil
}

func (f *fakeSender) GetQuoteMessage(ctx context.Context, messageID int64) (bot.QuotedMessage, error) {
	_ = ctx
	f.requestedQuoteMessageID = messageID
	return f.quoted, nil
}

type capturingQuote struct {
	result  string
	payload quote.Payload
}

func (q *capturingQuote) Generate(ctx context.Context, payload quote.Payload) (string, error) {
	_ = ctx
	q.payload = payload
	return q.result, nil
}

var _ = quote.Payload{}
