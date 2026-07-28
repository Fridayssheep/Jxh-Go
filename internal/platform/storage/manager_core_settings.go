package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	managerSettingsScopeGlobal = "global"
	managerSettingsScopeGroup  = "group"
)

type managerBasicSettingsDocument struct {
	Enabled *bool `json:"enabled,omitempty"`
}

type managerWelcomeSettingsDocument struct {
	Enabled         *bool   `json:"enabled,omitempty"`
	MessageTemplate *string `json:"message_template,omitempty"`
}

type managerSettingsDocument struct {
	KeywordReply  *managerBasicSettingsDocument   `json:"keyword_reply,omitempty"`
	AIQA          *managerBasicSettingsDocument   `json:"ai_qa,omitempty"`
	Quote         *managerBasicSettingsDocument   `json:"quote,omitempty"`
	LinkCleaner   *managerBasicSettingsDocument   `json:"link_cleaner,omitempty"`
	Welcome       *managerWelcomeSettingsDocument `json:"welcome,omitempty"`
	CustomCommand *managerBasicSettingsDocument   `json:"custom_commands,omitempty"`
}

func (s *Store) GetGlobalSettings(ctx context.Context) (settings.Global, error) {
	model, value, err := loadManagerGlobalSettings(s.db.WithContext(ctx), false)
	if err != nil {
		return settings.Global{}, err
	}
	return managerGlobalFromSetting(model, value), nil
}

func (s *Store) GetGroupSettings(ctx context.Context, groupID string) (value settings.Group, found bool, err error) {
	id, err := parseManagerGroupID(groupID)
	if err != nil {
		return settings.Group{}, false, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if exists, findErr := managerActiveGroupExists(tx, id, false); findErr != nil {
			return findErr
		} else if !exists {
			return nil
		}
		globalModel, globalFeatures, loadErr := loadManagerGlobalSettings(tx, false)
		if loadErr != nil {
			return loadErr
		}
		setting, exists, loadErr := loadManagerGroupSetting(tx, id, false)
		if loadErr != nil {
			return loadErr
		}
		overrides := settings.Overrides{}
		if exists {
			overrides, loadErr = decodeManagerOverrides(setting.SettingsJSON)
			if loadErr != nil {
				return loadErr
			}
			if managerOverridesEmpty(overrides) {
				exists = false
			}
		}
		value = managerGroupSettingsValue(groupID, globalModel, globalFeatures, setting, overrides, exists)
		found = true
		return nil
	})
	return value, found, err
}

func (s *Store) LoadRuntimeSettings(ctx context.Context) (state settings.RuntimeState, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		globalModel, globalFeatures, loadErr := loadManagerGlobalSettings(tx, false)
		if loadErr != nil {
			return loadErr
		}
		state.Global = managerGlobalFromSetting(globalModel, globalFeatures)
		var models []managerFeatureSetting
		if loadErr = tx.Where("scope_type = ? AND group_id IS NOT NULL", managerSettingsScopeGroup).
			Order("group_id ASC").Find(&models).Error; loadErr != nil {
			return loadErr
		}
		state.Groups = make([]settings.RuntimeGroup, 0, len(models))
		for _, model := range models {
			if model.GroupID == nil || *model.GroupID <= 0 {
				return errManagerInvalidState
			}
			overrides, decodeErr := decodeManagerOverrides(model.SettingsJSON)
			if decodeErr != nil {
				return decodeErr
			}
			if managerOverridesEmpty(overrides) {
				continue
			}
			state.Groups = append(state.Groups, settings.RuntimeGroup{
				GroupID: strconv.FormatInt(*model.GroupID, 10), Overrides: overrides, Version: model.Revision,
			})
		}
		return nil
	})
	return state, err
}

