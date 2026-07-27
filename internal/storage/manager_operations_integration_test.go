package storage

import (
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/analytics"
	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/customcommand"
	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/joinrequests"
	"github.com/zjutjh/jxh-go/internal/scheduledjobs"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/telemetry"
)

func TestManagerOperationsMySQLResourceLifecycle(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 5, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, now)
	principal := auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}
	request := auth.MutationContext{RequestID: "req_operations", IPAddress: "192.0.2.20", UserAgent: "integration-test"}
	managerIntegrationCreateGroup(t, store, principal, request, now, "10001")

	nextRun := now.Add(time.Hour)
	job, err := store.CreateScheduledJob(t.Context(), scheduledjobs.CreateMutation{
		Context: scheduledjobs.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(time.Minute)},
		Input: scheduledjobs.CreateInput{
			Name: "Morning notice", GroupID: "10001", Message: "Good morning",
			Schedule: scheduledjobs.Schedule{Type: scheduledjobs.TypeDaily, LocalTime: "06:00", Timezone: "Asia/Shanghai"},
			Enabled:  true,
		},
		NextRunAt: &nextRun,
	})
	if err != nil || job.ID == "" || job.Version != 1 || job.Status != scheduledjobs.StatusActive || job.UpdatedBy.UserID == nil {
		t.Fatalf("create scheduled job: job=%+v error=%v", job, err)
	}

	command, err := store.CreateCommand(t.Context(), customcommand.CreateMutation{
		Context: customcommand.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(2 * time.Minute)},
		Definition: customcommand.Definition{
			Name: "/hello", DisplayName: "Hello", Description: "Send a greeting",
			Scope:             customcommand.Scope{Type: customcommand.ScopeGroups, GroupIDs: []string{"10001"}},
			TriggerPermission: customcommand.TriggerEveryone,
			Actions:           []customcommand.Action{{Type: customcommand.ActionReplyText, Template: "Hello"}},
		},
		Status: customcommand.StatusActive, Enabled: true,
	})
	if err != nil || command.ID == "" || command.Version != 1 || command.Status != customcommand.StatusActive || command.UpdatedBy.UserID == nil {
		t.Fatalf("create custom command: command=%+v error=%v", command, err)
	}

	if _, err := sqlDB.ExecContext(t.Context(), `INSERT INTO group_join_requests
(flag, group_id, user_id, applicant_nickname, student_id, student_name, major, sub_type, comment,
 status, source, ai_parse_status, ai_parse_attempts, validation_snapshot, observed_status, decision_status,
 revision, requested_at, first_seen_at, last_seen_at, ai_parsed_at)
VALUES (?, ?, ?, ?, ?, ?, ?, 'add', ?, 'pending', 'event', 'succeeded', 1, ?, 'pending', 'pending', 1, ?, ?, ?, ?)`,
		"join-flag-1", 10001, 20001, "Applicant", "20260001", "Student", "Computer Science", "verification",
		`{"valid":true,"validation_errors":[]}`, now, now, now, now,
	); err != nil {
		t.Fatalf("seed join request: %v", err)
	}
	joinReservation, err := store.BeginDecisions(t.Context(), joinrequests.BeginMutation{
		Context: joinrequests.MutationContext{
			Actor: managerIntegrationAuditActor("usr_root"), Request: request, OccurredAt: now.Add(3 * time.Minute),
		},
		GroupID: "10001", Items: []joinrequests.VersionedRequest{{ID: "join-flag-1", Version: 1}},
		Action: joinrequests.ActionApprove, Source: joinrequests.SourceManual,
		IdempotencyKey: "join-decision-1", ProcessingExpiresAt: now.Add(4 * time.Minute),
	})
	if err != nil || joinReservation.Replay || len(joinReservation.Items) != 1 ||
		joinReservation.Items[0].Request.DecisionStatus != joinrequests.DecisionProcessing {
		t.Fatalf("begin join decision: reservation=%+v error=%v", joinReservation, err)
	}
	decisionID := joinReservation.Items[0].Decision.ID
	decisionResult, err := store.CompleteDecision(t.Context(), joinrequests.CompletionMutation{
		DecisionID: decisionID, RequestID: "join-flag-1", AttemptStatus: joinrequests.AttemptConfirmed,
		DecisionStatus: joinrequests.DecisionApproved, CompletedAt: now.Add(3*time.Minute + time.Second),
	})
	if err != nil || decisionResult.Request.DecisionStatus != joinrequests.DecisionApproved ||
		decisionResult.Request.Version != 3 || decisionResult.Decision.Status != joinrequests.AttemptConfirmed {
		t.Fatalf("complete join decision: result=%+v error=%v", decisionResult, err)
	}
	joinReplay, err := store.BeginDecisions(t.Context(), joinrequests.BeginMutation{
		Context: joinrequests.MutationContext{
			Actor: managerIntegrationAuditActor("usr_root"), Request: request, OccurredAt: now.Add(3 * time.Minute),
		},
		GroupID: "10001", Items: []joinrequests.VersionedRequest{{ID: "join-flag-1", Version: 1}},
		Action: joinrequests.ActionApprove, Source: joinrequests.SourceManual,
		IdempotencyKey: "join-decision-1", ProcessingExpiresAt: now.Add(4 * time.Minute),
	})
	if err != nil || !joinReplay.Replay || len(joinReplay.Items) != 1 || joinReplay.Items[0].Decision.ID != decisionID {
		t.Fatalf("replay join decision: reservation=%+v error=%v", joinReplay, err)
	}

	testSend, err := store.BeginScheduledJobTestSend(t.Context(), scheduledjobs.TestSendBegin{
		Context: scheduledjobs.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(4 * time.Minute)},
		JobID:   job.ID, ExpectedRevision: job.Version, IdempotencyKey: "scheduled-test-send-1",
	})
	if err != nil || !testSend.Fresh || testSend.Run.ID == "" || testSend.Run.CompletedAt != nil {
		t.Fatalf("begin scheduled test send: reservation=%+v error=%v", testSend, err)
	}
	completedRun, err := store.CompleteScheduledJobTestSend(t.Context(), scheduledjobs.TestSendCompletion{
		ExecutionID: testSend.ExecutionID, RunID: testSend.Run.ID, Result: scheduledjobs.RunSuccess,
		CompletedAt: now.Add(4*time.Minute + time.Second), Duration: 125 * time.Millisecond, MessageID: "message-1",
	})
	if err != nil || completedRun.Result != scheduledjobs.RunSuccess || completedRun.CompletedAt == nil ||
		completedRun.MessageID == nil || *completedRun.MessageID != "message-1" {
		t.Fatalf("complete scheduled test send: run=%+v error=%v", completedRun, err)
	}
	testReplay, err := store.BeginScheduledJobTestSend(t.Context(), scheduledjobs.TestSendBegin{
		Context: scheduledjobs.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(4 * time.Minute)},
		JobID:   job.ID, ExpectedRevision: job.Version, IdempotencyKey: "scheduled-test-send-1",
	})
	if err != nil || testReplay.Fresh || testReplay.Run.ID != completedRun.ID {
		t.Fatalf("replay scheduled test send: reservation=%+v error=%v", testReplay, err)
	}

	occurrenceID := "scheduled-" + job.ID + "-20260728"
	numericJobID := managerIntegrationJobID(t, job.ID)
	failedReservation, err := store.BeginScheduledJobRun(t.Context(), numericJobID, occurrenceID, now, now)
	if err != nil || !failedReservation.Fresh || failedReservation.RunID == "" {
		t.Fatalf("begin failed scheduled run: reservation=%+v error=%v", failedReservation, err)
	}
	if err := store.CompleteScheduledJobRun(t.Context(), scheduler.RunCompletion{
		RunID: failedReservation.RunID, Result: scheduler.RunFailed,
		CompletedAt: now.Add(time.Second), Duration: time.Second, ErrorCode: "send_rejected",
	}); err != nil {
		t.Fatalf("complete failed scheduled run: %v", err)
	}
	retryReservation, err := store.BeginScheduledJobRun(t.Context(), numericJobID, occurrenceID, now, now.Add(2*time.Second))
	if err != nil || !retryReservation.Fresh || retryReservation.RunID == failedReservation.RunID {
		t.Fatalf("begin scheduled retry: reservation=%+v error=%v", retryReservation, err)
	}
	if err := store.CompleteScheduledJobRun(t.Context(), scheduler.RunCompletion{
		RunID: retryReservation.RunID, Result: scheduler.RunSuccess,
		CompletedAt: now.Add(3 * time.Second), Duration: time.Second,
	}); err != nil {
		t.Fatalf("complete successful scheduled retry: %v", err)
	}
	terminalReservation, err := store.BeginScheduledJobRun(t.Context(), numericJobID, occurrenceID, now, now.Add(4*time.Second))
	if err != nil || terminalReservation.Fresh || terminalReservation.RunID != retryReservation.RunID || terminalReservation.Result != scheduler.RunSuccess {
		t.Fatalf("replay successful scheduled occurrence: reservation=%+v error=%v", terminalReservation, err)
	}

	recordedRun, err := store.RecordCommandRun(t.Context(), customcommand.Run{
		RunIdentity: "command-run-identity-1", CommandID: command.ID, CommandName: command.Name,
		GroupID: "10001", TriggeredByQQ: "20001", Result: customcommand.RunSuccess,
		ArgumentSummaries: []customcommand.ArgumentSummary{{Name: "text", Type: customcommand.ParameterText, Present: true, RuneLength: 5}},
		ActionSteps:       []customcommand.ActionStep{{Index: 0, Type: customcommand.ActionReplyText, Result: customcommand.StepSuccess, Duration: 20 * time.Millisecond}},
		Duration:          25 * time.Millisecond, RequestID: "req_command_run", OccurredAt: now.Add(5 * time.Minute),
	})
	if err != nil || recordedRun.ID == "" || recordedRun.Result != customcommand.RunSuccess || len(recordedRun.ActionSteps) != 1 {
		t.Fatalf("record command run: run=%+v error=%v", recordedRun, err)
	}

	actorHash := strings.Repeat("a", 64)
	if err := store.AppendTelemetryEvents(t.Context(), []telemetry.Event{
		{Kind: telemetry.EventGroupMessage, OccurredAt: now.Add(6 * time.Minute), GroupID: "10001", UserKey: actorHash, Result: telemetry.ResultSuccess, Count: 1},
		{Kind: telemetry.EventAIRequest, OccurredAt: now.Add(6*time.Minute + time.Second), GroupID: "10001", UserKey: actorHash, FeatureKey: "ai_qa", Result: telemetry.ResultSuccess, DurationMS: 125, Count: 2},
		{Kind: telemetry.EventCommandRun, OccurredAt: now.Add(6*time.Minute + 2*time.Second), GroupID: "10001", UserKey: actorHash, FeatureKey: "custom_commands", Result: telemetry.ResultSuccess, CommandID: command.ID, Count: 1},
		{Kind: telemetry.EventScheduledJobRun, OccurredAt: now.Add(6*time.Minute + 3*time.Second), GroupID: "10001", Result: telemetry.ResultSuccess, DurationMS: 50, JobID: job.ID, Count: 1},
		{Kind: telemetry.EventManualApproval, OccurredAt: now.Add(6*time.Minute + 4*time.Second), GroupID: "10001", Result: telemetry.ResultSuccess, Count: 1},
		{Kind: telemetry.EventAutomaticApproval, OccurredAt: now.Add(6*time.Minute + 5*time.Second), GroupID: "10001", Result: telemetry.ResultFailed, Count: 1},
		{Kind: telemetry.EventQuote, OccurredAt: now.Add(6*time.Minute + 6*time.Second), GroupID: "10001", FeatureKey: "quote", Result: telemetry.ResultSuccess, DurationMS: 20, Count: 1},
		{Kind: telemetry.EventQuote, OccurredAt: now.Add(6*time.Minute + 7*time.Second), GroupID: "10001", FeatureKey: "quote", Result: telemetry.ResultFallback, DurationMS: 30, Count: 1},
		{Kind: telemetry.EventQuote, OccurredAt: now.Add(6*time.Minute + 8*time.Second), GroupID: "10001", FeatureKey: "quote", Result: telemetry.ResultFailed, DurationMS: 40, Count: 1},
	}); err != nil {
		t.Fatalf("append telemetry: %v", err)
	}
	if err := store.AggregateTelemetryDaily(t.Context(), now.AddDate(0, 0, 1), "UTC"); err != nil {
		t.Fatalf("aggregate UTC telemetry: %v", err)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM bot_operation_daily WHERE timezone = 'UTC'", 76)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM bot_operation_events WHERE event_type = 'scheduled_job_run' AND job_id = "+strconv.FormatUint(numericJobID, 10)+" AND command_id IS NULL", 1)
	filter := analytics.Filter{
		From: now.Add(-time.Hour), To: now.Add(time.Hour), GroupIDs: []string{"10001"}, Timezone: "Asia/Shanghai",
	}
	summary, err := store.LoadSummary(t.Context(), filter)
	if err != nil || managerMetricValue(summary, analytics.MetricGroupMessageCount) != 1 ||
		managerMetricValue(summary, analytics.MetricAIRequestCount) != 2 ||
		managerMetricValue(summary, analytics.MetricActiveUserCount) != 1 ||
		managerMetricValue(summary, analytics.MetricManualApprovalCount) != 1 ||
		managerMetricValue(summary, analytics.MetricAutomaticApprovalCount) != 1 ||
		managerMetricValue(summary, analytics.MetricScheduledJobRunCount) != 1 ||
		managerMetricValue(summary, analytics.MetricQuoteSuccessCount) != 1 ||
		managerMetricValue(summary, analytics.MetricQuoteFallbackCount) != 1 ||
		managerMetricValue(summary, analytics.MetricQuoteFailureCount) != 1 {
		t.Fatalf("load analytics summary: summary=%+v error=%v", summary, err)
	}
	rankings, err := store.LoadRankings(t.Context(), analytics.StoreRankingsQuery{
		Filter: filter, Dimension: analytics.DimensionGroup, Metric: analytics.MetricGroupMessageCount, Limit: 10,
	})
	if err != nil || len(rankings.Items) != 1 || rankings.Items[0].Key != "10001" || rankings.Items[0].Value != 1 {
		t.Fatalf("load analytics rankings: rankings=%+v error=%v", rankings, err)
	}
	timeseries, err := store.LoadTimeseries(t.Context(), analytics.StoreTimeseriesQuery{
		Filter: filter, Granularity: analytics.GranularityHour, Metrics: []analytics.MetricKey{analytics.MetricAIRequestCount},
	})
	if err != nil || len(timeseries.Points[analytics.MetricAIRequestCount]) == 0 {
		t.Fatalf("load analytics timeseries: timeseries=%+v error=%v", timeseries, err)
	}

	joinRows, err := store.OpenJoinRequestExport(t.Context(), filter)
	if err != nil {
		t.Fatalf("open join request export: %v", err)
	}
	defer joinRows.Close()
	joinRow, ok, err := joinRows.Next(t.Context())
	if err != nil || !ok || joinRow.RequestID != "join-flag-1" || joinRow.DecisionStatus != string(joinrequests.DecisionApproved) {
		t.Fatalf("read join request export: row=%+v ok=%t error=%v", joinRow, ok, err)
	}
	scheduledRows, err := store.OpenScheduledJobRunExport(t.Context(), filter)
	if err != nil {
		t.Fatalf("open scheduled run export: %v", err)
	}
	defer scheduledRows.Close()
	wantScheduledRuns := map[string]analytics.Result{
		completedRun.ID: analytics.ResultSuccess, failedReservation.RunID: analytics.ResultFailed,
		retryReservation.RunID: analytics.ResultSuccess,
	}
	for {
		scheduledRow, ok, err := scheduledRows.Next(t.Context())
		if err != nil {
			t.Fatalf("read scheduled run export: %v", err)
		}
		if !ok {
			break
		}
		want, expected := wantScheduledRuns[scheduledRow.RunID]
		if !expected {
			continue
		}
		if scheduledRow.Result != want || scheduledRow.GroupID != "10001" {
			t.Fatalf("scheduled run export row=%+v want_result=%s", scheduledRow, want)
		}
		delete(wantScheduledRuns, scheduledRow.RunID)
	}
	if len(wantScheduledRuns) != 0 {
		t.Fatalf("scheduled run export missing=%v", wantScheduledRuns)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action IN ('scheduled_job.create', 'custom_command.create')", 2)
	assertManagerAuthCount(t, sqlDB, `SELECT
(SELECT COUNT(*) FROM scheduled_jobs WHERE updated_by_type = 'admin_user' AND updated_by_role = 'super_admin') +
(SELECT COUNT(*) FROM custom_commands WHERE updated_by_type = 'admin_user' AND updated_by_role = 'super_admin')`, 2)
}

