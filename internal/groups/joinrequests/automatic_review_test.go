package joinrequests

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
)

func TestCheckStudentID2026(t *testing.T) {
	now := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	valid := CheckStudentID("302026315326", now)
	if !valid.LengthValid || !valid.Numeric || !valid.YearValid || valid.EnrollmentYear != "2026" || valid.MajorCode != "315" {
		t.Fatalf("unexpected valid check: %+v", valid)
	}
	old := CheckStudentID("302025315326", now)
	if !old.LengthValid || !old.Numeric || old.YearValid || old.EnrollmentYear != "2025" {
		t.Fatalf("unexpected old-year check: %+v", old)
	}
	nondigit := CheckStudentID("30202631A326", now)
	if !nondigit.LengthValid || nondigit.Numeric || nondigit.EnrollmentYear != "" || nondigit.MajorCode != "" {
		t.Fatalf("unexpected nonnumeric check: %+v", nondigit)
	}
}

func TestAutomaticReviewJSONDoesNotContainFullStudentID(t *testing.T) {
	review := AutomaticReview{
		RuleVersion: AutoApprovalRuleVersion, Outcome: ReviewRejected,
		StudentID: CheckStudentID("302026315326", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		Roster:    RosterAssessment{Status: RosterNotConfigured}, ReasonCode: "test", Reason: "test", ReviewedAt: time.Now().UTC(),
	}
	payload, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "302026315326") {
		t.Fatalf("automatic review leaked full student ID: %s", payload)
	}
}

type reviewStore struct {
	Store
	evidence  MajorEvidence
	roster    AdmissionRosterRecord
	rosterErr error
	candidate AutoCandidate
	begin     BeginMutation
}

func (s *reviewStore) Lookup(context.Context, string) (AdmissionRosterRecord, error) {
	return s.roster, s.rosterErr
}

func (s *reviewStore) GetMajorEvidence(context.Context, string, string) (MajorEvidence, error) {
	return s.evidence, nil
}

func (s *reviewStore) ListAutoCandidates(context.Context, int) ([]AutoCandidate, error) {
	return []AutoCandidate{s.candidate}, nil
}

func (s *reviewStore) BeginDecisions(_ context.Context, mutation BeginMutation) (Reservation, error) {
	s.begin = mutation
	request := cloneRequest(s.candidate.Request)
	request.DecisionStatus = DecisionProcessing
	request.DecisionSource = decisionSourcePointer(SourceAutomatic)
	request.Version++
	review := mutation.ReviewSnapshots[request.ID]
	request.AutomaticReview = &review
	decision := Decision{
		ID: "dec_test", RequestID: request.ID, Action: mutation.Action, Source: SourceAutomatic,
		Status: AttemptStarted, Actor: &audit.Actor{Type: audit.ActorSystem, DisplayName: "automatic_join_rejection"},
		Reason: mutation.Reason, RuleVersion: mutation.RuleVersion, FieldSnapshot: cloneApplicantFieldsPointer(request.AIParse.Fields),
		ReviewSnapshot: &review, StartedAt: mutation.Context.OccurredAt.UTC(), TraceID: "trace_test",
	}
	return Reservation{Items: []ReservedItem{{Request: request, Decision: decision}}}, nil
}

func (s *reviewStore) CompleteDecision(_ context.Context, mutation CompletionMutation) (DecisionResult, error) {
	request := cloneRequest(s.candidate.Request)
	request.DecisionStatus = mutation.DecisionStatus
	request.DecisionSource = decisionSourcePointer(SourceAutomatic)
	request.Version += 2
	review := s.begin.ReviewSnapshots[request.ID]
	request.AutomaticReview = &review
	completedAt := mutation.CompletedAt.UTC()
	decision := Decision{
		ID: mutation.DecisionID, RequestID: request.ID, Action: s.begin.Action, Source: SourceAutomatic,
		Status: mutation.AttemptStatus, Actor: &audit.Actor{Type: audit.ActorSystem, DisplayName: "automatic_join_rejection"},
		Reason: s.begin.Reason, RuleVersion: s.begin.RuleVersion, FieldSnapshot: cloneApplicantFieldsPointer(request.AIParse.Fields),
		ReviewSnapshot: &review, StartedAt: s.begin.Context.OccurredAt.UTC(), CompletedAt: &completedAt, TraceID: "trace_test",
	}
	return DecisionResult{Request: request, Decision: decision}, nil
}

