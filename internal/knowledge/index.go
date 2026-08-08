package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync/atomic"
)

type Index struct {
	entries       []Entry
	exact         map[string]int
	exactConflict map[string]ExactConflict
	source        map[string]int
	catalog       map[string]Entry
	parsed        ParseResult
}

func NewIndex(entries []Entry) *Index {
	entries = cloneEntries(entries)
	ensureEntryIDs(entries)
	return NewIndexFromParseResult(ParseResult{Entries: entries, ActiveEntries: cloneEntries(entries)})
}

func NewIndexFromParseResult(result ParseResult) *Index {
	result = cloneParseResult(result)
	if result.ActiveEntries == nil {
		result.ActiveEntries = cloneEntries(result.Entries)
	}
	ensureEntryIDs(result.Entries)
	ensureEntryIDs(result.ActiveEntries)
	idx := &Index{
		entries:       cloneEntries(result.ActiveEntries),
		exact:         make(map[string]int),
		exactConflict: make(map[string]ExactConflict),
		source:        make(map[string]int),
		catalog:       make(map[string]Entry, len(result.Entries)*2),
		parsed:        result,
	}
	for _, entry := range idx.parsed.Entries {
		if _, exists := idx.catalog[entry.SourceKey]; !exists {
			idx.catalog[entry.SourceKey] = cloneEntry(entry)
		}
		if _, exists := idx.catalog[entry.ID]; !exists {
			idx.catalog[entry.ID] = cloneEntry(entry)
		}
	}
	lookupCandidates := make(map[string][]int)
	lookupDisplay := make(map[string]string)
	for entryIndex, entry := range idx.entries {
		if _, exists := idx.source[entry.SourceKey]; !exists {
			idx.source[entry.SourceKey] = entryIndex
		}
		if !entry.Enabled || !entry.ExactReply {
			continue
		}
		addLookupCandidate(lookupCandidates, lookupDisplay, entry.Keyword, entryIndex)
		for _, alias := range entry.Aliases {
			addLookupCandidate(lookupCandidates, lookupDisplay, alias, entryIndex)
		}
	}
	for key, candidates := range lookupCandidates {
		if lookupAnswersConflict(idx.entries, candidates) {
			conflict := ExactConflict{Key: lookupDisplay[key], Entries: make([]ExactConflictEntry, 0, len(candidates))}
			for _, entryIndex := range candidates {
				entry := idx.entries[entryIndex]
				conflict.Entries = append(conflict.Entries, ExactConflictEntry{
					Row: entry.SourceRow, Keyword: entry.Keyword, SourceKey: entry.SourceKey,
				})
			}
			idx.exactConflict[key] = conflict
			continue
		}
		idx.exact[key] = candidates[0]
	}
	return idx
}

func addLookupCandidate(candidates map[string][]int, display map[string]string, value string, entryIndex int) {
	key := normalizeLookup(value)
	if key == "" {
		return
	}
	for _, existing := range candidates[key] {
		if existing == entryIndex {
			return
		}
	}
	candidates[key] = append(candidates[key], entryIndex)
	if _, exists := display[key]; !exists {
		display[key] = value
	}
}

func lookupAnswersConflict(entries []Entry, candidates []int) bool {
	if len(candidates) < 2 {
		return false
	}
	answer := normalizeText(entries[candidates[0]].Answer)
	for _, entryIndex := range candidates[1:] {
		if normalizeText(entries[entryIndex].Answer) != answer {
			return true
		}
	}
	return false
}

func (i *Index) Lookup(message string) (Entry, bool) {
	if i == nil {
		return Entry{}, false
	}
	entryIndex, ok := i.exact[normalizeLookup(message)]
	if !ok {
		return Entry{}, false
	}
	return cloneEntry(i.entries[entryIndex]), true
}

func (i *Index) LookupConflict(message string) (ExactConflict, bool) {
	if i == nil {
		return ExactConflict{}, false
	}
	conflict, ok := i.exactConflict[normalizeLookup(message)]
	return cloneExactConflict(conflict), ok
}

func (i *Index) Keyword(sourceKey string) string {
	if i == nil {
		return ""
	}
	entryIndex, ok := i.source[sourceKey]
	if !ok {
		return ""
	}
	return i.entries[entryIndex].Keyword
}

