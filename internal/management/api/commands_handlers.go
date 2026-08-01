package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/management/auth"
)

type CommandOperations interface {
	Create(context.Context, auth.Principal, customcommand.Definition, auth.MutationContext) (customcommand.Command, error)
	Get(context.Context, auth.Principal, string) (customcommand.Command, error)
	List(context.Context, auth.Principal, customcommand.ListQuery) (customcommand.Page[customcommand.Command], error)
	Update(context.Context, auth.Principal, string, uint64, customcommand.Patch, auth.MutationContext) (customcommand.Command, error)
	Delete(context.Context, auth.Principal, string, uint64, auth.MutationContext) error
	ValidateDraft(context.Context, auth.Principal, customcommand.Definition, customcommand.ValidationSample) (customcommand.ValidationResult, error)
	ValidateStored(context.Context, auth.Principal, string, customcommand.ValidationSample) (customcommand.ValidationResult, error)
	ListRuns(context.Context, auth.Principal, customcommand.RunListQuery) (customcommand.Page[customcommand.Run], error)
}

type CommandHandlers struct {
	service CommandOperations
}

func NewCommandHandlers(service CommandOperations) (*CommandHandlers, error) {
	if service == nil {
		return nil, fmt.Errorf("custom command service is required")
	}
	return &CommandHandlers{service: service}, nil
}

func (h *CommandHandlers) Register(router *Router) error {
	if router == nil {
		return fmt.Errorf("admin router is required")
	}
	routes := []struct {
		method  string
		pattern string
		options RouteOptions
		handler http.HandlerFunc
	}{
		{http.MethodGet, "/api/admin/v1/commands", RouteOptions{Permission: auth.PermissionCommandsRead}, h.list},
		{http.MethodPost, "/api/admin/v1/commands", mutationRoute(auth.PermissionCommandsWrite), h.create},
		{http.MethodPost, "/api/admin/v1/commands/validate", mutationRoute(auth.PermissionCommandsWrite), h.validateDraft},
		{http.MethodGet, "/api/admin/v1/commands/{command_id}", RouteOptions{Permission: auth.PermissionCommandsRead}, h.get},
		{http.MethodPatch, "/api/admin/v1/commands/{command_id}", mutationRoute(auth.PermissionCommandsWrite), h.update},
		{http.MethodDelete, "/api/admin/v1/commands/{command_id}", mutationRoute(auth.PermissionCommandsWrite), h.delete},
		{http.MethodPost, "/api/admin/v1/commands/{command_id}/validate", mutationRoute(auth.PermissionCommandsWrite), h.validateStored},
		{http.MethodGet, "/api/admin/v1/commands/{command_id}/runs", RouteOptions{Permission: auth.PermissionCommandsRead}, h.listRuns},
	}
	for _, route := range routes {
		if err := router.HandleFunc(route.method, route.pattern, route.options, route.handler); err != nil {
			return err
		}
	}
	return nil
}

func (h *CommandHandlers) create(w http.ResponseWriter, r *http.Request) {
	var body commandDefinitionRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	definition, ok := body.definition()
	if !ok {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "required custom command fields are missing", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	command, err := h.service.Create(r.Context(), principalFromAuth(identity), definition, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, command.Version)
	writeJSON(w, http.StatusCreated, mapCommand(command))
}

func (h *CommandHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "command_id")
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	command, err := h.service.Get(r.Context(), principalFromAuth(identity), id)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, command.Version)
	writeJSON(w, http.StatusOK, mapCommand(command))
}

func (h *CommandHandlers) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseCommandListQuery(r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "custom command query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.List(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]commandDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapCommand(page.Items[index])
	}
	writeJSON(w, http.StatusOK, commandListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

type commandPatchField[T any] struct {
	Set   bool
	Value T
}

func (f *commandPatchField[T]) UnmarshalJSON(data []byte) error {
	f.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("null is not allowed")
	}
	return decodeStrictJSONBytes(data, &f.Value)
}

