package auth

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidPasswordHash   = errors.New("invalid password hash")
	ErrInvalidPasswordParams = errors.New("invalid password hashing parameters")
	ErrPasswordRandomness    = errors.New("password hashing randomness unavailable")
)

const (
	maxPasswordMemoryKiB  = 256 * 1024
	maxPasswordIterations = 10
	maxPasswordParallel   = 8
	minPasswordSaltBytes  = 16
	maxPasswordSaltBytes  = 32
	minPasswordKeyBytes   = 16
	maxPasswordKeyBytes   = 64
	maxPasswordPHCLength  = 512
)

type PasswordParams struct {
	MemoryKiB   uint32
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

func DefaultPasswordParams() PasswordParams {
	return PasswordParams{
		MemoryKiB:   64 * 1024,
		Iterations:  3,
		Parallelism: 2,
		SaltLength:  16,
		KeyLength:   32,
	}
}

type PasswordHasher struct {
	params PasswordParams
	random io.Reader
	mu     sync.Mutex
}

func NewPasswordHasher(params PasswordParams, random io.Reader) *PasswordHasher {
	return &PasswordHasher{params: params, random: random}
}

func (h *PasswordHasher) Hash(password []byte) (string, error) {
	if h == nil || !validPasswordParams(h.params) {
		return "", ErrInvalidPasswordParams
	}
	if h.random == nil {
		return "", ErrPasswordRandomness
	}
	salt := make([]byte, h.params.SaltLength)
	h.mu.Lock()
	_, err := io.ReadFull(h.random, salt)
	h.mu.Unlock()
	if err != nil {
		return "", fmt.Errorf("%w", ErrPasswordRandomness)
	}
	key := argon2.IDKey(password, salt, h.params.Iterations, h.params.MemoryKiB, h.params.Parallelism, h.params.KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version,
		h.params.MemoryKiB,
		h.params.Iterations,
		h.params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func (h *PasswordHasher) Verify(password []byte, encoded string) (bool, bool, error) {
	if h == nil || !validPasswordParams(h.params) {
		return false, false, ErrInvalidPasswordParams
	}
	params, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, false, err
	}
	actual := argon2.IDKey(password, salt, params.Iterations, params.MemoryKiB, params.Parallelism, params.KeyLength)
	matched := subtle.ConstantTimeCompare(actual, expected) == 1
	return matched, matched && params != h.params, nil
}

func parsePasswordHash(encoded string) (PasswordParams, []byte, []byte, error) {
	if len(encoded) == 0 || len(encoded) > maxPasswordPHCLength {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v=19" {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params, err := parsePasswordParams(parts[3])
	if err != nil {
		return PasswordParams{}, nil, nil, err
	}
	if len(parts[4]) > base64.RawStdEncoding.EncodedLen(maxPasswordSaltBytes) ||
		len(parts[5]) > base64.RawStdEncoding.EncodedLen(maxPasswordKeyBytes) {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < minPasswordSaltBytes || len(salt) > maxPasswordSaltBytes {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) < minPasswordKeyBytes || len(expected) > maxPasswordKeyBytes {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	params.SaltLength = uint32(len(salt))
	params.KeyLength = uint32(len(expected))
	if !validPasswordParams(params) {
		return PasswordParams{}, nil, nil, ErrInvalidPasswordHash
	}
	return params, salt, expected, nil
}

func parsePasswordParams(encoded string) (PasswordParams, error) {
	parts := strings.Split(encoded, ",")
	if len(parts) != 3 {
		return PasswordParams{}, ErrInvalidPasswordHash
	}
	memory, err := parsePHCUnsigned(parts[0], "m=", 32)
	if err != nil || memory == 0 || memory > maxPasswordMemoryKiB {
		return PasswordParams{}, ErrInvalidPasswordHash
	}
	iterations, err := parsePHCUnsigned(parts[1], "t=", 32)
	if err != nil || iterations == 0 || iterations > maxPasswordIterations {
		return PasswordParams{}, ErrInvalidPasswordHash
	}
	parallelism, err := parsePHCUnsigned(parts[2], "p=", 8)
	if err != nil || parallelism == 0 || parallelism > maxPasswordParallel {
		return PasswordParams{}, ErrInvalidPasswordHash
	}
	params := PasswordParams{
		MemoryKiB:   uint32(memory),
		Iterations:  uint32(iterations),
		Parallelism: uint8(parallelism),
	}
	if params.MemoryKiB < 8*uint32(params.Parallelism) {
		return PasswordParams{}, ErrInvalidPasswordHash
	}
	return params, nil
}

func parsePHCUnsigned(value, prefix string, bits int) (uint64, error) {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, ErrInvalidPasswordHash
	}
	parsed, err := strconv.ParseUint(value[len(prefix):], 10, bits)
	if err != nil {
		return 0, ErrInvalidPasswordHash
	}
	return parsed, nil
}

func validPasswordParams(params PasswordParams) bool {
	return params.MemoryKiB >= 8*uint32(params.Parallelism) &&
		params.MemoryKiB <= maxPasswordMemoryKiB &&
		params.Iterations >= 1 && params.Iterations <= maxPasswordIterations &&
		params.Parallelism >= 1 && params.Parallelism <= maxPasswordParallel &&
		params.SaltLength >= minPasswordSaltBytes && params.SaltLength <= maxPasswordSaltBytes &&
		params.KeyLength >= minPasswordKeyBytes && params.KeyLength <= maxPasswordKeyBytes
}
