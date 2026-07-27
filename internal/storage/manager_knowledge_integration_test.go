package storage

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/auth"
	"github.com/zjutjh/jxh-go/internal/knowledgeadmin"
)

func TestManagerKnowledgeMySQLPersistsReplayAndRecovery(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	requestedAt := time.Date(2026, time.July, 28, 10, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, requestedAt)
	actor := auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}
	begin := knowledgeadmin.BeginReload{
		Actor: actor, Context: auth.MutationContext{RequestID: "req_reload_1"}, OperationID: "kop_reload_1",
		IdempotencyKey: "reload-key-1", RequestHash: strings.Repeat("a", 64), RequestedAt: requestedAt,
	}
	accepted, fresh, err := store.BeginKnowledgeReload(t.Context(), begin)
	if err != nil || !fresh || accepted.Status != knowledgeadmin.OperationAccepted {
		t.Fatalf("begin reload: operation=%+v fresh=%t error=%v", accepted, fresh, err)
	}
	replayed, fresh, err := store.BeginKnowledgeReload(t.Context(), begin)
	if err != nil || fresh || replayed.ID != accepted.ID {
		t.Fatalf("replay accepted reload: operation=%+v fresh=%t error=%v", replayed, fresh, err)
	}
	conflict := begin
	conflict.RequestHash = strings.Repeat("b", 64)
	if _, _, err := store.BeginKnowledgeReload(t.Context(), conflict); !errors.Is(err, knowledgeadmin.ErrIdempotencyConflict) {
		t.Fatalf("idempotency conflict error=%v", err)
	}
	concurrent := begin
	concurrent.OperationID = "kop_reload_2"
	concurrent.IdempotencyKey = "reload-key-2"
	concurrent.Context.RequestID = "req_reload_2"
	if _, _, err := store.BeginKnowledgeReload(t.Context(), concurrent); !errors.Is(err, knowledgeadmin.ErrReloadInProgress) {
		t.Fatalf("concurrent reload error=%v", err)
	}

	running, err := store.TransitionKnowledgeReload(t.Context(), knowledgeadmin.ReloadTransition{
		OperationID: accepted.ID, From: knowledgeadmin.OperationAccepted, To: knowledgeadmin.OperationRunning,
		At: requestedAt.Add(time.Second),
	})
	if err != nil || running.Status != knowledgeadmin.OperationRunning {
		t.Fatalf("running operation=%+v error=%v", running, err)
	}
	failedAt := requestedAt.Add(2 * time.Second)
	failed, err := store.TransitionKnowledgeReload(t.Context(), knowledgeadmin.ReloadTransition{
		OperationID: accepted.ID, From: knowledgeadmin.OperationRunning, To: knowledgeadmin.OperationFailed,
		At: failedAt, ErrorCode: "reload_timeout", OutcomeUnknown: true,
	})
	if err != nil || failed.Status != knowledgeadmin.OperationFailed || failed.CompletedAt == nil || !failed.CompletedAt.Equal(failedAt) {
		t.Fatalf("failed operation=%+v error=%v", failed, err)
	}
	replayed, fresh, err = store.BeginKnowledgeReload(t.Context(), begin)
	if err != nil || fresh || replayed.Status != knowledgeadmin.OperationFailed || replayed.ErrorCode == nil || *replayed.ErrorCode != "reload_timeout" {
		t.Fatalf("replay failed reload: operation=%+v fresh=%t error=%v", replayed, fresh, err)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_idempotency_keys WHERE operation = 'knowledge.reload' AND state = 'completed' AND result_status = 'unknown'", 1)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'knowledge.reload' AND result = 'unknown'", 1)

	interrupted := begin
	interrupted.OperationID = "kop_reload_3"
	interrupted.IdempotencyKey = "reload-key-3"
	interrupted.Context.RequestID = "req_reload_3"
	interrupted.RequestedAt = requestedAt.Add(time.Minute)
	if _, fresh, err := store.BeginKnowledgeReload(t.Context(), interrupted); err != nil || !fresh {
		t.Fatalf("begin interrupted reload: fresh=%t error=%v", fresh, err)
	}
	recoveredAt := requestedAt.Add(2 * time.Minute)
	recovered, err := store.RecoverInterruptedKnowledgeReloads(t.Context(), recoveredAt)
	if err != nil || len(recovered) != 1 || recovered[0].Status != knowledgeadmin.OperationFailed ||
		recovered[0].ErrorCode == nil || *recovered[0].ErrorCode != "reload_interrupted" {
		t.Fatalf("recovered=%+v error=%v", recovered, err)
	}
	if again, err := store.RecoverInterruptedKnowledgeReloads(t.Context(), recoveredAt.Add(time.Minute)); err != nil || len(again) != 0 {
		t.Fatalf("second recovery=%+v error=%v", again, err)
	}
}