type commandPatchRequest struct {
	Name              commandPatchField[string]                          `json:"name"`
	DisplayName       commandPatchField[string]                          `json:"display_name"`
	Description       commandPatchField[string]                          `json:"description"`
	Scope             commandPatchField[commandScopeInput]               `json:"scope"`
	TriggerPermission commandPatchField[customcommand.TriggerPermission] `json:"trigger_permission"`
	Parameters        commandPatchField[[]commandParameterInput]         `json:"parameters"`
	Actions           commandPatchField[[]commandActionInput]            `json:"actions"`
	Enabled           commandPatchField[bool]                            `json:"enabled"`
}

func (h *CommandHandlers) update(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "command_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	var body commandPatchRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	patch, ok := body.patch()
	if !ok {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "custom command patch is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	command, err := h.service.Update(r.Context(), principalFromAuth(identity), id, revision, patch, mutationContextFromRequest(r))
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	setRevisionETag(w, command.Version)
	writeJSON(w, http.StatusOK, mapCommand(command))
}

func (h *CommandHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "command_id")
	if !ok {
		return
	}
	revision, ok := requiredRevision(w, r)
	if !ok {
		return
	}
	identity, _ := AuthFromContext(r.Context())
	if err := h.service.Delete(r.Context(), principalFromAuth(identity), id, revision, mutationContextFromRequest(r)); err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type commandDraftValidationRequest struct {
	Definition *commandDefinitionRequest `json:"definition"`
	Sample     *commandValidationSample  `json:"sample"`
}

func (h *CommandHandlers) validateDraft(w http.ResponseWriter, r *http.Request) {
	var body commandDraftValidationRequest
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	if body.Definition == nil || body.Sample == nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "command validation fields are missing", nil, false)
		return
	}
	definition, definitionOK := body.Definition.definition()
	sample, sampleOK := body.Sample.sample()
	if !definitionOK || !sampleOK {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "command validation input is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.ValidateDraft(r.Context(), principalFromAuth(identity), definition, sample)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapCommandValidation(result))
}

func (h *CommandHandlers) validateStored(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "command_id")
	if !ok {
		return
	}
	var body commandValidationSample
	if !decodeRequestJSON(w, r, &body) {
		return
	}
	sample, ok := body.sample()
	if !ok {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "command validation sample is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	result, err := h.service.ValidateStored(r.Context(), principalFromAuth(identity), id, sample)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, mapCommandValidation(result))
}

func (h *CommandHandlers) listRuns(w http.ResponseWriter, r *http.Request) {
	id, ok := pathIdentifier(w, r, "command_id")
	if !ok {
		return
	}
	query, err := parseCommandRunListQuery(id, r.URL.Query())
	if err != nil {
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "custom command run query is invalid", nil, false)
		return
	}
	identity, _ := AuthFromContext(r.Context())
	page, err := h.service.ListRuns(r.Context(), principalFromAuth(identity), query)
	if err != nil {
		h.writeServiceError(w, r, err)
		return
	}
	items := make([]commandRunDTO, len(page.Items))
	for index := range page.Items {
		items[index] = mapCommandRun(page.Items[index])
	}
	writeJSON(w, http.StatusOK, commandRunListDTO{Items: items, NextCursor: nullableString(page.NextCursor), HasMore: page.HasMore})
}

func (h *CommandHandlers) writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, customcommand.ErrForbidden):
		writeAPIError(w, r, http.StatusForbidden, CodeForbidden, "custom command operation is forbidden", nil, false)
	case errors.Is(err, customcommand.ErrInvalidInput):
		writeAPIError(w, r, http.StatusBadRequest, CodeBadRequest, "custom command input is invalid", nil, false)
	case errors.Is(err, customcommand.ErrNotFound):
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "custom command does not exist", nil, false)
	case errors.Is(err, customcommand.ErrConflict):
		writeAPIError(w, r, http.StatusConflict, "resource_version_conflict", "custom command conflicts with another resource", nil, false)
	case errors.Is(err, customcommand.ErrGatewayUnavailable):
		w.Header().Set("Retry-After", "3")
		writeAPIError(w, r, http.StatusServiceUnavailable, "dependency_unavailable", "command action gateway is unavailable", nil, true)
	default:
		writeAPIError(w, r, http.StatusInternalServerError, CodeInternal, "internal server error", nil, false)
	}
}

