package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/zjutjh/jxh-go/internal/audit"
	managersystem "github.com/zjutjh/jxh-go/internal/system"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const managerNapCatRestartOperation = "system.napcat_restart"

type managerRestartAuditMetadata struct {
	Phase     string `json:"phase"`
	Reason    string `json:"reason,omitempty"`
	Status    string `json:"status"`
	ErrorCode string `json:"error_code,omitempty"`
}

func (s *Store) BeginNapCatRestart(ctx context.Context, begin managersystem.BeginRestart) (
	operation managersystem.Operation,
	fresh bool,
	err error,
) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		requestedAt := begin.RequestedAt.UTC()
		var existing managerIdempotencyKey
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"actor_type = ? AND actor_id = ? AND operation = ? AND idempotency_key = ?",
			managerActorAdminUser, begin.Actor.UserID, managerNapCatRestartOperation, begin.IdempotencyKey,
		).Take(&existing).Error
		if findErr == nil {
			if !existing.ExpiresAt.After(requestedAt) {
				if deleteErr := tx.Delete(&existing).Error; deleteErr != nil {
					return deleteErr
				}
				findErr = gorm.ErrRecordNotFound
			} else {
				if existing.RequestHash != begin.RequestHash {
					return managersystem.ErrIdempotencyConflict
				}
				if existing.ResourceID == nil || *existing.ResourceID == "" {
					return errManagerInvalidState
				}
				var model managerSystemOperation
				if loadErr := tx.Where("operation_id = ? AND idempotency_id = ?", *existing.ResourceID, existing.ID).
					Take(&model).Error; loadErr != nil {
					return loadErr
				}
				operation = managerSystemOperationValue(model)
				return nil
			}
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		operationID, idErr := newManagerID("op")
		if idErr != nil {
			return idErr
		}
		auditContext, contextErr := managerAuditContextForMutation(tx, begin.Actor, begin.Context)
		if contextErr != nil {
			return contextErr
		}
		resourceType := "system_operation"
		idempotency := managerIdempotencyKey{
			ActorType: managerActorAdminUser, ActorID: begin.Actor.UserID, Operation: managerNapCatRestartOperation,
			IdempotencyKey: begin.IdempotencyKey, RequestHash: begin.RequestHash, State: managerIdempotencyInProgress,
			ResourceType: &resourceType, ResourceID: &operationID, TraceID: &operationID,
			CreatedAt: requestedAt, ExpiresAt: requestedAt.Add(managerIdempotencyTTL),
		}
		if createErr := tx.Create(&idempotency).Error; createErr != nil {
			if isManagerDuplicateKey(createErr) {
				return managersystem.ErrIdempotencyConflict
			}
			return createErr
		}
		model := managerSystemOperation{
			OperationID: operationID, Type: "napcat_restart", Status: string(managersystem.StatusAccepted),
			RequestedByType: managerActorAdminUser, RequestedBy: begin.Actor.UserID, IdempotencyID: &idempotency.ID,
			RequestID: begin.Context.RequestID, RequestedAt: requestedAt,
		}
		if createErr := tx.Create(&model).Error; createErr != nil {
			return createErr
		}
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: requestedAt, ScopeType: "system", Action: managerNapCatRestartOperation,
			TargetType: resourceType, TargetID: operationID, Result: audit.ResultSuccess,
			Metadata: managerRestartAuditMetadata{
				Phase: "requested", Reason: begin.Reason, Status: string(managersystem.StatusAccepted),
			},
		}); auditErr != nil {
			return auditErr
		}
		operation = managerSystemOperationValue(model)
		fresh = true
		return nil
	})
	return operation, fresh, err
}

