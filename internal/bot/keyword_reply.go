package bot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/zjutjh/jxh-go/internal/cqreply"
	napcatsdk "github.com/zjutjh/napcat-sdk"
	"github.com/zjutjh/napcat-sdk/message"
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

	chain := make(message.Chain, 0, len(parsed.Parts))
	for _, part := range parsed.Parts {
		switch part.Type {
		case cqreply.PartText:
			chain = append(chain, message.Text(part.Value))
		case cqreply.PartImage:
			chain = append(chain, message.Image(part.Value))
		}
	}
	if err := sender.SendGroupMessage(ctx, groupID, chain); err != nil {
		if isAmbiguousImageSendTimeout(err) {
			return fmt.Errorf("keyword image send outcome unknown, source_key=%q: %w", sourceKey, err)
		}
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

func isAmbiguousImageSendTimeout(err error) bool {
	if errors.Is(err, napcatsdk.ErrTimeout) {
		return true
	}
	var apiErr *napcatsdk.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	detail := strings.ToLower(apiErr.Message + " " + apiErr.Wording)
	return strings.Contains(detail, "timeout") || strings.Contains(detail, "超时")
}
