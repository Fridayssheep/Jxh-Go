# Group-Scoped Admin Permissions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make QQ owner/admin roles authoritative per group while keeping explicit bot-admin grants isolated by group.

**Architecture:** The router queries live member roles through the NapCat sender before each non-help admin command. `AdminHandler` synchronizes that role into a group-scoped store and falls back to an independent manual grant only for ordinary members. MySQL stores both signals under a `(group_id,user_id)` key.

**Tech Stack:** Go 1.25, NapCat SDK, GORM Gen, MySQL, Go testing.

---

### Task 1: Group-scoped permission domain and memory store

**Files:**
- Create: `internal/commands/admin_test.go`
- Modify: `internal/commands/admin.go`

- [x] **Step 1: Write failing domain tests**

Add tests that use the desired API:

```go
func TestPermissionMessageAllowsNativeGroupAdminAndScopesManualGrant(t *testing.T) {
    store := NewMemoryAdminStore()
    handler := NewAdminHandler(store)
    ctx := context.Background()

    if msg, err := handler.PermissionMessage(ctx, AdminInput{GroupID: 100, ActorID: 1, ActorRole: GroupRoleAdmin}); err != nil || msg != "" {
        t.Fatalf("native admin permission = %q, %v", msg, err)
    }
    if err := store.SetManualAdmin(ctx, 100, 2, true, GroupRoleMember); err != nil {
        t.Fatal(err)
    }
    if msg, _ := handler.PermissionMessage(ctx, AdminInput{GroupID: 200, ActorID: 2, ActorRole: GroupRoleMember}); msg == "" {
        t.Fatal("manual grant leaked across groups")
    }
}

func TestExecuteAuthorizedCannotRemoveNativeRole(t *testing.T) {
    handler := NewAdminHandler(NewMemoryAdminStore())
    response, err := handler.ExecuteAuthorized(context.Background(), AdminInput{
        GroupID: 100, Text: "移除管理员", AtUsers: []int64{2}, TargetRole: GroupRoleAdmin,
    })
    if err != nil || !strings.Contains(response, "QQ 群管理员") {
        t.Fatalf("response = %q, err = %v", response, err)
    }
}
```

Also cover role downgrade, retained independent manual grants, current-group list/clear behavior, and native-role labels.

- [x] **Step 2: Run tests and verify RED**

Run: `go test ./internal/commands -run 'Test(Permission|Execute|Clear|List)' -count=1`

Expected: compilation failure because `GroupID`, `ActorRole`, `TargetRole`, role constants, and group-scoped store methods do not exist.

- [x] **Step 3: Implement the domain API and memory store**

Introduce:

```go
const (
    GroupRoleOwner  = "owner"
    GroupRoleAdmin  = "admin"
    GroupRoleMember = "member"
)

type AdminRecord struct {
    GroupID       int64
    UserID        int64
    ManualGranted bool
    QQRole        string
}

type AdminStore interface {
    SyncAdminRole(context.Context, int64, int64, string) (AdminRecord, error)
    SetManualAdmin(context.Context, int64, int64, bool, string) error
    ClearManualAdmins(context.Context, int64) error
    ListAdmins(context.Context, int64) ([]AdminRecord, error)
    // existing blacklist methods remain unchanged
}
```

`PermissionMessage` must sync the live role first, allow `owner/admin`, and otherwise require `ManualGranted`. `ExecuteAuthorized` must scope all admin operations with `GroupID`, reject removal of native roles, and list the permission source.

- [x] **Step 4: Run domain tests and verify GREEN**

Run: `go test ./internal/commands -count=1`

Expected: PASS.

### Task 2: Live NapCat role lookup in the command router

**Files:**
- Modify: `internal/bot/command_router.go`
- Modify: `internal/bot/command_router_test.go`
- Modify: `internal/napcat/adapter.go`
- Modify: `internal/napcat/adapter_test.go`

- [x] **Step 1: Write failing router tests**

Extend the test sender with:

```go
func (s *recordingSender) GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error) {
    s.roleQueries = append(s.roleQueries, [2]int64{groupID, userID})
    if s.roleErr != nil { return "", s.roleErr }
    return s.roles[userID], nil
}
```

Test that every non-help `/admin` command queries the actor, QQ admins are allowed without manual grants, actor lookup failure sends `暂时无法确认群身份，请稍后重试`, add/remove queries the target only after actor authorization, and target lookup failure leaves the store unchanged.

- [x] **Step 2: Run router tests and verify RED**

Run: `go test ./internal/bot -run 'TestGroupCommandRouter.*(Admin|Role|Permission)' -count=1`

Expected: FAIL because the router does not use a group-role resolver.

- [x] **Step 3: Implement live role resolution**

