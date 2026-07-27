package joinrequests

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
)

var (
	ErrForbidden             = errors.New("join request operation forbidden")
	ErrInvalidInput          = errors.New("invalid join request input")
	ErrInvalidData           = errors.New("invalid join request data")
	ErrNotFound              = errors.New("join request not found")
	ErrConflict              = errors.New("join request state conflict")
	ErrIdempotencyConflict   = errors.New("join request idempotency conflict")
	ErrDependencyUnavailable = errors.New("join request dependency unavailable")
	ErrExternalFailure       = errors.New("join request external action failed")
)

type Store interface {
	GetPolicy(ctx context.Context, groupID string) (Policy, bool, error)
	UpdatePolicy(ctx context.Context, mutation PolicyMutation) (Policy, error)
	ListRequests(ctx context.Context, query ListQuery) (Page[Request], error)
	GetRequest(ctx context.Context, requestID string) (Request, bool, error)
	ListDecisions(ctx context.Context, query DecisionListQuery) (Page[Decision], bool, error)
	// BeginDecisions atomically verifies every revision and pending state, creates
	// the append-only attempts, and moves every request to processing.
	BeginDecisions(ctx context.Context, mutation BeginMutation) (Reservation, error)
	// CompleteDecision atomically finalizes the attempt and request state.
	CompleteDecision(ctx context.Context, mutation CompletionMutation) (DecisionResult, error)
	ListAutoCandidates(ctx context.Context, limit int) ([]AutoCandidate, error)
	// RecoverExpiredDecisions marks expired processing leases unknown. It must
	// never restore pending because the external call may already have run.
	RecoverExpiredDecisions(ctx context.Context, expiredBefore time.Time, limit int) ([]Request, error)
}

type PolicyMutation struct {
	Context          MutationContext
	GroupID          string
	ExpectedRevision uint64
	Patch            PolicyPatch
}

type Approver interface {
	JoinRequestDecisionAvailable() bool
	DecideJoinRequest(ctx context.Context, flag string, approve bool, reason string) ExternalResult
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type Options struct {
	Store              Store
	Approver           Approver
	Events             EventPublisher
	Now                func() time.Time
	OverdueAfter       time.Duration
	DecisionTimeout    time.Duration
	ProcessingLease    time.Duration
	PersistenceTimeout time.Duration
}

type Service struct {
	store              Store
	approver           Approver
	events             EventPublisher
	now                func() time.Time
	overdueAfter       time.Duration
	decisionTimeout    time.Duration
	processingLease    time.Duration
	persistenceTimeout time.Duration
}

const (
	defaultOverdueAfter       = 24 * time.Hour
	defaultDecisionTimeout    = 15 * time.Second
	defaultProcessingLease    = time.Minute
	defaultPersistenceTimeout = 5 * time.Second
)

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Approver == nil || options.Now == nil {
		return nil, ErrInvalidInput
	}
	overdueAfter := positiveDuration(options.OverdueAfter, defaultOverdueAfter)
	decisionTimeout := positiveDuration(options.DecisionTimeout, defaultDecisionTimeout)
	processingLease := positiveDuration(options.ProcessingLease, defaultProcessingLease)
	persistenceTimeout := positiveDuration(options.PersistenceTimeout, defaultPersistenceTimeout)
	return &Service{
		store: options.Store, approver: options.Approver, events: options.Events, now: options.Now,
		overdueAfter: overdueAfter, decisionTimeout: decisionTimeout, processingLease: processingLease,
		persistenceTimeout: persistenceTimeout,
	}, nil
}

func (s *Service) GetPolicy(ctx context.Context, principal auth.Principal, groupID string) (Policy, error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return Policy{}, ErrForbidden
	}
	if !validGroupID(groupID) {
		return Policy{}, ErrInvalidInput
	}
	value, found, err := s.store.GetPolicy(ctx, groupID)
	if err != nil {
		return Policy{}, fmt.Errorf("get join request policy: %w", err)
	}
	if !found {
		return Policy{}, ErrNotFound
	}
	if !validPolicy(value) || value.GroupID != groupID {
		return Policy{}, ErrInvalidData
	}
	return clonePolicy(value), nil
}

