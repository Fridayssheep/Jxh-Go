package joinrequests

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
)

func TestDecidePassesExactFlagAndConfirmsApproval(t *testing.T) {
	request := joinRequestFixture("原始/flag:123", DecisionProcessing, 4)
	decision := decisionFixture(request.ID, "dec_1", ActionApprove, AttemptStarted)
	store := &joinStoreFake{reservation: Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		return completedDecisionResult(request, decision, mutation), nil
	}
	approver := &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalConfirmed}}}
	recorder := &joinTelemetryRecorderFake{}
	service := newJoinServiceWithTelemetry(t, store, approver, recorder)

	result, err := service.Decide(t.Context(), joinMaintainer(), request.ID, 3, DecisionInput{Action: ActionApprove}, "decision-key-1", joinMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if approver.flags[0] != request.ID || !approver.approvals[0] {
		t.Fatalf("gateway flags=%v approvals=%v", approver.flags, approver.approvals)
	}
	if store.beginMutation.Items[0].ID != request.ID || store.beginMutation.Items[0].Version != 3 ||
		store.completions[0].AttemptStatus != AttemptConfirmed || store.completions[0].DecisionStatus != DecisionApproved {
		t.Fatalf("begin=%+v completion=%+v", store.beginMutation, store.completions)
	}
	if result.Request.DecisionStatus != DecisionApproved || result.Decision.Status != AttemptConfirmed {
		t.Fatalf("result=%+v", result)
	}
	if len(recorder.observations) != 1 || recorder.observations[0].Kind != telemetry.EventManualApproval ||
		recorder.observations[0].Result != telemetry.ResultSuccess || recorder.observations[0].GroupID != 123 {
		t.Fatalf("decision telemetry=%+v", recorder.observations)
	}
}

func TestManualRejectRequiresAndNormalizesReason(t *testing.T) {
	for _, reason := range []string{"", "   ", strings.Repeat("拒", 501)} {
		store := &joinStoreFake{}
		approver := &joinApproverFake{available: true}
		service := newJoinService(t, store, approver)
		_, err := service.Decide(t.Context(), joinMaintainer(), "flag_1", 1,
			DecisionInput{Action: ActionReject, Reason: reason}, "decision-key-1", joinMutationRequest())
		if !errors.Is(err, ErrInvalidInput) || store.beginCalls != 0 || len(approver.flags) != 0 {
			t.Fatalf("reason length=%d error=%v begin=%d gateway=%d", len([]rune(reason)), err, store.beginCalls, len(approver.flags))
		}
	}

	request := joinRequestFixture("flag_1", DecisionProcessing, 2)
	decision := decisionFixture(request.ID, "dec_1", ActionReject, AttemptStarted)
	store := &joinStoreFake{reservation: Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		return completedDecisionResult(request, decision, mutation), nil
	}
	approver := &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalConfirmed}}}
	service := newJoinService(t, store, approver)
	_, err := service.Decide(t.Context(), joinMaintainer(), request.ID, 1,
		DecisionInput{Action: ActionReject, Reason: "  资料不完整，请重新申请。  "}, "decision-key-1", joinMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if store.beginMutation.Reason == nil || *store.beginMutation.Reason != "资料不完整，请重新申请。" ||
		len(approver.reasons) != 1 || approver.reasons[0] != "资料不完整，请重新申请。" {
		t.Fatalf("stored reason=%v gateway reasons=%v", store.beginMutation.Reason, approver.reasons)
	}
}

func TestBulkManualRejectRequiresAndNormalizesReason(t *testing.T) {
	store := &joinStoreFake{}
	approver := &joinApproverFake{available: true}
	service := newJoinService(t, store, approver)
	input := BulkInput{GroupID: "123", Action: ActionReject, Items: []VersionedRequest{{ID: "flag_1", Version: 1}}}
	if _, err := service.BulkDecide(t.Context(), joinMaintainer(), input, "bulk-decision-key-1", joinMutationRequest()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty bulk rejection reason error=%v", err)
	}
	if store.beginCalls != 0 || len(approver.flags) != 0 {
		t.Fatalf("empty bulk reason begin=%d gateway=%d", store.beginCalls, len(approver.flags))
	}

	request := joinRequestFixture("flag_1", DecisionProcessing, 2)
	decision := decisionFixture(request.ID, "dec_1", ActionReject, AttemptStarted)
	store = &joinStoreFake{reservation: Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		return completedDecisionResult(request, decision, mutation), nil
	}
	approver = &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalConfirmed}}}
	service = newJoinService(t, store, approver)
	input.Reason = "  本批申请资料不完整。  "
	if _, err := service.BulkDecide(t.Context(), joinMaintainer(), input, "bulk-decision-key-1", joinMutationRequest()); err != nil {
		t.Fatal(err)
	}
	if store.beginMutation.Reason == nil || *store.beginMutation.Reason != "本批申请资料不完整。" ||
		len(approver.reasons) != 1 || approver.reasons[0] != "本批申请资料不完整。" {
		t.Fatalf("stored reason=%v gateway reasons=%v", store.beginMutation.Reason, approver.reasons)
	}
}

