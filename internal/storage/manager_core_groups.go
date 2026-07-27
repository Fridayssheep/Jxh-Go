package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/settings"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const managerGroupSyncOperation = "groups.sync"

type managerSyncAuditMetadata struct {
	Phase        string     `json:"phase"`
	SyncedAt     *time.Time `json:"synced_at,omitempty"`
	AddedCount   uint64     `json:"added_count"`
	UpdatedCount uint64     `json:"updated_count"`
	RemovedCount uint64     `json:"removed_count"`
	TotalCount   uint64     `json:"total_count"`
	ErrorCode    string     `json:"error_code,omitempty"`
}

type managerRemoteGroup struct {
	ID             int64
	Name           string
	MemberCount    uint64
	MaxMemberCount uint64
	BotRole        groups.Role
}

func (s *Store) ListGroups(ctx context.Context, query groups.StoreListQuery) (page groups.Page, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		groupsQuery := tx.Where("archived_at IS NULL")
		if query.Cursor != "" {
			cursor, parseErr := parseManagerGroupID(query.Cursor)
			if parseErr != nil {
				return parseErr
			}
			groupsQuery = groupsQuery.Where("group_id > ?", cursor)
		}
		if query.Query != "" {
			pattern := "%" + escapeManagerCoreLike(query.Query) + "%"
			groupsQuery = groupsQuery.Where(
				`name LIKE ? ESCAPE '\\' OR CAST(group_id AS CHAR) LIKE ? ESCAPE '\\'`, pattern, pattern,
			)
		}
		if query.BotRole != "" {
			groupsQuery = groupsQuery.Where("bot_role = ?", query.BotRole)
		}
		var models []managerManagedGroup
		if loadErr := groupsQuery.Order("group_id ASC").Find(&models).Error; loadErr != nil {
			return loadErr
		}
		_, globalFeatures, loadErr := loadManagerGlobalSettings(tx, false)
		if loadErr != nil {
			return loadErr
		}
		overridesByGroup, loadErr := loadManagerGroupOverrides(tx, models)
		if loadErr != nil {
			return loadErr
		}
		items := make([]groups.Group, 0, len(models))
		for _, model := range models {
			overrides := overridesByGroup[model.GroupID]
			item, mapErr := managerGroupFromModel(model, globalFeatures, overrides, query.ForceStale, query.StaleBefore)
			if mapErr != nil {
				return mapErr
			}
			if query.SnapshotState != "" && item.SnapshotState != query.SnapshotState {
				continue
			}
			if query.FeatureKey != "" && query.FeatureEnabled != nil {
				feature, ok := managerFindGroupFeature(item.Features, query.FeatureKey)
				if !ok || feature.Enabled != *query.FeatureEnabled {
					continue
				}
			}
			items = append(items, item)
		}
		limit := query.Limit
		if limit <= 0 {
			limit = 50
		}
		if len(items) > limit {
			page.HasMore = true
			items = items[:limit]
			page.NextCursor = items[len(items)-1].ID
		}
		page.Items = items
		return nil
	})
	return page, err
}

func (s *Store) GetGroup(ctx context.Context, id string) (value groups.Group, found bool, err error) {
	groupID, err := parseManagerGroupID(id)
	if err != nil {
		return groups.Group{}, false, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var model managerManagedGroup
		loadErr := tx.Where("group_id = ? AND archived_at IS NULL", groupID).Take(&model).Error
		if errors.Is(loadErr, gorm.ErrRecordNotFound) {
			return nil
		}
		if loadErr != nil {
			return loadErr
		}
		_, globalFeatures, loadErr := loadManagerGlobalSettings(tx, false)
		if loadErr != nil {
			return loadErr
		}
		setting, exists, loadErr := loadManagerGroupSetting(tx, groupID, false)
		if loadErr != nil {
			return loadErr
		}
		overrides := settings.Overrides{}
		if exists {
			overrides, loadErr = decodeManagerOverrides(setting.SettingsJSON)
			if loadErr != nil {
				return loadErr
			}
		}
		value, loadErr = managerGroupFromModel(model, globalFeatures, overrides, false, time.Time{})
		if loadErr != nil {
			return loadErr
		}
		found = true
		return nil
	})
	return value, found, err
}

