package settings

import (
	"sync"
	"testing"
	"time"
)

func TestRuntimeResolvesGlobalAndGroupSettings(t *testing.T) {
	runtime := NewDefaultRuntime()
	enabled := false
	template := "Welcome {{member_qq}} to {{group_id}}"
	overrides := Overrides{
		KeywordReply: &BasicOverride{Enabled: false},
		Welcome:      &WelcomeOverride{Enabled: &enabled, MessageTemplate: &template},
	}
	effective := resolve(DefaultFeatures(), overrides)
	if err := runtime.ApplyGroup(Group{
		GroupID: "123", Effective: effective, Overrides: overrides, GlobalVersion: 1, Version: 1,
		UpdatedAt: timePointer(time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)),
	}); err != nil {
		t.Fatal(err)
	}

	group := runtime.Effective("123")
	if group.KeywordReply.Enabled || group.Welcome.Enabled || group.Welcome.MessageTemplate != template {
		t.Fatalf("effective group settings = %+v", group)
	}
	if other := runtime.Effective("456"); !other.KeywordReply.Enabled || !other.Welcome.Enabled {
		t.Fatalf("inherited settings = %+v", other)
	}

	global := DefaultFeatures()
	global.AIQA.Enabled = false
	if err := runtime.ApplyGlobal(Global{
		Features: global, Version: 2, UpdatedAt: time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if runtime.Effective("123").AIQA.Enabled || runtime.Effective("456").AIQA.Enabled {
		t.Fatal("global update did not reach inherited group settings")
	}
	if runtime.Effective("123").KeywordReply.Enabled {
		t.Fatal("global update removed an unrelated group override")
	}

	if err := runtime.ApplyGroup(Group{
		GroupID: "123", Effective: global, GlobalVersion: 2, Version: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if !runtime.Effective("123").KeywordReply.Enabled {
		t.Fatal("zero-version group document did not restore inheritance")
	}
}

func TestRuntimeRejectsInconsistentDocumentsAndCopiesOverrides(t *testing.T) {
	runtime := NewDefaultRuntime()
	enabled := false
	overrides := Overrides{Welcome: &WelcomeOverride{Enabled: &enabled}}
	if err := runtime.ApplyGroup(Group{
		GroupID: "123", Effective: DefaultFeatures(), Overrides: overrides, GlobalVersion: 1, Version: 1,
		UpdatedAt: timePointer(time.Now().UTC()),
	}); err != ErrInvalidRuntime {
		t.Fatalf("inconsistent effective settings error = %v", err)
	}

	state := RuntimeState{
		Global: Global{Features: DefaultFeatures(), Version: 2, UpdatedAt: time.Now().UTC()},
		Groups: []RuntimeGroup{{GroupID: "123", Overrides: overrides, Version: 4}},
	}
	if err := runtime.Replace(state); err != nil {
		t.Fatal(err)
	}
	enabled = true
	if runtime.Effective("123").Welcome.Enabled {
		t.Fatal("runtime retained a caller-owned override pointer")
	}

	state.Groups = append(state.Groups, state.Groups[0])
	if err := runtime.Replace(state); err != ErrInvalidRuntime {
		t.Fatalf("duplicate group error = %v", err)
	}
	state.Groups = []RuntimeGroup{{GroupID: "456", Version: 1}}
	if err := runtime.Replace(state); err != ErrInvalidRuntime {
		t.Fatalf("empty override error = %v", err)
	}
}

func TestRuntimeConcurrentReadersObserveValidSnapshots(t *testing.T) {
	runtime := NewDefaultRuntime()
	var readers sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan string, 8)
	for index := 0; index < 8; index++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			<-start
			for iteration := 0; iteration < 2000; iteration++ {
				value := runtime.Effective("123")
				if value.Welcome.MessageTemplate == "" {
					errors <- "empty welcome template"
					return
				}
			}
		}()
	}
	close(start)
	for version := uint64(2); version <= 100; version++ {
		features := DefaultFeatures()
		features.AIQA.Enabled = version%2 == 0
		if err := runtime.ApplyGlobal(Global{Features: features, Version: version, UpdatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	readers.Wait()
	close(errors)
	for message := range errors {
		t.Error(message)
	}
}

func TestWelcomeTemplateValidationAndRendering(t *testing.T) {
	valid := []string{"hello", "{{member_qq}}", "group={{group_id}} member={{member_qq}}"}
	for _, value := range valid {
		if !validTemplate(value) {
			t.Errorf("validTemplate(%q) = false", value)
		}
	}
	invalid := []string{"", "{{member}}", "{{ member_qq }}", "{{member_qq}", "{{member_qq}}}}"}
	for _, value := range invalid {
		if validTemplate(value) {
			t.Errorf("validTemplate(%q) = true", value)
		}
	}
	if value := RenderWelcome("{{member_qq}}@{{group_id}}", 123, 456); value != "456@123" {
		t.Fatalf("RenderWelcome() = %q", value)
	}
}

func timePointer(value time.Time) *time.Time {
	return &value
}
