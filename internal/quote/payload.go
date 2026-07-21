package quote

import (
	"context"
	"maps"
	"strings"

	"github.com/zjutjh/napcat-sdk/message"
)

type MessageInput struct {
	UserID     int64
	Nickname   string
	RawMessage string
	Message    message.Chain
}

type ImageResolver interface {
	ResolveImage(ctx context.Context, file string) (string, error)
}

func BuildPayload(ctx context.Context, inputs []MessageInput, resolver ImageResolver) Payload {
	payload := make(Payload, 0, len(inputs))
	for _, input := range inputs {
		chain := input.Message
		if chain != nil {
			chain = enrichMessageImages(ctx, chain, resolver)
		}
		content := contentFromMessage(input.RawMessage, chain)
		if isEmptyContent(content) {
			continue
		}
		nickname := strings.TrimSpace(input.Nickname)
		if nickname == "" {
			nickname = "匿名"
		}
		payload = append(payload, Message{UserID: input.UserID, UserNickname: nickname, Message: content})
	}
	return payload
}

func enrichMessageImages(ctx context.Context, chain message.Chain, resolver ImageResolver) message.Chain {
	out := message.ChainOf(chain...)
	for i, segment := range out {
		switch segment.Type {
		case "image", "mface", "marketface", "emoji":
			data, ok := segment.Data.(map[string]any)
			if ok {
				out[i].Data = enrichImageData(ctx, segment, data, resolver)
			}
		}
	}
	return out
}

func enrichImageData(ctx context.Context, segment message.Segment, data map[string]any, resolver ImageResolver) map[string]any {
	out := maps.Clone(data)
	for _, source := range []string{segment.String("url"), segment.String("file")} {
		if source == "" {
			continue
		}
		if isUsableImageSource(source) {
			out["url"] = source
			return out
		}
		if resolver != nil {
			resolved, err := resolver.ResolveImage(ctx, source)
			if err == nil && isUsableImageSource(resolved) {
				out["url"] = resolved
				return out
			}
		}
	}
	return out
}
