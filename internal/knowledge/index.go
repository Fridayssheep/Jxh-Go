package knowledge

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync/atomic"
)

type Index struct {
	entries []Entry
	exact   map[string]int
	source  map[string]int
	parsed  ParseResult
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
		entries: cloneEntries(result.ActiveEntries),
		exact:   make(map[string]int),
		source:  make(map[string]int),
		parsed:  result,
	}
	for entryIndex, entry := range idx.entries {
		if _, exists := idx.source[entry.SourceKey]; !exists {
			idx.source[entry.SourceKey] = entryIndex
		}
		if !entry.Enabled || !entry.ExactReply {
			continue
		}
		idx.exact[normalizeLookup(entry.Keyword)] = entryIndex
		for _, alias := range entry.Aliases {
			idx.exact[normalizeLookup(alias)] = entryIndex
		}
	}
	return idx
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
	return strings.ToLower(strings.TrimSpace(value))
}
