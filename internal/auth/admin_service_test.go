package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAdminServiceAuthorizesBeforeStoreAndHasher(t *testing.T) {
	store := &fakeAdminStore{}
	passwords := &fakeAdminPasswords{}
	service := newAdminServiceFixture(t, store, passwords)

	_, err := service.CreateUser(t.Context(), Principal{Role: RoleMaintainer}, CreateUserInput{}, MutationContext{})
	if !errors.Is(err, ErrAdminForbidden) {
		t.Fatalf("CreateUser() error = %v", err)
	}
	if store.calls != 0 || passwords.hashCalls != 0 {
		t.Fatalf("store calls=%d hash calls=%d", store.calls, passwords.hashCalls)
	}
}

func TestAdminServiceCreatesNormalizedUserWithoutRetainingPassword(t *testing.T) {
	store := &fakeAdminStore{user: User{ID: "usr_1", Username: "root-admin", Role: RoleSuperAdmin, Enabled: true, Version: 1}}
	passwords := &fakeAdminPasswords{hash: "safe-phc"}
	service := newAdminServiceFixture(t, store, passwords)
	qq := "123456"

	got, err := service.CreateUser(t.Context(), superAdminPrincipal(), CreateUserInput{
		Username: " ROOT-ADMIN ", DisplayName: "Root", Role: RoleSuperAdmin, QQUserID: &qq, Password: "valid-password-123",
	}, MutationContext{RequestID: "req_1", IPAddress: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "usr_1" || store.create.Username != "root-admin" || store.create.PasswordHash != "safe-phc" {
		t.Fatalf("user=%+v mutation=%+v", got, store.create)
	}
	if passwords.lastPassword != "valid-password-123" || store.create.OccurredAt != time.Unix(100, 0) {
		t.Fatalf("password=%q occurred=%v", passwords.lastPassword, store.create.OccurredAt)
	}
}

func TestAdminServiceRejectsInvalidCreateBeforeHashAndStore(t *testing.T) {
	store := &fakeAdminStore{}
	passwords := &fakeAdminPasswords{}
	service := newAdminServiceFixture(t, store, passwords)

	_, err := service.CreateUser(t.Context(), superAdminPrincipal(), CreateUserInput{
		Username: "Bad Name", DisplayName: "Root", Role: RoleSuperAdmin, Password: "valid-password-123",
	}, MutationContext{})
	if !errors.Is(err, ErrInvalidAdminInput) || store.calls != 0 || passwords.hashCalls != 0 {
		t.Fatalf("error=%v store=%d hash=%d", err, store.calls, passwords.hashCalls)
	}
}

func TestResetPasswordUsesRevisionAndRevokesAllSessions(t *testing.T) {
	store := &fakeAdminStore{passwordReset: PasswordResetResult{
		User: User{ID: "usr_target", Version: 3}, RevokedSessionCount: 2, CompletedAt: time.Unix(100, 0),
	}}
	passwords := &fakeAdminPasswords{hash: "new-phc"}
	service := newAdminServiceFixture(t, store, passwords)

	result, err := service.ResetPassword(t.Context(), superAdminPrincipal(), "usr_target", 2, "new-valid-password", "idem-reset-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.RevokedSessionCount != 2 || store.reset.ExpectedRevision != 2 || store.reset.IdempotencyKey != "idem-reset-1" || store.reset.PasswordHash != "new-phc" {
		t.Fatalf("result=%+v mutation=%+v", result, store.reset)
	}
}

func TestUpdateUserCopiesPatchAndPropagatesTypedConflict(t *testing.T) {
	qq := "123456"
	store := &fakeAdminStore{updateErr: ErrLastSuperAdmin}
	service := newAdminServiceFixture(t, store, &fakeAdminPasswords{})
	patch := UserPatch{QQUserID: Field[*string]{Set: true, Value: &qq}, Enabled: Field[bool]{Set: true, Value: false}}

	_, err := service.UpdateUser(t.Context(), superAdminPrincipal(), "usr_1", 4, patch, MutationContext{RequestID: "req_1"})
	if !errors.Is(err, ErrLastSuperAdmin) {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	qq = "changed"
	if *store.update.Patch.QQUserID.Value != "123456" {
		t.Fatalf("store patch was aliased: %+v", store.update.Patch)
	}
}

func TestListSessionsMarksOnlyCallingSessionCurrent(t *testing.T) {
	currentFilter := true
	store := &fakeAdminStore{sessionPage: SessionPage{Items: []Session{{ID: "ses_1"}, {ID: "ses_2"}}}}
	service := newAdminServiceFixture(t, store, &fakeAdminPasswords{})

	page, err := service.ListSessions(t.Context(), Principal{UserID: "usr_1", SessionID: "ses_2", Role: RoleSuperAdmin}, SessionListQuery{Current: &currentFilter})
	if err != nil {
		t.Fatal(err)
	}
	if page.Items[0].Current || !page.Items[1].Current || store.sessionQuery.Limit != 50 {
		t.Fatalf("page=%+v query=%+v", page, store.sessionQuery)
	}
}

func TestAdminServiceValidatesQueriesAndIdempotencyBeforeStore(t *testing.T) {
	store := &fakeAdminStore{}
	service := newAdminServiceFixture(t, store, &fakeAdminPasswords{})
	principal := superAdminPrincipal()

	if _, err := service.ListUsers(t.Context(), principal, UserListQuery{Limit: 101}); !errors.Is(err, ErrInvalidAdminInput) {
		t.Fatalf("ListUsers() error = %v", err)
	}
	if _, err := service.RevokeSession(t.Context(), principal, "ses_1", "short", MutationContext{}); !errors.Is(err, ErrInvalidAdminInput) {
		t.Fatalf("RevokeSession() error = %v", err)
	}
	if store.calls != 0 {
		t.Fatalf("invalid input reached store %d times", store.calls)
	}
}

func newAdminServiceFixture(t *testing.T, store AdminStore, passwords PasswordEngine) *AdminService {
	t.Helper()
	service, err := NewAdminService(AdminServiceOptions{Store: store, Passwords: passwords, Now: func() time.Time { return time.Unix(100, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func superAdminPrincipal() Principal {
	return Principal{UserID: "usr_root", SessionID: "ses_root", Role: RoleSuperAdmin}
}

type fakeAdminPasswords struct {
	hash         string
	err          error
	hashCalls    int
	lastPassword string
}

func (f *fakeAdminPasswords) Hash(password []byte) (string, error) {
	f.hashCalls++
	f.lastPassword = string(password)
	return f.hash, f.err
}

func (f *fakeAdminPasswords) Verify([]byte, string) (bool, bool, error) { return false, false, nil }

type fakeAdminStore struct {
	user          User
	userFound     bool
	userPage      UserPage
	sessionPage   SessionPage
	passwordReset PasswordResetResult
	revokeResult  SessionRevokeResult
	createErr     error
	getErr        error
	listErr       error
	updateErr     error
	resetErr      error
	revokeErr     error
	calls         int
	create        CreateUserMutation
	update        UpdateUserMutation
	reset         ResetPasswordMutation
	revoke        RevokeSessionsMutation
	userQuery     UserListQuery
	sessionQuery  SessionListQuery
}

func (f *fakeAdminStore) CreateAdminUser(_ context.Context, mutation CreateUserMutation) (User, error) {
	f.calls++
	f.create = mutation
	return f.user, f.createErr
}

func (f *fakeAdminStore) GetAdminUser(_ context.Context, _ string) (User, bool, error) {
	f.calls++
	return f.user, f.userFound, f.getErr
}

func (f *fakeAdminStore) ListAdminUsers(_ context.Context, query UserListQuery) (UserPage, error) {
	f.calls++
	f.userQuery = query
	return f.userPage, f.listErr
}

func (f *fakeAdminStore) UpdateAdminUser(_ context.Context, mutation UpdateUserMutation) (User, error) {
	f.calls++
	f.update = mutation
	return f.user, f.updateErr
}

func (f *fakeAdminStore) ResetAdminUserPassword(_ context.Context, mutation ResetPasswordMutation) (PasswordResetResult, error) {
	f.calls++
	f.reset = mutation
	return f.passwordReset, f.resetErr
}

func (f *fakeAdminStore) RevokeAdminUserSessions(_ context.Context, mutation RevokeSessionsMutation) (SessionRevokeResult, error) {
	f.calls++
	f.revoke = mutation
	return f.revokeResult, f.revokeErr
}

func (f *fakeAdminStore) ListAdminSessions(_ context.Context, query SessionListQuery) (SessionPage, error) {
	f.calls++
	f.sessionQuery = query
	return f.sessionPage, f.listErr
}

func (f *fakeAdminStore) RevokeAdminSession(_ context.Context, mutation RevokeSessionsMutation) (SessionRevokeResult, error) {
	f.calls++
	f.revoke = mutation
	return f.revokeResult, f.revokeErr
}