func (s *Store) BeginGroupSync(ctx context.Context, begin groups.BeginSync) (reservation groups.SyncReservation, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		createdAt := begin.Context.OccurredAt.UTC()
		var syncLock managerFeatureSetting
		if lockErr := tx.Select("setting_id").Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("scope_type = ? AND group_id IS NULL", managerSettingsScopeGlobal).Take(&syncLock).Error; lockErr != nil {
			return lockErr
		}
		var existing managerIdempotencyKey
		findErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"actor_type = ? AND actor_id = ? AND operation = ? AND idempotency_key = ?",
			managerActorAdminUser, begin.Context.Actor.UserID, managerGroupSyncOperation, begin.IdempotencyKey,
		).Take(&existing).Error
		if findErr == nil {
			if !existing.ExpiresAt.After(createdAt) {
				if deleteErr := tx.Delete(&existing).Error; deleteErr != nil {
					return deleteErr
				}
				findErr = gorm.ErrRecordNotFound
			} else {
				if existing.RequestHash != managerGroupSyncRequestHash() {
					return groups.ErrIdempotencyConflict
				}
				var replayErr error
				reservation, replayErr = managerGroupSyncReservation(tx, existing)
				return replayErr
			}
		}
		if !errors.Is(findErr, gorm.ErrRecordNotFound) {
			return findErr
		}
		var active managerIdempotencyKey
		activeErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operation = ? AND state = ?", managerGroupSyncOperation, managerIdempotencyInProgress).
			Order("idempotency_id ASC").Take(&active).Error
		if activeErr == nil {
			return groups.ErrConflict
		}
		if !errors.Is(activeErr, gorm.ErrRecordNotFound) {
			return activeErr
		}
		executionID, idErr := newManagerID("sync")
		if idErr != nil {
			return idErr
		}
		auditContext, contextErr := managerAuditContextForMutation(tx, begin.Context.Actor, begin.Context.Request)
		if contextErr != nil {
			return contextErr
		}
		resourceType := "group_sync"
		model := managerIdempotencyKey{
			ActorType: managerActorAdminUser, ActorID: begin.Context.Actor.UserID, Operation: managerGroupSyncOperation,
			IdempotencyKey: begin.IdempotencyKey, RequestHash: managerGroupSyncRequestHash(),
			State: managerIdempotencyInProgress, ResourceType: &resourceType, ResourceID: &executionID,
			TraceID: &executionID, CreatedAt: createdAt, ExpiresAt: createdAt.Add(managerIdempotencyTTL),
		}
		if createErr := tx.Create(&model).Error; createErr != nil {
			if isManagerDuplicateKey(createErr) {
				return groups.ErrConflict
			}
			return createErr
		}
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: createdAt, ScopeType: "system", Action: managerGroupSyncOperation,
			TargetType: resourceType, TargetID: executionID, Result: audit.ResultSuccess,
			Metadata: managerSyncAuditMetadata{Phase: "requested"},
		}); auditErr != nil {
			return auditErr
		}
		reservation = groups.SyncReservation{ExecutionID: executionID, Fresh: true, InProgress: true}
		return nil
	})
	return reservation, err
}

