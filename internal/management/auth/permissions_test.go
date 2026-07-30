package auth

import (
	"reflect"
	"testing"
)

func TestPermissionsMatchRoleMatrix(t *testing.T) {
	if !Allowed(RoleObserver, PermissionAuditRead) {
		t.Fatal("observer must read redacted audit")
	}
	if !Allowed(RoleObserver, PermissionAnalyticsExport) {
		t.Fatal("observer must export analytics")
	}
	if Allowed(RoleObserver, PermissionJoinRequestsDecide) {
		t.Fatal("observer must not decide join requests")
	}
	if Allowed(RoleMaintainer, PermissionUsersManage) {
		t.Fatal("maintainer must not manage users")
	}
	if !Allowed(RoleMaintainer, PermissionKnowledgeReload) {
		t.Fatal("maintainer must reload knowledge")
	}
	if !Allowed(RoleSuperAdmin, PermissionNapCatRestart) {
		t.Fatal("super admin must restart NapCat")
	}
	if !Allowed(RoleSuperAdmin, PermissionConfigWrite) || Allowed(RoleMaintainer, PermissionConfigWrite) || Allowed(RoleObserver, PermissionConfigWrite) {
		t.Fatal("only super admin may write the process configuration")
	}
	if !Allowed(RoleSuperAdmin, PermissionJoinPoliciesWrite) || Allowed(RoleMaintainer, PermissionJoinPoliciesWrite) || Allowed(RoleObserver, PermissionJoinPoliciesWrite) {
		t.Fatal("only super admin may write join request policies")
	}
}

func TestPermissionMatrixExactlyCoversOpenAPIEnums(t *testing.T) {
	want := []Permission{
		PermissionOverviewRead,
		PermissionGroupsRead,
		PermissionGroupsSync,
		PermissionSettingsRead,
		PermissionSettingsWrite,
		PermissionJoinRequestsRead,
		PermissionJoinRequestsDecide,
		PermissionJoinPoliciesWrite,
		PermissionCommandsRead,
		PermissionCommandsWrite,
		PermissionScheduledJobsRead,
		PermissionScheduledJobsWrite,
		PermissionKnowledgeRead,
		PermissionKnowledgeReload,
		PermissionAnalyticsRead,
		PermissionAnalyticsExport,
		PermissionAuditRead,
		PermissionUsersManage,
		PermissionSessionsManage,
		PermissionSystemRead,
		PermissionConfigWrite,
		PermissionNapCatRestart,
		PermissionEventsRead,
	}
	if got := AllPermissions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("AllPermissions() = %#v, want %#v", got, want)
	}
	if got := PermissionsFor(RoleSuperAdmin); !reflect.DeepEqual(got, want) {
		t.Fatalf("super admin permissions = %#v, want %#v", got, want)
	}
	if got := len(PermissionsFor(RoleMaintainer)); got != 18 {
		t.Fatalf("maintainer permission count = %d, want 18", got)
	}
	if got := len(PermissionsFor(RoleObserver)); got != 12 {
		t.Fatalf("observer permission count = %d, want 12", got)
	}
}

func TestRolePermissionParsingAndPrincipal(t *testing.T) {
	role, err := ParseRole("maintainer")
	if err != nil || role != RoleMaintainer {
		t.Fatalf("ParseRole() = %q, %v", role, err)
	}
	permission, err := ParsePermission("settings:write")
	if err != nil || permission != PermissionSettingsWrite {
		t.Fatalf("ParsePermission() = %q, %v", permission, err)
	}
	if _, err := ParseRole("admin"); err == nil {
		t.Fatal("ParseRole(admin) error = nil")
	}
	if _, err := ParsePermission("settings:delete"); err == nil {
		t.Fatal("ParsePermission(settings:delete) error = nil")
	}
	principal := Principal{UserID: "usr_1", SessionID: "ses_1", Role: RoleMaintainer}
	if !principal.Has(PermissionSettingsWrite) || principal.Has(PermissionUsersManage) {
		t.Fatalf("Principal permissions do not match role: %+v", principal)
	}
}

func TestPermissionSlicesAreImmutableCopies(t *testing.T) {
	permissions := PermissionsFor(RoleObserver)
	permissions[0] = PermissionUsersManage
	if Allowed(RoleObserver, PermissionUsersManage) {
		t.Fatal("mutating returned permissions changed the role matrix")
	}
	all := AllPermissions()
	all[0] = PermissionUsersManage
	if got := AllPermissions()[0]; got != PermissionOverviewRead {
		t.Fatalf("mutating AllPermissions result changed source: %q", got)
	}
}