Add this router boundary:

```go
type GroupMemberRoleResolver interface {
    GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error)
}
```

For non-empty `/admin`, require the sender to implement it, query the actor, normalize/validate the role, and pass `GroupID` plus `ActorRole` to `PermissionMessage`. For `添加管理员` and `移除管理员`, query the first non-bot mention after authorization and pass `TargetRole` to `ExecuteAuthorized`.

Implement the production resolver with NapCat `GetGroupMemberInfo` using `NoCache: true` and validate `owner/admin/member` before returning.

- [x] **Step 4: Run router and adapter tests and verify GREEN**

Run: `go test ./internal/bot ./internal/napcat -count=1`

Expected: PASS.

### Task 3: MySQL schema and persistent store

**Files:**
- Modify: `deploy/mysql/init/001_schema.sql`
- Modify: `internal/storage/model/admins.gen.go`
- Modify: `internal/storage/query/admins.gen.go`
- Modify: `internal/storage/models.go`
- Modify: `internal/storage/store.go`
- Create: `internal/storage/admin_store_test.go`

- [x] **Step 1: Write failing storage behavior tests**

Read `JXH_TEST_MYSQL_DSN`; skip with an explicit message when it is absent. Against the disposable test database, truncate `admins`, then cover composite-key filtering and current-group-only clear/list operations. The test must call:

```go
record, err := store.SyncAdminRole(ctx, 100, 2, commands.GroupRoleAdmin)
admins, err := store.ListAdmins(ctx, 100)
err = store.SetManualAdmin(ctx, 200, 2, true, commands.GroupRoleMember)
```

and prove group `100` and `200` remain independent.

- [x] **Step 2: Run the storage test and verify RED**

Run with a disposable MySQL initialized from `deploy/mysql/init/001_schema.sql`:

```powershell
docker run --rm -d --name jxh-admin-test-mysql -e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=jxh_admin_test -p 13316:3306 -v "${PWD}/deploy/mysql/init:/docker-entrypoint-initdb.d:ro" mysql:8.4
$env:JXH_TEST_MYSQL_DSN='root:root@tcp(127.0.0.1:13316)/jxh_admin_test?charset=utf8mb4&parseTime=True&loc=Local'
go test ./internal/storage -run TestAdminStore -count=1
```

Expected: compilation failure because the persistent store still exposes global administrator methods.

- [x] **Step 3: Replace the schema and generated admin model**

The new table is:

```sql
CREATE TABLE `admins` (
  `group_id` bigint NOT NULL,
  `user_id` bigint NOT NULL,
  `manual_granted` tinyint(1) NOT NULL DEFAULT 0,
  `qq_role` varchar(16) NOT NULL DEFAULT 'member',
  `created_at` datetime(3) DEFAULT NULL,
  `updated_at` datetime(3) DEFAULT NULL,
  PRIMARY KEY (`group_id`,`user_id`),
  KEY `idx_admins_user_id` (`user_id`)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
```

Update `internal/storage/model/admins.gen.go` and the field declarations/maps in `internal/storage/query/admins.gen.go` to the exact output implied by these six columns. The local `gentool` binary is not installed, so do not alter unrelated generated query files.

- [x] **Step 4: Implement persistent group-scoped operations**

Use a transaction for role synchronization. Preserve `manual_granted` when the live QQ role changes; delete a row only when its role is `member` and it has no manual grant. Clearing a group sets its manual flags to false and deletes inactive member rows without touching other groups.

- [x] **Step 5: Run storage and full unit tests**

Run: `go test ./internal/storage ./internal/commands ./internal/bot ./internal/napcat -count=1`

Expected: PASS.

### Task 4: Documentation and full verification

**Files:**
- Modify: `README.md`
- Modify: `docs/superpowers/plans/2026-07-14-group-scoped-admin-permissions.md`

- [x] **Step 1: Update operator documentation**

Document that QQ owners/admins are queried live, manual grants are per group, native roles cannot be removed, `移除所有管理员` clears only current-group manual grants, and development databases must recreate the `admins` table.

- [x] **Step 2: Run formatting and static checks**

Run:

```powershell
gofmt -w internal/commands/admin.go internal/commands/admin_test.go internal/bot/command_router.go internal/bot/command_router_test.go internal/napcat/adapter.go internal/napcat/adapter_test.go internal/storage/models.go internal/storage/store.go internal/storage/admin_store_test.go
go test ./...
go test -race ./...
go vet ./...
docker compose config --quiet
```

Expected: every command exits 0.

- [x] **Step 3: Inspect the final diff**

Run: `git status --short` and `git diff --check`.

Expected: only intended permission, schema, generated model/query, tests, docs, and the plan are changed; no generated runtime data is tracked.
