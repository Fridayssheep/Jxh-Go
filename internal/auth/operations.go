package auth

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

const (
	passwordChangeRequestDomain = "jxh-admin/password-change-request/v1"
	passwordChangeSessionDomain = "jxh-admin/password-change-session/v1"
	passwordChangeCSRFDomain    = "jxh-admin/password-change-csrf/v1"
)

type CurrentSessionRevocation struct {
	Actor       Principal
	Context     MutationContext
	SessionID   string
	TokenDigest TokenDigest
	RevokedAt   time.Time
}

type PasswordChangeLookup struct {
	UserID         string
	SessionID      string
	IdempotencyKey string
	RequestHash    string
}

type PasswordChangeCommit struct {
	Actor               Principal
	Context             MutationContext
	IdempotencyKey      string
	RequestHash         string
	ExpectedUserVersion uint64
	PasswordHash        string
	PriorSessionID      string
	PriorTokenDigest    TokenDigest
	NewSession          Session
	NewTokenDigest      TokenDigest
	NewCSRFDigest       TokenDigest
	OccurredAt          time.Time
}

type ChangePasswordInput struct {
	Credential      string
	CurrentPassword string
	NewPassword     string
	IdempotencyKey  string
	Context         MutationContext
}

func (s *Service) Logout(ctx context.Context, credential string, identity AuthContext, request MutationContext) error {
	sessionToken, _, ok := parseSessionCredential(credential)
	if !ok || identity.User.ID == "" || identity.Session.ID == "" || identity.Session.UserID != identity.User.ID ||
		deriveSessionID(s.sessionSecret, sessionToken) != identity.Session.ID || !validMutationContext(request) {
		return ErrUnauthenticated
	}
	err := s.store.RevokeCurrentSession(ctx, CurrentSessionRevocation{
		Actor:   Principal{UserID: identity.User.ID, SessionID: identity.Session.ID, Role: identity.User.Role},
		Context: request, SessionID: identity.Session.ID, TokenDigest: digestSessionToken(s.sessionSecret, sessionToken), RevokedAt: s.now(),
	})
	if err != nil {
		return fmt.Errorf("revoke current admin session: %w", err)
	}
	return nil
}

func (s *Service) AuthenticateForRotation(ctx context.Context, credential string) (AuthContext, error) {
	sessionToken, csrfToken, ok := parseSessionCredential(credential)
	if !ok {
		return AuthContext{}, ErrUnauthenticated
	}
	identity, found, err := s.store.LookupReplacedSession(ctx, digestSessionToken(s.sessionSecret, sessionToken))
	if err != nil {
		return AuthContext{}, fmt.Errorf("lookup replaced admin session: %w", err)
	}
	csrfDigest := digestCSRFToken(s.sessionSecret, csrfToken)
	if !found || !identity.User.Enabled || identity.Session.UserID != identity.User.ID ||
		identity.Session.Status != SessionStatusRevoked || identity.Session.RevokedAt == nil ||
		!hmac.Equal(identity.CSRFDigest[:], csrfDigest[:]) {
		return AuthContext{}, ErrUnauthenticated
	}
	identity.Session.Current = false
	return AuthContext{
		User: identity.User, Session: identity.Session, Permissions: PermissionsFor(identity.User.Role), CSRFToken: csrfToken,
	}, nil
}

