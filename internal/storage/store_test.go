package storage

import (
	"context"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/commands"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestStoreUpsertKnowledgeEntriesPreservesVectorStateWhenContentUnchanged(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := KnowledgeEntry{
		SourceKey:    "faq:one",
		Keyword:      "one",
		EntryType:    "knowledge",
		Answer:       "answer",
		Content:      "same content",
		Enabled:      true,
		ExactReply:   true,
		AIEnabled:    true,
		VectorStatus: VectorStatusReady,
	}
	if err := store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{first}, 1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	var stored KnowledgeEntry
	if err := db.Where("source_key = ?", "faq:one").Take(&stored).Error; err != nil {
		t.Fatalf("take stored entry: %v", err)
	}
	now := time.Now().UTC()
	stored.VectorStatus = VectorStatusReady
	stored.VectorContentHash = stored.ContentHash
	stored.VectorSyncedAt = &now
	if err := db.Save(&stored).Error; err != nil {
		t.Fatalf("save vector state: %v", err)
	}

	second := first
	second.Answer = "new answer"
	if err := store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{second}, 2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var got KnowledgeEntry
	if err := db.Where("source_key = ?", "faq:one").Take(&got).Error; err != nil {
		t.Fatalf("take updated entry: %v", err)
	}
	if got.VectorStatus != VectorStatusReady {
		t.Fatalf("VectorStatus = %q, want %q", got.VectorStatus, VectorStatusReady)
	}
	if got.VectorContentHash != got.ContentHash {
		t.Fatalf("VectorContentHash = %q, want %q", got.VectorContentHash, got.ContentHash)
	}
	if got.VectorSyncedAt == nil {
		t.Fatal("VectorSyncedAt is nil, want preserved timestamp")
	}
}

func TestStoreUpsertKnowledgeEntriesResetsVectorStateWhenContentChanges(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	entry := KnowledgeEntry{
		SourceKey:    "faq:changed",
		Keyword:      "changed",
		EntryType:    "knowledge",
		Answer:       "answer",
		Content:      "old content",
		Enabled:      true,
		ExactReply:   true,
		AIEnabled:    true,
		VectorStatus: VectorStatusReady,
	}
	if err := store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{entry}, 1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	var stored KnowledgeEntry
	if err := db.Where("source_key = ?", "faq:changed").Take(&stored).Error; err != nil {
		t.Fatalf("take stored entry: %v", err)
	}
	now := time.Now().UTC()
	stored.VectorStatus = VectorStatusReady
	stored.VectorContentHash = stored.ContentHash
	stored.VectorSyncedAt = &now
	if err := db.Save(&stored).Error; err != nil {
		t.Fatalf("save vector state: %v", err)
	}

	entry.Content = "new content"
	if err := store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{entry}, 2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var got KnowledgeEntry
	if err := db.Where("source_key = ?", "faq:changed").Take(&got).Error; err != nil {
		t.Fatalf("take updated entry: %v", err)
	}
	if got.VectorStatus != VectorStatusPending {
		t.Fatalf("VectorStatus = %q, want %q", got.VectorStatus, VectorStatusPending)
	}
	if got.VectorContentHash != "" {
		t.Fatalf("VectorContentHash = %q, want empty", got.VectorContentHash)
	}
	if got.VectorSyncedAt != nil {
		t.Fatalf("VectorSyncedAt = %v, want nil", got.VectorSyncedAt)
	}
}

