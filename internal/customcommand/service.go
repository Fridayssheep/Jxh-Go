package customcommand

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
)

const defaultExecutionTimeout = 30 * time.Second

type Options struct {
	Store                 Store
	Gateway               Gateway
	Registry              *Registry
	Now                   func() time.Time
	BuiltinNames          []string
	ExecutionTimeout      time.Duration
	ArgumentSummaryKey    []byte
	Events                EventPublisher
	WorkerContext         context.Context
	PersistenceRetryDelay time.Duration
}

type Service struct {
	store            Store
	gateway          Gateway
	registry         *Registry
	now              func() time.Time
	executionTimeout time.Duration
	summaryKey       []byte
	events           EventPublisher
	workerCtx        context.Context
	cancel           context.CancelFunc
	retryDelay       time.Duration
	lifecycleMu      sync.Mutex
	closed           bool
	wait             sync.WaitGroup
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Now == nil || (len(options.ArgumentSummaryKey) > 0 && len(options.ArgumentSummaryKey) < 32) {
		return nil, ErrInvalidInput
	}
	registry := options.Registry
	if registry == nil {
		var err error
		registry, err = NewRegistry(options.BuiltinNames)
		if err != nil {
			return nil, err
		}
	}
	timeout := options.ExecutionTimeout
	if timeout <= 0 {
		timeout = defaultExecutionTimeout
	}
	if timeout > defaultExecutionTimeout {
		return nil, ErrInvalidInput
	}
	if options.PersistenceRetryDelay <= 0 {
		options.PersistenceRetryDelay = time.Second
	}
	workerContext := options.WorkerContext
	if workerContext == nil {
		workerContext = context.Background()
	}
	workerContext, cancel := context.WithCancel(workerContext)
	return &Service{
		store: options.Store, gateway: options.Gateway, registry: registry, now: options.Now,
		executionTimeout: timeout, summaryKey: append([]byte(nil), options.ArgumentSummaryKey...), events: options.Events,
		workerCtx: workerContext, cancel: cancel, retryDelay: options.PersistenceRetryDelay,
	}, nil
}

func (s *Service) scheduleRunPersistenceRetry(run Run) {
	run = cloneRun(run)
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return
	}
	s.wait.Add(1)
	s.lifecycleMu.Unlock()
	go func() {
		defer s.wait.Done()
		for {
			timer := time.NewTimer(s.retryDelay)
			select {
			case <-s.workerCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			attemptContext, cancel := context.WithTimeout(s.workerCtx, s.executionTimeout)
			stored, err := s.store.RecordCommandRun(attemptContext, cloneRun(run))
			cancel()
			if err != nil {
				continue
			}
			s.publishRunCompleted(stored)
			return
		}
	}()
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.lifecycleMu.Unlock()
	s.wait.Wait()
}

func (s *Service) Registry() *Registry {
	return s.registry
}

// Probe performs the immutable registry lookup used by the message hot path.
// Execute repeats the lookup before applying any side effect so a concurrent
// command update cannot execute a stale definition.
func (s *Service) Probe(message, groupID string) (TriggerPermission, bool) {
	command, matched := s.registry.Match(message, groupID)
	if !matched {
		return "", false
	}
	return command.TriggerPermission, true
}

