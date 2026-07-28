package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/knowledge"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	"github.com/zjutjh/jxh-go/internal/messaging/linkcleaner"
	"github.com/zjutjh/jxh-go/internal/messaging/quote"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
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
	recorder := &telemetryRecorderFake{}
	pipeline := NewPipeline(Options{Sender: sender, Settings: runtime, Telemetry: recorder})
	for _, text := range []string{"/ai question", "/q", "/test"} {
		if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{GroupID: 123, UserID: 456, Text: text}); err != nil {
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
	if !hasTelemetryResult(recorder.observations, telemetry.EventAIRequest, telemetry.ResultDisabled) {
		t.Fatalf("disabled AI telemetry=%+v", recorder.observations)
	}
}

func TestGroupCommandRouterRecordsAIOutcomes(t *testing.T) {
	for _, test := range []struct {
		name    string
		answer  string
		sources []string
		err     error
		want    telemetry.Result
	}{
		{name: "success", answer: "answer", sources: []string{"entry_1"}, want: telemetry.ResultSuccess},
		{name: "no knowledge", answer: "fallback", want: telemetry.ResultNoKnowledge},
		{name: "timeout", err: context.DeadlineExceeded, want: telemetry.ResultTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := newChannelTelemetryRecorder()
			router := NewGroupCommandRouter(Options{
				AI: &aiAnswererFake{answer: test.answer, sources: test.sources, err: test.err}, Telemetry: recorder,
			})
			if err := router.startAI(t.Context(), GroupMessage{GroupID: 123, UserID: 456}, &botSenderFake{}, "/ai private question"); err != nil {
				t.Fatal(err)
			}
			observation := waitTelemetry(t, recorder.events)
			if observation.Kind != telemetry.EventAIRequest || observation.Result != test.want ||
				observation.FeatureKey != string(settings.FeatureAIQA) || observation.GroupID != 123 || observation.UserID != 456 {
				t.Fatalf("observation=%+v", observation)
			}
			if strings.Contains(fmt.Sprintf("%+v", observation), "private question") ||
				(test.answer != "" && strings.Contains(fmt.Sprintf("%+v", observation), test.answer)) {
				t.Fatalf("AI telemetry retained content: %+v", observation)
			}
		})
	}
}

func TestGroupCommandRouterRecordsBusyAIWithoutStartingThirdRequest(t *testing.T) {
	recorder := newChannelTelemetryRecorder()
	answerer := &aiAnswererFake{answer: "answer", sources: []string{"entry_1"}, started: make(chan struct{}, 2), release: make(chan struct{})}
	router := NewGroupCommandRouter(Options{AI: answerer, Telemetry: recorder})
	sender := &botSenderFake{}
	message := GroupMessage{GroupID: 123, UserID: 456}
	if err := router.startAI(t.Context(), message, sender, "/ai first"); err != nil {
		t.Fatal(err)
	}
	if err := router.startAI(t.Context(), message, sender, "/ai second"); err != nil {
		t.Fatal(err)
	}
	waitStartedAI(t, answerer.started)
	waitStartedAI(t, answerer.started)
	if err := router.startAI(t.Context(), message, sender, "/ai third"); err != nil {
		t.Fatal(err)
	}
	if observation := waitTelemetry(t, recorder.events); observation.Result != telemetry.ResultBusy {
		t.Fatalf("busy observation=%+v", observation)
	}
	close(answerer.release)
	for range 2 {
		if observation := waitTelemetry(t, recorder.events); observation.Result != telemetry.ResultSuccess {
			t.Fatalf("completed observation=%+v", observation)
		}
	}
}

func TestGroupCommandRouterRecordsQuotePNGFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/gif/base64/" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("png-image"))
	}))
	defer server.Close()
	recorder := &telemetryRecorderFake{}
	sender := &botSenderFake{quoteMessages: []QuotedMessage{{
		UserID: 789, Nickname: "User", RawMessage: "private quote text", Message: message.ChainOf(message.Text("private quote text")),
	}}}
	pipeline := NewPipeline(Options{Sender: sender, Quote: quote.NewClient(server.URL, server.Client()), Telemetry: recorder})
	if err := pipeline.HandleGroupMessage(t.Context(), GroupMessage{
		GroupID: 123, UserID: 456, Text: "/q", ReplyMessageID: 99,
	}); err != nil {
		t.Fatal(err)
	}
	if !hasTelemetryResult(recorder.observations, telemetry.EventQuote, telemetry.ResultFallback) {
		t.Fatalf("quote telemetry=%+v", recorder.observations)
	}
	for _, observation := range recorder.observations {
		if strings.Contains(fmt.Sprintf("%+v", observation), "private quote text") {
			t.Fatalf("quote telemetry retained content: %+v", observation)
		}
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
	mu            sync.Mutex
	texts         []string
	messages      []message.Chain
	quoteReads    int
	quoteMessages []QuotedMessage
	quoteErr      error
	role          string
	roleReads     int
}

func (s *botSenderFake) SendGroupText(_ context.Context, _ int64, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.texts = append(s.texts, text)
	return nil
}

func (s *botSenderFake) SendGroupMessage(_ context.Context, _ int64, value message.Chain) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, append(message.Chain(nil), value...))
	return nil
}

func (*botSenderFake) SendGroupFlashFile(context.Context, int64, string, string) error {
	return nil
}

func (s *botSenderFake) GetQuoteMessages(context.Context, int64, int64, int) ([]QuotedMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quoteReads++
	return append([]QuotedMessage(nil), s.quoteMessages...), s.quoteErr
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

type channelTelemetryRecorder struct {
	events chan telemetry.Observation
}

func newChannelTelemetryRecorder() *channelTelemetryRecorder {
	return &channelTelemetryRecorder{events: make(chan telemetry.Observation, 8)}
}

func (r *channelTelemetryRecorder) Record(observation telemetry.Observation) bool {
	r.events <- observation
	return true
}

type aiAnswererFake struct {
	answer  string
	sources []string
	err     error
	started chan struct{}
	release chan struct{}
}

func (a *aiAnswererFake) AnswerWithSources(context.Context, string) (string, []string, error) {
	if a.started != nil {
		a.started <- struct{}{}
	}
	if a.release != nil {
		<-a.release
	}
	return a.answer, append([]string(nil), a.sources...), a.err
}

func waitTelemetry(t *testing.T, events <-chan telemetry.Observation) telemetry.Observation {
	t.Helper()
	select {
	case observation := <-events:
		return observation
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for telemetry")
		return telemetry.Observation{}
	}
}

func waitStartedAI(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for AI request")
	}
}

func hasTelemetryResult(observations []telemetry.Observation, kind telemetry.EventKind, result telemetry.Result) bool {
	for _, observation := range observations {
		if observation.Kind == kind && observation.Result == result {
			return true
		}
	}
	return false
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
