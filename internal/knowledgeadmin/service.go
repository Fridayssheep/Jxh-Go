package knowledgeadmin

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
)

const (
	defaultLimit         = 50
	defaultReloadTimeout = 2 * time.Minute
	minimumSecretBytes   = 32
)

var (
	idempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	errorCodePattern      = regexp.MustCompile(`^[a-z][a-z0-9_]{0,99}$`)
)

type safeErrorCoder interface {
	SafeErrorCode() string
}

type reloadOverlay struct {
	operation ReloadOperation
	result    *ReloadResult
}

type Service struct {
	store          Store
	operations     OperationStore
	reloader       Reloader
	events         EventSink
	secret         []byte
	reloadTimeout  time.Duration
	now            func() time.Time
	newOperationID func() string

	mu     sync.RWMutex
	latest *reloadOverlay
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil {
		return nil, ErrInvalidInput
	}
	if options.Reloader != nil && (options.Operations == nil || len(options.IdempotencySecret) < minimumSecretBytes) {
		return nil, ErrInvalidInput
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.ReloadTimeout == 0 {
		options.ReloadTimeout = defaultReloadTimeout
	}
	if options.ReloadTimeout < time.Millisecond || options.ReloadTimeout > 10*time.Minute {
		return nil, ErrInvalidInput
	}
	if options.NewOperationID == nil {
		options.NewOperationID = randomOperationID
	}
	return &Service{
		store: options.Store, operations: options.Operations, reloader: options.Reloader, events: options.Events,
		secret:        append([]byte(nil), options.IdempotencySecret...),
		reloadTimeout: options.ReloadTimeout, now: options.Now, newOperationID: options.NewOperationID,
	}, nil
}

func (s *Service) GetStatus(ctx context.Context, principal auth.Principal) (Status, error) {
	if !principal.Has(auth.PermissionKnowledgeRead) {
		return Status{}, ErrForbidden
	}
	status, err := s.store.Status(ctx)
	if err != nil {
		return Status{}, fmt.Errorf("load knowledge status: %w", err)
	}
	if err := validateStatus(status); err != nil {
		return Status{}, err
	}

	status = cloneStatus(status)
	s.mu.RLock()
	if s.latest != nil {
		overlay := cloneOverlay(*s.latest)
		applyOverlay(&status, overlay)
	}
	s.mu.RUnlock()
	return status, nil
}

func (s *Service) StartReload(
	ctx context.Context,
	principal auth.Principal,
	idempotencyKey string,
	request ...auth.MutationContext,
) (ReloadOperation, error) {
	if !principal.Has(auth.PermissionKnowledgeReload) {
		return ReloadOperation{}, ErrForbidden
	}
	if principal.UserID == "" || !idempotencyKeyPattern.MatchString(idempotencyKey) {
		return ReloadOperation{}, ErrInvalidInput
	}
	if s.reloader == nil {
		return ReloadOperation{}, ErrReloaderUnavailable
	}
	requestContext := auth.MutationContext{}
	if len(request) > 0 {
		requestContext = request[0]
	}
	if !validMutationContext(requestContext) {
		return ReloadOperation{}, ErrInvalidInput
	}
	operationID := s.newOperationID()
	requestedAt := s.now().UTC()
	if !validIdentifier(operationID) || requestedAt.IsZero() {
		return ReloadOperation{}, ErrInvalidData
	}
	operation, fresh, err := s.operations.BeginKnowledgeReload(ctx, BeginReload{
		Actor: principal, Context: requestContext, OperationID: operationID,
		IdempotencyKey: idempotencyKey, RequestHash: s.requestHash(), RequestedAt: requestedAt,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrReloadInProgress):
			return ReloadOperation{}, ErrReloadInProgress
		case errors.Is(err, ErrIdempotencyConflict):
			return ReloadOperation{}, ErrIdempotencyConflict
		default:
			return ReloadOperation{}, fmt.Errorf("begin knowledge reload: %w", err)
		}
	}
	if err := validateOperation(operation); err != nil || (fresh && operation.Status != OperationAccepted) {
		return ReloadOperation{}, ErrInvalidData
	}
	s.setLatest(operation, nil)

	if fresh {
		go func() {
			reloadCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.reloadTimeout)
			defer cancel()
			s.runReload(reloadCtx, operation)
		}()
	}
	return cloneOperation(operation), nil
}

