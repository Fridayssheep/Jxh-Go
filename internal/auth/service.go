package auth

import (
	"context"
	"crypto/hmac"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidCredentials = errors.New("invalid credentials")
	ErrUnauthenticated    = errors.New("unauthenticated")
)

const dummyPasswordPHC = "$argon2id$v=19$m=65536,t=3,p=2$AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

const sessionTouchInterval = time.Minute

const (
	maxLoginUsernameBytes  = 256
	maxLoginPasswordBytes  = 4096
	maxClientIPCharacters  = 64
	maxUserAgentCharacters = 300
)

type PasswordEngine interface {
	Hash(password []byte) (string, error)
	Verify(password []byte, encoded string) (matched bool, upgrade bool, err error)
}

type UserCredentials struct {
	User         User
	PasswordHash string
}

type SessionIdentity struct {
	User       User
	Session    Session
	CSRFDigest TokenDigest
}

type PasswordHashUpdate struct {
	UserID          string
	ExpectedVersion uint64
	PasswordHash    string
}

type LoginCommit struct {
	Session            Session
	TokenDigest        TokenDigest
	CSRFDigest         TokenDigest
	PasswordHashUpdate *PasswordHashUpdate
	// PriorTokenDigest, when set, may only revoke a session owned by Session.UserID.
	PriorTokenDigest *TokenDigest
}

type SessionTouch struct {
	SessionID        string
	IfLastSeenBefore time.Time
	LastSeenAt       time.Time
	ExpiresAt        time.Time
}

type Store interface {
	LookupUserByUsername(ctx context.Context, normalizedUsername string) (UserCredentials, bool, error)
	LookupUserByID(ctx context.Context, userID string) (UserCredentials, bool, error)
	// CommitLogin persists the new session and applies optional password/rotation changes atomically.
	CommitLogin(ctx context.Context, commit LoginCommit) (SessionIdentity, error)
	LookupSession(ctx context.Context, tokenDigest TokenDigest) (SessionIdentity, bool, error)
	// TouchSessionIfStale updates only an active row whose last_seen_at is not after IfLastSeenBefore.
	TouchSessionIfStale(ctx context.Context, touch SessionTouch) error
	LookupReplacedSession(ctx context.Context, tokenDigest TokenDigest) (SessionIdentity, bool, error)
	LookupPasswordChange(ctx context.Context, lookup PasswordChangeLookup) (SessionIdentity, bool, error)
	CommitPasswordChange(ctx context.Context, commit PasswordChangeCommit) (SessionIdentity, error)
	RevokeCurrentSession(ctx context.Context, revocation CurrentSessionRevocation) error
}

type LoginRequest struct {
	Username        string
	Password        string
	ClientIP        string
	UserAgent       string
	PriorCredential string
}

type AuthContext struct {
	User        User
	Session     Session
	Permissions []Permission
	CSRFToken   string
}

type LoginResult struct {
	AuthContext
	SessionToken string
}

type ServiceOptions struct {
	Store         Store
	Passwords     PasswordEngine
	Limiter       *LoginLimiter
	SessionSecret []byte
	Random        io.Reader
	AbsoluteTTL   time.Duration
	IdleTTL       time.Duration
	Now           func() time.Time
}

type Service struct {
	store         Store
	passwords     PasswordEngine
	limiter       *LoginLimiter
	sessionSecret []byte
	tokens        *sessionTokenGenerator
	absoluteTTL   time.Duration
	idleTTL       time.Duration
	now           func() time.Time
}

func NewService(opts ServiceOptions) (*Service, error) {
	if opts.Store == nil || opts.Passwords == nil || opts.Limiter == nil || len(opts.SessionSecret) < 32 ||
		opts.Random == nil || opts.AbsoluteTTL <= 0 || opts.IdleTTL <= 0 || opts.Now == nil {
		return nil, ErrInvalidAuthConfig
	}
	return &Service{
		store:         opts.Store,
		passwords:     opts.Passwords,
		limiter:       opts.Limiter,
		sessionSecret: append([]byte(nil), opts.SessionSecret...),
		tokens:        newSessionTokenGenerator(opts.Random),
		absoluteTTL:   opts.AbsoluteTTL,
		idleTTL:       opts.IdleTTL,
		now:           opts.Now,
	}, nil
}

