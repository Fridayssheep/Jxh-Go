package system

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/management/events"
	platformconfig "github.com/zjutjh/jxh-go/internal/platform/config"
	"github.com/zjutjh/jxh-go/internal/platform/health"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
)

var (
	ErrForbidden                    = errors.New("system operation forbidden")
	ErrInvalidInput                 = errors.New("invalid system operation input")
	ErrNapCatUnavailable            = errors.New("napcat unavailable")
	ErrIdempotencyConflict          = errors.New("system idempotency conflict")
	ErrConfigurationUnavailable     = errors.New("configuration file unavailable")
	ErrConfigurationVersionConflict = errors.New("configuration version conflict")
)

type DependencyKey string

const (
	DependencyMySQL     DependencyKey = "mysql"
	DependencyNapCat    DependencyKey = "napcat"
	DependencyWPS       DependencyKey = "wps"
	DependencyAI        DependencyKey = "ai"
	DependencyQuote     DependencyKey = "quote"
	DependencyWorker    DependencyKey = "worker"
	DependencyScheduler DependencyKey = "scheduler"
	DependencyTelemetry DependencyKey = "telemetry"
)

type DependencyStatus string

const (
	DependencyHealthy       DependencyStatus = "healthy"
	DependencyDegraded      DependencyStatus = "degraded"
	DependencyUnavailable   DependencyStatus = "unavailable"
	DependencyNotConfigured DependencyStatus = "not_configured"
	DependencyUnknown       DependencyStatus = "unknown"
)

type DependencyConfiguration struct {
	Configured bool
	Required   bool
}

type DependencyHealth struct {
	Key           DependencyKey
	Status        DependencyStatus
	Configured    bool
	Required      bool
	Latency       *time.Duration
	LastCheckedAt *time.Time
	LastSuccessAt *time.Time
	LastErrorAt   *time.Time
	Message       *string
}

type Health struct {
	GeneratedAt  time.Time
	Live         bool
	Ready        bool
	Dependencies []DependencyHealth
}

type Configuration struct {
	YAML                 string
	Version              uint64
	MaskedFields         []string
	EnvironmentOverrides []string
	RestartRequired      bool
}

type ConfigurationEditor interface {
	Read() (platformconfig.EditableDocument, error)
	Update(expectedVersion uint64, candidate string) (platformconfig.EditableDocument, error)
}

type HealthSource interface {
	Snapshot() health.Snapshot
}

type RestartGateway interface {
	Snapshot() napcat.Snapshot
	SetRestart(ctx context.Context) error
}

type OperationStatus string

const (
	StatusAccepted  OperationStatus = "accepted"
	StatusRunning   OperationStatus = "running"
	StatusSucceeded OperationStatus = "succeeded"
	StatusFailed    OperationStatus = "failed"
	StatusUnknown   OperationStatus = "unknown"
)

type Operation struct {
	ID          string
	Type        string
	Status      OperationStatus
	RequestedAt time.Time
	CompletedAt *time.Time
	ErrorCode   *string
}

type RestartInput struct {
	Confirmation string
	Reason       string
}

type BeginRestart struct {
	Actor          auth.Principal
	Context        auth.MutationContext
	IdempotencyKey string
	RequestHash    string
	Reason         string
	RequestedAt    time.Time
}

type FindRestart struct {
	ActorID        string
	IdempotencyKey string
	RequestHash    string
	At             time.Time
}

type Transition struct {
	OperationID string
	From        OperationStatus
	To          OperationStatus
	At          time.Time
	ErrorCode   string
}

type Store interface {
	// FindNapCatRestart returns an unexpired prior operation without creating a
	// new reservation. This lets retries replay while NapCat is disconnected.
	FindNapCatRestart(ctx context.Context, find FindRestart) (operation Operation, found bool, err error)
	// BeginNapCatRestart atomically reserves idempotency, writes the accepted
	// operation and its requested audit record. fresh=false replays that row.
	BeginNapCatRestart(ctx context.Context, begin BeginRestart) (operation Operation, fresh bool, err error)
	// TransitionNapCatRestart atomically advances the operation and writes the
	// corresponding final audit record when To is terminal.
	TransitionNapCatRestart(ctx context.Context, transition Transition) (Operation, error)
	// RecoverInterruptedNapCatRestarts marks operations left by an earlier
	// process as unknown and completes their idempotency and audit records.
	RecoverInterruptedNapCatRestarts(ctx context.Context, recoveredAt time.Time) ([]Operation, error)
}

