package napcat

import (
	"errors"
	"testing"
)

func TestScheduledJobSenderValidatesBeforeNetwork(t *testing.T) {
	sender := NewScheduledJobSender(NewGateway())
	if sender.Available() {
		t.Fatal("disconnected sender reported available")
	}
	if _, err := sender.Send(t.Context(), "not-a-group", "hello"); !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("invalid group error=%v", err)
	}
	if _, err := sender.Send(t.Context(), "123", "hello"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unavailable error=%v", err)
	}
	if NewScheduledJobSender(nil).Available() {
		t.Fatal("nil gateway reported available")
	}
}
