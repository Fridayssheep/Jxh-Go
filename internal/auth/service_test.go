package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSessionTokensUseIndependentRandomReadsAndDomainSeparatedDigests(t *testing.T) {
	random := &recordingRandomReader{}
	generator := newSessionTokenGenerator(random)
	sessionToken, csrfToken, err := generator.newPair()
	if err != nil {
		t.Fatal(err)
	}
	if len(random.readSizes) != 2 || random.readSizes[0] != sessionTokenBytes || random.readSizes[1] != sessionTokenBytes {
		t.Fatalf("random read sizes = %v, want [%d %d]", random.readSizes, sessionTokenBytes, sessionTokenBytes)
	}
	if sessionToken == csrfToken {
		t.Fatal("session and CSRF tokens are equal")
	}
	for name, token := range map[string]string{"session": sessionToken, "csrf": csrfToken} {
		decoded, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("decode %s token: %v", name, err)
		}
		if len(decoded) != sessionTokenBytes {
			t.Fatalf("%s token decoded length = %d, want %d", name, len(decoded), sessionTokenBytes)
		}
		if bytes.Contains([]byte(token), []byte{'='}) {
			t.Fatalf("%s token contains base64 padding", name)
		}
	}

	secret := []byte("session-digest-test-secret")
	sessionDigest := digestSessionToken(secret, sessionToken)
	csrfDigest := digestCSRFToken(secret, sessionToken)
	if len(sessionDigest) != 32 || len(csrfDigest) != 32 {
		t.Fatalf("digest lengths = %d and %d, want 32", len(sessionDigest), len(csrfDigest))
	}
	if sessionDigest == csrfDigest {
		t.Fatal("session and CSRF digest domains are not separated")
	}
}

func TestSessionTokenRandomnessFailureIsClassifiedAndReturnsNoToken(t *testing.T) {
	random := io.MultiReader(bytes.NewReader(bytes.Repeat([]byte{7}, sessionTokenBytes)), errorReader{})
	generator := newSessionTokenGenerator(random)
	sessionToken, csrfToken, err := generator.newPair()
	if !errors.Is(err, ErrSessionRandomness) {
		t.Fatalf("newPair() error = %v, want ErrSessionRandomness", err)
	}
	if sessionToken != "" || csrfToken != "" {
		t.Fatalf("newPair() returned partial tokens: session=%q csrf=%q", sessionToken, csrfToken)
	}
}

func TestSessionTokenGeneratorRejectsEqualRandomValues(t *testing.T) {
	generator := newSessionTokenGenerator(bytes.NewReader(bytes.Repeat([]byte{7}, 2*sessionTokenBytes)))
	sessionToken, csrfToken, err := generator.newPair()
	if !errors.Is(err, ErrSessionRandomness) {
		t.Fatalf("newPair() error = %v, want ErrSessionRandomness", err)
	}
	if sessionToken != "" || csrfToken != "" {
		t.Fatalf("newPair() returned equal token material: session=%q csrf=%q", sessionToken, csrfToken)
	}
}

func TestSessionCredentialCarriesAnExplicitVersion(t *testing.T) {
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{5}, sessionTokenBytes))
	csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{6}, sessionTokenBytes))
	credential, err := composeSessionCredential(sessionToken, csrfToken)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(credential)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded) != sessionCredentialBytes || decoded[0] != sessionCredentialVersion {
		t.Fatalf("credential prefix/length = %d/%d, want %d/%d", decoded[0], len(decoded), sessionCredentialVersion, sessionCredentialBytes)
	}
}

func TestLoginUsesUniformCredentialsErrorAndDummyVerification(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name          string
		stored        UserCredentials
		found         bool
		wantHash      string
		forbiddenHash string
	}{
		{
			name:     "known account wrong password",
			stored:   testUserCredentials("alice", true, "real-password-hash"),
			found:    true,
			wantHash: "real-password-hash",
		},
		{
			name:     "unknown account",
			found:    false,
			wantHash: dummyPasswordPHC,
		},
		{
			name:          "disabled account",
			stored:        testUserCredentials("alice", false, "disabled-real-password-hash"),
			found:         true,
			wantHash:      dummyPasswordPHC,
			forbiddenHash: "disabled-real-password-hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeAuthStore()
			if tt.found {
				store.users["alice"] = tt.stored
			}
			passwords := &spyPasswordEngine{}
			service := newTestAuthService(t, store, passwords, bytes.NewReader(bytes.Repeat([]byte{7}, 64)), now)

			_, err := service.Login(context.Background(), LoginRequest{
				Username:  "  ALICE  ",
				Password:  "wrong password",
				ClientIP:  "192.0.2.10",
				UserAgent: "test browser",
			})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
			}
			if got := store.userLookupsSnapshot(); len(got) != 1 || got[0] != "alice" {
				t.Fatalf("user lookups = %v, want [alice]", got)
			}
			calls := passwords.verifyHashesSnapshot()
			if len(calls) != 1 || calls[0] != tt.wantHash {
				t.Fatalf("Verify() hashes = %v, want [%q]", calls, tt.wantHash)
			}
			if tt.forbiddenHash != "" && calls[0] == tt.forbiddenHash {
				t.Fatal("disabled account used its real password hash")
			}
			if got := store.loginCommitsSnapshot(); len(got) != 0 {
				t.Fatalf("login commit count = %d, want 0", len(got))
			}
		})
	}
}