type EventPublisher interface {
	Publish(draft events.Draft) (events.Event, error)
}

type Options struct {
	Store                Store
	Health               HealthSource
	Gateway              RestartGateway
	Events               EventPublisher
	Configuration        ConfigurationEditor
	IdempotencySecret    []byte
	Dependencies         map[DependencyKey]DependencyConfiguration
	Now                  func() time.Time
	WorkerContext        context.Context
	WorkerTimeout        time.Duration
	TransitionRetryDelay time.Duration
	MaxConcurrentWorkers int
}

type Service struct {
	store         Store
	health        HealthSource
	gateway       RestartGateway
	events        EventPublisher
	configuration ConfigurationEditor
	secret        []byte
	dependencies  map[DependencyKey]DependencyConfiguration
	now           func() time.Time
	workerCtx     context.Context
	cancel        context.CancelFunc
	workerTimeout time.Duration
	retryDelay    time.Duration
	workers       chan struct{}
	wait          sync.WaitGroup
}

var systemIdempotencyKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)

func NewService(options Options) (*Service, error) {
	if options.Store == nil || options.Health == nil || options.Gateway == nil || len(options.IdempotencySecret) < 32 || options.Now == nil {
		return nil, ErrInvalidInput
	}
	if options.WorkerTimeout <= 0 {
		options.WorkerTimeout = 30 * time.Second
	}
	if options.TransitionRetryDelay <= 0 {
		options.TransitionRetryDelay = time.Second
	}
	if options.MaxConcurrentWorkers <= 0 {
		options.MaxConcurrentWorkers = 1
	}
	workerContext := options.WorkerContext
	if workerContext == nil {
		workerContext = context.Background()
	}
	workerContext, cancel := context.WithCancel(workerContext)
	dependencies := make(map[DependencyKey]DependencyConfiguration, len(options.Dependencies))
	for key, value := range options.Dependencies {
		if !validDependencyKey(key) {
			cancel()
			return nil, ErrInvalidInput
		}
		dependencies[key] = value
	}
	return &Service{
		store: options.Store, health: options.Health, gateway: options.Gateway, events: options.Events,
		configuration: options.Configuration,
		secret:        append([]byte(nil), options.IdempotencySecret...), dependencies: dependencies, now: options.Now,
		workerCtx: workerContext, cancel: cancel, workerTimeout: options.WorkerTimeout,
		retryDelay: options.TransitionRetryDelay,
		workers:    make(chan struct{}, options.MaxConcurrentWorkers),
	}, nil
}

func (s *Service) Health(_ context.Context, principal auth.Principal) (Health, error) {
	if !principal.Has(auth.PermissionSystemRead) {
		return Health{}, ErrForbidden
	}
	snapshot := s.health.Snapshot()
	components := []struct {
		key    DependencyKey
		status health.ComponentStatus
	}{
		{DependencyMySQL, snapshot.Database}, {DependencyNapCat, snapshot.NapCat}, {DependencyWPS, snapshot.WPS},
		{DependencyAI, snapshot.AI}, {DependencyQuote, snapshot.Quote}, {DependencyWorker, snapshot.Workers},
		{DependencyScheduler, snapshot.Scheduler}, {DependencyTelemetry, snapshot.Telemetry},
	}
	dependencies := make([]DependencyHealth, 0, len(components))
	for _, component := range components {
		configuration := s.dependencies[component.key]
		dependencies = append(dependencies, mapDependency(component.key, configuration, component.status))
	}
	return Health{GeneratedAt: s.now().UTC(), Live: snapshot.Live, Ready: snapshot.Ready, Dependencies: dependencies}, nil
}

func (s *Service) Configuration(_ context.Context, principal auth.Principal) (Configuration, error) {
	if !principal.Has(auth.PermissionSystemRead) {
		return Configuration{}, ErrForbidden
	}
	if s.configuration == nil {
		return Configuration{}, ErrConfigurationUnavailable
	}
	document, err := s.configuration.Read()
	if err != nil {
		return Configuration{}, ErrConfigurationUnavailable
	}
	return mapConfiguration(document), nil
}

