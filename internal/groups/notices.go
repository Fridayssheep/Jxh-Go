package groups

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	"github.com/zjutjh/jxh-go/internal/platform/napcat"
)

const (
	maxNoticeTargets       = 50
	maxNoticeContentLength = 5000
	noticeHashDomain       = "jxh-manager:group-notice:v1\x00"
)

type NoticePublishStatus string

const (
	NoticePublishSucceeded NoticePublishStatus = "success"
	NoticePublishPartial   NoticePublishStatus = "partial"
	NoticePublishFailed    NoticePublishStatus = "failed"
	NoticePublishUnknown   NoticePublishStatus = "unknown"
)

type NoticeItemStatus string

const (
	NoticeItemSucceeded NoticeItemStatus = "success"
	NoticeItemDenied    NoticeItemStatus = "denied"
	NoticeItemFailed    NoticeItemStatus = "failed"
	NoticeItemUnknown   NoticeItemStatus = "unknown"
)

type NoticeTarget struct {
	GroupID string `json:"group_id"`
	Name    string `json:"name"`
}

type NoticePublishInput struct {
	GroupIDs []string
	Content  string
}

type NoticePublishItem struct {
	Group     NoticeTarget     `json:"group"`
	BotRole   Role             `json:"bot_role"`
	Status    NoticeItemStatus `json:"status"`
	ErrorCode string           `json:"error_code,omitempty"`
}

type NoticePublishResult struct {
	PublicationID  string              `json:"publication_id"`
	Status         NoticePublishStatus `json:"status"`
	RequestedCount uint64              `json:"requested_count"`
	PublishedCount uint64              `json:"published_count"`
	DeniedCount    uint64              `json:"denied_count"`
	FailedCount    uint64              `json:"failed_count"`
	UnknownCount   uint64              `json:"unknown_count"`
	Items          []NoticePublishItem `json:"items"`
	CompletedAt    time.Time           `json:"completed_at"`
}

type BeginNoticePublication struct {
	Context        MutationContext
	IdempotencyKey string
	RequestHash    string
	Targets        []NoticeTarget
	RequestedAt    time.Time
}

type NoticeReservation struct {
	PublicationID string
	Fresh         bool
	InProgress    bool
	Result        *NoticePublishResult
}

type CompleteNoticePublication struct {
	PublicationID string
	Result        NoticePublishResult
}

func (s *Service) PublishNotices(
	ctx context.Context,
	principal auth.Principal,
	input NoticePublishInput,
	idempotencyKey string,
	request auth.MutationContext,
) (NoticePublishResult, error) {
	if !principal.Has(auth.PermissionGroupNoticesWrite) {
		return NoticePublishResult{}, ErrForbidden
	}
	input.Content = strings.TrimSpace(input.Content)
	if principal.UserID == "" || !idempotencyPattern.MatchString(idempotencyKey) || !validRequest(request) ||
		!validNoticeInput(input) {
		return NoticePublishResult{}, ErrInvalidInput
	}
	if !s.gateway.Snapshot().Connected {
		return NoticePublishResult{}, ErrGatewayUnavailable
	}
	targets, err := s.resolveNoticeTargets(ctx, input.GroupIDs)
	if err != nil {
		return NoticePublishResult{}, err
	}
	requestedAt := s.now().UTC()
	reservation, err := s.store.BeginGroupNoticePublication(ctx, BeginNoticePublication{
		Context:        MutationContext{Actor: principal, Request: request, OccurredAt: requestedAt},
		IdempotencyKey: idempotencyKey, RequestHash: s.noticeRequestHash(input), Targets: cloneNoticeTargets(targets),
		RequestedAt: requestedAt,
	})
	if err != nil {
		return NoticePublishResult{}, fmt.Errorf("begin group notice publication: %w", err)
	}
	if !reservation.Fresh {
		if reservation.Result != nil {
			if !validNoticeResult(*reservation.Result) {
				return NoticePublishResult{}, ErrInvalidData
			}
			return cloneNoticeResult(*reservation.Result), nil
		}
		if reservation.InProgress {
			return NoticePublishResult{}, ErrNoticeInProgress
		}
		return NoticePublishResult{}, ErrInvalidData
	}
	if reservation.PublicationID == "" {
		return NoticePublishResult{}, ErrInvalidData
	}

	result := NoticePublishResult{
		PublicationID:  reservation.PublicationID,
		RequestedCount: uint64(len(targets)),
		Items:          s.publishNoticeItems(ctx, targets, input.Content),
		CompletedAt:    s.now().UTC(),
	}
	summarizeNoticeResult(&result)
	completed, err := s.store.CompleteGroupNoticePublication(ctx, CompleteNoticePublication{
		PublicationID: reservation.PublicationID, Result: cloneNoticeResult(result),
	})
	if err != nil {
		return NoticePublishResult{}, fmt.Errorf("complete group notice publication: %w", err)
	}
	if !validNoticeResult(completed) {
		return NoticePublishResult{}, ErrInvalidData
	}
	return cloneNoticeResult(completed), nil
}