func TestDecideClassifiesUnavailableFailureAndUnknown(t *testing.T) {
	t.Run("preflight unavailable", func(t *testing.T) {
		store := &joinStoreFake{}
		service := newJoinService(t, store, &joinApproverFake{})
		_, err := service.Decide(t.Context(), joinMaintainer(), "flag_1", 1, DecisionInput{Action: ActionApprove}, "decision-key-1", joinMutationRequest())
		if !errors.Is(err, ErrDependencyUnavailable) || store.beginCalls != 0 {
			t.Fatalf("error=%v begin calls=%d", err, store.beginCalls)
		}
	})

	for _, test := range []struct {
		name           string
		external       ExternalResult
		wantError      error
		attemptStatus  AttemptStatus
		decisionStatus DecisionStatus
		telemetry      telemetry.Result
	}{
		{name: "explicit failure", external: ExternalResult{Outcome: ExternalFailed, ErrorCode: "upstream_rejected"}, wantError: ErrExternalFailure, attemptStatus: AttemptFailed, decisionStatus: DecisionPending, telemetry: telemetry.ResultFailed},
		{name: "unknown", external: ExternalResult{Outcome: ExternalUnknown, ErrorCode: "upstream_timeout"}, attemptStatus: AttemptUnknown, decisionStatus: DecisionUnknown, telemetry: telemetry.ResultUnknown},
		{name: "disconnect before call", external: ExternalResult{Outcome: ExternalUnavailable, ErrorCode: "dependency_unavailable"}, wantError: ErrDependencyUnavailable, attemptStatus: AttemptFailed, decisionStatus: DecisionPending, telemetry: telemetry.ResultFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := joinRequestFixture("flag_1", DecisionProcessing, 2)
			decision := decisionFixture(request.ID, "dec_1", ActionReject, AttemptStarted)
			store := &joinStoreFake{reservation: Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}}
			store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
				return completedDecisionResult(request, decision, mutation), nil
			}
			recorder := &joinTelemetryRecorderFake{}
			service := newJoinServiceWithTelemetry(t, store, &joinApproverFake{available: true, results: []ExternalResult{test.external}}, recorder)
			result, err := service.Decide(t.Context(), joinMaintainer(), request.ID, 1,
				DecisionInput{Action: ActionReject, Reason: "不符合入群要求"}, "decision-key-1", joinMutationRequest())
			if !errors.Is(err, test.wantError) || result.Request.DecisionStatus != test.decisionStatus || result.Decision.Status != test.attemptStatus {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			if len(recorder.observations) != 1 || recorder.observations[0].Result != test.telemetry {
				t.Fatalf("decision telemetry=%+v", recorder.observations)
			}
		})
	}
}

func TestDecisionTelemetryWaitsForDurableCompletion(t *testing.T) {
	request := joinRequestFixture("flag_1", DecisionProcessing, 2)
	decision := decisionFixture(request.ID, "dec_1", ActionApprove, AttemptStarted)
	store := &joinStoreFake{reservation: Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}}
	store.complete = func(CompletionMutation) (DecisionResult, error) {
		return DecisionResult{}, errors.New("database unavailable")
	}
	recorder := &joinTelemetryRecorderFake{}
	service := newJoinServiceWithTelemetry(t, store, &joinApproverFake{available: true}, recorder)
	_, err := service.Decide(t.Context(), joinMaintainer(), request.ID, 1, DecisionInput{Action: ActionApprove}, "decision-key-1", joinMutationRequest())
	if err == nil {
		t.Fatal("decision unexpectedly succeeded")
	}
	if len(recorder.observations) != 0 {
		t.Fatalf("failed persistence emitted telemetry=%+v", recorder.observations)
	}
}

