package cqreply

import (
	"html"
	"net/url"
	"path"
	"strings"
)

const (
	PartText       = "text"
	PartImage      = "image"
	localMediaRoot = "/app/jxh-media"
)

type Part struct {
	Type  string
	Value string
}

type Result struct {
	Parts              []Part
	PlainText          string
	ImageCount         int
	RejectedImageCount int
}

func Parse(answer string) Result {
	var result Result
	var plain strings.Builder
	remaining := answer

	appendText := func(text string) {
		if text == "" {
			return
		}
		plain.WriteString(text)
		if n := len(result.Parts); n > 0 && result.Parts[n-1].Type == PartText {
			result.Parts[n-1].Value += text
			return
		}
		result.Parts = append(result.Parts, Part{Type: PartText, Value: text})
	}

	for remaining != "" {
		start := strings.Index(remaining, "[CQ:")
		if start < 0 {
			appendText(remaining)
			break
		}
		appendText(remaining[:start])
		remaining = remaining[start:]

		end := strings.IndexByte(remaining, ']')
		if end < 0 {
			appendText(remaining)
			break
		}
		tag := remaining[:end+1]
		remaining = remaining[end+1:]

		imageURL, isImage := imageURLFromTag(tag)
		if !isImage {
			appendText(tag)
			continue
		}
		if imageURL == "" {
			result.RejectedImageCount++
			continue
		}
		result.Parts = append(result.Parts, Part{Type: PartImage, Value: imageURL})
		result.ImageCount++
	}

	result.PlainText = plain.String()
	return result
}

func imageURLFromTag(tag string) (string, bool) {
	body := strings.TrimSuffix(strings.TrimPrefix(tag, "[CQ:"), "]")
	parts := strings.Split(body, ",")
	if len(parts) == 0 || parts[0] != "image" {
		return "", false
	}

	params := make(map[string]string, len(parts)-1)
	for _, part := range parts[1:] {
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(key)] = html.UnescapeString(strings.TrimSpace(value))
	}
	if isRemoteURL(params["url"]) {
		return params["url"], true
	}
	if isRemoteURL(params["file"]) {
		return params["file"], true
	}
	if local := localImageURI(params["file"]); local != "" {
		return local, true
	}
	return "", true
}

func isRemoteURL(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := url.ParseRequestURI(value)
	return err == nil &&
		(strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) &&
		parsed.Host != "" && parsed.User == nil
}

func localImageURI(value string) string {
	if value == "" || path.IsAbs(value) || strings.ContainsAny(value, `\:?#`) {
		return ""
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ""
		}
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	return (&url.URL{Scheme: "file", Path: path.Join(localMediaRoot, cleaned)}).String()
}