func TestStoreUpsertKnowledgeEntriesDisablesEntriesMissingFromRun(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	first := KnowledgeEntry{SourceKey: "faq:one", Keyword: "one", EntryType: "knowledge", Answer: "one", Content: "one", Enabled: true, ExactReply: true, AIEnabled: true}
	second := KnowledgeEntry{SourceKey: "faq:two", Keyword: "two", EntryType: "knowledge", Answer: "two", Content: "two", Enabled: true, ExactReply: true, AIEnabled: true}
	if err := store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{first, second}, 1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := store.UpsertKnowledgeEntries(ctx, []KnowledgeEntry{second}, 2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var oldEntry KnowledgeEntry
	if err := db.Where("source_key = ?", "faq:one").Take(&oldEntry).Error; err != nil {
		t.Fatalf("take old entry: %v", err)
	}
	if oldEntry.Enabled {
		t.Fatal("old entry is enabled, want disabled")
	}
}

func TestStoreSeenOrMarkProcessedEvent(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	seen, err := store.SeenOrMarkProcessedEvent(ctx, "group:message:1", time.Now())
	if err != nil {
		t.Fatalf("first SeenOrMarkProcessedEvent: %v", err)
	}
	if seen {
		t.Fatal("first SeenOrMarkProcessedEvent seen = true, want false")
	}

	seen, err = store.SeenOrMarkProcessedEvent(ctx, "group:message:1", time.Now())
	if err != nil {
		t.Fatalf("second SeenOrMarkProcessedEvent: %v", err)
	}
	if !seen {
		t.Fatal("second SeenOrMarkProcessedEvent seen = false, want true")
	}
}

func TestKnowledgeEntryToModelOmitsZeroTimestamps(t *testing.T) {
	entry := KnowledgeEntry{SourceKey: "faq:zero-time", Keyword: "zero", EntryType: "knowledge"}

	got := knowledgeEntryToModel(entry)

	if got.CreatedAt != nil {
		t.Fatalf("CreatedAt = %v, want nil for zero time", got.CreatedAt)
	}
	if got.UpdatedAt != nil {
		t.Fatalf("UpdatedAt = %v, want nil for zero time", got.UpdatedAt)
	}
}

func TestStoreAdminBlacklistAndScheduledJobs(t *testing.T) {
	db := openTestDB(t)
	store := NewStore(db)
	ctx := context.Background()

	if err := store.AddAdmin(ctx, 1001); err != nil {
		t.Fatalf("AddAdmin: %v", err)
	}
	if err := store.AddAdmin(ctx, 1001); err != nil {
		t.Fatalf("AddAdmin duplicate: %v", err)
	}
	admins, err := store.ListAdmins(ctx)
	if err != nil {
		t.Fatalf("ListAdmins: %v", err)
	}
	if len(admins) != 1 || admins[0] != 1001 {
		t.Fatalf("admins = %#v, want [1001]", admins)
	}
	isAdmin, err := store.IsAdmin(ctx, 1001)
	if err != nil {
		t.Fatalf("IsAdmin: %v", err)
	}
	if !isAdmin {
		t.Fatal("IsAdmin = false, want true")
	}

	if err := store.AddBlacklist(ctx, 2002); err != nil {
		t.Fatalf("AddBlacklist: %v", err)
	}
	blocked, err := store.IsBlacklisted(ctx, 2002)
	if err != nil {
		t.Fatalf("IsBlacklisted: %v", err)
	}
	if !blocked {
		t.Fatal("IsBlacklisted = false, want true")
	}

	jobID, err := store.AddScheduledJob(ctx, commands.ScheduledJobInput{Type: "once", TimeHHMM: "12:30", GroupID: 3003, Message: "hello"})
	if err != nil {
		t.Fatalf("AddScheduledJob: %v", err)
	}
	jobs, err := store.ListScheduledJobs(ctx)
	if err != nil {
		t.Fatalf("ListScheduledJobs: %v", err)
	}
	if len(jobs) != 1 || jobs[0].ID != jobID || jobs[0].Message != "hello" {
		t.Fatalf("jobs = %#v, want one job with ID %d", jobs, jobID)
	}
	if err := store.RemoveScheduledJob(ctx, jobID); err != nil {
		t.Fatalf("RemoveScheduledJob: %v", err)
	}
	jobs, err = store.ListScheduledJobs(ctx)
	if err != nil {
		t.Fatalf("ListScheduledJobs after remove: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("jobs after remove = %#v, want empty", jobs)
	}
}

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite db: %v", err)
	}
	if err := db.AutoMigrate(&KnowledgeEntry{}, &KnowledgeImportRun{}, &Admin{}, &Blacklist{}, &ScheduledJob{}, &ProcessedEvent{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}
