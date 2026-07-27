package audit

import (
	"context"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/auth"
)

var (
	ErrInvalidStore = errors.New("invalid audit store")
	ErrForbidden    = errors.New("audit access forbidden")
	ErrInvalidQuery = errors.New("invalid audit query")
	ErrNotFound     = errors.New("audit log not found")
)

type Store interface {
	GetAuditLog(ctx context.Context, id string) (Log, bool, error)
	ListAuditLogs(ctx context.Context, query ListQuery) (Page, error)
}

type Service struct {
	store Store
}

func NewService(store Store) (*Service, error) {
	if store == nil {
		return nil, ErrInvalidStore
	}
	return &Service{store: store}, nil
}

func (s *Service) Get(ctx context.Context, principal auth.Principal, id string) (Log, error) {
	if !principal.Has(auth.PermissionAuditRead) {
		return Log{}, ErrForbidden
	}
	if !validText(id, 256) {
		return Log{}, ErrInvalidQuery
	}
	log, found, err := s.store.GetAuditLog(ctx, id)
	if err != nil {
		return Log{}, fmt.Errorf("get audit log: %w", err)
	}
	if !found {
		return Log{}, ErrNotFound
	}
	return RedactForRole(log, principal.Role), nil
}

func (s *Service) List(ctx context.Context, principal auth.Principal, query ListQuery) (Page, error) {
	if !principal.Has(auth.PermissionAuditRead) {
		return Page{}, ErrForbidden
	}
	if query.Limit == 0 {
		query.Limit = 50
	}
	if !validListQuery(query) {
		return Page{}, ErrInvalidQuery
	}
	page, err := s.store.ListAuditLogs(ctx, query)
	if err != nil {
		return Page{}, fmt.Errorf("list audit logs: %w", err)
	}
	page.Items = append([]Summary(nil), page.Items...)
	for index := range page.Items {
		page.Items[index] = boundSummary(page.Items[index])
	}
	page.NextCursor = truncateRunes(page.NextCursor, 256)
	return page, nil
}

func boundSummary(summary Summary) Summary {
	result := summary
	result.ID = truncateRunes(summary.ID, 256)
	result.Actor.DisplayName = truncateRunes(summary.Actor.DisplayName, 100)
	result.Actor.UserID, _ = cloneOptionalBounded(summary.Actor.UserID, 256, false)
	result.Actor.QQUserID, _ = cloneOptionalBounded(summary.Actor.QQUserID, 256, false)
	result.Action = truncateRunes(summary.Action, 100)
	result.Target.Type = truncateRunes(summary.Target.Type, 64)
	result.Target.ID = truncateRunes(summary.Target.ID, 256)
	result.Target.DisplayName = truncateRunes(summary.Target.DisplayName, 200)
	result.ErrorCode, _ = cloneOptionalBounded(summary.ErrorCode, 100, false)
	result.RequestID = truncateRunes(summary.RequestID, 256)
	return result
}

func validListQuery(query ListQuery) bool {
	if query.Limit < 1 || query.Limit > 100 || (query.Cursor != "" && !validText(query.Cursor, 256)) ||
		(query.ActorUserID != "" && !validText(query.ActorUserID, 256)) || len(query.Actions) > 20 || len(query.TargetTypes) > 20 {
		return false
	}
	if query.From != nil && query.To != nil && query.From.After(*query.To) {
		return false
	}
	if query.ActorType != "" && query.ActorType != ActorAdminUser && query.ActorType != ActorQQUser && query.ActorType != ActorSystem {
		return false
	}
	if query.Result != "" && query.Result != ResultSuccess && query.Result != ResultFailed && query.Result != ResultUnknown {
		return false
	}
	for _, value := range append(append([]string(nil), query.Actions...), query.TargetTypes...) {
		if !validText(value, 100) {
			return false
		}
	}
	return true
}

func validText(value string, maxLength int) bool {
	return len(value) >= 1 && len(value) <= maxLength*utf8.UTFMax && utf8.ValidString(value) && utf8.RuneCountInString(value) <= maxLength
}