func TestLoginPersistsOnlyDigestsAndReturnsAuthContext(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 123000000, time.UTC)
	store := newFakeAuthStore()
	store.users["alice"] = testUserCredentials("alice", true, "real-password-hash")
	passwords := &spyPasswordEngine{verifyOK: true}
	service := newTestAuthService(t, store, passwords, &recordingRandomReader{}, now)

	result, err := service.Login(context.Background(), LoginRequest{
		Username:  " Alice ",
		Password:  "correct password",
		ClientIP:  "192.0.2.10",
		UserAgent: "test browser",
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if result.SessionToken == "" || result.CSRFToken == "" || result.SessionToken == result.CSRFToken {
		t.Fatalf("invalid returned tokens: session=%q csrf=%q", result.SessionToken, result.CSRFToken)
	}
	if result.User.ID != "user-alice" || result.Session.UserID != result.User.ID {
		t.Fatalf("returned identity = user %+v session %+v", result.User, result.Session)
	}
	if result.Session.ID == "" {
		t.Fatal("returned session ID is empty")
	}
	if result.Session.Status != SessionStatusActive || !result.Session.Current {
		t.Fatalf("returned session status/current = %q/%v", result.Session.Status, result.Session.Current)
	}
	if result.Session.IPAddress != "192.0.2.10" || result.Session.UserAgent != "test browser" {
		t.Fatalf("returned session metadata = %+v", result.Session)
	}
	if !result.Session.CreatedAt.Equal(now) || !result.Session.LastSeenAt.Equal(now) ||
		!result.Session.ExpiresAt.Equal(now.Add(30*time.Minute)) ||
		!result.Session.AbsoluteExpiresAt.Equal(now.Add(12*time.Hour)) {
		t.Fatalf("returned session timestamps = %+v", result.Session)
	}
	if want := PermissionsFor(RoleMaintainer); !reflect.DeepEqual(result.Permissions, want) {
		t.Fatalf("permissions = %v, want %v", result.Permissions, want)
	}

	commits := store.loginCommitsSnapshot()
	if len(commits) != 1 {
		t.Fatalf("login commit count = %d, want 1", len(commits))
	}
	commit := commits[0]
	secret := bytes.Repeat([]byte{9}, 32)
	sessionToken, csrfFromCredential, ok := parseSessionCredential(result.SessionToken)
	if !ok || csrfFromCredential != result.CSRFToken {
		t.Fatalf("session credential did not preserve CSRF token: ok=%v csrf=%q", ok, csrfFromCredential)
	}
	if commit.TokenDigest != digestSessionToken(secret, sessionToken) {
		t.Fatal("stored session digest does not match returned token")
	}
	if commit.CSRFDigest != digestCSRFToken(secret, result.CSRFToken) {
		t.Fatal("stored CSRF digest does not match returned token")
	}
	if commit.TokenDigest == commit.CSRFDigest {
		t.Fatal("stored session and CSRF digests are equal")
	}
	if commit.Session != result.Session {
		t.Fatalf("committed session = %+v, want %+v", commit.Session, result.Session)
	}
	if commit.Session.ID == result.SessionToken || commit.Session.ID == result.CSRFToken {
		t.Fatal("session ID contains a browser token")
	}
	if commit.PasswordHashUpdate != nil || commit.PriorTokenDigest != nil {
		t.Fatalf("unexpected atomic login options: %+v", commit)
	}
	authenticated, err := service.Authenticate(context.Background(), result.SessionToken)
	if err != nil {
		t.Fatalf("Authenticate(login credential) error = %v", err)
	}
	if authenticated.CSRFToken != result.CSRFToken {
		t.Fatalf("Authenticate() CSRF token = %q, want login token", authenticated.CSRFToken)
	}
	assertPublicAuthTypesContainNoDigest(t)
}

func TestLoginAtomicallyUpgradesPasswordAndRotatesPriorSession(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	store := newFakeAuthStore()
	credentials := testUserCredentials("alice", true, "old-password-hash")
	credentials.User.Version = 41
	store.users["alice"] = credentials
	priorSessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, sessionTokenBytes))
	priorCSRFToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, sessionTokenBytes))
	priorCredential, err := composeSessionCredential(priorSessionToken, priorCSRFToken)
	if err != nil {
		t.Fatal(err)
	}
	priorIdentity := testSessionIdentity(now, now.Add(-time.Second), now.Add(time.Hour))
	priorIdentity.Session.ID = "prior-session-id"
	priorIdentity.CSRFDigest = digestCSRFToken(bytes.Repeat([]byte{9}, 32), priorCSRFToken)
	priorDigest := digestSessionToken(bytes.Repeat([]byte{9}, 32), priorSessionToken)
	store.sessions[priorDigest] = priorIdentity
	passwords := &spyPasswordEngine{
		verifyOK:   true,
		upgrade:    true,
		hashResult: "upgraded-password-hash",
	}
	service := newTestAuthService(t, store, passwords, &recordingRandomReader{}, now)

	_, err = service.Login(context.Background(), LoginRequest{
		Username:        "alice",
		Password:        "correct password",
		ClientIP:        "192.0.2.10",
		UserAgent:       "test browser",
		PriorCredential: priorCredential,
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if got := passwords.hashCallCount(); got != 1 {
		t.Fatalf("Hash() call count = %d, want 1", got)
	}
	commits := store.loginCommitsSnapshot()
	if len(commits) != 1 {
		t.Fatalf("CommitLogin() call count = %d, want 1", len(commits))
	}
	commit := commits[0]
	if commit.PriorTokenDigest == nil || *commit.PriorTokenDigest != priorDigest {
		t.Fatalf("PriorTokenDigest = %v, want %v", commit.PriorTokenDigest, priorDigest)
	}
	wantUpdate := &PasswordHashUpdate{
		UserID:          credentials.User.ID,
		ExpectedVersion: 41,
		PasswordHash:    "upgraded-password-hash",
	}
	if !reflect.DeepEqual(commit.PasswordHashUpdate, wantUpdate) {
		t.Fatalf("PasswordHashUpdate = %+v, want %+v", commit.PasswordHashUpdate, wantUpdate)
	}
	prior, replacement, upgraded := store.atomicLoginState("prior-session-id", "alice")
	if prior.Status != SessionStatusRevoked || prior.RevokedAt == nil {
		t.Fatalf("prior session was not atomically revoked: %+v", prior)
	}
	if replacement != commit.Session.ID {
		t.Fatalf("prior session replacement = %q, want %q", replacement, commit.Session.ID)
	}
	if upgraded.PasswordHash != "upgraded-password-hash" || upgraded.User.Version != 42 {
		t.Fatalf("credentials were not atomically upgraded: %+v", upgraded)
	}
}

func TestAuthenticateEnforcesIdleAndAbsoluteExpiry(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	credential, token, csrfToken := testSessionCredential(t, 5, 15)
	secret := bytes.Repeat([]byte{9}, 32)
	digest := digestSessionToken(secret, token)
	baseIdentity := SessionIdentity{
		User: User{
			ID:       "user-alice",
			Username: "alice",
			Role:     RoleMaintainer,
			Enabled:  true,
		},
		Session: Session{
			ID:                "session-alice",
			UserID:            "user-alice",
			Status:            SessionStatusActive,
			CreatedAt:         now.Add(-time.Hour),
			LastSeenAt:        now.Add(-30 * time.Second),
			ExpiresAt:         now.Add(time.Minute),
			AbsoluteExpiresAt: now.Add(time.Hour),
		},
		CSRFDigest: digestCSRFToken(secret, csrfToken),
	}
	tests := []struct {
		name   string
		found  bool
		mutate func(*SessionIdentity)
	}{
		{name: "missing", found: false},
		{name: "revoked", found: true, mutate: func(identity *SessionIdentity) {
			identity.Session.Status = SessionStatusRevoked
			revokedAt := now.Add(-time.Minute)
			identity.Session.RevokedAt = &revokedAt
		}},
		{name: "expired status", found: true, mutate: func(identity *SessionIdentity) {
			identity.Session.Status = SessionStatusExpired
		}},
		{name: "disabled user", found: true, mutate: func(identity *SessionIdentity) {
			identity.User.Enabled = false
		}},
		{name: "idle expiry equal now", found: true, mutate: func(identity *SessionIdentity) {
			identity.Session.ExpiresAt = now
		}},
		{name: "absolute expiry equal now", found: true, mutate: func(identity *SessionIdentity) {
			identity.Session.AbsoluteExpiresAt = now
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeAuthStore()
			if tt.found {
				identity := baseIdentity
				if tt.mutate != nil {
					tt.mutate(&identity)
				}
				store.sessions[digest] = identity
			}
			service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)

			_, err := service.Authenticate(context.Background(), credential)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("Authenticate() error = %v, want ErrUnauthenticated", err)
			}
			lookups := store.sessionLookupsSnapshot()
			if len(lookups) != 1 || lookups[0] != digest {
				t.Fatalf("session lookups = %v, want only token digest", lookups)
			}
		})
	}
}