func (s *Service) runReload(ctx context.Context, operation ReloadOperation) {
	running, err := s.operations.TransitionKnowledgeReload(ctx, ReloadTransition{
		OperationID: operation.ID, From: OperationAccepted, To: OperationRunning, At: s.now().UTC(),
	})
	if err != nil {
		s.finishReload(operation, OperationAccepted, "operation_state_unknown", true)
		return
	}
	s.setLatest(running, nil)

	result, err := s.reloader.Reload(ctx)
	completedAt := s.now().UTC()
	if err == nil {
		err = validateReloadResult(result)
	}
	if err != nil {
		code := safeReloadErrorCode(err)
		unknown := errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
		s.finishReload(running, OperationRunning, code, unknown)
		return
	}
	completed, transitionErr := s.operations.TransitionKnowledgeReload(ctx, ReloadTransition{
		OperationID: operation.ID, From: OperationRunning, To: OperationSucceeded, At: completedAt,
	})
	if transitionErr != nil {
		return
	}
	s.setLatest(completed, &result)
	s.publishCompletion(completed)
}

func (s *Service) finishReload(operation ReloadOperation, from OperationStatus, code string, unknown bool) {
	transitionContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	completed, err := s.operations.TransitionKnowledgeReload(transitionContext, ReloadTransition{
		OperationID: operation.ID, From: from, To: OperationFailed, At: s.now().UTC(),
		ErrorCode: code, OutcomeUnknown: unknown,
	})
	if err != nil {
		return
	}
	s.setLatest(completed, nil)
	s.publishCompletion(completed)
}

func (s *Service) requestHash() string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte("jxh-admin/knowledge-reload/v1"))
	return hex.EncodeToString(mac.Sum(nil))
}

func validMutationContext(value auth.MutationContext) bool {
	return validOptionalBoundedText(value.RequestID, 64) && validOptionalBoundedText(value.IPAddress, 64) &&
		validOptionalBoundedText(value.UserAgent, 300)
}

func validOptionalBoundedText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func (s *Service) setLatest(operation ReloadOperation, result *ReloadResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.latest != nil && s.latest.operation.ID != operation.ID &&
		(s.latest.operation.StartedAt.After(operation.StartedAt) || s.latest.operation.StartedAt.Equal(operation.StartedAt)) {
		return
	}
	var copied *ReloadResult
	if result != nil {
		value := *result
		copied = &value
	}
	s.latest = &reloadOverlay{operation: cloneOperation(operation), result: copied}
}

func (s *Service) RecoverInterrupted(ctx context.Context) (int, error) {
	if s.operations == nil {
		return 0, nil
	}
	operations, err := s.operations.RecoverInterruptedKnowledgeReloads(ctx, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("recover interrupted knowledge reloads: %w", err)
	}
	for _, operation := range operations {
		if validateOperation(operation) != nil || operation.Status != OperationFailed {
			return 0, ErrInvalidData
		}
		s.setLatest(operation, nil)
		s.publishCompletion(operation)
	}
	return len(operations), nil
}

func (s *Service) publishCompletion(operation ReloadOperation) {
	if s.events == nil || operation.CompletedAt == nil {
		return
	}
	reason := "failed"
	if operation.Status == OperationSucceeded {
		reason = "succeeded"
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventKnowledgeReloadCompleted, OccurredAt: *operation.CompletedAt,
		Resource: &events.Resource{Type: events.ResourceKnowledge, ID: operation.ID}, Reason: reason,
	})
}

func (s *Service) ListEntries(ctx context.Context, principal auth.Principal, query EntryQuery) (EntryPage, error) {
	if !principal.Has(auth.PermissionKnowledgeRead) {
		return EntryPage{}, ErrForbidden
	}
	normalizeEntryQuery(&query)
	if !validEntryQuery(query) {
		return EntryPage{}, ErrInvalidInput
	}
	page, err := s.store.ListEntries(ctx, query)
	if err != nil {
		return EntryPage{}, fmt.Errorf("list knowledge entries: %w", err)
	}
	if err := validateEntryPage(page); err != nil {
		return EntryPage{}, err
	}
	return cloneEntryPage(page), nil
}

func (s *Service) GetEntry(ctx context.Context, principal auth.Principal, id string) (Entry, error) {
	if !principal.Has(auth.PermissionKnowledgeRead) {
		return Entry{}, ErrForbidden
	}
	if !validIdentifier(id) {
		return Entry{}, ErrInvalidInput
	}
	entry, found, err := s.store.GetEntry(ctx, id)
	if err != nil {
		return Entry{}, fmt.Errorf("get knowledge entry: %w", err)
	}
	if !found {
		return Entry{}, ErrNotFound
	}
	if err := validateEntry(entry); err != nil {
		return Entry{}, err
	}
	return cloneEntry(entry), nil
}

