package knowledge

import "testing"

func TestIndexKeepsLastExactAliasAndReplacesAtomically(t *testing.T) {
	ref := NewIndexRef([]Entry{
		{SourceKey: "first", Keyword: "一", Aliases: []string{"相同"}, Enabled: true, ExactReply: true},
		{SourceKey: "second", Keyword: "二", Aliases: []string{"相同"}, Enabled: true, ExactReply: true},
	})

	entry, ok := ref.Lookup(" 相同 ")
	if !ok || entry.SourceKey != "second" {
		t.Fatalf("Lookup = %+v, %v, want second entry", entry, ok)
	}

	ref.Store(NewIndex([]Entry{{SourceKey: "next", Keyword: "新", Enabled: true, ExactReply: true}}))
	if _, ok := ref.Lookup("相同"); ok {
		t.Fatal("old alias remained after index replacement")
	}
	entry, ok = ref.Lookup("新")
	if !ok || entry.SourceKey != "next" {
		t.Fatalf("Lookup after replacement = %+v, %v", entry, ok)
	}
}
