package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/zjutjh/jxh-go/internal/management/auth"
	managersystem "github.com/zjutjh/jxh-go/internal/management/system"
)

func TestManagerSystemMySQLRecoversInterruptedRestarts(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)
	requestedAt := time.Date(2026, time.July, 28, 9, 0, 0, 0, time.UTC)
	insertManagerAuthTestUser(t, sqlDB, "usr_root", "root-admin", auth.RoleSuperAdmin, requestedAt)
	actor := auth.Principal{UserID: "usr_root", SessionID: "ses_root", Role: auth.RoleSuperAdmin}

	accepted, fresh, err := store.BeginNapCatRestart(t.Context(), managersystem.BeginRestart{
		Actor: actor, Context: auth.MutationContext{RequestID: "req_accepted"}, IdempotencyKey: "restart-accepted",
		RequestHash: "hash-accepted", Reason: "test recovery", RequestedAt: requestedAt,
	})
	if err != nil || !fresh {
		t.Fatalf("begin accepted restart: operation=%+v fresh=%t error=%v", accepted, fresh, err)
	}
	found, exists, err := store.FindNapCatRestart(t.Context(), managersystem.FindRestart{
		ActorID: actor.UserID, IdempotencyKey: "restart-accepted", RequestHash: "hash-accepted",
		At: requestedAt.Add(time.Second),
	})
	if err != nil || !exists || found.ID != accepted.ID || found.Status != managersystem.StatusAccepted {
		t.Fatalf("find accepted restart: operation=%+v exists=%t error=%v", found, exists, err)
	}
	if _, _, err := store.FindNapCatRestart(t.Context(), managersystem.FindRestart{
		ActorID: actor.UserID, IdempotencyKey: "restart-accepted", RequestHash: "different-hash",
		At: requestedAt.Add(time.Second),
	}); !errors.Is(err, managersystem.ErrIdempotencyConflict) {
		t.Fatalf("find conflicting restart error=%v", err)
	}
	running, fresh, err := store.BeginNapCatRestart(t.Context(), managersystem.BeginRestart{
		Actor: actor, Context: auth.MutationContext{RequestID: "req_running"}, IdempotencyKey: "restart-running",
		RequestHash: "hash-running", Reason: "test recovery", RequestedAt: requestedAt.Add(time.Second),
	})
	if err != nil || !fresh {
		t.Fatalf("begin running restart: operation=%+v fresh=%t error=%v", running, fresh, err)
	}
	if _, err := store.TransitionNapCatRestart(t.Context(), managersystem.Transition{
		OperationID: running.ID, From: managersystem.StatusAccepted, To: managersystem.StatusRunning,
		At: requestedAt.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	recoveredAt := requestedAt.Add(time.Minute)
	recovered, err := store.RecoverInterruptedNapCatRestarts(t.Context(), recoveredAt)
	if err != nil || len(recovered) != 2 {
		t.Fatalf("recovered=%+v error=%v", recovered, err)
	}
	for _, operation := range recovered {
		if operation.Status != managersystem.StatusUnknown || operation.CompletedAt == nil || !operation.CompletedAt.Equal(recoveredAt) ||
			operation.ErrorCode == nil || *operation.ErrorCode != "restart_interrupted" {
			t.Fatalf("recovered operation=%+v", operation)
		}
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM system_operations WHERE status = 'unknown' AND error_code = 'restart_interrupted'", 2)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_idempotency_keys WHERE state = 'completed' AND result_status = 'unknown' AND error_code = 'restart_interrupted'", 2)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'system.napcat_restart' AND result = 'unknown' AND error_code = 'restart_interrupted'", 2)
	found, exists, err = store.FindNapCatRestart(t.Context(), managersystem.FindRestart{
		ActorID: actor.UserID, IdempotencyKey: "restart-accepted", RequestHash: "hash-accepted",
		At: recoveredAt,
	})
	if err != nil || !exists || found.Status != managersystem.StatusUnknown || found.CompletedAt == nil {
		t.Fatalf("find recovered restart: operation=%+v exists=%t error=%v", found, exists, err)
	}

	replayed, err := store.RecoverInterruptedNapCatRestarts(t.Context(), recoveredAt.Add(time.Minute))
	if err != nil || len(replayed) != 0 {
		t.Fatalf("second recovery=%+v error=%v", replayed, err)
	}
}