func (s *Service) ListConflicts(ctx context.Context, principal auth.Principal, query ConflictQuery) (ConflictPage, error) {
	if !principal.Has(auth.PermissionKnowledgeRead) {
		return ConflictPage{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = defaultLimit
	}
	if !validConflictQuery(query) {
		return ConflictPage{}, ErrInvalidInput
	}
	page, err := s.store.ListConflicts(ctx, query)
	if err != nil {
		return ConflictPage{}, fmt.Errorf("list knowledge conflicts: %w", err)
	}
	if err := validateConflictPage(page); err != nil {
		return ConflictPage{}, err
	}
	return cloneConflictPage(page), nil
}

func applyOverlay(status *Status, overlay reloadOverlay) {
	operation := cloneOperation(overlay.operation)
	status.CurrentOperation = &operation
	status.LastAttemptAt = timePointer(operation.StartedAt)
	switch operation.Status {
	case OperationAccepted, OperationRunning:
		status.State = StateReloading
	case OperationSucceeded:
		status.State = StateReady
		status.LastSuccessAt = cloneTime(operation.CompletedAt)
		status.LastErrorCode = nil
		if overlay.result != nil {
			status.ActiveIndexVersion = stringPointer(overlay.result.ActiveIndexVersion)
			status.EntryCount = overlay.result.EntryCount
			status.ConflictCount = overlay.result.ConflictCount
		}
	case OperationFailed:
		if status.ActiveIndexVersion == nil {
			status.State = StateUnavailable
		} else {
			status.State = StateDegraded
		}
		status.LastErrorCode = cloneString(operation.ErrorCode)
	}
}

func operationInProgress(status OperationStatus) bool {
	return status == OperationAccepted || status == OperationRunning
}

func safeReloadErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "reload_timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "reload_canceled"
	}
	var coded safeErrorCoder
	if errors.As(err, &coded) {
		if code := coded.SafeErrorCode(); errorCodePattern.MatchString(code) {
			return code
		}
	}
	return "reload_failed"
}

func randomOperationID() string {
	var data [18]byte
	if _, err := rand.Read(data[:]); err != nil {
		return ""
	}
	return "kop_" + base64.RawURLEncoding.EncodeToString(data[:])
}

func normalizeEntryQuery(query *EntryQuery) {
	if query.Limit == 0 {
		query.Limit = defaultLimit
	}
}

func validEntryQuery(query EntryQuery) bool {
	return validText(query.Query, 200) && validText(query.Category, 100) && validOptionalEntryType(query.Type) &&
		validCursor(query.Cursor) && query.Limit >= 1 && query.Limit <= 100
}

func validConflictQuery(query ConflictQuery) bool {
	return validText(query.Query, 200) && validOptionalConflictType(query.Type) && validCursor(query.Cursor) &&
		query.Limit >= 1 && query.Limit <= 100
}

func validateStatus(status Status) error {
	if !validState(status.State) || status.EntryCount < 0 || status.ConflictCount < 0 ||
		!validOptionalText(status.ActiveIndexVersion, 128) || !validOptionalErrorCode(status.LastErrorCode) {
		return ErrInvalidData
	}
	if status.CurrentOperation != nil && validateOperation(*status.CurrentOperation) != nil {
		return ErrInvalidData
	}
	return nil
}

func validateOperation(operation ReloadOperation) error {
	if !validIdentifier(operation.ID) || operation.StartedAt.IsZero() || !validOperationStatus(operation.Status) ||
		!validOptionalErrorCode(operation.ErrorCode) {
		return ErrInvalidData
	}
	if operationInProgress(operation.Status) && (operation.CompletedAt != nil || operation.ErrorCode != nil) {
		return ErrInvalidData
	}
	if (operation.Status == OperationSucceeded || operation.Status == OperationFailed) && operation.CompletedAt == nil {
		return ErrInvalidData
	}
	return nil
}

func validateReloadResult(result ReloadResult) error {
	if !validText(result.ActiveIndexVersion, 128) || result.ActiveIndexVersion == "" || result.EntryCount < 0 || result.ConflictCount < 0 {
		return ErrInvalidData
	}
	return nil
}

func validateEntryPage(page EntryPage) error {
	if len(page.Items) > 100 || !validCursor(page.NextCursor) || (page.HasMore && page.NextCursor == "") {
		return ErrInvalidData
	}
	for _, entry := range page.Items {
		if err := validateEntrySummary(entry); err != nil {
			return err
		}
	}
	return nil
}

func validateEntrySummary(entry EntrySummary) error {
	if !validIdentifier(entry.ID) || !validText(entry.Title, 200) || !validText(entry.Category, 100) ||
		!validEntryType(entry.Type) || !validStringList(entry.Keywords, 100, 100) || !validStringList(entry.Aliases, 100, 100) ||
		entry.IndexedAt.IsZero() {
		return ErrInvalidData
	}
	return nil
}

