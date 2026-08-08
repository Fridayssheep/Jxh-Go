package groups

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
)

var (
	ErrForbidden           = errors.New("group operation forbidden")
	ErrInvalidInput        = errors.New("invalid group input")
	ErrInvalidData         = errors.New("invalid group data")
	ErrNotFound            = errors.New("group not found")
	ErrConflict            = errors.New("group sync conflict")
	ErrIdempotencyConflict = errors.New("group sync idempotency conflict")
	ErrGatewayUnavailable  = errors.New("group gateway unavailable")
	ErrNoticeInProgress    = errors.New("group notice publication is already in progress")
)

type Role string

const (
	RoleOwner   Role = "owner"
	RoleAdmin   Role = "admin"
	RoleMember  Role = "member"
	RoleUnknown Role = "unknown"
)

type SnapshotState string

const (
	SnapshotFresh SnapshotState = "fresh"
	SnapshotStale SnapshotState = "stale"
)

type FeatureKey string

const (
	FeatureKeywordReply  FeatureKey = "keyword_reply"
	FeatureAIQA          FeatureKey = "ai_qa"
	FeatureQuote         FeatureKey = "quote"
	FeatureLinkCleaner   FeatureKey = "link_cleaner"
	FeatureWelcome       FeatureKey = "welcome"
	FeatureCustomCommand FeatureKey = "custom_commands"
)

type FeatureSource string

const (
	FeatureGlobal        FeatureSource = "global"
	FeatureGroupOverride FeatureSource = "group_override"
)

type Feature struct {
	Key     FeatureKey
	Enabled bool
	Source  FeatureSource
}

type JoinRequestPolicySummary struct {
	Enabled    bool
	AutoReject bool
	Version    uint64
}

type Group struct {
	ID                string
	Name              string
	MemberCount       uint64
	MaxMemberCount    uint64
	BotRole           Role
	SnapshotState     SnapshotState
	LastSyncedAt      time.Time
	Features          []Feature
	JoinRequestPolicy JoinRequestPolicySummary
}

type ListQuery struct {
	Query          string
	BotRole        Role
	SnapshotState  SnapshotState
	FeatureKey     FeatureKey
	FeatureEnabled *bool
	Cursor         string
	Limit          int
}

type StoreListQuery struct {
	ListQuery
	ForceStale  bool
	StaleBefore time.Time
}

type Page struct {
	Items      []Group
	NextCursor string
	HasMore    bool
}

type MutationContext struct {
	Actor      auth.Principal
	Request    auth.MutationContext
	OccurredAt time.Time
}

type BeginSync struct {
	Context        MutationContext
	IdempotencyKey string
}

type SyncResult struct {
	SyncedAt     time.Time
	AddedCount   uint64
	UpdatedCount uint64
	RemovedCount uint64
	TotalCount   uint64
}

type SyncReservation struct {
	ExecutionID string
	Fresh       bool
	InProgress  bool
	Result      *SyncResult
	FailureCode string
}

type RemoteGroup struct {
	ID             string
	Name           string
	MemberCount    uint64
	MaxMemberCount uint64
	BotRole        Role
}

type CompleteSync struct {
	ExecutionID string
	CompletedAt time.Time
	Groups      []RemoteGroup
}

type FailSync struct {
	ExecutionID string
	CompletedAt time.Time
	ErrorCode   string
}