func TestDecisionCompletionRetriesWithoutRepeatingExternalAction(t *testing.T) {
	request := joinRequestFixture("flag_retry", DecisionProcessing, 2)
	decision := decisionFixture(request.ID, "dec_retry", ActionApprove, AttemptStarted)
	var attempts atomic.Int64
	store := &joinStoreFake{reservation: Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		if attempts.Add(1) == 1 {
			return DecisionResult{}, errors.New("database unavailable")
		}
		return completedDecisionResult(request, decision, mutation), nil
	}
	recorder := &joinRetryTelemetryRecorder{recorded: make(chan telemetry.Observation, 1)}
	approver := &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalConfirmed}}}
	service, err := NewService(Options{
		Store: store, Approver: approver, AutoRejectReasons: autoRejectReasonFake{value: "默认拒绝消息"},
		Telemetry: recorder, Now: func() time.Time { return joinTestTime },
		DecisionTimeout: time.Second, ProcessingLease: time.Minute, PersistenceTimeout: time.Second,
		PersistenceRetryDelay: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	if _, err := service.Decide(t.Context(), joinMaintainer(), request.ID, 1,
		DecisionInput{Action: ActionApprove}, "decision-retry-key", joinMutationRequest()); err == nil {
		t.Fatal("initial persistence failure did not reach the caller")
	}
	select {
	case observation := <-recorder.recorded:
		if observation.Result != telemetry.ResultSuccess || observation.Kind != telemetry.EventManualApproval {
			t.Fatalf("observation=%+v", observation)
		}
	case <-time.After(time.Second):
		t.Fatal("durable completion retry did not finish")
	}
	if attempts.Load() != 2 {
		t.Fatalf("completion attempts=%d, want 2", attempts.Load())
	}
	if len(approver.flags) != 1 {
		t.Fatalf("external decision calls=%d, want 1", len(approver.flags))
	}
}

func TestDecideConflictAndReplayNeverRepeatExternalAction(t *testing.T) {
	approver := &joinApproverFake{available: true}
	store := &joinStoreFake{beginErr: ErrConflict}
	service := newJoinService(t, store, approver)
	_, err := service.Decide(t.Context(), joinMaintainer(), "flag_1", 1, DecisionInput{Action: ActionApprove}, "decision-key-1", joinMutationRequest())
	if !errors.Is(err, ErrConflict) || len(approver.flags) != 0 {
		t.Fatalf("error=%v gateway calls=%d", err, len(approver.flags))
	}

	request := joinRequestFixture("flag_1", DecisionApproved, 3)
	decision := decisionFixture(request.ID, "dec_1", ActionApprove, AttemptConfirmed)
	completedAt := joinTestTime.Add(time.Second)
	decision.CompletedAt = &completedAt
	request.LastDecisionID = stringPointer(decision.ID)
	if !validRequest(request, true) || !validDecision(decision) {
		t.Fatalf("invalid replay fixture request_valid=%t decision_valid=%t request=%+v decision=%+v", validRequest(request, true), validDecision(decision), request, decision)
	}
	store = &joinStoreFake{reservation: Reservation{Replay: true, Items: []ReservedItem{{Request: request, Decision: decision}}}}
	replayRecorder := &joinTelemetryRecorderFake{}
	service = newJoinServiceWithTelemetry(t, store, approver, replayRecorder)
	result, err := service.Decide(t.Context(), joinMaintainer(), "flag_1", 2, DecisionInput{Action: ActionApprove}, "decision-key-1", joinMutationRequest())
	if err != nil || result.Decision.Status != AttemptConfirmed || len(approver.flags) != 0 {
		t.Fatalf("result=%+v error=%v gateway calls=%d", result, err, len(approver.flags))
	}
	if len(replayRecorder.observations) != 0 {
		t.Fatalf("replay emitted telemetry=%+v", replayRecorder.observations)
	}
}

func TestBulkDecisionReservesAllBeforeMixedExternalResults(t *testing.T) {
	firstRequest := joinRequestFixture("flag_1", DecisionProcessing, 2)
	secondRequest := joinRequestFixture("flag_2", DecisionProcessing, 5)
	firstDecision := decisionFixture(firstRequest.ID, "dec_1", ActionApprove, AttemptStarted)
	secondDecision := decisionFixture(secondRequest.ID, "dec_2", ActionApprove, AttemptStarted)
	store := &joinStoreFake{reservation: Reservation{Items: []ReservedItem{
		{Request: firstRequest, Decision: firstDecision}, {Request: secondRequest, Decision: secondDecision},
	}}}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		if mutation.RequestID == firstRequest.ID {
			return completedDecisionResult(firstRequest, firstDecision, mutation), nil
		}
		return completedDecisionResult(secondRequest, secondDecision, mutation), nil
	}
	approver := &joinApproverFake{available: true, results: []ExternalResult{
		{Outcome: ExternalConfirmed}, {Outcome: ExternalUnknown, ErrorCode: "upstream_timeout"},
	}}
	recorder := &joinTelemetryRecorderFake{}
	service := newJoinServiceWithTelemetry(t, store, approver, recorder)
	result, err := service.BulkDecide(t.Context(), joinMaintainer(), BulkInput{
		GroupID: "123", Action: ActionApprove,
		Items: []VersionedRequest{{ID: "flag_1", Version: 1}, {ID: "flag_2", Version: 4}},
	}, "bulk-decision-key-1", joinMutationRequest())
	if err != nil {
		t.Fatal(err)
	}
	if store.beginCalls != 1 || len(store.beginMutation.Items) != 2 || len(approver.flags) != 2 {
		t.Fatalf("begin=%d mutation=%+v gateway=%v", store.beginCalls, store.beginMutation, approver.flags)
	}
	if result.ConfirmedCount != 1 || result.UnknownCount != 1 || result.FailedCount != 0 ||
		result.Items[1].Outcome != ItemUnknown || result.Items[1].Error == nil {
		t.Fatalf("result=%+v", result)
	}
	if len(recorder.observations) != 2 || recorder.observations[0].Result != telemetry.ResultSuccess ||
		recorder.observations[1].Result != telemetry.ResultUnknown {
		t.Fatalf("bulk decision telemetry=%+v", recorder.observations)
	}
}