func (s *Service) resolveNoticeTargets(ctx context.Context, groupIDs []string) ([]NoticeTarget, error) {
	targets := make([]NoticeTarget, len(groupIDs))
	for index, groupID := range groupIDs {
		group, found, err := s.store.GetGroup(ctx, groupID)
		if err != nil {
			return nil, fmt.Errorf("load notice target group: %w", err)
		}
		if !found {
			return nil, ErrNotFound
		}
		if err := validateGroup(group); err != nil {
			return nil, err
		}
		targets[index] = NoticeTarget{GroupID: group.ID, Name: group.Name}
	}
	return targets, nil
}

func (s *Service) publishNoticeItems(ctx context.Context, targets []NoticeTarget, content string) []NoticePublishItem {
	items := make([]NoticePublishItem, len(targets))
	selfID, err := s.gateway.GetLoginUserID(ctx)
	if err != nil {
		for index, target := range targets {
			items[index] = noticeFailureItem(target, RoleUnknown, err, false)
		}
		return items
	}

	jobs := make(chan int)
	workerCount := min(s.noticeWorkers, len(targets))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				target := targets[index]
				groupID, parseErr := strconv.ParseInt(target.GroupID, 10, 64)
				if parseErr != nil || groupID <= 0 {
					items[index] = NoticePublishItem{Group: target, BotRole: RoleUnknown, Status: NoticeItemFailed, ErrorCode: "invalid_group_id"}
					continue
				}
				roleValue, roleErr := s.gateway.GetGroupMemberRole(ctx, groupID, selfID)
				if roleErr != nil {
					items[index] = noticeFailureItem(target, RoleUnknown, roleErr, false)
					continue
				}
				role := normalizeRole(roleValue)
				if role != RoleOwner && role != RoleAdmin {
					items[index] = NoticePublishItem{
						Group: target, BotRole: role, Status: NoticeItemDenied, ErrorCode: "bot_not_group_admin",
					}
					continue
				}
				if publishErr := s.gateway.PublishGroupNotice(ctx, groupID, content); publishErr != nil {
					items[index] = noticeFailureItem(target, role, publishErr, true)
					continue
				}
				items[index] = NoticePublishItem{Group: target, BotRole: role, Status: NoticeItemSucceeded}
			}
		}()
	}
	for index := range targets {
		select {
		case jobs <- index:
		case <-ctx.Done():
			close(jobs)
			workers.Wait()
			for pending := index; pending < len(targets); pending++ {
				if items[pending].Group.GroupID == "" {
					items[pending] = noticeFailureItem(targets[pending], RoleUnknown, ctx.Err(), false)
				}
			}
			return items
		}
	}
	close(jobs)
	workers.Wait()
	return items
}