func (s *Service) UpdateConfiguration(_ context.Context, principal auth.Principal, expectedVersion uint64, candidate string) (Configuration, error) {
	if !principal.Has(auth.PermissionConfigWrite) {
		return Configuration{}, ErrForbidden
	}
	if s.configuration == nil {
		return Configuration{}, ErrConfigurationUnavailable
	}
	document, err := s.configuration.Update(expectedVersion, candidate)
	if err != nil {
		switch {
		case errors.Is(err, platformconfig.ErrInvalidDocument):
			return Configuration{}, ErrInvalidInput
		case errors.Is(err, platformconfig.ErrVersionConflict):
			return Configuration{}, ErrConfigurationVersionConflict
		default:
			return Configuration{}, ErrConfigurationUnavailable
		}
	}
	return mapConfiguration(document), nil
}

func mapConfiguration(document platformconfig.EditableDocument) Configuration {
	return Configuration{
		YAML: document.YAML, Version: document.Version,
		MaskedFields:         append([]string(nil), document.MaskedFields...),
		EnvironmentOverrides: append([]string(nil), document.EnvironmentOverrides...),
		RestartRequired:      true,
	}
}

func (s *Service) RecoverInterrupted(ctx context.Context) (int, error) {
	operations, err := s.store.RecoverInterruptedNapCatRestarts(ctx, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("recover interrupted napcat restarts: %w", err)
	}
	for _, operation := range operations {
		if !validOperation(operation) || operation.Status != StatusUnknown {
			return 0, errors.New("invalid recovered restart operation")
		}
		s.publish(operation, "restart_interrupted")
	}
	return len(operations), nil
}

func (s *Service) RestartNapCat(ctx context.Context, principal auth.Principal, input RestartInput, idempotencyKey string, request ...auth.MutationContext) (Operation, error) {
	if !principal.Has(auth.PermissionNapCatRestart) {
		return Operation{}, ErrForbidden
	}
	requestContext := auth.MutationContext{}
	if len(request) > 0 {
		requestContext = request[0]
	}
	if input.Confirmation != "restart" || !validReason(input.Reason) || !systemIdempotencyKeyPattern.MatchString(idempotencyKey) ||
		principal.UserID == "" || !validRequestContext(requestContext) {
		return Operation{}, ErrInvalidInput
	}
	now := s.now().UTC()
	requestHash := s.requestHash(input)
	replayed, found, err := s.store.FindNapCatRestart(ctx, FindRestart{
		ActorID: principal.UserID, IdempotencyKey: idempotencyKey, RequestHash: requestHash, At: now,
	})
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return Operation{}, ErrIdempotencyConflict
		}
		return Operation{}, fmt.Errorf("find napcat restart: %w", err)
	}
	if found {
		if !validOperation(replayed) {
			return Operation{}, errors.New("invalid restart operation store result")
		}
		return cloneOperation(replayed), nil
	}
	if !s.gateway.Snapshot().Connected {
		return Operation{}, ErrNapCatUnavailable
	}
	operation, fresh, err := s.store.BeginNapCatRestart(ctx, BeginRestart{
		Actor: principal, Context: requestContext, IdempotencyKey: idempotencyKey,
		RequestHash: requestHash, Reason: input.Reason, RequestedAt: now,
	})
	if err != nil {
		if errors.Is(err, ErrIdempotencyConflict) {
			return Operation{}, ErrIdempotencyConflict
		}
		return Operation{}, fmt.Errorf("begin napcat restart: %w", err)
	}
	if !validOperation(operation) || (fresh && operation.Status != StatusAccepted) {
		return Operation{}, errors.New("invalid restart operation store result")
	}
	if fresh {
		s.publish(operation, "requested")
		s.wait.Add(1)
		go s.runRestart(operation)
	}
	return cloneOperation(operation), nil
}

func (s *Service) runRestart(operation Operation) {
	defer s.wait.Done()
	select {
	case s.workers <- struct{}{}:
		defer func() { <-s.workers }()
	case <-s.workerCtx.Done():
		s.finish(operation.ID, StatusAccepted, StatusUnknown, "worker_stopped")
		return
	}
	running, err := s.store.TransitionNapCatRestart(s.workerCtx, Transition{
		OperationID: operation.ID, From: StatusAccepted, To: StatusRunning, At: s.now().UTC(),
	})
	if err != nil {
		s.finish(operation.ID, StatusAccepted, StatusUnknown, "operation_state_unknown")
		return
	}
	s.publish(running, "running")
	workerContext, cancel := context.WithTimeout(s.workerCtx, s.workerTimeout)
	err = s.gateway.SetRestart(workerContext)
	cancel()
	status, code := classifyRestartResult(err)
	s.finish(operation.ID, StatusRunning, status, code)
}

