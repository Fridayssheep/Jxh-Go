package settings

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
)

var (
	ErrForbidden    = errors.New("settings operation forbidden")
	ErrInvalidInput = errors.New("invalid settings input")
	ErrInvalidData  = errors.New("invalid settings data")
	ErrNotFound     = errors.New("settings group not found")
	ErrConflict     = errors.New("settings revision conflict")
)

type Store interface {
	GetGlobalSettings(ctx context.Context) (Global, error)
	GetGroupSettings(ctx context.Context, groupID string) (Group, bool, error)
	LoadRuntimeSettings(ctx context.Context) (RuntimeState, error)
	// UpdateGlobalSettings atomically checks the revision, updates settings and
	// appends the audit record.
	UpdateGlobalSettings(ctx context.Context, mutation UpdateGlobalMutation) (Global, error)
	// UpdateGroupSettings checks that the managed group exists and atomically
	// applies field-level overrides with the audit record.
	UpdateGroupSettings(ctx context.Context, mutation UpdateGroupMutation) (Group, error)
	// DeleteGroupSettings atomically removes all overrides and writes the audit
	// record. It returns the inherited effective document with version zero.
	DeleteGroupSettings(ctx context.Context, mutation DeleteGroupMutation) (Group, error)
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type Options struct {
	Store   Store
	Runtime *Runtime
	Events  EventPublisher
	Now     func() time.Time
}

type Service struct {
	store    Store
	runtime  *Runtime
	events   EventPublisher
	now      func() time.Time
	mutation sync.Mutex
}

var templateVariablePattern = regexp.MustCompile(`\{\{([a-z_][a-z0-9_]*)\}\}`)

const runtimeRepairTimeout = 5 * time.Second

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Runtime == nil || options.Now == nil {
		return nil, ErrInvalidInput
	}
	return &Service{store: options.Store, runtime: options.Runtime, events: options.Events, now: options.Now}, nil
}

func (s *Service) ReloadRuntime(ctx context.Context) error {
	s.mutation.Lock()
	defer s.mutation.Unlock()
	state, err := s.store.LoadRuntimeSettings(ctx)
	if err != nil {
		return fmt.Errorf("load runtime settings: %w", err)
	}
	if err := validateRuntimeState(state); err != nil {
		return err
	}
	if err := s.runtime.Replace(state); err != nil {
		return ErrInvalidData
	}
	return nil
}

func (s *Service) GetGlobal(ctx context.Context, principal auth.Principal) (Global, error) {
	if !principal.Has(auth.PermissionSettingsRead) {
		return Global{}, ErrForbidden
	}
	value, err := s.store.GetGlobalSettings(ctx)
	if err != nil {
		return Global{}, fmt.Errorf("get global settings: %w", err)
	}
	if !validGlobal(value) {
		return Global{}, ErrInvalidData
	}
	return cloneGlobal(value), nil
}

func (s *Service) GetGroup(ctx context.Context, principal auth.Principal, groupID string) (Group, error) {
	if !principal.Has(auth.PermissionSettingsRead) {
		return Group{}, ErrForbidden
	}
	if !validGroupID(groupID) {
		return Group{}, ErrInvalidInput
	}
	value, found, err := s.store.GetGroupSettings(ctx, groupID)
	if err != nil {
		return Group{}, fmt.Errorf("get group settings: %w", err)
	}
	if !found {
		return Group{}, ErrNotFound
	}
	if !validGroup(value) || value.GroupID != groupID {
		return Group{}, ErrInvalidData
	}
	return cloneGroup(value), nil
}

