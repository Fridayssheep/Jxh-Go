package adminapi

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestParseIfMatchRequiresQuotedPositiveVersion(t *testing.T) {
	version, err := ParseIfMatch(`"7"`)
	if err != nil || version != 7 {
		t.Fatalf("ParseIfMatch() = %d, %v", version, err)
	}
	for _, value := range []string{"", "7", `W/"7"`, `"0"`, `"01"`, `"-1"`, `"7", "8"`} {
		if _, err := ParseIfMatch(value); !errors.Is(err, ErrInvalidIfMatch) {
			t.Fatalf("ParseIfMatch(%q) error = %v, want ErrInvalidIfMatch", value, err)
		}
	}
}

func TestParseLimitUsesContractBounds(t *testing.T) {
	for raw, want := range map[string]int{"": 50, "1": 1, "100": 100} {
		got, err := ParseLimit(raw)
		if err != nil || got != want {
			t.Fatalf("ParseLimit(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
	for _, raw := range []string{"0", "101", "-1", "1.5", " 2"} {
		if _, err := ParseLimit(raw); !errors.Is(err, ErrInvalidLimit) {
			t.Fatalf("ParseLimit(%q) error = %v, want ErrInvalidLimit", raw, err)
		}
	}
}

func TestParseIdempotencyKeyMatchesOpenAPIPattern(t *testing.T) {
	key, err := ParseIdempotencyKey("retry-key_1:attempt.2")
	if err != nil || key != "retry-key_1:attempt.2" {
		t.Fatalf("ParseIdempotencyKey() = %q, %v", key, err)
	}
	for _, raw := range []string{"short", "contains space", strings.Repeat("x", 129), "含中文字符-key"} {
		if _, err := ParseIdempotencyKey(raw); !errors.Is(err, ErrInvalidIdempotencyKey) {
			t.Fatalf("ParseIdempotencyKey(%q) error = %v", raw, err)
		}
	}
}

func TestParseUTCTimestampRequiresRFC3339UTC(t *testing.T) {
	got, err := ParseUTCTimestamp("2026-07-27T12:34:56.123Z")
	if err != nil || !got.Equal(time.Date(2026, 7, 27, 12, 34, 56, 123000000, time.UTC)) {
		t.Fatalf("ParseUTCTimestamp() = %v, %v", got, err)
	}
	if got, err := ParseUTCTimestamp("2026-07-27T12:34:56+00:00"); err != nil || got.Location() != time.UTC {
		t.Fatalf("ParseUTCTimestamp(+00:00) = %v, %v", got, err)
	}
	for _, raw := range []string{"", "2026-07-27 12:34:56Z", "2026-07-27T20:34:56+08:00"} {
		if _, err := ParseUTCTimestamp(raw); !errors.Is(err, ErrInvalidTimestamp) {
			t.Fatalf("ParseUTCTimestamp(%q) error = %v, want ErrInvalidTimestamp", raw, err)
		}
	}
}

func TestValidateOpaqueIDPreservesStringIdentifiers(t *testing.T) {
	value := "001234567890123456789"
	if got, err := ValidateOpaqueID(value); err != nil || got != value {
		t.Fatalf("ValidateOpaqueID() = %q, %v", got, err)
	}
	for _, raw := range []string{"", strings.Repeat("界", 257), string([]byte{0xff})} {
		if _, err := ValidateOpaqueID(raw); !errors.Is(err, ErrInvalidIdentifier) {
			t.Fatalf("ValidateOpaqueID(%q) error = %v, want ErrInvalidIdentifier", raw, err)
		}
	}
}
