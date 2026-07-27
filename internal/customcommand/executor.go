package customcommand

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
	"unicode/utf8"
)

const argumentSummaryDomain = "jxh-manager:custom-command-argument:v1\x00"

var executionMetadataPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)

// Execute returns handled=false when no active command exactly matches the
// first token or the command is outside the current group scope.
func (s *Service) Execute(ctx context.Context, input ExecuteInput) (Run, bool, error) {
	command, matched := s.registry.Match(input.Message, input.GroupID)
	if !matched {
		return Run{}, false, nil
	}
	startedAt := s.now().UTC()
	run := Run{
		RunIdentity: safeMetadata(input.RunIdentity, 128), CommandID: command.ID, CommandName: command.Name, GroupID: input.GroupID,
		TriggeredByQQ: input.SenderQQ, RequestID: safeMetadata(input.RequestID, 64), OccurredAt: startedAt,
	}
	run.ActionSteps = skippedSteps(command.Actions)
	if !validIdentifier(input.GroupID) || !validQQ(input.SenderQQ) ||
		(input.SenderRole != SenderOwner && input.SenderRole != SenderAdmin && input.SenderRole != SenderMember) {
		run.Result, run.ErrorCode = RunParseError, safeCode("invalid_trigger_context")
		return s.persistRun(ctx, run, startedAt)
	}
	if !triggerAllowed(command.TriggerPermission, input.SenderRole, input.MaintenanceAllowlisted) {
		run.Result, run.ErrorCode = RunDenied, safeCode("trigger_forbidden")
		return s.persistRun(ctx, run, startedAt)
	}
	values, _, err := parseMessage(command.Definition, input.Message)
	if err != nil || !memberArgumentsAllowed(command.Parameters, values, input.SenderQQ) {
		run.Result, run.ErrorCode = RunParseError, safeCode("argument_parse_failed")
		return s.persistRun(ctx, run, startedAt)
	}
	run.ArgumentSummaries = s.argumentSummaries(command.Parameters, values)
	if s.gateway == nil || !s.gateway.Available() {
		run.Result, run.ErrorCode = RunFailed, safeCode("dependency_unavailable")
		return s.persistRun(ctx, run, startedAt)
	}

	executionContext, cancel := context.WithTimeout(ctx, s.executionTimeout)
	defer cancel()
	sample := ValidationSample{GroupID: input.GroupID, SenderQQ: input.SenderQQ, SenderRole: input.SenderRole, Message: input.Message}
	successfulEffects := 0
	for index, action := range command.Actions {
		stepStartedAt := s.now().UTC()
		effects, actionErr := s.executeAction(executionContext, action, sample, values)
		stepCompletedAt := s.now().UTC()
		run.ActionSteps[index].Duration = safeDuration(stepCompletedAt.Sub(stepStartedAt))
		successfulEffects += effects
		if actionErr == nil {
			run.ActionSteps[index].Result = StepSuccess
			continue
		}
		stepResult, code := classifyActionError(actionErr)
		run.ActionSteps[index].Result = stepResult
		run.ActionSteps[index].ErrorCode = safeCode(code)
		run.ErrorCode = safeCode(code)
		if successfulEffects > 0 {
			run.Result = RunPartial
		} else if stepResult == StepUnknown {
			run.Result = RunUnknown
		} else {
			run.Result = RunFailed
		}
		return s.persistRun(ctx, run, startedAt)
	}
	run.Result = RunSuccess
	return s.persistRun(ctx, run, startedAt)
}

func (s *Service) executeAction(ctx context.Context, action Action, sample ValidationSample, values map[string]parsedValue) (int, error) {
	switch action.Type {
	case ActionReplyText:
		if err := s.gateway.ReplyText(ctx, sample.GroupID, renderTemplate(action.Template, sample, values)); err != nil {
			return 0, err
		}
		return 1, nil
	case ActionMention:
		member := sample.SenderQQ
		if action.Target == MentionParameter {
			member = values[action.MemberParameter].member
		}
		if err := s.gateway.Mention(ctx, sample.GroupID, member); err != nil {
			return 0, err
		}
		return 1, nil
	case ActionMuteMember:
		duration := time.Duration(action.Duration.Seconds) * time.Second
		if action.Duration.Type == DurationParameter {
			duration = values[action.Duration.Parameter].duration
		}
		if err := s.gateway.MuteMember(ctx, sample.GroupID, values[action.MemberParameter].member, duration); err != nil {
			return 0, err
		}
		return 1, nil
	case ActionSendGroupText:
		message := renderTemplate(action.Template, sample, values)
		completed := 0
		for _, groupID := range action.TargetGroupIDs {
			if err := s.gateway.SendGroupText(ctx, groupID, message); err != nil {
				return completed, err
			}
			completed++
		}
		return completed, nil
	default:
		return 0, ErrInvalidInput
	}
}

func (s *Service) persistRun(ctx context.Context, run Run, startedAt time.Time) (Run, bool, error) {
	completedAt := s.now().UTC()
	run.Duration = safeDuration(completedAt.Sub(startedAt))
	persistContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.executionTimeout)
	stored, err := s.store.RecordCommandRun(persistContext, cloneRun(run))
	cancel()
	if err != nil {
		return Run{}, true, fmt.Errorf("record custom command run: %w", err)
	}
	return cloneRun(stored), true, nil
}

func safeMetadata(value string, maximum int) string {
	if value == "" {
		return ""
	}
	if len(value) > maximum || !executionMetadataPattern.MatchString(value) {
		return ""
	}
	return value
}

func classifyActionError(err error) (StepResult, string) {
	switch {
	case errors.Is(err, ErrOutcomeUnknown), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StepUnknown, "action_outcome_unknown"
	case errors.Is(err, ErrGatewayUnavailable):
		return StepFailed, "dependency_unavailable"
	default:
		return StepFailed, "action_failed"
	}
}

func skippedSteps(actions []Action) []ActionStep {
	steps := make([]ActionStep, len(actions))
	for index, action := range actions {
		steps[index] = ActionStep{Index: index, Type: action.Type, Result: StepSkipped}
	}
	return steps
}

func memberArgumentsAllowed(parameters []Parameter, values map[string]parsedValue, senderQQ string) bool {
	for _, parameter := range parameters {
		value := values[parameter.Name]
		if parameter.Type == ParameterMember && value.present && value.member == senderQQ && !parameter.AllowTriggerer {
			return false
		}
	}
	return true
}

func (s *Service) argumentSummaries(parameters []Parameter, values map[string]parsedValue) []ArgumentSummary {
	result := make([]ArgumentSummary, 0, len(parameters))
	for _, parameter := range parameters {
		value := values[parameter.Name]
		summary := ArgumentSummary{Name: parameter.Name, Type: parameter.Type, Present: value.present}
		if value.present {
			summary.RuneLength = utf8.RuneCountInString(value.raw)
			if len(s.summaryKey) != 0 {
				mac := hmac.New(sha256.New, s.summaryKey)
				_, _ = mac.Write([]byte(argumentSummaryDomain))
				writeSummaryPart(mac, parameter.Name)
				writeSummaryPart(mac, string(parameter.Type))
				writeSummaryPart(mac, value.raw)
				summary.Digest = hex.EncodeToString(mac.Sum(nil))
			}
		}
		result = append(result, summary)
	}
	return result
}

func writeSummaryPart(mac interface{ Write([]byte) (int, error) }, value string) {
	_, _ = mac.Write([]byte(strconv.Itoa(len(value))))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(value))
	_, _ = mac.Write([]byte{0})
}