func (s *Service) UpdateGlobal(ctx context.Context, principal auth.Principal, revision uint64, patch GlobalPatch, request auth.MutationContext) (Global, error) {
	if !principal.Has(auth.PermissionSettingsWrite) {
		return Global{}, ErrForbidden
	}
	if revision == 0 || !validGlobalPatch(patch) || !validRequest(request) {
		return Global{}, ErrInvalidInput
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	value, err := s.store.UpdateGlobalSettings(ctx, UpdateGlobalMutation{
		Context: mutationContext(principal, request, s.now()), ExpectedRevision: revision, Patch: cloneGlobalPatch(patch),
	})
	if err != nil {
		return Global{}, fmt.Errorf("update global settings: %w", err)
	}
	if !validGlobal(value) || value.Version != revision+1 {
		return Global{}, ErrInvalidData
	}
	if err := s.runtime.ApplyGlobal(value); err != nil && !s.repairRuntime(ctx) {
		return Global{}, ErrInvalidData
	}
	s.publish("global", value.Version)
	return cloneGlobal(value), nil
}

func (s *Service) UpdateGroup(ctx context.Context, principal auth.Principal, groupID string, revision uint64, patch GroupPatch, request auth.MutationContext) (Group, error) {
	if !principal.Has(auth.PermissionSettingsWrite) {
		return Group{}, ErrForbidden
	}
	if !validGroupID(groupID) || !validGroupPatch(patch) || !validRequest(request) {
		return Group{}, ErrInvalidInput
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	value, err := s.store.UpdateGroupSettings(ctx, UpdateGroupMutation{
		Context: mutationContext(principal, request, s.now()), GroupID: groupID,
		ExpectedRevision: revision, Patch: cloneGroupPatch(patch),
	})
	if err != nil {
		return Group{}, fmt.Errorf("update group settings: %w", err)
	}
	if !validGroup(value) || value.GroupID != groupID || (value.Version != 0 && value.Version != revision+1) ||
		(value.Version == 0 && patchRequiresOverride(patch)) {
		return Group{}, ErrInvalidData
	}
	if err := s.runtime.ApplyGroup(value); err != nil && !s.repairRuntime(ctx) {
		return Group{}, ErrInvalidData
	}
	s.publish(groupID, value.Version)
	return cloneGroup(value), nil
}

func (s *Service) DeleteGroup(ctx context.Context, principal auth.Principal, groupID string, revision uint64, request auth.MutationContext) error {
	if !principal.Has(auth.PermissionSettingsWrite) {
		return ErrForbidden
	}
	if !validGroupID(groupID) || revision == 0 || !validRequest(request) {
		return ErrInvalidInput
	}
	s.mutation.Lock()
	defer s.mutation.Unlock()
	value, err := s.store.DeleteGroupSettings(ctx, DeleteGroupMutation{
		Context: mutationContext(principal, request, s.now()), GroupID: groupID, ExpectedRevision: revision,
	})
	if err != nil {
		return fmt.Errorf("delete group settings: %w", err)
	}
	if !validGroup(value) || value.GroupID != groupID || value.Version != 0 {
		return ErrInvalidData
	}
	if err := s.runtime.ApplyGroup(value); err != nil && !s.repairRuntime(ctx) {
		return ErrInvalidData
	}
	s.publish(groupID, 0)
	return nil
}

func (s *Service) repairRuntime(ctx context.Context) bool {
	repairContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimeRepairTimeout)
	defer cancel()
	state, err := s.store.LoadRuntimeSettings(repairContext)
	if err != nil || validateRuntimeState(state) != nil {
		return false
	}
	return s.runtime.Replace(state) == nil
}

func (s *Service) publish(id string, version uint64) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventSettingsUpdated, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceSettings, ID: id, Version: version}, Reason: "settings_updated",
	})
}

func mutationContext(principal auth.Principal, request auth.MutationContext, at time.Time) MutationContext {
	return MutationContext{Actor: principal, Request: request, OccurredAt: at.UTC()}
}

func validateRuntimeState(value RuntimeState) error {
	if !validGlobal(value.Global) {
		return ErrInvalidData
	}
	seen := make(map[string]struct{}, len(value.Groups))
	for _, group := range value.Groups {
		if !validGroupID(group.GroupID) || group.Version == 0 || overridesEmpty(group.Overrides) || !validOverrides(group.Overrides) {
			return ErrInvalidData
		}
		if _, duplicate := seen[group.GroupID]; duplicate {
			return ErrInvalidData
		}
		seen[group.GroupID] = struct{}{}
	}
	return nil
}

func validGlobal(value Global) bool {
	return value.Version >= 1 && !value.UpdatedAt.IsZero() && value.UpdatedAt.Location() == time.UTC &&
		validFeatures(value.Features) && validOptionalActor(value.UpdatedBy)
}

func validGroup(value Group) bool {
	if !validGroupID(value.GroupID) || value.GlobalVersion == 0 || !validFeatures(value.Effective) ||
		!validOverrides(value.Overrides) || !validOptionalActor(value.UpdatedBy) {
		return false
	}
	if value.Version == 0 {
		return value.UpdatedAt == nil && value.UpdatedBy == nil && overridesEmpty(value.Overrides)
	}
	return !overridesEmpty(value.Overrides) && value.UpdatedAt != nil && !value.UpdatedAt.IsZero() && value.UpdatedAt.Location() == time.UTC
}

func validFeatures(value Features) bool {
	return validTemplate(value.Welcome.MessageTemplate)
}

func validOverrides(value Overrides) bool {
	return validWelcomeOverride(value.Welcome)
}

func validWelcomeOverride(value *WelcomeOverride) bool {
	if value == nil {
		return true
	}
	if value.Enabled == nil && value.MessageTemplate == nil {
		return false
	}
	return value.MessageTemplate == nil || validTemplate(*value.MessageTemplate)
}

func validGlobalPatch(value GlobalPatch) bool {
	fields := []auth.Field[BasicPatch]{value.KeywordReply, value.AIQA, value.Quote, value.LinkCleaner, value.CustomCommand}
	set := false
	for _, field := range fields {
		if field.Set {
			set = true
			if !field.Value.Enabled.Set {
				return false
			}
		}
	}
	if value.Welcome.Set {
		set = true
		patch := value.Welcome.Value
		if !patch.Enabled.Set && !patch.MessageTemplate.Set {
			return false
		}
		if patch.MessageTemplate.Set && !validTemplate(patch.MessageTemplate.Value) {
			return false
		}
	}
	return set
}

