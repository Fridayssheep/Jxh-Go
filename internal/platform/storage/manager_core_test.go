package storage

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/groups"
	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/overview"
	"github.com/zjutjh/jxh-go/internal/management/settings"
	managersystem "github.com/zjutjh/jxh-go/internal/management/system"
)

func TestManagerStoreInterfaceSatisfaction(t *testing.T) {
	var store *Store
	var _ overview.Store = store
	var _ groups.Store = store
	var _ settings.Store = store
	var _ managersystem.Store = store
}

func TestManagerGlobalSettingsDocumentDefaultsAndRoundTrip(t *testing.T) {
	defaults, err := decodeManagerFeatures([]byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if defaults != settings.DefaultFeatures() {
		t.Fatalf("empty global document = %+v, want defaults", defaults)
	}

	want := settings.DefaultFeatures()
	want.KeywordReply.Enabled = false
	want.AIQA.Enabled = false
	want.Welcome.Enabled = false
	want.Welcome.MessageTemplate = "Welcome {{member_qq}} to {{group_id}}"
	encoded, err := encodeManagerFeatures(want)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		`"keyword_reply"`, `"ai_qa"`, `"quote"`, `"link_cleaner"`, `"welcome"`, `"custom_commands"`, `"message_template"`,
	} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("encoded global document missing %s: %s", key, encoded)
		}
	}
	got, err := decodeManagerFeatures(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
}

func TestManagerSettingsDocumentRejectsNonObjectsAndIncompleteBasicSettings(t *testing.T) {
	for _, document := range []string{`[]`, `null`, `{"keyword_reply":{}}`} {
		if _, err := decodeManagerFeatures([]byte(document)); err == nil {
			t.Fatalf("decodeManagerFeatures(%s) succeeded", document)
		}
	}
	if _, err := decodeManagerOverrides([]byte(`{"custom_commands":{}}`)); err == nil {
		t.Fatal("incomplete basic override was accepted")
	}
}

