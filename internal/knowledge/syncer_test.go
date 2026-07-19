package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type staticRowSource struct {
	data []byte
	err  error
}

func (s staticRowSource) Download(context.Context) ([]byte, error) {
	return s.data, s.err
}

func TestSyncerInstallsOnlyValidatedAndSavedWorkbook(t *testing.T) {
	dir := t.TempDir()
	cacheFile := filepath.Join(dir, "knowledge.xlsx")
	ref := NewIndexRef([]Entry{{SourceKey: "old", Keyword: "旧", Enabled: true, ExactReply: true}})
	parse := func(data []byte, _ string) (SyncResult, error) {
		if string(data) == "empty" {
			return SyncResult{}, nil
		}
		return SyncResult{Entries: []Entry{{SourceKey: "new", Keyword: "新", Enabled: true, ExactReply: true}}}, nil
	}

	syncer := NewSyncer(SyncerOptions{
		Source: staticRowSource{data: []byte("empty")}, CacheFile: cacheFile, Index: ref, Parse: parse,
	})
	if _, err := syncer.Sync(context.Background()); err == nil {
		t.Fatal("Sync accepted an empty workbook")
	}
	if _, err := os.Stat(cacheFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cache file exists after rejected sync: %v", err)
	}
	if entry, _ := ref.Lookup("旧"); entry.SourceKey != "old" {
		t.Fatalf("index changed after rejected sync: %+v", entry)
	}

	syncer.source = staticRowSource{data: []byte("valid")}
	if _, err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("Sync returned error: %v", err)
	}
	data, err := os.ReadFile(cacheFile)
	if err != nil || string(data) != "valid" {
		t.Fatalf("cache = %q, err %v", data, err)
	}
	if entry, _ := ref.Lookup("新"); entry.SourceKey != "new" {
		t.Fatalf("new index was not installed: %+v", entry)
	}
}

func TestSyncerLoadsValidatedCache(t *testing.T) {
	cacheFile := filepath.Join(t.TempDir(), "knowledge.xlsx")
	if err := os.WriteFile(cacheFile, []byte("cached"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref := NewIndexRef(nil)
	syncer := NewSyncer(SyncerOptions{
		CacheFile: cacheFile,
		Index:     ref,
		Parse: func(data []byte, _ string) (SyncResult, error) {
			if string(data) != "cached" {
				return SyncResult{}, errors.New("unexpected cache")
			}
			return SyncResult{Entries: []Entry{{SourceKey: "cached", Keyword: "缓存", Enabled: true, ExactReply: true}}}, nil
		},
	})

	if _, err := syncer.LoadCache(); err != nil {
		t.Fatalf("LoadCache returned error: %v", err)
	}
	if entry, _ := ref.Lookup("缓存"); entry.SourceKey != "cached" {
		t.Fatalf("cached index was not installed: %+v", entry)
	}
}