func (s *Service) finish(operationID string, from, to OperationStatus, errorCode string) {
	transition := Transition{OperationID: operationID, From: from, To: to, At: s.now().UTC(), ErrorCode: errorCode}
	for {
		transitionContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		operation, err := s.store.TransitionNapCatRestart(transitionContext, transition)
		cancel()
		if err == nil {
			reason := string(to)
			if errorCode != "" {
				reason = errorCode
			}
			s.publish(operation, reason)
			return
		}
		timer := time.NewTimer(s.retryDelay)
		select {
		case <-s.workerCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (s *Service) publish(operation Operation, reason string) {
	if s.events == nil {
		return
	}
	_, _ = s.events.Publish(events.Draft{
		Type: events.EventSystemHealthChanged, OccurredAt: s.now().UTC(),
		Resource: &events.Resource{Type: events.ResourceSystem, ID: operation.ID}, Reason: reason,
	})
}

func (s *Service) Close() {
	s.cancel()
	s.wait.Wait()
}

func (s *Service) requestHash(input RestartInput) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte("jxh-admin/napcat-restart/v1\x00"))
	_, _ = mac.Write([]byte(input.Confirmation))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(input.Reason))
	return hex.EncodeToString(mac.Sum(nil))
}

func classifyRestartResult(err error) (OperationStatus, string) {
	if err == nil {
		return StatusSucceeded, ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, napcat.ErrUnavailable) {
		return StatusUnknown, "restart_outcome_unknown"
	}
	var operationError *napcat.OperationError
	if errors.As(err, &operationError) {
		switch operationError.Code {
		case napcat.FailureDisconnected, napcat.FailureTransport, napcat.FailureTimeout, napcat.FailureUnknown:
			return StatusUnknown, "restart_outcome_unknown"
		default:
			return StatusFailed, "restart_rejected"
		}
	}
	return StatusUnknown, "restart_outcome_unknown"
}

func mapDependency(key DependencyKey, configuration DependencyConfiguration, status health.ComponentStatus) DependencyHealth {
	result := DependencyHealth{Key: key, Configured: configuration.Configured, Required: configuration.Required}
	if !configuration.Configured {
		result.Status = DependencyNotConfigured
	} else if status.CheckedAt.IsZero() {
		result.Status = DependencyUnknown
	} else if status.Available {
		result.Status = DependencyHealthy
	} else if strings.Contains(strings.ToLower(status.Code), "degraded") {
		result.Status = DependencyDegraded
	} else {
		result.Status = DependencyUnavailable
	}
	if status.Latency >= 0 && !status.CheckedAt.IsZero() {
		latency := status.Latency
		result.Latency = &latency
	}
	result.LastCheckedAt = optionalTime(status.CheckedAt)
	result.LastSuccessAt = optionalTime(status.LastSuccessAt)
	result.LastErrorAt = optionalTime(status.LastErrorAt)
	if status.Summary != "" {
		message := truncate(status.Summary, 300)
		result.Message = &message
	}
	return result
}

func optionalTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func truncate(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func validReason(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 500
}

func validOperation(operation Operation) bool {
	validStatus := operation.Status == StatusAccepted || operation.Status == StatusRunning || operation.Status == StatusSucceeded ||
		operation.Status == StatusFailed || operation.Status == StatusUnknown
	return operation.ID != "" && operation.Type == "napcat_restart" && validStatus && !operation.RequestedAt.IsZero()
}

func validRequestContext(value auth.MutationContext) bool {
	return validBoundedText(value.RequestID, 256, false) && validBoundedText(value.IPAddress, 64, true) &&
		validBoundedText(value.UserAgent, 300, true)
}

func validBoundedText(value string, limit int, optional bool) bool {
	if value == "" {
		return optional
	}
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= limit
}

func cloneOperation(operation Operation) Operation {
	operation.CompletedAt = optionalTimePointer(operation.CompletedAt)
	if operation.ErrorCode != nil {
		value := *operation.ErrorCode
		operation.ErrorCode = &value
	}
	return operation
}

func optionalTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	utc := value.UTC()
	return &utc
}

func validDependencyKey(key DependencyKey) bool {
	switch key {
	case DependencyMySQL, DependencyNapCat, DependencyWPS, DependencyAI, DependencyQuote,
		DependencyWorker, DependencyScheduler, DependencyTelemetry:
		return true
	default:
		return false
	}
}
