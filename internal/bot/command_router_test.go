package bot

import (
	"context"
	"testing"

	"github.com/zjutjh/jxh-go/internal/ai"
)

type recordingSender struct {
	groupID int64
	text    string
}

func (s *recordingSender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	_ = ctx
	s.groupID = groupID
	s.text = text
	return nil
}

func (s *recordingSender) SendGroupMessage(ctx context.Context, groupID int64, message any) error {
	_ = ctx
	_ = groupID
	_ = message
	return nil
}

func TestGroupCommandRouterHandlesTestCommand(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		Text:    "/test",
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /test")
	}
	if sender.groupID != 123 {
		t.Fatalf("sent group ID = %d, want 123", sender.groupID)
	}
	if sender.text != "精小弘正常" {
		t.Fatalf("sent text = %q, want %q", sender.text, "精小弘正常")
	}
}

func TestGroupCommandRouterReturnsDisabledAnswerWhenAIServiceIsNil(t *testing.T) {
	sender := &recordingSender{}
	router := NewGroupCommandRouter(Options{})

	handled, err := router.Handle(context.Background(), GroupMessage{
		GroupID: 123,
		Text:    "/ai 报到",
	}, sender)

	if err != nil {
		t.Fatalf("Handle returned error: %v", err)
	}
	if !handled {
		t.Fatal("Handle did not handle /ai")
	}
	if sender.groupID != 123 {
		t.Fatalf("sent group ID = %d, want 123", sender.groupID)
	}
	if sender.text != ai.DisabledAnswer {
		t.Fatalf("sent text = %q, want %q", sender.text, ai.DisabledAnswer)
	}
}
