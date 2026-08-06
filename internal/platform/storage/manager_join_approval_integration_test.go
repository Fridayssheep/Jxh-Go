package storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestJoinApprovalStorageIntegration(t *testing.T) {
	dsn := os.Getenv("JXH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("JXH_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := NewStore(db)
	resetJoinApprovalIntegrationData(t, db)

	activation := time.Date(2026, time.August, 7, 12, 0, 0, 0, time.UTC)
	if err := db.Exec(`INSERT INTO managed_groups
(group_id, name, member_count, max_member_count, bot_role, snapshot_state, revision, created_at, updated_at)
VALUES (?, ?, 0, 500, 'admin', 'fresh', 1, ?, ?)`, 10001, "Integration Group", activation.Add(-2*time.Hour), activation.Add(-2*time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := ensureManagerJoinPolicy(db, 10001, activation.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE group_join_policies SET enabled = TRUE, auto_reject = TRUE, updated_at = ? WHERE group_id = ?", activation.Add(-2*time.Hour), 10001).Error; err != nil {
		t.Fatal(err)
	}

	insertApprovedJoinRequest(t, db, "approved-manual", "302025315326", "计算机类", joinrequests.SourceManual, activation.Add(-24*time.Hour))
	if err := db.Exec("UPDATE group_join_requests SET validation_snapshot = NULL WHERE flag = ?", "approved-manual").Error; err != nil {
		t.Fatal(err)
	}
	insertApprovedJoinRequest(t, db, "approved-automatic", "302025315327", "计算机类", joinrequests.SourceAutomatic, activation.Add(-23*time.Hour))
	insertApprovedJoinRequest(t, db, "approved-invalid-id", "30202531532", "计算机类", joinrequests.SourceManual, activation.Add(-22*time.Hour))
	insertApprovedJoinRequest(t, db, "approved-invalid-fields", "302025315329", "计算机类", joinrequests.SourceManual, activation.Add(-22*time.Hour))
	if err := db.Exec("UPDATE group_join_requests SET comment = ? WHERE flag = ?", "302025315329 测试同学", "approved-invalid-fields").Error; err != nil {
		t.Fatal(err)
	}
	insertUnapprovedJoinRequest(t, db, "rejected-manual", "302025315328", "计算机类", activation.Add(-21*time.Hour), joinrequests.DecisionRejected)

	rebuild, err := store.RebuildMajorEvidence(ctx, joinrequests.EvidenceRebuildMutation{Context: systemJoinMutation("integration-rebuild", activation)})
	if err != nil {
		t.Fatal(err)
	}
	if rebuild.SampleCount != 2 || rebuild.RuleState.ActivatedAt == nil || !rebuild.RuleState.ActivatedAt.Equal(activation) {
		t.Fatalf("unexpected rebuild result: %+v", rebuild)
	}
	evidence, err := store.GetMajorEvidence(ctx, "2025", "315")
	if err != nil {
		t.Fatal(err)
	}
	if evidence.TotalSamples != 2 || len(evidence.MajorCounts) != 1 || evidence.MajorCounts[0].Major != "计算机类" {
		t.Fatalf("unexpected historical evidence: %+v", evidence)
	}
	samples, err := store.ListMajorEvidenceSamples(ctx, joinrequests.EvidenceListQuery{EnrollmentYear: "2025", MajorCode: "315", Page: 1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(samples.Items) != 2 || samples.Items[0].ApprovalSource == samples.Items[1].ApprovalSource {
		t.Fatalf("manual and automatic sources were not both retained: %+v", samples.Items)
	}

	oldRequest := insertPendingJoinRequest(t, db, "pending-before-v2", "302026315326", activation.Add(-time.Minute))
	newRequest := insertPendingJoinRequest(t, db, "pending-after-v2", "302026315327", activation.Add(time.Minute))
	candidates, err := store.ListAutoCandidates(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Request.ID != newRequest || candidates[0].Request.ID == oldRequest {
		t.Fatalf("activation cutoff selected wrong candidates: %+v", candidates)
	}

	first := samples.Items[0]
	correctedMajor := "计算机科学与技术"
	inactive := false
	updated, err := store.UpdateMajorEvidenceSample(ctx, joinrequests.EvidenceSampleMutation{
		Context:  systemJoinMutation("integration-sample-update", activation.Add(2*time.Minute)),
		SampleID: first.ID, ExpectedRevision: first.Version,
		Patch: joinrequests.EvidenceSamplePatch{Major: &correctedMajor, Active: &inactive},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Active || updated.Major != correctedMajor || updated.Version != first.Version+1 {
		t.Fatalf("unexpected updated sample: %+v", updated)
	}
	_, err = store.UpdateMajorEvidenceSample(ctx, joinrequests.EvidenceSampleMutation{
		Context:  systemJoinMutation("integration-sample-conflict", activation.Add(3*time.Minute)),
		SampleID: first.ID, ExpectedRevision: first.Version, Patch: joinrequests.EvidenceSamplePatch{Active: &inactive},
	})
	if !errors.Is(err, joinrequests.ErrConflict) {
		t.Fatalf("stale sample revision must conflict, got %v", err)
	}

	adminID := "integration-admin"
	adminMutation := joinrequests.MutationContext{
		Actor:   audit.Actor{Type: audit.ActorAdminUser, UserID: &adminID, DisplayName: "Integration Admin"},
		Request: auth.MutationContext{RequestID: "integration-idempotent-rebuild"}, OccurredAt: activation.Add(4 * time.Minute),
	}
	firstReplay, err := store.RebuildMajorEvidence(ctx, joinrequests.EvidenceRebuildMutation{Context: adminMutation, IdempotencyKey: "integration-rebuild-key"})
	if err != nil {
		t.Fatal(err)
	}
	secondReplay, err := store.RebuildMajorEvidence(ctx, joinrequests.EvidenceRebuildMutation{Context: adminMutation, IdempotencyKey: "integration-rebuild-key"})
	if err != nil {
		t.Fatal(err)
	}
	if firstReplay.RuleState.EvidenceVersion != secondReplay.RuleState.EvidenceVersion || firstReplay.SampleCount != secondReplay.SampleCount {
		t.Fatalf("rebuild idempotency changed result: first=%+v second=%+v", firstReplay, secondReplay)
	}

	rosterOne, err := store.Import(ctx, joinrequests.AdmissionRosterImport{
		Context:        systemJoinMutation("integration-roster-1", activation.Add(5*time.Minute)),
		IdempotencyKey: "integration-roster-key-1", FileName: "roster-1.csv", ContentHash: "hash-1",
		Entries: []joinrequests.AdmissionRosterEntry{{StudentID: "302026315326", Name: "测试同学", Major: "计算机类"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	lookup, err := store.Lookup(ctx, "302026315326")
	if err != nil || !lookup.Configured || !lookup.Found || lookup.Major != "计算机类" {
		t.Fatalf("unexpected roster lookup: %+v err=%v", lookup, err)
	}
	replayedRoster, err := store.Import(ctx, joinrequests.AdmissionRosterImport{
		Context:        systemJoinMutation("integration-roster-1-replay", activation.Add(6*time.Minute)),
		IdempotencyKey: "integration-roster-key-1", FileName: "roster-1.csv", ContentHash: "hash-1",
		Entries: []joinrequests.AdmissionRosterEntry{{StudentID: "302026315326", Name: "测试同学", Major: "计算机类"}},
	})
	if err != nil || replayedRoster.DatasetVersion == nil || rosterOne.DatasetVersion == nil || *replayedRoster.DatasetVersion != *rosterOne.DatasetVersion {
		t.Fatalf("roster replay failed: original=%+v replay=%+v err=%v", rosterOne, replayedRoster, err)
	}
	_, err = store.Import(ctx, joinrequests.AdmissionRosterImport{
		Context:        systemJoinMutation("integration-roster-conflict", activation.Add(7*time.Minute)),
		IdempotencyKey: "integration-roster-key-1", FileName: "roster-1.csv", ContentHash: "different-hash",
		Entries: []joinrequests.AdmissionRosterEntry{{StudentID: "302026315326", Major: "计算机类"}},
	})
	if !errors.Is(err, joinrequests.ErrIdempotencyConflict) {
		t.Fatalf("reused roster key with different content must conflict, got %v", err)
	}

	rosterTwo, err := store.Import(ctx, joinrequests.AdmissionRosterImport{
		Context:        systemJoinMutation("integration-roster-2", activation.Add(8*time.Minute)),
		IdempotencyKey: "integration-roster-key-2", FileName: "roster-2.xlsx", ContentHash: "hash-2",
		Entries: []joinrequests.AdmissionRosterEntry{{StudentID: "302026315329", Major: "机械类"}},
	})
	if err != nil || rosterTwo.DatasetVersion == nil || rosterOne.DatasetVersion == nil || *rosterTwo.DatasetVersion == *rosterOne.DatasetVersion {
		t.Fatalf("roster version did not switch: first=%+v second=%+v err=%v", rosterOne, rosterTwo, err)
	}
	lookup, err = store.Lookup(ctx, "302026315326")
	if err != nil || lookup.Found || lookup.DatasetVersion != *rosterTwo.DatasetVersion {
		t.Fatalf("lookup did not use new active roster: %+v err=%v", lookup, err)
	}
	var oldStatus string
	if err := db.Table("admission_roster_versions").Select("status").Where("version_id = ?", *rosterOne.DatasetVersion).Scan(&oldStatus).Error; err != nil || oldStatus != "superseded" {
		t.Fatalf("old roster status = %q, err=%v", oldStatus, err)
	}

	_, err = store.Import(ctx, joinrequests.AdmissionRosterImport{
		Context:        systemJoinMutation("integration-roster-rollback", activation.Add(9*time.Minute)),
		IdempotencyKey: "integration-roster-key-rollback", FileName: "broken.csv", ContentHash: "hash-broken",
		Entries: []joinrequests.AdmissionRosterEntry{{StudentID: "302026315330"}, {StudentID: "302026315330"}},
	})
	if err == nil {
		t.Fatal("duplicate roster entries must fail")
	}
	status, err := store.GetAdmissionRosterStatus(ctx)
	if err != nil || status.DatasetVersion == nil || *status.DatasetVersion != *rosterTwo.DatasetVersion {
		t.Fatalf("failed import replaced active roster: %+v err=%v", status, err)
	}

	var leakedAuditRows int64
	if err := db.Table("admin_audit_logs").Where(`
COALESCE(CAST(before_snapshot AS CHAR), '') LIKE ? OR
COALESCE(CAST(after_snapshot AS CHAR), '') LIKE ? OR
COALESCE(CAST(metadata AS CHAR), '') LIKE ?`, "%302026315326%", "%302026315326%", "%302026315326%").Count(&leakedAuditRows).Error; err != nil {
		t.Fatal(err)
	}
	if leakedAuditRows != 0 {
		t.Fatalf("audit snapshots leaked full student IDs in %d rows", leakedAuditRows)
	}
}

func resetJoinApprovalIntegrationData(t *testing.T, db *gorm.DB) {
	t.Helper()
	statements := []string{
		"DELETE FROM admission_roster_entries",
		"DELETE FROM admission_roster_versions",
		"DELETE FROM join_evidence_rebuild_operations",
		"DELETE FROM join_major_code_samples",
		"UPDATE group_join_requests SET last_decision_id = NULL WHERE last_decision_id IS NOT NULL",
		"DELETE FROM group_join_decisions",
		"DELETE FROM group_join_requests",
		"DELETE FROM group_join_policies",
		"DELETE FROM managed_groups",
		"DELETE FROM admin_audit_logs",
		"UPDATE join_approval_rule_state SET status = 'building', evidence_version = 0, activated_at = NULL, rebuilt_at = NULL, last_error_code = NULL, revision = 1 WHERE rule_version = 2",
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatalf("reset integration data with %q: %v", statement, err)
		}
	}
}

func insertApprovedJoinRequest(t *testing.T, db *gorm.DB, flag, studentID, major string, source joinrequests.DecisionSource, at time.Time) {
	t.Helper()
	internalID := insertIntegrationJoinRequest(t, db, flag, studentID, major, at, joinrequests.DecisionPending)
	decisionID := "decision-" + flag
	if err := db.Exec(`INSERT INTO group_join_decisions
(decision_id, request_id, idempotency_key, action, status, source, reason, actor_type, actor_display_name, field_snapshot, validation_snapshot, rule_version, trace_id, started_at, completed_at)
VALUES (?, ?, ?, 'approve', 'confirmed', ?, NULL, 'system', 'integration', JSON_OBJECT('student_id', ?, 'name', '测试同学', 'major', ?), JSON_OBJECT('valid', TRUE, 'validation_errors', JSON_ARRAY()), 1, ?, ?, ?)`,
		decisionID, internalID, "idempotency-"+flag, source, studentID, major, "trace-"+flag, at, at.Add(time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`UPDATE group_join_requests
SET status = 'processed', decision_status = 'approved', decision_source = ?, last_decision_id = ?, revision = revision + 1, processed_at = ?
WHERE id = ?`, source, decisionID, at.Add(time.Second), internalID).Error; err != nil {
		t.Fatal(err)
	}
}

func insertUnapprovedJoinRequest(t *testing.T, db *gorm.DB, flag, studentID, major string, at time.Time, status joinrequests.DecisionStatus) {
	t.Helper()
	internalID := insertIntegrationJoinRequest(t, db, flag, studentID, major, at, status)
	if err := db.Exec("UPDATE group_join_requests SET status = 'processed', decision_status = ?, revision = revision + 1 WHERE id = ?", status, internalID).Error; err != nil {
		t.Fatal(err)
	}
}

func insertPendingJoinRequest(t *testing.T, db *gorm.DB, flag, studentID string, at time.Time) string {
	t.Helper()
	insertIntegrationJoinRequest(t, db, flag, studentID, "计算机类", at, joinrequests.DecisionPending)
	return flag
}

func insertIntegrationJoinRequest(t *testing.T, db *gorm.DB, flag, studentID, major string, at time.Time, decisionStatus joinrequests.DecisionStatus) uint64 {
	t.Helper()
	if err := db.Exec(`INSERT INTO group_join_requests
(flag, group_id, user_id, applicant_nickname, student_id, student_name, major, sub_type, comment, status,
 observed_status, decision_status, source, ai_parse_status, ai_parse_attempts, validation_snapshot,
 requested_at, first_seen_at, last_seen_at, ai_parsed_at)
VALUES (?, 10001, 123456, 'Tester', ?, '测试同学', ?, 'add', ?, 'pending',
 'pending', ?, 'event', 'succeeded', 1, JSON_OBJECT('valid', TRUE, 'validation_errors', JSON_ARRAY()), ?, ?, ?, ?)`,
		flag, studentID, major, studentID+" 测试同学 "+major, decisionStatus, at, at, at, at).Error; err != nil {
		t.Fatal(err)
	}
	var id uint64
	if err := db.Table("group_join_requests").Select("id").Where("flag = ?", flag).Scan(&id).Error; err != nil || id == 0 {
		t.Fatalf("load inserted request id for %s: id=%d err=%v", flag, id, err)
	}
	return id
}

func systemJoinMutation(requestID string, at time.Time) joinrequests.MutationContext {
	return joinrequests.MutationContext{
		Actor:   audit.Actor{Type: audit.ActorSystem, DisplayName: "integration"},
		Request: auth.MutationContext{RequestID: requestID}, OccurredAt: at.UTC(),
	}
}
