package commands

import (
	"context"
	"strings"
	"testing"
)

func TestPermissionMessageAllowsNativeGroupAdminAndScopesManualGrant(t *testing.T) {
	store := NewMemoryAdminStore()
	handler := NewAdminHandler(store)
	ctx := context.Background()

	if msg, err := handler.PermissionMessage(ctx, AdminInput{
		GroupID: 100, ActorID: 1, ActorRole: GroupRoleAdmin,
	}); err != nil || msg != "" {
		t.Fatalf("native admin permission = %q, %v", msg, err)
	}
	if err := store.SetManualAdmin(ctx, 100, 2, true, GroupRoleMember); err != nil {
		t.Fatalf("SetManualAdmin returned error: %v", err)
	}
	if msg, err := handler.PermissionMessage(ctx, AdminInput{
		GroupID: 100, ActorID: 2, ActorRole: GroupRoleMember,
	}); err != nil || msg != "" {
		t.Fatalf("manual admin permission = %q, %v", msg, err)
	}
	if msg, err := handler.PermissionMessage(ctx, AdminInput{
		GroupID: 200, ActorID: 2, ActorRole: GroupRoleMember,
	}); err != nil || msg == "" {
		t.Fatalf("other-group permission = %q, %v; want denial", msg, err)
	}
}

func TestPermissionMessageRefreshesRoleAndRetainsIndependentManualGrant(t *testing.T) {
	store := NewMemoryAdminStore()
	handler := NewAdminHandler(store)
	ctx := context.Background()

	if msg, err := handler.PermissionMessage(ctx, AdminInput{
		GroupID: 100, ActorID: 1, ActorRole: GroupRoleOwner,
	}); err != nil || msg != "" {
		t.Fatalf("owner permission = %q, %v", msg, err)
	}
	if msg, err := handler.PermissionMessage(ctx, AdminInput{
		GroupID: 100, ActorID: 1, ActorRole: GroupRoleMember,
	}); err != nil || msg == "" {
		t.Fatalf("downgraded owner permission = %q, %v; want denial", msg, err)
	}

	if err := store.SetManualAdmin(ctx, 100, 2, true, GroupRoleMember); err != nil {
		t.Fatalf("SetManualAdmin returned error: %v", err)
	}
	for _, role := range []string{GroupRoleAdmin, GroupRoleMember} {
		if msg, err := handler.PermissionMessage(ctx, AdminInput{
			GroupID: 100, ActorID: 2, ActorRole: role,
		}); err != nil || msg != "" {
			t.Fatalf("permission with role %q = %q, %v", role, msg, err)
		}
	}
}

func TestExecuteAuthorizedProtectsNativeRolesFromRemoval(t *testing.T) {
	store := NewMemoryAdminStore()
	handler := NewAdminHandler(store)
	ctx := context.Background()

	for _, test := range []struct {
		role string
		want string
	}{
		{role: GroupRoleOwner, want: "QQ群主"},
		{role: GroupRoleAdmin, want: "QQ群管理员"},
	} {
		response, err := handler.ExecuteAuthorized(ctx, AdminInput{
			GroupID: 100, Text: "移除管理员", AtUsers: []int64{2}, TargetRole: test.role,
		})
		if err != nil {
			t.Fatalf("ExecuteAuthorized(%q) returned error: %v", test.role, err)
		}
		if !strings.Contains(response, test.want) || !strings.Contains(response, "无法移除") {
			t.Fatalf("ExecuteAuthorized(%q) = %q", test.role, response)
		}
	}
}

func TestExecuteAuthorizedClearsOnlyCurrentGroupManualAdmins(t *testing.T) {
	store := NewMemoryAdminStore()
	handler := NewAdminHandler(store)
	ctx := context.Background()

	if err := store.SetManualAdmin(ctx, 100, 1, true, GroupRoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncAdminRole(ctx, 100, 2, GroupRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManualAdmin(ctx, 200, 3, true, GroupRoleMember); err != nil {
		t.Fatal(err)
	}

	response, err := handler.ExecuteAuthorized(ctx, AdminInput{GroupID: 100, Text: "移除所有管理员"})
	if err != nil || response != "已移除当前群所有手动授权管理员" {
		t.Fatalf("clear response = %q, err = %v", response, err)
	}

	group100, err := store.ListAdmins(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(group100) != 1 || group100[0].UserID != 2 || group100[0].QQRole != GroupRoleAdmin {
		t.Fatalf("group 100 admins = %+v", group100)
	}
	group200, err := store.ListAdmins(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(group200) != 1 || group200[0].UserID != 3 || !group200[0].ManualGranted {
		t.Fatalf("group 200 admins = %+v", group200)
	}
}

func TestExecuteAuthorizedListsCurrentGroupAdminsWithSources(t *testing.T) {
	store := NewMemoryAdminStore()
	handler := NewAdminHandler(store)
	ctx := context.Background()

	if _, err := store.SyncAdminRole(ctx, 100, 2, GroupRoleOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncAdminRole(ctx, 100, 3, GroupRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManualAdmin(ctx, 100, 4, true, GroupRoleMember); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManualAdmin(ctx, 200, 5, true, GroupRoleMember); err != nil {
		t.Fatal(err)
	}

	response, err := handler.ExecuteAuthorized(ctx, AdminInput{GroupID: 100, Text: "所有管理员"})
	if err != nil {
		t.Fatalf("ExecuteAuthorized returned error: %v", err)
	}
	for _, want := range []string{"2（QQ群主）", "3（QQ群管理员）", "4（手动授权）"} {
		if !strings.Contains(response, want) {
			t.Fatalf("admin list %q does not contain %q", response, want)
		}
	}
	if strings.Contains(response, "5") {
		t.Fatalf("admin list leaked another group: %q", response)
	}
}