func (s *Service) UpdatePolicy(ctx context.Context, principal auth.Principal, groupID string, revision uint64, patch PolicyPatch, request auth.MutationContext) (Policy, error) {
	if !principal.Has(auth.PermissionJoinPoliciesWrite) {
		return Policy{}, ErrForbidden
	}
	if !validGroupID(groupID) || revision == 0 || !validMutationRequest(request) {
		return Policy{}, ErrInvalidInput
	}
	value, err := s.store.UpdatePolicy(ctx, PolicyMutation{
		Context: mutationContext(principal, request, s.now()), GroupID: groupID,
		ExpectedRevision: revision, Patch: patch,
	})
	if err != nil {
		return Policy{}, fmt.Errorf("update join request policy: %w", err)
	}
	if !validPolicy(value) || value.GroupID != groupID || value.Version != revision+1 || value.Enabled != patch.Enabled {
		return Policy{}, ErrInvalidData
	}
	s.publish(groupID, value.Version, "join_request_policy_updated")
	return clonePolicy(value), nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal, query ListQuery) (Page[Request], error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return Page[Request]{}, ErrForbidden
	}
	if query.Sort == "" {
		query.Sort = SortRequestedDesc
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if query.Overdue != nil {
		cutoff := s.now().UTC().Add(-s.overdueAfter)
		query.OverdueBefore = &cutoff
	}
	if !validListQuery(query) {
		return Page[Request]{}, ErrInvalidInput
	}
	page, err := s.store.ListRequests(ctx, cloneListQuery(query))
	if err != nil {
		return Page[Request]{}, fmt.Errorf("list join requests: %w", err)
	}
	items := make([]Request, len(page.Items))
	for index := range page.Items {
		if !validRequest(page.Items[index], false) {
			return Page[Request]{}, ErrInvalidData
		}
		items[index] = s.normalizeRequest(page.Items[index])
	}
	if page.NextCursor != "" && !validIdentifier(page.NextCursor, 256) {
		return Page[Request]{}, ErrInvalidData
	}
	return Page[Request]{Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore}, nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, requestID string) (Request, error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return Request{}, ErrForbidden
	}
	if !validRequestID(requestID) {
		return Request{}, ErrInvalidInput
	}
	value, found, err := s.store.GetRequest(ctx, requestID)
	if err != nil {
		return Request{}, fmt.Errorf("get join request: %w", err)
	}
	if !found {
		return Request{}, ErrNotFound
	}
	if !validRequest(value, true) || value.ID != requestID {
		return Request{}, ErrInvalidData
	}
	return s.normalizeRequest(value), nil
}