type Store interface {
	ListGroups(ctx context.Context, query StoreListQuery) (Page, error)
	GetGroup(ctx context.Context, id string) (Group, bool, error)
	// BeginGroupSync atomically reserves the idempotency key and appends the
	// requested audit record. A replay returns the prior result or failure.
	BeginGroupSync(ctx context.Context, begin BeginSync) (SyncReservation, error)
	// CompleteGroupSync atomically upserts the new directory, archives missing
	// groups, completes the reservation and appends the success audit record.
	CompleteGroupSync(ctx context.Context, completion CompleteSync) (SyncResult, error)
	// FailGroupSync completes the reservation and audit without changing the
	// last successful group directory snapshot.
	FailGroupSync(ctx context.Context, failure FailSync) error
	RecoverInterruptedGroupSyncs(ctx context.Context, recoveredAt time.Time) (int, error)
	BeginGroupNoticePublication(ctx context.Context, begin BeginNoticePublication) (NoticeReservation, error)
	CompleteGroupNoticePublication(ctx context.Context, completion CompleteNoticePublication) (NoticePublishResult, error)
	RecoverInterruptedGroupNoticePublications(ctx context.Context, recoveredAt time.Time) (int, error)
}

type Gateway interface {
	Snapshot() napcat.Snapshot
	GetGroupList(ctx context.Context) ([]napcat.GroupInfo, error)
	GetLoginUserID(ctx context.Context) (int64, error)
	GetGroupMemberRole(ctx context.Context, groupID, userID int64) (string, error)
	PublishGroupNotice(ctx context.Context, groupID int64, content string) error
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type Options struct {
	Store                 Store
	Gateway               Gateway
	Events                EventPublisher
	Now                   func() time.Time
	StaleAfter            time.Duration
	SyncTimeout           time.Duration
	MaxRoleWorkers        int
	WorkerContext         context.Context
	PersistenceRetryDelay time.Duration
	IdempotencySecret     []byte
	MaxNoticeWorkers      int
}

type Service struct {
	store          Store
	gateway        Gateway
	events         EventPublisher
	now            func() time.Time
	staleAfter     time.Duration
	syncTimeout    time.Duration
	maxRoleWorkers int
	workerCtx      context.Context
	cancel         context.CancelFunc
	retryDelay     time.Duration
	idempotencyKey []byte
	noticeWorkers  int
	lifecycleMu    sync.Mutex
	closed         bool
	wait           sync.WaitGroup
}

var idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

var featureOrder = []FeatureKey{
	FeatureKeywordReply, FeatureAIQA, FeatureQuote, FeatureLinkCleaner, FeatureWelcome, FeatureCustomCommand,
}

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Gateway == nil || options.Now == nil || len(options.IdempotencySecret) < 32 {
		return nil, ErrInvalidInput
	}
	if options.StaleAfter <= 0 {
		options.StaleAfter = 5 * time.Minute
	}
	if options.SyncTimeout <= 0 {
		options.SyncTimeout = 30 * time.Second
	}
	if options.MaxRoleWorkers <= 0 {
		options.MaxRoleWorkers = 8
	}
	if options.MaxRoleWorkers > 32 {
		return nil, ErrInvalidInput
	}
	if options.MaxNoticeWorkers <= 0 {
		options.MaxNoticeWorkers = 4
	}
	if options.MaxNoticeWorkers > 8 {
		return nil, ErrInvalidInput
	}
	if options.PersistenceRetryDelay <= 0 {
		options.PersistenceRetryDelay = time.Second
	}
	workerContext := options.WorkerContext
	if workerContext == nil {
		workerContext = context.Background()
	}
	workerContext, cancel := context.WithCancel(workerContext)
	return &Service{
		store: options.Store, gateway: options.Gateway, events: options.Events, now: options.Now,
		staleAfter: options.StaleAfter, syncTimeout: options.SyncTimeout, maxRoleWorkers: options.MaxRoleWorkers,
		workerCtx: workerContext, cancel: cancel, retryDelay: options.PersistenceRetryDelay,
		idempotencyKey: append([]byte(nil), options.IdempotencySecret...), noticeWorkers: options.MaxNoticeWorkers,
	}, nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal, query ListQuery) (Page, error) {
	if !principal.Has(auth.PermissionGroupsRead) {
		return Page{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validListQuery(query) {
		return Page{}, ErrInvalidInput
	}
	now := s.now().UTC()
	forceStale := !s.gateway.Snapshot().Connected
	page, err := s.store.ListGroups(ctx, StoreListQuery{
		ListQuery: query, ForceStale: forceStale, StaleBefore: now.Add(-s.staleAfter),
	})
	if err != nil {
		return Page{}, fmt.Errorf("list groups: %w", err)
	}
	for index := range page.Items {
		if err := validateGroup(page.Items[index]); err != nil {
			return Page{}, err
		}
		page.Items[index] = normalizeGroup(page.Items[index], forceStale, now.Add(-s.staleAfter))
	}
	return clonePage(page), nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, id string) (Group, error) {
	if !principal.Has(auth.PermissionGroupsRead) {
		return Group{}, ErrForbidden
	}
	if !validGroupID(id) {
		return Group{}, ErrInvalidInput
	}
	group, found, err := s.store.GetGroup(ctx, id)
	if err != nil {
		return Group{}, fmt.Errorf("get group: %w", err)
	}
	if !found {
		return Group{}, ErrNotFound
	}
	if err := validateGroup(group); err != nil {
		return Group{}, err
	}
	group = normalizeGroup(group, !s.gateway.Snapshot().Connected, s.now().UTC().Add(-s.staleAfter))
	return cloneGroup(group), nil
}

func (s *Service) Sync(ctx context.Context, principal auth.Principal, idempotencyKey string, request auth.MutationContext) (SyncResult, error) {
	if !principal.Has(auth.PermissionGroupsSync) {
		return SyncResult{}, ErrForbidden
	}
	if principal.UserID == "" || !idempotencyPattern.MatchString(idempotencyKey) || !validRequest(request) {
		return SyncResult{}, ErrInvalidInput
	}
	startedAt := s.now().UTC()
	reservation, err := s.store.BeginGroupSync(ctx, BeginSync{
		Context: MutationContext{Actor: principal, Request: request, OccurredAt: startedAt}, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return SyncResult{}, fmt.Errorf("begin group sync: %w", err)
	}
	if !reservation.Fresh {
		return replaySync(reservation)
	}
	if reservation.ExecutionID == "" {
		return SyncResult{}, ErrInvalidData
	}
	if !s.gateway.Snapshot().Connected {
		return SyncResult{}, s.failSync(ctx, reservation.ExecutionID, "napcat_unavailable", ErrGatewayUnavailable)
	}
	syncContext, cancel := context.WithTimeout(ctx, s.syncTimeout)
	remote, err := s.loadRemoteGroups(syncContext)
	cancel()
	if err != nil {
		return SyncResult{}, s.failSync(ctx, reservation.ExecutionID, "group_list_unavailable", ErrGatewayUnavailable)
	}
	completedAt := s.now().UTC()
	completionContext, completionCancel := context.WithTimeout(context.Background(), s.syncTimeout)
	result, err := s.store.CompleteGroupSync(completionContext, CompleteSync{
		ExecutionID: reservation.ExecutionID, CompletedAt: completedAt, Groups: cloneRemoteGroups(remote),
	})
	completionCancel()
	if err != nil {
		s.scheduleCompletionRetry(CompleteSync{
			ExecutionID: reservation.ExecutionID, CompletedAt: completedAt, Groups: cloneRemoteGroups(remote),
		})
		return SyncResult{}, fmt.Errorf("complete group sync: %w", err)
	}
	if !validSyncResult(result) {
		return SyncResult{}, ErrInvalidData
	}
	s.publishSync()
	return result, nil
}

func (s *Service) RecoverInterruptedSyncs(ctx context.Context) (int, error) {
	count, err := s.store.RecoverInterruptedGroupSyncs(ctx, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("recover interrupted group syncs: %w", err)
	}
	return count, nil
}

func (s *Service) loadRemoteGroups(ctx context.Context) ([]RemoteGroup, error) {
	values, err := s.gateway.GetGroupList(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]RemoteGroup, len(values))
	seen := make(map[int64]struct{}, len(values))
	for index, value := range values {
		if value.ID <= 0 || !validName(value.Name) || value.MemberCount < 0 || value.MaxMemberCount < 0 ||
			(value.MaxMemberCount > 0 && value.MemberCount > value.MaxMemberCount) {
			return nil, ErrInvalidData
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return nil, ErrInvalidData
		}
		seen[value.ID] = struct{}{}
		groups[index] = RemoteGroup{
			ID: strconv.FormatInt(value.ID, 10), Name: value.Name, MemberCount: uint64(value.MemberCount),
			MaxMemberCount: uint64(value.MaxMemberCount), BotRole: RoleUnknown,
		}
	}
	selfID, err := s.gateway.GetLoginUserID(ctx)
	if err != nil || len(values) == 0 {
		return groups, nil
	}
	s.loadRoles(ctx, values, groups, selfID)
	return groups, nil
}

func (s *Service) loadRoles(ctx context.Context, values []napcat.GroupInfo, groups []RemoteGroup, selfID int64) {
	workerCount := min(s.maxRoleWorkers, len(values))
	jobs := make(chan int)
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				role, err := s.gateway.GetGroupMemberRole(ctx, values[index].ID, selfID)
				if err == nil {
					groups[index].BotRole = normalizeRole(role)
				}
			}
		}()
	}
	for index := range values {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			return
		}
	}
	close(jobs)
	workers.Wait()
}

