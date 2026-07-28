package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/zjutjh/jxh-go/internal/management/auth"
)

func TestRunHashesPasswordWithConfiguredArgon2id(t *testing.T) {
	store := &fakeBootstrapStore{}
	err := run(t.Context(), []string{
		"-username", "root-admin",
		"-display-name", "Root",
		"-password-stdin",
	}, strings.NewReader("valid-password-123\n"), io.Discard, store)
	if err != nil {
		t.Fatal(err)
	}
	const wantPrefix = "$argon2id$v=19$m=65536,t=3,p=2$"
	if got := store.createdSnapshot().PasswordHash; !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("password hash prefix = %q, want %q", got, wantPrefix)
	}
}

func TestRunCreatesOnlyTheFirstSuperAdmin(t *testing.T) {
	store := &fakeBootstrapStore{}
	var output strings.Builder
	args := []string{
		"-username", "root-admin",
		"-display-name", "Root Administrator",
		"-password-stdin",
	}

	err := runWithDeps(t.Context(), args, strings.NewReader("valid-password-123\n"), &output, store, testBootstrapDeps())
	if err != nil {
		t.Fatal(err)
	}
	created := store.createdSnapshot()
	if created.User.Role != auth.RoleSuperAdmin {
		t.Fatalf("role = %q, want %q", created.User.Role, auth.RoleSuperAdmin)
	}
	if !created.User.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if created.User.Version != 1 {
		t.Fatalf("version = %d, want 1", created.User.Version)
	}
	if created.PasswordHash != "test-password-hash" {
		t.Fatalf("password hash = %q", created.PasswordHash)
	}
	if got, want := output.String(), "user_id=user-1 username=root-admin\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	for _, secret := range []string{"valid-password-123", "Root Administrator", "test-password-hash"} {
		if strings.Contains(output.String(), secret) {
			t.Fatalf("output contains non-public value %q: %q", secret, output.String())
		}
	}

	err = runWithDeps(t.Context(), args, strings.NewReader("another-password-123\n"), io.Discard, store, testBootstrapDeps())
	if !errors.Is(err, ErrAdminAlreadyExists) {
		t.Fatalf("second run error = %v, want ErrAdminAlreadyExists", err)
	}
	if got := store.createCallCount(); got != 2 {
		t.Fatalf("store calls = %d, want 2", got)
	}
}

func TestRunConcurrentBootstrapCreatesExactlyOneAdmin(t *testing.T) {
	store := &fakeBootstrapStore{}
	args := []string{
		"-username", "root-admin",
		"-display-name", "Root",
		"-password-stdin",
	}
	start := make(chan struct{})
	errorsByWorker := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			errorsByWorker <- runWithDeps(
				t.Context(), args, strings.NewReader("valid-password-123\n"), io.Discard, store, testBootstrapDeps(),
			)
		}()
	}
	close(start)
	workers.Wait()
	close(errorsByWorker)

	var succeeded, alreadyExists int
	for err := range errorsByWorker {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrAdminAlreadyExists):
			alreadyExists++
		default:
			t.Fatalf("unexpected bootstrap error: %v", err)
		}
	}
	if succeeded != 1 || alreadyExists != 1 {
		t.Fatalf("results = succeeded %d, already exists %d; want 1, 1", succeeded, alreadyExists)
	}
}

func TestRunRequiresExplicitPasswordStdinForNonTerminal(t *testing.T) {
	store := &fakeBootstrapStore{}
	input := &panicReader{}
	err := runWithDeps(t.Context(), []string{
		"-username", "root-admin",
		"-display-name", "Root",
	}, input, io.Discard, store, testBootstrapDeps())
	if !errors.Is(err, ErrPasswordInputNotTerminal) {
		t.Fatalf("error = %v, want ErrPasswordInputNotTerminal", err)
	}
	if input.called {
		t.Fatal("non-terminal stdin was read without -password-stdin")
	}
	if got := store.createCallCount(); got != 0 {
		t.Fatalf("store calls = %d, want 0", got)
	}
}

func TestRunReadsInteractivePasswordWithoutUsingReader(t *testing.T) {
	store := &fakeBootstrapStore{}
	input := &fakeTerminalInput{fd: 17}
	deps := testBootstrapDeps()
	deps.isTerminal = func(fd int) bool { return fd == 17 }
	deps.readTerminalPassword = func(fd int) ([]byte, error) {
		if fd != 17 {
			t.Fatalf("terminal fd = %d, want 17", fd)
		}
		return []byte("valid-password-123"), nil
	}

	err := runWithDeps(t.Context(), []string{
		"-username", "root-admin",
		"-display-name", "Root",
	}, input, io.Discard, store, deps)
	if err != nil {
		t.Fatal(err)
	}
	if input.readCalled {
		t.Fatal("interactive password was read through the echoing Reader path")
	}
}

