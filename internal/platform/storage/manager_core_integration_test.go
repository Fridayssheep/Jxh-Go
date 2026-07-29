package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/groups/joinrequests"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/overview"
	"github.com/zjutjh/jxh-go/internal/management/settings"
)

func TestManagerCoreMySQLResourceLifecycle(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 4, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, now)
	principal := auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}
	request := auth.MutationContext{RequestID: "req_core", IPAddress: "192.0.2.10", UserAgent: "integration-test"}

	reservation, err := store.BeginGroupSync(t.Context(), groups.BeginSync{
		Context:        groups.MutationContext{Actor: principal, Request: request, OccurredAt: now},
		IdempotencyKey: "groups-sync-core-1",
	})
	if err != nil || !reservation.Fresh || !reservation.InProgress || reservation.ExecutionID == "" {
		t.Fatalf("begin group sync: reservation=%+v error=%v", reservation, err)
	}
	syncResult, err := store.CompleteGroupSync(t.Context(), groups.CompleteSync{
		ExecutionID: reservation.ExecutionID,
		CompletedAt: now.Add(time.Second),
		Groups: []groups.RemoteGroup{{
			ID: "10001", Name: "Integration Group", MemberCount: 25, MaxMemberCount: 500, BotRole: groups.RoleAdmin,
		}},
	})
	if err != nil || syncResult.AddedCount != 1 || syncResult.TotalCount != 1 {
		t.Fatalf("complete group sync: result=%+v error=%v", syncResult, err)
	}
	replay, err := store.BeginGroupSync(t.Context(), groups.BeginSync{
		Context:        groups.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(2 * time.Second)},
		IdempotencyKey: "groups-sync-core-1",
	})
	if err != nil || replay.Fresh || replay.InProgress || replay.Result == nil || *replay.Result != syncResult {
		t.Fatalf("replay group sync: reservation=%+v error=%v", replay, err)
	}

	group, found, err := store.GetGroup(t.Context(), "10001")
	if err != nil || !found || group.Name != "Integration Group" || group.BotRole != groups.RoleAdmin || len(group.Features) != 6 {
		t.Fatalf("get group: group=%+v found=%t error=%v", group, found, err)
	}
	policy, found, err := store.GetPolicy(t.Context(), "10001")
	if err != nil || !found || policy.Enabled || policy.Version != 1 || policy.Mode != joinrequests.PolicyModeAIFieldsComplete {
		t.Fatalf("get default join policy: policy=%+v found=%t error=%v", policy, found, err)
	}
	policy, err = store.UpdatePolicy(t.Context(), joinrequests.PolicyMutation{
		Context: joinrequests.MutationContext{
			Actor: managerIntegrationAuditActor("usr_root"), Request: request,
			OccurredAt: now.Add(2500 * time.Millisecond),
		},
		GroupID: "10001", ExpectedRevision: policy.Version, Patch: joinrequests.PolicyPatch{
			Enabled: auth.Field[bool]{Set: true, Value: true},
		},
	})
	if err != nil || !policy.Enabled || policy.Version != 2 {
		t.Fatalf("update join policy: policy=%+v error=%v", policy, err)
	}
	studentIDRule, found, err := store.GetStudentIDRule(t.Context())
	if err != nil || !found || studentIDRule.Enabled || studentIDRule.Version != 1 || len(studentIDRule.Mappings) != 0 {
		t.Fatalf("get default student ID rule: rule=%+v found=%t error=%v", studentIDRule, found, err)
	}
	studentIDRule.Enabled = true
	studentIDRule.EnrollmentYearSegment = &joinrequests.StudentIDSegment{Offset: 2, Length: 4}
	studentIDRule.MajorCodeSegment = &joinrequests.StudentIDSegment{Offset: 6, Length: 3}
	studentIDRule.Mappings = []joinrequests.StudentMajorMapping{{
		EnrollmentYear: 2025, MajorCode: "315", MajorName: "Computer Science", Aliases: []string{"CS"},
	}}
	studentIDRule, err = store.UpdateStudentIDRule(t.Context(), joinrequests.StudentIDRuleMutation{
		Context: joinrequests.MutationContext{
			Actor: managerIntegrationAuditActor("usr_root"), Request: request, OccurredAt: now.Add(2750 * time.Millisecond),
		},
		ExpectedRevision: studentIDRule.Version, Rule: studentIDRule,
	})
	if err != nil || !studentIDRule.Enabled || studentIDRule.Version != 2 || studentIDRule.UpdatedBy == nil {
		t.Fatalf("update student ID rule: rule=%+v error=%v", studentIDRule, err)
	}
	if _, err := store.UpdateStudentIDRule(t.Context(), joinrequests.StudentIDRuleMutation{
		Context: joinrequests.MutationContext{
			Actor: managerIntegrationAuditActor("usr_root"), Request: request, OccurredAt: now.Add(2800 * time.Millisecond),
		},
		ExpectedRevision: 1, Rule: studentIDRule,
	}); !errors.Is(err, joinrequests.ErrConflict) {
		t.Fatalf("stale student ID rule update error=%v", err)
	}

	global, err := store.GetGlobalSettings(t.Context())
	if err != nil || global.Version != 1 || !global.Features.AIQA.Enabled {
		t.Fatalf("get global settings: value=%+v error=%v", global, err)
	}
	updatedGlobal, err := store.UpdateGlobalSettings(t.Context(), settings.UpdateGlobalMutation{
		Context:          settings.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(3 * time.Second)},
		ExpectedRevision: global.Version,
		Patch: settings.GlobalPatch{AIQA: auth.Field[settings.BasicPatch]{
			Set: true, Value: settings.BasicPatch{Enabled: auth.Field[bool]{Set: true, Value: false}},
		}},
	})
	if err != nil || updatedGlobal.Version != 2 || updatedGlobal.Features.AIQA.Enabled {
		t.Fatalf("update global settings: value=%+v error=%v", updatedGlobal, err)
	}
	if _, err := store.UpdateGlobalSettings(t.Context(), settings.UpdateGlobalMutation{
		Context:          settings.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(4 * time.Second)},
		ExpectedRevision: global.Version,
		Patch: settings.GlobalPatch{AIQA: auth.Field[settings.BasicPatch]{
			Set: true, Value: settings.BasicPatch{Enabled: auth.Field[bool]{Set: true, Value: true}},
		}},
	}); !errors.Is(err, settings.ErrConflict) {
		t.Fatalf("stale global update error=%v", err)
	}

	disabled := false
	groupSettings, err := store.UpdateGroupSettings(t.Context(), settings.UpdateGroupMutation{
		Context: settings.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(5 * time.Second)},
		GroupID: "10001", ExpectedRevision: 0,
		Patch: settings.GroupPatch{KeywordReply: settings.OverrideFeaturePatch{
			Set: true, Enabled: auth.Field[*bool]{Set: true, Value: &disabled},
		}},
	})
	if err != nil || groupSettings.Version != 1 || groupSettings.Effective.KeywordReply.Enabled || groupSettings.Effective.AIQA.Enabled {
		t.Fatalf("update group settings: value=%+v error=%v", groupSettings, err)
	}
	runtimeState, err := store.LoadRuntimeSettings(t.Context())
	if err != nil || runtimeState.Global.Version != 2 || len(runtimeState.Groups) != 1 || runtimeState.Groups[0].GroupID != "10001" {
		t.Fatalf("load runtime settings: state=%+v error=%v", runtimeState, err)
	}
	deleted, err := store.DeleteGroupSettings(t.Context(), settings.DeleteGroupMutation{
		Context: settings.MutationContext{Actor: principal, Request: request, OccurredAt: now.Add(6 * time.Second)},
		GroupID: "10001", ExpectedRevision: groupSettings.Version,
	})
	if err != nil || deleted.Version != 0 || deleted.Effective.KeywordReply.Enabled != updatedGlobal.Features.KeywordReply.Enabled {
		t.Fatalf("delete group settings: value=%+v error=%v", deleted, err)
	}

	page, err := store.ListGroups(t.Context(), groups.StoreListQuery{ListQuery: groups.ListQuery{Limit: 10}})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != "10001" {
		t.Fatalf("list groups: page=%+v error=%v", page, err)
	}
	data, err := store.LoadOverview(t.Context(), overview.StoreQuery{
		Range: overview.Range7Days, From: now.AddDate(0, 0, -7), To: now.AddDate(0, 0, 1),
		PreviousFrom: now.AddDate(0, 0, -14), Timezone: "UTC",
	})
	if err != nil || data.Metrics[overview.MetricActiveGroups].Value == nil ||
		*data.Metrics[overview.MetricActiveGroups].Value != 1 || data.Pending[overview.PendingJoinRequests] != 0 {
		t.Fatalf("load overview: data=%+v error=%v", data, err)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action IN ('groups.sync', 'join_policy.update', 'student_id_rule.update', 'settings.global.update', 'settings.group.update', 'settings.group.delete')", 7)
}

func managerIntegrationAuditActor(userID string) audit.Actor {
	return audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: "Root Admin"}
}
