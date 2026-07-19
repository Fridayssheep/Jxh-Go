package bot

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/zjutjh/jxh-go/internal/knowledge"
)

type cqReplySender struct {
	textCalls    []string
	messageCalls []any
	textErr      error
	messageErr   error
}

func (s *cqReplySender) SendGroupText(ctx context.Context, groupID int64, text string) error {
	_ = ctx
	_ = groupID
	s.textCalls = append(s.textCalls, text)
	return s.textErr
}

func (s *cqReplySender) SendGroupMessage(ctx context.Context, groupID int64, message any) error {
	_ = ctx
	_ = groupID
	s.messageCalls = append(s.messageCalls, message)
	return s.messageErr
}

func pipelineWithCQAnswer(sender Sender, answer string) *Pipeline {
	knowledgeIndex := knowledge.NewIndexRef([]knowledge.Entry{{
		SourceKey:  "map",
		Keyword:    "map",
		Answer:     answer,
		Enabled:    true,
		ExactReply: true,
	}})
	return NewPipeline(Options{Knowledge: knowledgeIndex, Sender: sender})
}

func TestPipelineSendsCQImageAnswerAsOrderedMessageChain(t *testing.T) {
	sender := &cqReplySender{}
	pipeline := pipelineWithCQAnswer(sender, "before[CQ:image,file=cache.image,url=https://cdn.example.com/map.png]after")

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 1001, UserID: 2002, Text: "map"})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if len(sender.textCalls) != 0 {
		t.Fatalf("text calls = %#v, want none", sender.textCalls)
	}
	want := []any{
		map[string]any{"type": "text", "data": map[string]any{"text": "before"}},
		map[string]any{"type": "image", "data": map[string]any{"file": "https://cdn.example.com/map.png"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "after"}},
	}
	if len(sender.messageCalls) != 1 || !reflect.DeepEqual(sender.messageCalls[0], want) {
		t.Fatalf("message calls = %#v, want %#v", sender.messageCalls, want)
	}
}

func TestPipelineSendsLocalCQImageFromFixedNapCatMount(t *testing.T) {
	sender := &cqReplySender{}
	pipeline := pipelineWithCQAnswer(sender, "before[CQ:image,file=maps/campus map.png]after")

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 1001, UserID: 2002, Text: "map"})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	want := []any{
		map[string]any{"type": "text", "data": map[string]any{"text": "before"}},
		map[string]any{"type": "image", "data": map[string]any{"file": "file:///app/jxh-media/maps/campus%20map.png"}},
		map[string]any{"type": "text", "data": map[string]any{"text": "after"}},
	}
	if len(sender.messageCalls) != 1 || !reflect.DeepEqual(sender.messageCalls[0], want) {
		t.Fatalf("message calls = %#v, want %#v", sender.messageCalls, want)
	}
}

func TestPipelineFallsBackToSurroundingTextWhenImageSendFails(t *testing.T) {
	sender := &cqReplySender{messageErr: errors.New("image rejected")}
	pipeline := pipelineWithCQAnswer(sender, "before[CQ:image,url=https://cdn.example.com/map.png]after")

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 1001, UserID: 2002, Text: "map"})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if !reflect.DeepEqual(sender.textCalls, []string{"beforeafter"}) {
		t.Fatalf("text calls = %#v, want text fallback", sender.textCalls)
	}
}

func TestPipelineReportsImageOnlyAnswerFailure(t *testing.T) {
	sender := &cqReplySender{messageErr: errors.New("image rejected")}
	pipeline := pipelineWithCQAnswer(sender, "[CQ:image,url=https://cdn.example.com/map.png]")

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 1001, UserID: 2002, Text: "map"})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if !reflect.DeepEqual(sender.textCalls, []string{imageReplyUnavailableText}) {
		t.Fatalf("text calls = %#v, want unavailable notice", sender.textCalls)
	}
}

func TestPipelineDoesNotExecuteUnsupportedCQTypes(t *testing.T) {
	sender := &cqReplySender{}
	answer := "hello [CQ:at,qq=all]"
	pipeline := pipelineWithCQAnswer(sender, answer)

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 1001, UserID: 2002, Text: "map"})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if !reflect.DeepEqual(sender.textCalls, []string{answer}) {
		t.Fatalf("text calls = %#v, want literal unsupported CQ", sender.textCalls)
	}
	if len(sender.messageCalls) != 0 {
		t.Fatalf("message calls = %#v, want none", sender.messageCalls)
	}
}

func TestPipelineReportsRejectedImageOnlyAnswer(t *testing.T) {
	sender := &cqReplySender{}
	pipeline := pipelineWithCQAnswer(sender, "[CQ:image,file=file:///tmp/map.png]")

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 1001, UserID: 2002, Text: "map"})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if !reflect.DeepEqual(sender.textCalls, []string{imageReplyUnavailableText}) {
		t.Fatalf("text calls = %#v, want unavailable notice", sender.textCalls)
	}
}
