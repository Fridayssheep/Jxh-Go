package scheduledjobs

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/audit"
	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/events"
	"github.com/zjutjh/jxh-go/internal/napcat"
)

var (
	ErrForbidden           = errors.New("scheduled job operation forbidden")
	ErrInvalidInput        = errors.New("invalid scheduled job input")
	ErrNotFound            = errors.New("scheduled job not found")
	ErrConflict            = errors.New("scheduled job conflict")
	ErrIdempotencyConflict = errors.New("scheduled job idempotency conflict")
	ErrSenderUnavailable   = errors.New("scheduled job sender unavailable")
)

type JobType string

const (
	TypeDaily JobType = "daily"
	TypeOnce  JobType = "once"
)

type Status string

const (
	StatusActive    Status = "active"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusArchived  Status = "archived"
)

type RunResult string

const (
	RunSuccess RunResult = "success"
	RunFailed  RunResult = "failed"
	RunUnknown RunResult = "unknown"
	RunSkipped RunResult = "skipped"
)

type RunKind string

const (
	RunScheduled RunKind = "scheduled"
	RunTest      RunKind = "test"
)

type Group struct {
	ID   string
	Name string
}

type Schedule struct {
	Type      JobType
	LocalTime string
	Timezone  string
	RunAt     *time.Time
}

type Job struct {
	ID            string
	Name          string
	Group         Group
	Message       string
	Type          JobType
	Schedule      Schedule
	Status        Status
	NextRunAt     *time.Time
	LastRunAt     *time.Time
	LastRunResult *RunResult
	Version       uint64
	CreatedAt     time.Time
	UpdatedAt     time.Time
	UpdatedBy     audit.Actor
}

type Run struct {
	ID           string
	JobID        string
	Kind         RunKind
	Result       RunResult
	ScheduledFor *time.Time
	StartedAt    time.Time
	CompletedAt  *time.Time
	Duration     time.Duration
	MessageID    *string
	ErrorCode    *string
	ErrorMessage *string
	TriggeredBy  *audit.Actor
}

type CreateInput struct {
	Name     string
	GroupID  string
	Message  string
	Schedule Schedule
	Enabled  bool
}

type Patch struct {
	Name     auth.Field[string]
	GroupID  auth.Field[string]
	Message  auth.Field[string]
	Schedule auth.Field[Schedule]
	Status   auth.Field[Status]
}

type ListQuery struct {
	GroupID   string
	Type      JobType
	Status    Status
	RunResult RunResult
	Cursor    string
	Limit     int
}

type RunListQuery struct {
	JobID  string
	Kind   RunKind
	Result RunResult
	From   *time.Time
	To     *time.Time
	Cursor string
	Limit  int
}

type Page[T any] struct {
	Items      []T
	NextCursor string
	HasMore    bool
}

type MutationContext struct {
	Actor      auth.Principal
	Request    auth.MutationContext
	OccurredAt time.Time
}

type CreateMutation struct {
	Context   MutationContext
	Input     CreateInput
	NextRunAt *time.Time
}

type UpdateMutation struct {
	Context          MutationContext
	JobID            string
	ExpectedRevision uint64
	Patch            Patch
	NextRunAt        auth.Field[*time.Time]
}

type ArchiveMutation struct {
	Context          MutationContext
	JobID            string
	ExpectedRevision uint64
}

type TestSendBegin struct {
	Context          MutationContext
	JobID            string
	ExpectedRevision uint64
	IdempotencyKey   string
}

type TestSendReservation struct {
	ExecutionID string
	Job         Job
	Run         Run
	Fresh       bool
}

type TestSendCompletion struct {
	ExecutionID  string
	RunID        string
	Result       RunResult
	CompletedAt  time.Time
	Duration     time.Duration
	MessageID    string
	ErrorCode    string
	ErrorMessage string
}

