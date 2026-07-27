package audit

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/auth"
)

func TestObserverAuditRedactionRemovesSecurityValues(t *testing.T) {
	original := Log{
		Target: Target{Type: "admin_session", ID: "session-1"},
		Before: map[string]any{
			"token_digest": "secret-digest",
			"enabled":      true,
			"nested": map[string]any{
				"display_name": "Root Admin",
			},
		},
	}
	got := RedactForRole(original, auth.RoleObserver)
	if got.Before["token_digest"] != RedactedValue || got.Before["enabled"] != RedactedValue {
		t.Fatalf("observer Before = %#v", got.Before)
	}
	nested, ok := got.Before["nested"].(map[string]any)
	if !ok || nested["display_name"] != RedactedValue || !got.Redacted {
		t.Fatalf("observer nested/redacted = %#v/%v", got.Before["nested"], got.Redacted)
	}
	if original.Before["enabled"] != true {
		t.Fatal("RedactForRole mutated its input")
	}
}

func TestObserverRedactsEveryFieldForSensitiveTargets(t *testing.T) {
	for _, targetType := range []string{
		"admin_user",
		"admin_session",
		"security_policy",
		"cross_group_action",
		"system_operation",
	} {
		t.Run(targetType, func(t *testing.T) {
			log := Log{
				Target: Target{Type: targetType},
				Before: map[string]any{
					"enabled": true,
					"nested":  map[string]any{"display_name": "Root Admin"},
				},
				After: map[string]any{
					"role":  "maintainer",
					"items": []any{1, "visible without role redaction"},
				},
				Metadata: map[string]any{"request_kind": "administrative"},
			}

			got := RedactForRole(log, auth.RoleObserver)
			assertEveryLeafRedacted(t, got.Before)
			assertEveryLeafRedacted(t, got.After)
			assertEveryLeafRedacted(t, got.Metadata)
			if !got.Redacted {
				t.Fatal("observer-sensitive audit was not marked redacted")
			}
		})
	}
}

func assertEveryLeafRedacted(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			assertEveryLeafRedacted(t, child)
		}
	case []any:
		for _, child := range typed {
			assertEveryLeafRedacted(t, child)
		}
	default:
		if typed != RedactedValue {
			t.Fatalf("unredacted observer leaf = %#v", typed)
		}
	}
}

func TestAllRolesAlwaysRedactDenylistedKeysAndValues(t *testing.T) {
	for _, role := range []auth.Role{auth.RoleObserver, auth.RoleMaintainer, auth.RoleSuperAdmin} {
		log := Log{
			Target: Target{Type: "settings", ID: "global"},
			Before: map[string]any{
				"password": "cleartext",
				"nested": []any{
					map[string]any{"session_token": "raw-token"},
					"Bearer should-never-escape",
				},
				"enabled": true,
			},
		}
		got := RedactForRole(log, role)
		if got.Before["password"] != RedactedValue || got.Before["enabled"] != true || !got.Redacted {
			t.Fatalf("role %s Before = %#v redacted=%v", role, got.Before, got.Redacted)
		}
		nested := got.Before["nested"].([]any)
		if nested[0].(map[string]any)["session_token"] != RedactedValue || nested[1] != RedactedValue {
			t.Fatalf("role %s nested = %#v", role, nested)
		}
	}
}

func TestAllRolesApplyCompleteAuditDenylist(t *testing.T) {
	denylistedKeys := []string{
		"password",
		"password_hash",
		"session_token",
		"csrfToken",
		"token_digest",
		"client_secret",
		"api-key",
		"authorization",
		"cookie_header",
		"verification_message",
		"upstream_raw_body",
		"raw_response",
		"wps_sid",
		"database_dsn",
	}
	for _, role := range []auth.Role{auth.RoleObserver, auth.RoleMaintainer, auth.RoleSuperAdmin} {
		for _, key := range denylistedKeys {
			got := RedactForRole(Log{
				Target: Target{Type: "settings"},
				Before: map[string]any{key: "raw-sensitive-value", "safe": "visible"},
			}, role)
			if got.Before[key] != RedactedValue || got.Before["safe"] != "visible" || !got.Redacted {
				t.Fatalf("role=%s key=%s before=%#v redacted=%v", role, key, got.Before, got.Redacted)
			}
		}
	}
}