// LoadRegistry replaces the runtime snapshot with every stored active
// command. It is intended for application startup before message delivery.
func (s *Service) LoadRegistry(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	const pageSize = 100
	commands := make([]Command, 0)
	cursor := ""
	seenCursors := make(map[string]struct{})
	for {
		page, err := s.store.ListCommands(ctx, ListQuery{Status: StatusActive, Limit: pageSize, Cursor: cursor})
		if err != nil {
			return fmt.Errorf("load custom command registry: %w", err)
		}
		if len(page.Items) > pageSize || (page.HasMore && !validIdentifier(page.NextCursor)) {
			return fmt.Errorf("load custom command registry: invalid store page: %w", ErrInvalidInput)
		}
		commands = append(commands, page.Items...)
		if !page.HasMore {
			break
		}
		if page.NextCursor == cursor {
			return fmt.Errorf("load custom command registry: repeated cursor: %w", ErrInvalidInput)
		}
		if _, duplicate := seenCursors[page.NextCursor]; duplicate {
			return fmt.Errorf("load custom command registry: cursor cycle: %w", ErrInvalidInput)
		}
		seenCursors[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	if err := s.registry.Replace(commands); err != nil {
		return fmt.Errorf("load custom command registry: %w", err)
	}
	return nil
}

func (s *Service) Create(ctx context.Context, principal auth.Principal, definition Definition, request auth.MutationContext) (Command, error) {
	if !principal.Has(auth.PermissionCommandsWrite) {
		return Command{}, ErrForbidden
	}
	if s.registry.Conflicts(definition.Name, "") {
		return Command{}, ErrConflict
	}
	if len(ValidateDefinition(definition)) != 0 || !validRequestContext(request) {
		return Command{}, ErrInvalidInput
	}
	if principal.Role != auth.RoleSuperAdmin && hasGroupSend(definition.Actions) {
		return Command{}, ErrForbidden
	}
	exists, err := s.store.CommandNameExists(ctx, definition.Name, "")
	if err != nil {
		return Command{}, fmt.Errorf("check custom command name: %w", err)
	}
	if exists {
		return Command{}, ErrConflict
	}
	command, err := s.store.CreateCommand(ctx, CreateMutation{
		Context: mutationContext(principal, request, s.now()), Definition: cloneDefinition(definition), Status: StatusDraft, Enabled: false,
	})
	if err != nil {
		return Command{}, fmt.Errorf("create custom command: %w", err)
	}
	if command.Enabled || command.Status != StatusDraft {
		return Command{}, fmt.Errorf("create custom command: invalid store result: %w", ErrInvalidInput)
	}
	if err := s.registry.Upsert(command); err != nil {
		return Command{}, fmt.Errorf("publish custom command: %w", err)
	}
	s.publishCommandUpdated(command, "created")
	return cloneCommand(command), nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, id string) (Command, error) {
	if !principal.Has(auth.PermissionCommandsRead) {
		return Command{}, ErrForbidden
	}
	if !validIdentifier(id) {
		return Command{}, ErrInvalidInput
	}
	command, found, err := s.store.GetCommand(ctx, id)
	if err != nil {
		return Command{}, fmt.Errorf("get custom command: %w", err)
	}
	if !found {
		return Command{}, ErrNotFound
	}
	return cloneCommand(command), nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal, query ListQuery) (Page[Command], error) {
	if !principal.Has(auth.PermissionCommandsRead) {
		return Page[Command]{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validListQuery(query) {
		return Page[Command]{}, ErrInvalidInput
	}
	page, err := s.store.ListCommands(ctx, query)
	if err != nil {
		return Page[Command]{}, fmt.Errorf("list custom commands: %w", err)
	}
	page.Items = cloneCommands(page.Items)
	return page, nil
}

func (s *Service) Update(ctx context.Context, principal auth.Principal, id string, revision uint64, patch Patch, request auth.MutationContext) (Command, error) {
	if !principal.Has(auth.PermissionCommandsWrite) {
		return Command{}, ErrForbidden
	}
	if !validIdentifier(id) || revision == 0 || !patchSet(patch) || !validRequestContext(request) {
		return Command{}, ErrInvalidInput
	}
	current, found, err := s.store.GetCommand(ctx, id)
	if err != nil {
		return Command{}, fmt.Errorf("get custom command for update: %w", err)
	}
	if !found {
		return Command{}, ErrNotFound
	}
	if current.Status == StatusArchived || current.Version != revision {
		return Command{}, ErrConflict
	}
	candidate := applyPatch(current, patch)
	if len(ValidateDefinition(candidate.Definition)) != 0 {
		return Command{}, ErrInvalidInput
	}
	if principal.Role != auth.RoleSuperAdmin && (hasGroupSend(current.Actions) || hasGroupSend(candidate.Actions)) && !enabledOnlyPatch(patch) {
		return Command{}, ErrForbidden
	}
	if candidate.Name != current.Name {
		if s.registry.Conflicts(candidate.Name, id) {
			return Command{}, ErrConflict
		}
		exists, err := s.store.CommandNameExists(ctx, candidate.Name, id)
		if err != nil {
			return Command{}, fmt.Errorf("check custom command name: %w", err)
		}
		if exists {
			return Command{}, ErrConflict
		}
	}
	command, err := s.store.UpdateCommand(ctx, UpdateMutation{
		Context: mutationContext(principal, request, s.now()), CommandID: id, ExpectedRevision: revision, Patch: clonePatch(patch),
	})
	if err != nil {
		return Command{}, fmt.Errorf("update custom command: %w", err)
	}
	if err := s.registry.Upsert(command); err != nil {
		return Command{}, fmt.Errorf("publish custom command: %w", err)
	}
	s.publishCommandUpdated(command, "updated")
	return cloneCommand(command), nil
}

func (s *Service) Archive(ctx context.Context, principal auth.Principal, id string, revision uint64, request auth.MutationContext) error {
	if !principal.Has(auth.PermissionCommandsWrite) {
		return ErrForbidden
	}
	if !validIdentifier(id) || revision == 0 || !validRequestContext(request) {
		return ErrInvalidInput
	}
	if err := s.store.ArchiveCommand(ctx, ArchiveMutation{
		Context: mutationContext(principal, request, s.now()), CommandID: id, ExpectedRevision: revision,
	}); err != nil {
		return fmt.Errorf("archive custom command: %w", err)
	}
	s.registry.Remove(id)
	s.publishCommandUpdated(Command{ID: id, Version: revision}, "archived")
	return nil
}

func (s *Service) publishCommandUpdated(command Command, reason string) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventCommandUpdated, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceCommand, ID: command.ID, Version: command.Version},
		Reason:   reason,
	})
}

