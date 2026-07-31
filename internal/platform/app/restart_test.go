package app

import (
	"context"
	"errors"
	"testing"
)

func TestRestartCoordinatorSchedulesOnlyOnce(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	coordinator := NewRestartCoordinator(cancel)
	if errors.Is(ErrRestartRequested, context.Canceled) {
		t.Fatal("restart cause must be distinguishable from an ordinary cancellation")
	}

	if !coordinator.Schedule("operation-first") {
		t.Fatal("first restart request was rejected")
	}
	<-ctx.Done()
	if !errors.Is(context.Cause(ctx), ErrRestartRequested) {
		t.Fatalf("context cause = %v, want %v", context.Cause(ctx), ErrRestartRequested)
	}
	if coordinator.Schedule("operation-second") {
		t.Fatal("second restart request was accepted")
	}
}