func noticeFailureItem(target NoticeTarget, role Role, err error, sending bool) NoticePublishItem {
	status := NoticeItemFailed
	code := "napcat_request_failed"
	if errors.Is(err, napcat.ErrUnavailable) {
		code = "dependency_unavailable"
	} else if errors.Is(err, context.Canceled) {
		code = "request_canceled"
		if sending {
			status = NoticeItemUnknown
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "upstream_timeout"
		if sending {
			status = NoticeItemUnknown
		}
	} else {
		var operationError *napcat.OperationError
		if errors.As(err, &operationError) {
			code = string(operationError.Code)
			switch operationError.Code {
			case napcat.FailureTimeout, napcat.FailureDisconnected, napcat.FailureTransport,
				napcat.FailureInvalidResponse, napcat.FailureUnknown:
				if sending {
					status = NoticeItemUnknown
				}
			}
		} else if !sending {
			code = "role_lookup_failed"
		}
	}
	return NoticePublishItem{Group: target, BotRole: role, Status: status, ErrorCode: code}
}

func summarizeNoticeResult(result *NoticePublishResult) {
	for _, item := range result.Items {
		switch item.Status {
		case NoticeItemSucceeded:
			result.PublishedCount++
		case NoticeItemDenied:
			result.DeniedCount++
		case NoticeItemFailed:
			result.FailedCount++
		case NoticeItemUnknown:
			result.UnknownCount++
		}
	}
	switch {
	case result.PublishedCount == result.RequestedCount:
		result.Status = NoticePublishSucceeded
	case result.PublishedCount > 0:
		result.Status = NoticePublishPartial
	case result.UnknownCount > 0:
		result.Status = NoticePublishUnknown
	default:
		result.Status = NoticePublishFailed
	}
}

func validNoticeInput(input NoticePublishInput) bool {
	if len(input.GroupIDs) == 0 || len(input.GroupIDs) > maxNoticeTargets || input.Content == "" ||
		!utf8.ValidString(input.Content) || utf8.RuneCountInString(input.Content) > maxNoticeContentLength {
		return false
	}
	seen := make(map[string]struct{}, len(input.GroupIDs))
	for _, groupID := range input.GroupIDs {
		if !validGroupID(groupID) {
			return false
		}
		if _, duplicate := seen[groupID]; duplicate {
			return false
		}
		seen[groupID] = struct{}{}
	}
	return true
}

func validNoticeResult(result NoticePublishResult) bool {
	if result.PublicationID == "" || result.CompletedAt.IsZero() || result.RequestedCount == 0 ||
		result.RequestedCount != uint64(len(result.Items)) ||
		result.RequestedCount != result.PublishedCount+result.DeniedCount+result.FailedCount+result.UnknownCount {
		return false
	}
	if result.Status != NoticePublishSucceeded && result.Status != NoticePublishPartial &&
		result.Status != NoticePublishFailed && result.Status != NoticePublishUnknown {
		return false
	}
	seen := make(map[string]struct{}, len(result.Items))
	for _, item := range result.Items {
		if !validGroupID(item.Group.GroupID) || !validName(item.Group.Name) || !validRole(item.BotRole) ||
			(item.Status != NoticeItemSucceeded && item.Status != NoticeItemDenied &&
				item.Status != NoticeItemFailed && item.Status != NoticeItemUnknown) {
			return false
		}
		if item.Status == NoticeItemSucceeded && item.ErrorCode != "" {
			return false
		}
		if item.Status != NoticeItemSucceeded && item.ErrorCode == "" {
			return false
		}
		if _, duplicate := seen[item.Group.GroupID]; duplicate {
			return false
		}
		seen[item.Group.GroupID] = struct{}{}
	}
	return true
}

func (s *Service) noticeRequestHash(input NoticePublishInput) string {
	groupIDs := append([]string(nil), input.GroupIDs...)
	sort.Strings(groupIDs)
	mac := hmac.New(sha256.New, s.idempotencyKey)
	_, _ = mac.Write([]byte(noticeHashDomain))
	_, _ = mac.Write([]byte(input.Content))
	for _, groupID := range groupIDs {
		_, _ = mac.Write([]byte{0})
		_, _ = mac.Write([]byte(groupID))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func cloneNoticeTargets(values []NoticeTarget) []NoticeTarget {
	return append([]NoticeTarget(nil), values...)
}

func cloneNoticeResult(value NoticePublishResult) NoticePublishResult {
	value.Items = append([]NoticePublishItem(nil), value.Items...)
	return value
}

func (s *Service) RecoverInterruptedOperations(ctx context.Context) (int, error) {
	syncCount, err := s.RecoverInterruptedSyncs(ctx)
	if err != nil {
		return 0, err
	}
	noticeCount, err := s.store.RecoverInterruptedGroupNoticePublications(ctx, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("recover interrupted group notice publications: %w", err)
	}
	return syncCount + noticeCount, nil
}
