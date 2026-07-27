package customcommand

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxParameters         = 10
	maxActions            = 10
	maxScopeGroups        = 100
	maxActionTargets      = 10
	maxTotalActionTargets = 20
	maxTotalTemplateRunes = 10000
	maxMessageRunes       = 2000
	maxPreviewRunes       = 2500
	maxMuteSeconds        = 2592000
)

var (
	commandNamePattern   = regexp.MustCompile(`^/[a-z](?:[a-z0-9_-]{0,31})$`)
	parameterNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	optionValuePattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	templateVariable     = regexp.MustCompile(`\{\{([a-z][a-z0-9_]*)\}\}`)
)

func ValidateDefinition(definition Definition) []ValidationIssue {
	issues := make([]ValidationIssue, 0)
	add := func(path, code, message string) {
		issues = append(issues, ValidationIssue{Path: path, Code: code, Message: message})
	}
	if !commandNamePattern.MatchString(definition.Name) {
		add("name", "invalid_command_name", "command name must be a lowercase slash command")
	}
	if !validRuneText(definition.DisplayName, 1, 64) {
		add("display_name", "invalid_length", "display name must contain 1 to 64 characters")
	}
	if !validRuneText(definition.Description, 0, 500) {
		add("description", "invalid_length", "description must contain at most 500 characters")
	}
	validateScope(definition.Scope, add)
	if definition.TriggerPermission != TriggerEveryone && definition.TriggerPermission != TriggerGroupAdmin &&
		definition.TriggerPermission != TriggerMaintenanceAllowlist {
		add("trigger_permission", "invalid_permission", "trigger permission is not supported")
	}
	if len(definition.Parameters) > maxParameters {
		add("parameters", "too_many_parameters", "at most 10 parameters are allowed")
	}
	parameterTypes := make(map[string]ParameterType, len(definition.Parameters))
	optionalSeen := false
	for index, parameter := range definition.Parameters {
		path := fmt.Sprintf("parameters[%d]", index)
		if !parameter.Required {
			optionalSeen = true
		} else if optionalSeen {
			add(path+".required", "required_after_optional", "required parameters must precede optional parameters")
		}
		if _, duplicate := parameterTypes[parameter.Name]; duplicate {
			add(path+".name", "duplicate_parameter", "parameter names must be unique")
		} else {
			parameterTypes[parameter.Name] = parameter.Type
		}
		validateParameter(parameter, path, add)
	}
	if len(definition.Actions) < 1 || len(definition.Actions) > maxActions {
		add("actions", "invalid_action_count", "between 1 and 10 actions are required")
	}
	totalTargets, totalTemplateRunes := 0, 0
	for index, action := range definition.Actions {
		path := fmt.Sprintf("actions[%d]", index)
		totalTargets += len(action.TargetGroupIDs)
		totalTemplateRunes += utf8.RuneCountInString(action.Template)
		validateAction(action, definition.TriggerPermission, parameterTypes, path, add)
	}
	if totalTargets > maxTotalActionTargets {
		add("actions", "too_many_targets", "a command may target at most 20 groups in total")
	}
	if totalTemplateRunes > maxTotalTemplateRunes {
		add("actions", "templates_too_large", "combined action templates are too large")
	}
	return issues
}

func validateScope(scope Scope, add func(string, string, string)) {
	if scope.Type != ScopeGlobal && scope.Type != ScopeGroups {
		add("scope.type", "invalid_scope", "scope type must be global or groups")
		return
	}
	if len(scope.GroupIDs) > maxScopeGroups || (scope.Type == ScopeGroups && len(scope.GroupIDs) == 0) ||
		(scope.Type == ScopeGlobal && len(scope.GroupIDs) != 0) {
		add("scope.group_ids", "invalid_scope_groups", "group IDs do not match the selected scope")
	}
	seen := make(map[string]struct{}, len(scope.GroupIDs))
	for index, groupID := range scope.GroupIDs {
		if !validIdentifier(groupID) {
			add(fmt.Sprintf("scope.group_ids[%d]", index), "invalid_identifier", "group ID is invalid")
		}
		if _, duplicate := seen[groupID]; duplicate {
			add(fmt.Sprintf("scope.group_ids[%d]", index), "duplicate_group", "group IDs must be unique")
		}
		seen[groupID] = struct{}{}
	}
}