func (s *Store) CompleteGroupSync(ctx context.Context, completion groups.CompleteSync) (result groups.SyncResult, err error) {
	remote, err := prepareManagerRemoteGroups(completion.Groups)
	if err != nil {
		return groups.SyncResult{}, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		idempotency, loadErr := loadManagerSyncIdempotency(tx, completion.ExecutionID)
		if loadErr != nil {
			return loadErr
		}
		if idempotency.State == managerIdempotencyCompleted {
			reservation, replayErr := managerGroupSyncReservation(tx, idempotency)
			if replayErr != nil {
				return replayErr
			}
			if reservation.Result == nil {
				return groups.ErrConflict
			}
			result = *reservation.Result
			return nil
		}
		if idempotency.State != managerIdempotencyInProgress {
			return errManagerInvalidState
		}
		auditContext, contextErr := findManagerAuditContext(tx, managerGroupSyncOperation, completion.ExecutionID)
		if contextErr != nil {
			return contextErr
		}
		var existingModels []managerManagedGroup
		if loadErr = tx.Clauses(clause.Locking{Strength: "UPDATE"}).Find(&existingModels).Error; loadErr != nil {
			return loadErr
		}
		existing := make(map[int64]managerManagedGroup, len(existingModels))
		for _, model := range existingModels {
			existing[model.GroupID] = model
		}
		completedAt := completion.CompletedAt.UTC()
		remoteIDs := make(map[int64]struct{}, len(remote))
		for _, group := range remote {
			remoteIDs[group.ID] = struct{}{}
			prior, exists := existing[group.ID]
			if !exists {
				model := managerManagedGroup{
					GroupID: group.ID, Name: group.Name, MemberCount: group.MemberCount, MaxMemberCount: group.MaxMemberCount,
					BotRole: string(group.BotRole), SnapshotState: string(groups.SnapshotFresh), LastSyncedAt: &completedAt,
					Revision: 1, CreatedAt: completedAt, UpdatedAt: completedAt,
				}
				if createErr := tx.Create(&model).Error; createErr != nil {
					return createErr
				}
				result.AddedCount++
				continue
			}
			if prior.ArchivedAt != nil {
				result.AddedCount++
			} else if prior.Name != group.Name || prior.MemberCount != group.MemberCount ||
				prior.MaxMemberCount != group.MaxMemberCount || prior.BotRole != string(group.BotRole) ||
				prior.SnapshotState != string(groups.SnapshotFresh) || prior.LastErrorCode != nil || prior.LastErrorMessage != nil {
				result.UpdatedCount++
			}
			updates := map[string]any{
				"name": group.Name, "member_count": group.MemberCount, "max_member_count": group.MaxMemberCount,
				"bot_role": group.BotRole, "snapshot_state": groups.SnapshotFresh, "last_error_code": nil,
				"last_error_message": nil, "last_synced_at": completedAt, "revision": prior.Revision + 1,
				"updated_at": completedAt, "archived_at": nil,
			}
			update := tx.Model(&managerManagedGroup{}).
				Where("group_id = ? AND revision = ?", group.ID, prior.Revision).Updates(updates)
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return groups.ErrConflict
			}
		}
		for _, prior := range existingModels {
			if prior.ArchivedAt != nil {
				continue
			}
			if _, retained := remoteIDs[prior.GroupID]; retained {
				continue
			}
			update := tx.Model(&managerManagedGroup{}).Where("group_id = ? AND revision = ?", prior.GroupID, prior.Revision).
				Updates(map[string]any{
					"snapshot_state": groups.SnapshotStale, "archived_at": completedAt,
					"revision": prior.Revision + 1, "updated_at": completedAt,
				})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return groups.ErrConflict
			}
			result.RemovedCount++
		}
		result.SyncedAt = completedAt
		result.TotalCount = uint64(len(remote))
		status := "succeeded"
		update := tx.Model(&managerIdempotencyKey{}).
			Where("idempotency_id = ? AND state = ?", idempotency.ID, managerIdempotencyInProgress).
			Updates(map[string]any{
				"state": managerIdempotencyCompleted, "result_status": status, "response_status": uint16(200),
				"error_code": nil, "completed_at": completedAt,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return groups.ErrConflict
		}
		metadata := managerSyncMetadataFromResult("completed", result, "")
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: completedAt, ScopeType: "system", Action: managerGroupSyncOperation,
			TargetType: "group_sync", TargetID: completion.ExecutionID, Result: audit.ResultSuccess, Metadata: metadata,
		}); auditErr != nil {
			return auditErr
		}
		return nil
	})
	return result, err
}

func (s *Store) FailGroupSync(ctx context.Context, failure groups.FailSync) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		idempotency, err := loadManagerSyncIdempotency(tx, failure.ExecutionID)
		if err != nil {
			return err
		}
		if idempotency.State == managerIdempotencyCompleted {
			if idempotency.ResultStatus != nil && (*idempotency.ResultStatus == "failed" || *idempotency.ResultStatus == "unknown") {
				return nil
			}
			return groups.ErrConflict
		}
		if idempotency.State != managerIdempotencyInProgress {
			return errManagerInvalidState
		}
		auditContext, err := findManagerAuditContext(tx, managerGroupSyncOperation, failure.ExecutionID)
		if err != nil {
			return err
		}
		completedAt := failure.CompletedAt.UTC()
		status := "failed"
		update := tx.Model(&managerIdempotencyKey{}).
			Where("idempotency_id = ? AND state = ?", idempotency.ID, managerIdempotencyInProgress).
			Updates(map[string]any{
				"state": managerIdempotencyCompleted, "result_status": status, "response_status": uint16(503),
				"error_code": failure.ErrorCode, "completed_at": completedAt,
			})
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return groups.ErrConflict
		}
		return insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: completedAt, ScopeType: "system", Action: managerGroupSyncOperation,
			TargetType: "group_sync", TargetID: failure.ExecutionID, Result: audit.ResultFailed,
			ErrorCode: failure.ErrorCode, Metadata: managerSyncAuditMetadata{Phase: "failed", ErrorCode: failure.ErrorCode},
		})
	})
}

