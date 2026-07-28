package storage

import (
	"reflect"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/automation/customcommand"
	"github.com/zjutjh/jxh-go/internal/automation/scheduledjobs"
	"github.com/zjutjh/jxh-go/internal/management/analytics"
	"github.com/zjutjh/jxh-go/internal/platform/telemetry"
)

func TestManagerCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 7, 28, 3, 4, 5, 678_000_000, time.UTC)
	encoded := encodeManagerCursor(at, "row_42")
	cursor, err := decodeManagerCursor(encoded)
	if err != nil {
		t.Fatalf("decodeManagerCursor() error = %v", err)
	}
	if cursor.Millis != at.UnixMilli() || cursor.ID != "row_42" {
		t.Fatalf("cursor = %+v", cursor)
	}
	for _, invalid := range []string{"", "not-base64", "e30"} {
		if _, err := decodeManagerCursor(invalid); err == nil {
			t.Fatalf("decodeManagerCursor(%q) error = nil", invalid)
		}
	}
}

func TestScheduledJobTypeAndOnceScheduleRoundTrip(t *testing.T) {
	databaseType, err := scheduledTypeToDatabase(scheduledjobs.TypeOnce)
	if err != nil || databaseType != databaseOnceJob {
		t.Fatalf("scheduledTypeToDatabase() = %q, %v", databaseType, err)
	}
	if jobType, err := scheduledTypeFromDatabase(databaseType); err != nil || jobType != scheduledjobs.TypeOnce {
		t.Fatalf("scheduledTypeFromDatabase() = %q, %v", jobType, err)
	}
	runAt := time.Date(2026, 8, 1, 1, 30, 0, 0, time.UTC)
	clock, runDate, err := scheduleColumns(scheduledjobs.Schedule{
		Type: scheduledjobs.TypeOnce, Timezone: "Asia/Shanghai", RunAt: &runAt,
	})
	if err != nil {
		t.Fatalf("scheduleColumns() error = %v", err)
	}
	if clock != "09:30" || runDate == nil || runDate.Format("2006-01-02") != "2026-08-01" {
		t.Fatalf("schedule columns = %q, %v", clock, runDate)
	}
}

func TestCustomCommandJSONRoundTrip(t *testing.T) {
	definition := customcommand.Definition{
		Name: "/welcome", DisplayName: "Welcome", Description: "Greets a member",
		Scope:             customcommand.Scope{Type: customcommand.ScopeGroups, GroupIDs: []string{"10001", "10002"}},
		TriggerPermission: customcommand.TriggerGroupAdmin,
		Parameters: []customcommand.Parameter{{
			Name: "member", DisplayName: "Member", Type: customcommand.ParameterMember, Required: true,
		}},
		Actions: []customcommand.Action{{
			Type: customcommand.ActionMention, Target: customcommand.MentionParameter, MemberParameter: "member",
		}},
	}
	scope, parameters, actions, err := commandJSON(definition)
	if err != nil {
		t.Fatalf("commandJSON() error = %v", err)
	}
	row := customCommandManagerRow{
		Name: definition.Name, DisplayName: definition.DisplayName, Description: definition.Description,
		ScopeType: string(definition.Scope.Type), ScopeJSON: scope,
		TriggerPermission: string(definition.TriggerPermission), ParametersJSON: parameters, ActionsJSON: actions,
	}
	got, err := commandDefinitionFromManagerRow(row)
	if err != nil {
		t.Fatalf("commandDefinitionFromManagerRow() error = %v", err)
	}
	if !reflect.DeepEqual(got, definition) {
		t.Fatalf("definition = %#v, want %#v", got, definition)
	}
}

func TestSameCommandRunPayloadUsesJSONSemantics(t *testing.T) {
	canonical := []byte(`{"version":1,"argument_summaries":[{"Name":"text","Type":"text","Present":true,"RuneLength":5,"Digest":"digest"}],"action_steps":[{"Index":0,"Type":"reply_text","Result":"success","Duration":20000000}]}`)
	normalized := []byte(`{ "action_steps": [{"Duration": 20000000, "Index": 0, "Result": "success", "Type": "reply_text"}], "argument_summaries": [{"Digest": "digest", "Name": "text", "Present": true, "RuneLength": 5, "Type": "text"}], "version": 1 }`)
	different := []byte(`{"version":1,"argument_summaries":[],"action_steps":[]}`)

	if !sameCommandRunPayload(canonical, normalized) {
		t.Fatal("semantically equal JSON payloads did not match")
	}
	if sameCommandRunPayload(canonical, different) {
		t.Fatal("different JSON payloads matched")
	}
	if sameCommandRunPayload(canonical, []byte(`{"version":`)) {
		t.Fatal("malformed JSON payload matched")
	}
}