func TestBulkDecisionConflictDoesNotCallGateway(t *testing.T) {
	store := &joinStoreFake{beginErr: ErrConflict}
	approver := &joinApproverFake{available: true}
	service := newJoinService(t, store, approver)
	_, err := service.BulkDecide(t.Context(), joinMaintainer(), BulkInput{
		GroupID: "123", Action: ActionReject, Reason: "不符合入群要求",
		Items: []VersionedRequest{{ID: "flag_1", Version: 1}},
	}, "bulk-decision-key-1", joinMutationRequest())
	if !errors.Is(err, ErrConflict) || len(approver.flags) != 0 {
		t.Fatalf("error=%v gateway calls=%d", err, len(approver.flags))
	}
}

func TestAutomaticApprovalUsesDeterministicValidatedRule(t *testing.T) {
	validRequest := joinRequestFixture("flag_valid", DecisionPending, 3)
	invalidRequest := joinRequestFixture("flag_invalid", DecisionPending, 7)
	invalidStudentID := "999999"
	invalidRequest.AIParse.Fields.StudentID = &invalidStudentID
	policy := joinPolicyFixture()
	policy.Enabled = true
	store := &joinStoreFake{autoCandidates: []AutoCandidate{{Request: validRequest, Policy: policy}, {Request: invalidRequest, Policy: policy}}}
	store.begin = func(mutation BeginMutation) (Reservation, error) {
		processing := cloneRequest(validRequest)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(validRequest.ID, "dec_auto", ActionApprove, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		snapshot := cloneApplicantFields(mutation.FieldSnapshots[validRequest.ID])
		decision.FieldSnapshot = &snapshot
		return Reservation{Items: []ReservedItem{{Request: processing, Decision: decision}}}, nil
	}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		processing := cloneRequest(validRequest)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(validRequest.ID, "dec_auto", ActionApprove, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		decision.FieldSnapshot = cloneApplicantFieldsPointer(validRequest.AIParse.Fields)
		return completedDecisionResult(processing, decision, mutation), nil
	}
	approver := &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalConfirmed}}}
	recorder := &joinTelemetryRecorderFake{}
	service := newJoinServiceWithTelemetry(t, store, approver, recorder)
	if err := service.ProcessAutoApprovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.beginCalls != 1 || len(approver.flags) != 1 || approver.flags[0] != validRequest.ID || !approver.approvals[0] {
		t.Fatalf("begin calls=%d gateway flags=%v approvals=%v", store.beginCalls, approver.flags, approver.approvals)
	}
	if store.beginMutation.Source != SourceAutomatic || store.beginMutation.Action != ActionApprove ||
		store.beginMutation.PolicyRevision == nil || *store.beginMutation.PolicyRevision != policy.Version ||
		store.beginMutation.RuleVersion == nil || *store.beginMutation.RuleVersion != AutoApprovalRuleVersion ||
		store.beginMutation.FieldSnapshots[validRequest.ID].Valid == false {
		t.Fatalf("automatic mutation=%+v", store.beginMutation)
	}
	if len(recorder.observations) != 1 || recorder.observations[0].Kind != telemetry.EventAutomaticApproval ||
		recorder.observations[0].Result != telemetry.ResultSuccess {
		t.Fatalf("automatic decision telemetry=%+v", recorder.observations)
	}
}