type commandScopeInput struct {
	Type     *customcommand.ScopeType `json:"type"`
	GroupIDs *[]string                `json:"group_ids"`
}

func (s commandScopeInput) scope() (customcommand.Scope, bool) {
	if s.Type == nil || s.GroupIDs == nil {
		return customcommand.Scope{}, false
	}
	return customcommand.Scope{Type: *s.Type, GroupIDs: append([]string(nil), (*s.GroupIDs)...)}, true
}

type commandDefinitionRequest struct {
	Name              *string                          `json:"name"`
	DisplayName       *string                          `json:"display_name"`
	Description       *string                          `json:"description"`
	Scope             *commandScopeInput               `json:"scope"`
	TriggerPermission *customcommand.TriggerPermission `json:"trigger_permission"`
	Parameters        *[]commandParameterInput         `json:"parameters"`
	Actions           *[]commandActionInput            `json:"actions"`
}

func (r commandDefinitionRequest) definition() (customcommand.Definition, bool) {
	if r.Name == nil || r.DisplayName == nil || r.Description == nil || r.Scope == nil || r.TriggerPermission == nil ||
		r.Parameters == nil || r.Actions == nil {
		return customcommand.Definition{}, false
	}
	scope, ok := r.Scope.scope()
	if !ok {
		return customcommand.Definition{}, false
	}
	parameters := make([]customcommand.Parameter, len(*r.Parameters))
	for index := range *r.Parameters {
		if !(*r.Parameters)[index].set {
			return customcommand.Definition{}, false
		}
		parameters[index] = (*r.Parameters)[index].value
	}
	actions := make([]customcommand.Action, len(*r.Actions))
	for index := range *r.Actions {
		if !(*r.Actions)[index].set {
			return customcommand.Definition{}, false
		}
		actions[index] = (*r.Actions)[index].value
	}
	return customcommand.Definition{
		Name: *r.Name, DisplayName: *r.DisplayName, Description: *r.Description, Scope: scope,
		TriggerPermission: *r.TriggerPermission, Parameters: parameters, Actions: actions,
	}, true
}

func (r commandPatchRequest) patch() (customcommand.Patch, bool) {
	patch := customcommand.Patch{
		Name:              auth.Field[string]{Set: r.Name.Set, Value: r.Name.Value},
		DisplayName:       auth.Field[string]{Set: r.DisplayName.Set, Value: r.DisplayName.Value},
		Description:       auth.Field[string]{Set: r.Description.Set, Value: r.Description.Value},
		TriggerPermission: auth.Field[customcommand.TriggerPermission]{Set: r.TriggerPermission.Set, Value: r.TriggerPermission.Value},
		Enabled:           auth.Field[bool]{Set: r.Enabled.Set, Value: r.Enabled.Value},
	}
	if r.Scope.Set {
		scope, ok := r.Scope.Value.scope()
		if !ok {
			return customcommand.Patch{}, false
		}
		patch.Scope = auth.Field[customcommand.Scope]{Set: true, Value: scope}
	}
	if r.Parameters.Set {
		parameters := make([]customcommand.Parameter, len(r.Parameters.Value))
		for index := range r.Parameters.Value {
			if !r.Parameters.Value[index].set {
				return customcommand.Patch{}, false
			}
			parameters[index] = r.Parameters.Value[index].value
		}
		patch.Parameters = auth.Field[[]customcommand.Parameter]{Set: true, Value: parameters}
	}
	if r.Actions.Set {
		actions := make([]customcommand.Action, len(r.Actions.Value))
		for index := range r.Actions.Value {
			if !r.Actions.Value[index].set {
				return customcommand.Patch{}, false
			}
			actions[index] = r.Actions.Value[index].value
		}
		patch.Actions = auth.Field[[]customcommand.Action]{Set: true, Value: actions}
	}
	return patch, r.Name.Set || r.DisplayName.Set || r.Description.Set || r.Scope.Set || r.TriggerPermission.Set ||
		r.Parameters.Set || r.Actions.Set || r.Enabled.Set
}

type commandParameterInput struct {
	set   bool
	value customcommand.Parameter
}