func (s *Service) ListDecisions(ctx context.Context, principal auth.Principal, query DecisionListQuery) (Page[Decision], error) {
	if !principal.Has(auth.PermissionJoinRequestsRead) {
		return Page[Decision]{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validDecisionListQuery(query) {
		return Page[Decision]{}, ErrInvalidInput
	}
	page, found, err := s.store.ListDecisions(ctx, query)
	if err != nil {
		return Page[Decision]{}, fmt.Errorf("list join request decisions: %w", err)
	}
	if !found {
		return Page[Decision]{}, ErrNotFound
	}
	items := make([]Decision, len(page.Items))
	for index := range page.Items {
		if !validDecision(page.Items[index]) || page.Items[index].RequestID != query.RequestID {
			return Page[Decision]{}, ErrInvalidData
		}
		items[index] = cloneDecision(page.Items[index])
	}
	if page.NextCursor != "" && !validIdentifier(page.NextCursor, 256) {
		return Page[Decision]{}, ErrInvalidData
	}
	return Page[Decision]{Items: items, NextCursor: page.NextCursor, HasMore: page.HasMore}, nil
}

func (s *Service) Decide(ctx context.Context, principal auth.Principal, requestID string, revision uint64, input DecisionInput, idempotencyKey string, request auth.MutationContext) (DecisionResult, error) {
	if !principal.Has(auth.PermissionJoinRequestsDecide) {
		return DecisionResult{}, ErrForbidden
	}
	if !validRequestID(requestID) || revision == 0 || !validDecisionInput(input) ||
		!idempotencyKeyPattern.MatchString(idempotencyKey) || !validMutationRequest(request) {
		return DecisionResult{}, ErrInvalidInput
	}
	if !s.approver.JoinRequestDecisionAvailable() {
		return DecisionResult{}, ErrDependencyUnavailable
	}
	reason := optionalReason(input.Reason)
	reservation, err := s.store.BeginDecisions(ctx, BeginMutation{
		Context: mutationContext(principal, request, s.now()), Items: []VersionedRequest{{ID: requestID, Version: revision}},
		Action: input.Action, Source: SourceManual, Reason: reason, IdempotencyKey: idempotencyKey,
		ProcessingExpiresAt: s.now().UTC().Add(s.processingLease),
	})
	if err != nil {
		return DecisionResult{}, fmt.Errorf("begin join request decision: %w", err)
	}
	if len(reservation.Items) != 1 {
		return DecisionResult{}, ErrInvalidData
	}
	item := reservation.Items[0]
	if reservation.Replay {
		return replayResult(item)
	}
	if !validReservedItem(item, requestID, input.Action) {
		return DecisionResult{}, ErrInvalidData
	}
	return s.execute(ctx, item, input.Reason)
}

func (s *Service) BulkDecide(ctx context.Context, principal auth.Principal, input BulkInput, idempotencyKey string, request auth.MutationContext) (BulkResult, error) {
	if !principal.Has(auth.PermissionJoinRequestsDecide) {
		return BulkResult{}, ErrForbidden
	}
	if !validBulkInput(input) || !idempotencyKeyPattern.MatchString(idempotencyKey) || !validMutationRequest(request) {
		return BulkResult{}, ErrInvalidInput
	}
	if !s.approver.JoinRequestDecisionAvailable() {
		return BulkResult{}, ErrDependencyUnavailable
	}
	reservation, err := s.store.BeginDecisions(ctx, BeginMutation{
		Context: mutationContext(principal, request, s.now()), GroupID: input.GroupID, Items: cloneVersionedRequests(input.Items),
		Action: input.Action, Source: SourceManual, Reason: optionalReason(input.Reason), IdempotencyKey: idempotencyKey,
		ProcessingExpiresAt: s.now().UTC().Add(s.processingLease),
	})
	if err != nil {
		return BulkResult{}, fmt.Errorf("begin bulk join request decisions: %w", err)
	}
	if !validReservation(reservation, input) {
		return BulkResult{}, ErrInvalidData
	}
	result := BulkResult{GroupID: input.GroupID, Action: input.Action, Items: make([]BulkItemResult, 0, len(reservation.Items))}
	for _, reserved := range reservation.Items {
		if reservation.Replay {
			item, err := replayBulkItem(reserved)
			if err != nil {
				return BulkResult{}, err
			}
			result.append(item)
			continue
		}
		if ctx.Err() != nil {
			result.append(s.completeWithoutExternal(reserved, "request_canceled"))
			continue
		}
		completed, executeErr := s.execute(ctx, reserved, input.Reason)
		result.append(bulkItemFromResult(reserved, completed, executeErr))
	}
	return cloneBulkResult(result), nil
}

func (s *Service) execute(ctx context.Context, item ReservedItem, reason string) (DecisionResult, error) {
	decisionContext, cancel := context.WithTimeout(ctx, s.decisionTimeout)
	external := s.approver.DecideJoinRequest(decisionContext, item.Request.ID, item.Decision.Action == ActionApprove, reason)
	cancel()
	if !validExternalResult(external) {
		external = ExternalResult{Outcome: ExternalUnknown, ErrorCode: "invalid_gateway_result"}
	}
	attemptStatus, decisionStatus := outcomeStatuses(external.Outcome, item.Decision.Action)
	completion := CompletionMutation{
		DecisionID: item.Decision.ID, RequestID: item.Request.ID, AttemptStatus: attemptStatus,
		DecisionStatus: decisionStatus, ErrorCode: optionalErrorCode(external.ErrorCode), CompletedAt: s.now().UTC(),
	}
	persistContext, persistCancel := context.WithTimeout(context.WithoutCancel(ctx), s.persistenceTimeout)
	result, err := s.store.CompleteDecision(persistContext, completion)
	persistCancel()
	if err != nil {
		return DecisionResult{}, fmt.Errorf("complete join request decision: %w", err)
	}
	if !validDecisionResult(result, item.Request.ID, item.Decision.ID, attemptStatus, decisionStatus) {
		return DecisionResult{}, ErrInvalidData
	}
	result.Request = s.normalizeRequest(result.Request)
	s.publish(result.Request.ID, result.Request.Version, "join_request_decided")
	switch external.Outcome {
	case ExternalFailed:
		return cloneDecisionResult(result), ErrExternalFailure
	case ExternalUnavailable:
		return cloneDecisionResult(result), ErrDependencyUnavailable
	default:
		return cloneDecisionResult(result), nil
	}
}

func (s *Service) completeWithoutExternal(item ReservedItem, errorCode string) BulkItemResult {
	completion := CompletionMutation{
		DecisionID: item.Decision.ID, RequestID: item.Request.ID, AttemptStatus: AttemptFailed,
		DecisionStatus: DecisionPending, ErrorCode: &errorCode, CompletedAt: s.now().UTC(),
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.persistenceTimeout)
	result, err := s.store.CompleteDecision(ctx, completion)
	cancel()
	if err != nil || !validDecisionResult(result, item.Request.ID, item.Decision.ID, AttemptFailed, DecisionPending) {
		return BulkItemResult{
			RequestID: item.Request.ID, Outcome: ItemFailed, Request: cloneRequest(item.Request), Decision: cloneDecision(item.Decision),
			Error: &ItemError{Code: "persistence_unavailable", Message: "decision completion could not be persisted", Retryable: true},
		}
	}
	return BulkItemResult{
		RequestID: item.Request.ID, Outcome: ItemFailed, Request: s.normalizeRequest(result.Request), Decision: cloneDecision(result.Decision),
		Error: &ItemError{Code: errorCode, Message: "decision was not sent", Retryable: true},
	}
}

func (s *Service) normalizeRequest(value Request) Request {
	result := cloneRequest(value)
	result.Overdue = result.DecisionStatus == DecisionPending && !result.RequestedAt.After(s.now().UTC().Add(-s.overdueAfter))
	if result.AIParse.Fields != nil {
		fields := ValidateApplicantFields(*result.AIParse.Fields, result.VerificationMessage)
		result.AIParse.Fields = &fields
	}
	return result
}

func (s *Service) publish(id string, version uint64, reason string) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventJoinRequestUpdated, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceJoinRequest, ID: id, Version: version}, Reason: reason,
	})
}