func (s *Store) RecoverInterruptedGroupSyncs(ctx context.Context, recoveredAt time.Time) (count int, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var models []managerIdempotencyKey
		if loadErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("operation = ? AND state = ?", managerGroupSyncOperation, managerIdempotencyInProgress).
			Order("idempotency_id ASC").Find(&models).Error; loadErr != nil {
			return loadErr
		}
		completedAt := recoveredAt.UTC()
		for _, model := range models {
			if model.TraceID == nil || *model.TraceID == "" {
				return errManagerInvalidState
			}
			auditContext, contextErr := findManagerAuditContext(tx, managerGroupSyncOperation, *model.TraceID)
			if contextErr != nil {
				return contextErr
			}
			status := "unknown"
			code := "sync_interrupted"
			update := tx.Model(&managerIdempotencyKey{}).
				Where("idempotency_id = ? AND state = ?", model.ID, managerIdempotencyInProgress).
				Updates(map[string]any{
					"state": managerIdempotencyCompleted, "result_status": status, "response_status": uint16(500),
					"error_code": code, "completed_at": completedAt,
				})
			if update.Error != nil {
				return update.Error
			}
			if update.RowsAffected != 1 {
				return groups.ErrConflict
			}
			if auditErr := insertManagerAudit(tx, managerAuditEntry{
				Context: auditContext, OccurredAt: completedAt, ScopeType: "system", Action: managerGroupSyncOperation,
				TargetType: "group_sync", TargetID: *model.TraceID, Result: audit.ResultUnknown, ErrorCode: code,
				Metadata: managerSyncAuditMetadata{Phase: "recovered", ErrorCode: code},
			}); auditErr != nil {
				return auditErr
			}
			count++
		}
		return nil
	})
	return count, err
}

func loadManagerGroupOverrides(tx *gorm.DB, models []managerManagedGroup) (map[int64]settings.Overrides, error) {
	result := make(map[int64]settings.Overrides)
	if len(models) == 0 {
		return result, nil
	}
	ids := make([]int64, len(models))
	for index, model := range models {
		ids[index] = model.GroupID
	}
	var settingsModels []managerFeatureSetting
	if err := tx.Where("scope_type = ? AND group_id IN ?", managerSettingsScopeGroup, ids).Find(&settingsModels).Error; err != nil {
		return nil, err
	}
	for _, model := range settingsModels {
		if model.GroupID == nil {
			return nil, errManagerInvalidState
		}
		overrides, err := decodeManagerOverrides(model.SettingsJSON)
		if err != nil {
			return nil, err
		}
		result[*model.GroupID] = overrides
	}
	return result, nil
}

func managerGroupFromModel(
	model managerManagedGroup,
	global settings.Features,
	overrides settings.Overrides,
	forceStale bool,
	staleBefore time.Time,
) (groups.Group, error) {
	if model.GroupID <= 0 || model.LastSyncedAt == nil || model.LastSyncedAt.IsZero() {
		return groups.Group{}, errManagerInvalidState
	}
	state := groups.SnapshotState(model.SnapshotState)
	if state != groups.SnapshotFresh && state != groups.SnapshotStale {
		return groups.Group{}, errManagerInvalidState
	}
	lastSyncedAt := model.LastSyncedAt.UTC()
	if forceStale || (!staleBefore.IsZero() && lastSyncedAt.Before(staleBefore)) {
		state = groups.SnapshotStale
	}
	return groups.Group{
		ID: strconv.FormatInt(model.GroupID, 10), Name: model.Name, MemberCount: model.MemberCount,
		MaxMemberCount: model.MaxMemberCount, BotRole: groups.Role(model.BotRole), SnapshotState: state,
		LastSyncedAt: lastSyncedAt, Features: managerGroupFeatures(global, overrides),
	}, nil
}

func managerGroupFeatures(global settings.Features, overrides settings.Overrides) []groups.Feature {
	effective := resolveManagerFeatures(global, overrides)
	return []groups.Feature{
		{Key: groups.FeatureKeywordReply, Enabled: effective.KeywordReply.Enabled, Source: managerFeatureSource(overrides.KeywordReply != nil)},
		{Key: groups.FeatureAIQA, Enabled: effective.AIQA.Enabled, Source: managerFeatureSource(overrides.AIQA != nil)},
		{Key: groups.FeatureQuote, Enabled: effective.Quote.Enabled, Source: managerFeatureSource(overrides.Quote != nil)},
		{Key: groups.FeatureLinkCleaner, Enabled: effective.LinkCleaner.Enabled, Source: managerFeatureSource(overrides.LinkCleaner != nil)},
		{Key: groups.FeatureWelcome, Enabled: effective.Welcome.Enabled, Source: managerFeatureSource(overrides.Welcome != nil)},
		{Key: groups.FeatureCustomCommand, Enabled: effective.CustomCommand.Enabled, Source: managerFeatureSource(overrides.CustomCommand != nil)},
	}
}

