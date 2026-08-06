package storage

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type joinApprovalRuleStateRow struct {
	RuleVersion     uint64     `gorm:"column:rule_version;primaryKey"`
	Status          string     `gorm:"column:status"`
	EvidenceVersion uint64     `gorm:"column:evidence_version"`
	ActivatedAt     *time.Time `gorm:"column:activated_at"`
	RebuiltAt       *time.Time `gorm:"column:rebuilt_at"`
	LastErrorCode   *string    `gorm:"column:last_error_code"`
	Revision        uint64     `gorm:"column:revision"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (joinApprovalRuleStateRow) TableName() string { return "join_approval_rule_state" }

type joinMajorCodeSampleRow struct {
	SampleID               uint64    `gorm:"column:sample_id;primaryKey"`
	EnrollmentYear         string    `gorm:"column:enrollment_year"`
	MajorCode              string    `gorm:"column:major_code"`
	MajorName              string    `gorm:"column:major_name"`
	NormalizedMajor        string    `gorm:"column:normalized_major"`
	SourceRequestID        uint64    `gorm:"column:source_request_id"`
	SourceDecisionID       string    `gorm:"column:source_decision_id"`
	ApprovalSource         string    `gorm:"column:approval_source"`
	SourceGroupID          int64     `gorm:"column:source_group_id"`
	Active                 bool      `gorm:"column:active"`
	Revision               uint64    `gorm:"column:revision"`
	CorrectedByType        *string   `gorm:"column:corrected_by_type"`
	CorrectedByUserID      *string   `gorm:"column:corrected_by_user_id"`
	CorrectedByDisplayName *string   `gorm:"column:corrected_by_display_name"`
	CreatedAt              time.Time `gorm:"column:created_at"`
	UpdatedAt              time.Time `gorm:"column:updated_at"`
}

type joinEvidenceRebuildOperationRow struct {
	ActorID        string    `gorm:"column:actor_id;primaryKey"`
	IdempotencyKey string    `gorm:"column:idempotency_key;primaryKey"`
	ResultJSON     []byte    `gorm:"column:result_json"`
	CreatedAt      time.Time `gorm:"column:created_at"`
}

func (joinEvidenceRebuildOperationRow) TableName() string { return "join_evidence_rebuild_operations" }

func (joinMajorCodeSampleRow) TableName() string { return "join_major_code_samples" }

type admissionRosterVersionRow struct {
	VersionID             string     `gorm:"column:version_id;primaryKey"`
	IdempotencyKey        string     `gorm:"column:idempotency_key"`
	ContentHash           string     `gorm:"column:content_hash"`
	FileName              string     `gorm:"column:file_name"`
	Status                string     `gorm:"column:status"`
	RowCount              uint64     `gorm:"column:row_count"`
	Revision              uint64     `gorm:"column:revision"`
	ImportedByType        string     `gorm:"column:imported_by_type"`
	ImportedByUserID      *string    `gorm:"column:imported_by_user_id"`
	ImportedByDisplayName string     `gorm:"column:imported_by_display_name"`
	CreatedAt             time.Time  `gorm:"column:created_at"`
	ActivatedAt           *time.Time `gorm:"column:activated_at"`
}

func (admissionRosterVersionRow) TableName() string { return "admission_roster_versions" }

type admissionRosterEntryRow struct {
	VersionID   string    `gorm:"column:version_id;primaryKey"`
	StudentID   string    `gorm:"column:student_id;primaryKey"`
	StudentName *string   `gorm:"column:student_name"`
	Major       *string   `gorm:"column:major"`
	CreatedAt   time.Time `gorm:"column:created_at"`
}

func (admissionRosterEntryRow) TableName() string { return "admission_roster_entries" }

func ruleStateFromRow(row joinApprovalRuleStateRow) joinrequests.RuleState {
	return joinrequests.RuleState{
		RuleVersion: row.RuleVersion, Status: joinrequests.RuleStateStatus(row.Status), EvidenceVersion: row.EvidenceVersion,
		ActivatedAt: utcTimePointer(row.ActivatedAt), RebuiltAt: utcTimePointer(row.RebuiltAt),
		LastErrorCode: row.LastErrorCode, Version: row.Revision,
	}
}

func (s *Store) GetAutomaticRuleState(ctx context.Context) (joinrequests.RuleState, error) {
	var row joinApprovalRuleStateRow
	if err := s.db.WithContext(ctx).Where("rule_version = ?", joinrequests.AutoApprovalRuleVersion).Take(&row).Error; err != nil {
		return joinrequests.RuleState{}, err
	}
	return ruleStateFromRow(row), nil
}

func (s *Store) GetMajorEvidence(ctx context.Context, enrollmentYear, majorCode string) (joinrequests.MajorEvidence, error) {
	state, err := s.GetAutomaticRuleState(ctx)
	if err != nil {
		return joinrequests.MajorEvidence{}, err
	}
	if state.Status != joinrequests.RuleStateReady || state.ActivatedAt == nil {
		return joinrequests.MajorEvidence{}, joinrequests.ErrDependencyUnavailable
	}
	evidence := joinrequests.MajorEvidence{EnrollmentYear: enrollmentYear, MajorCode: majorCode, Version: state.EvidenceVersion}
	type countRow struct {
		Major string `gorm:"column:major"`
		Count uint64 `gorm:"column:sample_count"`
	}
	var rows []countRow
	err = s.db.WithContext(ctx).Table("join_major_code_samples").
		Select("major_name AS major, COUNT(*) AS sample_count").
		Where("enrollment_year = ? AND major_code = ? AND active = TRUE", enrollmentYear, majorCode).
		Group("major_name").Order("sample_count DESC").Order("major_name ASC").Scan(&rows).Error
	if err != nil {
		return joinrequests.MajorEvidence{}, err
	}
	for _, row := range rows {
		evidence.MajorCounts = append(evidence.MajorCounts, joinrequests.MajorCount{Major: row.Major, Count: row.Count})
		evidence.TotalSamples += row.Count
	}
	return evidence, nil
}

func (s *Store) ListMajorEvidence(ctx context.Context) ([]joinrequests.EvidenceSummary, joinrequests.RuleState, error) {
	state, err := s.GetAutomaticRuleState(ctx)
	if err != nil {
		return nil, joinrequests.RuleState{}, err
	}
	type countRow struct {
		EnrollmentYear string `gorm:"column:enrollment_year"`
		MajorCode      string `gorm:"column:major_code"`
		Major          string `gorm:"column:major"`
		Count          uint64 `gorm:"column:sample_count"`
	}
	var rows []countRow
	err = s.db.WithContext(ctx).Table("join_major_code_samples").
		Select("enrollment_year, major_code, major_name AS major, COUNT(*) AS sample_count").Where("active = TRUE").
		Group("enrollment_year, major_code, major_name").Order("enrollment_year DESC").Order("major_code ASC").Order("sample_count DESC").Scan(&rows).Error
	if err != nil {
		return nil, joinrequests.RuleState{}, err
	}
	byKey := make(map[string]int)
	result := make([]joinrequests.EvidenceSummary, 0)
	for _, row := range rows {
		key := row.EnrollmentYear + ":" + row.MajorCode
		index, ok := byKey[key]
		if !ok {
			index = len(result)
			byKey[key] = index
			result = append(result, joinrequests.EvidenceSummary{EnrollmentYear: row.EnrollmentYear, MajorCode: row.MajorCode})
		}
		result[index].MajorCounts = append(result[index].MajorCounts, joinrequests.MajorCount{Major: row.Major, Count: row.Count})
		result[index].TotalSamples += row.Count
	}
	return result, state, nil
}

func (s *Store) ListMajorEvidenceSamples(ctx context.Context, query joinrequests.EvidenceListQuery) (joinrequests.Page[joinrequests.EvidenceSample], error) {
	db := s.db.WithContext(ctx).Model(&joinMajorCodeSampleRow{})
	if query.EnrollmentYear != "" {
		db = db.Where("enrollment_year = ?", query.EnrollmentYear)
	}
	if query.MajorCode != "" {
		db = db.Where("major_code = ?", query.MajorCode)
	}
	if query.Active != nil {
		db = db.Where("active = ?", *query.Active)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		return joinrequests.Page[joinrequests.EvidenceSample]{}, err
	}
	var rows []joinMajorCodeSampleRow
	offset := (query.Page - 1) * query.Limit
	if err := db.Order("updated_at DESC").Order("sample_id DESC").Offset(offset).Limit(query.Limit).Find(&rows).Error; err != nil {
		return joinrequests.Page[joinrequests.EvidenceSample]{}, err
	}
	items := make([]joinrequests.EvidenceSample, len(rows))
	for index, row := range rows {
		items[index] = evidenceSampleFromRow(row)
	}
	return joinrequests.Page[joinrequests.EvidenceSample]{Items: items, HasMore: offset+len(items) < int(total), TotalCount: int(total)}, nil
}

func evidenceSampleFromRow(row joinMajorCodeSampleRow) joinrequests.EvidenceSample {
	return joinrequests.EvidenceSample{
		ID: row.SampleID, EnrollmentYear: row.EnrollmentYear, MajorCode: row.MajorCode, Major: row.MajorName,
		ApprovalSource: joinrequests.DecisionSource(row.ApprovalSource), SourceGroupID: strconv.FormatInt(row.SourceGroupID, 10),
		Active: row.Active, Version: row.Revision, UpdatedAt: row.UpdatedAt.UTC(),
	}
}

func (s *Store) UpdateMajorEvidenceSample(ctx context.Context, mutation joinrequests.EvidenceSampleMutation) (joinrequests.EvidenceSample, error) {
	var result joinrequests.EvidenceSample
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var before joinMajorCodeSampleRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("sample_id = ?", mutation.SampleID).Take(&before).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return joinrequests.ErrNotFound
			}
			return err
		}
		if before.Revision != mutation.ExpectedRevision {
			return joinrequests.ErrConflict
		}
		updates := map[string]any{"revision": gorm.Expr("revision + 1"), "updated_at": mutation.Context.OccurredAt.UTC()}
		if mutation.Patch.Major != nil {
			updates["major_name"] = *mutation.Patch.Major
			updates["normalized_major"] = normalizeEvidenceMajor(*mutation.Patch.Major)
		}
		if mutation.Patch.Active != nil {
			updates["active"] = *mutation.Patch.Active
		}
		actor := domainDecisionActor(mutation.Context.Actor)
		updates["corrected_by_type"] = actor.Type
		updates["corrected_by_user_id"] = actor.UserID
		updates["corrected_by_display_name"] = actor.DisplayName
		updated := tx.Model(&joinMajorCodeSampleRow{}).Where("sample_id = ? AND revision = ?", mutation.SampleID, mutation.ExpectedRevision).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		if err := tx.Model(&joinApprovalRuleStateRow{}).Where("rule_version = ?", joinrequests.AutoApprovalRuleVersion).
			Updates(map[string]any{"evidence_version": gorm.Expr("evidence_version + 1"), "revision": gorm.Expr("revision + 1")}).Error; err != nil {
			return err
		}
		var after joinMajorCodeSampleRow
		if err := tx.Where("sample_id = ?", mutation.SampleID).Take(&after).Error; err != nil {
			return err
		}
		result = evidenceSampleFromRow(after)
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: actor, OccurredAt: mutation.Context.OccurredAt, Request: mutation.Context.Request,
			Source: sourceForManagerActor(mutation.Context.Actor.Type), Action: "join_evidence_sample.update",
			TargetType: "join_major_code_sample", TargetID: strconv.FormatUint(mutation.SampleID, 10),
			Before: map[string]any{"major": before.MajorName, "active": before.Active, "revision": before.Revision},
			After:  map[string]any{"major": after.MajorName, "active": after.Active, "revision": after.Revision}, Metadata: map[string]any{},
		})
	})
	return result, err
}

func normalizeEvidenceMajor(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), "")
}

type approvedEvidenceSource struct {
	RequestID      uint64 `gorm:"column:request_id"`
	StudentID      string `gorm:"column:student_id"`
	Major          string `gorm:"column:major"`
	DecisionID     string `gorm:"column:decision_id"`
	ApprovalSource string `gorm:"column:approval_source"`
	GroupID        int64  `gorm:"column:group_id"`
}

func approvedEvidenceQuery(tx *gorm.DB) *gorm.DB {
	return tx.Table("group_join_requests AS request").Select(`request.id AS request_id, request.student_id, request.major,
decision.decision_id, decision.source AS approval_source, request.group_id`).
		Joins("JOIN group_join_decisions AS decision ON decision.decision_id = request.last_decision_id AND decision.request_id = request.id").
		Where("request.decision_status = ? AND request.ai_parse_status = ? AND decision.status = ? AND decision.action = ? AND decision.source IN ?",
			joinrequests.DecisionApproved, joinrequests.AIParseSucceeded, joinrequests.AttemptConfirmed, joinrequests.ActionApprove,
			[]joinrequests.DecisionSource{joinrequests.SourceManual, joinrequests.SourceAutomatic}).
		Where("request.student_id IS NOT NULL AND request.major IS NOT NULL AND request.group_id IS NOT NULL").
		Where("JSON_EXTRACT(request.validation_snapshot, '$.valid') = TRUE")
}

func insertEvidenceSource(tx *gorm.DB, source approvedEvidenceSource, now time.Time) (bool, error) {
	check := joinrequests.CheckStudentID(source.StudentID, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if !check.LengthValid || !check.Numeric || strings.TrimSpace(source.Major) == "" {
		return false, nil
	}
	row := joinMajorCodeSampleRow{
		EnrollmentYear: check.EnrollmentYear, MajorCode: check.MajorCode, MajorName: strings.TrimSpace(source.Major),
		NormalizedMajor: normalizeEvidenceMajor(source.Major), SourceRequestID: source.RequestID,
		SourceDecisionID: source.DecisionID, ApprovalSource: source.ApprovalSource, SourceGroupID: source.GroupID,
		Active: true, Revision: 1, CreatedAt: now.UTC(), UpdatedAt: now.UTC(),
	}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	return result.RowsAffected == 1, result.Error
}

func (s *Store) RebuildMajorEvidence(ctx context.Context, mutation joinrequests.EvidenceRebuildMutation) (joinrequests.EvidenceRebuildResult, error) {
	var result joinrequests.EvidenceRebuildResult
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		context := mutation.Context
		var state joinApprovalRuleStateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("rule_version = ?", joinrequests.AutoApprovalRuleVersion).Take(&state).Error; err != nil {
			return err
		}
		actorID := ""
		if context.Actor.UserID != nil {
			actorID = *context.Actor.UserID
		}
		if mutation.IdempotencyKey != "" && actorID != "" {
			var replay joinEvidenceRebuildOperationRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("actor_id = ? AND idempotency_key = ?", actorID, mutation.IdempotencyKey).Take(&replay).Error
			if err == nil {
				return opsUnmarshalJSON(replay.ResultJSON, &result)
			}
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		var sources []approvedEvidenceSource
		if err := approvedEvidenceQuery(tx).Order("request.id ASC").Scan(&sources).Error; err != nil {
			return err
		}
		for _, source := range sources {
			if _, err := insertEvidenceSource(tx, source, context.OccurredAt); err != nil {
				return err
			}
		}
		activatedAt := state.ActivatedAt
		if activatedAt == nil {
			value := context.OccurredAt.UTC()
			activatedAt = &value
		}
		updated := tx.Model(&joinApprovalRuleStateRow{}).Where("rule_version = ? AND revision = ?", state.RuleVersion, state.Revision).
			Updates(map[string]any{
				"status": joinrequests.RuleStateReady, "evidence_version": gorm.Expr("evidence_version + 1"),
				"activated_at": activatedAt, "rebuilt_at": context.OccurredAt.UTC(), "last_error_code": nil,
				"revision": gorm.Expr("revision + 1"), "updated_at": context.OccurredAt.UTC(),
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return joinrequests.ErrConflict
		}
		var activeCount int64
		if err := tx.Model(&joinMajorCodeSampleRow{}).Where("active = TRUE").Count(&activeCount).Error; err != nil {
			return err
		}
		if err := tx.Where("rule_version = ?", state.RuleVersion).Take(&state).Error; err != nil {
			return err
		}
		result = joinrequests.EvidenceRebuildResult{RuleState: ruleStateFromRow(state), SampleCount: uint64(activeCount)}
		if mutation.IdempotencyKey != "" && actorID != "" {
			payload, err := opsMarshalJSON(result)
			if err != nil {
				return err
			}
			if err := tx.Create(&joinEvidenceRebuildOperationRow{
				ActorID: actorID, IdempotencyKey: mutation.IdempotencyKey, ResultJSON: payload, CreatedAt: context.OccurredAt.UTC(),
			}).Error; err != nil {
				if isManagerDuplicateKey(err) {
					return joinrequests.ErrIdempotencyConflict
				}
				return err
			}
		}
		actor := domainDecisionActor(context.Actor)
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: actor, OccurredAt: context.OccurredAt, Request: context.Request,
			Source: sourceForManagerActor(context.Actor.Type), Action: "join_evidence.rebuild",
			TargetType: "join_approval_rule", TargetID: strconv.FormatUint(state.RuleVersion, 10),
			Before: map[string]any{"evidence_version": state.EvidenceVersion - 1},
			After:  map[string]any{"evidence_version": state.EvidenceVersion, "sample_count": activeCount}, Metadata: map[string]any{},
		})
	})
	return result, err
}

func (s *Store) IndexApprovedRequest(ctx context.Context, requestID string) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var source approvedEvidenceSource
		if err := approvedEvidenceQuery(tx).Where("request.flag = ?", requestID).Take(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		inserted, err := insertEvidenceSource(tx, source, time.Now().UTC())
		if err != nil || !inserted {
			return err
		}
		return tx.Model(&joinApprovalRuleStateRow{}).Where("rule_version = ?", joinrequests.AutoApprovalRuleVersion).
			Updates(map[string]any{"evidence_version": gorm.Expr("evidence_version + 1"), "revision": gorm.Expr("revision + 1")}).Error
	})
}

func (s *Store) GetAdmissionRosterStatus(ctx context.Context) (joinrequests.AdmissionRosterStatus, error) {
	var row admissionRosterVersionRow
	if err := s.db.WithContext(ctx).Where("status = ?", "active").Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return joinrequests.AdmissionRosterStatus{}, nil
		}
		return joinrequests.AdmissionRosterStatus{}, err
	}
	version, fileName := row.VersionID, row.FileName
	return joinrequests.AdmissionRosterStatus{
		Configured: true, DatasetVersion: &version, FileName: &fileName, RowCount: row.RowCount,
		ActivatedAt: utcTimePointer(row.ActivatedAt),
	}, nil
}

func (s *Store) Lookup(ctx context.Context, studentID string) (joinrequests.AdmissionRosterRecord, error) {
	status, err := s.GetAdmissionRosterStatus(ctx)
	if err != nil || !status.Configured || status.DatasetVersion == nil {
		return joinrequests.AdmissionRosterRecord{Configured: status.Configured}, err
	}
	record := joinrequests.AdmissionRosterRecord{Configured: true, DatasetVersion: *status.DatasetVersion}
	var row admissionRosterEntryRow
	if err := s.db.WithContext(ctx).Where("version_id = ? AND student_id = ?", *status.DatasetVersion, studentID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return record, nil
		}
		return joinrequests.AdmissionRosterRecord{}, err
	}
	record.Found = true
	if row.Major != nil {
		record.Major = *row.Major
	}
	return record, nil
}

func (s *Store) Import(ctx context.Context, input joinrequests.AdmissionRosterImport) (joinrequests.AdmissionRosterStatus, error) {
	var status joinrequests.AdmissionRosterStatus
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing admissionRosterVersionRow
		if err := tx.Where("idempotency_key = ?", input.IdempotencyKey).Take(&existing).Error; err == nil {
			if existing.ContentHash != input.ContentHash || existing.FileName != input.FileName {
				return joinrequests.ErrIdempotencyConflict
			}
			version, fileName := existing.VersionID, existing.FileName
			status = joinrequests.AdmissionRosterStatus{Configured: existing.Status == "active", DatasetVersion: &version, FileName: &fileName, RowCount: existing.RowCount, ActivatedAt: utcTimePointer(existing.ActivatedAt)}
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		versionID, err := newManagerID("ros")
		if err != nil {
			return err
		}
		now := input.Context.OccurredAt.UTC()
		if err := tx.Model(&admissionRosterVersionRow{}).Where("status = ?", "active").Updates(map[string]any{"status": "superseded", "revision": gorm.Expr("revision + 1")}).Error; err != nil {
			return err
		}
		actor := domainDecisionActor(input.Context.Actor)
		row := admissionRosterVersionRow{
			VersionID: versionID, IdempotencyKey: input.IdempotencyKey, ContentHash: input.ContentHash,
			FileName: input.FileName, Status: "active", RowCount: uint64(len(input.Entries)), Revision: 1,
			ImportedByType: actor.Type, ImportedByUserID: actor.UserID, ImportedByDisplayName: actor.DisplayName,
			CreatedAt: now, ActivatedAt: &now,
		}
		if err := tx.Create(&row).Error; err != nil {
			if isManagerDuplicateKey(err) {
				return joinrequests.ErrConflict
			}
			return err
		}
		entries := make([]admissionRosterEntryRow, len(input.Entries))
		for index, entry := range input.Entries {
			entries[index] = admissionRosterEntryRow{VersionID: versionID, StudentID: entry.StudentID, StudentName: optionalManagerString(entry.Name), Major: optionalManagerString(entry.Major), CreatedAt: now}
		}
		for start := 0; start < len(entries); start += 500 {
			end := start + 500
			if end > len(entries) {
				end = len(entries)
			}
			if err := tx.Create(entries[start:end]).Error; err != nil {
				return err
			}
		}
		fileName := input.FileName
		status = joinrequests.AdmissionRosterStatus{Configured: true, DatasetVersion: &versionID, FileName: &fileName, RowCount: uint64(len(entries)), ActivatedAt: &now}
		return writeManagerAudit(tx, managerAuditWrite{
			Actor: actor, OccurredAt: now, Request: input.Context.Request, Source: sourceForManagerActor(input.Context.Actor.Type),
			Action: "admission_roster.import", TargetType: "admission_roster", TargetID: versionID,
			Before: map[string]any{}, After: map[string]any{"version_id": versionID, "row_count": len(entries), "file_name": input.FileName}, Metadata: map[string]any{},
		})
	})
	return status, err
}

func optionalManagerString(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func sortedMajorCounts(values []joinrequests.MajorCount) []joinrequests.MajorCount {
	result := append([]joinrequests.MajorCount(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count == result[j].Count {
			return result[i].Major < result[j].Major
		}
		return result[i].Count > result[j].Count
	})
	return result
}
