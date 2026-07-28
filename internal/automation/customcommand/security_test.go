package customcommand

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestCrossGroupDefinitionsRequireSuperAdminButApprovedToggleDoesNot(t *testing.T) {
	store := &fakeStore{}
	service := newServiceFixture(t, store, nil)
	_, err := service.Create(t.Context(), writerPrincipal(), validDefinition(), validMutationRequest())
	if !errors.Is(err, ErrForbidden) || store.calls != 0 {
		t.Fatalf("maintainer create err=%v calls=%d", err, store.calls)
	}

	superAdmin := auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}
	if _, err = service.Create(t.Context(), superAdmin, validDefinition(), validMutationRequest()); err != nil {
		t.Fatalf("super-admin create: %v", err)
	}

	store.calls = 0
	if _, err = service.Update(t.Context(), writerPrincipal(), "cmd_1", 1, Patch{
		Enabled: auth.Field[bool]{Set: true, Value: false},
	}, validMutationRequest()); err != nil {
		t.Fatalf("maintainer toggle approved definition: %v", err)
	}
	if store.calls != 2 {
		t.Fatalf("toggle calls=%d", store.calls)
	}

	store.calls = 0
	_, err = service.Update(t.Context(), writerPrincipal(), "cmd_1", 1, Patch{
		Description: auth.Field[string]{Set: true, Value: "changed"},
	}, validMutationRequest())
	if !errors.Is(err, ErrForbidden) || store.calls != 1 {
		t.Fatalf("maintainer definition edit err=%v calls=%d", err, store.calls)
	}
}

func TestStoredPreviewDoesNotConflictWithItself(t *testing.T) {
	store := &fakeStore{}
	service := newServiceFixture(t, store, nil)
	if err := service.Registry().Upsert(storedCommand()); err != nil {
		t.Fatal(err)
	}
	result, err := service.ValidateStored(t.Context(), writerPrincipal(), "cmd_1", ValidationSample{
		GroupID: "123", SenderQQ: "9988", SenderRole: SenderAdmin, Message: `/welcome 7788 "text" 60`,
	})
	if err != nil || !result.Valid {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPreviewPreservesQuotedEmptyTextAtZeroMinimum(t *testing.T) {
	store := &fakeStore{}
	service := newServiceFixture(t, store, nil)
	definition := Definition{
		Name: "/empty", DisplayName: "Empty", Scope: Scope{Type: ScopeGlobal}, TriggerPermission: TriggerEveryone,
		Parameters: []Parameter{{Name: "text", DisplayName: "Text", Type: ParameterText, Required: true, MinLength: 0, MaxLength: 10}},
		Actions:    []Action{{Type: ActionReplyText, Template: "value={{text}}"}},
	}
	result, err := service.ValidateDraft(t.Context(), writerPrincipal(), definition, ValidationSample{
		GroupID: "123", SenderQQ: "9988", SenderRole: SenderMember, Message: `/empty ""`,
	})
	if err != nil || !result.Valid || len(result.ParsedArguments) != 1 || result.ParsedArguments[0].DisplayValue != "" ||
		len(result.RenderedActions) != 1 || result.RenderedActions[0].Preview != "value=" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPersistRunDetachesFromCanceledActionContext(t *testing.T) {
	store := &contextCheckingStore{}
	service := newServiceFixture(t, store, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, handled, err := service.persistRun(ctx, Run{Result: RunUnknown}, time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC))
	if err != nil || !handled || store.sawCanceled {
		t.Fatalf("handled=%v err=%v saw_canceled=%v", handled, err, store.sawCanceled)
	}
}

func TestCompileAndRegistrySnapshotsAreImmutable(t *testing.T) {
	definition := validDefinition()
	compiled, err := Compile(definition)
	if err != nil {
		t.Fatal(err)
	}
	definition.Scope.GroupIDs[0] = "999"
	definition.Actions[0].Template = "changed"
	if got := compiled.Definition(); got.Scope.GroupIDs[0] != "123" || got.Actions[0].Template != "hello {{text}}" {
		t.Fatalf("compiled definition aliased source: %+v", got)
	}

	registry, err := NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Upsert(storedCommand()); err != nil {
		t.Fatal(err)
	}
	first, ok := registry.Match("/welcome 7788 text 60", "123")
	if !ok {
		t.Fatal("command did not match")
	}
	first.Actions[0].Template = "mutated"
	second, ok := registry.Match("/welcome 7788 text 60", "123")
	if !ok || second.Actions[0].Template != "hello {{text}}" {
		t.Fatalf("registry snapshot was mutable: %+v", second)
	}
}

func TestExecutePersistsDeniedParseAndUnknownWithSkippedSteps(t *testing.T) {
	cases := []struct {
		name       string
		message    string
		role       SenderRole
		gateway    Gateway
		wantResult RunResult
		wantFirst  StepResult
	}{
		{name: "denied", message: "/simple private", role: SenderMember, gateway: &unknownGateway{}, wantResult: RunDenied, wantFirst: StepSkipped},
		{name: "parse", message: "/simple", role: SenderAdmin, gateway: &unknownGateway{}, wantResult: RunParseError, wantFirst: StepSkipped},
		{name: "unknown", message: "/simple private", role: SenderAdmin, gateway: &unknownGateway{}, wantResult: RunUnknown, wantFirst: StepUnknown},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeStore{}
			service := newServiceFixture(t, store, test.gateway)
			command := storedCommand()
			command.Definition = Definition{
				Name: "/simple", DisplayName: "Simple", Scope: Scope{Type: ScopeGlobal}, TriggerPermission: TriggerGroupAdmin,
				Parameters: []Parameter{{Name: "text", DisplayName: "Text", Type: ParameterText, Required: true, MinLength: 1, MaxLength: 100}},
				Actions:    []Action{{Type: ActionReplyText, Template: "{{text}}"}, {Type: ActionMention, Target: MentionTriggerer}},
			}
			if err := service.Registry().Upsert(command); err != nil {
				t.Fatal(err)
			}
			run, handled, err := service.Execute(t.Context(), ExecuteInput{GroupID: "123", SenderQQ: "9988", SenderRole: test.role, Message: test.message})
			if err != nil || !handled || run.Result != test.wantResult || len(run.ActionSteps) != 2 ||
				run.ActionSteps[0].Result != test.wantFirst || run.ActionSteps[1].Result != StepSkipped {
				t.Fatalf("handled=%v run=%+v err=%v", handled, run, err)
			}
			if strings.Contains(fmt.Sprintf("%+v", store.recorded), "private") {
				t.Fatalf("record retained message text: %+v", store.recorded)
			}
		})
	}
}

type contextCheckingStore struct {
	fakeStore
	sawCanceled bool
}

func (s *contextCheckingStore) RecordCommandRun(ctx context.Context, run Run) (Run, error) {
	s.sawCanceled = ctx.Err() != nil
	run.ID = "run_1"
	return run, nil
}

type unknownGateway struct{}

func (*unknownGateway) Available() bool                                                 { return true }
func (*unknownGateway) ReplyText(context.Context, string, string) error                 { return ErrOutcomeUnknown }
func (*unknownGateway) Mention(context.Context, string, string) error                   { return nil }
func (*unknownGateway) MuteMember(context.Context, string, string, time.Duration) error { return nil }
func (*unknownGateway) SendGroupText(context.Context, string, string) error             { return nil }
