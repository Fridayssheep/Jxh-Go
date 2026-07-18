package quote

import (
	"encoding/json"
	"html"
	"strconv"
	"strings"
	"unicode/utf8"
)

type onebotSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

func contentFromMessage(raw string, structured any) []MessageSegment {
	if content, ok := structuredMessageContent(structured); ok {
		return content
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

func structuredMessageContent(raw any) ([]MessageSegment, bool) {
	onebotSegments, ok := decodeOneBotSegments(raw)
	if !ok {
		return nil, false
	}
	var segments []MessageSegment
	for _, segment := range onebotSegments {
		switch segment.Type {
		case "reply":
			continue
		case "text":
			appendTextSegment(&segments, segmentDataString(segment.Data, "text"))
		case "image":
			appendDataImageSegment(&segments, segment.Data, "", "[图片]")
		case "mface", "marketface":
			appendDataImageSegment(&segments, segment.Data, "sticker", "[表情]")
		case "face", "sface", "bface":
			appendQQFaceSegment(&segments, stringMapFromAnyMap(segment.Data))
		case "emoji":
			appendStructuredEmojiSegment(&segments, segment.Data)
		case "at":
			appendAtSegment(&segments, segment.Data)
		case "record":
			appendTextSegment(&segments, "[语音]")
		case "video":
			appendTextSegment(&segments, "[视频]")
		}
	}
	segments = mergeAdjacentTextSegments(segments)
	if len(segments) == 0 {
		return nil, true
	}
	return segments, true
}

func decodeOneBotSegments(raw any) ([]onebotSegment, bool) {
	if raw == nil {
		return nil, false
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, false
	}
	var segments []onebotSegment
	if err := json.Unmarshal(data, &segments); err != nil || len(segments) == 0 {
		return nil, false
	}
	return segments, true
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
		appendQQFaceSegment(segments, params)
	case "emoji":
		appendCQEmojiSegment(segments, params)
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

func appendDataImageSegment(segments *[]MessageSegment, data map[string]any, kind, fallback string) {
	source := firstUsableImageSource(segmentDataString(data, "url"), segmentDataString(data, "file"))
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

func appendQQFaceSegment(segments *[]MessageSegment, params map[string]string) {
	id := firstNonEmpty(params["id"], params["face_id"], params["emoji_id"])
	if len(id) <= 10 {
		if _, err := strconv.ParseUint(id, 10, 64); err == nil {
			*segments = append(*segments, MessageSegment{Type: "face", ID: id})
			return
		}
	}
	if url := strings.TrimSpace(params["url"]); url != "" {
		appendImageSegment(segments, url, "emoji")
		return
	}
	appendTextSegment(segments, "[表情]")
}

func appendCQEmojiSegment(segments *[]MessageSegment, params map[string]string) {
	if url := strings.TrimSpace(params["url"]); url != "" {
		appendImageSegment(segments, url, "emoji")
		return
	}
	id := firstNonEmpty(params["id"], params["emoji_id"])
	if text, ok := unicodeEmoji(id); ok {
		appendTextSegment(segments, text)
		return
	}
	appendTextSegment(segments, "[表情]")
}

func appendStructuredEmojiSegment(segments *[]MessageSegment, data map[string]any) {
	if source := firstUsableImageSource(segmentDataString(data, "url"), segmentDataString(data, "file")); source != "" {
		appendImageSegment(segments, source, "emoji")
		return
	}
	appendCQEmojiSegment(segments, stringMapFromAnyMap(data))
}

func appendAtSegment(segments *[]MessageSegment, data map[string]any) {
	name := firstNonEmpty(segmentDataString(data, "name"), segmentDataString(data, "card"), segmentDataString(data, "nickname"))
	if name == "" {
		name = segmentDataString(data, "qq")
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

func segmentDataString(data map[string]any, key string) string {
	if data == nil {
		return ""
	}
	switch value := data[key].(type) {
	case string:
		return value
	case float64:
		return strconv.FormatInt(int64(value), 10)
	case int:
		return strconv.Itoa(value)
	case int64:
		return strconv.FormatInt(value, 10)
	default:
		return ""
	}
}

func stringMapFromAnyMap(data map[string]any) map[string]string {
	out := make(map[string]string, len(data))
	for key := range data {
		out[key] = segmentDataString(data, key)
	}
	return out
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
