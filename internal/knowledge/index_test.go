package knowledge

import "testing"

func TestIndexUsesImmutableSourceKeyLookup(t *testing.T) {
	entries := []Entry{{SourceKey: "source-1", Keyword: "first"}, {SourceKey: "source-2", Keyword: "second"}}
	index := NewIndex(entries)
	entries[0].Keyword = "changed"
	if got := index.Keyword("source-1"); got != "first" {
		t.Fatalf("Keyword() = %q", got)
	}
	snapshot := index.Entries()
	snapshot[0].Keyword = "mutated"
	if got := index.Keyword("source-1"); got != "first" {
		t.Fatalf("snapshot mutated index: %q", got)
	}
}

func TestIndexResolvesSourceAndRuntimeKeys(t *testing.T) {
	index := NewIndex([]Entry{{SourceKey: "%400", Keyword: "Campus calendar", Enabled: false}})
	entries := index.Entries()
	if len(entries) != 1 || entries[0].ID == "" {
		t.Fatalf("entries = %+v", entries)
	}
	for _, key := range []string{"%400", entries[0].ID} {
		entry, ok := index.ResolveKey(key)
		if !ok || entry.SourceKey != "%400" || entry.Keyword != "Campus calendar" {
			t.Fatalf("ResolveKey(%q) = %+v, %t", key, entry, ok)
		}
	}
}
