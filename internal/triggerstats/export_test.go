package triggerstats

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xuri/excelize/v2"
)

func TestServiceExportsAllTriggerSummariesToXLSX(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, location)
	exportDir := t.TempDir()
	store := &memoryTriggerStore{summaries: []Summary{
		{SourceKey: "menu", Keyword: "菜单", TriggerType: TriggerTypeKeywordReply, Count: 12, LastTriggered: now},
		{SourceKey: "traffic", Keyword: "交通", TriggerType: TriggerTypeAIRetrieval, Count: 5, LastTriggered: now.Add(-time.Hour)},
	}}
	service := NewService(store, Options{Now: func() time.Time { return now }, ExportDir: exportDir})

	result, err := service.ExportForDays(context.Background(), 7)

	if err != nil {
		t.Fatalf("ExportForDays returned error: %v", err)
	}
	if result.Count != 2 {
		t.Fatalf("exported count = %d, want 2", result.Count)
	}
	if filepath.Dir(result.Path) != exportDir || !strings.HasPrefix(filepath.Base(result.Path), "trigger_stats_7d_20260714_153000_") {
		t.Fatalf("export path = %q", result.Path)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatalf("export file does not exist: %v", err)
	}
	if store.limit != 0 {
		t.Fatalf("summary limit = %d, want all", store.limit)
	}
	wantSince := time.Date(2026, 7, 8, 0, 0, 0, 0, location)
	if store.since == nil || !store.since.Equal(wantSince) {
		t.Fatalf("summary since = %v, want %v", store.since, wantSince)
	}

	f, err := excelize.OpenFile(result.Path)
	if err != nil {
		t.Fatalf("open exported xlsx: %v", err)
	}
	defer f.Close()
	for cell, want := range map[string]string{
		"A2": "菜单",
		"B2": "menu",
		"C2": "关键词回复",
		"D2": "12",
		"F2": "最近7个自然日",
		"A3": "交通",
		"C3": "/ai 检索",
	} {
		got, err := f.GetCellValue("词条统计", cell)
		if err != nil {
			t.Fatalf("read %s: %v", cell, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", cell, got, want)
		}
	}
}

func TestServiceExportsAllTimeTriggerStatsWithUniquePaths(t *testing.T) {
	now := time.Date(2026, 7, 14, 15, 30, 0, 0, time.Local)
	service := NewService(&memoryTriggerStore{}, Options{
		Now:       func() time.Time { return now },
		ExportDir: t.TempDir(),
	})

	first, err := service.ExportForDays(context.Background(), 0)
	if err != nil {
		t.Fatalf("first ExportForDays returned error: %v", err)
	}
	second, err := service.ExportForDays(context.Background(), 0)
	if err != nil {
		t.Fatalf("second ExportForDays returned error: %v", err)
	}
	if first.Path == second.Path {
		t.Fatalf("export paths are equal: %q", first.Path)
	}
	if !strings.Contains(filepath.Base(first.Path), "trigger_stats_all_") {
		t.Fatalf("all-time export path = %q", first.Path)
	}
}