type Store interface {
	CreateScheduledJob(ctx context.Context, mutation CreateMutation) (Job, error)
	GetScheduledJob(ctx context.Context, id string) (Job, bool, error)
	ListScheduledJobs(ctx context.Context, query ListQuery) (Page[Job], error)
	UpdateScheduledJob(ctx context.Context, mutation UpdateMutation) (Job, error)
	ArchiveScheduledJob(ctx context.Context, mutation ArchiveMutation) error
	ListScheduledJobRuns(ctx context.Context, query RunListQuery) (Page[Run], error)
	// BeginTestSend atomically checks revision, reserves idempotency and writes a
	// started test run. Fresh=false returns the prior terminal run for replay.
	BeginScheduledJobTestSend(ctx context.Context, begin TestSendBegin) (TestSendReservation, error)
	CompleteScheduledJobTestSend(ctx context.Context, completion TestSendCompletion) (Run, error)
	// RecoverInterruptedScheduledJobRuns atomically changes unfinished runs left
	// by an earlier process to unknown without changing their parent jobs.
	RecoverInterruptedScheduledJobRuns(ctx context.Context, recoveredAt time.Time) (int, error)
}

type Sender interface {
	Available() bool
	Send(ctx context.Context, groupID string, message string) (messageID string, err error)
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type Options struct {
	Store                 Store
	Sender                Sender
	Events                EventPublisher
	Now                   func() time.Time
	SendTimeout           time.Duration
	PersistenceRetryDelay time.Duration
	WorkerContext         context.Context
}

type Service struct {
	store       Store
	sender      Sender
	events      EventPublisher
	now         func() time.Time
	sendTimeout time.Duration
	retryDelay  time.Duration
	workerCtx   context.Context
	cancel      context.CancelFunc
	lifecycleMu sync.Mutex
	closed      bool
	wait        sync.WaitGroup
}

var idempotencyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Now == nil {
		return nil, ErrInvalidInput
	}
	if options.SendTimeout <= 0 {
		options.SendTimeout = 30 * time.Second
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
		store: options.Store, sender: options.Sender, events: options.Events, now: options.Now,
		sendTimeout: options.SendTimeout, retryDelay: options.PersistenceRetryDelay,
		workerCtx: workerContext, cancel: cancel,
	}, nil
}