func (i *Index) ResolveKey(value string) (Entry, bool) {
	if i == nil {
		return Entry{}, false
	}
	entry, ok := i.catalog[value]
	return cloneEntry(entry), ok
}

func (i *Index) Entries() []Entry {
	if i == nil {
		return []Entry{}
	}
	return cloneEntries(i.parsed.Entries)
}

func (i *Index) ParseResult() ParseResult {
	if i == nil {
		return ParseResult{Entries: []Entry{}, ActiveEntries: []Entry{}}
	}
	return cloneParseResult(i.parsed)
}

type IndexRef struct {
	value atomic.Pointer[Index]
}

func NewIndexRef(entries []Entry) *IndexRef {
	ref := &IndexRef{}
	ref.Store(NewIndex(entries))
	return ref
}

func (r *IndexRef) Store(index *Index) {
	if index == nil {
		index = NewIndex(nil)
	}
	r.value.Store(index)
}

func (r *IndexRef) Lookup(message string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	index := r.value.Load()
	if index == nil {
		return Entry{}, false
	}
	return index.Lookup(message)
}

func (r *IndexRef) LookupConflict(message string) (ExactConflict, bool) {
	if r == nil {
		return ExactConflict{}, false
	}
	index := r.value.Load()
	if index == nil {
		return ExactConflict{}, false
	}
	return index.LookupConflict(message)
}

func (r *IndexRef) Keyword(sourceKey string) string {
	if r == nil {
		return ""
	}
	index := r.value.Load()
	if index == nil {
		return ""
	}
	return index.Keyword(sourceKey)
}

func (r *IndexRef) ResolveKey(value string) (Entry, bool) {
	if r == nil {
		return Entry{}, false
	}
	index := r.value.Load()
	if index == nil {
		return Entry{}, false
	}
	return index.ResolveKey(value)
}

func (r *IndexRef) Entries() []Entry {
	if r == nil {
		return []Entry{}
	}
	index := r.value.Load()
	if index == nil {
		return []Entry{}
	}
	return index.Entries()
}

func (r *IndexRef) ParseResult() ParseResult {
	if r == nil {
		return ParseResult{Entries: []Entry{}, ActiveEntries: []Entry{}}
	}
	index := r.value.Load()
	if index == nil {
		return ParseResult{Entries: []Entry{}, ActiveEntries: []Entry{}}
	}
	return index.ParseResult()
}

func cloneEntries(entries []Entry) []Entry {
	out := make([]Entry, len(entries))
	for i := range entries {
		out[i] = cloneEntry(entries[i])
	}
	return out
}

func cloneEntry(entry Entry) Entry {
	entry.Aliases = append([]string(nil), entry.Aliases...)
	return entry
}

func cloneExactConflict(conflict ExactConflict) ExactConflict {
	conflict.Entries = append([]ExactConflictEntry(nil), conflict.Entries...)
	return conflict
}

func cloneParseResult(value ParseResult) ParseResult {
	value.Entries = cloneEntries(value.Entries)
	value.ActiveEntries = cloneEntries(value.ActiveEntries)
	value.Issues = append([]ParseIssue(nil), value.Issues...)
	conflicts := make([]ParseConflict, len(value.Conflicts))
	for index := range value.Conflicts {
		conflicts[index] = value.Conflicts[index]
		conflicts[index].EntryIDs = append([]string(nil), value.Conflicts[index].EntryIDs...)
	}
	value.Conflicts = conflicts
	value.DisabledEntryIDs = append([]string(nil), value.DisabledEntryIDs...)
	value.AIOnlyEntryIDs = append([]string(nil), value.AIOnlyEntryIDs...)
	value.Warnings = append([]string(nil), value.Warnings...)
	return value
}

func ensureEntryIDs(entries []Entry) {
	for index := range entries {
		if entries[index].ID != "" {
			continue
		}
		sum := sha256.Sum256([]byte(entries[index].SourceKey + "\x00" + entries[index].Keyword))
		entries[index].ID = "ke_" + hex.EncodeToString(sum[:12])
	}
}

func normalizeLookup(value string) string {
	return strings.ToLower(strings.TrimSpace(foldFullwidthPercent(value)))
}

func foldFullwidthPercent(value string) string {
	return strings.ReplaceAll(value, "\uFF05", "%")
}
