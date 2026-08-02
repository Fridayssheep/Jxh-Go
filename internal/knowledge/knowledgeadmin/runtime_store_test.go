package knowledgeadmin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge"
)

func TestRuntimeStoreListsFiltersAndPaginatesImmutableIndex(t *testing.T) {
	index := knowledge.NewIndexRef([]knowledge.Entry{
		{SourceKey: "src-1", Keyword: "Apply", Category: "Guide", Aliases: []string{"join"}, Answer: "Answer 1", Enabled: true, ExactReply: true, AIEnabled: true},
		{SourceKey: "src-2", Keyword: "Join", Category: "Guide", Answer: "Answer 2", Enabled: true, ExactReply: true},
		{SourceKey: "src-3", Keyword: "Dorm", Category: "Life", Answer: "Answer 3", Enabled: false},
	})
	indexedAt := time.Date(2026, 7, 28, 2, 0, 0, 0, time.UTC)
	store, err := NewRuntimeStore(RuntimeStoreOptions{
		Index: index, SourceConfigured: true, Now: func() time.Time { return indexedAt },
	})
	if err != nil {
		t.Fatal(err)
	}
	flag := true
	first, err := store.ListEntries(t.Context(), EntryQuery{Category: "guide", Enabled: &flag, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.TotalCount != 2 || !first.HasMore || first.NextCursor == "" || !first.Items[0].HasConflict {
		t.Fatalf("first page=%+v", first)
	}
	second, err := store.ListEntries(t.Context(), EntryQuery{Category: "guide", Enabled: &flag, Cursor: first.NextCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.TotalCount != 2 || second.HasMore || second.Items[0].ID == first.Items[0].ID {
		t.Fatalf("second page=%+v", second)
	}
	directSecond, err := store.ListEntries(t.Context(), EntryQuery{Category: "guide", Enabled: &flag, Page: 2, Limit: 1})
	if err != nil || directSecond.TotalCount != 2 || len(directSecond.Items) != 1 || directSecond.Items[0].ID != second.Items[0].ID {
		t.Fatalf("direct second page=%+v error=%v", directSecond, err)
	}
	if _, err := store.ListEntries(t.Context(), EntryQuery{Category: "life", Cursor: first.NextCursor, Limit: 1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("reused cursor error=%v", err)
	}
	entry, found, err := store.GetEntry(t.Context(), first.Items[0].ID)
	if err != nil || !found || entry.Answer == "" || !entry.IndexedAt.Equal(indexedAt) {
		t.Fatalf("entry=%+v found=%v error=%v", entry, found, err)
	}
	entry.Aliases = append(entry.Aliases, "mutated")
	again, found, _ := store.GetEntry(t.Context(), first.Items[0].ID)
	if !found || len(again.Aliases) == len(entry.Aliases) {
		t.Fatalf("entry aliases runtime index: %+v", again)
	}
}

func TestRuntimeStoreExposesConflictsAndSafeStatus(t *testing.T) {
	index := knowledge.NewIndexRef([]knowledge.Entry{
		{SourceKey: "src-1", Keyword: "Apply", Aliases: []string{"same"}, Enabled: true, ExactReply: true},
		{SourceKey: "src-2", Keyword: "Other", Aliases: []string{"same"}, Enabled: true, ExactReply: true},
	})
	store, err := NewRuntimeStore(RuntimeStoreOptions{
		Index: index, SourceConfigured: true, Now: func() time.Time { return time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.Status(t.Context())
	if err != nil || status.State != StateReady || status.ActiveIndexVersion == nil || status.ConflictCount != 1 || !status.SourceConfigured {
		t.Fatalf("status=%+v error=%v", status, err)
	}
	page, err := store.ListConflicts(t.Context(), ConflictQuery{Type: ConflictAlias, Limit: 50})
	if err != nil || page.TotalCount != 1 || len(page.Items) != 1 || len(page.Items[0].EntryIDs) != 2 || page.Items[0].Key != "same" {
		t.Fatalf("conflicts=%+v error=%v", page, err)
	}
}

func TestRuntimeStoreListsEverySourceConflictCandidateByStableID(t *testing.T) {
	parsed := knowledge.ParseRows([][]string{
		{"First", "Answer one", "", "", "FAQ", "both", "enabled", "same-source"},
		{"Second", "Answer two", "", "", "FAQ", "both", "enabled", "same-source"},
	})
	index := knowledge.NewIndexRef(nil)
	index.Store(knowledge.NewIndexFromParseResult(parsed))
	store, err := NewRuntimeStore(RuntimeStoreOptions{
		Index: index, SourceConfigured: true, Now: func() time.Time { return time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := store.ListEntries(t.Context(), EntryQuery{HasConflict: boolPointer(true), Limit: 50})
	if err != nil || len(entries.Items) != 2 {
		t.Fatalf("entries=%+v error=%v", entries, err)
	}
	conflicts, err := store.ListConflicts(t.Context(), ConflictQuery{Type: ConflictSourceKey, Limit: 50})
	if err != nil || len(conflicts.Items) != 1 || len(conflicts.Items[0].EntryIDs) != 2 {
		t.Fatalf("conflicts=%+v error=%v", conflicts, err)
	}
	for _, id := range conflicts.Items[0].EntryIDs {
		if _, found, getErr := store.GetEntry(t.Context(), id); getErr != nil || !found {
			t.Fatalf("conflict candidate %q found=%v error=%v", id, found, getErr)
		}
	}
	if got := index.Keyword("same-source"); got != "First" {
		t.Fatalf("runtime source winner=%q", got)
	}
}

func TestRuntimeStoreReloadUpdatesVersionAndRedactsSyncError(t *testing.T) {
	index := knowledge.NewIndexRef([]knowledge.Entry{{SourceKey: "before", Keyword: "Before", Enabled: true, ExactReply: true}})
	now := time.Date(2026, 7, 28, 1, 0, 0, 0, time.UTC)
	syncer := &runtimeSyncerFake{run: func(context.Context) error {
		index.Store(knowledge.NewIndex([]knowledge.Entry{{SourceKey: "after", Keyword: "After", Enabled: true, AIEnabled: true}}))
		return nil
	}}
	store, err := NewRuntimeStore(RuntimeStoreOptions{Index: index, Syncer: syncer, SourceConfigured: true, Now: func() time.Time {
		now = now.Add(time.Second)
		return now
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Reload(t.Context())
	if err != nil || result.EntryCount != 1 || result.ActiveIndexVersion == "" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	entryPage, err := store.ListEntries(t.Context(), EntryQuery{Query: "After", Limit: 50})
	if err != nil || len(entryPage.Items) != 1 || entryPage.Items[0].Type != EntryTypeAIKnowledge {
		t.Fatalf("page=%+v error=%v", entryPage, err)
	}

	syncer.run = func(context.Context) error { return errors.New("WPS SID=secret") }
	_, err = store.Reload(t.Context())
	var safe interface{ SafeErrorCode() string }
	if !errors.As(err, &safe) || safe.SafeErrorCode() != "reload_failed" || err.Error() != "knowledge reload failed" {
		t.Fatalf("error=%v", err)
	}
}

type runtimeSyncerFake struct {
	run func(context.Context) error
}

func boolPointer(value bool) *bool { return &value }

func (s *runtimeSyncerFake) Sync(ctx context.Context) error { return s.run(ctx) }
