package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const editorTestConfig = `app:
  timezone: "Asia/Shanghai"
admin:
  addr: "127.0.0.1:8090"
  public_origin: "http://127.0.0.1:5173"
  session_secret: "session-secret-that-is-long-enough"
  cookie_secure: false
onebot:
  ws_url: "ws://127.0.0.1:3001"
  access_token: "onebot-secret"
wps:
  share_url: "https://example.test/knowledge.xlsx"
  sid: "wps-secret"
database:
  host: "127.0.0.1"
  port: 3306
  user: "jxh"
  password: "database-secret"
  name: "jxh_bot"
  dsn: "dsn-secret"
ai:
  enabled: true
  provider: "openai"
  api_key: "ai-secret"
  model: "test-model"
quote:
  base_url: "http://127.0.0.1:5000"
scheduler:
  timezone: "Asia/Shanghai"
`

func TestFileEditorReadMasksSecretsAndReportsEnvironmentOverrides(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	t.Setenv("JXH_AI_API_KEY", "environment-secret")
	t.Setenv("JXH_ONEBOT_WS_URL", "ws://environment.test:3001")
	editor, err := NewFileEditor(path)
	if err != nil {
		t.Fatal(err)
	}

	document, err := editor.Read()
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"session-secret-that-is-long-enough", "onebot-secret", "https://example.test/knowledge.xlsx", "wps-secret",
		"database-secret", "dsn-secret", "ai-secret", "environment-secret",
	} {
		if strings.Contains(document.YAML, secret) {
			t.Fatalf("editable YAML exposed secret %q", secret)
		}
	}
	if got, want := strings.Count(document.YAML, SecretPlaceholder), 7; got != want {
		t.Fatalf("secret placeholders = %d, want %d\n%s", got, want, document.YAML)
	}
	if got := strings.Join(document.MaskedFields, ","); got != "admin.session_secret,ai.api_key,database.dsn,database.password,onebot.access_token,wps.share_url,wps.sid" {
		t.Fatalf("masked fields = %q", got)
	}
	if got := strings.Join(document.EnvironmentOverrides, ","); got != "ai.api_key,onebot.ws_url" {
		t.Fatalf("environment overrides = %q", got)
	}
	if document.Version == 0 || document.Version > maxJavaScriptSafeInteger {
		t.Fatalf("version = %d, want a positive JavaScript-safe integer", document.Version)
	}
}

func TestFileEditorUpdatePreservesMaskedSecretsAndChangesVersion(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	editor, err := NewFileEditor(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := editor.Read()
	if err != nil {
		t.Fatal(err)
	}
	candidate := strings.Replace(before.YAML, "Asia/Shanghai", "Asia/Tokyo", 1)

	after, err := editor.Update(before.Version, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version == before.Version {
		t.Fatal("version did not change after a successful update")
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	if !strings.Contains(text, `timezone: Asia/Tokyo`) && !strings.Contains(text, `timezone: "Asia/Tokyo"`) {
		t.Fatalf("updated timezone missing from file:\n%s", text)
	}
	for _, secret := range []string{
		"session-secret-that-is-long-enough", "onebot-secret", "https://example.test/knowledge.xlsx", "wps-secret",
		"database-secret", "dsn-secret", "ai-secret",
	} {
		if !strings.Contains(text, secret) {
			t.Fatalf("secret %q was not preserved", secret)
		}
	}
	if strings.Contains(text, SecretPlaceholder) {
		t.Fatal("secret placeholder was persisted")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("config permissions = %o, want no group/other access", info.Mode().Perm())
	}
}

func TestFileEditorUpdateCanReplaceASecret(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	editor, err := NewFileEditor(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := editor.Read()
	if err != nil {
		t.Fatal(err)
	}
	candidate := strings.Replace(document.YAML, SecretPlaceholder, "replacement-session-secret-that-is-long-enough", 1)

	if _, err := editor.Update(document.Version, candidate); err != nil {
		t.Fatal(err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "session_secret: replacement-session-secret-that-is-long-enough") || strings.Contains(string(written), "session_secret: session-secret-that-is-long-enough") {
		t.Fatalf("session secret was not replaced:\n%s", written)
	}
}

func TestFileEditorUpdateDoesNotWidenOwnerReadOnlyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix owner permission bits")
	}
	path := writeEditorTestConfig(t, editorTestConfig)
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	editor, err := NewFileEditor(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := editor.Read()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := editor.Update(document.Version, document.YAML); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o400 {
		t.Fatalf("config permissions = %o, want 400", got)
	}
}

func TestRestrictedFileModePreservesOnlyOwnerAccess(t *testing.T) {
	for _, test := range []struct {
		original os.FileMode
		want     os.FileMode
	}{
		{0o644, 0o600},
		{0o640, 0o600},
		{0o600, 0o600},
		{0o400, 0o400},
	} {
		if got := restrictedFileMode(test.original); got != test.want {
			t.Fatalf("restrictedFileMode(%o) = %o, want %o", test.original, got, test.want)
		}
	}
}

func TestFileEditorRejectsStaleOrInvalidDocumentsWithoutChangingFile(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	editor, err := NewFileEditor(path)
	if err != nil {
		t.Fatal(err)
	}
	document, err := editor.Read()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := editor.Update(document.Version+1, document.YAML); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale update error = %v, want ErrVersionConflict", err)
	}
	invalid := document.YAML + "unknown_section:\n  value: true\n"
	if _, err := editor.Update(document.Version, invalid); !errors.Is(err, ErrInvalidDocument) {
		t.Fatalf("invalid update error = %v, want ErrInvalidDocument", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("rejected update changed the config file")
	}
}

func writeEditorTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