func (s *Service) Login(ctx context.Context, request LoginRequest) (LoginResult, error) {
	now := s.now()
	usernameValid := validUTF8Bytes(request.Username, maxLoginUsernameBytes)
	passwordValid := validUTF8Bytes(request.Password, maxLoginPasswordBytes)
	clientIPValid := validUTF8Characters(request.ClientIP, maxClientIPCharacters)
	metadataValid := validUTF8Characters(request.UserAgent, maxUserAgentCharacters)
	username := ""
	if usernameValid {
		username = normalizeUsername(request.Username)
	}
	clientIP := ""
	if clientIPValid {
		clientIP = request.ClientIP
	}
	if err := s.limiter.Check(username, clientIP, now); err != nil {
		return LoginResult{}, err
	}
	password := []byte("invalid login input")
	if passwordValid {
		password = []byte(request.Password)
	}
	defer clear(password)
	if !usernameValid || !passwordValid || !clientIPValid || !metadataValid {
		_, _, _ = s.passwords.Verify(password, dummyPasswordPHC)
		s.limiter.RecordFailure(username, clientIP, now)
		return LoginResult{}, ErrInvalidCredentials
	}

	credentials, found, err := s.store.LookupUserByUsername(ctx, username)
	if err != nil {
		return LoginResult{}, fmt.Errorf("lookup login user: %w", err)
	}
	if !found || !credentials.User.Enabled {
		_, _, _ = s.passwords.Verify(password, dummyPasswordPHC)
		s.limiter.RecordFailure(username, clientIP, now)
		return LoginResult{}, ErrInvalidCredentials
	}

	matched, upgrade, err := s.passwords.Verify(password, credentials.PasswordHash)
	if err != nil || !matched {
		s.limiter.RecordFailure(username, clientIP, now)
		return LoginResult{}, ErrInvalidCredentials
	}
	var passwordHashUpdate *PasswordHashUpdate
	if upgrade {
		upgradedHash, err := s.passwords.Hash(password)
		if err != nil {
			return LoginResult{}, fmt.Errorf("upgrade password hash: %w", err)
		}
		passwordHashUpdate = &PasswordHashUpdate{
			UserID:          credentials.User.ID,
			ExpectedVersion: credentials.User.Version,
			PasswordHash:    upgradedHash,
		}
	}
	var priorTokenDigest *TokenDigest
	if priorSessionToken, _, ok := parseSessionCredential(request.PriorCredential); ok {
		digest := digestSessionToken(s.sessionSecret, priorSessionToken)
		priorTokenDigest = &digest
	}

	sessionToken, csrfToken, err := s.tokens.newPair()
	if err != nil {
		return LoginResult{}, err
	}
	sessionCredential, err := composeSessionCredential(sessionToken, csrfToken)
	if err != nil {
		return LoginResult{}, err
	}
	absoluteExpiresAt := now.Add(s.absoluteTTL)
	expiresAt := now.Add(s.idleTTL)
	if absoluteExpiresAt.Before(expiresAt) {
		expiresAt = absoluteExpiresAt
	}
	session := Session{
		ID:                deriveSessionID(s.sessionSecret, sessionToken),
		UserID:            credentials.User.ID,
		Status:            SessionStatusActive,
		Current:           true,
		IPAddress:         request.ClientIP,
		UserAgent:         request.UserAgent,
		CreatedAt:         now,
		LastSeenAt:        now,
		ExpiresAt:         expiresAt,
		AbsoluteExpiresAt: absoluteExpiresAt,
	}
	commit := LoginCommit{
		Session:            session,
		TokenDigest:        digestSessionToken(s.sessionSecret, sessionToken),
		CSRFDigest:         digestCSRFToken(s.sessionSecret, csrfToken),
		PasswordHashUpdate: passwordHashUpdate,
		PriorTokenDigest:   priorTokenDigest,
	}
	committed, err := s.store.CommitLogin(ctx, commit)
	if err != nil {
		return LoginResult{}, fmt.Errorf("commit login: %w", err)
	}
	if !committed.User.Enabled || committed.User.ID != credentials.User.ID || committed.Session.ID != session.ID ||
		committed.Session.UserID != committed.User.ID || committed.Session.Status != SessionStatusActive ||
		committed.Session.RevokedAt != nil || !hmac.Equal(committed.CSRFDigest[:], commit.CSRFDigest[:]) {
		return LoginResult{}, errors.New("invalid login store result")
	}
	committed.Session.Current = true
	s.limiter.RecordSuccess(username, now)
	return LoginResult{
		AuthContext: AuthContext{
			User:        committed.User,
			Session:     committed.Session,
			Permissions: PermissionsFor(committed.User.Role),
			CSRFToken:   csrfToken,
		},
		SessionToken: sessionCredential,
	}, nil
}