func (s *Service) failSync(_ context.Context, executionID, code string, result error) error {
	failure := FailSync{ExecutionID: executionID, CompletedAt: s.now().UTC(), ErrorCode: code}
	failureContext, failureCancel := context.WithTimeout(context.Background(), s.syncTimeout)
	err := s.store.FailGroupSync(failureContext, failure)
	failureCancel()
	if err != nil {
		s.scheduleFailureRetry(failure)
		return fmt.Errorf("record group sync failure: %w", err)
	}
	return result
}

func (s *Service) scheduleCompletionRetry(completion CompleteSync) {
	completion.Groups = cloneRemoteGroups(completion.Groups)
	s.startRetryWorker(func(ctx context.Context) bool {
		result, err := s.store.CompleteGroupSync(ctx, completion)
		if err != nil {
			return false
		}
		if validSyncResult(result) {
			s.publishSync()
		}
		return true
	})
}

func (s *Service) scheduleFailureRetry(failure FailSync) {
	s.startRetryWorker(func(ctx context.Context) bool {
		return s.store.FailGroupSync(ctx, failure) == nil
	})
}

func (s *Service) startRetryWorker(attempt func(context.Context) bool) {
	s.lifecycleMu.Lock()
	if s.closed {
		s.lifecycleMu.Unlock()
		return
	}
	s.wait.Add(1)
	s.lifecycleMu.Unlock()
	go func() {
		defer s.wait.Done()
		for {
			timer := time.NewTimer(s.retryDelay)
			select {
			case <-s.workerCtx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return
			case <-timer.C:
			}
			attemptContext, cancel := context.WithTimeout(s.workerCtx, s.syncTimeout)
			completed := attempt(attemptContext)
			cancel()
			if completed {
				return
			}
		}
	}()
}

