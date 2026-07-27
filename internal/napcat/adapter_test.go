package napcat

import (
	"context"
	"testing"
	"time"
)

func TestSessionWorkersCancelThenWaitForCleanup(t *testing.T) {
	workers := newSessionWorkers(t.Context())
	started := make(chan struct{})
	release := make(chan struct{})
	cleaned := make(chan struct{})
	workers.Start(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		<-release
		close(cleaned)
	})
	<-started

	closed := make(chan struct{})
	go func() {
		workers.Close()
		close(closed)
	}()
	select {
	case <-workers.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("session worker did not receive cancellation")
	}
	select {
	case <-closed:
		t.Fatal("session closed before worker cleanup completed")
	default:
	}
	close(release)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("session worker cleanup did not finish")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("session close did not wait for worker cleanup")
	}
}