func (s *Service) Create(ctx context.Context, principal auth.Principal, input CreateInput, request auth.MutationContext) (Job, error) {
	if !principal.Has(auth.PermissionScheduledJobsWrite) {
		return Job{}, ErrForbidden
	}
	now := s.now().UTC()
	if !validCreate(input) || !validScheduleAt(input.Schedule, now) || !validRequest(request) {
		return Job{}, ErrInvalidInput
	}
	var nextRunAt *time.Time
	if input.Enabled {
		nextRunAt = calculateNextRun(input.Schedule, now)
	}
	job, err := s.store.CreateScheduledJob(ctx, CreateMutation{
		Context: mutationContext(principal, request, now), Input: cloneCreate(input), NextRunAt: nextRunAt,
	})
	if err != nil {
		return Job{}, fmt.Errorf("create scheduled job: %w", err)
	}
	s.publish(job, "created")
	return cloneJob(job), nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, id string) (Job, error) {
	if !principal.Has(auth.PermissionScheduledJobsRead) {
		return Job{}, ErrForbidden
	}
	if !validID(id) {
		return Job{}, ErrInvalidInput
	}
	job, found, err := s.store.GetScheduledJob(ctx, id)
	if err != nil {
		return Job{}, fmt.Errorf("get scheduled job: %w", err)
	}
	if !found {
		return Job{}, ErrNotFound
	}
	return cloneJob(job), nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal, query ListQuery) (Page[Job], error) {
	if !principal.Has(auth.PermissionScheduledJobsRead) {
		return Page[Job]{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validListQuery(query) {
		return Page[Job]{}, ErrInvalidInput
	}
	page, err := s.store.ListScheduledJobs(ctx, query)
	if err != nil {
		return Page[Job]{}, fmt.Errorf("list scheduled jobs: %w", err)
	}
	page.Items = cloneJobs(page.Items)
	return page, nil
}

func (s *Service) Update(ctx context.Context, principal auth.Principal, id string, revision uint64, patch Patch, request auth.MutationContext) (Job, error) {
	if !principal.Has(auth.PermissionScheduledJobsWrite) {
		return Job{}, ErrForbidden
	}
	now := s.now().UTC()
	if !validID(id) || revision == 0 || !validPatch(patch) ||
		(patch.Schedule.Set && !validScheduleAt(patch.Schedule.Value, now)) || !validRequest(request) {
		return Job{}, ErrInvalidInput
	}
	nextRunAt, err := s.nextRunForPatch(ctx, id, revision, patch, now)
	if err != nil {
		return Job{}, err
	}
	job, err := s.store.UpdateScheduledJob(ctx, UpdateMutation{
		Context: mutationContext(principal, request, now), JobID: id, ExpectedRevision: revision,
		Patch: clonePatch(patch), NextRunAt: nextRunAt,
	})
	if err != nil {
		return Job{}, fmt.Errorf("update scheduled job: %w", err)
	}
	s.publish(job, "updated")
	return cloneJob(job), nil
}

func (s *Service) nextRunForPatch(ctx context.Context, id string, revision uint64, patch Patch, now time.Time) (auth.Field[*time.Time], error) {
	if !patch.Schedule.Set && !patch.Status.Set {
		return auth.Field[*time.Time]{}, nil
	}
	current, found, err := s.store.GetScheduledJob(ctx, id)
	if err != nil {
		return auth.Field[*time.Time]{}, fmt.Errorf("get scheduled job for update: %w", err)
	}
	if !found {
		return auth.Field[*time.Time]{}, ErrNotFound
	}
	if current.Version != revision {
		return auth.Field[*time.Time]{}, ErrConflict
	}
	schedule := current.Schedule
	if patch.Schedule.Set {
		schedule = patch.Schedule.Value
	}
	status := current.Status
	if patch.Status.Set {
		status = patch.Status.Value
	}
	var next *time.Time
	if status == StatusActive {
		next = calculateNextRun(schedule, now)
	}
	return auth.Field[*time.Time]{Set: true, Value: next}, nil
}

func (s *Service) Archive(ctx context.Context, principal auth.Principal, id string, revision uint64, request auth.MutationContext) error {
	if !principal.Has(auth.PermissionScheduledJobsWrite) {
		return ErrForbidden
	}
	if !validID(id) || revision == 0 || !validRequest(request) {
		return ErrInvalidInput
	}
	if err := s.store.ArchiveScheduledJob(ctx, ArchiveMutation{
		Context: mutationContext(principal, request, s.now()), JobID: id, ExpectedRevision: revision,
	}); err != nil {
		return fmt.Errorf("archive scheduled job: %w", err)
	}
	s.publish(Job{ID: id}, "archived")
	return nil
}

func (s *Service) ListRuns(ctx context.Context, principal auth.Principal, query RunListQuery) (Page[Run], error) {
	if !principal.Has(auth.PermissionScheduledJobsRead) {
		return Page[Run]{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validRunListQuery(query) {
		return Page[Run]{}, ErrInvalidInput
	}
	if _, found, err := s.store.GetScheduledJob(ctx, query.JobID); err != nil {
		return Page[Run]{}, fmt.Errorf("check scheduled job: %w", err)
	} else if !found {
		return Page[Run]{}, ErrNotFound
	}
	page, err := s.store.ListScheduledJobRuns(ctx, query)
	if err != nil {
		return Page[Run]{}, fmt.Errorf("list scheduled job runs: %w", err)
	}
	page.Items = cloneRuns(page.Items)
	return page, nil
}

func (s *Service) TestSend(ctx context.Context, principal auth.Principal, id string, revision uint64, idempotencyKey string, request auth.MutationContext) (Run, error) {
	if !principal.Has(auth.PermissionScheduledJobsWrite) {
		return Run{}, ErrForbidden
	}
	if !validID(id) || revision == 0 || !idempotencyPattern.MatchString(idempotencyKey) || !validRequest(request) {
		return Run{}, ErrInvalidInput
	}
	if s.sender == nil || !s.sender.Available() {
		return Run{}, ErrSenderUnavailable
	}
	startedAt := s.now().UTC()
	reservation, err := s.store.BeginScheduledJobTestSend(ctx, TestSendBegin{
		Context: mutationContext(principal, request, startedAt), JobID: id,
		ExpectedRevision: revision, IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return Run{}, fmt.Errorf("begin scheduled job test send: %w", err)
	}
	if !reservation.Fresh {
		return cloneRun(reservation.Run), nil
	}
	sendContext, cancel := context.WithTimeout(context.Background(), s.sendTimeout)
	messageID, sendErr := s.sender.Send(sendContext, reservation.Job.Group.ID, reservation.Job.Message)
	cancel()
	completedAt := s.now().UTC()
	result, code, message := classifySend(sendErr)
	completion := TestSendCompletion{
		ExecutionID: reservation.ExecutionID, RunID: reservation.Run.ID, Result: result,
		CompletedAt: completedAt, Duration: nonNegativeDuration(completedAt.Sub(reservation.Run.StartedAt)),
		MessageID: safeText(messageID, 256), ErrorCode: code, ErrorMessage: message,
	}
	completionContext, completionCancel := context.WithTimeout(context.Background(), s.sendTimeout)
	run, err := s.store.CompleteScheduledJobTestSend(completionContext, completion)
	completionCancel()
	if err != nil {
		s.scheduleTestSendCompletionRetry(completion)
		return Run{}, fmt.Errorf("complete scheduled job test send: %w", err)
	}
	s.publishRun(run)
	return cloneRun(run), nil
}

func (s *Service) scheduleTestSendCompletionRetry(completion TestSendCompletion) {
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
			attemptContext, cancel := context.WithTimeout(s.workerCtx, s.sendTimeout)
			run, err := s.store.CompleteScheduledJobTestSend(attemptContext, completion)
			cancel()
			if err != nil {
				continue
			}
			s.publishRun(run)
			return
		}
	}()
}

func (s *Service) Close() {
	if s == nil {
		return
	}
	s.lifecycleMu.Lock()
	if !s.closed {
		s.closed = true
		s.cancel()
	}
	s.lifecycleMu.Unlock()
	s.wait.Wait()
}

func (s *Service) RecoverInterruptedRuns(ctx context.Context) (int, error) {
	recovered, err := s.store.RecoverInterruptedScheduledJobRuns(ctx, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("recover interrupted scheduled job runs: %w", err)
	}
	return recovered, nil
}

func classifySend(err error) (RunResult, string, string) {
	if err == nil {
		return RunSuccess, "", ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, napcat.ErrUnavailable) {
		return RunUnknown, "send_outcome_unknown", "发送结果未确认"
	}
	var operationError *napcat.OperationError
	if errors.As(err, &operationError) {
		switch operationError.Code {
		case napcat.FailureUpstreamRejected, napcat.FailureInvalidResponse:
			return RunFailed, "send_rejected", "NapCat 未完成发送"
		default:
			return RunUnknown, "send_outcome_unknown", "发送结果未确认"
		}
	}
	return RunUnknown, "send_outcome_unknown", "发送结果未确认"
}

func (s *Service) publish(job Job, reason string) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventScheduledJobUpdated, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceScheduledJob, ID: job.ID, Version: job.Version}, Reason: reason,
	})
}