func (s *Service) Close() {
	s.lifecycleMu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.lifecycleMu.Unlock()
	s.wait.Wait()
}

func (s *Service) publishSync() {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventGroupUpdated, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceGroup}, Reason: "directory_sync",
	})
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventOverviewUpdated, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceOverview}, Reason: "group_directory_sync",
	})
}

func replaySync(reservation SyncReservation) (SyncResult, error) {
	if reservation.Result != nil {
		if !validSyncResult(*reservation.Result) {
			return SyncResult{}, ErrInvalidData
		}
		return *reservation.Result, nil
	}
	if reservation.FailureCode != "" {
		return SyncResult{}, ErrGatewayUnavailable
	}
	if reservation.InProgress {
		return SyncResult{}, ErrConflict
	}
	return SyncResult{}, ErrInvalidData
}

func validListQuery(query ListQuery) bool {
	return validOptionalText(query.Query, 100) && (query.BotRole == "" || validRole(query.BotRole)) &&
		(query.SnapshotState == "" || query.SnapshotState == SnapshotFresh || query.SnapshotState == SnapshotStale) &&
		(query.FeatureKey == "" || validFeatureKey(query.FeatureKey)) && (query.FeatureEnabled == nil || query.FeatureKey != "") &&
		(query.Cursor == "" || validID(query.Cursor)) && query.Limit >= 1 && query.Limit <= 100
}

