package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/messaging/cqreply"
)

var (
	codePattern  = regexp.MustCompile(`^%\d+$`)
	childPattern = regexp.MustCompile(`(?m)(%\d+)\s+([^\n\r%]+)`)
)

type rawRow struct {
	row      int
	keyword  string
	answer   string
	aliases  []string
	category string
	usage    string
	status   string
	sourceID string
}

type parsedCandidate struct {
	row   int
	entry Entry
}

func ParseRows(rows [][]string) ParseResult {
	result := ParseResult{}
	raws := make([]rawRow, 0, len(rows))
	for index, row := range rows {
		r := rowToRaw(row, index+1)
		if r.keyword == "" {
			result.Issues = append(result.Issues, ParseIssue{Row: r.row, Reason: IssueMissingKeyword})
			continue
		}
		if r.answer == "" {
			result.Issues = append(result.Issues, ParseIssue{Row: r.row, Reason: IssueMissingAnswer})
			continue
		}
		raws = append(raws, r)
	}

	titleByCode := collectCodeTitles(raws)
	children := collectChildren(raws)
	pathByCode := buildPaths(titleByCode, children)
	candidates := make([]parsedCandidate, 0, len(raws))
	for _, raw := range raws {
		entry := enrich(raw, titleByCode, children, pathByCode)
		candidates = append(candidates, parsedCandidate{row: raw.row, entry: entry})
	}
	assignCandidateIDs(candidates)
	active, sourceConflicts, duplicateIssues := resolveSourceConflicts(candidates)
	result.Conflicts = append(result.Conflicts, sourceConflicts...)
	result.Issues = append(result.Issues, duplicateIssues...)
	result.Entries = candidateEntries(candidates)
	resolveExactConflicts(&result, &active)
	result.ActiveEntries = cloneEntries(active)
	for _, entry := range result.Entries {
		if !entry.Enabled {
			result.DisabledEntryIDs = append(result.DisabledEntryIDs, entry.ID)
		}
		if entry.Enabled && entry.AIEnabled && !entry.ExactReply {
			result.AIOnlyEntryIDs = append(result.AIOnlyEntryIDs, entry.ID)
		}
	}
	if len(result.Issues) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d rows were ignored or deduplicated", len(result.Issues)))
	}
	if len(result.Conflicts) > 0 {
		result.Warnings = append(result.Warnings, fmt.Sprintf("%d conflicts require review", len(result.Conflicts)))
	}
	return result
}

func assignCandidateIDs(candidates []parsedCandidate) {
	baseCounts := make(map[string]int, len(candidates))
	for _, candidate := range candidates {
		baseCounts[candidate.entry.SourceKey+"\x00"+candidate.entry.Keyword]++
	}
	for index := range candidates {
		value := candidates[index].entry.SourceKey + "\x00" + candidates[index].entry.Keyword
		if baseCounts[value] > 1 {
			value += fmt.Sprintf("\x00row:%d", candidates[index].row)
		}
		sum := sha256.Sum256([]byte(value))
		candidates[index].entry.ID = "ke_" + hex.EncodeToString(sum[:12])
	}
}

func resolveSourceConflicts(candidates []parsedCandidate) ([]Entry, []ParseConflict, []ParseIssue) {
	bySource := make(map[string][]int)
	order := make([]string, 0, len(candidates))
	for index, candidate := range candidates {
		if _, exists := bySource[candidate.entry.SourceKey]; !exists {
			order = append(order, candidate.entry.SourceKey)
		}
		bySource[candidate.entry.SourceKey] = append(bySource[candidate.entry.SourceKey], index)
	}
	active := make([]Entry, 0, len(order))
	conflicts := make([]ParseConflict, 0)
	issues := make([]ParseIssue, 0)
	for _, sourceKey := range order {
		indices := bySource[sourceKey]
		first := candidates[indices[0]].entry
		if len(indices) > 1 {
			identical := true
			ids := make([]string, 0, len(indices))
			for _, index := range indices {
				ids = append(ids, candidates[index].entry.ID)
				if normalizeText(candidates[index].entry.Answer) != normalizeText(first.Answer) {
					identical = false
				}
			}
			if identical {
				for _, index := range indices[1:] {
					issues = append(issues, ParseIssue{Row: candidates[index].row, Reason: IssueDuplicateIdentical})
				}
			} else {
				for _, index := range indices {
					candidates[index].entry.AIEnabled = false
				}
				first.AIEnabled = false
				conflicts = append(conflicts, ParseConflict{Type: ConflictSourceKey, Key: sourceKey, EntryIDs: ids})
			}
		}
		active = append(active, first)
	}
	return active, conflicts, issues
}