func TestManagerOverridesRoundTripPatchAndResolution(t *testing.T) {
	disabled := false
	template := "Hi {{member_qq}}"
	want := settings.Overrides{
		KeywordReply: &settings.BasicOverride{Enabled: false},
		Welcome:      &settings.WelcomeOverride{Enabled: &disabled, MessageTemplate: &template},
	}
	encoded, err := encodeManagerOverrides(want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "ai_qa") || !strings.Contains(string(encoded), "message_template") {
		t.Fatalf("group override document is not sparse: %s", encoded)
	}
	got, err := decodeManagerOverrides(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.KeywordReply == nil || got.KeywordReply.Enabled || got.Welcome == nil || got.Welcome.Enabled == nil ||
		*got.Welcome.Enabled || got.Welcome.MessageTemplate == nil || *got.Welcome.MessageTemplate != template {
		t.Fatalf("decoded overrides = %+v", got)
	}

	global := settings.DefaultFeatures()
	effective := resolveManagerFeatures(global, got)
	if effective.KeywordReply.Enabled || effective.Welcome.Enabled || effective.Welcome.MessageTemplate != template || !effective.AIQA.Enabled {
		t.Fatalf("effective settings = %+v", effective)
	}

	patch := settings.GroupPatch{
		KeywordReply: settings.OverrideFeaturePatch{Set: true, Clear: true},
		Welcome: settings.OverrideFeaturePatch{
			Set: true, Enabled: auth.Field[*bool]{Set: true, Value: nil},
			MessageTemplate: auth.Field[*string]{Set: true, Value: nil},
		},
	}
	got = applyManagerGroupPatch(got, patch)
	if !managerOverridesEmpty(got) {
		t.Fatalf("clearing the final fields left overrides: %+v", got)
	}
}

func TestManagerGlobalPatchChangesOnlySelectedFields(t *testing.T) {
	features := settings.DefaultFeatures()
	patch := settings.GlobalPatch{
		AIQA: auth.Field[settings.BasicPatch]{
			Set: true, Value: settings.BasicPatch{Enabled: auth.Field[bool]{Set: true, Value: false}},
		},
		Welcome: auth.Field[settings.WelcomePatch]{
			Set: true, Value: settings.WelcomePatch{MessageTemplate: auth.Field[string]{Set: true, Value: "Hello {{member_qq}}"}},
		},
	}
	got := applyManagerGlobalPatch(features, patch)
	if got.AIQA.Enabled || !got.KeywordReply.Enabled || !got.Welcome.Enabled || got.Welcome.MessageTemplate != "Hello {{member_qq}}" {
		t.Fatalf("patched global settings = %+v", got)
	}
}

func TestManagerGroupFeaturesHaveStableOrderAndSources(t *testing.T) {
	disabled := false
	overrides := settings.Overrides{
		AIQA:    &settings.BasicOverride{Enabled: false},
		Welcome: &settings.WelcomeOverride{Enabled: &disabled},
	}
	features := managerGroupFeatures(settings.DefaultFeatures(), overrides)
	wantKeys := []groups.FeatureKey{
		groups.FeatureKeywordReply, groups.FeatureAIQA, groups.FeatureQuote, groups.FeatureLinkCleaner,
		groups.FeatureWelcome, groups.FeatureCustomCommand,
	}
	if len(features) != len(wantKeys) {
		t.Fatalf("feature count = %d", len(features))
	}
	for index, key := range wantKeys {
		if features[index].Key != key {
			t.Fatalf("feature %d = %s, want %s", index, features[index].Key, key)
		}
	}
	if features[0].Source != groups.FeatureGlobal || features[1].Source != groups.FeatureGroupOverride ||
		features[1].Enabled || features[4].Source != groups.FeatureGroupOverride || features[4].Enabled {
		t.Fatalf("features = %+v", features)
	}
}

func TestManagerGroupMappingAppliesComputedStaleness(t *testing.T) {
	lastSynced := time.Date(2026, 7, 28, 1, 0, 0, 0, time.FixedZone("local", 8*60*60))
	model := managerManagedGroup{
		GroupID: 123, Name: "Alpha", MemberCount: 10, MaxMemberCount: 100, BotRole: string(groups.RoleAdmin),
		SnapshotState: string(groups.SnapshotFresh), LastSyncedAt: &lastSynced,
	}
	group, err := managerGroupFromModel(model, settings.DefaultFeatures(), settings.Overrides{}, false, lastSynced.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if group.SnapshotState != groups.SnapshotStale || group.LastSyncedAt.Location() != time.UTC || len(group.Features) != 6 {
		t.Fatalf("group = %+v", group)
	}
}

func TestManagerSyncResultMetadataRoundTrip(t *testing.T) {
	want := groups.SyncResult{
		SyncedAt:   time.Date(2026, 7, 28, 2, 3, 4, 5_000_000, time.UTC),
		AddedCount: 1, UpdatedCount: 2, RemovedCount: 3, TotalCount: 4,
	}
	encoded, err := json.Marshal(managerSyncMetadataFromResult("completed", want, ""))
	if err != nil {
		t.Fatal(err)
	}
	var decoded managerSyncAuditMetadata
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	got := groups.SyncResult{
		SyncedAt: decoded.SyncedAt.UTC(), AddedCount: decoded.AddedCount, UpdatedCount: decoded.UpdatedCount,
		RemovedCount: decoded.RemovedCount, TotalCount: decoded.TotalCount,
	}
	if decoded.Phase != "completed" || decoded.SyncedAt == nil || got != want {
		t.Fatalf("metadata = %+v, result = %+v", decoded, got)
	}
}

func TestManagerDailyMetricValuesAndRestartTransitions(t *testing.T) {
	if got := managerDailyMetricValue(managerDailyMetric{MetricKey: "command_run_count", ValueCount: 7}); got != 7 {
		t.Fatalf("count metric = %v", got)
	}
	if got := managerDailyMetricValue(managerDailyMetric{MetricKey: "ai_success_rate", ValueCount: 3, SampleCount: 4}); got != 75 {
		t.Fatalf("success rate = %v", got)
	}
	if !managerValidRestartTransition(managersystem.StatusAccepted, managersystem.StatusRunning) ||
		!managerValidRestartTransition(managersystem.StatusRunning, managersystem.StatusSucceeded) ||
		managerValidRestartTransition(managersystem.StatusAccepted, managersystem.StatusSucceeded) {
		t.Fatal("restart transition graph is invalid")
	}
}

func TestManagerOpaqueIDsAndGroupSyncHash(t *testing.T) {
	first, err := newManagerID("aud")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newManagerID("aud")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !strings.HasPrefix(first, "aud_") || len(first) > 64 {
		t.Fatalf("generated ids = %q, %q", first, second)
	}
	hash := managerGroupSyncRequestHash()
	if len(hash) != 64 || hash != managerGroupSyncRequestHash() {
		t.Fatalf("group sync request hash = %q", hash)
	}
}