func managerFeatureSource(overridden bool) groups.FeatureSource {
	if overridden {
		return groups.FeatureGroupOverride
	}
	return groups.FeatureGlobal
}

func managerFindGroupFeature(features []groups.Feature, key groups.FeatureKey) (groups.Feature, bool) {
	for _, feature := range features {
		if feature.Key == key {
			return feature, true
		}
	}
	return groups.Feature{}, false
}

func prepareManagerRemoteGroups(values []groups.RemoteGroup) ([]managerRemoteGroup, error) {
	result := make([]managerRemoteGroup, len(values))
	seen := make(map[int64]struct{}, len(values))
	for index, value := range values {
		id, err := parseManagerGroupID(value.ID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, errManagerInvalidState
		}
		seen[id] = struct{}{}
		result[index] = managerRemoteGroup{
			ID: id, Name: value.Name, MemberCount: value.MemberCount, MaxMemberCount: value.MaxMemberCount, BotRole: value.BotRole,
		}
	}
	return result, nil
}

func loadManagerSyncIdempotency(tx *gorm.DB, executionID string) (managerIdempotencyKey, error) {
	var model managerIdempotencyKey
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("operation = ? AND trace_id = ?", managerGroupSyncOperation, executionID).Take(&model).Error
	return model, err
}

func managerGroupSyncReservation(tx *gorm.DB, model managerIdempotencyKey) (groups.SyncReservation, error) {
	if model.TraceID == nil || *model.TraceID == "" {
		return groups.SyncReservation{}, errManagerInvalidState
	}
	reservation := groups.SyncReservation{ExecutionID: *model.TraceID}
	switch model.State {
	case managerIdempotencyInProgress:
		reservation.InProgress = true
		return reservation, nil
	case managerIdempotencyCompleted:
		if model.ResultStatus == nil {
			return groups.SyncReservation{}, errManagerInvalidState
		}
		if *model.ResultStatus != "succeeded" {
			if model.ErrorCode != nil {
				reservation.FailureCode = *model.ErrorCode
			}
			if reservation.FailureCode == "" {
				reservation.FailureCode = "sync_failed"
			}
			return reservation, nil
		}
		result, err := loadManagerSyncResult(tx, *model.TraceID)
		if err != nil {
			return groups.SyncReservation{}, err
		}
		reservation.Result = &result
		return reservation, nil
	default:
		return groups.SyncReservation{}, errManagerInvalidState
	}
}

func loadManagerSyncResult(tx *gorm.DB, executionID string) (groups.SyncResult, error) {
	var logs []managerAdminAuditLog
	if err := tx.Where("action = ? AND target_id = ?", managerGroupSyncOperation, executionID).
		Order("occurred_at DESC").Order("audit_log_id DESC").Find(&logs).Error; err != nil {
		return groups.SyncResult{}, err
	}
	for _, log := range logs {
		var metadata managerSyncAuditMetadata
		if err := json.Unmarshal(log.Metadata, &metadata); err != nil {
			return groups.SyncResult{}, fmt.Errorf("decode group sync result: %w", err)
		}
		if metadata.Phase != "completed" {
			continue
		}
		if metadata.SyncedAt == nil || metadata.SyncedAt.IsZero() {
			return groups.SyncResult{}, errManagerInvalidState
		}
		return groups.SyncResult{
			SyncedAt: metadata.SyncedAt.UTC(), AddedCount: metadata.AddedCount, UpdatedCount: metadata.UpdatedCount,
			RemovedCount: metadata.RemovedCount, TotalCount: metadata.TotalCount,
		}, nil
	}
	return groups.SyncResult{}, errManagerInvalidState
}

func managerSyncMetadataFromResult(phase string, result groups.SyncResult, errorCode string) managerSyncAuditMetadata {
	syncedAt := result.SyncedAt.UTC()
	return managerSyncAuditMetadata{
		Phase: phase, SyncedAt: &syncedAt, AddedCount: result.AddedCount, UpdatedCount: result.UpdatedCount,
		RemovedCount: result.RemovedCount, TotalCount: result.TotalCount, ErrorCode: errorCode,
	}
}

func escapeManagerCoreLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}