func validGroupPatch(value GroupPatch) bool {
	fields := []struct {
		patch   OverrideFeaturePatch
		welcome bool
	}{
		{value.KeywordReply, false}, {value.AIQA, false}, {value.Quote, false}, {value.LinkCleaner, false},
		{value.Welcome, true}, {value.CustomCommand, false},
	}
	set := false
	for _, field := range fields {
		patch := field.patch
		if !patch.Set {
			continue
		}
		set = true
		if patch.Clear {
			if patch.Enabled.Set || patch.MessageTemplate.Set {
				return false
			}
			continue
		}
		if !patch.Enabled.Set && !patch.MessageTemplate.Set {
			return false
		}
		if !field.welcome && (!patch.Enabled.Set || patch.MessageTemplate.Set) {
			return false
		}
		if patch.MessageTemplate.Set && patch.MessageTemplate.Value != nil && !validTemplate(*patch.MessageTemplate.Value) {
			return false
		}
	}
	return set
}

func patchRequiresOverride(value GroupPatch) bool {
	for _, patch := range []OverrideFeaturePatch{
		value.KeywordReply, value.AIQA, value.Quote, value.LinkCleaner, value.Welcome, value.CustomCommand,
	} {
		if patch.Set && !patch.Clear &&
			((patch.Enabled.Set && patch.Enabled.Value != nil) || (patch.MessageTemplate.Set && patch.MessageTemplate.Value != nil)) {
			return true
		}
	}
	return false
}

func validTemplate(value string) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 2000 {
		return false
	}
	for _, match := range templateVariablePattern.FindAllStringSubmatch(value, -1) {
		if match[1] != "member_qq" && match[1] != "group_id" {
			return false
		}
	}
	withoutVariables := templateVariablePattern.ReplaceAllString(value, "")
	return !strings.Contains(withoutVariables, "{{") && !strings.Contains(withoutVariables, "}}")
}

func validOptionalActor(value *audit.Actor) bool {
	if value == nil {
		return true
	}
	if value.Type != audit.ActorAdminUser && value.Type != audit.ActorQQUser && value.Type != audit.ActorSystem {
		return false
	}
	return utf8.ValidString(value.DisplayName) && utf8.RuneCountInString(value.DisplayName) <= 100
}

func validRequest(value auth.MutationContext) bool {
	return validText(value.RequestID, 256) && validOptionalText(value.IPAddress, 64) && validOptionalText(value.UserAgent, 300)
}

func validGroupID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return strings.TrimLeft(value, "0") != ""
}

func validText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || validText(value, maximum)
}

func overridesEmpty(value Overrides) bool {
	return value.KeywordReply == nil && value.AIQA == nil && value.Quote == nil && value.LinkCleaner == nil &&
		value.Welcome == nil && value.CustomCommand == nil
}

func cloneFeatures(value Features) Features {
	return value
}

func cloneOverrides(value Overrides) Overrides {
	return Overrides{
		KeywordReply: cloneBasicOverride(value.KeywordReply), AIQA: cloneBasicOverride(value.AIQA), Quote: cloneBasicOverride(value.Quote),
		LinkCleaner: cloneBasicOverride(value.LinkCleaner), Welcome: cloneWelcomeOverride(value.Welcome),
		CustomCommand: cloneBasicOverride(value.CustomCommand),
	}
}

func cloneBasicOverride(value *BasicOverride) *BasicOverride {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneWelcomeOverride(value *WelcomeOverride) *WelcomeOverride {
	if value == nil {
		return nil
	}
	return &WelcomeOverride{Enabled: cloneBool(value.Enabled), MessageTemplate: cloneString(value.MessageTemplate)}
}

func cloneGlobal(value Global) Global {
	value.Features = cloneFeatures(value.Features)
	value.UpdatedAt = value.UpdatedAt.UTC()
	value.UpdatedBy = cloneActor(value.UpdatedBy)
	return value
}

func cloneGroup(value Group) Group {
	value.Effective = cloneFeatures(value.Effective)
	value.Overrides = cloneOverrides(value.Overrides)
	if value.UpdatedAt != nil {
		updated := value.UpdatedAt.UTC()
		value.UpdatedAt = &updated
	}
	value.UpdatedBy = cloneActor(value.UpdatedBy)
	return value
}

func cloneActor(value *audit.Actor) *audit.Actor {
	if value == nil {
		return nil
	}
	copy := *value
	copy.UserID = cloneString(value.UserID)
	copy.QQUserID = cloneString(value.QQUserID)
	return &copy
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneGlobalPatch(value GlobalPatch) GlobalPatch {
	return value
}

func cloneGroupPatch(value GroupPatch) GroupPatch {
	for _, patch := range []*OverrideFeaturePatch{
		&value.KeywordReply, &value.AIQA, &value.Quote, &value.LinkCleaner, &value.Welcome, &value.CustomCommand,
	} {
		patch.Enabled.Value = cloneBool(patch.Enabled.Value)
		patch.MessageTemplate.Value = cloneString(patch.MessageTemplate.Value)
	}
	return value
}
