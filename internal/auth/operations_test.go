package auth

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestChangePasswordRotatesCredentialAndReplaysDeterministically(t *testing.T) {
	service, store, identity, credential := newPasswordChangeFixture(t)
	input := ChangePasswordInput{
		Credential: credential, CurrentPassword: "current-password", NewPassword: "new-password-123",
		IdempotencyKey: "idem-change-1", Context: MutationContext{RequestID: "req_1", IPAddress: "192.0.2.1", UserAgent: "test"},
	}
	first, err := service.ChangePassword(t.Context(), identity, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.SessionToken == credential || first.SessionToken == "" || first.CSRFToken == identity.CSRFToken || first.Session.ID == identity.Session.ID {
		t.Fatalf("credential was not rotated: old=%+v new=%+v", identity, first.AuthContext)
	}
	if len(store.passwordCommits) != 1 || store.passwordCommits[0].PasswordHash != "new-hash" {
		t.Fatalf("commits=%+v", store.passwordCommits)
	}

	recovered, err := service.AuthenticateForRotation(t.Context(), credential)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.ChangePassword(t.Context(), recovered, input)
	if err != nil {
		t.Fatal(err)
	}
	if replay.SessionToken != first.SessionToken || replay.CSRFToken != first.CSRFToken || len(store.passwordCommits) != 1 {
		t.Fatalf("replay=%+v first=%+v commits=%d", replay, first, len(store.passwordCommits))
	}
}

func TestChangePasswordRejectsWrongCurrentPasswordBeforeCommit(t *testing.T) {
	service, store, identity, credential := newPasswordChangeFixture(t)
	service.passwords.(*spyPasswordEngine).setVerifyResult(false)
	_, err := service.ChangePassword(t.Context(), identity, ChangePasswordInput{
		Credential: credential, CurrentPassword: "wrong-password", NewPassword: "new-password-123",
		IdempotencyKey: "idem-change-1", Context: MutationContext{IPAddress: "192.0.2.1"},
	})
	if !errors.Is(err, ErrInvalidCredentials) || len(store.passwordCommits) != 0 {
		t.Fatalf("error=%v commits=%d", err, len(store.passwordCommits))
	}
}

func TestLogoutPersistsOnlyDigestAndIsIdempotent(t *testing.T) {
	service, store, identity, credential := newPasswordChangeFixture(t)
	if err := service.Logout(t.Context(), credential, identity, MutationContext{RequestID: "req_1"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Logout(t.Context(), credential, identity, MutationContext{RequestID: "req_2"}); err != nil {
		t.Fatal(err)
	}
	if len(store.revocations) != 2 || store.revocations[0].TokenDigest == (TokenDigest{}) {
		t.Fatalf("revocations=%+v", store.revocations)
	}
}

func newPasswordChangeFixture(t *testing.T) (*Service, *fakeAuthStore, AuthContext, string) {
	t.Helper()
	store := newFakeAuthStore()
	passwords := &spyPasswordEngine{verifyOK: true, hashResult: "new-hash"}
	secret := bytes.Repeat([]byte{9}, 32)
	limiter, err := NewLoginLimiter(LoginLimiterOptions{Secret: secret, Window: time.Minute, MaxAttempts: 5, Capacity: 32})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store: store, Passwords: passwords, Limiter: limiter, SessionSecret: secret,
		Random: bytes.NewReader(bytes.Repeat([]byte{4}, 128)), AbsoluteTTL: time.Hour, IdleTTL: 30 * time.Minute,
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	credential, token, csrf := testSessionCredential(t, 5, 6)
	user := User{ID: "usr_1", Username: "alice", Role: RoleSuperAdmin, Enabled: true, Version: 2}
	session := Session{
		ID: deriveSessionID(secret, token), UserID: user.ID, Status: SessionStatusActive, Current: true,
		CreatedAt: time.Unix(1, 0), LastSeenAt: time.Unix(1, 0), ExpiresAt: time.Unix(1000, 0), AbsoluteExpiresAt: time.Unix(2000, 0),
	}
	store.users["alice"] = UserCredentials{User: user, PasswordHash: "current-hash"}
	store.sessions[digestSessionToken(secret, token)] = SessionIdentity{User: user, Session: session, CSRFDigest: digestCSRFToken(secret, csrf)}
	return service, store, AuthContext{User: user, Session: session, Permissions: PermissionsFor(user.Role), CSRFToken: csrf}, credential
}
