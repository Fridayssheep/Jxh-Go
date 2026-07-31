package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const editorTestConfig = `app:
  timezone: "Asia/Shanghai"
admin:
  addr: "127.0.0.1:8090"
  session_secret: "deployment-session-secret"
onebot:
  ws_url: "ws://127.0.0.1:3001"
  access_token: "deployment-onebot-secret"
wps:
  share_url: "https://example.test/knowledge.xlsx?token=seed-secret"
  sid: "wps-seed-secret"
  sheet: "release"
  cache_file: "./unique/deployment-cache.xlsx"
  timeout_sec: 120
database:
  host: "unique-database-host"
  password: "deployment-database-secret"
  max_open_conns: 73
  max_idle_conns: 19
  trigger_log_retention_days: 180
ai:
  enabled: true
  provider: "openai"
  base_url: "https://api.example.test/v1"
  api_key: "ai-seed-secret"
  model: "seed-model"
  timeout_sec: 30
  max_question_chars: 500
quote:
  base_url: "http://quote:5000"
  timeout_sec: 10
scheduler:
  timezone: "Asia/Shanghai"
`

func TestFileEditorReadsOnlyEffectiveEditableSettings(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	t.Setenv("JXH_WPS_SID", "environment-wps-secret")
	t.Setenv("JXH_AI_MODEL", "environment-model")
	editor := mustFileEditor(t, path)

	settings, err := editor.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if settings.WPS.ShareURL.Configured != true || settings.WPS.ShareURL.Source != SourceFile {
		t.Fatalf("share URL state = %#v", settings.WPS.ShareURL)
	}
	if settings.WPS.SID.Configured != true || settings.WPS.SID.Source != SourceEnvironment {
		t.Fatalf("SID state = %#v", settings.WPS.SID)
	}
	if settings.AI.APIKey.Configured != true || settings.AI.APIKey.Source != SourceFile {
		t.Fatalf("API key state = %#v", settings.AI.APIKey)
	}
	if settings.AI.Model != "environment-model" {
		t.Fatalf("AI model = %q", settings.AI.Model)
	}
	if got, want := settings.EnvironmentOverrides, []string{"ai.model", "wps.sid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("environment overrides = %v, want %v", got, want)
	}
	if settings.Version == 0 || settings.Version > maxJavaScriptSafeInteger {
		t.Fatalf("version = %d", settings.Version)
	}
	if text := settingsForLeakCheck(settings); strings.Contains(text, "seed-secret") || strings.Contains(text, "environment-wps-secret") {
		t.Fatalf("settings leaked a secret: %s", text)
	}
}

func TestFileEditorPatchesAllowedPathsAndPreservesDeploymentConfig(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	editor := mustFileEditor(t, path)
	before, err := editor.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	patch := SettingsPatch{
		WPS:       &WPSSettingsPatch{Sheet: ptr(" next "), TimeoutSec: ptr(45)},
		AI:        &AISettingsPatch{Provider: ptr("ark"), BaseURL: ptr("https://ark.example.test/v1"), Model: ptr("next-model"), TimeoutSec: ptr(60), MaxQuestionChars: ptr(900)},
		Quote:     &QuoteSettingsPatch{BaseURL: ptr("https://quote.example.test"), TimeoutSec: ptr(20)},
		Time:      &TimeSettingsPatch{AppTimezone: ptr("Asia/Tokyo"), SchedulerTimezone: ptr("UTC")},
		Retention: &RetentionSettingsPatch{TriggerLogRetentionDays: ptr(365)},
	}
	after, changed, err := editor.Update(t.Context(), before.Version, patch)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version == before.Version {
		t.Fatal("version did not change")
	}
	if after.WPS.Sheet != "next" || after.AI.Provider != "ark" || after.Time.SchedulerTimezone != "UTC" {
		t.Fatalf("updated settings = %#v", after)
	}
	if len(changed) != 12 || !sortStringsAreStrict(changed) {
		t.Fatalf("changed fields = %v", changed)
	}

	var raw map[string]any
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(contents, &raw); err != nil {
		t.Fatal(err)
	}
	assertYAMLValue(t, raw, "admin", "session_secret", "deployment-session-secret")
	assertYAMLValue(t, raw, "onebot", "access_token", "deployment-onebot-secret")
	assertYAMLValue(t, raw, "database", "host", "unique-database-host")
	assertYAMLValue(t, raw, "database", "max_open_conns", 73)
	assertYAMLValue(t, raw, "database", "max_idle_conns", 19)
	assertYAMLValue(t, raw, "wps", "cache_file", "./unique/deployment-cache.xlsx")
	assertYAMLValue(t, raw, "wps", "sid", "wps-seed-secret")
	assertYAMLValue(t, raw, "ai", "enabled", true)
}

func TestFileEditorRejectsEnvironmentManagedFields(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	t.Setenv("JXH_AI_MODEL", "environment-model")
	t.Setenv("JXH_WPS_SID", "environment-secret")
	editor := mustFileEditor(t, path)
	settings, err := editor.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = editor.Update(t.Context(), settings.Version, SettingsPatch{
		WPS: &WPSSettingsPatch{SID: &SecretUpdate{Operation: SecretReplace, Value: "replacement"}},
		AI:  &AISettingsPatch{Model: ptr("replacement")},
	})
	var managed *ManagedFieldsError
	if !errors.As(err, &managed) || !reflect.DeepEqual(managed.Fields, []string{"ai.model", "wps.sid"}) {
		t.Fatalf("error = %#v, want sorted managed fields", err)
	}
}