func TestAuthenticateReturnsContextAndTouchesConditionallyAtSixtySeconds(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	credential, token, csrfToken := testSessionCredential(t, 6, 16)
	secret := bytes.Repeat([]byte{9}, 32)
	digest := digestSessionToken(secret, token)

	t.Run("fresh session is not touched", func(t *testing.T) {
		store := newFakeAuthStore()
		identity := testSessionIdentity(now, now.Add(-59*time.Second), now.Add(time.Hour))
		identity.CSRFDigest = digestCSRFToken(secret, csrfToken)
		store.sessions[digest] = identity
		service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)

		result, err := service.Authenticate(context.Background(), credential)
		if err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}
		if result.User.ID != "user-alice" || result.Session.ID != "session-alice" || !result.Session.Current {
			t.Fatalf("AuthContext identity = %+v", result)
		}
		if want := PermissionsFor(RoleMaintainer); !reflect.DeepEqual(result.Permissions, want) {
			t.Fatalf("permissions = %v, want %v", result.Permissions, want)
		}
		if result.CSRFToken != csrfToken {
			t.Fatalf("CSRFToken = %q, want credential token", result.CSRFToken)
		}
		if got := store.touchesSnapshot(); len(got) != 0 {
			t.Fatalf("touch count = %d, want 0", len(got))
		}
	})

	t.Run("sixty seconds uses conditional touch once", func(t *testing.T) {
		store := newFakeAuthStore()
		absoluteExpiry := now.Add(10 * time.Minute)
		identity := testSessionIdentity(now, now.Add(-time.Minute), absoluteExpiry)
		identity.CSRFDigest = digestCSRFToken(secret, csrfToken)
		store.sessions[digest] = identity
		service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)

		result, err := service.Authenticate(context.Background(), credential)
		if err != nil {
			t.Fatalf("Authenticate() error = %v", err)
		}
		if !result.Session.LastSeenAt.Equal(now) || !result.Session.ExpiresAt.Equal(absoluteExpiry) {
			t.Fatalf("touched session timestamps = %+v", result.Session)
		}
		if _, err := service.Authenticate(context.Background(), credential); err != nil {
			t.Fatalf("second Authenticate() error = %v", err)
		}
		touches := store.touchesSnapshot()
		if len(touches) != 1 {
			t.Fatalf("conditional touch count = %d, want 1", len(touches))
		}
		wantTouch := SessionTouch{
			SessionID:        "session-alice",
			IfLastSeenBefore: now.Add(-time.Minute),
			LastSeenAt:       now,
			ExpiresAt:        absoluteExpiry,
		}
		if touches[0] != wantTouch {
			t.Fatalf("touch = %+v, want %+v", touches[0], wantTouch)
		}
	})
}

