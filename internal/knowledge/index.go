package knowledge

import (
	"strings"
	"sync/atomic"
)

type Index struct {
	entries []Entry
	exact   map[string]int
}

func NewIndex(entries []Entry) *Index {
	idx := &Index{
		entries: cloneEntries(entries),
		exact:   make(map[string]int),
	}
	for entryIndex, entry := range idx.entries {
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
	for _, entry := range i.entries {
		if entry.SourceKey == sourceKey {
			return entry.Keyword
		}
	}
	return ""
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

func normalizeLookup(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