func validUTF8Bytes(value string, maxBytes int) bool {
	return len(value) <= maxBytes && utf8.ValidString(value)
}

func validUTF8Characters(value string, maxCharacters int) bool {
	return len(value) <= maxCharacters*utf8.UTFMax && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxCharacters
}

func (s *Service) Authenticate(ctx context.Context, sessionToken string) (AuthContext, error) {
	return s.authenticate(ctx, sessionToken, true)
}

// AuthenticatePassive validates the current database state without extending
// idle expiry. Long-lived streams use it to reauthorize in place.
func (s *Service) AuthenticatePassive(ctx context.Context, sessionToken string) (AuthContext, error) {
	return s.authenticate(ctx, sessionToken, false)
}

func (s *Service) authenticate(ctx context.Context, sessionToken string, touch bool) (AuthContext, error) {
	sessionToken, csrfToken, ok := parseSessionCredential(sessionToken)
	if !ok {
		return AuthContext{}, ErrUnauthenticated
	}
	identity, found, err := s.store.LookupSession(ctx, digestSessionToken(s.sessionSecret, sessionToken))
	if err != nil {
		return AuthContext{}, fmt.Errorf("lookup authenticated session: %w", err)
	}
	now := s.now()
	if !found || !identity.User.Enabled || identity.Session.UserID != identity.User.ID ||
		identity.Session.Status != SessionStatusActive || identity.Session.RevokedAt != nil ||
		!identity.Session.ExpiresAt.After(now) || !identity.Session.AbsoluteExpiresAt.After(now) {
		return AuthContext{}, ErrUnauthenticated
	}
	csrfDigest := digestCSRFToken(s.sessionSecret, csrfToken)
	if !hmac.Equal(identity.CSRFDigest[:], csrfDigest[:]) {
		return AuthContext{}, ErrUnauthenticated
	}
	if touch && !now.Before(identity.Session.LastSeenAt.Add(sessionTouchInterval)) {
		expiresAt := now.Add(s.idleTTL)
		if identity.Session.AbsoluteExpiresAt.Before(expiresAt) {
			expiresAt = identity.Session.AbsoluteExpiresAt
		}
		touch := SessionTouch{
			SessionID:        identity.Session.ID,
			IfLastSeenBefore: now.Add(-sessionTouchInterval),
			LastSeenAt:       now,
			ExpiresAt:        expiresAt,
		}
		if err := s.store.TouchSessionIfStale(ctx, touch); err != nil {
			return AuthContext{}, fmt.Errorf("touch authenticated session: %w", err)
		}
		identity.Session.LastSeenAt = now
		identity.Session.ExpiresAt = expiresAt
	}
	identity.Session.Current = true
	return AuthContext{
		User:        identity.User,
		Session:     identity.Session,
		Permissions: PermissionsFor(identity.User.Role),
		CSRFToken:   csrfToken,
	}, nil
}
