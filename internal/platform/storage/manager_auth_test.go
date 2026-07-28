package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/zjutjh/jxh-go/internal/management/audit"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/idempotency"
	"github.com/zjutjh/jxh-go/internal/platform/database"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var managerAuthTestSchemaID atomic.Uint64

func TestManagerAuthCursorBindsKindAndFilters(t *testing.T) {
	at := time.Date(2026, time.July, 28, 12, 0, 0, 987654321, time.FixedZone("test", 8*60*60))
	current := true
	query := auth.SessionListQuery{UserID: "usr_1", Status: auth.SessionStatusActive, Current: &current, CurrentSessionID: "ses_current"}
	fingerprint := managerSessionFilterFingerprint(query)
	cursor, err := encodeManagerAuthCursor("sessions", at, "ses_2", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeManagerAuthCursor(cursor, "sessions", fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != "ses_2" || !decoded.time().Equal(normalizedManagerTime(at)) {
		t.Fatalf("decoded cursor = %+v", decoded)
	}

	changed := query
	changed.CurrentSessionID = "ses_other"
	if _, err := decodeManagerAuthCursor(cursor, "sessions", managerSessionFilterFingerprint(changed)); err == nil {
		t.Fatal("cursor was accepted with a different current session")
	}
	if _, err := decodeManagerAuthCursor(cursor, "users", fingerprint); err == nil {
		t.Fatal("cursor was accepted for a different resource type")
	}
	if columns := strings.Join(managerSessionPublicColumns(), ","); strings.Contains(columns, "token_digest") || strings.Contains(columns, "csrf_digest") {
		t.Fatalf("public session columns expose a digest: %s", columns)
	}
}

func TestManagerFailureRedactsDatabaseDetails(t *testing.T) {
	secret := "password_hash=$argon2id$sensitive"
	raw := &drivermysql.MySQLError{Number: 1064, Message: secret}
	err := managerFailure("load admin", raw)
	if !errors.Is(err, errManagerStorage) {
		t.Fatalf("error = %v, want errManagerStorage", err)
	}
	if strings.Contains(err.Error(), secret) || errors.Is(err, raw) {
		t.Fatalf("database error was exposed: %v", err)
	}

	canceled := managerFailure("load admin", context.Canceled)
	if !errors.Is(canceled, context.Canceled) {
		t.Fatalf("context cancellation was hidden: %v", canceled)
	}
}

func TestManagerAuthMySQLConcurrentIdempotency(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 4, 5, 6, 123456789, time.UTC)
	requested := idempotency.Reservation{
		ActorType: idempotency.ActorAdminUser, ActorID: "usr_root", Operation: "test.concurrent",
		Key: "idem-concurrent-1", RequestHash: strings.Repeat("a", 64), State: idempotency.StateInProgress,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}

	const callers = 12
	type outcome struct {
		reservation idempotency.Reservation
		err         error
	}
	start := make(chan struct{})
	outcomes := make(chan outcome, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			reservation, err := store.ReserveIdempotency(t.Context(), requested)
			outcomes <- outcome{reservation: reservation, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(outcomes)

	var id uint64
	fresh := 0
	for result := range outcomes {
		if result.err != nil {
			t.Fatalf("reserve idempotency key: %v", result.err)
		}
		if result.reservation.Fresh {
			fresh++
		}
		if id == 0 {
			id = result.reservation.ID
		} else if result.reservation.ID != id {
			t.Fatalf("reservation IDs differ: %d and %d", id, result.reservation.ID)
		}
	}
	if fresh != 1 {
		t.Fatalf("fresh reservations = %d, want 1", fresh)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_idempotency_keys", 1)

	different := requested
	different.RequestHash = strings.Repeat("b", 64)
	if _, err := store.ReserveIdempotency(t.Context(), different); !errors.Is(err, idempotency.ErrKeyReused) {
		t.Fatalf("different request hash error = %v, want ErrKeyReused", err)
	}

	completedAt := now.Add(time.Minute)
	completion := idempotency.Completion{RequestHash: requested.RequestHash, CompletedAt: completedAt, Result: idempotency.Result{
		Status: idempotency.ResultSucceeded, ResponseStatus: 201,
		Resource: &idempotency.Resource{Type: "admin_user", ID: "usr_1"}, CompletedAt: completedAt,
	}}
	completed, err := store.CompleteIdempotency(t.Context(), id, completion)
	if err != nil {
		t.Fatal(err)
	}
	second := completion
	second.Result.ResponseStatus = 202
	replayed, err := store.CompleteIdempotency(t.Context(), id, second)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Result == nil || replayed.Result == nil || completed.Result.ResponseStatus != 201 || replayed.Result.ResponseStatus != 201 {
		t.Fatalf("completion replay changed result: first=%+v second=%+v", completed.Result, replayed.Result)
	}
}

func TestManagerAuthMySQLProtectsLastSuperAdminConcurrently(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 5, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_1", "root-one", auth.RoleSuperAdmin, now)
	insertManagerAuthTestUser(t, sqlDB, "usr_2", "root-two", auth.RoleSuperAdmin, now)

	type outcome struct{ err error }
	start := make(chan struct{})
	outcomes := make(chan outcome, 2)
	var group sync.WaitGroup
	for index, userID := range []string{"usr_1", "usr_2"} {
		group.Add(1)
		go func(index int, userID string) {
			defer group.Done()
			<-start
			_, err := store.UpdateAdminUser(t.Context(), auth.UpdateUserMutation{
				Actor:   auth.Principal{UserID: "usr_1", SessionID: "ses_actor", Role: auth.RoleSuperAdmin},
				Context: auth.MutationContext{RequestID: fmt.Sprintf("req_%d", index)}, UserID: userID, ExpectedRevision: 1,
				Patch: auth.UserPatch{Enabled: auth.Field[bool]{Set: true, Value: false}}, OccurredAt: now.Add(time.Duration(index) * time.Millisecond),
			})
			outcomes <- outcome{err: err}
		}(index, userID)
	}
	close(start)
	group.Wait()
	close(outcomes)

	succeeded, protected := 0, 0
	for result := range outcomes {
		switch {
		case result.err == nil:
			succeeded++
		case errors.Is(result.err, auth.ErrLastSuperAdmin):
			protected++
		default:
			t.Fatalf("unexpected update error: %v", result.err)
		}
	}
	if succeeded != 1 || protected != 1 {
		t.Fatalf("outcomes: succeeded=%d protected=%d", succeeded, protected)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_users WHERE role = 'super_admin' AND enabled = TRUE", 1)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'user.update'", 1)
}

func TestManagerAuthMySQLPasswordResetReplayIsTransactionalAndRedacted(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 6, 0, 0, 987654321, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, now)
	insertManagerAuthTestUser(t, sqlDB, "usr_target", "target-admin", auth.RoleMaintainer, now)
	firstDigest := insertManagerAuthTestSession(t, sqlDB, "ses_1", "usr_target", 0, now)
	secondDigest := insertManagerAuthTestSession(t, sqlDB, "ses_2", "usr_target", 0, now)

	mutation := auth.ResetPasswordMutation{
		Actor:   auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin},
		Context: auth.MutationContext{RequestID: "req_reset", IPAddress: "192.0.2.1", UserAgent: "manager-test"},
		UserID:  "usr_target", ExpectedRevision: 1, PasswordHash: "$argon2id$first-sensitive-hash",
		RequestHash: strings.Repeat("c", 64), IdempotencyKey: "idem-reset-1", OccurredAt: now,
	}
	first, err := store.ResetAdminUserPassword(t.Context(), mutation)
	if err != nil {
		t.Fatal(err)
	}
	replayMutation := mutation
	replayMutation.PasswordHash = "$argon2id$different-randomized-hash"
	replayed, err := store.ResetAdminUserPassword(t.Context(), replayMutation)
	if err != nil {
		t.Fatal(err)
	}
	if first.User != replayed.User || first.RevokedSessionCount != 2 || replayed.RevokedSessionCount != 2 || !first.CompletedAt.Equal(replayed.CompletedAt) {
		t.Fatalf("password reset replay differs: first=%+v replay=%+v", first, replayed)
	}

	var storedHash string
	var revision uint64
	if err := sqlDB.QueryRowContext(t.Context(), "SELECT password_hash, revision FROM admin_users WHERE user_id = 'usr_target'").Scan(&storedHash, &revision); err != nil {
		t.Fatal(err)
	}
	if storedHash != mutation.PasswordHash || revision != 2 {
		t.Fatalf("stored password state = %q revision %d", storedHash, revision)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_sessions WHERE user_id = 'usr_target' AND status = 'revoked'", 2)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'user.password_reset'", 1)

	var before, after, metadata []byte
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT before_snapshot, after_snapshot, metadata
FROM admin_audit_logs WHERE action = 'user.password_reset'`).Scan(&before, &after, &metadata); err != nil {
		t.Fatal(err)
	}
	auditJSON := string(before) + string(after) + string(metadata)
	for _, secret := range []string{mutation.PasswordHash, replayMutation.PasswordHash, firstDigest, secondDigest} {
		if strings.Contains(auditJSON, secret) {
			t.Fatalf("audit JSON contains sensitive value %q", secret)
		}
	}

	reused := mutation
	reused.RequestHash = strings.Repeat("d", 64)
	if _, err := store.ResetAdminUserPassword(t.Context(), reused); !errors.Is(err, auth.ErrAdminIdempotencyReuse) {
		t.Fatalf("reused reset key error = %v, want ErrAdminIdempotencyReuse", err)
	}
}

func TestManagerAuthMySQLAuditFailureRollsBackMutation(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	if _, err := sqlDB.ExecContext(t.Context(), "DROP TABLE admin_audit_logs"); err != nil {
		t.Fatal(err)
	}
	_, err := store.CreateAdminUser(t.Context(), auth.CreateUserMutation{
		Actor:   auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin},
		Context: auth.MutationContext{RequestID: "req_create"}, Username: "rolled-back", DisplayName: "Rolled Back",
		Role: auth.RoleObserver, PasswordHash: "$argon2id$not-persisted", OccurredAt: time.Now(),
	})
	if !errors.Is(err, errManagerStorage) || strings.Contains(err.Error(), "admin_audit_logs") {
		t.Fatalf("create error = %v", err)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_users WHERE username = 'rolled-back'", 0)
}

func TestManagerAuthMySQLStablePaginationAndCurrentSession(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 7, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, now)
	for _, suffix := range []string{"1", "2", "3", "4"} {
		insertManagerAuthTestUser(t, sqlDB, "usr_"+suffix, "observer-"+suffix, auth.RoleObserver, now)
	}
	for _, suffix := range []string{"a", "b", "c", "d"} {
		insertManagerAuthTestSession(t, sqlDB, "ses_"+suffix, "usr_root", 0, now)
	}
	for _, suffix := range []string{"1", "2", "3", "4"} {
		if _, err := sqlDB.ExecContext(t.Context(), `INSERT INTO admin_audit_logs
(audit_log_id, occurred_at, actor_type, action, target_type, target_id, result, request_id, source, metadata, redacted)
VALUES (?, ?, 'system', 'test.action', 'admin_user', ?, 'success', ?, 'system', JSON_OBJECT(), TRUE)`,
			"aud_"+suffix, now, "usr_"+suffix, "req_"+suffix); err != nil {
			t.Fatal(err)
		}
	}

	users := collectManagerAuthUsers(t, store, auth.UserListQuery{Role: auth.RoleObserver, Limit: 2})
	if got := strings.Join(users, ","); got != "usr_4,usr_3,usr_2,usr_1" {
		t.Fatalf("user pagination order = %s", got)
	}

	query := auth.SessionListQuery{CurrentSessionID: "ses_c", AsOf: now, Limit: 2}
	first, err := store.ListAdminSessions(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "ses_d" || first.Items[1].ID != "ses_c" || !first.Items[1].Current || !first.HasMore {
		t.Fatalf("first session page = %+v", first)
	}
	query.Cursor = first.NextCursor
	second, err := store.ListAdminSessions(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.Items[0].ID != "ses_b" || second.Items[1].ID != "ses_a" || second.HasMore {
		t.Fatalf("second session page = %+v", second)
	}
	changed := query
	changed.CurrentSessionID = "ses_b"
	if _, err := store.ListAdminSessions(t.Context(), changed); !errors.Is(err, auth.ErrInvalidAdminInput) {
		t.Fatalf("session cursor with changed caller error = %v", err)
	}
	current := true
	currentPage, err := store.ListAdminSessions(t.Context(), auth.SessionListQuery{Current: &current, CurrentSessionID: "ses_c", AsOf: now, Limit: 10})
	if err != nil || len(currentPage.Items) != 1 || currentPage.Items[0].ID != "ses_c" || !currentPage.Items[0].Current {
		t.Fatalf("current session page = %+v, error = %v", currentPage, err)
	}
	current = false
	otherPage, err := store.ListAdminSessions(t.Context(), auth.SessionListQuery{Current: &current, CurrentSessionID: "ses_c", AsOf: now, Limit: 10})
	if err != nil || len(otherPage.Items) != 3 {
		t.Fatalf("non-current session page = %+v, error = %v", otherPage, err)
	}

	auditIDs := collectManagerAuthAudit(t, store, audit.ListQuery{Actions: []string{"test.action"}, Limit: 2})
	if got := strings.Join(auditIDs, ","); got != "aud_4,aud_3,aud_2,aud_1" {
		t.Fatalf("audit pagination order = %s", got)
	}
}

func TestManagerAuthMySQLUserPaginationSurvivesUpdatesAndMatchesExactQQ(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	createdAt := time.Date(2026, time.July, 28, 7, 30, 0, 0, time.UTC)
	for _, suffix := range []string{"1", "2", "3", "4"} {
		insertManagerAuthTestUser(t, sqlDB, "usr_"+suffix, "observer-"+suffix, auth.RoleObserver, createdAt)
	}
	if _, err := sqlDB.ExecContext(t.Context(), "UPDATE admin_users SET qq_user_id = ? WHERE user_id = ?", "123456789", "usr_1"); err != nil {
		t.Fatal(err)
	}

	query := auth.UserListQuery{Role: auth.RoleObserver, Limit: 2}
	first, err := store.ListAdminUsers(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 2 || first.Items[0].ID != "usr_4" || first.Items[1].ID != "usr_3" || !first.HasMore {
		t.Fatalf("first user page = %+v", first)
	}
	if _, err := sqlDB.ExecContext(t.Context(), "UPDATE admin_users SET updated_at = ? WHERE user_id = ?", createdAt.Add(24*time.Hour), "usr_2"); err != nil {
		t.Fatal(err)
	}
	query.Cursor = first.NextCursor
	second, err := store.ListAdminUsers(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 2 || second.Items[0].ID != "usr_2" || second.Items[1].ID != "usr_1" || second.HasMore {
		t.Fatalf("second user page after update = %+v", second)
	}

	matched, err := store.ListAdminUsers(t.Context(), auth.UserListQuery{Query: "123456789", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(matched.Items) != 1 || matched.Items[0].ID != "usr_1" {
		t.Fatalf("exact QQ user match = %+v", matched)
	}
}

func TestManagerAuthMySQLMaterializesAndFiltersExpiredSessions(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	asOf := time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, asOf.Add(-time.Hour))
	for _, sessionID := range []string{"ses_active", "ses_idle_expired", "ses_absolute_expired", "ses_revoked"} {
		insertManagerAuthTestSession(t, sqlDB, sessionID, "usr_root", 0, asOf.Add(-time.Hour))
	}
	if _, err := sqlDB.ExecContext(t.Context(), `UPDATE admin_sessions SET expires_at = ?, absolute_expires_at = ? WHERE session_id = ?`,
		asOf.Add(time.Hour), asOf.Add(2*time.Hour), "ses_active"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(t.Context(), `UPDATE admin_sessions SET expires_at = ?, absolute_expires_at = ? WHERE session_id = ?`,
		asOf.Add(-time.Millisecond), asOf.Add(time.Hour), "ses_idle_expired"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(t.Context(), `UPDATE admin_sessions SET expires_at = ?, absolute_expires_at = ? WHERE session_id = ?`,
		asOf.Add(time.Hour), asOf.Add(-time.Millisecond), "ses_absolute_expired"); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.ExecContext(t.Context(), `UPDATE admin_sessions SET status = 'revoked', revoked_at = ? WHERE session_id = ?`,
		asOf.Add(-time.Minute), "ses_revoked"); err != nil {
		t.Fatal(err)
	}

	expired, err := store.ListAdminSessions(t.Context(), auth.SessionListQuery{Status: auth.SessionStatusExpired, AsOf: asOf, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(expired.Items) != 2 || expired.Items[0].Status != auth.SessionStatusExpired || expired.Items[1].Status != auth.SessionStatusExpired {
		t.Fatalf("expired session page = %+v", expired)
	}
	active, err := store.ListAdminSessions(t.Context(), auth.SessionListQuery{Status: auth.SessionStatusActive, AsOf: asOf, Limit: 10})
	if err != nil || len(active.Items) != 1 || active.Items[0].ID != "ses_active" {
		t.Fatalf("active session page = %+v, error = %v", active, err)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_sessions WHERE status = 'expired'", 2)
}

func TestManagerAuthMySQLLoginReturnsCommittedUserState(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	createdAt := time.Date(2026, time.July, 28, 8, 30, 0, 0, time.UTC)
	loginAt := createdAt.Add(time.Hour)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, createdAt)
	token := managerAuthTestTokenDigest("upgraded-login-token")
	csrf := managerAuthTestTokenDigest("upgraded-login-csrf")

	identity, err := store.CommitLogin(t.Context(), auth.LoginCommit{
		Session: auth.Session{
			ID: "ses_upgraded", UserID: "usr_root", Status: auth.SessionStatusActive,
			CreatedAt: loginAt, LastSeenAt: loginAt, ExpiresAt: loginAt.Add(time.Hour), AbsoluteExpiresAt: loginAt.Add(12 * time.Hour),
		},
		TokenDigest: token, CSRFDigest: csrf,
		PasswordHashUpdate: &auth.PasswordHashUpdate{
			UserID: "usr_root", ExpectedVersion: 1, PasswordHash: "$argon2id$upgraded-test-value",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if identity.User.Version != 2 || identity.User.LastLoginAt == nil || !identity.User.LastLoginAt.Equal(loginAt) ||
		!identity.User.UpdatedAt.Equal(loginAt) || identity.Session.ID != "ses_upgraded" || identity.CSRFDigest != csrf {
		t.Fatalf("committed login identity has version=%d last_login=%v updated=%v session=%s",
			identity.User.Version, identity.User.LastLoginAt, identity.User.UpdatedAt, identity.Session.ID)
	}
	var storedHash string
	var revision uint64
	var storedLastLogin, storedUpdated time.Time
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT password_hash, revision, last_login_at, updated_at
FROM admin_users WHERE user_id = ?`, "usr_root").Scan(&storedHash, &revision, &storedLastLogin, &storedUpdated); err != nil {
		t.Fatal(err)
	}
	if storedHash != "$argon2id$upgraded-test-value" || revision != identity.User.Version ||
		!storedLastLogin.Equal(*identity.User.LastLoginAt) || !storedUpdated.Equal(identity.User.UpdatedAt) {
		t.Fatalf("stored login state differs: revision=%d last_login=%v updated=%v", revision, storedLastLogin, storedUpdated)
	}
}

func TestManagerAuthMySQLSessionReplacementChains(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	now := time.Date(2026, time.July, 28, 8, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, now)
	oldToken := managerAuthTestTokenDigest("old-token")
	insertManagerAuthTestSessionWithDigest(t, sqlDB, "ses_old", "usr_root", 0, now, oldToken)

	loginToken := managerAuthTestTokenDigest("login-token")
	loginCSRF := managerAuthTestTokenDigest("login-csrf")
	loginIdentity, err := store.CommitLogin(t.Context(), auth.LoginCommit{
		Session: auth.Session{ID: "ses_login", UserID: "usr_root", Status: auth.SessionStatusActive,
			CreatedAt: now.Add(time.Minute), LastSeenAt: now.Add(time.Minute), ExpiresAt: now.Add(time.Hour), AbsoluteExpiresAt: now.Add(12 * time.Hour)},
		TokenDigest: loginToken, CSRFDigest: loginCSRF, PriorTokenDigest: &oldToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if loginIdentity.User.Version != 1 || loginIdentity.User.LastLoginAt == nil ||
		!loginIdentity.User.LastLoginAt.Equal(now.Add(time.Minute)) || loginIdentity.Session.ID != "ses_login" {
		t.Fatalf("committed login identity = %+v", loginIdentity)
	}
	assertManagerAuthReplacement(t, sqlDB, "ses_old", "ses_login", 1)

	passwordToken := managerAuthTestTokenDigest("password-token")
	passwordCSRF := managerAuthTestTokenDigest("password-csrf")
	commit := auth.PasswordChangeCommit{
		Actor:   auth.Principal{UserID: "usr_root", SessionID: "ses_login", Role: auth.RoleSuperAdmin},
		Context: auth.MutationContext{RequestID: "req_password"}, IdempotencyKey: "idem-password-1",
		RequestHash: strings.Repeat("e", 64), ExpectedUserVersion: 1, PasswordHash: "$argon2id$new-password-hash",
		PriorSessionID: "ses_login", PriorTokenDigest: loginToken,
		NewSession: auth.Session{ID: "ses_password", UserID: "usr_root", Status: auth.SessionStatusActive,
			CreatedAt: now.Add(2 * time.Minute), LastSeenAt: now.Add(2 * time.Minute), ExpiresAt: now.Add(2 * time.Hour), AbsoluteExpiresAt: now.Add(12 * time.Hour)},
		NewTokenDigest: passwordToken, NewCSRFDigest: passwordCSRF, OccurredAt: now.Add(2 * time.Minute),
	}
	identity, err := store.CommitPasswordChange(t.Context(), commit)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.CommitPasswordChange(t.Context(), commit)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Session.ID != "ses_password" || replayed.Session.ID != identity.Session.ID || identity.User.Version != 2 || replayed.User.Version != 2 {
		t.Fatalf("password change identities: first=%+v replay=%+v", identity, replayed)
	}
	assertManagerAuthReplacement(t, sqlDB, "ses_login", "ses_password", 2)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_sessions WHERE user_id = 'usr_root' AND status = 'active'", 1)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'auth.password_change'", 1)

	replacement, found, err := store.LookupPasswordChange(t.Context(), auth.PasswordChangeLookup{
		UserID: "usr_root", SessionID: "ses_login", IdempotencyKey: commit.IdempotencyKey, RequestHash: commit.RequestHash,
		At: commit.OccurredAt.Add(time.Minute),
	})
	if err != nil || !found || replacement.Session.ID != "ses_password" {
		t.Fatalf("password change lookup = %+v, found=%t error=%v", replacement, found, err)
	}
	if _, _, err := store.LookupPasswordChange(t.Context(), auth.PasswordChangeLookup{
		UserID: "usr_root", SessionID: "ses_login", IdempotencyKey: commit.IdempotencyKey,
		RequestHash: strings.Repeat("f", 64), At: commit.OccurredAt.Add(time.Minute),
	}); !errors.Is(err, auth.ErrAdminIdempotencyReuse) {
		t.Fatalf("unexpired conflicting password change lookup error = %v", err)
	}
	if _, err := sqlDB.ExecContext(t.Context(), `UPDATE admin_idempotency_keys SET expires_at = ?
WHERE actor_id = ? AND operation = ? AND idempotency_key = ?`,
		commit.OccurredAt, "usr_root", operationPasswordChange, commit.IdempotencyKey); err != nil {
		t.Fatal(err)
	}
	for _, requestHash := range []string{commit.RequestHash, strings.Repeat("f", 64)} {
		if expired, found, err := store.LookupPasswordChange(t.Context(), auth.PasswordChangeLookup{
			UserID: "usr_root", SessionID: "ses_login", IdempotencyKey: commit.IdempotencyKey,
			RequestHash: requestHash, At: commit.OccurredAt.Add(time.Minute),
		}); err != nil || found || expired.Session.ID != "" {
			t.Fatalf("expired password change lookup = %+v, found=%t error=%v", expired, found, err)
		}
	}
	rotated, found, err := store.LookupReplacedSession(t.Context(), oldToken)
	if err != nil || !found || rotated.Session.ID != "ses_old" || rotated.Session.Status != auth.SessionStatusRevoked {
		t.Fatalf("rotated session lookup = %+v, found=%t error=%v", rotated, found, err)
	}
}

func collectManagerAuthUsers(t *testing.T, store *Store, query auth.UserListQuery) []string {
	t.Helper()
	var ids []string
	for {
		page, err := store.ListAdminUsers(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		if !page.HasMore {
			return ids
		}
		if page.NextCursor == "" {
			t.Fatal("user page has more rows without a cursor")
		}
		query.Cursor = page.NextCursor
	}
}

func collectManagerAuthAudit(t *testing.T, store *Store, query audit.ListQuery) []string {
	t.Helper()
	var ids []string
	for {
		page, err := store.ListAuditLogs(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range page.Items {
			ids = append(ids, item.ID)
		}
		if !page.HasMore {
			return ids
		}
		if page.NextCursor == "" {
			t.Fatal("audit page has more rows without a cursor")
		}
		query.Cursor = page.NextCursor
	}
}

func openManagerAuthTestStore(t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	rawDSN := os.Getenv("JXH_MYSQL_INTEGRATION_DSN")
	if rawDSN == "" {
		t.Skip("JXH_MYSQL_INTEGRATION_DSN is not set")
	}
	parsed, err := drivermysql.ParseDSN(rawDSN)
	if err != nil {
		t.Fatal("parse MySQL integration DSN: invalid DSN")
	}
	adminConfig := parsed.Clone()
	adminConfig.DBName = ""
	adminConfig.MultiStatements = true
	adminDB, err := sql.Open("mysql", adminConfig.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if err := adminDB.PingContext(t.Context()); err != nil {
		_ = adminDB.Close()
		t.Fatalf("ping MySQL integration server: %v", err)
	}

	schema := fmt.Sprintf("jxh_manager_auth_test_%d_%d", time.Now().UnixNano(), managerAuthTestSchemaID.Add(1))
	if _, err := adminDB.ExecContext(t.Context(), "CREATE DATABASE `"+schema+"`"); err != nil {
		_ = adminDB.Close()
		t.Fatalf("create MySQL integration schema: %v", err)
	}
	databaseConfig := parsed.Clone()
	databaseConfig.DBName = schema
	databaseConfig.MultiStatements = true
	databaseConfig.ParseTime = true
	databaseConfig.Loc = time.UTC
	databaseConfig.ClientFoundRows = true
	sqlDB, err := sql.Open("mysql", databaseConfig.FormatDSN())
	if err != nil {
		_, _ = adminDB.ExecContext(context.Background(), "DROP DATABASE `"+schema+"`")
		_ = adminDB.Close()
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(32)
	t.Cleanup(func() {
		_ = sqlDB.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := adminDB.ExecContext(ctx, "DROP DATABASE `"+schema+"`"); err != nil {
			t.Errorf("drop MySQL integration schema: %v", err)
		}
		_ = adminDB.Close()
	})

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate manager auth test source")
	}
	migrations, err := database.LoadMigrations(filepath.Join(filepath.Dir(filename), "..", "..", "..", "deploy", "mysql", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (database.Runner{DB: sqlDB, LockTimeout: 5 * time.Second}).Apply(t.Context(), migrations); err != nil {
		t.Fatalf("apply MySQL migrations: %v", err)
	}
	gormDB, err := gorm.Open(gormmysql.New(gormmysql.Config{Conn: sqlDB, SkipInitializeWithVersion: true}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatalf("open GORM database: %v", err)
	}
	return NewStore(gormDB), sqlDB
}

func insertManagerAuthTestUser(t *testing.T, db *sql.DB, id, username string, role auth.Role, at time.Time) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), `INSERT INTO admin_users
(user_id, username, display_name, password_hash, role, enabled, revision, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, TRUE, 1, ?, ?)`, id, username, username, "$argon2id$test-hash", role, at, at)
	if err != nil {
		t.Fatal(err)
	}
}

func insertManagerAuthTestSession(t *testing.T, db *sql.DB, id, userID string, depth uint64, at time.Time) string {
	t.Helper()
	token := managerAuthTestTokenDigest("token:" + id)
	insertManagerAuthTestSessionWithDigest(t, db, id, userID, depth, at, token)
	return digestString(token)
}

func insertManagerAuthTestSessionWithDigest(t *testing.T, db *sql.DB, id, userID string, depth uint64, at time.Time, token auth.TokenDigest) {
	t.Helper()
	csrf := managerAuthTestTokenDigest("csrf:" + id)
	_, err := db.ExecContext(t.Context(), `INSERT INTO admin_sessions
(session_id, user_id, token_digest, csrf_digest, status, created_at, last_seen_at, expires_at, absolute_expires_at, replacement_depth)
VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)`,
		id, userID, digestString(token), digestString(csrf), at, at, at.Add(time.Hour), at.Add(12*time.Hour), depth)
	if err != nil {
		t.Fatal(err)
	}
}

func managerAuthTestTokenDigest(value string) auth.TokenDigest {
	return auth.TokenDigest(sha256.Sum256([]byte(value)))
}

func assertManagerAuthReplacement(t *testing.T, db *sql.DB, prior, replacement string, depth uint64) {
	t.Helper()
	var status auth.SessionStatus
	var replacementID string
	var replacementDepth uint64
	if err := db.QueryRowContext(t.Context(), `SELECT status, replaced_by_session_id, replaced_by_depth
FROM admin_sessions WHERE session_id = ?`, prior).Scan(&status, &replacementID, &replacementDepth); err != nil {
		t.Fatal(err)
	}
	if status != auth.SessionStatusRevoked || replacementID != replacement || replacementDepth != depth {
		t.Fatalf("session %s replacement = status %s, ID %s, depth %d", prior, status, replacementID, replacementDepth)
	}
}

func assertManagerAuthCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(t.Context(), query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("count for %q = %d, want %d", query, count, want)
	}
}
