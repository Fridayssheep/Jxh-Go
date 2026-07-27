package customcommand

func cloneScope(value Scope) Scope {
	value.GroupIDs = append([]string(nil), value.GroupIDs...)
	return value
}

func cloneParameter(value Parameter) Parameter {
	value.Options = append([]FixedOption(nil), value.Options...)
	return value
}

func cloneParameters(values []Parameter) []Parameter {
	result := make([]Parameter, len(values))
	for index := range values {
		result[index] = cloneParameter(values[index])
	}
	return result
}

func cloneAction(value Action) Action {
	value.TargetGroupIDs = append([]string(nil), value.TargetGroupIDs...)
	return value
}

func cloneActions(values []Action) []Action {
	result := make([]Action, len(values))
	for index := range values {
		result[index] = cloneAction(values[index])
	}
	return result
}

func cloneDefinition(value Definition) Definition {
	value.Scope = cloneScope(value.Scope)
	value.Parameters = cloneParameters(value.Parameters)
	value.Actions = cloneActions(value.Actions)
	return value
}

func cloneCommand(value Command) Command {
	value.Definition = cloneDefinition(value.Definition)
	value.UpdatedBy.UserID = cloneString(value.UpdatedBy.UserID)
	value.UpdatedBy.QQUserID = cloneString(value.UpdatedBy.QQUserID)
	return value
}

func cloneCommands(values []Command) []Command {
	result := make([]Command, len(values))
	for index := range values {
		result[index] = cloneCommand(values[index])
	}
	return result
}

func cloneRun(value Run) Run {
	value.ArgumentSummaries = append([]ArgumentSummary(nil), value.ArgumentSummaries...)
	value.ActionSteps = append([]ActionStep(nil), value.ActionSteps...)
	for index := range value.ActionSteps {
		value.ActionSteps[index].ErrorCode = cloneString(value.ActionSteps[index].ErrorCode)
	}
	value.ErrorCode = cloneString(value.ErrorCode)
	return value
}

func cloneRuns(values []Run) []Run {
	result := make([]Run, len(values))
	for index := range values {
		result[index] = cloneRun(values[index])
	}
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func clonePatch(value Patch) Patch {
	value.Scope.Value = cloneScope(value.Scope.Value)
	value.Parameters.Value = cloneParameters(value.Parameters.Value)
	value.Actions.Value = cloneActions(value.Actions.Value)
	return value
}
