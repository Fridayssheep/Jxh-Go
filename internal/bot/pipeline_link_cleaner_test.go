package bot

import (
	"context"
	"errors"
	"testing"
)

type recordingLinkCleaner struct {
	text     string
	segments []MessageSegment
	cleaned  []string
	err      error
}

func (c *recordingLinkCleaner) CleanMessage(ctx context.Context, text string, segments []MessageSegment) ([]string, error) {
	_ = ctx
	c.text = text
	c.segments = append([]MessageSegment(nil), segments...)
	return append([]string(nil), c.cleaned...), c.err
}

func TestPipelineRepliesWithCleanedTrackedLinks(t *testing.T) {
	sender := &recordingSender{}
	cleaner := &recordingLinkCleaner{cleaned: []string{
		"https://www.bilibili.com/video/BV1one/",
		"https://www.xiaohongshu.com/explore/item?xsec_token=token",
	}}
	pipeline := NewPipeline(Options{Sender: sender, LinkCleaner: cleaner})
	segments := []MessageSegment{{Type: "json", Data: map[string]any{"data": "card"}}}

	err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{
		GroupID:  123,
		UserID:   456,
		Text:     "shared link",
		Segments: segments,
	})

	if err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if cleaner.text != "shared link" || len(cleaner.segments) != 1 {
		t.Fatalf("cleaner input = %q, %+v", cleaner.text, cleaner.segments)
	}
	want := "精小弘觉得这个链接十分甚至九分不对劲，帮你移除了里面的TrackID：\n" +
		"https://www.bilibili.com/video/BV1one/\n" +
		"https://www.xiaohongshu.com/explore/item?xsec_token=token"
	if sender.text != want {
		t.Fatalf("sent text = %q, want %q", sender.text, want)
	}
}

func TestPipelineContinuesWhenTrackedLinkCleaningFails(t *testing.T) {
	sender := &recordingSender{}
	pipeline := NewPipeline(Options{
		Sender:      sender,
		LinkCleaner: &recordingLinkCleaner{err: errors.New("short link unavailable")},
	})

	if err := pipeline.HandleGroupMessage(context.Background(), GroupMessage{GroupID: 123, UserID: 456, Text: "ordinary text"}); err != nil {
		t.Fatalf("HandleGroupMessage returned error: %v", err)
	}
	if sender.text != "" {
		t.Fatalf("sent text = %q, want no response", sender.text)
	}
}
