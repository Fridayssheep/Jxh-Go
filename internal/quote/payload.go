package quote

import (
	"context"
	"maps"
	"strings"
)

type MessageInput struct {
	UserID     int64
	Nickname   string
	RawMessage string
	Message    any
}

type ImageResolver interface {
	ResolveImage(ctx context.Context, file string) (string, error)
}

func BuildPayload(ctx context.Context, inputs []MessageInput, resolver ImageResolver) Payload {
	payload := make(Payload, 0, len(inputs))
	for _, input := range inputs {
		message := input.Message
		if message != nil {
			message = enrichMessageImages(ctx, message, resolver)
		}
		content := contentFromMessage(input.RawMessage, message)
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

func enrichMessageImages(ctx context.Context, raw any, resolver ImageResolver) any {
	segments, ok := decodeOneBotSegments(raw)
	if !ok {
		return raw
	}
	out := make([]map[string]any, 0, len(segments))
	for _, segment := range segments {
		outSegment := map[string]any{
			"type": segment.Type,
			"data": maps.Clone(segment.Data),
		}
		switch segment.Type {
		case "image", "mface", "marketface", "emoji":
			outSegment["data"] = enrichImageData(ctx, segment.Data, resolver)
		}
		out = append(out, outSegment)
	}
	return out
}

func enrichImageData(ctx context.Context, data map[string]any, resolver ImageResolver) map[string]any {
	out := maps.Clone(data)
	for _, source := range []string{segmentDataString(data, "url"), segmentDataString(data, "file")} {
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
