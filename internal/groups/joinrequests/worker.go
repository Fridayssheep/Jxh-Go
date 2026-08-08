package joinrequests

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/platform/safego"
)

const (
	defaultAutoApprovalInterval = 5 * time.Second
	defaultAutoApprovalBatch    = 20
	defaultRecoveryBatch        = 100
)

func (s *Service) RunAutoApprover(ctx context.Context) {
	s.runAutoApprovalRound(ctx)
	ticker := time.NewTicker(defaultAutoApprovalInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.runAutoApprovalRound(ctx)
		}
	}
}

func (s *Service) ProcessAutoApprovals(ctx context.Context) error {
	if !s.approver.JoinRequestDecisionAvailable() {
		return ErrDependencyUnavailable
	}
	candidates, err := s.store.ListAutoCandidates(ctx, defaultAutoApprovalBatch)
	if err != nil {
		return fmt.Errorf("list automatic join approval candidates: %w", err)
	}
	var processErrors []error
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return errors.Join(append(processErrors, ctx.Err())...)
		}
		request := s.normalizeRequest(candidate.Request)
		action, review, eligible, reviewErr := s.automaticDecision(ctx, request, candidate.Policy)
		if reviewErr != nil {
			processErrors = append(processErrors, reviewErr)
			continue
		}
		if !eligible {
			continue
		}
		fields := cloneApplicantFields(*request.AIParse.Fields)
		ruleVersion := AutoApprovalRuleVersion
		key := automaticDecisionKey(request.ID, request.Version, ruleVersion, action)
		policyRevision := candidate.Policy.Version
		startedAt := s.now().UTC()
		actorName := "automatic_join_approval"
		if action == ActionReject {
			actorName = "automatic_join_rejection"
		}
		reservation, err := s.store.BeginDecisions(ctx, BeginMutation{
			Context: MutationContext{
				Actor:   audit.Actor{Type: audit.ActorSystem, DisplayName: actorName},
				Request: auth.MutationContext{RequestID: key}, OccurredAt: startedAt,
			},
			GroupID: request.Group.ID, Items: []VersionedRequest{{ID: request.ID, Version: request.Version}},
			Action: action, Source: SourceAutomatic, Reason: &review.Reason, IdempotencyKey: key,
			ProcessingExpiresAt: startedAt.Add(s.processingLease), PolicyRevision: &policyRevision, RuleVersion: &ruleVersion,
			FieldSnapshots:  map[string]ApplicantFields{request.ID: fields},
			ReviewSnapshots: map[string]AutomaticReview{request.ID: review},
		})
		if err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIdempotencyConflict) {
				continue
			}
			processErrors = append(processErrors, fmt.Errorf("begin automatic join decision: %w", err))
			continue
		}
		if len(reservation.Items) != 1 {
			processErrors = append(processErrors, ErrInvalidData)
			continue
		}
		item := reservation.Items[0]
		if reservation.Replay {
			if !validRequest(item.Request, true) || !validDecision(item.Decision) ||
				item.Request.ID != request.ID || item.Decision.RequestID != request.ID || item.Decision.Action != action {
				processErrors = append(processErrors, ErrInvalidData)
			}
			continue
		}
		if !validReservedItem(item, request.ID, action) {
			processErrors = append(processErrors, ErrInvalidData)
			continue
		}
		if _, err := s.execute(ctx, item, ""); err != nil &&
			!errors.Is(err, ErrExternalFailure) && !errors.Is(err, ErrDependencyUnavailable) {
			processErrors = append(processErrors, err)
		}
	}
	return errors.Join(processErrors...)
}

func (s *Service) RecoverInterrupted(ctx context.Context) error {
	requests, err := s.store.RecoverExpiredDecisions(ctx, s.now().UTC(), defaultRecoveryBatch)
	if err != nil {
		return fmt.Errorf("recover interrupted join request decisions: %w", err)
	}
	for _, request := range requests {
		if !validRequest(request, true) || request.DecisionStatus != DecisionUnknown {
			return ErrInvalidData
		}
		s.publish(request.ID, request.Version, "join_request_decision_recovered")
	}
	return nil
}

func (s *Service) runAutoApprovalRound(ctx context.Context) {
	defer safego.Recover("automatic join approval")
	if err := s.RecoverInterrupted(ctx); err != nil && ctx.Err() == nil {
		log.Printf("recover interrupted join request decisions failed: %v", err)
	}
	if err := s.ProcessAutoApprovals(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, ErrDependencyUnavailable) {
		log.Printf("automatic join approval round failed: %v", err)
	}
}

