package management

import (
	"bytes"
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
