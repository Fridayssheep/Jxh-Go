package storage

import (
	"context"
	"os"
	"testing"

	"github.com/zjutjh/jxh-go/internal/commands"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestAdminStoreScopesRolesAndManualGrantsByGroup(t *testing.T) {
	store := openAdminTestStore(t)
	ctx := context.Background()

	record, err := store.SyncAdminRole(ctx, 100, 2, commands.GroupRoleAdmin)
	if err != nil {
		t.Fatalf("SyncAdminRole returned error: %v", err)
	}
	if record.GroupID != 100 || record.UserID != 2 || record.QQRole != commands.GroupRoleAdmin {
		t.Fatalf("synced record = %+v", record)
	}
	if err := store.SetManualAdmin(ctx, 200, 2, true, commands.GroupRoleMember); err != nil {
		t.Fatalf("SetManualAdmin returned error: %v", err)
	}

	group100, err := store.ListAdmins(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	group200, err := store.ListAdmins(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(group100) != 1 || group100[0].QQRole != commands.GroupRoleAdmin || group100[0].ManualGranted {
		t.Fatalf("group 100 admins = %+v", group100)
	}
	if len(group200) != 1 || group200[0].QQRole != commands.GroupRoleMember || !group200[0].ManualGranted {
		t.Fatalf("group 200 admins = %+v", group200)
	}

	if _, err := store.SyncAdminRole(ctx, 100, 2, commands.GroupRoleMember); err != nil {
		t.Fatalf("downgrade SyncAdminRole returned error: %v", err)
	}
	group100, err = store.ListAdmins(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(group100) != 0 {
		t.Fatalf("downgraded native admin remains active: %+v", group100)
	}
	group200, err = store.ListAdmins(ctx, 200)
	if err != nil || len(group200) != 1 {
		t.Fatalf("other group changed after downgrade: %+v, %v", group200, err)
	}
}

func TestAdminStorePreservesManualGrantAndClearsOnlyOneGroup(t *testing.T) {
	store := openAdminTestStore(t)
	ctx := context.Background()

	if err := store.SetManualAdmin(ctx, 100, 1, true, commands.GroupRoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SyncAdminRole(ctx, 100, 1, commands.GroupRoleAdmin); err != nil {
		t.Fatal(err)
	}
	record, err := store.SyncAdminRole(ctx, 100, 1, commands.GroupRoleMember)
	if err != nil {
		t.Fatal(err)
	}
	if !record.ManualGranted || record.QQRole != commands.GroupRoleMember {
		t.Fatalf("manual grant not preserved after role changes: %+v", record)
	}
	if _, err := store.SyncAdminRole(ctx, 100, 2, commands.GroupRoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := store.SetManualAdmin(ctx, 200, 3, true, commands.GroupRoleMember); err != nil {
		t.Fatal(err)
	}

	if err := store.ClearManualAdmins(ctx, 100); err != nil {
		t.Fatalf("ClearManualAdmins returned error: %v", err)
	}
	group100, err := store.ListAdmins(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(group100) != 1 || group100[0].UserID != 2 || group100[0].QQRole != commands.GroupRoleOwner {
		t.Fatalf("group 100 admins after clear = %+v", group100)
	}
	group200, err := store.ListAdmins(ctx, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(group200) != 1 || group200[0].UserID != 3 || !group200[0].ManualGranted {
		t.Fatalf("group 200 admins after group 100 clear = %+v", group200)
	}
}

func openAdminTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("JXH_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("JXH_TEST_MYSQL_DSN is not configured")
	}
	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	if err := db.Exec("TRUNCATE TABLE admins").Error; err != nil {
		t.Fatalf("truncate admins: %v", err)
	}
	t.Cleanup(func() {
		if err := db.Exec("TRUNCATE TABLE admins").Error; err != nil {
			t.Errorf("cleanup admins: %v", err)
		}
	})
	return NewStore(db)
}