func TestLoginRejectsInvalidOrOversizedTextBeforeLookup(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		mutate func(*LoginRequest)
	}{
		{name: "invalid UTF-8 username", mutate: func(request *LoginRequest) {
			request.Username = string([]byte{0xff})
		}},
		{name: "invalid UTF-8 password", mutate: func(request *LoginRequest) {
			request.Password = string([]byte{0xff})
		}},
		{name: "oversized username", mutate: func(request *LoginRequest) {
			request.Username = strings.Repeat("a", 257)
		}},
		{name: "oversized password", mutate: func(request *LoginRequest) {
			request.Password = strings.Repeat("p", 4097)
		}},
		{name: "oversized user agent", mutate: func(request *LoginRequest) {
			request.UserAgent = strings.Repeat("界", 301)
		}},
		{name: "oversized client IP", mutate: func(request *LoginRequest) {
			request.ClientIP = strings.Repeat("1", 65)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newFakeAuthStore()
			store.users["alice"] = testUserCredentials("alice", true, "real-password-hash")
			passwords := &spyPasswordEngine{}
			service := newTestAuthService(t, store, passwords, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)
			request := LoginRequest{
				Username:  "alice",
				Password:  "password",
				ClientIP:  "192.0.2.10",
				UserAgent: "test browser",
			}
			tt.mutate(&request)

			_, err := service.Login(context.Background(), request)
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Login() error = %v, want ErrInvalidCredentials", err)
			}
			if got := store.userLookupsSnapshot(); len(got) != 0 {
				t.Fatalf("user lookup count = %d, want 0", len(got))
			}
			calls := passwords.verifyHashesSnapshot()
			if len(calls) != 1 || calls[0] != dummyPasswordPHC {
				t.Fatalf("Verify() hashes = %v, want one dummy PHC", calls)
			}
		})
	}
}

