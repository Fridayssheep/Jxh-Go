package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge/knowledgeadmin"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const managerKnowledgeReloadOperation = "knowledge.reload"

var _ knowledgeadmin.OperationStore = (*Store)(nil)

type managerKnowledgeAuditMetadata struct {
	Phase     string `json:"phase"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

func (s *Store) BeginKnowledgeReload(ctx context.Context, begin knowledgeadmin.BeginReload) (
	operation knowledgeadmin.ReloadOperation,
	fresh bool,
	err error,
) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var migration struct {
			Version uint64 `gorm:"column:version"`
		}
		if lockErr := tx.Table("schema_migrations").Clauses(clause.Locking{Strength: "UPDATE"}).
			Select("version").Order("version ASC").Limit(1).Take(&migration).Error; lockErr != nil {
			return lockErr
		}

		requestedAt := begin.RequestedAt.UTC()
		var existing managerIdempotencyKey
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"actor_type = ? AND actor_id = ? AND operation = ? AND idempotency_key = ?",
			managerActorAdminUser, begin.Actor.UserID, managerKnowledgeReloadOperation, begin.IdempotencyKey,
		).Take(&existing).Error
		if findErr == nil {
			if !existing.ExpiresAt.After(requestedAt) && existing.State == managerIdempotencyCompleted {
				if deleteErr := tx.Delete(&existing).Error; deleteErr != nil {
					return deleteErr
				}
				findErr = gorm.ErrRecordNotFound
			} else {
				if existing.RequestHash != begin.RequestHash {
					return knowledgeadmin.ErrIdempotencyConflict
				}
				if existing.ResourceID == nil || *existing.ResourceID == "" {
					return errManagerInvalidState
				}
				var model managerSystemOperation
				if loadErr := tx.Where("operation_id = ? AND idempotency_id = ? AND type = ?",
					*existing.ResourceID, existing.ID, "knowledge_reload").Take(&model).Error; loadErr != nil {
					return loadErr
				}
				operation = managerKnowledgeOperationValue(model)
				return nil
			}
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}

		var active managerSystemOperation
		activeErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("type = ? AND status IN ?", "knowledge_reload", []string{
				string(knowledgeadmin.OperationAccepted), string(knowledgeadmin.OperationRunning),
			}).Order("requested_at ASC").Order("operation_id ASC").Limit(1).Take(&active).Error
		if activeErr == nil {
			return knowledgeadmin.ErrReloadInProgress
		}
		if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}

		auditContext, contextErr := managerAuditContextForMutation(tx, begin.Actor, begin.Context)
		if contextErr != nil {
			return contextErr
		}
		resourceType := "knowledge_reload"
		idempotency := managerIdempotencyKey{
			ActorType: managerActorAdminUser, ActorID: begin.Actor.UserID, Operation: managerKnowledgeReloadOperation,
			IdempotencyKey: begin.IdempotencyKey, RequestHash: begin.RequestHash, State: managerIdempotencyInProgress,
			ResourceType: &resourceType, ResourceID: &begin.OperationID, TraceID: &begin.OperationID,
			CreatedAt: requestedAt, ExpiresAt: requestedAt.Add(managerIdempotencyTTL),
		}
		if createErr := tx.Create(&idempotency).Error; createErr != nil {
			if isManagerDuplicateKey(createErr) {
				return knowledgeadmin.ErrIdempotencyConflict
			}
			return createErr
		}
		requestID := begin.Context.RequestID
		if requestID == "" {
			requestID = begin.OperationID
		}
		model := managerSystemOperation{
			OperationID: begin.OperationID, Type: "knowledge_reload", Status: string(knowledgeadmin.OperationAccepted),
			RequestedByType: managerActorAdminUser, RequestedBy: begin.Actor.UserID, IdempotencyID: &idempotency.ID,
			RequestID: requestID, RequestedAt: requestedAt,
		}
		if createErr := tx.Create(&model).Error; createErr != nil {
			return createErr
		}
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: requestedAt, ScopeType: "knowledge", Action: managerKnowledgeReloadOperation,
			TargetType: resourceType, TargetID: begin.OperationID, Result: audit.ResultSuccess,
			Metadata: managerKnowledgeAuditMetadata{Phase: "requested", Status: string(knowledgeadmin.OperationAccepted)},
		}); auditErr != nil {
			return auditErr
		}
		operation = managerKnowledgeOperationValue(model)
		fresh = true
		return nil
	})
	return operation, fresh, err
}

func (s *Store) TransitionKnowledgeReload(ctx context.Context, transition knowledgeadmin.ReloadTransition) (
	operation knowledgeadmin.ReloadOperation,
	err error,
) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model managerSystemOperation
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operation_id = ? AND type = ?", transition.OperationID, "knowledge_reload").Take(&model).Error; loadErr != nil {
			return loadErr
		}
		if model.IdempotencyID == nil {
			return errManagerInvalidState
		}
		var idempotency managerIdempotencyKey
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("idempotency_id = ?", *model.IdempotencyID).Take(&idempotency).Error; loadErr != nil {
			return loadErr
		}
		if model.Status == string(transition.To) && managerKnowledgeTerminal(transition.To) &&
			idempotency.State == managerIdempotencyCompleted {
			operation = managerKnowledgeOperationValue(model)
			return nil
		}
		if model.Status != string(transition.From) || idempotency.State != managerIdempotencyInProgress ||
			!managerValidKnowledgeTransition(transition.From, transition.To) {
			return fmt.Errorf("%w: invalid knowledge reload transition", errManagerInvalidState)
		}

		completedAt := transition.At.UTC()
		terminal := managerKnowledgeTerminal(transition.To)
		updates := map[string]any{"status": transition.To}
		if terminal {
			updates["completed_at"] = completedAt
			updates["error_code"] = optionalString(transition.ErrorCode)
		}
		update := tx.Model(&managerSystemOperation{}).
			Where("operation_id = ? AND status = ?", model.OperationID, transition.From).Updates(updates)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return errManagerInvalidState
		}
		model.Status = string(transition.To)
		if terminal {
			model.CompletedAt = &completedAt
			model.ErrorCode = optionalString(transition.ErrorCode)
			resultStatus := string(transition.To)
			if transition.OutcomeUnknown {
				resultStatus = "unknown"
			}
			idempotencyUpdate := tx.Model(&managerIdempotencyKey{}).
				Where("idempotency_id = ? AND state = ?", idempotency.ID, managerIdempotencyInProgress).
				Updates(map[string]any{
					"state": managerIdempotencyCompleted, "result_status": resultStatus,
					"response_status": uint16(202), "error_code": model.ErrorCode, "completed_at": completedAt,
				})
			if idempotencyUpdate.Error != nil {
				return idempotencyUpdate.Error
			}
			if idempotencyUpdate.RowsAffected != 1 {
				return errManagerInvalidState
			}
			auditContext, contextErr := findManagerAuditContext(tx, managerKnowledgeReloadOperation, model.OperationID)
			if contextErr != nil {
				return contextErr
			}
			auditResult := audit.ResultFailed
			if transition.To == knowledgeadmin.OperationSucceeded {
				auditResult = audit.ResultSuccess
			} else if transition.OutcomeUnknown {
				auditResult = audit.ResultUnknown
			}
			if auditErr := insertManagerAudit(tx, managerAuditEntry{
				Context: auditContext, OccurredAt: completedAt, ScopeType: "knowledge", Action: managerKnowledgeReloadOperation,
				TargetType: "knowledge_reload", TargetID: model.OperationID, Result: auditResult,
				ErrorCode: transition.ErrorCode, Metadata: managerKnowledgeAuditMetadata{
					Phase: "completed", Status: string(transition.To), ErrorCode: transition.ErrorCode,
				},
			}); auditErr != nil {
				return auditErr
			}
		}
		operation = managerKnowledgeOperationValue(model)
		return nil
	})
	return operation, err
}

func (s *Store) RecoverInterruptedKnowledgeReloads(ctx context.Context, recoveredAt time.Time) (
	operations []knowledgeadmin.ReloadOperation,
	err error,
) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var models []managerSystemOperation
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("type = ? AND status IN ?", "knowledge_reload", []string{
				string(knowledgeadmin.OperationAccepted), string(knowledgeadmin.OperationRunning),
			}).Order("requested_at ASC").Order("operation_id ASC").Find(&models).Error; loadErr != nil {
			return loadErr
		}
		completedAt := recoveredAt.UTC()
		const errorCode = "reload_interrupted"
		for _, model := range models {
			if model.IdempotencyID == nil {
				return errManagerInvalidState
			}
			updated := tx.Model(&managerSystemOperation{}).
				Where("operation_id = ? AND status IN ?", model.OperationID, []string{
					string(knowledgeadmin.OperationAccepted), string(knowledgeadmin.OperationRunning),
				}).Updates(map[string]any{
				"status": knowledgeadmin.OperationFailed, "completed_at": completedAt, "error_code": errorCode,
			})
			if updated.Error != nil {
				return updated.Error
			}
			if updated.RowsAffected != 1 {
				continue
			}
			idempotencyUpdate := tx.Model(&managerIdempotencyKey{}).
				Where("idempotency_id = ? AND state = ?", *model.IdempotencyID, managerIdempotencyInProgress).
				Updates(map[string]any{
					"state": managerIdempotencyCompleted, "result_status": "unknown",
					"response_status": uint16(202), "error_code": errorCode, "completed_at": completedAt,
				})
			if idempotencyUpdate.Error != nil {
				return idempotencyUpdate.Error
			}
			if idempotencyUpdate.RowsAffected != 1 {
				return errManagerInvalidState
			}
			auditContext, contextErr := findManagerAuditContext(tx, managerKnowledgeReloadOperation, model.OperationID)
			if contextErr != nil {
				return contextErr
			}
			if auditErr := insertManagerAudit(tx, managerAuditEntry{
				Context: auditContext, OccurredAt: completedAt, ScopeType: "knowledge", Action: managerKnowledgeReloadOperation,
				TargetType: "knowledge_reload", TargetID: model.OperationID, Result: audit.ResultUnknown,
				ErrorCode: errorCode, Metadata: managerKnowledgeAuditMetadata{
					Phase: "completed", Status: string(knowledgeadmin.OperationFailed), ErrorCode: errorCode,
				},
			}); auditErr != nil {
				return auditErr
			}
			model.Status = string(knowledgeadmin.OperationFailed)
			model.CompletedAt = &completedAt
			model.ErrorCode = optionalString(errorCode)
			operations = append(operations, managerKnowledgeOperationValue(model))
		}
		return nil
	})
	return operations, err
}

func managerKnowledgeOperationValue(model managerSystemOperation) knowledgeadmin.ReloadOperation {
	operation := knowledgeadmin.ReloadOperation{
		ID: model.OperationID, Status: knowledgeadmin.OperationStatus(model.Status), StartedAt: model.RequestedAt.UTC(),
		CompletedAt: cloneManagerTime(model.CompletedAt), ErrorCode: cloneManagerString(model.ErrorCode),
	}
	return operation
}

func managerKnowledgeTerminal(status knowledgeadmin.OperationStatus) bool {
	return status == knowledgeadmin.OperationSucceeded || status == knowledgeadmin.OperationFailed
}

func managerValidKnowledgeTransition(from, to knowledgeadmin.OperationStatus) bool {
	if from == knowledgeadmin.OperationAccepted {
		return to == knowledgeadmin.OperationRunning || to == knowledgeadmin.OperationFailed
	}
	return from == knowledgeadmin.OperationRunning && managerKnowledgeTerminal(to)
}