func (s *Service) publishRun(run Run) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventScheduledJobRunCompleted, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceScheduledJob, ID: run.JobID}, Reason: string(run.Result),
	})
}

func mutationContext(principal auth.Principal, request auth.MutationContext, at time.Time) MutationContext {
	return MutationContext{Actor: principal, Request: request, OccurredAt: at.UTC()}
}

func validCreate(input CreateInput) bool {
	return validText(input.Name, 100) && validGroupID(input.GroupID) && validText(input.Message, 4000) && validSchedule(input.Schedule)
}

func validPatch(patch Patch) bool {
	if !patch.Name.Set && !patch.GroupID.Set && !patch.Message.Set && !patch.Schedule.Set && !patch.Status.Set {
		return false
	}
	return (!patch.Name.Set || validText(patch.Name.Value, 100)) &&
		(!patch.GroupID.Set || validGroupID(patch.GroupID.Value)) &&
		(!patch.Message.Set || validText(patch.Message.Value, 4000)) &&
		(!patch.Schedule.Set || validSchedule(patch.Schedule.Value)) &&
		(!patch.Status.Set || patch.Status.Value == StatusActive || patch.Status.Value == StatusPaused)
}

func validSchedule(schedule Schedule) bool {
	switch schedule.Type {
	case TypeDaily:
		if schedule.RunAt != nil || !validText(schedule.Timezone, 64) {
			return false
		}
		parsed, err := time.Parse("15:04", schedule.LocalTime)
		if err != nil || parsed.Format("15:04") != schedule.LocalTime {
			return false
		}
		_, err = time.LoadLocation(schedule.Timezone)
		return err == nil
	case TypeOnce:
		return schedule.RunAt != nil && schedule.RunAt.Location() == time.UTC && schedule.LocalTime == "" && schedule.Timezone == ""
	default:
		return false
	}
}

func validScheduleAt(schedule Schedule, now time.Time) bool {
	return validSchedule(schedule) && (schedule.Type != TypeOnce || schedule.RunAt.After(now))
}