func (r *BulkResult) append(item BulkItemResult) {
	r.Items = append(r.Items, item)
	switch item.Outcome {
	case ItemConfirmed:
		r.ConfirmedCount++
	case ItemUnknown:
		r.UnknownCount++
	default:
		r.FailedCount++
	}
}

func mutationContext(principal auth.Principal, request auth.MutationContext, at time.Time) MutationContext {
	userID := principal.UserID
	return MutationContext{
		Actor:   audit.Actor{Type: audit.ActorAdminUser, UserID: &userID, DisplayName: principal.UserID},
		Request: request, OccurredAt: at.UTC(),
	}
}

func outcomeStatuses(outcome ExternalOutcome, action Action) (AttemptStatus, DecisionStatus) {
	switch outcome {
	case ExternalConfirmed:
		if action == ActionApprove {
			return AttemptConfirmed, DecisionApproved
		}
		return AttemptConfirmed, DecisionRejected
	case ExternalUnknown:
		return AttemptUnknown, DecisionUnknown
	default:
		return AttemptFailed, DecisionPending
	}
}

func replayResult(item ReservedItem) (DecisionResult, error) {
	if item.Decision.Status == AttemptStarted || item.Request.DecisionStatus == DecisionProcessing {
		return DecisionResult{}, ErrConflict
	}
	if !validDecisionResult(DecisionResult{Request: item.Request, Decision: item.Decision}, item.Request.ID, item.Decision.ID, item.Decision.Status, item.Request.DecisionStatus) {
		return DecisionResult{}, ErrInvalidData
	}
	result := DecisionResult{Request: cloneRequest(item.Request), Decision: cloneDecision(item.Decision)}
	switch item.Decision.Status {
	case AttemptFailed:
		return result, ErrExternalFailure
	default:
		return result, nil
	}
}