func (s *Store) TransitionNapCatRestart(ctx context.Context, transition managersystem.Transition) (
	operation managersystem.Operation,
	err error,
) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reference managerSystemOperation
		if loadErr := tx.Select("operation_id", "idempotency_id").
			Where("operation_id = ? AND type = ?", transition.OperationID, "napcat_restart").Take(&reference).Error; loadErr != nil {
			return loadErr
		}
		if reference.IdempotencyID == nil {
			return errManagerInvalidState
		}
		var idempotency managerIdempotencyKey
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("idempotency_id = ?", *reference.IdempotencyID).Take(&idempotency).Error; loadErr != nil {
			return loadErr
		}
		var model managerSystemOperation
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operation_id = ? AND type = ?", transition.OperationID, "napcat_restart").Take(&model).Error; loadErr != nil {
			return loadErr
		}
		if model.Status == string(transition.To) {
			storedErrorCode := ""
			if model.ErrorCode != nil {
				storedErrorCode = *model.ErrorCode
			}
			if managerRestartTerminal(transition.To) && storedErrorCode == transition.ErrorCode &&
				idempotency.State == managerIdempotencyCompleted && idempotency.ResultStatus != nil &&
				*idempotency.ResultStatus == string(transition.To) {
				operation = managerSystemOperationValue(model)
				return nil
			}
			return fmt.Errorf("%w: restart transition already reached %s", errManagerInvalidState, transition.To)
		}
		if idempotency.State != managerIdempotencyInProgress {
			return errManagerInvalidState
		}
		if model.Status != string(transition.From) {
			return fmt.Errorf("%w: restart status is %s, expected %s", errManagerInvalidState, model.Status, transition.From)
		}
		if !managerValidRestartTransition(transition.From, transition.To) {
			return fmt.Errorf("%w: restart transition %s to %s", errManagerInvalidState, transition.From, transition.To)
		}
		terminal := managerRestartTerminal(transition.To)
		completedAt := transition.At.UTC()
		updates := map[string]any{"status": transition.To}
		if terminal {
			updates["completed_at"] = completedAt
			if transition.ErrorCode == "" {
				updates["error_code"] = nil
			} else {
				updates["error_code"] = transition.ErrorCode
			}
		} else {
			updates["completed_at"] = nil
			updates["error_code"] = nil
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
		model.CompletedAt = nil
		model.ErrorCode = nil
		if terminal {
			model.CompletedAt = &completedAt
			model.ErrorCode = optionalString(transition.ErrorCode)
		}
		if terminal {
			if model.IdempotencyID == nil || *model.IdempotencyID != idempotency.ID {
				return errManagerInvalidState
			}
			if idempotency.State != managerIdempotencyInProgress {
				return errManagerInvalidState
			}
			resultStatus := string(transition.To)
			responseStatus := managerRestartResponseStatus(transition.To)
			idempotencyUpdate := tx.Model(&managerIdempotencyKey{}).
				Where("idempotency_id = ? AND state = ?", idempotency.ID, managerIdempotencyInProgress).
				Updates(map[string]any{
					"state": managerIdempotencyCompleted, "result_status": resultStatus,
					"response_status": responseStatus, "error_code": model.ErrorCode, "completed_at": completedAt,
				})
			if idempotencyUpdate.Error != nil {
				return idempotencyUpdate.Error
			}
			if idempotencyUpdate.RowsAffected != 1 {
				return errManagerInvalidState
			}
			auditContext, contextErr := findManagerAuditContext(tx, managerNapCatRestartOperation, model.OperationID)
			if contextErr != nil {
				return contextErr
			}
			if auditErr := insertManagerAudit(tx, managerAuditEntry{
				Context: auditContext, OccurredAt: completedAt, ScopeType: "system", Action: managerNapCatRestartOperation,
				TargetType: "system_operation", TargetID: model.OperationID, Result: managerRestartAuditResult(transition.To),
				ErrorCode: transition.ErrorCode, Metadata: managerRestartAuditMetadata{
					Phase: "completed", Status: string(transition.To), ErrorCode: transition.ErrorCode,
				},
			}); auditErr != nil {
				return auditErr
			}
		}
		operation = managerSystemOperationValue(model)
		return nil
	})
	return operation, err
}

func managerSystemOperationValue(model managerSystemOperation) managersystem.Operation {
	value := managersystem.Operation{
		ID: model.OperationID, Type: model.Type, Status: managersystem.OperationStatus(model.Status),
		RequestedAt: model.RequestedAt.UTC(), ErrorCode: cloneManagerString(model.ErrorCode),
	}
	if model.CompletedAt != nil {
		completedAt := model.CompletedAt.UTC()
		value.CompletedAt = &completedAt
	}
	return value
}

func managerRestartTerminal(status managersystem.OperationStatus) bool {
	return status == managersystem.StatusSucceeded || status == managersystem.StatusFailed || status == managersystem.StatusUnknown
}

func managerValidRestartTransition(from, to managersystem.OperationStatus) bool {
	switch from {
	case managersystem.StatusAccepted:
		return to == managersystem.StatusRunning || to == managersystem.StatusUnknown
	case managersystem.StatusRunning:
		return managerRestartTerminal(to)
	default:
		return false
	}
}

func managerRestartResponseStatus(status managersystem.OperationStatus) uint16 {
	_ = status
	return 202
}

func managerRestartAuditResult(status managersystem.OperationStatus) audit.Result {
	switch status {
	case managersystem.StatusSucceeded:
		return audit.ResultSuccess
	case managersystem.StatusFailed:
		return audit.ResultFailed
	default:
		return audit.ResultUnknown
	}
}
