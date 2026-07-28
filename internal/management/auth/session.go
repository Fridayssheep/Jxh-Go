package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"sync"
)

var ErrSessionRandomness = errors.New("session randomness unavailable")

const (
	sessionTokenBytes        = 32
	sessionCredentialVersion = 1
	sessionCredentialBytes   = 1 + 2*sessionTokenBytes
	sessionTokenDomain       = "jxh-admin/session-token/v1"
	csrfTokenDomain          = "jxh-admin/csrf-token/v1"
	sessionIDDomain          = "jxh-admin/session-id/v1"
)

type TokenDigest [sha256.Size]byte

type sessionTokenGenerator struct {
	mu     sync.Mutex
	random io.Reader
}

func newSessionTokenGenerator(random io.Reader) *sessionTokenGenerator {
	return &sessionTokenGenerator{random: random}
}

func (g *sessionTokenGenerator) newPair() (string, string, error) {
	if g == nil || g.random == nil {
		return "", "", ErrSessionRandomness
	}
	var sessionBytes [sessionTokenBytes]byte
	var csrfBytes [sessionTokenBytes]byte
	g.mu.Lock()
	_, sessionErr := io.ReadFull(g.random, sessionBytes[:])
	_, csrfErr := io.ReadFull(g.random, csrfBytes[:])
	g.mu.Unlock()
	if sessionErr != nil || csrfErr != nil {
		return "", "", ErrSessionRandomness
	}
	if sessionBytes == csrfBytes {
		return "", "", ErrSessionRandomness
	}
	return base64.RawURLEncoding.EncodeToString(sessionBytes[:]),
		base64.RawURLEncoding.EncodeToString(csrfBytes[:]), nil
}

func digestSessionToken(secret []byte, token string) TokenDigest {
	return digestAuthToken(secret, sessionTokenDomain, token)
}

func digestCSRFToken(secret []byte, token string) TokenDigest {
	return digestAuthToken(secret, csrfTokenDomain, token)
}

func deriveSessionID(secret []byte, token string) string {
	digest := digestAuthToken(secret, sessionIDDomain, token)
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func composeSessionCredential(sessionToken, csrfToken string) (string, error) {
	sessionBytes, err := base64.RawURLEncoding.DecodeString(sessionToken)
	if err != nil || len(sessionBytes) != sessionTokenBytes {
		return "", ErrSessionRandomness
	}
	csrfBytes, err := base64.RawURLEncoding.DecodeString(csrfToken)
	if err != nil || len(csrfBytes) != sessionTokenBytes {
		return "", ErrSessionRandomness
	}
	credential := make([]byte, 0, sessionCredentialBytes)
	credential = append(credential, sessionCredentialVersion)
	credential = append(credential, sessionBytes...)
	credential = append(credential, csrfBytes...)
	return base64.RawURLEncoding.EncodeToString(credential), nil
}

func parseSessionCredential(credential string) (string, string, bool) {
	if len(credential) != base64.RawURLEncoding.EncodedLen(sessionCredentialBytes) {
		return "", "", false
	}
	var decoded [sessionCredentialBytes]byte
	n, err := base64.RawURLEncoding.Decode(decoded[:], []byte(credential))
	if err != nil || n != sessionCredentialBytes || decoded[0] != sessionCredentialVersion {
		return "", "", false
	}
	return base64.RawURLEncoding.EncodeToString(decoded[1 : 1+sessionTokenBytes]),
		base64.RawURLEncoding.EncodeToString(decoded[1+sessionTokenBytes:]), true
}

func digestAuthToken(secret []byte, domain, token string) TokenDigest {
	digest := hmac.New(sha256.New, secret)
	_, _ = digest.Write([]byte(domain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(token))
	var result TokenDigest
	copy(result[:], digest.Sum(nil))
	return result
}
