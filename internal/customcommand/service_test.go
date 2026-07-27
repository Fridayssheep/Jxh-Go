package customcommand

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
)

func TestDefinitionRejectsPrivilegeEscalationAndDynamicTargets(t *testing.T) {
	definition := validDefinition()
	definition.TriggerPermission = TriggerEveryone
	definition.Actions = []Action{{
		Type: ActionMuteMember, MemberParameter: "member",
		Duration: DurationSource{Type: DurationFixed, Seconds: 60},
	}, {
		Type: ActionSendGroupText, TargetGroupIDs: []string{"123"}, Template: "hello",
	}}
	issues := ValidateDefinition(definition)
	if !hasIssue(issues, "privilege_escalation") {
		t.Fatalf("issues=%+v", issues)
	}

	definition = validDefinition()
	definition.Actions = []Action{{Type: ActionSendGroupText, TargetGroupIDs: []string{"{{target}}"}, Template: "hello"}}
	if issues = ValidateDefinition(definition); !hasIssue(issues, "invalid_group_send_action") {
		t.Fatalf("dynamic target was accepted: %+v", issues)
	}
}

func TestValidateDraftHasNoStoreOrGatewaySideEffects(t *testing.T) {
	store := &fakeStore{}
	gateway := &fakeGateway{available: true}
	service := newServiceFixture(t, store, gateway)
	result, err := service.ValidateDraft(t.Context(), writerPrincipal(), validDefinition(), ValidationSample{
		GroupID: "123", SenderQQ: "9988", SenderRole: SenderAdmin, Message: `/welcome 7788 "private words" 60`,
	})
	if err != nil || !result.Valid || len(result.RenderedActions) != 4 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if store.calls != 0 || len(gateway.calls) != 0 {
		t.Fatalf("preview caused effects: store=%d gateway=%v", store.calls, gateway.calls)
	}
}

func TestExecuteRunsActionsInOrderStopsAndRecordsPartialWithoutRawText(t *testing.T) {
	store := &fakeStore{}
	gateway := &fakeGateway{available: true, failAt: 3}
	service := newServiceFixture(t, store, gateway)
	command := storedCommand()
	if err := service.Registry().Upsert(command); err != nil {
		t.Fatal(err)
	}
	run, handled, err := service.Execute(t.Context(), ExecuteInput{
		GroupID: "123", SenderQQ: "9988", SenderRole: SenderAdmin, Message: `/welcome 7788 "private words" 60`,
	})
	if err != nil || !handled {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	if got := strings.Join(gateway.calls, ","); got != "reply,mention,mute" {
		t.Fatalf("calls=%q", got)
	}
	if run.Result != RunPartial || len(run.ActionSteps) != 4 || run.ActionSteps[0].Result != StepSuccess ||
		run.ActionSteps[1].Result != StepSuccess || run.ActionSteps[2].Result != StepFailed || run.ActionSteps[3].Result != StepSkipped {
		t.Fatalf("run=%+v", run)
	}
	if strings.Contains(fmt.Sprintf("%+v", store.recorded), "private words") {
		t.Fatalf("record retained raw text: %+v", store.recorded)
	}
	if len(store.recorded.ArgumentSummaries) != 3 || store.recorded.ArgumentSummaries[1].Digest == "" {
		t.Fatalf("summaries=%+v", store.recorded.ArgumentSummaries)
	}
}

func TestExecuteRetriesRunPersistenceWithoutRepeatingActions(t *testing.T) {
	base := &fakeStore{}
	store := &retryCommandStore{fakeStore: base, completed: make(chan Run, 1)}
	gateway := &fakeGateway{available: true}
	eventSink := &retryCommandEventSink{completed: make(chan events.Draft, 1)}
	service, err := NewService(Options{
		Store: store, Gateway: gateway, Events: eventSink,
		Now:                   func() time.Time { return time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC) },
		ArgumentSummaryKey:    []byte("0123456789abcdef0123456789abcdef"),
		PersistenceRetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if err := service.Registry().Upsert(storedCommand()); err != nil {
		t.Fatal(err)
	}
	_, handled, err := service.Execute(t.Context(), ExecuteInput{
		RunIdentity: "qqmsg:123:456", GroupID: "123", SenderQQ: "9988", SenderRole: SenderAdmin,
		Message: `/welcome 7788 "safe text" 60`,
	})
	if err == nil || !handled {
		t.Fatalf("handled=%t error=%v", handled, err)
	}
	var stored Run
	select {
	case stored = <-store.completed:
	case <-time.After(time.Second):
		t.Fatal("command run persistence retry did not finish")
	}
	if stored.Result != RunSuccess || store.attempts.Load() != 2 {
		t.Fatalf("stored=%+v attempts=%d", stored, store.attempts.Load())
	}
	if got := strings.Join(gateway.calls, ","); got != "reply,mention,mute,send" {
		t.Fatalf("action calls=%q", got)
	}
	select {
	case draft := <-eventSink.completed:
		if draft.Type != events.EventCommandRunCompleted || draft.Resource == nil || draft.Resource.ID != "cmd_1" {
			t.Fatalf("draft=%+v", draft)
		}
	case <-time.After(time.Second):
		t.Fatal("command completion event was not published")
	}
}

func TestBuiltinConflictAndPermissionHappenBeforeStoreMutation(t *testing.T) {
	store := &fakeStore{}
	service := newServiceFixture(t, store, nil)
	definition := validDefinition()
	definition.Name = "/admin"
	_, err := service.Create(t.Context(), writerPrincipal(), definition, validMutationRequest())
	if !errors.Is(err, ErrConflict) || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
	_, err = service.Create(t.Context(), auth.Principal{Role: auth.RoleObserver}, validDefinition(), validMutationRequest())
	if !errors.Is(err, ErrForbidden) || store.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, store.calls)
	}
}