func (p *commandParameterInput) UnmarshalJSON(data []byte) error {
	var kind struct {
		Type customcommand.ParameterType `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	type base struct {
		Name        *string                     `json:"name"`
		DisplayName *string                     `json:"display_name"`
		Type        customcommand.ParameterType `json:"type"`
		Required    *bool                       `json:"required"`
	}
	var value customcommand.Parameter
	switch kind.Type {
	case customcommand.ParameterText:
		var input struct {
			base
			MinLength *int `json:"min_length"`
			MaxLength *int `json:"max_length"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Name == nil || input.DisplayName == nil || input.Required == nil || input.MinLength == nil || input.MaxLength == nil {
			return fmt.Errorf("invalid text parameter")
		}
		value = customcommand.Parameter{Name: *input.Name, DisplayName: *input.DisplayName, Type: input.Type, Required: *input.Required, MinLength: *input.MinLength, MaxLength: *input.MaxLength}
	case customcommand.ParameterInteger:
		var input struct {
			base
			Minimum *int64 `json:"minimum"`
			Maximum *int64 `json:"maximum"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Name == nil || input.DisplayName == nil || input.Required == nil || input.Minimum == nil || input.Maximum == nil {
			return fmt.Errorf("invalid integer parameter")
		}
		value = customcommand.Parameter{Name: *input.Name, DisplayName: *input.DisplayName, Type: input.Type, Required: *input.Required, Minimum: *input.Minimum, Maximum: *input.Maximum}
	case customcommand.ParameterDuration:
		var input struct {
			base
			Minimum *int64 `json:"minimum_seconds"`
			Maximum *int64 `json:"maximum_seconds"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Name == nil || input.DisplayName == nil || input.Required == nil || input.Minimum == nil || input.Maximum == nil {
			return fmt.Errorf("invalid duration parameter")
		}
		value = customcommand.Parameter{Name: *input.Name, DisplayName: *input.DisplayName, Type: input.Type, Required: *input.Required, MinimumSeconds: *input.Minimum, MaximumSeconds: *input.Maximum}
	case customcommand.ParameterMember:
		var input struct {
			base
			AllowTriggerer *bool `json:"allow_triggerer"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Name == nil || input.DisplayName == nil || input.Required == nil || input.AllowTriggerer == nil {
			return fmt.Errorf("invalid member parameter")
		}
		value = customcommand.Parameter{Name: *input.Name, DisplayName: *input.DisplayName, Type: input.Type, Required: *input.Required, AllowTriggerer: *input.AllowTriggerer}
	case customcommand.ParameterFixedOption:
		var input struct {
			base
			Options *[]commandFixedOptionInput `json:"options"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Name == nil || input.DisplayName == nil || input.Required == nil || input.Options == nil {
			return fmt.Errorf("invalid fixed option parameter")
		}
		options := make([]customcommand.FixedOption, len(*input.Options))
		for index, option := range *input.Options {
			if option.Value == nil || option.Label == nil {
				return fmt.Errorf("invalid fixed option")
			}
			options[index] = customcommand.FixedOption{Value: *option.Value, Label: *option.Label}
		}
		value = customcommand.Parameter{Name: *input.Name, DisplayName: *input.DisplayName, Type: input.Type, Required: *input.Required, Options: options}
	default:
		return fmt.Errorf("unknown command parameter type")
	}
	p.set, p.value = true, value
	return nil
}

type commandFixedOptionInput struct {
	Value *string `json:"value"`
	Label *string `json:"label"`
}

type commandActionInput struct {
	set   bool
	value customcommand.Action
}

type nullableCommandString struct {
	Set   bool
	Value *string
}

func (s *nullableCommandString) UnmarshalJSON(data []byte) error {
	s.Set = true
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		s.Value = nil
		return nil
	}
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	s.Value = &value
	return nil
}

func (a *commandActionInput) UnmarshalJSON(data []byte) error {
	var kind struct {
		Type customcommand.ActionType `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	var value customcommand.Action
	switch kind.Type {
	case customcommand.ActionReplyText:
		var input struct {
			Type     customcommand.ActionType `json:"type"`
			Template *string                  `json:"template"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Template == nil {
			return fmt.Errorf("invalid reply action")
		}
		value = customcommand.Action{Type: input.Type, Template: *input.Template}
	case customcommand.ActionMention:
		var input struct {
			Type            customcommand.ActionType     `json:"type"`
			Target          *customcommand.MentionTarget `json:"target"`
			MemberParameter nullableCommandString        `json:"member_parameter"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Target == nil || !input.MemberParameter.Set {
			return fmt.Errorf("invalid mention action")
		}
		memberParameter := ""
		if input.MemberParameter.Value != nil {
			memberParameter = *input.MemberParameter.Value
		}
		value = customcommand.Action{Type: input.Type, Target: *input.Target, MemberParameter: memberParameter}
	case customcommand.ActionMuteMember:
		var input struct {
			Type            customcommand.ActionType `json:"type"`
			MemberParameter *string                  `json:"member_parameter"`
			Duration        *commandDurationInput    `json:"duration"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.MemberParameter == nil || input.Duration == nil || !input.Duration.set {
			return fmt.Errorf("invalid mute action")
		}
		value = customcommand.Action{Type: input.Type, MemberParameter: *input.MemberParameter, Duration: input.Duration.value}
	case customcommand.ActionSendGroupText:
		var input struct {
			Type           customcommand.ActionType `json:"type"`
			TargetGroupIDs *[]string                `json:"target_group_ids"`
			Template       *string                  `json:"template"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.TargetGroupIDs == nil || input.Template == nil {
			return fmt.Errorf("invalid group send action")
		}
		value = customcommand.Action{Type: input.Type, TargetGroupIDs: append([]string(nil), (*input.TargetGroupIDs)...), Template: *input.Template}
	default:
		return fmt.Errorf("unknown command action type")
	}
	a.set, a.value = true, value
	return nil
}

type commandDurationInput struct {
	set   bool
	value customcommand.DurationSource
}

func (d *commandDurationInput) UnmarshalJSON(data []byte) error {
	var kind struct {
		Type customcommand.DurationSourceType `json:"type"`
	}
	if err := json.Unmarshal(data, &kind); err != nil {
		return err
	}
	switch kind.Type {
	case customcommand.DurationFixed:
		var input struct {
			Type    customcommand.DurationSourceType `json:"type"`
			Seconds *int64                           `json:"seconds"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Seconds == nil {
			return fmt.Errorf("invalid fixed duration")
		}
		d.value = customcommand.DurationSource{Type: input.Type, Seconds: *input.Seconds}
	case customcommand.DurationParameter:
		var input struct {
			Type      customcommand.DurationSourceType `json:"type"`
			Parameter *string                          `json:"parameter"`
		}
		if err := decodeStrictJSONBytes(data, &input); err != nil || input.Parameter == nil {
			return fmt.Errorf("invalid parameter duration")
		}
		d.value = customcommand.DurationSource{Type: input.Type, Parameter: *input.Parameter}
	default:
		return fmt.Errorf("unknown duration source")
	}
	d.set = true
	return nil
}

type commandValidationSample struct {
	GroupID    *string                   `json:"group_id"`
	SenderQQ   *string                   `json:"sender_qq"`
	SenderRole *customcommand.SenderRole `json:"sender_role"`
	Message    *string                   `json:"message"`
}

func (s commandValidationSample) sample() (customcommand.ValidationSample, bool) {
	if s.GroupID == nil || s.SenderQQ == nil || s.SenderRole == nil || s.Message == nil {
		return customcommand.ValidationSample{}, false
	}
	return customcommand.ValidationSample{GroupID: *s.GroupID, SenderQQ: *s.SenderQQ, SenderRole: *s.SenderRole, Message: *s.Message}, true
}

func parseCommandListQuery(values url.Values) (customcommand.ListQuery, error) {
	if !validSingleQueryKeys(values, "query", "enabled", "status", "scope_type", "group_id", "action_type", "trigger_permission", "cursor", "limit") {
		return customcommand.ListQuery{}, customcommand.ErrInvalidInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return customcommand.ListQuery{}, err
	}
	query := customcommand.ListQuery{
		Query: values.Get("query"), Status: customcommand.Status(values.Get("status")), ScopeType: customcommand.ScopeType(values.Get("scope_type")),
		GroupID: values.Get("group_id"), ActionType: customcommand.ActionType(values.Get("action_type")),
		TriggerPermission: customcommand.TriggerPermission(values.Get("trigger_permission")), Cursor: values.Get("cursor"), Limit: limit,
	}
	if value := values.Get("enabled"); value != "" {
		enabled, err := parseStrictBool(value)
		if err != nil {
			return customcommand.ListQuery{}, err
		}
		query.Enabled = &enabled
	}
	if query.Status != "" && query.Status != customcommand.StatusDraft && query.Status != customcommand.StatusActive &&
		query.Status != customcommand.StatusDisabled {
		return customcommand.ListQuery{}, customcommand.ErrInvalidInput
	}
	if query.ScopeType != "" && query.ScopeType != customcommand.ScopeGlobal && query.ScopeType != customcommand.ScopeGroups {
		return customcommand.ListQuery{}, customcommand.ErrInvalidInput
	}
	if query.ActionType != "" && query.ActionType != customcommand.ActionReplyText && query.ActionType != customcommand.ActionMention &&
		query.ActionType != customcommand.ActionMuteMember && query.ActionType != customcommand.ActionSendGroupText {
		return customcommand.ListQuery{}, customcommand.ErrInvalidInput
	}
	if query.TriggerPermission != "" && query.TriggerPermission != customcommand.TriggerEveryone &&
		query.TriggerPermission != customcommand.TriggerGroupAdmin && query.TriggerPermission != customcommand.TriggerMaintenanceAllowlist {
		return customcommand.ListQuery{}, customcommand.ErrInvalidInput
	}
	return query, nil
}

func parseCommandRunListQuery(commandID string, values url.Values) (customcommand.RunListQuery, error) {
	if !validSingleQueryKeys(values, "result", "from", "to", "cursor", "limit") {
		return customcommand.RunListQuery{}, customcommand.ErrInvalidInput
	}
	limit, err := ParseLimit(values.Get("limit"))
	if err != nil {
		return customcommand.RunListQuery{}, err
	}
	query := customcommand.RunListQuery{CommandID: commandID, Result: customcommand.RunResult(values.Get("result")), Cursor: values.Get("cursor"), Limit: limit}
	if query.Result != "" && query.Result != customcommand.RunSuccess && query.Result != customcommand.RunDenied &&
		query.Result != customcommand.RunParseError && query.Result != customcommand.RunFailed && query.Result != customcommand.RunPartial && query.Result != customcommand.RunUnknown {
		return customcommand.RunListQuery{}, customcommand.ErrInvalidInput
	}
	if value := values.Get("from"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return customcommand.RunListQuery{}, err
		}
		query.From = &parsed
	}
	if value := values.Get("to"); value != "" {
		parsed, err := ParseUTCTimestamp(value)
		if err != nil {
			return customcommand.RunListQuery{}, err
		}
		query.To = &parsed
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return customcommand.RunListQuery{}, customcommand.ErrInvalidInput
	}
	return query, nil
}

type commandScopeDTO struct {
	Type     customcommand.ScopeType `json:"type"`
	GroupIDs []string                `json:"group_ids"`
}

type commandDTO struct {
	ID                string                          `json:"command_id"`
	Name              string                          `json:"name"`
	DisplayName       string                          `json:"display_name"`
	Description       string                          `json:"description"`
	Scope             commandScopeDTO                 `json:"scope"`
	TriggerPermission customcommand.TriggerPermission `json:"trigger_permission"`
	Parameters        []any                           `json:"parameters"`
	Actions           []any                           `json:"actions"`
	Enabled           bool                            `json:"enabled"`
	Status            customcommand.Status            `json:"status"`
	Version           uint64                          `json:"version"`
	CreatedAt         time.Time                       `json:"created_at"`
	UpdatedAt         time.Time                       `json:"updated_at"`
	UpdatedBy         auditActorDTO                   `json:"updated_by"`
}

type commandListDTO struct {
	Items      []commandDTO `json:"items"`
	NextCursor *string      `json:"next_cursor"`
	HasMore    bool         `json:"has_more"`
}

func mapCommand(value customcommand.Command) commandDTO {
	parameters := make([]any, len(value.Parameters))
	for index, parameter := range value.Parameters {
		parameters[index] = mapCommandParameter(parameter)
	}
	actions := make([]any, len(value.Actions))
	for index, action := range value.Actions {
		actions[index] = mapCommandAction(action)
	}
	return commandDTO{
		ID: value.ID, Name: value.Name, DisplayName: value.DisplayName, Description: value.Description,
		Scope:             commandScopeDTO{Type: value.Scope.Type, GroupIDs: append([]string{}, value.Scope.GroupIDs...)},
		TriggerPermission: value.TriggerPermission, Parameters: parameters, Actions: actions, Enabled: value.Enabled,
		Status: value.Status, Version: value.Version, CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC(), UpdatedBy: mapAuditActor(value.UpdatedBy),
	}
}

func mapCommandParameter(value customcommand.Parameter) any {
	type base struct {
		Name        string                      `json:"name"`
		DisplayName string                      `json:"display_name"`
		Type        customcommand.ParameterType `json:"type"`
		Required    bool                        `json:"required"`
	}
	common := base{Name: value.Name, DisplayName: value.DisplayName, Type: value.Type, Required: value.Required}
	switch value.Type {
	case customcommand.ParameterText:
		return struct {
			base
			MinLength int `json:"min_length"`
			MaxLength int `json:"max_length"`
		}{common, value.MinLength, value.MaxLength}
	case customcommand.ParameterInteger:
		return struct {
			base
			Minimum int64 `json:"minimum"`
			Maximum int64 `json:"maximum"`
		}{common, value.Minimum, value.Maximum}
	case customcommand.ParameterDuration:
		return struct {
			base
			Minimum int64 `json:"minimum_seconds"`
			Maximum int64 `json:"maximum_seconds"`
		}{common, value.MinimumSeconds, value.MaximumSeconds}
	case customcommand.ParameterMember:
		return struct {
			base
			AllowTriggerer bool `json:"allow_triggerer"`
		}{common, value.AllowTriggerer}
	case customcommand.ParameterFixedOption:
		return struct {
			base
			Options []commandFixedOptionDTO `json:"options"`
		}{common, mapFixedOptions(value.Options)}
	default:
		return common
	}
}

type commandFixedOptionDTO struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

func mapFixedOptions(values []customcommand.FixedOption) []commandFixedOptionDTO {
	result := make([]commandFixedOptionDTO, len(values))
	for index, value := range values {
		result[index] = commandFixedOptionDTO{Value: value.Value, Label: value.Label}
	}
	return result
}

func mapCommandAction(value customcommand.Action) any {
	switch value.Type {
	case customcommand.ActionReplyText:
		return struct {
			Type     customcommand.ActionType `json:"type"`
			Template string                   `json:"template"`
		}{value.Type, value.Template}
	case customcommand.ActionMention:
		var parameter *string
		if value.MemberParameter != "" {
			copy := value.MemberParameter
			parameter = &copy
		}
		return struct {
			Type            customcommand.ActionType    `json:"type"`
			Target          customcommand.MentionTarget `json:"target"`
			MemberParameter *string                     `json:"member_parameter"`
		}{value.Type, value.Target, parameter}
	case customcommand.ActionMuteMember:
		return struct {
			Type            customcommand.ActionType `json:"type"`
			MemberParameter string                   `json:"member_parameter"`
			Duration        any                      `json:"duration"`
		}{value.Type, value.MemberParameter, mapDurationSource(value.Duration)}
	case customcommand.ActionSendGroupText:
		return struct {
			Type           customcommand.ActionType `json:"type"`
			TargetGroupIDs []string                 `json:"target_group_ids"`
			Template       string                   `json:"template"`
		}{value.Type, append([]string{}, value.TargetGroupIDs...), value.Template}
	default:
		return struct {
			Type customcommand.ActionType `json:"type"`
		}{value.Type}
	}
}

func mapDurationSource(value customcommand.DurationSource) any {
	if value.Type == customcommand.DurationFixed {
		return struct {
			Type    customcommand.DurationSourceType `json:"type"`
			Seconds int64                            `json:"seconds"`
		}{value.Type, value.Seconds}
	}
	return struct {
		Type      customcommand.DurationSourceType `json:"type"`
		Parameter string                           `json:"parameter"`
	}{value.Type, value.Parameter}
}

type validationIssueDTO struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

type parsedCommandArgumentDTO struct {
	Name         string                      `json:"name"`
	Type         customcommand.ParameterType `json:"type"`
	DisplayValue string                      `json:"display_value"`
}

type renderedCommandActionDTO struct {
	Index   int                      `json:"index"`
	Type    customcommand.ActionType `json:"type"`
	Preview string                   `json:"preview"`
}

type commandValidationDTO struct {
	Valid           bool                       `json:"valid"`
	Issues          []validationIssueDTO       `json:"issues"`
	Warnings        []validationIssueDTO       `json:"warnings"`
	ParsedArguments []parsedCommandArgumentDTO `json:"parsed_arguments"`
	RenderedActions []renderedCommandActionDTO `json:"rendered_actions"`
}

func mapCommandValidation(value customcommand.ValidationResult) commandValidationDTO {
	result := commandValidationDTO{Valid: value.Valid, Issues: make([]validationIssueDTO, len(value.Issues)), Warnings: make([]validationIssueDTO, len(value.Warnings)), ParsedArguments: make([]parsedCommandArgumentDTO, len(value.ParsedArguments)), RenderedActions: make([]renderedCommandActionDTO, len(value.RenderedActions))}
	for index, issue := range value.Issues {
		result.Issues[index] = validationIssueDTO{issue.Path, issue.Code, issue.Message}
	}
	for index, issue := range value.Warnings {
		result.Warnings[index] = validationIssueDTO{issue.Path, issue.Code, issue.Message}
	}
	for index, argument := range value.ParsedArguments {
		result.ParsedArguments[index] = parsedCommandArgumentDTO{argument.Name, argument.Type, argument.DisplayValue}
	}
	for index, action := range value.RenderedActions {
		result.RenderedActions[index] = renderedCommandActionDTO{action.Index, action.Type, action.Preview}
	}
	return result
}

type commandActionStepDTO struct {
	Index      int                      `json:"index"`
	Type       customcommand.ActionType `json:"type"`
	Result     customcommand.StepResult `json:"result"`
	DurationMS int64                    `json:"duration_ms"`
	ErrorCode  *string                  `json:"error_code"`
}

type commandRunDTO struct {
	ID            string                  `json:"run_id"`
	CommandID     string                  `json:"command_id"`
	CommandName   string                  `json:"command_name"`
	GroupID       string                  `json:"group_id"`
	TriggeredByQQ string                  `json:"triggered_by_qq"`
	Result        customcommand.RunResult `json:"result"`
	ActionSteps   []commandActionStepDTO  `json:"action_steps"`
	DurationMS    int64                   `json:"duration_ms"`
	ErrorCode     *string                 `json:"error_code"`
	OccurredAt    time.Time               `json:"occurred_at"`
}

type commandRunListDTO struct {
	Items      []commandRunDTO `json:"items"`
	NextCursor *string         `json:"next_cursor"`
	HasMore    bool            `json:"has_more"`
}

func mapCommandRun(value customcommand.Run) commandRunDTO {
	steps := make([]commandActionStepDTO, len(value.ActionSteps))
	for index, step := range value.ActionSteps {
		duration := step.Duration.Milliseconds()
		if duration < 0 {
			duration = 0
		}
		steps[index] = commandActionStepDTO{Index: step.Index, Type: step.Type, Result: step.Result, DurationMS: duration, ErrorCode: step.ErrorCode}
	}
	duration := value.Duration.Milliseconds()
	if duration < 0 {
		duration = 0
	}
	return commandRunDTO{ID: value.ID, CommandID: value.CommandID, CommandName: value.CommandName, GroupID: value.GroupID,
		TriggeredByQQ: value.TriggeredByQQ, Result: value.Result, ActionSteps: steps, DurationMS: duration,
		ErrorCode: value.ErrorCode, OccurredAt: value.OccurredAt.UTC()}
}
