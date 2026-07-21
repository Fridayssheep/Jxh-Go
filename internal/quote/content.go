package quote

import (
	"html"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/zjutjh/napcat-sdk/message"
)

func contentFromMessage(raw string, structured message.Chain) []MessageSegment {
	if len(structured) > 0 {
		return structuredMessageContent(structured)
	}
	return parseCQMessage(raw)
}

func isEmptyContent(content []MessageSegment) bool {
	for _, segment := range content {
		if segment.Type != "text" || strings.TrimSpace(segment.Text) != "" {
			return false
		}
	}
	return true
}

func structuredMessageContent(chain message.Chain) []MessageSegment {
	var segments []MessageSegment
	for _, segment := range chain {
		switch segment.Type {
		case "reply":
			continue
		case "text":
			appendTextSegment(&segments, segment.String("text"))
		case "image":
			appendStructuredImageSegment(&segments, segment, "", "[图片]")
		case "mface", "marketface":
			appendStructuredImageSegment(&segments, segment, "sticker", "[表情]")
		case "face", "sface", "bface":
			appendQQFaceSegment(&segments, firstNonEmpty(segment.String("id"), segment.String("face_id"), segment.String("emoji_id")), segment.String("url"))
		case "emoji":
			if source := firstUsableImageSource(segment.String("url"), segment.String("file")); source != "" {
				appendImageSegment(&segments, source, "emoji")
			} else {
				appendEmojiSegment(&segments, firstNonEmpty(segment.String("id"), segment.String("emoji_id")), "")
			}
		case "at":
			appendAtSegment(&segments, segment)
		case "record":
			appendTextSegment(&segments, "[语音]")
		case "video":
			appendTextSegment(&segments, "[视频]")
		}
	}
	return mergeAdjacentTextSegments(segments)
}

func parseCQMessage(raw string) []MessageSegment {
	var segments []MessageSegment
	for len(raw) > 0 {
		start := strings.Index(raw, "[CQ:")
		if start < 0 {
			appendTextSegment(&segments, raw)
			break
		}
		appendTextSegment(&segments, raw[:start])
		raw = raw[start:]

		end := strings.IndexByte(raw, ']')
		if end < 0 {
			appendTextSegment(&segments, raw)
			break
		}
		appendCQSegment(&segments, raw[4:end])
		raw = raw[end+1:]
	}
	return mergeAdjacentTextSegments(segments)
}

func appendTextSegment(segments *[]MessageSegment, text string) {
	text = html.UnescapeString(text)
	if text == "" {
		return
	}
	*segments = append(*segments, MessageSegment{Type: "text", Text: text})
}

func appendCQSegment(segments *[]MessageSegment, body string) {
	parts := strings.Split(body, ",")
	if len(parts) == 0 {
		return
	}
	segmentType := parts[0]
	params := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		params[key] = html.UnescapeString(value)
	}

	switch segmentType {
	case "reply":
		return
	case "image":
		appendURLImageSegment(segments, params["url"], "", "[图片]")
	case "mface", "marketface":
		appendURLImageSegment(segments, params["url"], "sticker", "[表情]")
	case "face", "sface", "bface":
		appendQQFaceSegment(segments, firstNonEmpty(params["id"], params["face_id"], params["emoji_id"]), params["url"])
	case "emoji":
		appendEmojiSegment(segments, firstNonEmpty(params["id"], params["emoji_id"]), params["url"])
	case "dice", "rps":
		appendTextSegment(segments, "[表情]")
	case "text":
		appendTextSegment(segments, params["text"])
	}
}

func appendURLImageSegment(segments *[]MessageSegment, url, kind, fallback string) {
	url = strings.TrimSpace(url)
	if url == "" {
		appendTextSegment(segments, fallback)
		return
	}
	appendImageSegment(segments, url, kind)
}

func appendStructuredImageSegment(segments *[]MessageSegment, segment message.Segment, kind, fallback string) {
	source := firstUsableImageSource(segment.String("url"), segment.String("file"))
	if source == "" {
		appendTextSegment(segments, fallback)
		return
	}
	appendImageSegment(segments, source, kind)
}

func appendImageSegment(segments *[]MessageSegment, url, kind string) {
	url = normalizeImageSource(url)
	*segments = append(*segments, MessageSegment{Type: "image", Kind: kind, URL: url})
}

func normalizeImageSource(source string) string {
	source = strings.TrimSpace(source)
	if strings.HasPrefix(source, "base64://") {
		return "data:image/png;base64," + strings.TrimPrefix(source, "base64://")
	}
	return source
}

func appendQQFaceSegment(segments *[]MessageSegment, id, url string) {
	if len(id) <= 10 {
		if _, err := strconv.ParseUint(id, 10, 64); err == nil {
			*segments = append(*segments, MessageSegment{Type: "face", ID: id})
			return
		}
	}
	if url = strings.TrimSpace(url); url != "" {
		appendImageSegment(segments, url, "emoji")
		return
	}
	appendTextSegment(segments, "[表情]")
}

func appendEmojiSegment(segments *[]MessageSegment, id, url string) {
	if url = strings.TrimSpace(url); url != "" {
		appendImageSegment(segments, url, "emoji")
		return
	}
	if text, ok := unicodeEmoji(id); ok {
		appendTextSegment(segments, text)
		return
	}
	appendTextSegment(segments, "[表情]")
}

func appendAtSegment(segments *[]MessageSegment, segment message.Segment) {
	name := firstNonEmpty(segment.String("name"), segment.String("card"), segment.String("nickname"))
	if name == "" {
		name = segment.String("qq")
	}
	if name == "" {
		return
	}
	appendTextSegment(segments, "@"+name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstUsableImageSource(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if isUsableImageSource(value) {
			return value
		}
	}
	return ""
}

func isUsableImageSource(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(lower, "data:image/") ||
		strings.HasPrefix(lower, "base64://")
}

func unicodeEmoji(id string) (string, bool) {
	codepoint, err := strconv.ParseInt(strings.TrimSpace(id), 10, 32)
	if err != nil {
		return "", false
	}
	r := rune(codepoint)
	if !utf8.ValidRune(r) || r == utf8.RuneError {
		return "", false
	}
	return string(r), true
}

func mergeAdjacentTextSegments(segments []MessageSegment) []MessageSegment {
	if len(segments) < 2 {
		return segments
	}
	merged := make([]MessageSegment, 0, len(segments))
	for _, segment := range segments {
		if segment.Type == "text" && len(merged) > 0 && merged[len(merged)-1].Type == "text" {
			merged[len(merged)-1].Text += segment.Text
			continue
		}
		merged = append(merged, segment)
	}
	return merged
}