func TestRegistryUsesExactNameAndFixedScope(t *testing.T) {
	registry, err := NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	command := storedCommand()
	if err := registry.Upsert(command); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Match("/welcomee 7788 text 60", "123"); ok {
		t.Fatal("prefix command matched")
	}
	if _, ok := registry.Match("/welcome 7788 text 60", "456"); ok {
		t.Fatal("command matched outside fixed scope")
	}
	if _, ok := registry.Match("/welcome 7788 text 60", "123"); !ok {
		t.Fatal("exact command did not match")
	}
}

func TestLoadRegistryPaginatesAndProbeUsesPublishedSnapshot(t *testing.T) {
	first := storedCommand()
	second := storedCommand()
	second.ID = "cmd_2"
	second.Name = "/second"
	store := &fakeStore{listPages: []Page[Command]{
		{Items: []Command{first}, HasMore: true, NextCursor: "cursor_2"},
		{Items: []Command{second}},
	}}
	service := newServiceFixture(t, store, nil)
	if err := service.LoadRegistry(t.Context()); err != nil {
		t.Fatal(err)
	}
	if permission, ok := service.Probe("/second 7788 words 60", "123"); !ok || permission != TriggerGroupAdmin {
		t.Fatalf("probe permission=%q matched=%t", permission, ok)
	}
	if len(store.listQueries) != 2 || store.listQueries[0].Cursor != "" || store.listQueries[1].Cursor != "cursor_2" ||
		store.listQueries[0].Status != StatusActive || store.listQueries[0].Limit != 100 {
		t.Fatalf("queries=%+v", store.listQueries)
	}
}

func TestLoadRegistryRejectsCursorCyclesWithoutReplacingSnapshot(t *testing.T) {
	store := &fakeStore{listPages: []Page[Command]{
		{HasMore: true, NextCursor: "cursor_2"},
		{HasMore: true, NextCursor: "cursor_2"},
	}}
	service := newServiceFixture(t, store, nil)
	if err := service.Registry().Upsert(storedCommand()); err != nil {
		t.Fatal(err)
	}
	if err := service.LoadRegistry(t.Context()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("err=%v", err)
	}
	if _, matched := service.Probe("/welcome 7788 words 60", "123"); !matched {
		t.Fatal("failed load replaced the prior registry")
	}
}