type exactCandidate struct {
	entryIndex int
	entryID    string
	alias      bool
}

func resolveExactConflicts(result *ParseResult, active *[]Entry) {
	byKey := make(map[string][]exactCandidate)
	display := make(map[string]string)
	for index, entry := range *active {
		if !entry.Enabled || !entry.ExactReply {
			continue
		}
		addExactCandidate(byKey, display, entry.Keyword, exactCandidate{entryIndex: index, entryID: entry.ID})
		for _, alias := range entry.Aliases {
			addExactCandidate(byKey, display, alias, exactCandidate{entryIndex: index, entryID: entry.ID, alias: true})
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	allEntryIndex := make(map[string]int, len(result.Entries))
	for index, entry := range result.Entries {
		allEntryIndex[entry.ID] = index
	}
	for _, key := range keys {
		candidates := byKey[key]
		if len(candidates) < 2 {
			continue
		}
		allAliases := true
		ids := make([]string, len(candidates))
		for index, candidate := range candidates {
			allAliases = allAliases && candidate.alias
			ids[index] = candidate.entryID
			(*active)[candidate.entryIndex].ExactReply = false
			if allIndex, exists := allEntryIndex[candidate.entryID]; exists {
				result.Entries[allIndex].ExactReply = false
			}
		}
		conflictType := ConflictKeyword
		if allAliases {
			conflictType = ConflictAlias
		}
		result.Conflicts = append(result.Conflicts, ParseConflict{Type: conflictType, Key: display[key], EntryIDs: ids})
	}
}

func addExactCandidate(byKey map[string][]exactCandidate, display map[string]string, value string, candidate exactCandidate) {
	key := normalizeLookup(value)
	if key == "" {
		return
	}
	for index := range byKey[key] {
		if byKey[key][index].entryID == candidate.entryID {
			byKey[key][index].alias = byKey[key][index].alias && candidate.alias
			return
		}
	}
	byKey[key] = append(byKey[key], candidate)
	display[key] = value
}

func candidateEntries(candidates []parsedCandidate) []Entry {
	entries := make([]Entry, len(candidates))
	for index := range candidates {
		entries[index] = cloneEntry(candidates[index].entry)
	}
	return entries
}

func rowToRaw(row []string, rowNumber int) rawRow {
	get := func(idx int) string {
		if idx >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[idx])
	}
	return rawRow{
		row:      rowNumber,
		keyword:  get(0),
		answer:   get(1),
		aliases:  splitList(get(3)),
		category: get(4),
		usage:    strings.ToLower(get(5)),
		status:   strings.ToLower(get(6)),
		sourceID: get(7),
	}
}

func collectCodeTitles(raws []rawRow) map[string]string {
	titles := make(map[string]string)
	for _, raw := range raws {
		if !codePattern.MatchString(raw.keyword) {
			continue
		}
		plainAnswer := cqreply.Parse(raw.answer).PlainText
		trimmed := strings.TrimSpace(strings.Split(plainAnswer, "\n")[0])
		if trimmed != "" && !strings.Contains(trimmed, "%") {
			titles[raw.keyword] = compactTitle(trimmed)
		}
	}
	for _, raw := range raws {
		for _, match := range childPattern.FindAllStringSubmatch(cqreply.Parse(raw.answer).PlainText, -1) {
			titles[match[1]] = compactTitle(match[2])
		}
	}
	return titles
}

func collectChildren(raws []rawRow) map[string][]string {
	children := make(map[string][]string)
	for _, raw := range raws {
		if !codePattern.MatchString(raw.keyword) {
			continue
		}
		for _, match := range childPattern.FindAllStringSubmatch(raw.answer, -1) {
			children[raw.keyword] = append(children[raw.keyword], match[1])
		}
	}
	return children
}

func buildPaths(titles map[string]string, children map[string][]string) map[string]string {
	parent := make(map[string]string)
	for p, kids := range children {
		for _, child := range kids {
			if _, exists := parent[child]; !exists {
				parent[child] = p
			}
		}
	}
	paths := make(map[string]string)
	for code := range titles {
		var parts []string
		seen := make(map[string]struct{})
		for cur := code; cur != ""; cur = parent[cur] {
			if _, ok := seen[cur]; ok {
				break
			}
			seen[cur] = struct{}{}
			if title := titles[cur]; title != "" {
				parts = append(parts, title)
			}
			if _, ok := parent[cur]; !ok {
				break
			}
		}
		if len(parts) > 0 {
			slices.Reverse(parts)
			paths[code] = strings.Join(parts, " / ")
		}
	}
	return paths
}

func enrich(raw rawRow, titles map[string]string, children map[string][]string, paths map[string]string) Entry {
	entryType := classify(raw, children)
	enabled := raw.status == "" || raw.status == "enabled"
	usage := raw.usage
	if usage == "" {
		switch entryType {
		case EntryTypeChitchat:
			usage = "exact"
		case EntryTypeMenuNode:
			usage = "exact"
			if hasFacts(raw.answer) {
				usage = "both"
			}
		default:
			usage = "both"
		}
	}
	aliases := uniqueStrings(raw.aliases)
	if title := titles[raw.keyword]; title != "" {
		aliases = uniqueStrings(append(aliases, title))
	}
	if path := paths[raw.keyword]; path != "" {
		parts := strings.Split(path, " / ")
		aliases = uniqueStrings(append(aliases, parts[len(parts)-1]))
	}
	sourceKey := raw.sourceID
	if sourceKey == "" {
		sourceKey = normalizeLookup(raw.keyword)
	}
	category := raw.category
	if category == "" {
		category = inferCategory(paths[raw.keyword], raw.keyword, raw.answer)
	}
	explicitAIUsage := raw.usage == "ai" || raw.usage == "both"
	return Entry{
		SourceKey:  sourceKey,
		Keyword:    raw.keyword,
		EntryType:  entryType,
		Path:       paths[raw.keyword],
		Aliases:    aliases,
		Category:   category,
		Answer:     raw.answer,
		Content:    buildContent(raw.keyword, aliases, paths[raw.keyword], category, raw.answer),
		Enabled:    enabled,
		ExactReply: enabled && (usage == "both" || usage == "exact"),
		AIEnabled:  enabled && (usage == "both" || usage == "ai") && (entryType != EntryTypeChitchat || explicitAIUsage),
	}
}

func classify(raw rawRow, children map[string][]string) string {
	if len(children[raw.keyword]) > 0 {
		return EntryTypeMenuNode
	}
	if isChitchat(raw.keyword, raw.answer) {
		return EntryTypeChitchat
	}
	return EntryTypeKnowledge
}

func isChitchat(keyword, answer string) bool {
	if codePattern.MatchString(keyword) {
		return false
	}
	keyLen := utf8.RuneCountInString(keyword)
	ansLen := utf8.RuneCountInString(answer)
	if keyLen <= 6 && ansLen <= 30 && !strings.Contains(answer, "\n") {
		return true
	}
	return false
}

func hasFacts(answer string) bool {
	return strings.Contains(answer, "。") || strings.Contains(answer, "：") || strings.Contains(answer, "【")
}

func buildContent(keyword string, aliases []string, path, category, answer string) string {
	parts := make([]string, 0, len(aliases)+4)
	parts = append(parts, keyword)
	parts = append(parts, aliases...)
	parts = append(parts, path, category, answer)
	for i := range parts {
		parts[i] = cqreply.Parse(parts[i]).PlainText
	}
	return strings.ToLower(strings.Join(strings.Fields(strings.Join(parts, " ")), " "))
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ';' || r == '；' || r == ',' || r == '，'
	})
	out := make([]string, 0, len(fields))
	for _, field := range fields {
		if trimmed := strings.TrimSpace(field); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeText(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}

func compactTitle(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, " \t-—:：")
	return value
}

func inferCategory(path, keyword, answer string) string {
	text := path + keyword + answer
	switch {
	case strings.Contains(text, "交通") || strings.Contains(text, "火车") || strings.Contains(text, "机场") || strings.Contains(text, "公交"):
		return "交通"
	case strings.Contains(text, "选课") || strings.Contains(text, "学分") || strings.Contains(text, "绩点"):
		return "学习"
	case strings.Contains(text, "寝室") || strings.Contains(text, "宿舍"):
		return "宿舍"
	case strings.Contains(text, "报到") || strings.Contains(text, "开学"):
		return "报到"
	default:
		return ""
	}
}