func TestAutomaticFailureBecomesTerminalUnknownAndIsNotCounted(t *testing.T) {
	request := joinRequestFixture("flag_auto_failed", DecisionPending, 3)
	policy := joinPolicyFixture()
	policy.Enabled = true
	store := &joinStoreFake{autoCandidates: []AutoCandidate{{Request: request, Policy: policy}}}
	store.begin = func(mutation BeginMutation) (Reservation, error) {
		processing := cloneRequest(request)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(request.ID, "dec_auto_failed", ActionApprove, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		return Reservation{Items: []ReservedItem{{Request: processing, Decision: decision}}}, nil
	}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		processing := cloneRequest(request)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(request.ID, "dec_auto_failed", ActionApprove, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		return completedDecisionResult(processing, decision, mutation), nil
	}
	approver := &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalFailed, ErrorCode: "upstream_rejected"}}}
	recorder := &joinTelemetryRecorderFake{}
	service := newJoinServiceWithTelemetry(t, store, approver, recorder)
	if err := service.ProcessAutoApprovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.completions) != 1 || store.completions[0].AttemptStatus != AttemptUnknown ||
		store.completions[0].DecisionStatus != DecisionUnknown {
		t.Fatalf("automatic failure completion=%+v", store.completions)
	}
	if len(recorder.observations) != 0 {
		t.Fatalf("failed automatic approval emitted telemetry=%+v", recorder.observations)
	}
}

