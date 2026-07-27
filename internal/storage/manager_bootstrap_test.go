package storage

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/zjutjh/jxh-go/internal/auth"
)

func TestManagerBootstrapMySQLCreatesExactlyOneAuditedAdmin(t *testing.T) {
	store, sqlDB := openManagerAuthTestStore(t)

	const workers = 8
	type outcome struct {
		user    auth.User
		created bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan outcome, workers)
	var group sync.WaitGroup
	for index := range workers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			user, created, err := store.CreateFirstSuperAdmin(t.Context(), auth.BootstrapAdmin{
				User: auth.User{
					Username: fmt.Sprintf("root-%d", index), DisplayName: fmt.Sprintf("Root %d", index),
					Role: auth.RoleSuperAdmin, Enabled: true, Version: 1,
				},
				PasswordHash: fmt.Sprintf("$argon2id$sensitive-%d", index),
			})
			results <- outcome{user: user, created: created, err: err}
		}(index)
	}
	close(start)
	group.Wait()
	close(results)

	var winner auth.User
	createdCount := 0
	for result := range results {
		if result.err != nil {
			t.Fatalf("bootstrap: %v", result.err)
		}
		if result.created {
			createdCount++
			winner = result.user
		} else if result.user.ID != "" {
			t.Fatalf("rejected bootstrap returned user %+v", result.user)
		}
	}
	if createdCount != 1 || winner.ID == "" || winner.Role != auth.RoleSuperAdmin || !winner.Enabled || winner.Version != 1 {
		t.Fatalf("bootstrap winner = %+v, created count = %d", winner, createdCount)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_users", 1)
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_audit_logs WHERE action = 'user.bootstrap'", 1)

	var afterSnapshot, metadata []byte
	if err := sqlDB.QueryRowContext(t.Context(), `SELECT after_snapshot, metadata
FROM admin_audit_logs WHERE action = 'user.bootstrap'`).Scan(&afterSnapshot, &metadata); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(afterSnapshot)+string(metadata), "argon2id") {
		t.Fatalf("bootstrap audit contains a password hash: after=%s metadata=%s", afterSnapshot, metadata)
	}

	if _, err := sqlDB.ExecContext(t.Context(), "UPDATE admin_users SET enabled = FALSE, deleted_at = CURRENT_TIMESTAMP(3)"); err != nil {
		t.Fatal(err)
	}
	_, created, err := store.CreateFirstSuperAdmin(context.Background(), auth.BootstrapAdmin{
		User:         auth.User{Username: "second-root", DisplayName: "Second Root", Role: auth.RoleSuperAdmin, Enabled: true, Version: 1},
		PasswordHash: "$argon2id$must-not-be-stored",
	})
	if err != nil || created {
		t.Fatalf("bootstrap after soft deletion: created=%t error=%v", created, err)
	}
	assertManagerAuthCount(t, sqlDB, "SELECT COUNT(*) FROM admin_users", 1)
}
