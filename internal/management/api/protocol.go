package adminapi

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidIfMatch        = errors.New("invalid If-Match version")
	ErrInvalidLimit          = errors.New("invalid pagination limit")
	ErrInvalidIdempotencyKey = errors.New("invalid idempotency key")
	ErrInvalidTimestamp      = errors.New("invalid UTC timestamp")
	ErrInvalidIdentifier     = errors.New("invalid opaque identifier")
)

var idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func ParseIfMatch(value string) (uint64, error) {
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' {
		return 0, ErrInvalidIfMatch
	}
	raw := value[1 : len(value)-1]
	if raw == "" || raw[0] == '0' {
		return 0, ErrInvalidIfMatch
	}
	for _, character := range raw {
		if character < '0' || character > '9' {
			return 0, ErrInvalidIfMatch
		}
	}
	version, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || version == 0 {
		return 0, ErrInvalidIfMatch
	}
	return version, nil
}

func ParseIfMatchIncludingZero(value string) (uint64, error) {
	if value == `"0"` {
		return 0, nil
	}
	return ParseIfMatch(value)
}

func ParseLimit(value string) (int, error) {
	if value == "" {
		return 50, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil || limit < 1 || limit > 100 {
		return 0, ErrInvalidLimit
	}
	return limit, nil
}

func ParseIdempotencyKey(value string) (string, error) {
	if !idempotencyKeyPattern.MatchString(value) {
		return "", ErrInvalidIdempotencyKey
	}
	return value, nil
}

func ParseUTCTimestamp(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") && !strings.HasSuffix(value, "+00:00") {
		return time.Time{}, ErrInvalidTimestamp
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, ErrInvalidTimestamp
	}
	return parsed.UTC(), nil
}

func ValidateOpaqueID(value string) (string, error) {
	if !utf8.ValidString(value) {
		return "", ErrInvalidIdentifier
	}
	length := utf8.RuneCountInString(value)
	if length < 1 || length > 256 {
		return "", ErrInvalidIdentifier
	}
	return value, nil
}