func TestAutomaticRejectionUsesConfiguredReasonAndSkipsUnparsedRequests(t *testing.T) {
	invalidRequest := joinRequestFixture("flag_invalid", DecisionPending, 7)
	invalidStudentID := "999999"
	invalidRequest.AIParse.Fields.StudentID = &invalidStudentID
	failedRequest := joinRequestFixture("flag_failed", DecisionPending, 2)
	failedRequest.AIParse = AIParseResult{Status: AIParseFailed}
	policy := joinPolicyFixture()
	policy.AutoReject = true
	store := &joinStoreFake{autoCandidates: []AutoCandidate{
		{Request: failedRequest, Policy: policy},
		{Request: invalidRequest, Policy: policy},
	}}
	reason := "申请资料不完整，请补充后重新申请。"
	store.begin = func(mutation BeginMutation) (Reservation, error) {
		processing := cloneRequest(invalidRequest)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(invalidRequest.ID, "dec_reject", ActionReject, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.Reason = stringPointer(reason)
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		decision.FieldSnapshot = cloneApplicantFieldsPointer(invalidRequest.AIParse.Fields)
		return Reservation{Items: []ReservedItem{{Request: processing, Decision: decision}}}, nil
	}
	store.complete = func(mutation CompletionMutation) (DecisionResult, error) {
		processing := cloneRequest(invalidRequest)
		processing.DecisionStatus = DecisionProcessing
		processing.DecisionSource = decisionSourcePointer(SourceAutomatic)
		processing.Version++
		decision := decisionFixture(invalidRequest.ID, "dec_reject", ActionReject, AttemptStarted)
		decision.Source = SourceAutomatic
		decision.Reason = stringPointer(reason)
		decision.RuleVersion = uint64Pointer(AutoApprovalRuleVersion)
		decision.FieldSnapshot = cloneApplicantFieldsPointer(invalidRequest.AIParse.Fields)
		return completedDecisionResult(processing, decision, mutation), nil
	}
	approver := &joinApproverFake{available: true, results: []ExternalResult{{Outcome: ExternalConfirmed}}}
	recorder := &joinTelemetryRecorderFake{}
	service := newJoinServiceWithOptions(t, store, approver, recorder, autoRejectReasonFake{value: reason})
	if err := service.ProcessAutoApprovals(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.beginCalls != 1 || store.beginMutation.Action != ActionReject || store.beginMutation.Reason == nil ||
		*store.beginMutation.Reason != reason {
		t.Fatalf("automatic rejection mutation=%+v calls=%d", store.beginMutation, store.beginCalls)
	}
	if len(approver.flags) != 1 || approver.flags[0] != invalidRequest.ID || approver.approvals[0] || approver.reasons[0] != reason {
		t.Fatalf("gateway flags=%v approvals=%v reasons=%v", approver.flags, approver.approvals, approver.reasons)
	}
	if len(recorder.observations) != 0 {
		t.Fatalf("automatic rejection changed approval telemetry=%+v", recorder.observations)
	}
}

func TestApplicantValidationUsesSafeIntersectionAndOriginalMessage(t *testing.T) {
	studentID, name, major := "123456", "张三", "计算机"
	valid := ValidateApplicantFields(ApplicantFields{StudentID: &studentID, Name: &name, Major: &major}, "学号123456 姓名张三 专业计算机")
	if !valid.Valid || len(valid.ValidationErrors) != 0 {
		t.Fatalf("valid fields=%+v", valid)
	}
	letterID := "A23456"
	inventedMajor := "自动化"
	invalid := ValidateApplicantFields(ApplicantFields{StudentID: &letterID, Name: &name, Major: &inventedMajor}, "学号A23456 姓名张三 专业计算机")
	if invalid.Valid || len(invalid.ValidationErrors) != 2 || invalid.ValidationErrors[0] != "student_id_invalid" ||
		invalid.ValidationErrors[1] != "major_not_in_verification_message" {
		t.Fatalf("invalid fields=%+v", invalid)
	}
}

func TestListComputesOverdueAndPolicyUpdateUsesRevision(t *testing.T) {
	request := joinRequestFixture("flag_1", DecisionPending, 1)
	request.RequestedAt = joinTestTime.Add(-25 * time.Hour)
	store := &joinStoreFake{requestPage: Page[Request]{Items: []Request{request}}, policy: joinPolicyFixture(), policyFound: true}
	service := newJoinService(t, store, &joinApproverFake{available: true})
	overdue := true
	page, err := service.List(t.Context(), joinMaintainer(), ListQuery{Overdue: &overdue})
	if err != nil || !page.Items[0].Overdue || store.listQuery.OverdueBefore == nil {
		t.Fatalf("page=%+v query=%+v error=%v", page, store.listQuery, err)
	}

	store.policy.Version = 2
	store.policy.Enabled = true
	updated, err := service.UpdatePolicy(t.Context(), joinSuperAdmin(), "123", 1, PolicyPatch{
		Enabled: auth.Field[bool]{Set: true, Value: true},
	}, joinMutationRequest())
	if err != nil || !updated.Enabled || store.policyMutation.ExpectedRevision != 1 {
		t.Fatalf("policy=%+v mutation=%+v error=%v", updated, store.policyMutation, err)
	}

	store.policy.Version = 3
	store.policy.AutoReject = true
	updated, err = service.UpdatePolicy(t.Context(), joinSuperAdmin(), "123", 2, PolicyPatch{
		AutoReject: auth.Field[bool]{Set: true, Value: true},
	}, joinMutationRequest())
	if err != nil || !updated.Enabled || !updated.AutoReject || !store.policyMutation.Patch.AutoReject.Set {
		t.Fatalf("auto reject policy=%+v mutation=%+v error=%v", updated, store.policyMutation, err)
	}

	store.updatePolicyCalls = 0
	if _, err := service.UpdatePolicy(t.Context(), joinSuperAdmin(), "123", 3, PolicyPatch{}, joinMutationRequest()); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty policy patch error=%v", err)
	}
	if store.updatePolicyCalls != 0 {
		t.Fatal("empty policy patch reached store")
	}
}

func TestRecoverInterruptedOnlyPublishesUnknownRequests(t *testing.T) {
	request := joinRequestFixture("flag_1", DecisionUnknown, 5)
	store := &joinStoreFake{recovered: []Request{request}}
	service := newJoinService(t, store, &joinApproverFake{available: true})
	if err := service.RecoverInterrupted(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !store.recoveryCutoff.Equal(joinTestTime) {
		t.Fatalf("recovery cutoff=%v", store.recoveryCutoff)
	}
}

func TestCheckedObservationNeverOverwritesConfirmedDecision(t *testing.T) {
	for _, status := range []DecisionStatus{DecisionApproved, DecisionRejected, DecisionProcessing, DecisionExternalProcessed} {
		if merged := MergeObservedDecisionStatus(status, ObservedChecked); merged != status {
			t.Errorf("MergeObservedDecisionStatus(%s, checked) = %s", status, merged)
		}
	}
	for _, status := range []DecisionStatus{DecisionPending, DecisionUnknown} {
		if merged := MergeObservedDecisionStatus(status, ObservedChecked); merged != DecisionExternalProcessed {
			t.Errorf("MergeObservedDecisionStatus(%s, checked) = %s", status, merged)
		}
	}
	if merged := MergeObservedDecisionStatus(DecisionApproved, ObservedPending); merged != DecisionApproved {
		t.Errorf("pending observation changed confirmed status to %s", merged)
	}
}

var joinTestTime = time.Date(2026, 7, 28, 3, 0, 0, 0, time.UTC)

func newJoinService(t *testing.T, store Store, approver Approver) *Service {
	return newJoinServiceWithTelemetry(t, store, approver, nil)
}

func newJoinServiceWithTelemetry(t *testing.T, store Store, approver Approver, recorder TelemetryRecorder) *Service {
	return newJoinServiceWithOptions(t, store, approver, recorder, autoRejectReasonFake{value: "默认拒绝消息"})
}

func newJoinServiceWithOptions(
	t *testing.T,
	store Store,
	approver Approver,
	recorder TelemetryRecorder,
	reasons AutoRejectReasonProvider,
) *Service {
	t.Helper()
	service, err := NewService(Options{
		Store: store, Approver: approver, AutoRejectReasons: reasons,
		Telemetry: recorder, Now: func() time.Time { return joinTestTime },
		DecisionTimeout: time.Second, ProcessingLease: time.Minute, PersistenceTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(service.Close)
	return service
}

type autoRejectReasonFake struct {
	value string
}

func (f autoRejectReasonFake) AutoRejectReason() string { return f.value }

type joinTelemetryRecorderFake struct {
	observations []telemetry.Observation
}

type joinRetryTelemetryRecorder struct {
	recorded chan telemetry.Observation
}

func (r *joinRetryTelemetryRecorder) Record(observation telemetry.Observation) bool {
	r.recorded <- observation
	return true
}

func (r *joinTelemetryRecorderFake) Record(observation telemetry.Observation) bool {
	r.observations = append(r.observations, observation)
	return true
}

func joinMaintainer() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleMaintainer}
}

func joinSuperAdmin() auth.Principal {
	return auth.Principal{UserID: "usr_1", SessionID: "ses_1", Role: auth.RoleSuperAdmin}
}

func joinMutationRequest() auth.MutationContext {
	return auth.MutationContext{RequestID: "req_1", IPAddress: "192.0.2.1", UserAgent: "join-request-test"}
}

func joinPolicyFixture() Policy {
	return Policy{
		GroupID: "123", Mode: PolicyModeAIFieldsComplete, RequiredFields: PolicyRequiredFields(),
		Version: 1, UpdatedAt: joinTestTime,
	}
}

func joinRequestFixture(id string, status DecisionStatus, version uint64) Request {
	studentID, name, major := "123456", "张三", "计算机"
	comment := "detail"
	return Request{
		ID: id, Group: GroupReference{ID: "123", Name: "Test Group"}, ApplicantQQ: "456",
		VerificationMessage: "学号123456 姓名张三 专业计算机", SubType: SubTypeAdd, Source: RequestSourceEvent,
		ObservedStatus: ObservedPending, DecisionStatus: status,
		AIParse:     AIParseResult{Status: AIParseSucceeded, Fields: &ApplicantFields{StudentID: &studentID, Name: &name, Major: &major}},
		RequestedAt: joinTestTime.Add(-time.Hour), Version: version, Comment: &comment,
		FirstObservedAt: joinTestTime.Add(-time.Hour), LastObservedAt: joinTestTime,
	}
}

func decisionFixture(requestID, decisionID string, action Action, status AttemptStatus) Decision {
	userID := "usr_1"
	return Decision{
		ID: decisionID, RequestID: requestID, Action: action, Source: SourceManual, Status: status,
		Actor:     &audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: "Admin"},
		StartedAt: joinTestTime, TraceID: "req_1",
	}
}

func completedDecisionResult(request Request, decision Decision, mutation CompletionMutation) DecisionResult {
	request.DecisionStatus = mutation.DecisionStatus
	request.Version++
	request.LastDecisionID = stringPointer(decision.ID)
	source := decision.Source
	request.DecisionSource = &source
	decision.Status = mutation.AttemptStatus
	decision.CompletedAt = timePointer(mutation.CompletedAt)
	decision.ErrorCode = cloneString(mutation.ErrorCode)
	return DecisionResult{Request: request, Decision: decision}
}

func stringPointer(value string) *string                         { return &value }
func uint64Pointer(value uint64) *uint64                         { return &value }
func decisionSourcePointer(value DecisionSource) *DecisionSource { return &value }
func timePointer(value time.Time) *time.Time                     { return &value }

type joinApproverFake struct {
	available bool
	results   []ExternalResult
	flags     []string
	approvals []bool
	reasons   []string
}

func (a *joinApproverFake) JoinRequestDecisionAvailable() bool { return a.available }

func (a *joinApproverFake) DecideJoinRequest(_ context.Context, flag string, approve bool, reason string) ExternalResult {
	a.flags = append(a.flags, flag)
	a.approvals = append(a.approvals, approve)
	a.reasons = append(a.reasons, reason)
	if len(a.results) == 0 {
		return ExternalResult{Outcome: ExternalConfirmed}
	}
	result := a.results[0]
	a.results = a.results[1:]
	return result
}

type joinStoreFake struct {
	policy                Policy
	policyFound           bool
	requestPage           Page[Request]
	request               Request
	requestFound          bool
	decisionPage          Page[Decision]
	decisionsFound        bool
	reservation           Reservation
	beginErr              error
	begin                 func(BeginMutation) (Reservation, error)
	complete              func(CompletionMutation) (DecisionResult, error)
	autoCandidates        []AutoCandidate
	recovered             []Request
	beginMutation         BeginMutation
	policyMutation        PolicyMutation
	studentIDRule         StudentIDRule
	studentIDRuleFound    bool
	studentIDRuleMutation StudentIDRuleMutation
	listQuery             ListQuery
	completions           []CompletionMutation
	beginCalls            int
	updatePolicyCalls     int
	recoveryCutoff        time.Time
}

func (s *joinStoreFake) GetStudentIDRule(context.Context) (StudentIDRule, bool, error) {
	return cloneStudentIDRule(s.studentIDRule), s.studentIDRuleFound, nil
}

func (s *joinStoreFake) UpdateStudentIDRule(_ context.Context, mutation StudentIDRuleMutation) (StudentIDRule, error) {
	s.studentIDRuleMutation = mutation
	result := cloneStudentIDRule(mutation.Rule)
	result.Version = mutation.ExpectedRevision + 1
	result.UpdatedAt = mutation.Context.OccurredAt.UTC()
	actor := mutation.Context.Actor
	result.UpdatedBy = &actor
	s.studentIDRule, s.studentIDRuleFound = cloneStudentIDRule(result), true
	return result, nil
}

func (s *joinStoreFake) GetPolicy(context.Context, string) (Policy, bool, error) {
	return s.policy, s.policyFound, nil
}

func (s *joinStoreFake) UpdatePolicy(_ context.Context, mutation PolicyMutation) (Policy, error) {
	s.updatePolicyCalls++
	s.policyMutation = mutation
	return s.policy, nil
}

func (s *joinStoreFake) ListRequests(_ context.Context, query ListQuery) (Page[Request], error) {
	s.listQuery = query
	return s.requestPage, nil
}

func (s *joinStoreFake) GetRequest(context.Context, string) (Request, bool, error) {
	return s.request, s.requestFound, nil
}

func (s *joinStoreFake) ListDecisions(context.Context, DecisionListQuery) (Page[Decision], bool, error) {
	return s.decisionPage, s.decisionsFound, nil
}

func (s *joinStoreFake) BeginDecisions(_ context.Context, mutation BeginMutation) (Reservation, error) {
	s.beginCalls++
	s.beginMutation = mutation
	if s.begin != nil {
		return s.begin(mutation)
	}
	return s.reservation, s.beginErr
}

func (s *joinStoreFake) CompleteDecision(_ context.Context, mutation CompletionMutation) (DecisionResult, error) {
	s.completions = append(s.completions, mutation)
	if s.complete == nil {
		return DecisionResult{}, nil
	}
	return s.complete(mutation)
}

func (s *joinStoreFake) ListAutoCandidates(context.Context, int) ([]AutoCandidate, error) {
	return s.autoCandidates, nil
}

func (*joinStoreFake) RetireStaleAutomaticRequests(context.Context, MutationContext) error {
	return nil
}

func (s *joinStoreFake) RecoverExpiredDecisions(_ context.Context, cutoff time.Time, _ int) ([]Request, error) {
	s.recoveryCutoff = cutoff
	return s.recovered, nil
}
