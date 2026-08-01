package knowledgeadmin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zjutjh/jxh-go/internal/knowledge"
)

type RuntimeSyncer interface {
	Sync(ctx context.Context) error
}

type RuntimeStoreOptions struct {
	Index            *knowledge.IndexRef
	Syncer           RuntimeSyncer
	SourceConfigured bool
	Now              func() time.Time
}

func (s *RuntimeStore) ResolveKnowledgeKey(value string) (string, string, bool) {
	if s == nil || s.index == nil {
		return "", "", false
	}
	entry, ok := s.index.ResolveKey(value)
	if !ok {
		return "", "", false
	}
	return entry.SourceKey, entry.Keyword, true
}

// RuntimeStore exposes the atomically swapped WPS index to the management
// service. It contains no credentials and does not persist management state.
type RuntimeStore struct {
	index            *knowledge.IndexRef
	syncer           RuntimeSyncer
	sourceConfigured bool
	now              func() time.Time

	reloadMu sync.Mutex
	mu       sync.RWMutex
	status   Status
	indexed  time.Time
}

func NewRuntimeStore(options RuntimeStoreOptions) (*RuntimeStore, error) {
	if options.Index == nil || options.Now == nil {
		return nil, ErrInvalidInput
	}
	entries := options.Index.Entries()
	now := options.Now().UTC()
	store := &RuntimeStore{
		index: options.Index, syncer: options.Syncer, sourceConfigured: options.SourceConfigured,
		now: options.Now, indexed: now,
	}
	store.status = store.statusForEntries(entries, now)
	return store, nil
}

func (s *RuntimeStore) Status(context.Context) (Status, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneStatus(s.status), nil
}

func (s *RuntimeStore) Reload(ctx context.Context) (ReloadResult, error) {
	if s.syncer == nil {
		return ReloadResult{}, ErrReloaderUnavailable
	}
	s.reloadMu.Lock()
	defer s.reloadMu.Unlock()

	attemptedAt := s.now().UTC()
	s.mu.Lock()
	s.status.LastAttemptAt = timePointer(attemptedAt)
	s.mu.Unlock()
	if err := s.syncer.Sync(ctx); err != nil {
		code := "reload_failed"
		if ctx.Err() != nil {
			code = "reload_timeout"
		}
		s.mu.Lock()
		if s.status.ActiveIndexVersion == nil {
			s.status.State = StateUnavailable
		} else {
			s.status.State = StateDegraded
		}
		s.status.LastErrorCode = stringPointer(code)
		s.mu.Unlock()
		return ReloadResult{}, runtimeReloadError{code: code}
	}

	entries := s.index.Entries()
	if len(entries) == 0 {
		code := "empty_index"
		s.mu.Lock()
		if s.status.ActiveIndexVersion == nil {
			s.status.State = StateUnavailable
		} else {
			s.status.State = StateDegraded
		}
		s.status.LastErrorCode = &code
		s.mu.Unlock()
		return ReloadResult{}, runtimeReloadError{code: "empty_index"}
	}
	completedAt := s.now().UTC()
	status := s.statusForEntries(entries, completedAt)
	status.LastAttemptAt = timePointer(attemptedAt)
	s.mu.Lock()
	s.status = status
	s.indexed = completedAt
	s.mu.Unlock()
	return ReloadResult{
		ActiveIndexVersion: dereference(status.ActiveIndexVersion), EntryCount: status.EntryCount,
		ConflictCount: status.ConflictCount,
	}, nil
}

func (s *RuntimeStore) Sync(ctx context.Context) error {
	_, err := s.Reload(ctx)
	return err
}

