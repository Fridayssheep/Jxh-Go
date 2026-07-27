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