func TestRunRejectsPasswordFlagWithoutLeakingValue(t *testing.T) {
	const secret = "do-not-accept-this-password"
	err := runWithDeps(t.Context(), []string{
		"-username", "root-admin",
		"-display-name", "Root",
		"-password=" + secret,
	}, strings.NewReader("ignored\n"), io.Discard, &fakeBootstrapStore{}, testBootstrapDeps())
	if err == nil {
		t.Fatal("error = nil, want rejected password flag")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error contains password: %v", err)
	}
}

func TestRunValidatesBootstrapFieldsBeforeStore(t *testing.T) {
	tests := []struct {
		name        string
		username    string
		displayName string
		password    string
		want        error
	}{
		{name: "uppercase username", username: "Root-Admin", displayName: "Root", password: "valid-password-123", want: ErrInvalidUsername},
		{name: "short username", username: "ab", displayName: "Root", password: "valid-password-123", want: ErrInvalidUsername},
		{name: "blank display name", username: "root-admin", displayName: "  ", password: "valid-password-123", want: ErrInvalidDisplayName},
		{name: "long display name", username: "root-admin", displayName: strings.Repeat("管", 65), password: "valid-password-123", want: ErrInvalidDisplayName},
		{name: "short password", username: "root-admin", displayName: "Root", password: "too-short", want: ErrInvalidPassword},
		{name: "long password", username: "root-admin", displayName: "Root", password: strings.Repeat("密", 129), want: ErrInvalidPassword},
		{name: "multiple stdin lines", username: "root-admin", displayName: "Root", password: "valid-password-123\nsecond-line", want: ErrInvalidPassword},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeBootstrapStore{}
			err := runWithDeps(t.Context(), []string{
				"-username", test.username,
				"-display-name", test.displayName,
				"-password-stdin",
			}, strings.NewReader(test.password+"\n"), io.Discard, store, testBootstrapDeps())
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if got := store.createCallCount(); got != 0 {
				t.Fatalf("store calls = %d, want 0", got)
			}
		})
	}
}

func TestRunSanitizesStoreErrors(t *testing.T) {
	const secret = "valid-password-123"
	store := &fakeBootstrapStore{err: fmt.Errorf("database rejected password %s", secret)}
	err := runWithDeps(t.Context(), []string{
		"-username", "root-admin",
		"-display-name", "Root",
		"-password-stdin",
	}, strings.NewReader(secret+"\n"), io.Discard, store, testBootstrapDeps())
	if err == nil {
		t.Fatal("error = nil, want store failure")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error contains secret: %v", err)
	}
}

func TestRunHelpDoesNotRequireConfiguredStore(t *testing.T) {
	var output strings.Builder
	if err := runWithDeps(t.Context(), []string{"-h"}, strings.NewReader(""), &output, nil, testBootstrapDeps()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "-password-stdin") || strings.Contains(output.String(), "-password ") {
		t.Fatalf("help output does not describe the safe password input: %q", output.String())
	}
}

func testBootstrapDeps() bootstrapDeps {
	return bootstrapDeps{
		hashPassword: func(password []byte) (string, error) {
			if len(password) == 0 {
				return "", errors.New("empty password")
			}
			return "test-password-hash", nil
		},
		isTerminal: func(int) bool { return false },
		readTerminalPassword: func(int) ([]byte, error) {
			return nil, errors.New("unexpected terminal password read")
		},
	}
}

type fakeBootstrapStore struct {
	mu      sync.Mutex
	created bootstrapAdmin
	calls   int
	err     error
}

func (s *fakeBootstrapStore) CreateFirstSuperAdmin(_ context.Context, admin bootstrapAdmin) (auth.User, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	if s.err != nil {
		return auth.User{}, false, s.err
	}
	if s.created.User.ID != "" {
		return auth.User{}, false, nil
	}
	admin.User.ID = "user-1"
	s.created = admin
	return admin.User, true, nil
}

func (s *fakeBootstrapStore) createdSnapshot() bootstrapAdmin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

func (s *fakeBootstrapStore) createCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type panicReader struct {
	called bool
}

func (r *panicReader) Read([]byte) (int, error) {
	r.called = true
	panic("stdin must not be read")
}

type fakeTerminalInput struct {
	fd         uintptr
	readCalled bool
}

func (r *fakeTerminalInput) Fd() uintptr {
	return r.fd
}

func (r *fakeTerminalInput) Read([]byte) (int, error) {
	r.readCalled = true
	return 0, io.EOF
}