func (s *Store) UpdateGlobalSettings(ctx context.Context, mutation settings.UpdateGlobalMutation) (value settings.Global, err error) {
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		model, features, loadErr := loadManagerGlobalSettings(tx, true)
		if loadErr != nil {
			return loadErr
		}
		if model.Revision != mutation.ExpectedRevision {
			return settings.ErrConflict
		}
		before := encodeManagerFeaturesDocument(features)
		features = applyManagerGlobalPatch(features, mutation.Patch)
		encoded, encodeErr := encodeManagerFeatures(features)
		if encodeErr != nil {
			return encodeErr
		}
		auditContext, contextErr := managerAuditContextForMutation(tx, mutation.Context.Actor, mutation.Context.Request)
		if contextErr != nil {
			return contextErr
		}
		updatedAt := mutation.Context.OccurredAt.UTC()
		nextRevision := mutation.ExpectedRevision + 1
		updates := map[string]any{
			"settings_json": encoded, "revision": nextRevision, "updated_by_type": auditContext.Actor.Type,
			"updated_by_user_id": auditContext.Actor.UserID, "updated_by_qq_user_id": auditContext.Actor.QQUserID,
			"updated_by_display_name": auditContext.Actor.DisplayName, "updated_by_role": auditContext.Actor.Role,
			"updated_at": updatedAt,
		}
		result := tx.Model(&managerFeatureSetting{}).
			Where("setting_id = ? AND revision = ?", model.SettingID, mutation.ExpectedRevision).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return settings.ErrConflict
		}
		model.SettingsJSON = encoded
		model.Revision = nextRevision
		model.UpdatedByType = auditContext.Actor.Type
		model.UpdatedByUserID = auditContext.Actor.UserID
		model.UpdatedByQQUserID = auditContext.Actor.QQUserID
		model.UpdatedByDisplayName = auditContext.Actor.DisplayName
		model.UpdatedByRole = auditContext.Actor.Role
		model.UpdatedAt = updatedAt
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: updatedAt, ScopeType: managerSettingsScopeGlobal,
			Action: "settings.global.update", TargetType: "feature_settings", TargetID: model.SettingID,
			Result: audit.ResultSuccess, Before: before, After: encodeManagerFeaturesDocument(features),
			Metadata: map[string]any{"previous_revision": mutation.ExpectedRevision, "revision": nextRevision},
		}); auditErr != nil {
			return auditErr
		}
		value = managerGlobalFromSetting(model, features)
		return nil
	})
	return value, err
}

func (s *Store) UpdateGroupSettings(ctx context.Context, mutation settings.UpdateGroupMutation) (value settings.Group, err error) {
	groupID, err := parseManagerGroupID(mutation.GroupID)
	if err != nil {
		return settings.Group{}, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		globalModel, globalFeatures, loadErr := loadManagerGlobalSettings(tx, true)
		if loadErr != nil {
			return loadErr
		}
		if exists, findErr := managerActiveGroupExists(tx, groupID, true); findErr != nil {
			return findErr
		} else if !exists {
			return settings.ErrNotFound
		}
		model, exists, loadErr := loadManagerGroupSetting(tx, groupID, true)
		if loadErr != nil {
			return loadErr
		}
		if (exists && model.Revision != mutation.ExpectedRevision) || (!exists && mutation.ExpectedRevision != 0) {
			return settings.ErrConflict
		}
		overrides := settings.Overrides{}
		if exists {
			overrides, loadErr = decodeManagerOverrides(model.SettingsJSON)
			if loadErr != nil {
				return loadErr
			}
		}
		before := encodeManagerOverridesDocument(overrides)
		overrides = applyManagerGroupPatch(overrides, mutation.Patch)
		auditContext, contextErr := managerAuditContextForMutation(tx, mutation.Context.Actor, mutation.Context.Request)
		if contextErr != nil {
			return contextErr
		}
		updatedAt := mutation.Context.OccurredAt.UTC()
		if managerOverridesEmpty(overrides) {
			if exists {
				result := tx.Where("setting_id = ? AND revision = ?", model.SettingID, mutation.ExpectedRevision).
					Delete(&managerFeatureSetting{})
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return settings.ErrConflict
				}
			}
			value = managerGroupSettingsValue(mutation.GroupID, globalModel, globalFeatures, managerFeatureSetting{}, settings.Overrides{}, false)
		} else {
			encoded, encodeErr := encodeManagerOverrides(overrides)
			if encodeErr != nil {
				return encodeErr
			}
			nextRevision := mutation.ExpectedRevision + 1
			if exists {
				updates := map[string]any{
					"settings_json": encoded, "revision": nextRevision, "updated_by_type": auditContext.Actor.Type,
					"updated_by_user_id": auditContext.Actor.UserID, "updated_by_qq_user_id": auditContext.Actor.QQUserID,
					"updated_by_display_name": auditContext.Actor.DisplayName, "updated_by_role": auditContext.Actor.Role,
					"updated_at": updatedAt,
				}
				result := tx.Model(&managerFeatureSetting{}).
					Where("setting_id = ? AND revision = ?", model.SettingID, mutation.ExpectedRevision).Updates(updates)
				if result.Error != nil {
					return result.Error
				}
				if result.RowsAffected != 1 {
					return settings.ErrConflict
				}
			} else {
				settingID, idErr := newManagerID("set")
				if idErr != nil {
					return idErr
				}
				model = managerFeatureSetting{SettingID: settingID, ScopeType: managerSettingsScopeGroup, GroupID: &groupID}
			}
			model.SettingsJSON = encoded
			model.Revision = nextRevision
			model.UpdatedByType = auditContext.Actor.Type
			model.UpdatedByUserID = auditContext.Actor.UserID
			model.UpdatedByQQUserID = auditContext.Actor.QQUserID
			model.UpdatedByDisplayName = auditContext.Actor.DisplayName
			model.UpdatedByRole = auditContext.Actor.Role
			model.UpdatedAt = updatedAt
			if !exists {
				model.CreatedAt = updatedAt
				if createErr := tx.Create(&model).Error; createErr != nil {
					if isManagerDuplicateKey(createErr) {
						return settings.ErrConflict
					}
					return createErr
				}
			}
			value = managerGroupSettingsValue(mutation.GroupID, globalModel, globalFeatures, model, overrides, true)
		}
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: updatedAt, ScopeType: managerSettingsScopeGroup, ScopeID: mutation.GroupID,
			Action: "settings.group.update", TargetType: "managed_group", TargetID: mutation.GroupID,
			Result: audit.ResultSuccess, Before: before, After: encodeManagerOverridesDocument(overrides),
			Metadata: map[string]any{"previous_revision": mutation.ExpectedRevision, "revision": value.Version},
		}); auditErr != nil {
			return auditErr
		}
		return nil
	})
	return value, err
}