func validateEntry(entry Entry) error {
	if !validIdentifier(entry.ID) || !validText(entry.SourceKey, 256) || !validText(entry.Title, 200) ||
		!validText(entry.Category, 100) || !validEntryType(entry.Type) || !validStringList(entry.Keywords, 100, 100) ||
		!validStringList(entry.Aliases, 100, 100) || !validText(entry.Question, 4000) || !validText(entry.Answer, 20000) ||
		entry.IndexedAt.IsZero() {
		return ErrInvalidData
	}
	return nil
}

func validateConflictPage(page ConflictPage) error {
	if len(page.Items) > 100 || !validCursor(page.NextCursor) || (page.HasMore && page.NextCursor == "") {
		return ErrInvalidData
	}
	for _, conflict := range page.Items {
		if !validIdentifier(conflict.ID) || !validConflictType(conflict.Type) || !validText(conflict.Key, 256) ||
			len(conflict.EntryIDs) < 2 || conflict.DetectedAt.IsZero() {
			return ErrInvalidData
		}
		seen := make(map[string]struct{}, len(conflict.EntryIDs))
		for _, id := range conflict.EntryIDs {
			if !validIdentifier(id) {
				return ErrInvalidData
			}
			if _, exists := seen[id]; exists {
				return ErrInvalidData
			}
			seen[id] = struct{}{}
		}
	}
	return nil
}

func validState(value State) bool {
	switch value {
	case StateReady, StateReloading, StateDegraded, StateUnavailable, StateNotConfigured:
		return true
	default:
		return false
	}
}

func validOperationStatus(value OperationStatus) bool {
	switch value {
	case OperationAccepted, OperationRunning, OperationSucceeded, OperationFailed:
		return true
	default:
		return false
	}
}

func validEntryType(value EntryType) bool {
	return value == EntryTypeExactReply || value == EntryTypeAIKnowledge || value == EntryTypeHybrid
}

func validOptionalEntryType(value EntryType) bool {
	return value == "" || validEntryType(value)
}

func validConflictType(value ConflictType) bool {
	return value == ConflictSourceKey || value == ConflictKeyword || value == ConflictAlias
}

func validOptionalConflictType(value ConflictType) bool {
	return value == "" || validConflictType(value)
}

func validIdentifier(value string) bool {
	return validText(value, 256) && value != ""
}

func validCursor(value string) bool {
	return validText(value, 2048)
}

func validText(value string, maximum int) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validOptionalText(value *string, maximum int) bool {
	return value == nil || validText(*value, maximum)
}

func validOptionalErrorCode(value *string) bool {
	return value == nil || errorCodePattern.MatchString(*value)
}

func validStringList(values []string, maximumItems, maximumLength int) bool {
	if len(values) > maximumItems {
		return false
	}
	for _, value := range values {
		if !validText(value, maximumLength) {
			return false
		}
	}
	return true
}

func cloneStatus(value Status) Status {
	value.ActiveIndexVersion = cloneString(value.ActiveIndexVersion)
	value.LastAttemptAt = cloneTime(value.LastAttemptAt)
	value.LastSuccessAt = cloneTime(value.LastSuccessAt)
	value.LastErrorCode = cloneString(value.LastErrorCode)
	if value.CurrentOperation != nil {
		operation := cloneOperation(*value.CurrentOperation)
		value.CurrentOperation = &operation
	}
	return value
}

func cloneOverlay(value reloadOverlay) reloadOverlay {
	value.operation = cloneOperation(value.operation)
	if value.result != nil {
		result := *value.result
		value.result = &result
	}
	return value
}

func cloneOperation(value ReloadOperation) ReloadOperation {
	value.CompletedAt = cloneTime(value.CompletedAt)
	value.ErrorCode = cloneString(value.ErrorCode)
	return value
}

func cloneEntryPage(value EntryPage) EntryPage {
	items := make([]EntrySummary, len(value.Items))
	for index := range value.Items {
		items[index] = cloneEntrySummary(value.Items[index])
	}
	value.Items = items
	return value
}

func cloneEntrySummary(value EntrySummary) EntrySummary {
	value.Keywords = append([]string(nil), value.Keywords...)
	value.Aliases = append([]string(nil), value.Aliases...)
	value.SourceUpdatedAt = cloneTime(value.SourceUpdatedAt)
	return value
}

func cloneEntry(value Entry) Entry {
	value.Keywords = append([]string(nil), value.Keywords...)
	value.Aliases = append([]string(nil), value.Aliases...)
	value.SourceUpdatedAt = cloneTime(value.SourceUpdatedAt)
	return value
}

func cloneConflictPage(value ConflictPage) ConflictPage {
	items := make([]Conflict, len(value.Items))
	for index := range value.Items {
		items[index] = value.Items[index]
		items[index].EntryIDs = append([]string(nil), value.Items[index].EntryIDs...)
	}
	value.Items = items
	return value
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func timePointer(value time.Time) *time.Time {
	copy := value.UTC()
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func stringPointer(value string) *string {
	copy := value
	return &copy
}