func validateParameter(parameter Parameter, path string, add func(string, string, string)) {
	if !parameterNamePattern.MatchString(parameter.Name) {
		add(path+".name", "invalid_parameter_name", "parameter name is invalid")
	}
	if !validRuneText(parameter.DisplayName, 1, 64) {
		add(path+".display_name", "invalid_length", "parameter display name is invalid")
	}
	switch parameter.Type {
	case ParameterText:
		if parameter.MinLength < 0 || parameter.MinLength > 500 || parameter.MaxLength < 1 || parameter.MaxLength > 500 ||
			parameter.MinLength > parameter.MaxLength || parameter.Minimum != 0 || parameter.Maximum != 0 ||
			parameter.MinimumSeconds != 0 || parameter.MaximumSeconds != 0 || parameter.AllowTriggerer || len(parameter.Options) != 0 {
			add(path, "invalid_text_parameter", "text parameter constraints are invalid")
		}
	case ParameterInteger:
		if parameter.Minimum < -1000000000 || parameter.Maximum > 1000000000 || parameter.Minimum > parameter.Maximum ||
			parameter.MinLength != 0 || parameter.MaxLength != 0 || parameter.MinimumSeconds != 0 ||
			parameter.MaximumSeconds != 0 || parameter.AllowTriggerer || len(parameter.Options) != 0 {
			add(path, "invalid_integer_parameter", "integer parameter constraints are invalid")
		}
	case ParameterDuration:
		if parameter.MinimumSeconds < 1 || parameter.MaximumSeconds > maxMuteSeconds || parameter.MinimumSeconds > parameter.MaximumSeconds ||
			parameter.MinLength != 0 || parameter.MaxLength != 0 || parameter.Minimum != 0 || parameter.Maximum != 0 ||
			parameter.AllowTriggerer || len(parameter.Options) != 0 {
			add(path, "invalid_duration_parameter", "duration parameter constraints are invalid")
		}
	case ParameterMember:
		if parameter.MinLength != 0 || parameter.MaxLength != 0 || parameter.Minimum != 0 || parameter.Maximum != 0 ||
			parameter.MinimumSeconds != 0 || parameter.MaximumSeconds != 0 || len(parameter.Options) != 0 {
			add(path, "invalid_member_parameter", "member parameter contains unsupported constraints")
		}
	case ParameterFixedOption:
		if len(parameter.Options) < 1 || len(parameter.Options) > 30 || parameter.MinLength != 0 || parameter.MaxLength != 0 ||
			parameter.Minimum != 0 || parameter.Maximum != 0 || parameter.MinimumSeconds != 0 ||
			parameter.MaximumSeconds != 0 || parameter.AllowTriggerer {
			add(path, "invalid_fixed_options", "fixed option parameter constraints are invalid")
		}
		seen := make(map[string]struct{}, len(parameter.Options))
		for index, option := range parameter.Options {
			optionPath := fmt.Sprintf("%s.options[%d]", path, index)
			if !optionValuePattern.MatchString(option.Value) || !validRuneText(option.Label, 1, 100) {
				add(optionPath, "invalid_fixed_option", "fixed option is invalid")
			}
			if _, duplicate := seen[option.Value]; duplicate {
				add(optionPath+".value", "duplicate_fixed_option", "fixed option values must be unique")
			}
			seen[option.Value] = struct{}{}
		}
	default:
		add(path+".type", "invalid_parameter_type", "parameter type is not supported")
	}
}

