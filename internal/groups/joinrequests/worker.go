package joinrequests

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"strings"
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
		action, reason, gatewayReason, eligible := s.automaticDecision(request, candidate.Policy)
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
			Action: action, Source: SourceAutomatic, Reason: &reason, IdempotencyKey: key,
			ProcessingExpiresAt: startedAt.Add(s.processingLease), PolicyRevision: &policyRevision, RuleVersion: &ruleVersion,
			FieldSnapshots: map[string]ApplicantFields{request.ID: fields},
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
		if _, err := s.execute(ctx, item, gatewayReason); err != nil &&
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

func (s *Service) automaticDecision(request Request, policy Policy) (Action, string, string, bool) {
	if !validRequest(request, true) || !validPolicy(policy) || policy.GroupID != request.Group.ID ||
		request.SubType != SubTypeAdd || request.DecisionStatus != DecisionPending || request.AIParse.Status != AIParseSucceeded ||
		request.AIParse.Fields == nil {
		return "", "", "", false
	}
	if request.AIParse.Fields.Valid && policy.Enabled {
		return ActionApprove, "all_required_ai_fields_valid", "", true
	}
	if !request.AIParse.Fields.Valid && policy.AutoReject {
		reason := s.autoRejectReasons.AutoRejectReason()
		if !validText(reason, 500, false) || strings.TrimSpace(reason) != reason {
			return "", "", "", false
		}
		return ActionReject, reason, reason, true
	}
	return "", "", "", false
}

func automaticDecisionKey(requestID string, requestVersion, ruleVersion uint64, action Action) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d\x00%s", requestID, requestVersion, ruleVersion, action)))
	return "auto-" + base64.RawURLEncoding.EncodeToString(digest[:])
}