func (s *RuntimeStore) ListEntries(_ context.Context, query EntryQuery) (EntryPage, error) {
	entries, conflicts, indexedAt := s.snapshot()
	conflicted := conflictedEntryIDs(conflicts)
	items := make([]EntrySummary, 0, len(entries))
	for _, source := range entries {
		entry := mapRuntimeEntry(source, indexedAt, conflicted[runtimeEntryID(source)])
		if matchesEntryQuery(entry, query) {
			items = append(items, summaryFromEntry(entry))
		}
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	offset, err := decodeCursor(query.Cursor, entryQueryFingerprint(query))
	if err != nil || offset > len(items) {
		return EntryPage{}, ErrInvalidInput
	}
	end := offset + query.Limit
	if end > len(items) {
		end = len(items)
	}
	page := EntryPage{Items: append([]EntrySummary(nil), items[offset:end]...), HasMore: end < len(items)}
	if page.HasMore {
		page.NextCursor = encodeCursor(end, entryQueryFingerprint(query))
	}
	return page, nil
}

func (s *RuntimeStore) GetEntry(_ context.Context, id string) (Entry, bool, error) {
	entries, conflicts, indexedAt := s.snapshot()
	conflicted := conflictedEntryIDs(conflicts)
	for _, source := range entries {
		if runtimeEntryID(source) == id {
			return mapRuntimeEntry(source, indexedAt, conflicted[id]), true, nil
		}
	}
	return Entry{}, false, nil
}

func (s *RuntimeStore) ListConflicts(_ context.Context, query ConflictQuery) (ConflictPage, error) {
	_, conflicts, _ := s.snapshot()
	filtered := make([]Conflict, 0, len(conflicts))
	needle := strings.ToLower(query.Query)
	for _, conflict := range conflicts {
		if query.Type != "" && conflict.Type != query.Type {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(conflict.Key), needle) {
			continue
		}
		filtered = append(filtered, conflict)
	}
	sort.Slice(filtered, func(left, right int) bool { return filtered[left].ID < filtered[right].ID })
	offset, err := decodeCursor(query.Cursor, conflictQueryFingerprint(query))
	if err != nil || offset > len(filtered) {
		return ConflictPage{}, ErrInvalidInput
	}
	end := offset + query.Limit
	if end > len(filtered) {
		end = len(filtered)
	}
	page := ConflictPage{Items: cloneConflictPage(ConflictPage{Items: filtered[offset:end]}).Items, HasMore: end < len(filtered)}
	if page.HasMore {
		page.NextCursor = encodeCursor(end, conflictQueryFingerprint(query))
	}
	return page, nil
}

func (s *RuntimeStore) snapshot() ([]knowledge.Entry, []Conflict, time.Time) {
	entries := s.index.Entries()
	s.mu.RLock()
	indexedAt := s.indexed
	s.mu.RUnlock()
	return entries, runtimeConflicts(s.index.ParseResult(), entries, indexedAt), indexedAt
}

func (s *RuntimeStore) statusForEntries(entries []knowledge.Entry, at time.Time) Status {
	status := Status{SourceConfigured: s.sourceConfigured, EntryCount: len(entries)}
	if len(entries) > 0 {
		version := runtimeIndexVersion(entries)
		status.ActiveIndexVersion = &version
		status.LastSuccessAt = timePointer(at)
	}
	if !s.sourceConfigured {
		status.State = StateNotConfigured
	} else if len(entries) == 0 {
		status.State = StateUnavailable
	} else {
		status.State = StateReady
	}
	status.ConflictCount = len(runtimeConflicts(s.index.ParseResult(), entries, at))
	return status
}

func mapRuntimeEntry(source knowledge.Entry, indexedAt time.Time, conflict bool) Entry {
	typeValue := EntryTypeExactReply
	if source.AIEnabled && source.ExactReply {
		typeValue = EntryTypeHybrid
	} else if source.AIEnabled {
		typeValue = EntryTypeAIKnowledge
	}
	return Entry{
		ID: runtimeEntryID(source), SourceKey: source.SourceKey, Title: source.Keyword, Category: source.Category,
		Type: typeValue, Keywords: []string{source.Keyword}, Aliases: append([]string(nil), source.Aliases...),
		Question: source.Keyword, Answer: source.Answer, Enabled: source.Enabled, ExactReply: source.ExactReply,
		AIEnabled: source.AIEnabled, HasConflict: conflict, IndexedAt: indexedAt,
	}
}

func summaryFromEntry(entry Entry) EntrySummary {
	return EntrySummary{
		ID: entry.ID, Title: entry.Title, Category: entry.Category, Type: entry.Type,
		Keywords: append([]string(nil), entry.Keywords...), Aliases: append([]string(nil), entry.Aliases...),
		Enabled: entry.Enabled, ExactReply: entry.ExactReply, AIEnabled: entry.AIEnabled,
		HasConflict: entry.HasConflict, SourceUpdatedAt: cloneTime(entry.SourceUpdatedAt), IndexedAt: entry.IndexedAt,
	}
}

func matchesEntryQuery(entry Entry, query EntryQuery) bool {
	if query.Category != "" && !strings.EqualFold(entry.Category, query.Category) ||
		query.Type != "" && entry.Type != query.Type || !matchesBool(entry.Enabled, query.Enabled) ||
		!matchesBool(entry.ExactReply, query.ExactReply) || !matchesBool(entry.AIEnabled, query.AIEnabled) ||
		!matchesBool(entry.HasConflict, query.HasConflict) {
		return false
	}
	if query.Query == "" {
		return true
	}
	needle := strings.ToLower(query.Query)
	values := []string{entry.Title, entry.Category, entry.SourceKey, entry.Question, entry.Answer}
	values = append(values, entry.Keywords...)
	values = append(values, entry.Aliases...)
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}

func matchesBool(value bool, expected *bool) bool { return expected == nil || value == *expected }

func runtimeEntryID(entry knowledge.Entry) string {
	if entry.ID != "" {
		return entry.ID
	}
	sum := sha256.Sum256([]byte(entry.SourceKey + "\x00" + entry.Keyword))
	return "ke_" + hex.EncodeToString(sum[:12])
}

func runtimeIndexVersion(entries []knowledge.Entry) string {
	copyOfEntries := append([]knowledge.Entry(nil), entries...)
	sort.Slice(copyOfEntries, func(left, right int) bool {
		return runtimeEntryID(copyOfEntries[left]) < runtimeEntryID(copyOfEntries[right])
	})
	hash := sha256.New()
	for _, entry := range copyOfEntries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%t\x00%t\x00%t\n", entry.SourceKey, entry.Keyword, entry.Answer,
			entry.Enabled, entry.ExactReply, entry.AIEnabled)
	}
	return "idx_" + hex.EncodeToString(hash.Sum(nil)[:12])
}

