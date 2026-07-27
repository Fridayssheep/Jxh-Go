package settings

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
)

func TestServiceGlobalUpdateChangesInheritedSettingsOnlyAfterStoreCommit(t *testing.T) {
	runtime := NewDefaultRuntime()
	store := &settingsStoreFake{}
	service := newSettingsService(t, store, runtime)
	features := DefaultFeatures()
	features.AIQA.Enabled = false
	store.globalResult = Global{Features: features, Version: 2, UpdatedAt: settingsTestTime}
	patch := GlobalPatch{AIQA: auth.Field[BasicPatch]{Set: true, Value: BasicPatch{Enabled: auth.Field[bool]{Set: true, Value: false}}}}

	value, err := service.UpdateGlobal(t.Context(), settingsWriter(), 1, patch, validMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != 2 || runtime.Enabled(123, FeatureAIQA) || store.globalMutation.ExpectedRevision != 1 {
		t.Fatalf("value=%+v effective=%+v mutation=%+v", value, runtime.Effective("123"), store.globalMutation)
	}
	if store.globalMutation.Context.Actor.UserID != "usr_1" || !store.globalMutation.Context.OccurredAt.Equal(settingsTestTime) {
		t.Fatalf("mutation context = %+v", store.globalMutation.Context)
	}

	store.updateGlobalErr = ErrConflict
	store.globalResult = Global{}
	_, err = service.UpdateGlobal(t.Context(), settingsWriter(), 2, patch, validMutationRequest())
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if runtime.Enabled(123, FeatureAIQA) {
		t.Fatal("failed store mutation changed runtime")
	}
}

func TestServiceGroupFirstWriteAndFullClear(t *testing.T) {
	runtime := NewDefaultRuntime()
	store := &settingsStoreFake{}
	service := newSettingsService(t, store, runtime)
	override := Overrides{KeywordReply: &BasicOverride{Enabled: false}}
	store.groupResult = Group{
		GroupID: "123", Effective: resolve(DefaultFeatures(), override), Overrides: override,
		GlobalVersion: 1, Version: 1, UpdatedAt: timePointer(settingsTestTime),
	}
	patch := GroupPatch{KeywordReply: OverrideFeaturePatch{
		Set: true, Enabled: auth.Field[*bool]{Set: true, Value: boolPointer(false)},
	}}
	value, err := service.UpdateGroup(t.Context(), settingsWriter(), "123", 0, patch, validMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != 1 || runtime.Enabled(123, FeatureKeywordReply) || store.groupMutation.ExpectedRevision != 0 {
		t.Fatalf("value=%+v effective=%+v mutation=%+v", value, runtime.Effective("123"), store.groupMutation)
	}

	store.groupResult = Group{GroupID: "123", Effective: DefaultFeatures(), GlobalVersion: 1, Version: 0}
	clear := GroupPatch{KeywordReply: OverrideFeaturePatch{Set: true, Clear: true}}
	value, err = service.UpdateGroup(t.Context(), settingsWriter(), "123", 1, clear, validMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != 0 || !runtime.Enabled(123, FeatureKeywordReply) {
		t.Fatalf("cleared value=%+v effective=%+v", value, runtime.Effective("123"))
	}
}

func TestServiceRejectsZeroVersionWhenPatchCreatesOverride(t *testing.T) {
	runtime := NewDefaultRuntime()
	store := &settingsStoreFake{groupResult: Group{
		GroupID: "123", Effective: DefaultFeatures(), GlobalVersion: 1, Version: 0,
	}}
	service := newSettingsService(t, store, runtime)
	patch := GroupPatch{AIQA: OverrideFeaturePatch{
		Set: true, Enabled: auth.Field[*bool]{Set: true, Value: boolPointer(false)},
	}}
	if _, err := service.UpdateGroup(t.Context(), settingsWriter(), "123", 0, patch, validMutationRequest()); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("invalid zero-version result error = %v", err)
	}
	if !runtime.Enabled(123, FeatureAIQA) {
		t.Fatal("invalid store result changed runtime")
	}
}

func TestServiceRepairsRuntimeAfterCommittedMutationVersionDrift(t *testing.T) {
	runtimeFeatures := DefaultFeatures()
	runtime, err := NewRuntime(runtimeFeatures, 2)
	if err != nil {
		t.Fatal(err)
	}
	persisted := DefaultFeatures()
	persisted.AIQA.Enabled = false
	global := Global{Features: persisted, Version: 2, UpdatedAt: settingsTestTime}
	store := &settingsStoreFake{globalResult: global, runtimeState: RuntimeState{Global: global}}
	service := newSettingsService(t, store, runtime)
	patch := GlobalPatch{AIQA: auth.Field[BasicPatch]{Set: true, Value: BasicPatch{
		Enabled: auth.Field[bool]{Set: true, Value: false},
	}}}
	if _, err := service.UpdateGlobal(t.Context(), settingsWriter(), 1, patch, validMutationRequest()); err != nil {
		t.Fatal(err)
	}
	if runtime.Enabled(123, FeatureAIQA) {
		t.Fatal("runtime repair did not load the committed settings")
	}

	runtime, err = NewRuntime(runtimeFeatures, 2)
	if err != nil {
		t.Fatal(err)
	}
	store = &settingsStoreFake{globalResult: global, loadErr: errors.New("database unavailable")}
	service = newSettingsService(t, store, runtime)
	if _, err := service.UpdateGlobal(t.Context(), settingsWriter(), 1, patch, validMutationRequest()); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("failed repair error = %v", err)
	}
	if !runtime.Enabled(123, FeatureAIQA) {
		t.Fatal("failed repair discarded the last valid snapshot")
	}
}

func TestServiceRejectsInvalidPatchesAndUnauthorizedAccessBeforeStore(t *testing.T) {
	runtime := NewDefaultRuntime()
	store := &settingsStoreFake{}
	service := newSettingsService(t, store, runtime)
	unknownTemplate := "hello {{secret}}"
	patch := GlobalPatch{Welcome: auth.Field[WelcomePatch]{Set: true, Value: WelcomePatch{
		MessageTemplate: auth.Field[string]{Set: true, Value: unknownTemplate},
	}}}
	if _, err := service.UpdateGlobal(t.Context(), settingsWriter(), 1, patch, validMutationRequest()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid template error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("invalid patch reached store %d times", store.calls)
	}
	if _, err := service.GetGlobal(t.Context(), auth.Principal{Role: "invalid"}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("unauthorized read error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("unauthorized read reached store %d times", store.calls)
	}
}

func TestServiceReloadRejectsInvalidStateWithoutReplacingSnapshot(t *testing.T) {
	runtime := NewDefaultRuntime()
	store := &settingsStoreFake{runtimeState: RuntimeState{
		Global: Global{Features: DefaultFeatures(), Version: 2, UpdatedAt: settingsTestTime},
		Groups: []RuntimeGroup{
			{GroupID: "123", Overrides: Overrides{AIQA: &BasicOverride{Enabled: false}}, Version: 1},
			{GroupID: "123", Overrides: Overrides{AIQA: &BasicOverride{Enabled: true}}, Version: 2},
		},
	}}
	service := newSettingsService(t, store, runtime)
	if err := service.ReloadRuntime(t.Context()); !errors.Is(err, ErrInvalidData) {
		t.Fatalf("reload error = %v", err)
	}
	if !runtime.Enabled(123, FeatureAIQA) {
		t.Fatal("invalid reload replaced the last valid snapshot")
	}

	store.runtimeState.Groups = store.runtimeState.Groups[:1]
	if err := service.ReloadRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if runtime.Enabled(123, FeatureAIQA) {
		t.Fatal("valid reload did not replace runtime")
	}
}

func TestServiceDeleteRequiresExistingRevision(t *testing.T) {
	runtime := NewDefaultRuntime()
	service := newSettingsService(t, &settingsStoreFake{}, runtime)
	if err := service.DeleteGroup(t.Context(), settingsWriter(), "123", 0, validMutationRequest()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("DeleteGroup revision zero error = %v", err)
	}
}

var settingsTestTime = time.Date(2026, 7, 28, 1, 2, 3, 0, time.UTC)

func newSettingsService(t *testing.T, store Store, runtime *Runtime) *Service {
	t.Helper()
	service, err := NewService(Options{Store: store, Runtime: runtime, Now: func() time.Time { return settingsTestTime }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func settingsWriter() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleMaintainer}
}

func validMutationRequest() auth.MutationContext {
	return auth.MutationContext{RequestID: "req_1", IPAddress: "192.0.2.1", UserAgent: "settings-test"}
}

func boolPointer(value bool) *bool {
	return &value
}

type settingsStoreFake struct {
	globalResult    Global
	groupResult     Group
	runtimeState    RuntimeState
	updateGlobalErr error
	updateGroupErr  error
	deleteGroupErr  error
	getGlobalErr    error
	getGroupErr     error
	loadErr         error
	globalMutation  UpdateGlobalMutation
	groupMutation   UpdateGroupMutation
	deleteMutation  DeleteGroupMutation
	calls           int
}

func (s *settingsStoreFake) GetGlobalSettings(context.Context) (Global, error) {
	s.calls++
	return s.globalResult, s.getGlobalErr
}

func (s *settingsStoreFake) GetGroupSettings(context.Context, string) (Group, bool, error) {
	s.calls++
	return s.groupResult, s.getGroupErr == nil, s.getGroupErr
}

func (s *settingsStoreFake) LoadRuntimeSettings(context.Context) (RuntimeState, error) {
	s.calls++
	return s.runtimeState, s.loadErr
}

func (s *settingsStoreFake) UpdateGlobalSettings(_ context.Context, mutation UpdateGlobalMutation) (Global, error) {
	s.calls++
	s.globalMutation = mutation
	return s.globalResult, s.updateGlobalErr
}

func (s *settingsStoreFake) UpdateGroupSettings(_ context.Context, mutation UpdateGroupMutation) (Group, error) {
	s.calls++
	s.groupMutation = mutation
	return s.groupResult, s.updateGroupErr
}

func (s *settingsStoreFake) DeleteGroupSettings(_ context.Context, mutation DeleteGroupMutation) (Group, error) {
	s.calls++
	s.deleteMutation = mutation
	return s.groupResult, s.deleteGroupErr
}

type settingsEventSink struct {
	drafts []events.Draft
}

func (s *settingsEventSink) Publish(draft events.Draft) (events.Event, error) {
	s.drafts = append(s.drafts, draft)
	return events.Event{}, nil
}