func (s *Service) automaticDecision(ctx context.Context, request Request, policy Policy) (Action, AutomaticReview, bool, error) {
	if !validRequest(request, true) || !validPolicy(policy) || policy.GroupID != request.Group.ID ||
		request.SubType != SubTypeAdd || request.DecisionStatus != DecisionPending || request.AIParse.Status != AIParseSucceeded ||
		request.AIParse.Fields == nil {
		return "", AutomaticReview{}, false, nil
	}
	now := s.now().In(s.location)
	review := AutomaticReview{
		RuleVersion: AutoApprovalRuleVersion, Roster: RosterAssessment{Status: RosterNotConfigured}, ReviewedAt: now.UTC(),
	}
	studentID := optionalValue(request.AIParse.Fields.StudentID)
	review.StudentID = CheckStudentID(studentID, now)
	rosterMajor := ""
	reject := func(code, reason string) (Action, AutomaticReview, bool, error) {
		review.Outcome, review.ReasonCode, review.Reason = ReviewRejected, code, reason
		return ActionReject, review, policy.AutoReject, nil
	}
	if !request.AIParse.Fields.Valid {
		return reject("applicant_fields_invalid", "AI 提取的姓名、学号或专业不完整，无法通过自动校验。")
	}
	if !review.StudentID.LengthValid {
		return reject("student_id_length_invalid", "学号长度必须为 12 位。")
	}
	if !review.StudentID.Numeric {
		return reject("student_id_not_numeric", "学号必须全部由数字组成。")
	}
	if !review.StudentID.YearValid {
		return reject("enrollment_year_mismatch", fmt.Sprintf("学号中的入学年份为 %s，当前应为 %s。", review.StudentID.EnrollmentYear, review.StudentID.ExpectedYear))
	}
	roster, err := s.rosterReader.Lookup(ctx, studentID)
	if err != nil {
		review.Outcome, review.Roster.Status, review.ReasonCode, review.Reason = ReviewDependencyPending, RosterUnavailable, "roster_unavailable", "录取名单服务暂时不可用，等待重试。"
		return "", review, false, fmt.Errorf("lookup admission roster: %w", ErrDependencyUnavailable)
	}
	if roster.Configured {
		version := roster.DatasetVersion
		review.Roster.DatasetVersion = &version
		if !roster.Found {
			review.Roster.Status = RosterNotFound
			return reject("student_not_in_roster", "当前录取名单中未找到该学号。")
		}
		review.Roster.Status = RosterMatched
		// A roster major that differs from what the applicant typed is not decisive on its
		// own: the same major is written many ways ("计算机类", "计算机类卓越班",
		// "计算机科学与技术"). Only mechanically equal names short-circuit here; anything
		// else is recorded and handed to the AI judge below, which sees the roster major as
		// authoritative context and decides whether the two names denote one major.
		if roster.Major != "" && !majorNamesRelated(roster.Major, optionalValue(request.AIParse.Fields.Major)) {
			review.Roster.Status = RosterMajorMismatch
			rosterMajor = roster.Major
		}
	}
	evidence, err := s.store.GetMajorEvidence(ctx, review.StudentID.EnrollmentYear, review.StudentID.MajorCode)
	if err != nil {
		review.Outcome, review.ReasonCode, review.Reason = ReviewDependencyPending, "evidence_unavailable", "专业代码证据库暂时不可用，等待重试。"
		return "", review, false, fmt.Errorf("load major code evidence: %w", ErrDependencyUnavailable)
	}
	review.Evidence = &evidence
	if evidence.TotalSamples < MinimumEvidenceSamples {
		judgement := MajorCodeJudgement{Decision: MajorCodeUncertain, Confidence: ConfidenceLow, Reason: fmt.Sprintf("同届该专业代码仅有 %d 条有效样本，少于最低要求 %d 条。", evidence.TotalSamples, MinimumEvidenceSamples)}
		review.Judgement = &judgement
		return reject("major_evidence_insufficient", judgement.Reason)
	}
	if s.majorCodeJudge == nil {
		// Without the judge an unresolved roster disagreement has nothing left to resolve
		// it, so fall back to the deterministic rejection rather than approving blindly.
		if rosterMajor != "" {
			return reject("roster_major_mismatch", "申请专业与录取名单中的专业不一致，且 AI 专业判断服务未配置。")
		}
		review.Outcome, review.ReasonCode, review.Reason = ReviewDependencyPending, "major_judge_unavailable", "AI 专业判断服务未配置，等待人工处理或服务恢复。"
		return "", review, false, fmt.Errorf("judge major code: %w", ErrDependencyUnavailable)
	}
	judgement, err := s.majorCodeJudge.Judge(ctx, MajorCodeJudgeInput{
		EnrollmentYear: evidence.EnrollmentYear, MajorCode: evidence.MajorCode,
		ApplicantMajor: optionalValue(request.AIParse.Fields.Major), TotalSamples: evidence.TotalSamples,
		MajorCounts: append([]MajorCount(nil), evidence.MajorCounts...), EvidenceVersion: evidence.Version,
		RosterMajor: rosterMajor,
	})
	if err != nil {
		review.Outcome, review.ReasonCode, review.Reason = ReviewDependencyPending, "major_judge_unavailable", "AI 专业判断服务暂时不可用，等待重试。"
		return "", review, false, fmt.Errorf("judge major code: %w", ErrDependencyUnavailable)
	}
	review.Judgement = &judgement
	if judgement.Decision != MajorCodeMatch || judgement.Confidence != ConfidenceHigh {
		if rosterMajor != "" {
			return reject("roster_major_mismatch", judgement.Reason)
		}
		return reject("major_code_mismatch", judgement.Reason)
	}
	if rosterMajor != "" {
		// The judge resolved the textual disagreement in the applicant's favour, so the
		// roster is recorded as matched rather than leaving a mismatch on an approval.
		review.Roster.Status = RosterMatched
	}
	review.Outcome, review.ReasonCode, review.Reason = ReviewPassed, "automatic_review_passed", judgement.Reason
	return ActionApprove, review, policy.Enabled, nil
}

func automaticDecisionKey(requestID string, requestVersion, ruleVersion uint64, action Action) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", requestID, requestVersion, ruleVersion, action)))
	return "auto-" + base64.RawURLEncoding.EncodeToString(digest[:])
}
