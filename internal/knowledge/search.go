package knowledge

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/cqreply"
)

const (
	defaultSearchLimit   = 5
	maxSearchLimit       = 10
	maxSearchQueryRunes  = 200
	maxSearchResultRunes = 12000
	maxRegexLength       = 100 // Limit regex complexity to prevent slow scanning
)

type SearchQuery struct {
	Query string
	Mode  string
	Limit int
}

type SearchResult struct {
	SourceKey string `json:"source_key"`
	Keyword   string `json:"keyword"`
	Path      string `json:"path,omitempty"`
	Category  string `json:"category,omitempty"`
	Answer    string `json:"answer"`
}

func (i *Index) Search(input SearchQuery) ([]SearchResult, error) {
	if i == nil {
		return nil, nil
	}
	query := strings.TrimSpace(input.Query)
	if query == "" {
		return nil, fmt.Errorf("query is empty")
	}
	if utf8.RuneCountInString(query) > maxSearchQueryRunes {
		return nil, fmt.Errorf("query is longer than %d characters", maxSearchQueryRunes)
	}
	limit := input.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	match, err := searchMatcher(input.Mode, query)
	if err != nil {
		return nil, err
	}
	results := make([]SearchResult, 0, limit)
	usedRunes := 0
	for _, entry := range i.entries {
		if !entry.Enabled || !entry.AIEnabled || !match(entry.Content) {
			continue
		}
		result := SearchResult{
			SourceKey: entry.SourceKey,
			Keyword:   cqreply.Parse(entry.Keyword).PlainText,
			Path:      cqreply.Parse(entry.Path).PlainText,
			Category:  cqreply.Parse(entry.Category).PlainText,
			Answer:    cqreply.Parse(entry.Answer).PlainText,
		}
		metadataRunes := utf8.RuneCountInString(result.SourceKey + result.Keyword + result.Path + result.Category)
		remaining := maxSearchResultRunes - usedRunes - metadataRunes
		if remaining <= 0 {
			break
		}
		result.Answer = truncateRunes(result.Answer, remaining)
		usedRunes += metadataRunes + utf8.RuneCountInString(result.Answer)
		results = append(results, result)
		if len(results) == limit || usedRunes >= maxSearchResultRunes {
			break
		}
	}
	return results, nil
}

func (r *IndexRef) Search(input SearchQuery) ([]SearchResult, error) {
	if r == nil {
		return nil, nil
	}
	index := r.value.Load()
	if index == nil {
		return nil, nil
	}
	return index.Search(input)
}

func searchMatcher(mode, query string) (func(string) bool, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "and"
	}
	switch mode {
	case "and", "or":
		terms := strings.Fields(strings.ToLower(query))
		return func(content string) bool {
			for _, term := range terms {
				contains := strings.Contains(content, term)
				if mode == "and" && !contains {
					return false
				}
				if mode == "or" && contains {
					return true
				}
			}
			return mode == "and"
		}, nil
	case "regex":
		if utf8.RuneCountInString(query) > maxRegexLength {
			return nil, fmt.Errorf("regex is longer than %d characters", maxRegexLength)
		}
		re, err := regexp.Compile("(?i)" + query)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %w", err)
		}
		return re.MatchString, nil
	default:
		return nil, fmt.Errorf("unsupported search mode %q", mode)
	}
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