func TestAuthenticateRejectsMalformedTokenBeforeStore(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	for _, token := range []string{
		"",
		"short",
		strings.Repeat("*", base64.RawURLEncoding.EncodedLen(sessionTokenBytes)),
		strings.Repeat("a", 4096),
	} {
		store := newFakeAuthStore()
		service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)

		_, err := service.Authenticate(context.Background(), token)
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Authenticate(%q) error = %v, want ErrUnauthenticated", token, err)
		}
		if got := store.sessionLookupsSnapshot(); len(got) != 0 {
			t.Fatalf("Authenticate(%q) lookup count = %d, want 0", token, len(got))
		}
	}
}

func TestLoginRateLimitsEitherUsernameOrIPBucket(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)

	t.Run("username across IP addresses", func(t *testing.T) {
		store := newFakeAuthStore()
		store.users["alice"] = testUserCredentials("alice", true, "real-password-hash")
		passwords := &spyPasswordEngine{}
		service := newTestAuthServiceWithMaxAttempts(t, store, passwords, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now, 2)
		for _, clientIP := range []string{"192.0.2.10", "192.0.2.11"} {
			_, err := service.Login(context.Background(), LoginRequest{Username: "alice", Password: "wrong", ClientIP: clientIP})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("failed Login() error = %v, want ErrInvalidCredentials", err)
			}
		}
		_, err := service.Login(context.Background(), LoginRequest{Username: " ALICE ", Password: "wrong", ClientIP: "192.0.2.99"})
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("threshold Login() error = %v, want ErrRateLimited", err)
		}
	})

	t.Run("IP across usernames", func(t *testing.T) {
		store := newFakeAuthStore()
		passwords := &spyPasswordEngine{}
		service := newTestAuthServiceWithMaxAttempts(t, store, passwords, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now, 2)
		for _, username := range []string{"alice", "bob"} {
			_, err := service.Login(context.Background(), LoginRequest{Username: username, Password: "wrong", ClientIP: "192.0.2.10"})
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("failed Login() error = %v, want ErrInvalidCredentials", err)
			}
		}
		_, err := service.Login(context.Background(), LoginRequest{Username: "charlie", Password: "wrong", ClientIP: "192.0.2.10"})
		if !errors.Is(err, ErrRateLimited) {
			t.Fatalf("threshold Login() error = %v, want ErrRateLimited", err)
		}
	})
}

func TestLoginSuccessClearsUsernameButPreservesSharedIPBucket(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	store := newFakeAuthStore()
	store.users["alice"] = testUserCredentials("alice", true, "real-password-hash")
	passwords := &spyPasswordEngine{}
	service := newTestAuthServiceWithMaxAttempts(t, store, passwords, &recordingRandomReader{}, now, 2)

	login := func(username, clientIP string) error {
		_, err := service.Login(context.Background(), LoginRequest{
			Username: username,
			Password: "password",
			ClientIP: clientIP,
		})
		return err
	}
	if err := login("alice", "192.0.2.10"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("first failed Login() error = %v", err)
	}
	passwords.setVerifyResult(true)
	if err := login("alice", "192.0.2.11"); err != nil {
		t.Fatalf("first successful Login() error = %v", err)
	}
	passwords.setVerifyResult(false)
	if err := login("alice", "192.0.2.12"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("post-success failed Login() error = %v", err)
	}
	passwords.setVerifyResult(true)
	if err := login("alice", "192.0.2.13"); err != nil {
		t.Fatalf("username bucket was not cleared, Login() error = %v", err)
	}

	passwords.setVerifyResult(false)
	if err := login("bob", "192.0.2.10"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("second shared-IP failure = %v", err)
	}
	if err := login("charlie", "192.0.2.10"); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("shared IP bucket was cleared, Login() error = %v", err)
	}
}