type conflictCandidate struct {
	entryID string
	alias   bool
}

func detectRuntimeConflicts(entries []knowledge.Entry, at time.Time) []Conflict {
	byKey := make(map[string][]conflictCandidate)
	display := make(map[string]string)
	for _, entry := range entries {
		if !entry.Enabled || !entry.ExactReply {
			continue
		}
		id := runtimeEntryID(entry)
		key := strings.ToLower(strings.TrimSpace(entry.Keyword))
		if key != "" {
			byKey[key] = appendUniqueCandidate(byKey[key], conflictCandidate{entryID: id})
			display[key] = entry.Keyword
		}
		for _, alias := range entry.Aliases {
			key = strings.ToLower(strings.TrimSpace(alias))
			if key == "" {
				continue
			}
			byKey[key] = appendUniqueCandidate(byKey[key], conflictCandidate{entryID: id, alias: true})
			display[key] = alias
		}
	}
	conflicts := make([]Conflict, 0)
	for normalized, candidates := range byKey {
		if len(candidates) < 2 {
			continue
		}
		entryIDs := make([]string, len(candidates))
		typeValue := ConflictKeyword
		allAliases := true
		for index, candidate := range candidates {
			entryIDs[index] = candidate.entryID
			allAliases = allAliases && candidate.alias
		}
		if allAliases {
			typeValue = ConflictAlias
		}
		sort.Strings(entryIDs)
		sum := sha256.Sum256([]byte(string(typeValue) + "\x00" + normalized))
		conflicts = append(conflicts, Conflict{
			ID: "kc_" + hex.EncodeToString(sum[:12]), Type: typeValue, Key: display[normalized],
			EntryIDs: entryIDs, DetectedAt: at,
		})
	}
	return conflicts
}

func runtimeConflicts(parsed knowledge.ParseResult, entries []knowledge.Entry, at time.Time) []Conflict {
	if len(parsed.Conflicts) == 0 {
		return detectRuntimeConflicts(entries, at)
	}
	conflicts := make([]Conflict, 0, len(parsed.Conflicts))
	for _, source := range parsed.Conflicts {
		typeValue := ConflictKeyword
		switch source.Type {
		case knowledge.ConflictSourceKey:
			typeValue = ConflictSourceKey
		case knowledge.ConflictAlias:
			typeValue = ConflictAlias
		}
		sum := sha256.Sum256([]byte(string(typeValue) + "\x00" + strings.ToLower(source.Key)))
		conflicts = append(conflicts, Conflict{
			ID: "kc_" + hex.EncodeToString(sum[:12]), Type: typeValue, Key: source.Key,
			EntryIDs: append([]string(nil), source.EntryIDs...), DetectedAt: at,
		})
	}
	return conflicts
}

func appendUniqueCandidate(values []conflictCandidate, candidate conflictCandidate) []conflictCandidate {
	for index := range values {
		if values[index].entryID == candidate.entryID {
			values[index].alias = values[index].alias && candidate.alias
			return values
		}
	}
	return append(values, candidate)
}

func conflictedEntryIDs(conflicts []Conflict) map[string]bool {
	result := make(map[string]bool)
	for _, conflict := range conflicts {
		for _, id := range conflict.EntryIDs {
			result[id] = true
		}
	}
	return result
}

func encodeCursor(offset int, fingerprint string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(offset) + ":" + fingerprint))
}

func decodeCursor(cursor, fingerprint string) (int, error) {
	if cursor == "" {
		return 0, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, ErrInvalidInput
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 || parts[1] != fingerprint {
		return 0, ErrInvalidInput
	}
	offset, err := strconv.Atoi(parts[0])
	if err != nil || offset < 0 {
		return 0, ErrInvalidInput
	}
	return offset, nil
}

func entryQueryFingerprint(query EntryQuery) string {
	return shortHash(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s", query.Query, query.Category, query.Type,
		optionalBool(query.Enabled), optionalBool(query.ExactReply), optionalBool(query.AIEnabled), optionalBool(query.HasConflict)))
}

func conflictQueryFingerprint(query ConflictQuery) string {
	return shortHash(query.Query + "\x00" + string(query.Type))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func optionalBool(value *bool) string {
	if value == nil {
		return "null"
	}
	return strconv.FormatBool(*value)
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

type runtimeReloadError struct{ code string }

func (e runtimeReloadError) Error() string         { return "knowledge reload failed" }
func (e runtimeReloadError) SafeErrorCode() string { return e.code }
