package bot

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/zjutjh/jxh-go/internal/cqreply"
)

const imageReplyUnavailableText = "词条中的图片暂时无法发送，请联系管理员检查图片链接。"

func sendKeywordReply(ctx context.Context, sender Sender, groupID int64, sourceKey, answer string) error {
	parsed := cqreply.Parse(answer)
	if parsed.RejectedImageCount > 0 {
		log.Printf("keyword reply ignored %d unsafe or invalid image source(s), source_key=%q", parsed.RejectedImageCount, sourceKey)
	}
	if parsed.ImageCount == 0 {
		fallback := parsed.PlainText
		if strings.TrimSpace(fallback) == "" && parsed.RejectedImageCount > 0 {
			fallback = imageReplyUnavailableText
		}
		return sender.SendGroupText(ctx, groupID, fallback)
	}

	message := make([]any, 0, len(parsed.Parts))
	for _, part := range parsed.Parts {
		switch part.Type {
		case cqreply.PartText:
			message = append(message, map[string]any{
				"type": "text",
				"data": map[string]any{"text": part.Value},
			})
		case cqreply.PartImage:
			message = append(message, map[string]any{
				"type": "image",
				"data": map[string]any{"file": part.Value},
			})
		}
	}
	if err := sender.SendGroupMessage(ctx, groupID, message); err != nil {
		log.Printf("send keyword image reply failed, source_key=%q: %v", sourceKey, err)
		fallback := parsed.PlainText
		if strings.TrimSpace(fallback) == "" {
			fallback = imageReplyUnavailableText
		}
		if fallbackErr := sender.SendGroupText(ctx, groupID, fallback); fallbackErr != nil {
			return fmt.Errorf("send keyword image reply: %v; send text fallback: %w", err, fallbackErr)
		}
	}
	return nil
}