func (s *Service) ChangePassword(ctx context.Context, identity AuthContext, input ChangePasswordInput) (LoginResult, error) {
	limiterIdentity := "change:" + identity.User.ID
	now := s.now()
	if err := s.limiter.Check(limiterIdentity, input.Context.IPAddress, now); err != nil {
		return LoginResult{}, err
	}
	if identity.User.ID == "" || identity.Session.ID == "" || identity.Session.UserID != identity.User.ID ||
		!validPassword(input.CurrentPassword) || !validPassword(input.NewPassword) ||
		!validIdempotencyKey(input.IdempotencyKey) || !validMutationContext(input.Context) {
		s.limiter.RecordFailure(limiterIdentity, input.Context.IPAddress, now)
		return LoginResult{}, ErrInvalidCredentials
	}
	priorToken, _, ok := parseSessionCredential(input.Credential)
	if !ok || deriveSessionID(s.sessionSecret, priorToken) != identity.Session.ID {
		s.limiter.RecordFailure(limiterIdentity, input.Context.IPAddress, now)
		return LoginResult{}, ErrUnauthenticated
	}
	requestHash := s.passwordChangeRequestHash(identity.User.ID, identity.Session.ID, input.CurrentPassword, input.NewPassword)
	lookup := PasswordChangeLookup{
		UserID: identity.User.ID, SessionID: identity.Session.ID, IdempotencyKey: input.IdempotencyKey, RequestHash: requestHash,
	}
	replayed, found, err := s.store.LookupPasswordChange(ctx, lookup)
	if err != nil {
		return LoginResult{}, fmt.Errorf("lookup password change replay: %w", err)
	}
	sessionToken, csrfToken := s.passwordChangeTokens(lookup)
	if found {
		result, err := s.passwordChangeResult(replayed, sessionToken, csrfToken)
		if err != nil {
			return LoginResult{}, err
		}
		s.limiter.RecordSuccess(limiterIdentity, now)
		return result, nil
	}

	currentPassword := []byte(input.CurrentPassword)
	newPassword := []byte(input.NewPassword)
	defer clear(currentPassword)
	defer clear(newPassword)
	credentials, found, err := s.store.LookupUserByID(ctx, identity.User.ID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("lookup password change user: %w", err)
	}
	if !found || !credentials.User.Enabled || credentials.User.ID != identity.User.ID {
		s.limiter.RecordFailure(limiterIdentity, input.Context.IPAddress, now)
		return LoginResult{}, ErrInvalidCredentials
	}
	matched, _, err := s.passwords.Verify(currentPassword, credentials.PasswordHash)
	if err != nil || !matched {
		s.limiter.RecordFailure(limiterIdentity, input.Context.IPAddress, now)
		return LoginResult{}, ErrInvalidCredentials
	}
	passwordHash, err := s.passwords.Hash(newPassword)
	if err != nil {
		return LoginResult{}, fmt.Errorf("hash changed password: %w", err)
	}
	credential, err := composeSessionCredential(sessionToken, csrfToken)
	if err != nil {
		return LoginResult{}, err
	}
	absoluteExpiresAt := now.Add(s.absoluteTTL)
	expiresAt := now.Add(s.idleTTL)
	if absoluteExpiresAt.Before(expiresAt) {
		expiresAt = absoluteExpiresAt
	}
	newSession := Session{
		ID: deriveSessionID(s.sessionSecret, sessionToken), UserID: identity.User.ID, Status: SessionStatusActive, Current: true,
		IPAddress: input.Context.IPAddress, UserAgent: input.Context.UserAgent, CreatedAt: now, LastSeenAt: now,
		ExpiresAt: expiresAt, AbsoluteExpiresAt: absoluteExpiresAt,
	}
	committed, err := s.store.CommitPasswordChange(ctx, PasswordChangeCommit{
		Actor:   Principal{UserID: identity.User.ID, SessionID: identity.Session.ID, Role: identity.User.Role},
		Context: input.Context, IdempotencyKey: input.IdempotencyKey, RequestHash: requestHash,
		ExpectedUserVersion: credentials.User.Version, PasswordHash: passwordHash,
		PriorSessionID: identity.Session.ID, PriorTokenDigest: digestSessionToken(s.sessionSecret, priorToken),
		NewSession: newSession, NewTokenDigest: digestSessionToken(s.sessionSecret, sessionToken),
		NewCSRFDigest: digestCSRFToken(s.sessionSecret, csrfToken), OccurredAt: now,
	})
	if err != nil {
		return LoginResult{}, fmt.Errorf("commit password change: %w", err)
	}
	result, err := s.passwordChangeResult(committed, sessionToken, csrfToken)
	if err != nil {
		return LoginResult{}, err
	}
	result.SessionToken = credential
	s.limiter.RecordSuccess(limiterIdentity, now)
	return result, nil
}

func (s *Service) passwordChangeResult(identity SessionIdentity, sessionToken, csrfToken string) (LoginResult, error) {
	expectedSessionID := deriveSessionID(s.sessionSecret, sessionToken)
	expectedCSRF := digestCSRFToken(s.sessionSecret, csrfToken)
	if !identity.User.Enabled || identity.Session.ID != expectedSessionID || identity.Session.UserID != identity.User.ID ||
		identity.Session.Status != SessionStatusActive || !hmac.Equal(identity.CSRFDigest[:], expectedCSRF[:]) {
		return LoginResult{}, errors.New("invalid password change store result")
	}
	credential, err := composeSessionCredential(sessionToken, csrfToken)
	if err != nil {
		return LoginResult{}, err
	}
	identity.Session.Current = true
	return LoginResult{
		AuthContext: AuthContext{
			User: identity.User, Session: identity.Session, Permissions: PermissionsFor(identity.User.Role), CSRFToken: csrfToken,
		},
		SessionToken: credential,
	}, nil
}

func (s *Service) passwordChangeRequestHash(userID, sessionID, currentPassword, newPassword string) string {
	mac := hmac.New(sha256.New, s.sessionSecret)
	writeHMACPart(mac, passwordChangeRequestDomain)
	writeHMACPart(mac, userID)
	writeHMACPart(mac, sessionID)
	writeHMACPart(mac, currentPassword)
	writeHMACPart(mac, newPassword)
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *Service) passwordChangeTokens(lookup PasswordChangeLookup) (string, string) {
	return s.passwordChangeToken(passwordChangeSessionDomain, lookup), s.passwordChangeToken(passwordChangeCSRFDomain, lookup)
}

func (s *Service) passwordChangeToken(domain string, lookup PasswordChangeLookup) string {
	mac := hmac.New(sha256.New, s.sessionSecret)
	writeHMACPart(mac, domain)
	writeHMACPart(mac, lookup.UserID)
	writeHMACPart(mac, lookup.SessionID)
	writeHMACPart(mac, lookup.IdempotencyKey)
	writeHMACPart(mac, lookup.RequestHash)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func writeHMACPart(mac interface{ Write([]byte) (int, error) }, value string) {
	_, _ = mac.Write([]byte{byte(len(value) >> 24), byte(len(value) >> 16), byte(len(value) >> 8), byte(len(value))})
	_, _ = mac.Write([]byte(value))
}