func validateAction(action Action, permission TriggerPermission, parameters map[string]ParameterType, path string, add func(string, string, string)) {
	switch action.Type {
	case ActionReplyText:
		if !validRuneText(action.Template, 1, 2000) || action.Target != "" || action.MemberParameter != "" ||
			action.Duration.Type != "" || len(action.TargetGroupIDs) != 0 {
			add(path, "invalid_reply_action", "reply action is invalid")
		}
		validateTemplate(action.Template, parameters, path+".template", add)
	case ActionMention:
		valid := action.Template == "" && action.Duration.Type == "" && len(action.TargetGroupIDs) == 0
		if action.Target == MentionTriggerer {
			valid = valid && action.MemberParameter == ""
		} else if action.Target == MentionParameter {
			valid = valid && parameters[action.MemberParameter] == ParameterMember
		} else {
			valid = false
		}
		if !valid {
			add(path, "invalid_mention_action", "mention target must reference a member parameter or the triggerer")
		}
	case ActionMuteMember:
		valid := action.Template == "" && action.Target == "" && len(action.TargetGroupIDs) == 0 &&
			parameters[action.MemberParameter] == ParameterMember
		if action.Duration.Type == DurationFixed {
			valid = valid && action.Duration.Seconds >= 1 && action.Duration.Seconds <= maxMuteSeconds && action.Duration.Parameter == ""
		} else if action.Duration.Type == DurationParameter {
			valid = valid && action.Duration.Seconds == 0 && parameters[action.Duration.Parameter] == ParameterDuration
		} else {
			valid = false
		}
		if !valid {
			add(path, "invalid_mute_action", "mute action parameters are invalid")
		}
		if permission == TriggerEveryone {
			add(path, "privilege_escalation", "mute actions cannot be triggered by every member")
		}
	case ActionSendGroupText:
		valid := validRuneText(action.Template, 1, 2000) && action.Target == "" && action.MemberParameter == "" && action.Duration.Type == "" &&
			len(action.TargetGroupIDs) >= 1 && len(action.TargetGroupIDs) <= maxActionTargets
		seen := make(map[string]struct{}, len(action.TargetGroupIDs))
		for _, groupID := range action.TargetGroupIDs {
			if !validFixedGroupID(groupID) {
				valid = false
			}
			if _, duplicate := seen[groupID]; duplicate {
				valid = false
			}
			seen[groupID] = struct{}{}
		}
		if !valid {
			add(path, "invalid_group_send_action", "group send targets and template are invalid")
		}
		if permission == TriggerEveryone {
			add(path, "privilege_escalation", "cross-group sends cannot be triggered by every member")
		}
		validateTemplate(action.Template, parameters, path+".template", add)
	default:
		add(path+".type", "invalid_action_type", "action type is not supported")
	}
}

func validateTemplate(value string, parameters map[string]ParameterType, path string, add func(string, string, string)) {
	allowed := map[string]struct{}{"sender_qq": {}, "group_id": {}}
	for name := range parameters {
		allowed[name] = struct{}{}
	}
	for _, match := range templateVariable.FindAllStringSubmatch(value, -1) {
		if _, ok := allowed[match[1]]; !ok {
			add(path, "unknown_template_variable", "template contains an unknown variable")
		}
	}
	remainder := templateVariable.ReplaceAllString(value, "")
	if strings.Contains(remainder, "{{") || strings.Contains(remainder, "}}") {
		add(path, "invalid_template", "template supports only simple allowlisted variables")
	}
}

func applyPatch(command Command, patch Patch) Command {
	if patch.Name.Set {
		command.Name = patch.Name.Value
	}
	if patch.DisplayName.Set {
		command.DisplayName = patch.DisplayName.Value
	}
	if patch.Description.Set {
		command.Description = patch.Description.Value
	}
	if patch.Scope.Set {
		command.Scope = cloneScope(patch.Scope.Value)
	}
	if patch.TriggerPermission.Set {
		command.TriggerPermission = patch.TriggerPermission.Value
	}
	if patch.Parameters.Set {
		command.Parameters = cloneParameters(patch.Parameters.Value)
	}
	if patch.Actions.Set {
		command.Actions = cloneActions(patch.Actions.Value)
	}
	if patch.Enabled.Set {
		command.Enabled = patch.Enabled.Value
		if patch.Enabled.Value {
			command.Status = StatusActive
		} else if command.Status != StatusDraft {
			command.Status = StatusDisabled
		}
	}
	return command
}

func patchSet(patch Patch) bool {
	return patch.Name.Set || patch.DisplayName.Set || patch.Description.Set || patch.Scope.Set ||
		patch.TriggerPermission.Set || patch.Parameters.Set || patch.Actions.Set || patch.Enabled.Set
}

func validIdentifier(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) >= 1 && utf8.RuneCountInString(value) <= 256
}

func validFixedGroupID(value string) bool {
	return validIdentifier(value) && !strings.Contains(value, "{{") && !strings.Contains(value, "}}")
}

func validRuneText(value string, minimum, maximum int) bool {
	if !utf8.ValidString(value) {
		return false
	}
	length := utf8.RuneCountInString(value)
	return length >= minimum && length <= maximum
}