func (s *Store) DeleteGroupSettings(ctx context.Context, mutation settings.DeleteGroupMutation) (value settings.Group, err error) {
	groupID, err := parseManagerGroupID(mutation.GroupID)
	if err != nil {
		return settings.Group{}, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		globalModel, globalFeatures, loadErr := loadManagerGlobalSettings(tx, true)
		if loadErr != nil {
			return loadErr
		}
		if exists, findErr := managerActiveGroupExists(tx, groupID, true); findErr != nil {
			return findErr
		} else if !exists {
			return settings.ErrNotFound
		}
		model, exists, loadErr := loadManagerGroupSetting(tx, groupID, true)
		if loadErr != nil {
			return loadErr
		}
		if !exists || model.Revision != mutation.ExpectedRevision {
			return settings.ErrConflict
		}
		overrides, loadErr := decodeManagerOverrides(model.SettingsJSON)
		if loadErr != nil {
			return loadErr
		}
		auditContext, contextErr := managerAuditContextForMutation(tx, mutation.Context.Actor, mutation.Context.Request)
		if contextErr != nil {
			return contextErr
		}
		result := tx.Where("setting_id = ? AND revision = ?", model.SettingID, mutation.ExpectedRevision).
			Delete(&managerFeatureSetting{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return settings.ErrConflict
		}
		occurredAt := mutation.Context.OccurredAt.UTC()
		if auditErr := insertManagerAudit(tx, managerAuditEntry{
			Context: auditContext, OccurredAt: occurredAt, ScopeType: managerSettingsScopeGroup, ScopeID: mutation.GroupID,
			Action: "settings.group.delete", TargetType: "managed_group", TargetID: mutation.GroupID,
			Result: audit.ResultSuccess, Before: encodeManagerOverridesDocument(overrides),
			After:    encodeManagerOverridesDocument(settings.Overrides{}),
			Metadata: map[string]any{"previous_revision": mutation.ExpectedRevision, "revision": 0},
		}); auditErr != nil {
			return auditErr
		}
		value = managerGroupSettingsValue(mutation.GroupID, globalModel, globalFeatures, managerFeatureSetting{}, settings.Overrides{}, false)
		return nil
	})
	return value, err
}

func loadManagerGlobalSettings(tx *gorm.DB, lock bool) (managerFeatureSetting, settings.Features, error) {
	var model managerFeatureSetting
	query := tx.Where("scope_type = ? AND group_id IS NULL", managerSettingsScopeGlobal)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if err := query.Take(&model).Error; err != nil {
		return managerFeatureSetting{}, settings.Features{}, err
	}
	features, err := decodeManagerFeatures(model.SettingsJSON)
	return model, features, err
}