func TestAuthServiceErrorsPreserveClassificationWithoutSensitiveValues(t *testing.T) {
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, time.UTC)
	const password = "sensitive-login-password"
	const passwordHash = "sensitive-password-hash"

	t.Run("session randomness", func(t *testing.T) {
		store := newFakeAuthStore()
		store.users["alice"] = testUserCredentials("alice", true, passwordHash)
		service := newTestAuthService(t, store, &spyPasswordEngine{verifyOK: true}, errorReader{}, now)
		_, err := service.Login(context.Background(), LoginRequest{Username: "alice", Password: password, ClientIP: "192.0.2.10"})
		assertClassifiedErrorWithoutSecrets(t, err, ErrSessionRandomness, password, passwordHash)
	})

	t.Run("password upgrade randomness", func(t *testing.T) {
		store := newFakeAuthStore()
		store.users["alice"] = testUserCredentials("alice", true, passwordHash)
		passwords := &spyPasswordEngine{verifyOK: true, upgrade: true, hashErr: ErrPasswordRandomness}
		service := newTestAuthService(t, store, passwords, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)
		_, err := service.Login(context.Background(), LoginRequest{Username: "alice", Password: password, ClientIP: "192.0.2.10"})
		assertClassifiedErrorWithoutSecrets(t, err, ErrPasswordRandomness, password, passwordHash)
	})

	t.Run("user lookup store error", func(t *testing.T) {
		storeErr := errors.New("user lookup unavailable")
		store := newFakeAuthStore()
		store.lookupUserErr = storeErr
		service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)
		_, err := service.Login(context.Background(), LoginRequest{Username: "alice", Password: password, ClientIP: "192.0.2.10"})
		assertClassifiedErrorWithoutSecrets(t, err, storeErr, password, passwordHash)
	})

	t.Run("atomic login store error", func(t *testing.T) {
		storeErr := errors.New("login transaction unavailable")
		store := newFakeAuthStore()
		store.users["alice"] = testUserCredentials("alice", true, passwordHash)
		store.commitErr = storeErr
		random := &recordingRandomReader{}
		service := newTestAuthService(t, store, &spyPasswordEngine{verifyOK: true}, random, now)
		_, err := service.Login(context.Background(), LoginRequest{Username: "alice", Password: password, ClientIP: "192.0.2.10"})
		sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, sessionTokenBytes))
		csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{2}, sessionTokenBytes))
		assertClassifiedErrorWithoutSecrets(t, err, storeErr, password, passwordHash, sessionToken, csrfToken)
	})

	t.Run("session lookup store error", func(t *testing.T) {
		storeErr := errors.New("session lookup unavailable")
		store := newFakeAuthStore()
		store.lookupSessionErr = storeErr
		service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)
		credential, _, _ := testSessionCredential(t, 5, 15)
		_, err := service.Authenticate(context.Background(), credential)
		assertClassifiedErrorWithoutSecrets(t, err, storeErr, credential)
	})

	t.Run("conditional touch store error", func(t *testing.T) {
		storeErr := errors.New("session touch unavailable")
		store := newFakeAuthStore()
		store.touchErr = storeErr
		credential, token, csrfToken := testSessionCredential(t, 6, 16)
		secret := bytes.Repeat([]byte{9}, 32)
		digest := digestSessionToken(secret, token)
		identity := testSessionIdentity(now, now.Add(-time.Minute), now.Add(time.Hour))
		identity.CSRFDigest = digestCSRFToken(secret, csrfToken)
		store.sessions[digest] = identity
		service := newTestAuthService(t, store, &spyPasswordEngine{}, bytes.NewReader(bytes.Repeat([]byte{1}, 64)), now)
		_, err := service.Authenticate(context.Background(), credential)
		assertClassifiedErrorWithoutSecrets(t, err, storeErr, credential)
	})
}

func TestAuthConstructorsRejectInvalidConfiguration(t *testing.T) {
	if _, err := NewLoginLimiter(LoginLimiterOptions{}); !errors.Is(err, ErrInvalidAuthConfig) {
		t.Fatalf("NewLoginLimiter() error = %v, want ErrInvalidAuthConfig", err)
	}
	if _, err := NewService(ServiceOptions{}); !errors.Is(err, ErrInvalidAuthConfig) {
		t.Fatalf("NewService() error = %v, want ErrInvalidAuthConfig", err)
	}
}

func assertClassifiedErrorWithoutSecrets(t *testing.T, err, target error, secrets ...string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, target)
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error contains sensitive value %q", secret)
		}
	}
}

func testSessionIdentity(now, lastSeenAt, absoluteExpiresAt time.Time) SessionIdentity {
	return SessionIdentity{
		User: User{
			ID:       "user-alice",
			Username: "alice",
			Role:     RoleMaintainer,
			Enabled:  true,
		},
		Session: Session{
			ID:                "session-alice",
			UserID:            "user-alice",
			Status:            SessionStatusActive,
			CreatedAt:         now.Add(-time.Hour),
			LastSeenAt:        lastSeenAt,
			ExpiresAt:         now.Add(time.Minute),
			AbsoluteExpiresAt: absoluteExpiresAt,
		},
	}
}

func testSessionCredential(t *testing.T, sessionByte, csrfByte byte) (string, string, string) {
	t.Helper()
	sessionToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{sessionByte}, sessionTokenBytes))
	csrfToken := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{csrfByte}, sessionTokenBytes))
	credential, err := composeSessionCredential(sessionToken, csrfToken)
	if err != nil {
		t.Fatal(err)
	}
	return credential, sessionToken, csrfToken
}

func assertPublicAuthTypesContainNoDigest(t *testing.T) {
	t.Helper()
	for _, value := range []any{User{}, Session{}, AuthContext{}, LoginResult{}} {
		typeOf := reflect.TypeOf(value)
		for i := range typeOf.NumField() {
			field := typeOf.Field(i)
			if field.Type == reflect.TypeOf(TokenDigest{}) {
				t.Fatalf("public %s contains digest field %s", typeOf.Name(), field.Name)
			}
		}
	}
}