func (s *Service) publishRunCompleted(run Run) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventCommandRunCompleted, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceCommand, ID: run.CommandID},
		Reason:   string(run.Result),
	})
}

func (s *Service) ValidateDraft(_ context.Context, principal auth.Principal, definition Definition, sample ValidationSample) (ValidationResult, error) {
	if !principal.Has(auth.PermissionCommandsWrite) {
		return ValidationResult{}, ErrForbidden
	}
	return s.preview(definition, sample, ""), nil
}

func (s *Service) ValidateStored(ctx context.Context, principal auth.Principal, id string, sample ValidationSample) (ValidationResult, error) {
	if !principal.Has(auth.PermissionCommandsWrite) {
		return ValidationResult{}, ErrForbidden
	}
	if !validIdentifier(id) {
		return ValidationResult{}, ErrInvalidInput
	}
	command, found, err := s.store.GetCommand(ctx, id)
	if err != nil {
		return ValidationResult{}, fmt.Errorf("get custom command for validation: %w", err)
	}
	if !found {
		return ValidationResult{}, ErrNotFound
	}
	return s.preview(command.Definition, sample, command.ID), nil
}

func (s *Service) preview(definition Definition, sample ValidationSample, excludingCommandID string) ValidationResult {
	result := ValidationResult{Issues: []ValidationIssue{}, Warnings: []ValidationIssue{}, ParsedArguments: []ParsedArgument{}, RenderedActions: []RenderedAction{}}
	result.Issues = append(result.Issues, ValidateDefinition(definition)...)
	result.Issues = append(result.Issues, validateSample(sample)...)
	if s.registry.Conflicts(definition.Name, excludingCommandID) {
		result.Issues = append(result.Issues, ValidationIssue{Path: "definition.name", Code: "command_name_conflict", Message: "command name conflicts with an active command"})
	}
	if len(result.Issues) != 0 {
		return result
	}
	if !inScope(definition.Scope, sample.GroupID) {
		result.Issues = append(result.Issues, ValidationIssue{Path: "sample.group_id", Code: "outside_scope", Message: "sample group is outside the command scope"})
		return result
	}
	if definition.TriggerPermission == TriggerGroupAdmin && sample.SenderRole == SenderMember {
		result.Issues = append(result.Issues, ValidationIssue{Path: "sample.sender_role", Code: "trigger_forbidden", Message: "sample sender cannot trigger this command"})
		return result
	}
	if definition.TriggerPermission == TriggerMaintenanceAllowlist {
		result.Warnings = append(result.Warnings, ValidationIssue{Path: "sample.sender_qq", Code: "allowlist_not_checked", Message: "maintenance allowlist membership is not checked in preview"})
	}
	values, parsed, err := parseMessage(definition, sample.Message)
	if err != nil || !memberArgumentsAllowed(definition.Parameters, values, sample.SenderQQ) {
		result.Issues = append(result.Issues, ValidationIssue{Path: "sample.message", Code: "parse_error", Message: "sample message does not match the command definition"})
		return result
	}
	result.ParsedArguments = parsed
	result.RenderedActions = renderActions(definition, sample, values)
	result.Valid = true
	return result
}