func validateGroup(group Group) error {
	if !validGroupID(group.ID) || !validName(group.Name) || !validRole(group.BotRole) ||
		(group.SnapshotState != SnapshotFresh && group.SnapshotState != SnapshotStale) || group.LastSyncedAt.IsZero() ||
		group.LastSyncedAt.Location() != time.UTC || group.MaxMemberCount > 0 && group.MemberCount > group.MaxMemberCount ||
		len(group.Features) != len(featureOrder) || group.JoinRequestPolicy.Version == 0 {
		return ErrInvalidData
	}
	seen := make(map[FeatureKey]struct{}, len(group.Features))
	for _, feature := range group.Features {
		if !validFeatureKey(feature.Key) || (feature.Source != FeatureGlobal && feature.Source != FeatureGroupOverride) {
			return ErrInvalidData
		}
		if _, duplicate := seen[feature.Key]; duplicate {
			return ErrInvalidData
		}
		seen[feature.Key] = struct{}{}
	}
	return nil
}

func validSyncResult(value SyncResult) bool {
	return !value.SyncedAt.IsZero() && value.SyncedAt.Location() == time.UTC &&
		value.AddedCount <= value.TotalCount && value.UpdatedCount <= value.TotalCount-value.AddedCount
}

func validRequest(value auth.MutationContext) bool {
	return validText(value.RequestID, 256) && validOptionalText(value.IPAddress, 64) && validOptionalText(value.UserAgent, 300)
}

func validGroupID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return strings.TrimLeft(value, "0") != ""
}

func validName(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 100
}

func validRole(value Role) bool {
	return value == RoleOwner || value == RoleAdmin || value == RoleMember || value == RoleUnknown
}

func validFeatureKey(value FeatureKey) bool {
	for _, candidate := range featureOrder {
		if value == candidate {
			return true
		}
	}
	return false
}

func validID(value string) bool {
	return validText(value, 256)
}

func validText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validOptionalText(value string, maximum int) bool {
	return value == "" || validText(value, maximum)
}

func normalizeRole(value string) Role {
	switch Role(value) {
	case RoleOwner:
		return RoleOwner
	case RoleAdmin:
		return RoleAdmin
	case RoleMember:
		return RoleMember
	default:
		return RoleUnknown
	}
}

func normalizeGroup(value Group, forceStale bool, staleBefore time.Time) Group {
	if forceStale || value.LastSyncedAt.Before(staleBefore) {
		value.SnapshotState = SnapshotStale
	}
	return value
}

func clonePage(value Page) Page {
	result := Page{Items: make([]Group, len(value.Items)), NextCursor: value.NextCursor, HasMore: value.HasMore}
	for index := range value.Items {
		result.Items[index] = cloneGroup(value.Items[index])
	}
	return result
}

func cloneGroup(value Group) Group {
	value.LastSyncedAt = value.LastSyncedAt.UTC()
	value.Features = append([]Feature(nil), value.Features...)
	return value
}

func cloneRemoteGroups(values []RemoteGroup) []RemoteGroup {
	return append([]RemoteGroup(nil), values...)
}