func newTestAuthService(t *testing.T, store *fakeAuthStore, passwords *spyPasswordEngine, random io.Reader, now time.Time) *Service {
	return newTestAuthServiceWithMaxAttempts(t, store, passwords, random, now, 5)
}

func newTestAuthServiceWithMaxAttempts(t *testing.T, store *fakeAuthStore, passwords *spyPasswordEngine, random io.Reader, now time.Time, maxAttempts int) *Service {
	t.Helper()
	limiter, err := NewLoginLimiter(LoginLimiterOptions{
		Window:      5 * time.Minute,
		MaxAttempts: maxAttempts,
		Capacity:    128,
		Secret:      bytes.Repeat([]byte{1}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(ServiceOptions{
		Store:         store,
		Passwords:     passwords,
		Limiter:       limiter,
		SessionSecret: bytes.Repeat([]byte{9}, 32),
		Random:        random,
		AbsoluteTTL:   12 * time.Hour,
		IdleTTL:       30 * time.Minute,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testUserCredentials(username string, enabled bool, passwordHash string) UserCredentials {
	return UserCredentials{
		User: User{
			ID:          "user-" + username,
			Username:    username,
			DisplayName: username,
			Role:        RoleMaintainer,
			Enabled:     enabled,
			Version:     1,
		},
		PasswordHash: passwordHash,
	}
}

type spyPasswordEngine struct {
	mu           sync.Mutex
	verifyHashes []string
	verifyOK     bool
	upgrade      bool
	verifyErr    error
	hashResult   string
	hashErr      error
	hashCalls    int
}

func (s *spyPasswordEngine) Verify(_ []byte, encoded string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyHashes = append(s.verifyHashes, encoded)
	return s.verifyOK, s.upgrade, s.verifyErr
}

func (s *spyPasswordEngine) Hash([]byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hashCalls++
	return s.hashResult, s.hashErr
}

func (s *spyPasswordEngine) verifyHashesSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.verifyHashes...)
}

func (s *spyPasswordEngine) hashCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hashCalls
}

func (s *spyPasswordEngine) setVerifyResult(ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.verifyOK = ok
}

type fakeAuthStore struct {
	mu               sync.Mutex
	users            map[string]UserCredentials
	userLookups      []string
	loginCommits     []LoginCommit
	sessions         map[TokenDigest]SessionIdentity
	sessionLookups   []TokenDigest
	touches          []SessionTouch
	replacedBy       map[string]string
	lookupUserErr    error
	commitErr        error
	lookupSessionErr error
	touchErr         error
	passwordChanges  map[string]SessionIdentity
	passwordCommits  []PasswordChangeCommit
	revocations      []CurrentSessionRevocation
}

func newFakeAuthStore() *fakeAuthStore {
	return &fakeAuthStore{
		users:           make(map[string]UserCredentials),
		sessions:        make(map[TokenDigest]SessionIdentity),
		replacedBy:      make(map[string]string),
		passwordChanges: make(map[string]SessionIdentity),
	}
}

func (s *fakeAuthStore) LookupUserByID(_ context.Context, userID string) (UserCredentials, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lookupUserErr != nil {
		return UserCredentials{}, false, s.lookupUserErr
	}
	for _, user := range s.users {
		if user.User.ID == userID {
			return user, true, nil
		}
	}
	return UserCredentials{}, false, nil
}

func (s *fakeAuthStore) LookupUserByUsername(_ context.Context, username string) (UserCredentials, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.userLookups = append(s.userLookups, username)
	if s.lookupUserErr != nil {
		return UserCredentials{}, false, s.lookupUserErr
	}
	user, ok := s.users[username]
	return user, ok, nil
}

func (s *fakeAuthStore) CommitLogin(_ context.Context, commit LoginCommit) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	var credentials UserCredentials
	for username, candidate := range s.users {
		if candidate.User.ID != commit.Session.UserID {
			continue
		}
		if commit.PasswordHashUpdate != nil {
			candidate.PasswordHash = commit.PasswordHashUpdate.PasswordHash
			candidate.User.Version++
			s.users[username] = candidate
		}
		credentials = candidate
		break
	}
	if commit.PriorTokenDigest != nil {
		if identity, ok := s.sessions[*commit.PriorTokenDigest]; ok && identity.User.ID == commit.Session.UserID {
			identity.Session.Status = SessionStatusRevoked
			revokedAt := commit.Session.CreatedAt
			identity.Session.RevokedAt = &revokedAt
			s.sessions[*commit.PriorTokenDigest] = identity
			s.replacedBy[identity.Session.ID] = commit.Session.ID
		}
	}
	s.sessions[commit.TokenDigest] = SessionIdentity{
		User:       credentials.User,
		Session:    commit.Session,
		CSRFDigest: commit.CSRFDigest,
	}
	s.loginCommits = append(s.loginCommits, commit)
	return nil
}

func (s *fakeAuthStore) atomicLoginState(priorSessionID, username string) (Session, string, UserCredentials) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var prior Session
	for _, identity := range s.sessions {
		if identity.Session.ID == priorSessionID {
			prior = identity.Session
			break
		}
	}
	return prior, s.replacedBy[priorSessionID], s.users[username]
}

func (s *fakeAuthStore) LookupSession(_ context.Context, digest TokenDigest) (SessionIdentity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionLookups = append(s.sessionLookups, digest)
	if s.lookupSessionErr != nil {
		return SessionIdentity{}, false, s.lookupSessionErr
	}
	identity, ok := s.sessions[digest]
	return identity, ok, nil
}

func (s *fakeAuthStore) sessionLookupsSnapshot() []TokenDigest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TokenDigest(nil), s.sessionLookups...)
}