func TestFileEditorSecretKeepReplaceAndClear(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	editor := mustFileEditor(t, path)
	before, err := editor.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	updated, changed, err := editor.Update(t.Context(), before.Version, SettingsPatch{
		WPS: &WPSSettingsPatch{
			ShareURL: &SecretUpdate{Operation: SecretReplace, Value: "https://new.example.test/sheet?token=next"},
			SID:      &SecretUpdate{Operation: SecretClear},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.WPS.ShareURL.Configured || updated.WPS.SID.Configured {
		t.Fatalf("secret states = %#v %#v", updated.WPS.ShareURL, updated.WPS.SID)
	}
	if !reflect.DeepEqual(changed, []string{"wps.share_url", "wps.sid"}) {
		t.Fatalf("changed fields = %v", changed)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if strings.Contains(text, "wps-seed-secret") || strings.Contains(text, "token=seed-secret") {
		t.Fatal("old WPS secrets were retained")
	}
	if !strings.Contains(text, "token=next") {
		t.Fatal("replacement secret was not persisted")
	}
	if strings.Contains(settingsForLeakCheck(updated), "token=next") {
		t.Fatal("replacement secret leaked in response")
	}

	kept, changed, err := editor.Update(t.Context(), updated.Version, SettingsPatch{WPS: &WPSSettingsPatch{Sheet: ptr("other")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || !kept.WPS.ShareURL.Configured {
		t.Fatalf("implicit keep failed: changed=%v settings=%#v", changed, kept.WPS)
	}
}

func TestFileEditorValidatesURLsTimezonesEnumsAndRanges(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	tests := []struct {
		name  string
		patch SettingsPatch
		path  string
	}{
		{"WPS URL", SettingsPatch{WPS: &WPSSettingsPatch{ShareURL: &SecretUpdate{Operation: SecretReplace, Value: "ftp://example.test/a"}}}, "wps.share_url"},
		{"AI URL userinfo", SettingsPatch{AI: &AISettingsPatch{BaseURL: ptr("https://user:pass@example.test")}}, "ai.base_url"},
		{"provider", SettingsPatch{AI: &AISettingsPatch{Provider: ptr("unknown")}}, "ai.provider"},
		{"timezone", SettingsPatch{Time: &TimeSettingsPatch{AppTimezone: ptr("Mars/Olympus")}}, "app.timezone"},
		{"WPS timeout", SettingsPatch{WPS: &WPSSettingsPatch{TimeoutSec: ptr(601)}}, "wps.timeout_sec"},
		{"retention", SettingsPatch{Retention: &RetentionSettingsPatch{TriggerLogRetentionDays: ptr(-1)}}, "database.trigger_log_retention_days"},
		{"empty secret replacement", SettingsPatch{AI: &AISettingsPatch{APIKey: &SecretUpdate{Operation: SecretReplace}}}, "ai.api_key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			editor := mustFileEditor(t, path)
			settings, err := editor.Read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = editor.Update(t.Context(), settings.Version, test.patch)
			var validation *ValidationError
			if !errors.As(err, &validation) || !validation.HasPath(test.path) {
				t.Fatalf("error = %#v, want validation for %s", err, test.path)
			}
		})
	}
}

func TestFileEditorDetectsVersionConflictAcrossInstances(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	first := mustFileEditor(t, path)
	second := mustFileEditor(t, path)
	settings, err := first.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := first.Update(t.Context(), settings.Version, SettingsPatch{WPS: &WPSSettingsPatch{Sheet: ptr("first")}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := second.Update(t.Context(), settings.Version, SettingsPatch{WPS: &WPSSettingsPatch{Sheet: ptr("second")}}); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("error = %v, want version conflict", err)
	}
}

func TestFileEditorRejectsEmptyPatch(t *testing.T) {
	path := writeEditorTestConfig(t, editorTestConfig)
	editor := mustFileEditor(t, path)
	settings, err := editor.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := editor.Update(t.Context(), settings.Version, SettingsPatch{}); !errors.Is(err, ErrEmptyPatch) {
		t.Fatalf("error = %v, want empty patch", err)
	}
}

func mustFileEditor(t *testing.T, path string) *FileEditor {
	t.Helper()
	editor, err := NewFileEditor(path)
	if err != nil {
		t.Fatal(err)
	}
	return editor
}

func writeEditorTestConfig(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func ptr[T any](value T) *T { return &value }

func settingsForLeakCheck(settings Settings) string {
	return strings.Join([]string{
		settings.WPS.Sheet,
		settings.AI.Provider,
		settings.AI.BaseURL,
		settings.AI.Model,
		settings.Quote.BaseURL,
		settings.Time.AppTimezone,
		settings.Time.SchedulerTimezone,
		string(settings.WPS.ShareURL.Source),
		string(settings.WPS.SID.Source),
		string(settings.AI.APIKey.Source),
	}, "|")
}

func sortStringsAreStrict(values []string) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

func assertYAMLValue(t *testing.T, raw map[string]any, section, key string, want any) {
	t.Helper()
	sectionValue, ok := raw[section].(map[string]any)
	if !ok {
		t.Fatalf("section %s = %#v", section, raw[section])
	}
	if got := sectionValue[key]; !reflect.DeepEqual(got, want) {
		t.Fatalf("%s.%s = %#v, want %#v", section, key, got, want)
	}
}
