package joinrequests

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/safego"
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
		if !eligibleAutoCandidate(request, candidate.Policy) {
			continue
		}
		fields := cloneApplicantFields(*request.AIParse.Fields)
		ruleVersion := AutoApprovalRuleVersion
		reason := "all_required_ai_fields_valid"
		key := autoApprovalKey(request.ID, request.Version, ruleVersion)
		startedAt := s.now().UTC()
		reservation, err := s.store.BeginDecisions(ctx, BeginMutation{
			Context: MutationContext{
				Actor:   audit.Actor{Type: audit.ActorSystem, DisplayName: "automatic_join_approval"},
				Request: auth.MutationContext{RequestID: key}, OccurredAt: startedAt,
			},
			GroupID: request.Group.ID, Items: []VersionedRequest{{ID: request.ID, Version: request.Version}},
			Action: ActionApprove, Source: SourceAutomatic, Reason: &reason, IdempotencyKey: key,
			ProcessingExpiresAt: startedAt.Add(s.processingLease), RuleVersion: &ruleVersion,
			FieldSnapshots: map[string]ApplicantFields{request.ID: fields},
		})
		if err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrIdempotencyConflict) {
				continue
			}
			processErrors = append(processErrors, fmt.Errorf("begin automatic join approval: %w", err))
			continue
		}
		if len(reservation.Items) != 1 {
			processErrors = append(processErrors, ErrInvalidData)
			continue
		}
		item := reservation.Items[0]
		if reservation.Replay {
			if !validRequest(item.Request, true) || !validDecision(item.Decision) ||
				item.Request.ID != request.ID || item.Decision.RequestID != request.ID || item.Decision.Action != ActionApprove {
				processErrors = append(processErrors, ErrInvalidData)
			}
			continue
		}
		if !validReservedItem(item, request.ID, ActionApprove) {
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

func eligibleAutoCandidate(request Request, policy Policy) bool {
	return validRequest(request, true) && validPolicy(policy) && policy.GroupID == request.Group.ID && policy.Enabled &&
		request.SubType == SubTypeAdd && request.DecisionStatus == DecisionPending && request.AIParse.Status == AIParseSucceeded &&
		request.AIParse.Fields != nil && request.AIParse.Fields.Valid
}

func autoApprovalKey(requestID string, requestVersion, ruleVersion uint64) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", requestID, requestVersion, ruleVersion)))
	return fmt.Sprintf("auto-%x", digest[:])
}