func TestRedactionBoundsStringsAndRejectsUnserializableValues(t *testing.T) {
	log := Log{
		Target: Target{Type: "settings"},
		Before: map[string]any{
			"description": strings.Repeat("x", maxAuditValueString+50),
			"invalid":     func() {},
		},
	}
	got := RedactForRole(log, auth.RoleSuperAdmin)
	if len(got.Before["description"].(string)) != maxAuditValueString {
		t.Fatalf("description length = %d", len(got.Before["description"].(string)))
	}
	if got.Before["invalid"] != RedactedValue || !got.Redacted {
		t.Fatalf("invalid value = %#v redacted=%v", got.Before["invalid"], got.Redacted)
	}
}

func TestRedactionDeepCopiesRecursiveValuesAndOptionalStrings(t *testing.T) {
	userID := "user-1"
	qqUserID := "10001"
	errorCode := "safe_error"
	ipAddress := "192.0.2.10"
	userAgent := "test browser"
	original := Log{
		Actor:     Actor{UserID: &userID, QQUserID: &qqUserID},
		ErrorCode: &errorCode,
		IPAddress: &ipAddress,
		UserAgent: &userAgent,
		Before: map[string]any{
			"nested": map[string]any{
				"plain": "original",
				"items": []any{map[string]any{"value": "original"}},
			},
		},
	}

	got := RedactForRole(original, auth.RoleSuperAdmin)
	*got.Actor.UserID = "changed"
	*got.Actor.QQUserID = "changed"
	*got.ErrorCode = "changed"
	*got.IPAddress = "changed"
	*got.UserAgent = "changed"
	nested := got.Before["nested"].(map[string]any)
	nested["plain"] = "changed"
	nested["items"].([]any)[0].(map[string]any)["value"] = "changed"

	if *original.Actor.UserID != "user-1" || *original.Actor.QQUserID != "10001" ||
		*original.ErrorCode != "safe_error" || *original.IPAddress != "192.0.2.10" || *original.UserAgent != "test browser" {
		t.Fatalf("optional strings were aliased: %+v", original)
	}
	originalNested := original.Before["nested"].(map[string]any)
	if originalNested["plain"] != "original" || originalNested["items"].([]any)[0].(map[string]any)["value"] != "original" {
		t.Fatalf("recursive values were aliased: %#v", original.Before)
	}
}

func TestRedactionEnforcesSchemaStringAndStructureBounds(t *testing.T) {
	errorCode := strings.Repeat("界", 101)
	log := Log{
		ID:        strings.Repeat("界", 257),
		Action:    strings.Repeat("界", 101),
		ErrorCode: &errorCode,
		Before:    map[string]any{"unicode": strings.Repeat("界", maxAuditValueString+50)},
	}

	got := RedactForRole(log, auth.RoleSuperAdmin)
	if utf8.RuneCountInString(got.ID) != 256 || utf8.RuneCountInString(got.Action) != 100 ||
		got.ErrorCode == nil || utf8.RuneCountInString(*got.ErrorCode) != 100 {
		t.Fatalf("schema strings were not bounded: id=%d action=%d error=%v",
			utf8.RuneCountInString(got.ID), utf8.RuneCountInString(got.Action), got.ErrorCode)
	}
	if value := got.Before["unicode"].(string); utf8.RuneCountInString(value) != maxAuditValueString {
		t.Fatalf("unicode audit value length = %d", utf8.RuneCountInString(value))
	}

	oversized := make(map[string]any, 600)
	for index := range 600 {
		oversized[string(rune(0x4e00+index))] = strings.Repeat("界", maxAuditValueString)
	}
	deep := any("leaf")
	for range 80 {
		deep = map[string]any{"next": deep}
	}
	structured := RedactForRole(Log{Before: map[string]any{"oversized": oversized, "deep": deep}}, auth.RoleSuperAdmin)
	encoded, err := json.Marshal(structured.Before)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > maxAuditObjectBytes {
		t.Fatalf("redacted object bytes = %d, want <= %d", len(encoded), maxAuditObjectBytes)
	}
	if depth := auditValueDepth(structured.Before["deep"]); depth > 32 {
		t.Fatalf("redacted object depth = %d, want <= 32", depth)
	}
	if !got.Redacted || !structured.Redacted {
		t.Fatal("bounded audit was not marked redacted")
	}
}

