package knowledge

import (
	"strings"
	"testing"
)

func TestParseRowsKeepsCQAnswerButRemovesImagesFromAIContent(t *testing.T) {
	answer := "See the map: [CQ:image,file=cache.image,url=https://cdn.example.com/map.png] Gate 1."

	entries, report := ParseRows([][]string{{"campus map", answer, "", "", "guide", "both", "enabled", "map"}})

	if report.ImportedRows != 1 || len(entries) != 1 {
		t.Fatalf("imported rows = %d, entries = %d", report.ImportedRows, len(entries))
	}
	if entries[0].Answer != answer {
		t.Fatalf("answer = %q, want raw CQ answer %q", entries[0].Answer, answer)
	}
	if strings.Contains(entries[0].Content, "[CQ:image") || strings.Contains(entries[0].Content, "cdn.example.com") {
		t.Fatalf("AI content still contains image CQ data: %q", entries[0].Content)
	}
	if !strings.Contains(entries[0].Content, "campus map guide see the map: gate 1.") {
		t.Fatalf("AI content lost surrounding text: %q", entries[0].Content)
	}
}

func TestParseRowsExplicitAIUsageOverridesChitchatInference(t *testing.T) {
	entries, _ := ParseRows([][]string{{"你好", "你好呀", "", "", "", "ai", "enabled", "hello"}})

	if len(entries) != 1 || entries[0].EntryType != EntryTypeChitchat {
		t.Fatalf("entries = %+v, want one chitchat entry", entries)
	}
	if !entries[0].AIEnabled || entries[0].ExactReply {
		t.Fatalf("entry flags = AI %v, exact %v", entries[0].AIEnabled, entries[0].ExactReply)
	}
}

func TestParseRowsRemovesImageCQFromMenuPathsAndAliases(t *testing.T) {
	rows := [][]string{
		{"%1", "Campus [CQ:image,url=https://cdn.example.com/campus.png]\n%2 Map [CQ:image,url=https://cdn.example.com/map.png]", "", "", "", "both", "enabled", "menu"},
		{"%2", "Map details", "", "", "", "both", "enabled", "map"},
	}

	entries, _ := ParseRows(rows)
	for _, entry := range entries {
		if strings.Contains(entry.Path, "[CQ:image") || strings.Contains(entry.Path, "cdn.example.com") {
			t.Fatalf("entry %q path contains image CQ data: %q", entry.Keyword, entry.Path)
		}
		for _, alias := range entry.Aliases {
			if strings.Contains(alias, "[CQ:image") || strings.Contains(alias, "cdn.example.com") {
				t.Fatalf("entry %q alias contains image CQ data: %q", entry.Keyword, alias)
			}
		}
	}
}
