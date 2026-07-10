package napcat

import (
	"testing"

	"github.com/zjutjh/napcat-sdk/event"
	"github.com/zjutjh/napcat-sdk/message"
)

func TestToGroupMessageMarksOwnerAndExtractsMentions(t *testing.T) {
	ev := &event.GroupMessage{
		GroupID:    1001,
		UserID:     2002,
		MessageID:  3003,
		RawMessage: "@bot /test",
		Sender: event.GroupSender{
			Role: "owner",
		},
		Message: message.ChainOf(
			message.At(9999),
			message.Text("/test"),
		),
	}
	ev.Base.EventSelfID = 9999

	msg := toGroupMessage(ev)

	if !msg.IsOwner {
		t.Fatal("IsOwner = false, want true")
	}
	if msg.SelfID != 9999 {
		t.Fatalf("SelfID = %d, want 9999", msg.SelfID)
	}
	if len(msg.AtUsers) != 1 || msg.AtUsers[0] != 9999 {
		t.Fatalf("AtUsers = %v, want [9999]", msg.AtUsers)
	}
	if msg.Text != "/test" {
		t.Fatalf("Text = %q, want /test", msg.Text)
	}
}