func auditValueDepth(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		maxChild := 0
		for _, child := range typed {
			if depth := auditValueDepth(child); depth > maxChild {
				maxChild = depth
			}
		}
		return 1 + maxChild
	case []any:
		maxChild := 0
		for _, child := range typed {
			if depth := auditValueDepth(child); depth > maxChild {
				maxChild = depth
			}
		}
		return 1 + maxChild
	default:
		return 1
	}
}

func TestServiceAuthorizesBeforeStoreAndRedactsDetail(t *testing.T) {
	store := &fakeStore{detail: Log{
		ID:       "audit-1",
		Target:   Target{Type: "admin_user", ID: "user-1"},
		Before:   map[string]any{"role": "super_admin"},
		Metadata: map[string]any{},
	}}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Get(t.Context(), auth.Principal{}, "audit-1"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Get() error = %v, want ErrForbidden", err)
	}
	if store.getCalls != 0 {
		t.Fatalf("unauthorized store calls = %d", store.getCalls)
	}
	if _, err := service.List(t.Context(), auth.Principal{}, ListQuery{}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("List() error = %v, want ErrForbidden", err)
	}
	if store.listCalls != 0 {
		t.Fatalf("unauthorized list store calls = %d", store.listCalls)
	}
	got, err := service.Get(t.Context(), auth.Principal{Role: auth.RoleObserver}, "audit-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Before["role"] != RedactedValue || store.getCalls != 1 {
		t.Fatalf("Get() = %#v calls=%d", got.Before, store.getCalls)
	}
}

func TestServiceListReturnsDeepCopiedBoundedSummaries(t *testing.T) {
	userID := "user-1"
	errorCode := strings.Repeat("e", 101)
	store := &fakeStore{page: Page{Items: []Summary{{
		ID:        strings.Repeat("i", 257),
		Actor:     Actor{UserID: &userID, DisplayName: strings.Repeat("d", 101)},
		Action:    strings.Repeat("a", 101),
		Target:    Target{Type: strings.Repeat("t", 65), ID: strings.Repeat("x", 257), DisplayName: strings.Repeat("n", 201)},
		ErrorCode: &errorCode,
		RequestID: strings.Repeat("r", 257),
	}}}}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}

	page, err := service.List(t.Context(), auth.Principal{Role: auth.RoleObserver}, ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	item := &page.Items[0]
	if len(item.ID) != 256 || len(item.Action) != 100 || len(item.Target.Type) != 64 ||
		len(item.Target.ID) != 256 || len(item.Target.DisplayName) != 200 || len(item.RequestID) != 256 ||
		len(item.Actor.DisplayName) != 100 || item.ErrorCode == nil || len(*item.ErrorCode) != 100 {
		t.Fatalf("summary was not schema-bounded: %+v", *item)
	}
	*item.Actor.UserID = "changed"
	*item.ErrorCode = "changed"
	if *store.page.Items[0].Actor.UserID != "user-1" || *store.page.Items[0].ErrorCode != strings.Repeat("e", 101) {
		t.Fatal("List() returned Store-owned pointers")
	}
}

func TestServiceValidatesListQueryBeforeStore(t *testing.T) {
	store := &fakeStore{}
	service, err := NewService(store)
	if err != nil {
		t.Fatal(err)
	}
	from := time.Unix(20, 0)
	to := time.Unix(10, 0)
	_, err = service.List(t.Context(), auth.Principal{Role: auth.RoleMaintainer}, ListQuery{From: &from, To: &to, Limit: 50})
	if !errors.Is(err, ErrInvalidQuery) || store.listCalls != 0 {
		t.Fatalf("List() error=%v calls=%d", err, store.listCalls)
	}
}

type fakeStore struct {
	detail    Log
	page      Page
	getErr    error
	listErr   error
	getCalls  int
	listCalls int
}

func (s *fakeStore) GetAuditLog(_ context.Context, id string) (Log, bool, error) {
	s.getCalls++
	if s.getErr != nil {
		return Log{}, false, s.getErr
	}
	return s.detail, s.detail.ID == id, nil
}

func (s *fakeStore) ListAuditLogs(_ context.Context, _ ListQuery) (Page, error) {
	s.listCalls++
	return s.page, s.listErr
}
