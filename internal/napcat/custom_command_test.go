package napcat

import (
	"errors"
	"testing"

	"github.com/zjutjh/jxh-go/internal/customcommand"
)

func TestCustomCommandGatewayRejectsInvalidIDsAndUnavailableGateway(t *testing.T) {
	adapter := NewCustomCommandGateway(NewGateway())
	if adapter.Available() {
		t.Fatal("disconnected gateway reported available")
	}
	if err := adapter.ReplyText(t.Context(), "not-a-group", "hello"); !errors.Is(err, customcommand.ErrInvalidInput) {
		t.Fatalf("invalid ID error=%v", err)
	}
	if err := adapter.ReplyText(t.Context(), "123", "hello"); !errors.Is(err, customcommand.ErrGatewayUnavailable) {
		t.Fatalf("unavailable error=%v", err)
	}
	if NewCustomCommandGateway(nil).Available() {
		t.Fatal("nil gateway reported available")
	}
}

func TestCustomCommandGatewayMapsOnlyUncertainOutcomes(t *testing.T) {
	for _, code := range []FailureCode{FailureCanceled, FailureTimeout, FailureDisconnected, FailureTransport, FailureUnknown} {
		if err := mapCustomCommandActionError(operationFailure("send", code)); !errors.Is(err, customcommand.ErrOutcomeUnknown) {
			t.Fatalf("code=%s err=%v", code, err)
		}
	}
	if err := mapCustomCommandActionError(operationFailure("send", FailureUpstreamRejected)); errors.Is(err, customcommand.ErrOutcomeUnknown) || !errors.Is(err, ErrOperationFailed) {
		t.Fatalf("definitive rejection error=%v", err)
	}
}