func TestManagerAuditWritersSanitizePayloadsBeforeMySQLPersistence(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, now)
	principal := auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}
	request := auth.MutationContext{RequestID: "req_audit_sanitize", IPAddress: "192.0.2.30", UserAgent: "integration-test"}

	authPayload := map[string]any{
		"safe": "auth-visible",
		"nested": []any{
			map[string]any{"password": "auth-plain-password", "token": "auth-raw-token"},
			map[string]any{"cookie": "auth-raw-cookie"},
		},
	}
	if err := writeManagerAuthAudit(store.db.WithContext(t.Context()), managerAuthAuditWrite{
		ID: "aud_auth_sanitize", Actor: principal, Context: request, At: now,
		Action: "test.auth_sanitize", Target: audit.Target{Type: "admin_user", ID: "usr_root"},
		Before: authPayload,
		After: map[string]any{
			"safe":     "auth-after-visible",
			"upstream": map[string]any{"body": "auth-upstream-body"},
		},
		Metadata: map[string]any{"raw_response": "auth-raw-response"},
	}); err != nil {
		t.Fatalf("write auth audit: %v", err)
	}

	role := string(auth.RoleSuperAdmin)
	actorID := principal.UserID
	if err := writeManagerAudit(store.db.WithContext(t.Context()), managerAuditWrite{
		Actor:      OpsActorColumns{Type: string(audit.ActorAdminUser), UserID: &actorID, DisplayName: "Root Admin", Role: &role},
		OccurredAt: now.Add(time.Second), Request: request, Action: "test.operations_sanitize",
		TargetType: "scheduled_job", TargetID: "job-1",
		Before: map[string]any{
			"safe":          "operations-visible",
			"password_hash": "operations-password-hash",
		},
		After: map[string]any{"session_token": "operations-session-token", "safe": "operations-after-visible"},
		Metadata: map[string]any{
			"nested":            []any{map[string]any{"cookie_header": "operations-cookie"}},
			"upstream_raw_body": "operations-upstream-body",
		},
	}); err != nil {
		t.Fatalf("write operations audit: %v", err)
	}

	assertPersistedAuditSanitized(t, sqlDB, "test.auth_sanitize",
		[]string{"auth-plain-password", "auth-raw-token", "auth-raw-cookie", "auth-upstream-body", "auth-raw-response"},
		[]string{"auth-visible", "auth-after-visible"},
	)
	assertPersistedAuditSanitized(t, sqlDB, "test.operations_sanitize",
		[]string{"operations-password-hash", "operations-session-token", "operations-cookie", "operations-upstream-body"},
		[]string{"operations-visible", "operations-after-visible"},
	)

	if authPayload["nested"].([]any)[0].(map[string]any)["password"] != "auth-plain-password" {
		t.Fatalf("audit persistence mutated the source payload: %#v", authPayload)
	}
}

