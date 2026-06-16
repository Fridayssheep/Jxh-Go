package storage_test

import (
	"context"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/commands"
	"github.com/zjutjh/jxh-go/internal/scheduler"
	"github.com/zjutjh/jxh-go/internal/storage"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestKnowledgeUpsertMarksChangedVectorPending(t *testing.T) {
	store := newTestStore(t)
	first := storage.KnowledgeEntry{SourceKey: "x", Keyword: "x", EntryType: "knowledge", Answer: "old", Content: "old", Enabled: true, ExactReply: true, AIEnabled: true}
	if err := store.UpsertKnowledgeEntries(context.Background(), []storage.KnowledgeEntry{first}, 1); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second := storage.KnowledgeEntry{SourceKey: "x", Keyword: "x", EntryType: "knowledge", Answer: "new", Content: "new", Enabled: true, ExactReply: true, AIEnabled: true}
	if err := store.UpsertKnowledgeEntries(context.Background(), []storage.KnowledgeEntry{second}, 2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := store.ListEnabledKnowledge(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].VectorStatus != storage.VectorStatusPending {
		t.Fatalf("entries = %#v", got)
	}
}

func TestProcessedEventsCleanup(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-100 * time.Hour)
	if err := store.MarkProcessedEvent(ctx, "old", old); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkProcessedEvent(ctx, "new", time.Now()); err != nil {
		t.Fatal(err)
	}
	removed, err := store.CleanupProcessedEvents(ctx, time.Now().Add(-72*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
}

func TestSeenOrMarkProcessedEvent(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	seen, err := store.SeenOrMarkProcessedEvent(ctx, "event-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if seen {
		t.Fatal("first event should not be seen")
	}
	seen, err = store.SeenOrMarkProcessedEvent(ctx, "event-1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !seen {
		t.Fatal("second event should be seen")
	}
}

func TestScheduledJobRuntimeState(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	id, err := store.AddScheduledJob(ctx, commands.ScheduledJobInput{
		Type:     scheduler.JobTypeOnce,
		TimeHHMM: "10:00",
		GroupID:  123,
		Message:  "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := store.ListActiveSchedulerJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != id || jobs[0].Message != "hello" {
		t.Fatalf("jobs = %#v", jobs)
	}
	now := time.Now()
	if err := store.MarkScheduledJobRan(ctx, id, now, true); err != nil {
		t.Fatal(err)
	}
	jobs, err = store.ListActiveSchedulerJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("disabled job should not be active: %#v", jobs)
	}
}

func newTestStore(t *testing.T) *storage.Store {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	store := storage.NewStore(db)
	mustExec(t, db, `
CREATE TABLE knowledge_entries (
  id integer PRIMARY KEY AUTOINCREMENT,
  source_key text NOT NULL UNIQUE,
  keyword text NOT NULL,
  entry_type text NOT NULL,
  path text,
  aliases_json text,
  category text,
  tags_json text,
  answer text NOT NULL,
  content text NOT NULL,
  enabled boolean NOT NULL,
  exact_reply boolean NOT NULL,
  ai_enabled boolean NOT NULL,
  content_hash text NOT NULL,
  vector_status text NOT NULL DEFAULT 'pending',
  vector_content_hash text,
  vector_synced_at datetime,
  last_import_run_id integer,
  source_updated_at datetime,
  created_at datetime,
  updated_at datetime
)`)
	mustExec(t, db, `
CREATE TABLE admins (
  user_id integer PRIMARY KEY,
  created_at datetime
)`)
	mustExec(t, db, `
CREATE TABLE blacklists (
  user_id integer PRIMARY KEY,
  created_at datetime
)`)
	mustExec(t, db, `
CREATE TABLE scheduled_jobs (
  id integer PRIMARY KEY AUTOINCREMENT,
  type text NOT NULL,
  time_hhmm text NOT NULL,
  group_id integer NOT NULL,
  message text NOT NULL,
  enabled boolean NOT NULL,
  last_run_at datetime,
  created_at datetime,
  updated_at datetime
)`)
	mustExec(t, db, `
CREATE TABLE processed_events (
  event_key text PRIMARY KEY,
  processed_at datetime
)`)
	mustExec(t, db, `CREATE INDEX idx_processed_events_processed_at ON processed_events (processed_at)`)
	return store
}

func mustExec(t *testing.T, db *gorm.DB, query string) {
	t.Helper()
	if err := db.Exec(query).Error; err != nil {
		t.Fatal(err)
	}
}
