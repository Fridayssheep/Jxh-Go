package knowledge

import (
	"strings"
	"testing"
)

func TestIndexSearchModesAndStableOrder(t *testing.T) {
	index := NewIndex([]Entry{
		{SourceKey: "first", Keyword: "空调缴费", Content: "宿舍 空调 缴费", Answer: "第一条", Enabled: true, AIEnabled: true},
		{SourceKey: "second", Keyword: "宿舍网络", Content: "宿舍 网络 报修", Answer: "第二条", Enabled: true, AIEnabled: true},
		{SourceKey: "third", Keyword: "空调报修", Content: "宿舍 空调 报修", Answer: "第三条", Enabled: true, AIEnabled: true},
	})

	andResults, err := index.Search(SearchQuery{Query: "宿舍 空调", Mode: "and"})
	if err != nil {
		t.Fatalf("and search returned error: %v", err)
	}
	if got := resultKeys(andResults); strings.Join(got, ",") != "first,third" {
		t.Fatalf("and keys = %v", got)
	}

	orResults, err := index.Search(SearchQuery{Query: "缴费 网络", Mode: "or"})
	if err != nil {
		t.Fatalf("or search returned error: %v", err)
	}
	if got := resultKeys(orResults); strings.Join(got, ",") != "first,second" {
		t.Fatalf("or keys = %v", got)
	}

	regexResults, err := index.Search(SearchQuery{Query: "空调.*(缴费|报修)", Mode: "regex"})
	if err != nil {
		t.Fatalf("regex search returned error: %v", err)
	}
	if got := resultKeys(regexResults); strings.Join(got, ",") != "first,third" {
		t.Fatalf("regex keys = %v", got)
	}
}

func TestIndexSearchFiltersStatusAndCapsLimit(t *testing.T) {
	entries := make([]Entry, 0, 13)
	for i := 0; i < 13; i++ {
		entries = append(entries, Entry{SourceKey: string(rune('a' + i)), Content: "match", Enabled: true, AIEnabled: true})
	}
	entries[0].Enabled = false
	entries[1].AIEnabled = false
	results, err := NewIndex(entries).Search(SearchQuery{Query: "match", Mode: "and", Limit: 99})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if len(results) != maxSearchLimit {
		t.Fatalf("result count = %d, want %d", len(results), maxSearchLimit)
	}
	if results[0].SourceKey != "c" {
		t.Fatalf("first result = %q, want c", results[0].SourceKey)
	}
}

func TestIndexSearchRejectsInvalidInput(t *testing.T) {
	index := NewIndex(nil)
	for _, input := range []SearchQuery{
		{Query: "[", Mode: "regex"},
		{Query: "x", Mode: "unknown"},
		{Query: strings.Repeat("x", maxSearchQueryRunes+1), Mode: "and"},
	} {
		if _, err := index.Search(input); err == nil {
			t.Fatalf("Search(%+v) returned nil error", input)
		}
	}
}

func TestIndexSearchStripsCQAndRespectsCharacterBudget(t *testing.T) {
	answer := "[CQ:image,file=https://example.com/a.png]" + strings.Repeat("答", maxSearchResultRunes+100)
	results, err := NewIndex([]Entry{{
		SourceKey: "source", Keyword: "关键词", Content: "match", Answer: answer, Enabled: true, AIEnabled: true,
	}}).Search(SearchQuery{Query: "match", Mode: "and"})
	if err != nil {
		t.Fatalf("search returned error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
	if strings.Contains(results[0].Answer, "CQ:image") {
		t.Fatalf("answer still contains CQ: %q", results[0].Answer[:20])
	}
	if got := len([]rune(results[0].SourceKey + results[0].Keyword + results[0].Answer)); got > maxSearchResultRunes {
		t.Fatalf("result rune count = %d, want <= %d", got, maxSearchResultRunes)
	}
}

func resultKeys(results []SearchResult) []string {
	keys := make([]string, len(results))
	for i := range results {
		keys[i] = results[i].SourceKey
	}
	return keys
}