func (s *fakeAuthStore) TouchSessionIfStale(_ context.Context, touch SessionTouch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touchErr != nil {
		return s.touchErr
	}
	for digest, identity := range s.sessions {
		if identity.Session.ID != touch.SessionID || identity.Session.LastSeenAt.After(touch.IfLastSeenBefore) {
			continue
		}
		identity.Session.LastSeenAt = touch.LastSeenAt
		identity.Session.ExpiresAt = touch.ExpiresAt
		s.sessions[digest] = identity
		s.touches = append(s.touches, touch)
		break
	}
	return nil
}

func (s *fakeAuthStore) LookupReplacedSession(_ context.Context, digest TokenDigest) (SessionIdentity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.sessions[digest]
	return identity, ok && identity.Session.Status == SessionStatusRevoked && s.replacedBy[identity.Session.ID] != "", nil
}

func (s *fakeAuthStore) LookupPasswordChange(_ context.Context, lookup PasswordChangeLookup) (SessionIdentity, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identity, ok := s.passwordChanges[lookup.SessionID+"\x00"+lookup.IdempotencyKey+"\x00"+lookup.RequestHash]
	return identity, ok, nil
}

func (s *fakeAuthStore) CommitPasswordChange(_ context.Context, commit PasswordChangeCommit) (SessionIdentity, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return SessionIdentity{}, s.commitErr
	}
	var credentials UserCredentials
	for username, candidate := range s.users {
		if candidate.User.ID == commit.Actor.UserID {
			candidate.PasswordHash = commit.PasswordHash
			candidate.User.Version++
			s.users[username] = candidate
			credentials = candidate
		}
	}
	revokedAt := commit.OccurredAt
	for digest, identity := range s.sessions {
		if identity.User.ID == commit.Actor.UserID && identity.Session.Status == SessionStatusActive {
			identity.Session.Status = SessionStatusRevoked
			identity.Session.RevokedAt = &revokedAt
			s.sessions[digest] = identity
			s.replacedBy[identity.Session.ID] = commit.NewSession.ID
		}
	}
	identity := SessionIdentity{User: credentials.User, Session: commit.NewSession, CSRFDigest: commit.NewCSRFDigest}
	s.sessions[commit.NewTokenDigest] = identity
	s.passwordChanges[commit.PriorSessionID+"\x00"+commit.IdempotencyKey+"\x00"+commit.RequestHash] = identity
	s.passwordCommits = append(s.passwordCommits, commit)
	return identity, nil
}

func (s *fakeAuthStore) RevokeCurrentSession(_ context.Context, revocation CurrentSessionRevocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.commitErr != nil {
		return s.commitErr
	}
	if identity, ok := s.sessions[revocation.TokenDigest]; ok && identity.Session.Status == SessionStatusActive {
		identity.Session.Status = SessionStatusRevoked
		identity.Session.RevokedAt = &revocation.RevokedAt
		s.sessions[revocation.TokenDigest] = identity
	}
	s.revocations = append(s.revocations, revocation)
	return nil
}

func (s *fakeAuthStore) userLookupsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.userLookups...)
}

func (s *fakeAuthStore) loginCommitsSnapshot() []LoginCommit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]LoginCommit(nil), s.loginCommits...)
}

func (s *fakeAuthStore) touchesSnapshot() []SessionTouch {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SessionTouch(nil), s.touches...)
}

type recordingRandomReader struct {
	readSizes []int
	next      byte
}

func (r *recordingRandomReader) Read(p []byte) (int, error) {
	r.readSizes = append(r.readSizes, len(p))
	r.next++
	for i := range p {
		p[i] = r.next
	}
	return len(p), nil
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("random source unavailable")
}