func loadManagerGroupSetting(tx *gorm.DB, groupID int64, lock bool) (managerFeatureSetting, bool, error) {
	var model managerFeatureSetting
	query := tx.Where("scope_type = ? AND group_id = ?", managerSettingsScopeGroup, groupID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return managerFeatureSetting{}, false, nil
	}
	return model, err == nil, err
}

func managerActiveGroupExists(tx *gorm.DB, groupID int64, lock bool) (bool, error) {
	var model managerManagedGroup
	query := tx.Select("group_id").Where("group_id = ? AND archived_at IS NULL", groupID)
	if lock {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	err := query.Take(&model).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return err == nil, err
}

func managerGlobalFromSetting(model managerFeatureSetting, features settings.Features) settings.Global {
	return settings.Global{
		Features: features, Version: model.Revision, UpdatedAt: model.UpdatedAt.UTC(), UpdatedBy: managerSettingActor(model),
	}
}

func managerGroupSettingsValue(
	groupID string,
	globalModel managerFeatureSetting,
	globalFeatures settings.Features,
	model managerFeatureSetting,
	overrides settings.Overrides,
	exists bool,
) settings.Group {
	value := settings.Group{
		GroupID: groupID, Effective: resolveManagerFeatures(globalFeatures, overrides), Overrides: overrides,
		GlobalVersion: globalModel.Revision,
	}
	if exists {
		updatedAt := model.UpdatedAt.UTC()
		value.Version = model.Revision
		value.UpdatedAt = &updatedAt
		value.UpdatedBy = managerSettingActor(model)
	}
	return value
}

func managerSettingActor(model managerFeatureSetting) *audit.Actor {
	return &audit.Actor{
		Type: audit.ActorType(model.UpdatedByType), UserID: cloneManagerString(model.UpdatedByUserID),
		QQUserID: cloneManagerString(model.UpdatedByQQUserID), DisplayName: model.UpdatedByDisplayName,
	}
}

func decodeManagerFeatures(raw []byte) (settings.Features, error) {
	var document managerSettingsDocument
	if err := decodeManagerSettingsDocument(raw, &document); err != nil {
		return settings.Features{}, err
	}
	features := settings.DefaultFeatures()
	applyBasicDocument := func(document *managerBasicSettingsDocument, enabled *bool) error {
		if document == nil {
			return nil
		}
		if document.Enabled == nil {
			return errManagerInvalidState
		}
		*enabled = *document.Enabled
		return nil
	}
	if err := applyBasicDocument(document.KeywordReply, &features.KeywordReply.Enabled); err != nil {
		return settings.Features{}, err
	}
	if err := applyBasicDocument(document.AIQA, &features.AIQA.Enabled); err != nil {
		return settings.Features{}, err
	}
	if err := applyBasicDocument(document.Quote, &features.Quote.Enabled); err != nil {
		return settings.Features{}, err
	}
	if err := applyBasicDocument(document.LinkCleaner, &features.LinkCleaner.Enabled); err != nil {
		return settings.Features{}, err
	}
	if err := applyBasicDocument(document.CustomCommand, &features.CustomCommand.Enabled); err != nil {
		return settings.Features{}, err
	}
	if document.Welcome != nil {
		if document.Welcome.Enabled == nil && document.Welcome.MessageTemplate == nil {
			return settings.Features{}, errManagerInvalidState
		}
		if document.Welcome.Enabled != nil {
			features.Welcome.Enabled = *document.Welcome.Enabled
		}
		if document.Welcome.MessageTemplate != nil {
			features.Welcome.MessageTemplate = *document.Welcome.MessageTemplate
		}
	}
	return features, nil
}

func decodeManagerOverrides(raw []byte) (settings.Overrides, error) {
	var document managerSettingsDocument
	if err := decodeManagerSettingsDocument(raw, &document); err != nil {
		return settings.Overrides{}, err
	}
	decodeBasic := func(value *managerBasicSettingsDocument) (*settings.BasicOverride, error) {
		if value == nil {
			return nil, nil
		}
		if value.Enabled == nil {
			return nil, errManagerInvalidState
		}
		return &settings.BasicOverride{Enabled: *value.Enabled}, nil
	}
	var overrides settings.Overrides
	var err error
	if overrides.KeywordReply, err = decodeBasic(document.KeywordReply); err != nil {
		return settings.Overrides{}, err
	}
	if overrides.AIQA, err = decodeBasic(document.AIQA); err != nil {
		return settings.Overrides{}, err
	}
	if overrides.Quote, err = decodeBasic(document.Quote); err != nil {
		return settings.Overrides{}, err
	}
	if overrides.LinkCleaner, err = decodeBasic(document.LinkCleaner); err != nil {
		return settings.Overrides{}, err
	}
	if overrides.CustomCommand, err = decodeBasic(document.CustomCommand); err != nil {
		return settings.Overrides{}, err
	}
	if document.Welcome != nil {
		if document.Welcome.Enabled == nil && document.Welcome.MessageTemplate == nil {
			return settings.Overrides{}, errManagerInvalidState
		}
		overrides.Welcome = &settings.WelcomeOverride{
			Enabled: cloneManagerBool(document.Welcome.Enabled), MessageTemplate: cloneManagerString(document.Welcome.MessageTemplate),
		}
	}
	return overrides, nil
}

func decodeManagerSettingsDocument(raw []byte, destination *managerSettingsDocument) error {
	var object map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || object == nil {
		return errManagerInvalidState
	}
	if err := json.Unmarshal(raw, destination); err != nil {
		return fmt.Errorf("decode feature settings: %w", err)
	}
	return nil
}

func encodeManagerFeatures(features settings.Features) ([]byte, error) {
	return marshalManagerJSON(encodeManagerFeaturesDocument(features))
}

func encodeManagerFeaturesDocument(features settings.Features) managerSettingsDocument {
	return managerSettingsDocument{
		KeywordReply:  &managerBasicSettingsDocument{Enabled: cloneManagerBool(&features.KeywordReply.Enabled)},
		AIQA:          &managerBasicSettingsDocument{Enabled: cloneManagerBool(&features.AIQA.Enabled)},
		Quote:         &managerBasicSettingsDocument{Enabled: cloneManagerBool(&features.Quote.Enabled)},
		LinkCleaner:   &managerBasicSettingsDocument{Enabled: cloneManagerBool(&features.LinkCleaner.Enabled)},
		Welcome:       &managerWelcomeSettingsDocument{Enabled: cloneManagerBool(&features.Welcome.Enabled), MessageTemplate: stringPointer(features.Welcome.MessageTemplate)},
		CustomCommand: &managerBasicSettingsDocument{Enabled: cloneManagerBool(&features.CustomCommand.Enabled)},
	}
}

func encodeManagerOverrides(overrides settings.Overrides) ([]byte, error) {
	return marshalManagerJSON(encodeManagerOverridesDocument(overrides))
}

func encodeManagerOverridesDocument(overrides settings.Overrides) managerSettingsDocument {
	document := managerSettingsDocument{}
	if overrides.KeywordReply != nil {
		document.KeywordReply = &managerBasicSettingsDocument{Enabled: cloneManagerBool(&overrides.KeywordReply.Enabled)}
	}
	if overrides.AIQA != nil {
		document.AIQA = &managerBasicSettingsDocument{Enabled: cloneManagerBool(&overrides.AIQA.Enabled)}
	}
	if overrides.Quote != nil {
		document.Quote = &managerBasicSettingsDocument{Enabled: cloneManagerBool(&overrides.Quote.Enabled)}
	}
	if overrides.LinkCleaner != nil {
		document.LinkCleaner = &managerBasicSettingsDocument{Enabled: cloneManagerBool(&overrides.LinkCleaner.Enabled)}
	}
	if overrides.Welcome != nil {
		document.Welcome = &managerWelcomeSettingsDocument{
			Enabled: cloneManagerBool(overrides.Welcome.Enabled), MessageTemplate: cloneManagerString(overrides.Welcome.MessageTemplate),
		}
	}
	if overrides.CustomCommand != nil {
		document.CustomCommand = &managerBasicSettingsDocument{Enabled: cloneManagerBool(&overrides.CustomCommand.Enabled)}
	}
	return document
}

func applyManagerGlobalPatch(features settings.Features, patch settings.GlobalPatch) settings.Features {
	applyBasic := func(fieldSet bool, enabledSet bool, value bool, destination *bool) {
		if fieldSet && enabledSet {
			*destination = value
		}
	}
	applyBasic(patch.KeywordReply.Set, patch.KeywordReply.Value.Enabled.Set, patch.KeywordReply.Value.Enabled.Value, &features.KeywordReply.Enabled)
	applyBasic(patch.AIQA.Set, patch.AIQA.Value.Enabled.Set, patch.AIQA.Value.Enabled.Value, &features.AIQA.Enabled)
	applyBasic(patch.Quote.Set, patch.Quote.Value.Enabled.Set, patch.Quote.Value.Enabled.Value, &features.Quote.Enabled)
	applyBasic(patch.LinkCleaner.Set, patch.LinkCleaner.Value.Enabled.Set, patch.LinkCleaner.Value.Enabled.Value, &features.LinkCleaner.Enabled)
	applyBasic(patch.CustomCommand.Set, patch.CustomCommand.Value.Enabled.Set, patch.CustomCommand.Value.Enabled.Value, &features.CustomCommand.Enabled)
	if patch.Welcome.Set {
		if patch.Welcome.Value.Enabled.Set {
			features.Welcome.Enabled = patch.Welcome.Value.Enabled.Value
		}
		if patch.Welcome.Value.MessageTemplate.Set {
			features.Welcome.MessageTemplate = patch.Welcome.Value.MessageTemplate.Value
		}
	}
	return features
}

func applyManagerGroupPatch(overrides settings.Overrides, patch settings.GroupPatch) settings.Overrides {
	applyBasic := func(destination **settings.BasicOverride, value settings.OverrideFeaturePatch) {
		if !value.Set {
			return
		}
		if value.Clear || (value.Enabled.Set && value.Enabled.Value == nil) {
			*destination = nil
			return
		}
		if value.Enabled.Set {
			*destination = &settings.BasicOverride{Enabled: *value.Enabled.Value}
		}
	}
	applyBasic(&overrides.KeywordReply, patch.KeywordReply)
	applyBasic(&overrides.AIQA, patch.AIQA)
	applyBasic(&overrides.Quote, patch.Quote)
	applyBasic(&overrides.LinkCleaner, patch.LinkCleaner)
	applyBasic(&overrides.CustomCommand, patch.CustomCommand)
	if patch.Welcome.Set {
		if patch.Welcome.Clear {
			overrides.Welcome = nil
		} else {
			welcome := &settings.WelcomeOverride{}
			if overrides.Welcome != nil {
				welcome.Enabled = cloneManagerBool(overrides.Welcome.Enabled)
				welcome.MessageTemplate = cloneManagerString(overrides.Welcome.MessageTemplate)
			}
			if patch.Welcome.Enabled.Set {
				welcome.Enabled = cloneManagerBool(patch.Welcome.Enabled.Value)
			}
			if patch.Welcome.MessageTemplate.Set {
				welcome.MessageTemplate = cloneManagerString(patch.Welcome.MessageTemplate.Value)
			}
			if welcome.Enabled == nil && welcome.MessageTemplate == nil {
				overrides.Welcome = nil
			} else {
				overrides.Welcome = welcome
			}
		}
	}
	return overrides
}

func resolveManagerFeatures(global settings.Features, overrides settings.Overrides) settings.Features {
	result := global
	if overrides.KeywordReply != nil {
		result.KeywordReply.Enabled = overrides.KeywordReply.Enabled
	}
	if overrides.AIQA != nil {
		result.AIQA.Enabled = overrides.AIQA.Enabled
	}
	if overrides.Quote != nil {
		result.Quote.Enabled = overrides.Quote.Enabled
	}
	if overrides.LinkCleaner != nil {
		result.LinkCleaner.Enabled = overrides.LinkCleaner.Enabled
	}
	if overrides.CustomCommand != nil {
		result.CustomCommand.Enabled = overrides.CustomCommand.Enabled
	}
	if overrides.Welcome != nil {
		if overrides.Welcome.Enabled != nil {
			result.Welcome.Enabled = *overrides.Welcome.Enabled
		}
		if overrides.Welcome.MessageTemplate != nil {
			result.Welcome.MessageTemplate = *overrides.Welcome.MessageTemplate
		}
	}
	return result
}

func managerOverridesEmpty(value settings.Overrides) bool {
	return value.KeywordReply == nil && value.AIQA == nil && value.Quote == nil && value.LinkCleaner == nil &&
		value.Welcome == nil && value.CustomCommand == nil
}

func parseManagerGroupID(value string) (int64, error) {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("parse group id %q: %w", value, errManagerInvalidState)
	}
	return id, nil
}

func cloneManagerString(value *string) *string {
	if value == nil {
		return nil
	}
	return stringPointer(*value)
}

func cloneManagerBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
