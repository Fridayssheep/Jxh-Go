package management

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestOpaqueEventIDSourceProducesValidDistinctIDs(t *testing.T) {
	random := bytes.NewReader(append(bytes.Repeat([]byte{0x41}, 18), bytes.Repeat([]byte{0x42}, 18)...))
	source := opaqueEventIDSource(random)
	first, err := source()
	if err != nil {
		t.Fatal(err)
	}
	second, err := source()
	if err != nil {
		t.Fatal(err)
	}
	if first != "evt_QUFBQUFBQUFBQUFBQUFBQUFB" || second != "evt_QkJCQkJCQkJCQkJCQkJCQkJC" || second == first {
		t.Fatalf("ids=%q/%q", first, second)
	}
}

func TestNewBackendRejectsMissingDependencies(t *testing.T) {
	if _, err := NewBackend(Options{}); err == nil {
		t.Fatal("NewBackend() error=nil")
	}
}

func TestConfigurationEditorIsOptionalAndUsesTheLoadedSource(t *testing.T) {
	if editor := newConfigurationEditor(""); editor != nil {
		t.Fatal("empty source path created a configuration editor")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("app:\n  timezone: Asia/Shanghai\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	editor := newConfigurationEditor(path)
	if editor == nil {
		t.Fatal("loaded source path did not create a configuration editor")
	}
	if document, err := editor.Read(); err != nil || document.Version == 0 {
		t.Fatalf("configuration document=%+v error=%v", document, err)
	}
}