func assertPersistedAuditSanitized(t *testing.T, db *sql.DB, action string, secrets, visible []string) {
	t.Helper()
	var before, after, metadata []byte
	var redacted bool
	if err := db.QueryRowContext(t.Context(), `SELECT before_snapshot, after_snapshot, metadata, redacted
FROM admin_audit_logs WHERE action = ?`, action).Scan(&before, &after, &metadata, &redacted); err != nil {
		t.Fatal(err)
	}
	persisted := string(before) + string(after) + string(metadata)
	if !redacted {
		t.Fatalf("audit %q was not marked redacted: %s", action, persisted)
	}
	for _, secret := range secrets {
		if strings.Contains(persisted, secret) {
			t.Fatalf("audit %q persisted sensitive value %q: %s", action, secret, persisted)
		}
	}
	for _, marker := range visible {
		if !strings.Contains(persisted, marker) {
			t.Fatalf("audit %q lost safe value %q: %s", action, marker, persisted)
		}
	}
}

func managerMetricValue(summary analytics.SummaryData, key analytics.MetricKey) float64 {
	value := summary.Values[key]
	if !value.Available || value.Value == nil {
		return -1
	}
	return *value.Value
}

func managerIntegrationJobID(t *testing.T, value string) uint64 {
	t.Helper()
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		t.Fatalf("parse scheduled job ID %q: %v", value, err)
	}
	return id
}

func managerIntegrationCreateGroup(
	t *testing.T,
	store *Store,
	principal auth.Principal,
	request auth.MutationContext,
	at time.Time,
	groupID string,
) {
	t.Helper()
	reservation, err := store.BeginGroupSync(t.Context(), groups.BeginSync{
		Context:        groups.MutationContext{Actor: principal, Request: request, OccurredAt: at},
		IdempotencyKey: "groups-sync-" + groupID,
	})
	if err != nil {
		t.Fatalf("begin group sync: %v", err)
	}
	if _, err := store.CompleteGroupSync(t.Context(), groups.CompleteSync{
		ExecutionID: reservation.ExecutionID, CompletedAt: at.Add(time.Second),
		Groups: []groups.RemoteGroup{{
			ID: groupID, Name: "Integration Group " + groupID, MemberCount: 25, MaxMemberCount: 500, BotRole: groups.RoleAdmin,
		}},
	}); err != nil {
		t.Fatalf("complete group sync: %v", err)
	}
}