func replayBulkItem(item ReservedItem) (BulkItemResult, error) {
	result, err := replayResult(item)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrInvalidData) {
		return BulkItemResult{}, err
	}
	return bulkItemFromResult(item, result, err), nil
}

func bulkItemFromResult(reserved ReservedItem, result DecisionResult, err error) BulkItemResult {
	item := BulkItemResult{RequestID: reserved.Request.ID, Request: result.Request, Decision: result.Decision}
	if item.Request.ID == "" {
		item.Request = cloneRequest(reserved.Request)
	}
	if item.Decision.ID == "" {
		item.Decision = cloneDecision(reserved.Decision)
	}
	switch {
	case errors.Is(err, ErrDependencyUnavailable):
		item.Outcome = ItemFailed
		item.Error = &ItemError{Code: "dependency_unavailable", Message: "NapCat was unavailable before the decision call", Retryable: true}
	case errors.Is(err, ErrExternalFailure):
		item.Outcome = ItemFailed
		item.Error = &ItemError{Code: valueOrDefault(result.Decision.ErrorCode, "upstream_failure"), Message: "NapCat rejected the decision", Retryable: true}
	case err != nil:
		item.Outcome = ItemFailed
		item.Error = &ItemError{Code: "persistence_unavailable", Message: "decision completion could not be persisted", Retryable: true}
	case result.Decision.Status == AttemptUnknown:
		item.Outcome = ItemUnknown
		item.Error = &ItemError{Code: valueOrDefault(result.Decision.ErrorCode, "outcome_unknown"), Message: "NapCat decision outcome is unknown", Retryable: false}
	default:
		item.Outcome = ItemConfirmed
	}
	return item
}

func validReservedItem(value ReservedItem, requestID string, action Action) bool {
	return validRequest(value.Request, true) && validDecision(value.Decision) && value.Request.ID == requestID &&
		value.Request.DecisionStatus == DecisionProcessing && value.Decision.RequestID == requestID &&
		value.Decision.Action == action && value.Decision.Status == AttemptStarted
}

func validReservation(value Reservation, input BulkInput) bool {
	if len(value.Items) != len(input.Items) {
		return false
	}
	expected := make(map[string]struct{}, len(input.Items))
	for _, item := range input.Items {
		expected[item.ID] = struct{}{}
	}
	for _, item := range value.Items {
		if item.Request.Group.ID != input.GroupID || item.Decision.Action != input.Action {
			return false
		}
		if _, ok := expected[item.Request.ID]; !ok {
			return false
		}
		delete(expected, item.Request.ID)
		if value.Replay {
			if !validRequest(item.Request, true) || !validDecision(item.Decision) ||
				item.Decision.RequestID != item.Request.ID {
				return false
			}
		} else if !validReservedItem(item, item.Request.ID, input.Action) {
			return false
		}
	}
	return len(expected) == 0
}

func validDecisionResult(value DecisionResult, requestID, decisionID string, attemptStatus AttemptStatus, decisionStatus DecisionStatus) bool {
	return validRequest(value.Request, true) && validDecision(value.Decision) && value.Request.ID == requestID &&
		value.Decision.ID == decisionID && value.Decision.RequestID == requestID && value.Decision.Status == attemptStatus &&
		value.Request.DecisionStatus == decisionStatus
}

func positiveDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func optionalReason(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func optionalErrorCode(value string) *string {
	if value == "" {
		return nil
	}
	copy := value
	return &copy
}

func valueOrDefault(value *string, fallback string) string {
	if value == nil || *value == "" {
		return fallback
	}
	return *value
}

func clonePolicy(value Policy) Policy {
	value.RequiredFields = append([]string(nil), value.RequiredFields...)
	value.UpdatedBy = cloneActor(value.UpdatedBy)
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value
}

func cloneRequest(value Request) Request {
	value.ApplicantNickname = cloneString(value.ApplicantNickname)
	value.DecisionSource = cloneDecisionSource(value.DecisionSource)
	value.LastDecisionID = cloneString(value.LastDecisionID)
	value.Comment = cloneString(value.Comment)
	value.AIParse = cloneAIParse(value.AIParse)
	value.RequestedAt = value.RequestedAt.UTC()
	value.FirstObservedAt = utcOrZero(value.FirstObservedAt)
	value.LastObservedAt = utcOrZero(value.LastObservedAt)
	return value
}

func cloneDecision(value Decision) Decision {
	value.Actor = cloneActor(value.Actor)
	value.Reason = cloneString(value.Reason)
	value.RuleVersion = cloneUint64(value.RuleVersion)
	value.FieldSnapshot = cloneApplicantFieldsPointer(value.FieldSnapshot)
	value.StartedAt = value.StartedAt.UTC()
	value.CompletedAt = cloneTime(value.CompletedAt)
	value.ErrorCode = cloneString(value.ErrorCode)
	return value
}

func cloneDecisionResult(value DecisionResult) DecisionResult {
	return DecisionResult{Request: cloneRequest(value.Request), Decision: cloneDecision(value.Decision)}
}

func cloneBulkResult(value BulkResult) BulkResult {
	result := value
	result.Items = make([]BulkItemResult, len(value.Items))
	for index, item := range value.Items {
		result.Items[index] = item
		result.Items[index].Request = cloneRequest(item.Request)
		result.Items[index].Decision = cloneDecision(item.Decision)
		if item.Error != nil {
			copy := *item.Error
			result.Items[index].Error = &copy
		}
	}
	return result
}

func cloneAIParse(value AIParseResult) AIParseResult {
	value.Fields = cloneApplicantFieldsPointer(value.Fields)
	value.ErrorCode = cloneString(value.ErrorCode)
	value.CompletedAt = cloneTime(value.CompletedAt)
	return value
}

func cloneApplicantFields(value ApplicantFields) ApplicantFields {
	value.StudentID = cloneString(value.StudentID)
	value.Name = cloneString(value.Name)
	value.Major = cloneString(value.Major)
	value.ValidationErrors = append([]string{}, value.ValidationErrors...)
	return value
}

func cloneApplicantFieldsPointer(value *ApplicantFields) *ApplicantFields {
	if value == nil {
		return nil
	}
	copy := cloneApplicantFields(*value)
	return &copy
}

func cloneActor(value *audit.Actor) *audit.Actor {
	if value == nil {
		return nil
	}
	copy := *value
	copy.UserID = cloneString(value.UserID)
	copy.QQUserID = cloneString(value.QQUserID)
	return &copy
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneDecisionSource(value *DecisionSource) *DecisionSource {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneUint64(value *uint64) *uint64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func utcOrZero(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC()
}

func cloneVersionedRequests(value []VersionedRequest) []VersionedRequest {
	return append([]VersionedRequest(nil), value...)
}

func cloneListQuery(value ListQuery) ListQuery {
	value.DecisionStatuses = append([]DecisionStatus(nil), value.DecisionStatuses...)
	value.RequestedFrom = cloneTime(value.RequestedFrom)
	value.RequestedTo = cloneTime(value.RequestedTo)
	value.OverdueBefore = cloneTime(value.OverdueBefore)
	return value
}