func TestTelemetryOutcomeNormalization(t *testing.T) {
	tests := map[telemetry.Result]string{
		telemetry.ResultSuccess:     string(analytics.ResultSuccess),
		telemetry.ResultDenied:      string(analytics.ResultDenied),
		telemetry.ResultNoKnowledge: string(analytics.ResultFallback),
		telemetry.ResultTimeout:     string(analytics.ResultUnknown),
		telemetry.ResultDisabled:    string(analytics.ResultSkipped),
		telemetry.ResultParseFailed: string(analytics.ResultFailed),
	}
	for input, want := range tests {
		if got := telemetryOutcome(input); got != want {
			t.Errorf("telemetryOutcome(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestAnalyticsMetricNumbersUseCountsAndDistinctActors(t *testing.T) {
	counted, err := opsMarshalJSON(telemetryMetadataPayload{Count: 3})
	if err != nil {
		t.Fatal(err)
	}
	success, failed := string(analytics.ResultSuccess), string(analytics.ResultFailed)
	actorA, actorB := "actor-a", "actor-b"
	duration100, duration400 := uint64(100), uint64(400)
	events := []telemetryEventManagerRow{
		{EventType: string(telemetry.EventAIRequest), Outcome: &success, DurationMS: &duration100, Metadata: counted},
		{EventType: string(telemetry.EventAIRequest), Outcome: &failed, DurationMS: &duration400},
		{EventType: string(telemetry.EventGroupMessage), ActorHash: &actorA},
		{EventType: string(telemetry.EventGroupMessage), ActorHash: &actorA},
		{EventType: string(telemetry.EventGroupMessage), ActorHash: &actorB},
	}
	if value, available := analyticsMetricNumber(events, analytics.MetricAIRequestCount); !available || value != 4 {
		t.Fatalf("AI request count = %v, %v", value, available)
	}
	if value, available := analyticsMetricNumber(events, analytics.MetricAISuccessRate); !available || value != 75 {
		t.Fatalf("AI success rate = %v, %v", value, available)
	}
	if value, available := analyticsMetricNumber(events, analytics.MetricAIDurationMS); !available || value != 175 {
		t.Fatalf("AI duration = %v, %v", value, available)
	}
	if value, available := analyticsMetricNumber(events, analytics.MetricActiveUserCount); !available || value != 2 {
		t.Fatalf("active users = %v, %v", value, available)
	}
}

func TestAnalyticsMetricNumbersCombineDailyAndRawBoundaries(t *testing.T) {
	success := string(analytics.ResultSuccess)
	duration := uint64(500)
	raw := []telemetryEventManagerRow{{
		EventType: string(telemetry.EventAIRequest), Outcome: &success, DurationMS: &duration,
	}}
	daily := []telemetryDailyManagerRow{
		{MetricKey: string(analytics.MetricAIRequestCount), ValueCount: 4, SampleCount: 4},
		{MetricKey: string(analytics.MetricAISuccessRate), ValueCount: 3, SampleCount: 4},
		{MetricKey: string(analytics.MetricAIDurationMS), ValueSum: 800, SampleCount: 4},
	}
	if value, available := analyticsMetricNumberCombined(raw, daily, analytics.MetricAIRequestCount); !available || value != 5 {
		t.Fatalf("combined AI request count = %v, %v", value, available)
	}
	if value, available := analyticsMetricNumberCombined(raw, daily, analytics.MetricAISuccessRate); !available || value != 80 {
		t.Fatalf("combined AI success rate = %v, %v", value, available)
	}
	if value, available := analyticsMetricNumberCombined(raw, daily, analytics.MetricAIDurationMS); !available || value != 260 {
		t.Fatalf("combined AI duration = %v, %v", value, available)
	}
}

func TestFullAnalyticsDayRangeUsesRequestedTimezone(t *testing.T) {
	filter := analytics.Filter{
		From: time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC), Timezone: "UTC",
	}
	start, end, ok := fullAnalyticsDayRange(filter)
	if !ok || !start.Equal(time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)) ||
		!end.Equal(time.Date(2026, 7, 22, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("range start=%v end=%v ok=%t", start, end, ok)
	}
}