func calculateNextRun(schedule Schedule, now time.Time) *time.Time {
	switch schedule.Type {
	case TypeOnce:
		if schedule.RunAt == nil || !schedule.RunAt.After(now) {
			return nil
		}
		return cloneTime(schedule.RunAt)
	case TypeDaily:
		location, err := time.LoadLocation(schedule.Timezone)
		if err != nil {
			return nil
		}
		clock, err := time.Parse("15:04", schedule.LocalTime)
		if err != nil {
			return nil
		}
		localNow := now.In(location)
		candidate := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), clock.Hour(), clock.Minute(), 0, 0, location)
		if !candidate.After(localNow) {
			candidate = candidate.AddDate(0, 0, 1)
		}
		result := candidate.UTC()
		return &result
	default:
		return nil
	}
}

func validListQuery(query ListQuery) bool {
	return query.Limit >= 1 && query.Limit <= 100 && (query.GroupID == "" || validGroupID(query.GroupID)) &&
		(query.Type == "" || query.Type == TypeDaily || query.Type == TypeOnce) &&
		(query.Status == "" || validStatus(query.Status)) && (query.RunResult == "" || validRunResult(query.RunResult)) &&
		(query.Cursor == "" || validID(query.Cursor))
}

func validRunListQuery(query RunListQuery) bool {
	return validID(query.JobID) && query.Limit >= 1 && query.Limit <= 100 &&
		(query.Kind == "" || query.Kind == RunScheduled || query.Kind == RunTest) &&
		(query.Result == "" || validRunResult(query.Result)) &&
		(query.From == nil || query.From.Location() == time.UTC) && (query.To == nil || query.To.Location() == time.UTC) &&
		(query.From == nil || query.To == nil || !query.From.After(*query.To)) &&
		(query.Cursor == "" || validID(query.Cursor))
}

func validStatus(value Status) bool {
	return value == StatusActive || value == StatusPaused || value == StatusCompleted || value == StatusArchived
}

func validRunResult(value RunResult) bool {
	return value == RunSuccess || value == RunFailed || value == RunUnknown || value == RunSkipped
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

func validID(value string) bool {
	return validText(value, 256)
}

func validText(value string, maximum int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maximum
}

func validRequest(value auth.MutationContext) bool {
	return validText(value.RequestID, 256) && (value.IPAddress == "" || validText(value.IPAddress, 64)) &&
		(value.UserAgent == "" || validText(value.UserAgent, 300))
}

func safeText(value string, maximum int) string {
	if !utf8.ValidString(value) {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}

func nonNegativeDuration(value time.Duration) time.Duration {
	if value < 0 {
		return 0
	}
	return value
}

func cloneCreate(value CreateInput) CreateInput {
	value.Schedule = cloneSchedule(value.Schedule)
	return value
}

func clonePatch(value Patch) Patch {
	value.Schedule.Value = cloneSchedule(value.Schedule.Value)
	return value
}

func cloneSchedule(value Schedule) Schedule {
	value.RunAt = cloneTime(value.RunAt)
	return value
}

func cloneJob(value Job) Job {
	value.Schedule = cloneSchedule(value.Schedule)
	value.NextRunAt = cloneTime(value.NextRunAt)
	value.LastRunAt = cloneTime(value.LastRunAt)
	if value.LastRunResult != nil {
		result := *value.LastRunResult
		value.LastRunResult = &result
	}
	value.UpdatedBy.UserID = cloneString(value.UpdatedBy.UserID)
	value.UpdatedBy.QQUserID = cloneString(value.UpdatedBy.QQUserID)
	return value
}

func cloneJobs(values []Job) []Job {
	result := make([]Job, len(values))
	for index := range values {
		result[index] = cloneJob(values[index])
	}
	return result
}

func cloneRun(value Run) Run {
	value.ScheduledFor = cloneTime(value.ScheduledFor)
	value.CompletedAt = cloneTime(value.CompletedAt)
	value.MessageID = cloneString(value.MessageID)
	value.ErrorCode = cloneString(value.ErrorCode)
	value.ErrorMessage = cloneString(value.ErrorMessage)
	if value.TriggeredBy != nil {
		actor := *value.TriggeredBy
		actor.UserID = cloneString(actor.UserID)
		actor.QQUserID = cloneString(actor.QQUserID)
		value.TriggeredBy = &actor
	}
	return value
}

func cloneRuns(values []Run) []Run {
	result := make([]Run, len(values))
	for index := range values {
		result[index] = cloneRun(values[index])
	}
	return result
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
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