func (s *reviewStore) IndexApprovedRequest(context.Context, string) error { return nil }

func decisionSourcePointer(value DecisionSource) *DecisionSource { return &value }

type reviewJudge struct {
	result MajorCodeJudgement
	err    error
}

func (j reviewJudge) Judge(context.Context, MajorCodeJudgeInput) (MajorCodeJudgement, error) {
	return j.result, j.err
}

type reviewApprover struct{ reason string }

func (a *reviewApprover) JoinRequestDecisionAvailable() bool { return true }
func (a *reviewApprover) DecideJoinRequest(_ context.Context, _ string, _ bool, reason string) ExternalResult {
	a.reason = reason
	return ExternalResult{Outcome: ExternalConfirmed}
}

type reviewReasonProvider struct{}

func (reviewReasonProvider) AutoRejectReason() string { return "legacy reason" }

type injectedRosterReader struct{ record AdmissionRosterRecord }

func (r injectedRosterReader) Lookup(context.Context, string) (AdmissionRosterRecord, error) {
	return r.record, nil
}

func validReviewCandidate(now time.Time) AutoCandidate {
	studentID, name, major := "302026315326", "测试同学", "计算机类"
	fields := ApplicantFields{StudentID: &studentID, Name: &name, Major: &major, Valid: true}
	return AutoCandidate{
		Request: Request{
			ID: "request_test", Group: GroupReference{ID: "123456", Name: "测试群"}, ApplicantQQ: "12345",
			VerificationMessage: "302026315326 测试同学 计算机类", SubType: SubTypeAdd, Source: RequestSourceEvent,
			ObservedStatus: ObservedPending, DecisionStatus: DecisionPending,
			AIParse:     AIParseResult{Status: AIParseSucceeded, Fields: &fields, CompletedAt: timePointer(now)},
			RequestedAt: now, Version: 1, FirstObservedAt: now, LastObservedAt: now,
		},
		Policy: Policy{GroupID: "123456", Enabled: true, Mode: PolicyModeAIFieldsComplete, RequiredFields: PolicyRequiredFields(), AutoReject: true, Version: 1, UpdatedAt: now},
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func TestAutomaticRejectKeepsReasonOutOfGateway(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := &reviewStore{candidate: validReviewCandidate(now), evidence: MajorEvidence{
		EnrollmentYear: "2026", MajorCode: "315", TotalSamples: 2, Version: 1,
		MajorCounts: []MajorCount{{Major: "计算机类", Count: 2}},
	}}
	approver := &reviewApprover{}
	service, err := NewService(Options{Store: store, Approver: approver, AutoRejectReasons: reviewReasonProvider{}, Now: func() time.Time { return now }, Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if err := service.ProcessAutoApprovals(context.Background()); err != nil {
		t.Fatal(err)
	}
	if approver.reason != "" {
		t.Fatalf("gateway received internal rejection reason %q", approver.reason)
	}
	if store.begin.Reason == nil || !strings.Contains(*store.begin.Reason, "少于最低要求") {
		t.Fatalf("internal decision did not retain rejection reason: %+v", store.begin.Reason)
	}
}

func TestAutomaticDecisionRequiresHighConfidenceMatch(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	store := &reviewStore{evidence: MajorEvidence{
		EnrollmentYear: "2026", MajorCode: "315", TotalSamples: 3, Version: 2,
		MajorCounts: []MajorCount{{Major: "计算机类", Count: 3}},
	}}
	service, err := NewService(Options{
		Store: store, Approver: &reviewApprover{}, AutoRejectReasons: reviewReasonProvider{}, Now: func() time.Time { return now }, Location: time.UTC,
		MajorCodeJudge: reviewJudge{result: MajorCodeJudgement{Decision: MajorCodeMatch, Confidence: ConfidenceHigh, Reason: "样本一致。"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	action, review, eligible, err := service.automaticDecision(context.Background(), validReviewCandidate(now).Request, validReviewCandidate(now).Policy)
	if err != nil || !eligible || action != ActionApprove || review.Outcome != ReviewPassed {
		t.Fatalf("expected approval, action=%s eligible=%v review=%+v err=%v", action, eligible, review, err)
	}
	service.majorCodeJudge = reviewJudge{err: errors.New("model unavailable")}
	_, review, eligible, err = service.automaticDecision(context.Background(), validReviewCandidate(now).Request, validReviewCandidate(now).Policy)
	if !errors.Is(err, ErrDependencyUnavailable) || eligible || review.Outcome != ReviewDependencyPending {
		t.Fatalf("model failure must stay pending, eligible=%v review=%+v err=%v", eligible, review, err)
	}
}

func TestCheckStudentIDUsesConfiguredTimezoneAtYearBoundary(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	instant := time.Date(2025, time.December, 31, 16, 30, 0, 0, time.UTC)
	check := CheckStudentID("302026315326", instant.In(location))
	if check.ExpectedYear != "2026" || !check.YearValid {
		t.Fatalf("expected Shanghai local year 2026, got %+v", check)
	}
}

func TestAutomaticDecisionRosterGates(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	candidate := validReviewCandidate(now)
	tests := []struct {
		name        string
		roster      AdmissionRosterRecord
		rosterErr   error
		wantCode    string
		wantOutcome ReviewOutcome
		wantError   bool
	}{
		{name: "student missing", roster: AdmissionRosterRecord{Configured: true, DatasetVersion: "roster-1"}, wantCode: "student_not_in_roster", wantOutcome: ReviewRejected},
		{name: "major mismatch", roster: AdmissionRosterRecord{Configured: true, Found: true, DatasetVersion: "roster-1", Major: "机械类"}, wantCode: "roster_major_mismatch", wantOutcome: ReviewRejected},
		{name: "dependency unavailable", rosterErr: errors.New("database unavailable"), wantCode: "roster_unavailable", wantOutcome: ReviewDependencyPending, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &reviewStore{roster: test.roster, rosterErr: test.rosterErr}
			service, err := NewService(Options{
				Store: store, Approver: &reviewApprover{}, AutoRejectReasons: reviewReasonProvider{},
				Now: func() time.Time { return now }, Location: time.UTC,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			action, review, eligible, decisionErr := service.automaticDecision(context.Background(), candidate.Request, candidate.Policy)
			if review.ReasonCode != test.wantCode || review.Outcome != test.wantOutcome {
				t.Fatalf("unexpected review: %+v", review)
			}
			if test.wantError {
				if !errors.Is(decisionErr, ErrDependencyUnavailable) || eligible || action != "" {
					t.Fatalf("dependency failure must remain pending: action=%s eligible=%v err=%v", action, eligible, decisionErr)
				}
			} else if decisionErr != nil || !eligible || action != ActionReject {
				t.Fatalf("roster rejection expected: action=%s eligible=%v err=%v", action, eligible, decisionErr)
			}
		})
	}
}

func TestAutomaticDecisionRejectsEveryNonHighMatch(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	candidate := validReviewCandidate(now)
	evidence := MajorEvidence{
		EnrollmentYear: "2026", MajorCode: "315", TotalSamples: 3, Version: 2,
		MajorCounts: []MajorCount{{Major: "计算机类", Count: 3}},
	}
	judgements := []MajorCodeJudgement{
		{Decision: MajorCodeMatch, Confidence: ConfidenceMedium, Reason: "置信度不足。"},
		{Decision: MajorCodeMatch, Confidence: ConfidenceLow, Reason: "置信度不足。"},
		{Decision: MajorCodeMismatch, Confidence: ConfidenceHigh, Reason: "样本不匹配。"},
		{Decision: MajorCodeUncertain, Confidence: ConfidenceLow, Reason: "样本存在冲突。"},
	}
	for _, judgement := range judgements {
		store := &reviewStore{evidence: evidence}
		service, err := NewService(Options{
			Store: store, Approver: &reviewApprover{}, AutoRejectReasons: reviewReasonProvider{},
			Now: func() time.Time { return now }, Location: time.UTC, MajorCodeJudge: reviewJudge{result: judgement},
		})
		if err != nil {
			t.Fatal(err)
		}
		action, review, eligible, decisionErr := service.automaticDecision(context.Background(), candidate.Request, candidate.Policy)
		service.Close()
		if decisionErr != nil || action != ActionReject || !eligible || review.Outcome != ReviewRejected {
			t.Fatalf("judgement %+v must reject: action=%s eligible=%v review=%+v err=%v", judgement, action, eligible, review, decisionErr)
		}
	}
}

func TestAutomaticDecisionRespectsIndependentPolicySwitches(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	candidate := validReviewCandidate(now)
	store := &reviewStore{evidence: MajorEvidence{
		EnrollmentYear: "2026", MajorCode: "315", TotalSamples: 3, Version: 2,
		MajorCounts: []MajorCount{{Major: "计算机类", Count: 3}},
	}}
	service, err := NewService(Options{
		Store: store, Approver: &reviewApprover{}, AutoRejectReasons: reviewReasonProvider{},
		Now: func() time.Time { return now }, Location: time.UTC,
		MajorCodeJudge: reviewJudge{result: MajorCodeJudgement{Decision: MajorCodeMatch, Confidence: ConfidenceHigh, Reason: "样本一致。"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	approveDisabled := candidate.Policy
	approveDisabled.Enabled = false
	action, review, eligible, err := service.automaticDecision(context.Background(), candidate.Request, approveDisabled)
	if err != nil || eligible || action != ActionApprove || review.Outcome != ReviewPassed {
		t.Fatalf("passed review must stay manual when approval is disabled: action=%s eligible=%v review=%+v err=%v", action, eligible, review, err)
	}

	invalid := candidate.Request
	invalid.AIParse.Fields = cloneApplicantFieldsPointer(candidate.Request.AIParse.Fields)
	invalid.AIParse.Fields.Valid = false
	rejectDisabled := candidate.Policy
	rejectDisabled.AutoReject = false
	action, review, eligible, err = service.automaticDecision(context.Background(), invalid, rejectDisabled)
	if err != nil || eligible || action != ActionReject || review.Outcome != ReviewRejected {
		t.Fatalf("failed review must stay manual when rejection is disabled: action=%s eligible=%v review=%+v err=%v", action, eligible, review, err)
	}
}

func TestAutomaticDecisionUsesInjectedRosterReader(t *testing.T) {
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	candidate := validReviewCandidate(now)
	store := &reviewStore{evidence: MajorEvidence{
		EnrollmentYear: "2026", MajorCode: "315", TotalSamples: 3, Version: 2,
		MajorCounts: []MajorCount{{Major: "计算机类", Count: 3}},
	}}
	service, err := NewService(Options{
		Store: store, Approver: &reviewApprover{}, AutoRejectReasons: reviewReasonProvider{},
		Now: func() time.Time { return now }, Location: time.UTC,
		RosterReader: injectedRosterReader{record: AdmissionRosterRecord{Configured: true, DatasetVersion: "external-v1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	action, review, eligible, err := service.automaticDecision(context.Background(), candidate.Request, candidate.Policy)
	if err != nil || action != ActionReject || !eligible || review.ReasonCode != "student_not_in_roster" ||
		review.Roster.DatasetVersion == nil || *review.Roster.DatasetVersion != "external-v1" {
		t.Fatalf("injected roster reader was not used: action=%s eligible=%v review=%+v err=%v", action, eligible, review, err)
	}
}
