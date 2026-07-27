package knowledge

import "testing"

func TestParseRowsReturnsManagementCandidatesAndConflictSafeRuntimeEntries(t *testing.T) {
	result := ParseRows([][]string{
		{"Hello", "Answer one", "", "shared", "FAQ", "both", "enabled", "src-1"},
		{"World", "Answer two", "", "hello", "FAQ", "both", "enabled", "src-2"},
		{"Duplicate one", "First answer", "", "", "FAQ", "both", "enabled", "same-source"},
		{"Duplicate two", "Different answer", "", "", "FAQ", "both", "enabled", "same-source"},
		{"Disabled", "Disabled answer", "", "", "FAQ", "both", "disabled", "src-disabled"},
		{"AI only", "AI answer", "", "", "FAQ", "ai", "enabled", "src-ai"},
		{"", "Missing keyword"},
		{"Missing answer", ""},
	})
	if len(result.Entries) != 6 || len(result.ActiveEntries) != 5 {
		t.Fatalf("entries=%d active=%d", len(result.Entries), len(result.ActiveEntries))
	}
	if len(result.Issues) != 2 || result.Issues[0].Reason != IssueMissingKeyword || result.Issues[1].Reason != IssueMissingAnswer {
		t.Fatalf("issues=%+v", result.Issues)
	}
	if len(result.Conflicts) != 2 {
		t.Fatalf("conflicts=%+v", result.Conflicts)
	}
	for _, conflict := range result.Conflicts {
		if len(conflict.EntryIDs) != 2 || conflict.EntryIDs[0] == conflict.EntryIDs[1] {
			t.Fatalf("conflict=%+v", conflict)
		}
	}
	if len(result.DisabledEntryIDs) != 1 || len(result.AIOnlyEntryIDs) != 3 || len(result.Warnings) != 2 {
		t.Fatalf("disabled=%v ai_only=%v warnings=%v", result.DisabledEntryIDs, result.AIOnlyEntryIDs, result.Warnings)
	}

	index := NewIndexFromParseResult(result)
	if _, found := index.Lookup("hello"); found {
		t.Fatal("conflicted exact keyword remained active")
	}
	if got := index.Keyword("same-source"); got != "Duplicate one" {
		t.Fatalf("Keyword(same-source)=%q", got)
	}
	snapshot := index.ParseResult()
	snapshot.Entries[0].Answer = "mutated"
	snapshot.Conflicts[0].EntryIDs[0] = "mutated"
	again := index.ParseResult()
	if again.Entries[0].Answer == "mutated" || again.Conflicts[0].EntryIDs[0] == "mutated" {
		t.Fatal("parse result aliases immutable index")
	}
}

func TestParseRowsDeduplicatesIdenticalSourceWithoutConflict(t *testing.T) {
	result := ParseRows([][]string{
		{"One", "Same", "", "", "", "both", "enabled", "source"},
		{"Two", "Same", "", "", "", "both", "enabled", "source"},
	})
	if len(result.ActiveEntries) != 1 || len(result.Entries) != 2 || len(result.Conflicts) != 0 ||
		len(result.Issues) != 1 || result.Issues[0].Reason != IssueDuplicateIdentical {
		t.Fatalf("result=%+v", result)
	}
}
