package idempotency

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"time"
	"unicode/utf8"
)

const (
	defaultTTL           = 24 * time.Hour
	maxTTL               = 365 * 24 * time.Hour
	minSecretBytes       = 32
	maxSecretBytes       = 1024
	maxCanonicalJSONSize = 1 << 20
	requestHashDomain    = "jxh-manager:idempotency-request:v1\x00"
)

var (
	actorIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
	operationPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._:-]{0,99}$`)
	keyPattern       = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	errorCodePattern = regexp.MustCompile(`^[a-z][a-z0-9._:-]{0,99}$`)
	resourcePattern  = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	traceIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)
)

type Service struct {
	store  Store
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewService(store Store, secret []byte, opts Options) (*Service, error) {
	if nilInterface(store) {
		return nil, ErrInvalidStore
	}
	if len(secret) < minSecretBytes || len(secret) > maxSecretBytes {
		return nil, ErrInvalidSecret
	}
	ttl := opts.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < 0 || ttl > maxTTL {
		return nil, fmt.Errorf("%w: invalid retention period", ErrInvalidInput)
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Service{
		store:  store,
		secret: append([]byte(nil), secret...),
		ttl:    ttl,
		now:    now,
	}, nil
}

func (s *Service) Begin(ctx context.Context, input BeginInput) (BeginResult, error) {
	if err := validateScope(input.Actor, input.Operation, input.Key); err != nil {
		return BeginResult{}, err
	}
	canonical, err := canonicalJSON(input.Payload)
	if err != nil {
		return BeginResult{}, fmt.Errorf("%w: request payload is not canonicalizable JSON", ErrInvalidInput)
	}
	requestHash := s.requestHash(input.Operation, canonical)
	now := s.now().UTC()
	reservation, err := s.store.ReserveIdempotency(ctx, Reservation{
		ActorType:   input.Actor.Type,
		ActorID:     input.Actor.ID,
		Operation:   input.Operation,
		Key:         input.Key,
		RequestHash: requestHash,
		State:       StateInProgress,
		CreatedAt:   now,
		ExpiresAt:   now.Add(s.ttl),
	})
	if err != nil {
		if errors.Is(err, ErrKeyReused) {
			return BeginResult{}, ErrKeyReused
		}
		return BeginResult{}, fmt.Errorf("reserve idempotency key: %w", err)
	}
	if !hmac.Equal([]byte(reservation.RequestHash), []byte(requestHash)) {
		return BeginResult{}, ErrKeyReused
	}
	if err := validateReservation(reservation, input.Actor, input.Operation, input.Key); err != nil {
		return BeginResult{}, err
	}

	result := BeginResult{RequestHash: requestHash}
	switch {
	case reservation.Fresh:
		execution := executionFromReservation(reservation)
		result.Disposition = DispositionExecute
		result.Execution = &execution
	case reservation.State == StateInProgress:
		result.Disposition = DispositionInProgress
	case reservation.State == StateCompleted:
		result.Disposition = DispositionReplay
		replayed := cloneResult(*reservation.Result)
		result.Result = &replayed
	default:
		return BeginResult{}, ErrInvalidRecord
	}
	return result, nil
}

func (s *Service) Complete(ctx context.Context, execution Execution, result Result) (Result, error) {
	if err := validateExecution(execution); err != nil {
		return Result{}, err
	}
	completedAt := s.now().UTC()
	result.CompletedAt = completedAt
	if err := validateResult(result); err != nil {
		return Result{}, err
	}
	completion := Completion{
		RequestHash: execution.RequestHash,
		Result:      cloneResult(result),
		CompletedAt: completedAt,
	}
	record, err := s.store.CompleteIdempotency(ctx, execution.ReservationID, completion)
	if err != nil {
		if errors.Is(err, ErrKeyReused) {
			return Result{}, ErrKeyReused
		}
		return Result{}, fmt.Errorf("complete idempotency key: %w", err)
	}
	if record.ID != execution.ReservationID {
		return Result{}, ErrInvalidRecord
	}
	if !hmac.Equal([]byte(record.RequestHash), []byte(execution.RequestHash)) {
		return Result{}, ErrKeyReused
	}
	if err := validateReservation(record, execution.Actor, execution.Operation, execution.Key); err != nil {
		return Result{}, err
	}
	if record.State != StateCompleted || record.Result == nil {
		return Result{}, ErrInvalidRecord
	}
	return cloneResult(*record.Result), nil
}

func (s *Service) MarkUnknown(
	ctx context.Context,
	execution Execution,
	responseStatus int,
	errorCode string,
	traceID string,
	resource *Resource,
) (Result, error) {
	return s.Complete(ctx, execution, Result{
		Status:         ResultUnknown,
		ResponseStatus: responseStatus,
		ErrorCode:      errorCode,
		TraceID:        traceID,
		Resource:       cloneResource(resource),
	})
}

func (s *Service) requestHash(operation string, canonical []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(requestHashDomain))
	_, _ = mac.Write([]byte(operation))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(canonical)
	return hex.EncodeToString(mac.Sum(nil))
}

func canonicalJSON(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxCanonicalJSONSize {
		return nil, ErrInvalidInput
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || len(canonical) > maxCanonicalJSONSize {
		return nil, ErrInvalidInput
	}
	return canonical, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return fmt.Errorf("multiple JSON values")
	}
	return err
}

func validateScope(actor Actor, operation, key string) error {
	if actor.Type != ActorAdminUser && actor.Type != ActorQQUser && actor.Type != ActorSystem {
		return fmt.Errorf("%w: unsupported actor type", ErrInvalidInput)
	}
	if !actorIDPattern.MatchString(actor.ID) || !operationPattern.MatchString(operation) || !keyPattern.MatchString(key) {
		return ErrInvalidInput
	}
	return nil
}

func validateExecution(execution Execution) error {
	if execution.ReservationID == 0 {
		return ErrInvalidInput
	}
	if err := validateScope(execution.Actor, execution.Operation, execution.Key); err != nil {
		return err
	}
	if !validSHA256Hex(execution.RequestHash) {
		return ErrInvalidInput
	}
	return nil
}

func validateReservation(record Reservation, actor Actor, operation, key string) error {
	if record.ID == 0 || record.ActorType != actor.Type || record.ActorID != actor.ID || record.Operation != operation || record.Key != key ||
		!validSHA256Hex(record.RequestHash) || record.ExpiresAt.Before(record.CreatedAt) {
		return ErrInvalidRecord
	}
	switch record.State {
	case StateInProgress:
		if record.Result != nil || record.CompletedAt != nil {
			return ErrInvalidRecord
		}
	case StateCompleted:
		if record.Fresh || record.Result == nil || record.CompletedAt == nil {
			return ErrInvalidRecord
		}
		if err := validateResult(*record.Result); err != nil || !record.Result.CompletedAt.Equal(*record.CompletedAt) {
			return ErrInvalidRecord
		}
	default:
		return ErrInvalidRecord
	}
	if record.Fresh && record.State != StateInProgress {
		return ErrInvalidRecord
	}
	return nil
}

func validateResult(result Result) error {
	if result.ResponseStatus < 100 || result.ResponseStatus > 599 || result.CompletedAt.IsZero() {
		return ErrInvalidInput
	}
	switch result.Status {
	case ResultSucceeded:
		if result.ErrorCode != "" {
			return ErrInvalidInput
		}
	case ResultFailed, ResultUnknown:
		if !errorCodePattern.MatchString(result.ErrorCode) {
			return ErrInvalidInput
		}
	default:
		return ErrInvalidInput
	}
	if result.Resource != nil && (!resourcePattern.MatchString(result.Resource.Type) || !validText(result.Resource.ID, 256)) {
		return ErrInvalidInput
	}
	if result.ResultingSessionID != "" && !actorIDPattern.MatchString(result.ResultingSessionID) {
		return ErrInvalidInput
	}
	if result.TraceID != "" && !traceIDPattern.MatchString(result.TraceID) {
		return ErrInvalidInput
	}
	return nil
}

func validSHA256Hex(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validText(value string, maxRunes int) bool {
	return value != "" && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxRunes
}

func executionFromReservation(record Reservation) Execution {
	return Execution{
		ReservationID: record.ID,
		Actor:         Actor{Type: record.ActorType, ID: record.ActorID},
		Operation:     record.Operation,
		Key:           record.Key,
		RequestHash:   record.RequestHash,
	}
}

func cloneResult(result Result) Result {
	result.Resource = cloneResource(result.Resource)
	return result
}

func cloneResource(resource *Resource) *Resource {
	if resource == nil {
		return nil
	}
	cloned := *resource
	return &cloned
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
