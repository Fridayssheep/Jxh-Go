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

func (i *Index) Entries() []Entry {
	if i == nil {
		return nil
	}
	return cloneEntries(i.entries)
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
	return r.value.Load().Lookup(message)
}

func (r *IndexRef) Entries() []Entry {
	if r == nil {
		return nil
	}
	return r.value.Load().Entries()
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
	entry.Tags = append([]string(nil), entry.Tags...)
	return entry
}

func normalizeLookup(value string) string {
	return strings.TrimSpace(value)
}