func (s *Service) ListRuns(ctx context.Context, principal auth.Principal, query RunListQuery) (Page[Run], error) {
	if !principal.Has(auth.PermissionCommandsRead) {
		return Page[Run]{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validRunListQuery(query) {
		return Page[Run]{}, ErrInvalidInput
	}
	if _, found, err := s.store.GetCommand(ctx, query.CommandID); err != nil {
		return Page[Run]{}, fmt.Errorf("check custom command: %w", err)
	} else if !found {
		return Page[Run]{}, ErrNotFound
	}
	page, err := s.store.ListCommandRuns(ctx, query)
	if err != nil {
		return Page[Run]{}, fmt.Errorf("list custom command runs: %w", err)
	}
	page.Items = cloneRuns(page.Items)
	return page, nil
}

func validateSample(sample ValidationSample) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	if !validIdentifier(sample.GroupID) {
		issues = append(issues, ValidationIssue{Path: "sample.group_id", Code: "invalid_group_id", Message: "sample group ID is invalid"})
	}
	if !validQQ(sample.SenderQQ) {
		issues = append(issues, ValidationIssue{Path: "sample.sender_qq", Code: "invalid_sender", Message: "sample sender QQ is invalid"})
	}
	if sample.SenderRole != SenderOwner && sample.SenderRole != SenderAdmin && sample.SenderRole != SenderMember {
		issues = append(issues, ValidationIssue{Path: "sample.sender_role", Code: "invalid_sender_role", Message: "sample sender role is invalid"})
	}
	if !validRuneText(sample.Message, 1, maxMessageRunes) {
		issues = append(issues, ValidationIssue{Path: "sample.message", Code: "invalid_message", Message: "sample message must contain 1 to 2000 characters"})
	}
	return issues
}

func mutationContext(principal auth.Principal, request auth.MutationContext, at time.Time) MutationContext {
	return MutationContext{Actor: principal, Request: request, OccurredAt: at.UTC()}
}

func validRequestContext(request auth.MutationContext) bool {
	return validRuneText(request.RequestID, 1, 256) && validRuneText(request.IPAddress, 0, 64) && validRuneText(request.UserAgent, 0, 300)
}

func validListQuery(query ListQuery) bool {
	return query.Limit >= 1 && query.Limit <= 100 && validRuneText(query.Query, 0, 100) &&
		(query.Status == "" || validStatus(query.Status)) && (query.ScopeType == "" || query.ScopeType == ScopeGlobal || query.ScopeType == ScopeGroups) &&
		(query.GroupID == "" || validIdentifier(query.GroupID)) && (query.ActionType == "" || validActionType(query.ActionType)) &&
		(query.TriggerPermission == "" || validTriggerPermission(query.TriggerPermission)) && (query.Cursor == "" || validIdentifier(query.Cursor))
}

func validRunListQuery(query RunListQuery) bool {
	return validIdentifier(query.CommandID) && query.Limit >= 1 && query.Limit <= 100 &&
		(query.Result == "" || validRunResult(query.Result)) && (query.From == nil || query.From.Location() == time.UTC) &&
		(query.To == nil || query.To.Location() == time.UTC) && (query.From == nil || query.To == nil || !query.From.After(*query.To)) &&
		(query.Cursor == "" || validIdentifier(query.Cursor))
}

func validStatus(value Status) bool {
	return value == StatusDraft || value == StatusActive || value == StatusDisabled || value == StatusArchived
}

func validActionType(value ActionType) bool {
	return value == ActionReplyText || value == ActionMention || value == ActionMuteMember || value == ActionSendGroupText
}

func validTriggerPermission(value TriggerPermission) bool {
	return value == TriggerEveryone || value == TriggerGroupAdmin || value == TriggerMaintenanceAllowlist
}

func validRunResult(value RunResult) bool {
	return value == RunSuccess || value == RunDenied || value == RunParseError || value == RunFailed || value == RunPartial || value == RunUnknown
}

func hasGroupSend(actions []Action) bool {
	for _, action := range actions {
		if action.Type == ActionSendGroupText {
			return true
		}
	}
	return false
}

func enabledOnlyPatch(patch Patch) bool {
	return patch.Enabled.Set && !patch.Name.Set && !patch.DisplayName.Set && !patch.Description.Set && !patch.Scope.Set &&
		!patch.TriggerPermission.Set && !patch.Parameters.Set && !patch.Actions.Set
}

func triggerAllowed(permission TriggerPermission, role SenderRole, maintenanceAllowlisted bool) bool {
	switch permission {
	case TriggerEveryone:
		return true
	case TriggerGroupAdmin:
		return role == SenderOwner || role == SenderAdmin
	case TriggerMaintenanceAllowlist:
		return maintenanceAllowlisted
	default:
		return false
	}
}

func safeCode(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func safeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}