func TestServicePublishesSanitizedDefinitionAndRunEvents(t *testing.T) {
	store := &fakeStore{}
	eventSink := &commandEventSinkFake{}
	service := newServiceFixtureWithEvents(t, store, &fakeGateway{available: true}, eventSink)
	definition := validDefinition()
	created, err := service.Create(t.Context(), auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}, definition, validMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	created.Enabled = true
	created.Status = StatusActive
	if err := service.Registry().Upsert(created); err != nil {
		t.Fatal(err)
	}
	if _, handled, err := service.Execute(t.Context(), ExecuteInput{
		GroupID: "123", SenderQQ: "9988", SenderRole: SenderAdmin, Message: `/welcome 7788 "private words" 60`,
	}); err != nil || !handled {
		t.Fatalf("handled=%t err=%v", handled, err)
	}
	if len(eventSink.drafts) != 2 || eventSink.drafts[0].Type != events.EventCommandUpdated ||
		eventSink.drafts[1].Type != events.EventCommandRunCompleted || eventSink.drafts[1].Reason != string(RunSuccess) {
		t.Fatalf("drafts=%+v", eventSink.drafts)
	}
	if strings.Contains(fmt.Sprintf("%+v", eventSink.drafts), "private words") {
		t.Fatalf("events retained command arguments: %+v", eventSink.drafts)
	}
}

func TestDefinitionAcceptsOpaqueGroupIdentifiersAndRejectsRequiredAfterOptional(t *testing.T) {
	definition := validDefinition()
	definition.Scope.GroupIDs = []string{"group:east-campus"}
	definition.Actions[3].TargetGroupIDs = []string{"group:announcements"}
	if issues := ValidateDefinition(definition); len(issues) != 0 {
		t.Fatalf("opaque group identifiers were rejected: %+v", issues)
	}
	definition.Parameters[0].Required = false
	if issues := ValidateDefinition(definition); !hasIssue(issues, "required_after_optional") {
		t.Fatalf("ambiguous positional parameters were accepted: %+v", issues)
	}
}

func validDefinition() Definition {
	return Definition{
		Name: "/welcome", DisplayName: "Welcome", Description: "Test", Scope: Scope{Type: ScopeGroups, GroupIDs: []string{"123"}},
		TriggerPermission: TriggerGroupAdmin,
		Parameters: []Parameter{
			{Name: "member", DisplayName: "Member", Type: ParameterMember, Required: true, AllowTriggerer: false},
			{Name: "text", DisplayName: "Text", Type: ParameterText, Required: true, MinLength: 1, MaxLength: 100},
			{Name: "seconds", DisplayName: "Duration", Type: ParameterDuration, Required: true, MinimumSeconds: 1, MaximumSeconds: 3600},
		},
		Actions: []Action{
			{Type: ActionReplyText, Template: "hello {{text}}"},
			{Type: ActionMention, Target: MentionParameter, MemberParameter: "member"},
			{Type: ActionMuteMember, MemberParameter: "member", Duration: DurationSource{Type: DurationParameter, Parameter: "seconds"}},
			{Type: ActionSendGroupText, TargetGroupIDs: []string{"456"}, Template: "{{sender_qq}}: {{text}}"},
		},
	}
}

func storedCommand() Command {
	userID := "usr_1"
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	return Command{
		ID: "cmd_1", Definition: validDefinition(), Enabled: true, Status: StatusActive, Version: 1,
		CreatedAt: now, UpdatedAt: now, UpdatedBy: audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: "Admin"},
	}
}

func writerPrincipal() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleMaintainer}
}

func validMutationRequest() auth.MutationContext {
	return auth.MutationContext{RequestID: "req_1", IPAddress: "127.0.0.1", UserAgent: "test"}
}

