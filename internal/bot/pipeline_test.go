package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/customcommand"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/settings"
	"github.com/zjutjh/jxh-go/internal/telemetry"
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

func TestPipelineRunsCustomCommandBetweenBuiltinsAndKeywordReply(t *testing.T) {
	executor := &customCommandExecutorFake{matched: true, handled: true, permission: customcommand.TriggerEveryone}
	sender := &botSenderFake{}
	pipeline := NewPipeline(Options{
		Sender: sender, CustomCommands: executor,
		Knowledge: knowledge.NewIndexRef([]knowledge.Entry{{
			SourceKey: "key_1", Keyword: "/hello", Answer: "keyword", Enabled: true, ExactReply: true,
		}}),
	})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{
		GroupID: 123, UserID: 456, MessageID: 789, Text: "/hello",
	}); err != nil {
		t.Fatal(err)
	}
	if executor.executeCalls != 1 || executor.input.RunIdentity != "qqmsg:123:789" || executor.input.SenderRole != customcommand.SenderMember {
		t.Fatalf("executor=%+v", executor)
	}
	if len(sender.texts) != 0 {
		t.Fatalf("custom command fell through to keyword reply: %v", sender.texts)
	}

	executor.executeCalls = 0
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, UserID: 456, Text: "/test"}); err != nil {
		t.Fatal(err)
	}
	if executor.executeCalls != 0 || len(sender.texts) != 1 {
		t.Fatalf("builtin did not take precedence: calls=%d texts=%v", executor.executeCalls, sender.texts)
	}
}

func TestPipelineCustomCommandHonorsFeatureAndPermissionResolvers(t *testing.T) {
	features := settings.DefaultFeatures()
	features.CustomCommand.Enabled = false
	runtime, err := settings.NewRuntime(features, 1)
	if err != nil {
		t.Fatal(err)
	}
	executor := &customCommandExecutorFake{matched: true, handled: true, permission: customcommand.TriggerGroupAdmin}
	sender := &botSenderFake{role: "owner"}
	pipeline := NewPipeline(Options{Sender: sender, Settings: runtime, CustomCommands: executor})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, UserID: 456, Text: "/hello"}); err != nil {
		t.Fatal(err)
	}
	if executor.probeCalls != 0 || sender.roleReads != 0 {
		t.Fatalf("disabled feature touched executor or gateway: probe=%d roles=%d", executor.probeCalls, sender.roleReads)
	}

	features.CustomCommand.Enabled = true
	runtime, err = settings.NewRuntime(features, 1)
	if err != nil {
		t.Fatal(err)
	}
	pipeline = NewPipeline(Options{Sender: sender, Settings: runtime, CustomCommands: executor})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, UserID: 456, Text: "/hello"}); err != nil {
		t.Fatal(err)
	}
	if executor.input.SenderRole != customcommand.SenderOwner || sender.roleReads != 1 {
		t.Fatalf("role=%q reads=%d", executor.input.SenderRole, sender.roleReads)
	}

	executor.permission = customcommand.TriggerMaintenanceAllowlist
	allowlist := &maintenanceAllowlistFake{allowed: true}
	pipeline = NewPipeline(Options{Sender: sender, Settings: runtime, CustomCommands: executor, MaintenanceAllowlist: allowlist})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, UserID: 456, Text: "/hello"}); err != nil {
		t.Fatal(err)
	}
	if !executor.input.MaintenanceAllowlisted || allowlist.calls != 1 {
		t.Fatalf("allowlisted=%t calls=%d", executor.input.MaintenanceAllowlisted, allowlist.calls)
	}
}

func TestPipelineStopsAfterCustomCommandResolverFailure(t *testing.T) {
	executor := &customCommandExecutorFake{matched: true, handled: true, permission: customcommand.TriggerMaintenanceAllowlist}
	pipeline := NewPipeline(Options{
		Sender: &botSenderFake{}, CustomCommands: executor,
		MaintenanceAllowlist: &maintenanceAllowlistFake{err: errors.New("store unavailable")},
	})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, UserID: 456, Text: "/hello"}); err == nil {
		t.Fatal("expected resolver error")
	}
	if executor.executeCalls != 0 {
		t.Fatal("command executed after resolver failure")
	}
}

func TestPipelineRecordsOnlyStructuredTelemetry(t *testing.T) {
	recorder := &telemetryRecorderFake{}
	executor := &customCommandExecutorFake{
		matched: true, handled: true, permission: customcommand.TriggerEveryone,
		run: customcommand.Run{CommandID: "cmd_1", Result: customcommand.RunSuccess, Duration: time.Second},
	}
	pipeline := NewPipeline(Options{Sender: &botSenderFake{}, CustomCommands: executor, Telemetry: recorder})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{
		GroupID: 123, UserID: 456, MessageID: 789, Text: "/hello private words",
	}); err != nil {
		t.Fatal(err)
	}
	if len(recorder.observations) != 2 {
		t.Fatalf("observations=%+v", recorder.observations)
	}
	messageEvent, commandEvent := recorder.observations[0], recorder.observations[1]
	if messageEvent.Kind != telemetry.EventGroupMessage || commandEvent.Kind != telemetry.EventCommandRun ||
		commandEvent.CommandID != "cmd_1" || commandEvent.Result != telemetry.ResultSuccess ||
		commandEvent.FeatureKey != string(settings.FeatureCustomCommand) {
		t.Fatalf("observations=%+v", recorder.observations)
	}
	for _, observation := range recorder.observations {
		if strings.Contains(fmt.Sprintf("%+v", observation), "private words") {
			t.Fatalf("telemetry retained message body: %+v", observation)
		}
	}
}

type botSenderFake struct {
	texts      []string
	messages   []message.Chain
	quoteReads int
	role       string
	roleReads  int
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

func (s *botSenderFake) GetGroupMemberRole(context.Context, int64, int64) (string, error) {
	s.roleReads++
	if s.role == "" {
		return "member", nil
	}
	return s.role, nil
}

type customCommandExecutorFake struct {
	matched      bool
	handled      bool
	permission   customcommand.TriggerPermission
	probeCalls   int
	executeCalls int
	input        customcommand.ExecuteInput
	run          customcommand.Run
}

func (f *customCommandExecutorFake) Probe(string, string) (customcommand.TriggerPermission, bool) {
	f.probeCalls++
	return f.permission, f.matched
}

func (f *customCommandExecutorFake) Execute(_ context.Context, input customcommand.ExecuteInput) (customcommand.Run, bool, error) {
	f.executeCalls++
	f.input = input
	return f.run, f.handled, nil
}

type telemetryRecorderFake struct {
	observations []telemetry.Observation
}

func (f *telemetryRecorderFake) Record(observation telemetry.Observation) bool {
	f.observations = append(f.observations, observation)
	return true
}

type maintenanceAllowlistFake struct {
	allowed bool
	err     error
	calls   int
}

func (f *maintenanceAllowlistFake) Contains(context.Context, int64, int64) (bool, error) {
	f.calls++
	return f.allowed, f.err
}
