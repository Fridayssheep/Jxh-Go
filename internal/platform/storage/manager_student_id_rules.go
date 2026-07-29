package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const studentIDRuleID = "student_id_rule"

type studentIDRuleManagerRow struct {
	RuleID     string `gorm:"column:rule_id;primaryKey"`
	ConfigJSON []byte `gorm:"column:config_json"`
	Revision   uint64 `gorm:"column:revision"`
	ManagerUpdatedByColumns
	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
}

func (studentIDRuleManagerRow) TableName() string { return "student_id_rules" }

type studentIDRuleConfigPayload struct {
	Enabled               bool                               `json:"enabled"`
	StudentIDLength       int                                `json:"student_id_length"`
	EnrollmentYearSegment *studentIDRuleSegmentPayload       `json:"enrollment_year_segment"`
	MajorCodeSegment      *studentIDRuleSegmentPayload       `json:"major_code_segment"`
	Mappings              []studentIDRuleMajorMappingPayload `json:"mappings"`
}

type studentIDRuleSegmentPayload struct {
	Offset int `json:"offset"`
	Length int `json:"length"`
}

type studentIDRuleMajorMappingPayload struct {
	EnrollmentYear int      `json:"enrollment_year"`
	MajorCode      string   `json:"major_code"`
	MajorName      string   `json:"major_name"`
	Aliases        []string `json:"aliases"`
}

func (s *Store) GetStudentIDRule(ctx context.Context) (joinrequests.StudentIDRule, bool, error) {
	var row studentIDRuleManagerRow
	err := s.db.WithContext(ctx).Where("rule_id = ?", studentIDRuleID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return joinrequests.StudentIDRule{}, false, nil
	}
	if err != nil {
		return joinrequests.StudentIDRule{}, false, err
	}
	value, err := studentIDRuleFromManagerRow(row)
	return value, err == nil, err
}

func (s *Store) UpdateStudentIDRule(ctx context.Context, mutation joinrequests.StudentIDRuleMutation) (joinrequests.StudentIDRule, error) {
	configJSON, err := studentIDRuleConfigJSON(mutation.Rule)
	if err != nil {
		return joinrequests.StudentIDRule{}, err
	}
	var result joinrequests.StudentIDRule
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before studentIDRuleManagerRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("rule_id = ?", studentIDRuleID).First(&before).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return joinrequests.ErrConflict
			}
			return err
		}
		if before.Revision != mutation.ExpectedRevision {
			return joinrequests.ErrConflict
		}
		actor := auditActorUpdatedBy(mutation.Context.Actor)
		updated := tx.Model(&studentIDRuleManagerRow{}).
			Where("rule_id = ? AND revision = ?", studentIDRuleID, mutation.ExpectedRevision).
			Updates(map[string]any{
				"config_json": configJSON, "revision": gorm.Expr("revision + 1"),
				"updated_by_type": actor.Type, "updated_by_user_id": actor.UserID,
				"updated_by_qq_user_id": actor.QQUserID, "updated_by_display_name": actor.DisplayName,
				"updated_by_role": actor.Role, "updated_at": mutation.Context.OccurredAt.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		var after studentIDRuleManagerRow
		if err := tx.Where("rule_id = ?", studentIDRuleID).First(&after).Error; err != nil {
			return err
		}
		converted, err := studentIDRuleFromManagerRow(after)
		if err != nil {
			return err
		}
		result = converted
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: domainDecisionActor(mutation.Context.Actor), OccurredAt: mutation.Context.OccurredAt,
			Request: mutation.Context.Request, Source: sourceForManagerActor(mutation.Context.Actor.Type),
			Action: "student_id_rule.update", TargetType: "settings", TargetID: studentIDRuleID,
			Before: studentIDRuleAuditSnapshot(before), After: studentIDRuleAuditSnapshot(after), Metadata: map[string]any{},
		})
	})
	return result, err
}

func studentIDRuleFromManagerRow(row studentIDRuleManagerRow) (joinrequests.StudentIDRule, error) {
	var payload studentIDRuleConfigPayload
	if err := opsUnmarshalJSON(row.ConfigJSON, &payload); err != nil {
		return joinrequests.StudentIDRule{}, err
	}
	if payload.StudentIDLength != joinrequests.StudentIDLength {
		return joinrequests.StudentIDRule{}, fmt.Errorf("student ID rule length = %d", payload.StudentIDLength)
	}
	mappings := make([]joinrequests.StudentMajorMapping, len(payload.Mappings))
	for index, mapping := range payload.Mappings {
		mappings[index] = joinrequests.StudentMajorMapping{
			EnrollmentYear: mapping.EnrollmentYear, MajorCode: mapping.MajorCode,
			MajorName: mapping.MajorName, Aliases: append([]string{}, mapping.Aliases...),
		}
	}
	actor := updatedByActor(row.ManagerUpdatedByColumns)
	return joinrequests.StudentIDRule{
		Enabled: payload.Enabled, EnrollmentYearSegment: domainStudentIDSegment(payload.EnrollmentYearSegment),
		MajorCodeSegment: domainStudentIDSegment(payload.MajorCodeSegment), Mappings: mappings,
		Version: row.Revision, UpdatedAt: row.UpdatedAt.UTC(), UpdatedBy: &actor,
	}, nil
}

func studentIDRuleConfigJSON(rule joinrequests.StudentIDRule) ([]byte, error) {
	mappings := make([]studentIDRuleMajorMappingPayload, len(rule.Mappings))
	for index, mapping := range rule.Mappings {
		mappings[index] = studentIDRuleMajorMappingPayload{
			EnrollmentYear: mapping.EnrollmentYear, MajorCode: mapping.MajorCode,
			MajorName: mapping.MajorName, Aliases: append([]string{}, mapping.Aliases...),
		}
	}
	return opsMarshalJSON(studentIDRuleConfigPayload{
		Enabled: rule.Enabled, StudentIDLength: joinrequests.StudentIDLength,
		EnrollmentYearSegment: managerStudentIDSegment(rule.EnrollmentYearSegment),
		MajorCodeSegment:      managerStudentIDSegment(rule.MajorCodeSegment), Mappings: mappings,
	})
}

func domainStudentIDSegment(value *studentIDRuleSegmentPayload) *joinrequests.StudentIDSegment {
	if value == nil {
		return nil
	}
	return &joinrequests.StudentIDSegment{Offset: value.Offset, Length: value.Length}
}

func managerStudentIDSegment(value *joinrequests.StudentIDSegment) *studentIDRuleSegmentPayload {
	if value == nil {
		return nil
	}
	return &studentIDRuleSegmentPayload{Offset: value.Offset, Length: value.Length}
}

func studentIDRuleAuditSnapshot(row studentIDRuleManagerRow) map[string]any {
	var payload studentIDRuleConfigPayload
	_ = opsUnmarshalJSON(row.ConfigJSON, &payload)
	digest := sha256.Sum256(row.ConfigJSON)
	return map[string]any{
		"enabled": payload.Enabled, "mapping_count": len(payload.Mappings),
		"config_digest": hex.EncodeToString(digest[:]), "revision": row.Revision,
	}
}