func newServiceFixture(t *testing.T, store Store, gateway Gateway) *Service {
	return newServiceFixtureWithEvents(t, store, gateway, nil)
}

func newServiceFixtureWithEvents(t *testing.T, store Store, gateway Gateway, eventSink EventPublisher) *Service {
	t.Helper()
	service, err := NewService(Options{
		Store: store, Gateway: gateway, Now: func() time.Time { return time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC) },
		ArgumentSummaryKey: []byte("0123456789abcdef0123456789abcdef"), Events: eventSink,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

type commandEventSinkFake struct {
	drafts []events.Draft
}

type retryCommandEventSink struct {
	completed chan events.Draft
}

func (s *retryCommandEventSink) Publish(draft events.Draft) (events.Event, error) {
	s.completed <- draft
	return events.Event{}, nil
}

func (f *commandEventSinkFake) Publish(draft events.Draft) (events.Event, error) {
	f.drafts = append(f.drafts, draft)
	return events.Event{}, nil
}

func hasIssue(issues []ValidationIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

type fakeStore struct {
	calls       int
	recorded    Run
	listPages   []Page[Command]
	listQueries []ListQuery
	listIndex   int
}

type retryCommandStore struct {
	*fakeStore
	attempts  atomic.Int64
	completed chan Run
}

func (s *retryCommandStore) RecordCommandRun(ctx context.Context, run Run) (Run, error) {
	if s.attempts.Add(1) == 1 {
		return Run{}, errors.New("database unavailable")
	}
	stored, err := s.fakeStore.RecordCommandRun(ctx, run)
	if err == nil {
		s.completed <- cloneRun(stored)
	}
	return stored, err
}

func (s *fakeStore) CommandNameExists(context.Context, string, string) (bool, error) {
	s.calls++
	return false, nil
}

func (s *fakeStore) CreateCommand(_ context.Context, mutation CreateMutation) (Command, error) {
	s.calls++
	command := storedCommand()
	command.Definition = mutation.Definition
	command.Enabled, command.Status = false, StatusDraft
	return command, nil
}

func (s *fakeStore) GetCommand(context.Context, string) (Command, bool, error) {
	s.calls++
	return storedCommand(), true, nil
}

func (s *fakeStore) ListCommands(_ context.Context, query ListQuery) (Page[Command], error) {
	s.calls++
	s.listQueries = append(s.listQueries, query)
	if s.listIndex < len(s.listPages) {
		page := s.listPages[s.listIndex]
		s.listIndex++
		return page, nil
	}
	return Page[Command]{}, nil
}

func (s *fakeStore) UpdateCommand(context.Context, UpdateMutation) (Command, error) {
	s.calls++
	return storedCommand(), nil
}

func (s *fakeStore) ArchiveCommand(context.Context, ArchiveMutation) error {
	s.calls++
	return nil
}

func (s *fakeStore) ListCommandRuns(context.Context, RunListQuery) (Page[Run], error) {
	s.calls++
	return Page[Run]{}, nil
}

func (s *fakeStore) RecordCommandRun(_ context.Context, run Run) (Run, error) {
	s.calls++
	s.recorded = cloneRun(run)
	run.ID = "run_1"
	return run, nil
}

type fakeGateway struct {
	available bool
	failAt    int
	calls     []string
}

func (g *fakeGateway) Available() bool { return g.available }

func (g *fakeGateway) append(name string) error {
	g.calls = append(g.calls, name)
	if g.failAt == len(g.calls) {
		return errors.New("private upstream failure")
	}
	return nil
}

func (g *fakeGateway) ReplyText(context.Context, string, string) error {
	return g.append("reply")
}

func (g *fakeGateway) Mention(context.Context, string, string) error {
	return g.append("mention")
}

func (g *fakeGateway) MuteMember(context.Context, string, string, time.Duration) error {
	return g.append("mute")
}

func (g *fakeGateway) SendGroupText(context.Context, string, string) error {
	return g.append("send")
}
