package bot

import (
	"context"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/settings"
	"github.com/zjutjh/napcat-sdk/message"
)

func TestPipelineSettingsDisableAutomaticFeatures(t *testing.T) {
	features := settings.DefaultFeatures()
	features.KeywordReply.Enabled = false
	features.LinkCleaner.Enabled = false
	features.Welcome.Enabled = false
	runtime, err := settings.NewRuntime(features, 1)
	if err != nil {
		t.Fatal(err)
	}
	sender := &botSenderFake{}
	pipeline := NewPipeline(Options{
		Sender: sender, Settings: runtime, LinkCleaner: linkcleaner.NewService(),
		Knowledge: knowledge.NewIndexRef([]knowledge.Entry{{
			SourceKey: "key_1", Keyword: "hello", Answer: "world", Enabled: true, ExactReply: true,
		}}),
	})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, Text: "https://www.bilibili.com/video/BV1?spm_id_from=tracked"}); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.HandleGroupIncrease(t.Context(), 123, 456); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 0 || len(sender.messages) != 0 {
		t.Fatalf("disabled automatic features sent texts=%v messages=%v", sender.texts, sender.messages)
	}
}

func TestPipelineSettingsDisableInteractiveCommandsButNotTest(t *testing.T) {
	features := settings.DefaultFeatures()
	features.AIQA.Enabled = false
	features.Quote.Enabled = false
	runtime, err := settings.NewRuntime(features, 1)
	if err != nil {
		t.Fatal(err)
	}
	sender := &botSenderFake{}
	pipeline := NewPipeline(Options{Sender: sender, Settings: runtime})
	for _, text := range []string{"/ai question", "/q", "/test"} {
		if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.texts) != 3 || sender.texts[0] != disabledFeatureReply || sender.texts[1] != disabledFeatureReply ||
		sender.texts[2] == disabledFeatureReply {
		t.Fatalf("command replies = %v", sender.texts)
	}
	if sender.quoteReads != 0 {
		t.Fatalf("disabled quote read %d messages", sender.quoteReads)
	}
}

func TestPipelineUsesConfiguredWelcomeTemplateAndKeepsDefaultsWithoutRuntime(t *testing.T) {
	features := settings.DefaultFeatures()
	features.Welcome.MessageTemplate = "Welcome {{member_qq}} to {{group_id}}"
	runtime, err := settings.NewRuntime(features, 1)
	if err != nil {
		t.Fatal(err)
	}
	sender := &botSenderFake{}
	pipeline := NewPipeline(Options{
		Sender: sender, Settings: runtime,
		Knowledge: knowledge.NewIndexRef([]knowledge.Entry{{
			SourceKey: "key_1", Keyword: "hello", Answer: "world", Enabled: true, ExactReply: true,
		}}),
	})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := pipeline.HandleGroupIncrease(t.Context(), 123, 456); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 || sender.texts[0] != "world" {
		t.Fatalf("keyword replies = %v", sender.texts)
	}
	if len(sender.messages) != 1 || sender.messages[0].Text() != "Welcome 456 to 123" {
		t.Fatalf("welcome messages = %v", sender.messages)
	}

	defaultSender := &botSenderFake{}
	defaultPipeline := NewPipeline(Options{Sender: defaultSender})
	if err := defaultPipeline.HandleGroupIncrease(t.Context(), 123, 456); err != nil {
		t.Fatal(err)
	}
	if len(defaultSender.messages) != 1 || defaultSender.messages[0].Text() != settings.DefaultWelcomeTemplate {
		t.Fatalf("default welcome = %v", defaultSender.messages)
	}
}

type botSenderFake struct {
	texts      []string
	messages   []message.Chain
	quoteReads int
}

func (s *botSenderFake) SendGroupText(_ context.Context, _ int64, text string) error {
	s.texts = append(s.texts, text)
	return nil
}

func (s *botSenderFake) SendGroupMessage(_ context.Context, _ int64, value message.Chain) error {
	s.messages = append(s.messages, append(message.Chain(nil), value...))
	return nil
}

func (*botSenderFake) SendGroupFlashFile(context.Context, int64, string, string) error {
	return nil
}

func (s *botSenderFake) GetQuoteMessages(context.Context, int64, int64, int) ([]QuotedMessage, error) {
	s.quoteReads++
	return nil, nil
}

func (*botSenderFake) ResolveImage(context.Context, string) (string, error) {
	return "", nil
}

func (*botSenderFake) SetGroupBan(context.Context, int64, int64, time.Duration) error {
	return nil
}

func (*botSenderFake) SetRestart(context.Context) error {
	return nil
}

func (*botSenderFake) GetGroupMemberRole(context.Context, int64, int64) (string, error) {
	return "member", nil
}
