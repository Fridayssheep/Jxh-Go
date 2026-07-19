package linkcleaner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zjutjh/jxh-go/internal/bot"
)

const (
	defaultTimeout      = 5 * time.Second
	defaultMaxRedirects = 3
	maxCardJSONBytes    = 512 << 10
	maxValueDepth       = 8
	maxCandidateURLs    = 32
)

var httpURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

type Options struct {
	Client       *http.Client
	Timeout      time.Duration
	MaxRedirects int
}

type Service struct {
	client       *http.Client
	maxRedirects int
}

func NewService(opts Options) *Service {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{}
	}
	clientCopy := *client
	clientCopy.Timeout = timeout
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	maxRedirects := opts.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = defaultMaxRedirects
	}
	return &Service{client: &clientCopy, maxRedirects: maxRedirects}
}

func (s *Service) CleanMessage(ctx context.Context, text string, segments []bot.MessageSegment) ([]string, error) {
	candidates := extractMessageURLs(text, segments)
	cleaned := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	var errs []error
	for _, candidate := range candidates {
		value, changed, err := s.CleanURL(ctx, candidate)
		if err != nil {
			errs = append(errs, fmt.Errorf("clean %q: %w", candidate, err))
			continue
		}
		if !changed {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		cleaned = append(cleaned, value)
	}
	return cleaned, errors.Join(errs...)
}

func (s *Service) CleanURL(ctx context.Context, raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false, err
	}
	kind := classifyHost(parsed.Hostname())
	if kind == hostUnsupported {
		return raw, false, nil
	}
	if err := validateNetworkURL(parsed); err != nil {
		return "", false, err
	}
	if kind == hostB23 || kind == hostXHSLink {
		if s == nil || s.client == nil {
			return "", false, fmt.Errorf("short-link resolver is not initialized")
		}
		parsed, err = s.resolveShortURL(ctx, parsed, kind)
		if err != nil {
			return "", false, err
		}
		kind = classifyHost(parsed.Hostname())
	}

	switch kind {
	case hostBilibili:
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
	case hostXiaohongshu:
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return "", false, fmt.Errorf("parse query: %w", err)
		}
		cleanQuery := make(url.Values)
		for _, token := range query["xsec_token"] {
			cleanQuery.Add("xsec_token", token)
		}
		parsed.RawQuery = cleanQuery.Encode()
		parsed.ForceQuery = false
		parsed.Fragment = ""
	default:
		return raw, false, nil
	}

	value := parsed.String()
	return value, value != raw, nil
}

func (s *Service) resolveShortURL(ctx context.Context, initial *url.URL, shortKind hostKind) (*url.URL, error) {
	current := cloneURL(initial)
	for i := 0; i < s.maxRedirects; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "Jxh-Go link cleaner")
		resp, err := s.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if resp.StatusCode < http.StatusMultipleChoices || resp.StatusCode >= http.StatusBadRequest {
			return nil, fmt.Errorf("short link returned HTTP %d", resp.StatusCode)
		}
		location := strings.TrimSpace(resp.Header.Get("Location"))
		if location == "" {
			return nil, fmt.Errorf("short link redirect has no Location header")
		}
		nextRef, err := url.Parse(location)
		if err != nil {
			return nil, fmt.Errorf("parse redirect: %w", err)
		}
		next := current.ResolveReference(nextRef)
		if err := validateRedirectTarget(next, shortKind); err != nil {
			return nil, err
		}
		nextKind := classifyHost(next.Hostname())
		if shortKind == hostB23 && nextKind == hostBilibili {
			return next, nil
		}
		if shortKind == hostXHSLink && nextKind == hostXiaohongshu {
			return next, nil
		}
		current = next
	}
	return nil, fmt.Errorf("short link exceeded %d redirects", s.maxRedirects)
}

func extractMessageURLs(text string, segments []bot.MessageSegment) []string {
	var candidates []string
	seen := make(map[string]struct{})
	appendFromString(&candidates, seen, text)
	for _, segment := range segments {
		switch strings.ToLower(strings.TrimSpace(segment.Type)) {
		case "json", "miniapp":
			appendFromValue(&candidates, seen, segment.Data, 0)
		}
		if len(candidates) >= maxCandidateURLs {
			break
		}
	}
	return candidates
}

func appendFromValue(out *[]string, seen map[string]struct{}, value any, depth int) {
	if depth > maxValueDepth || len(*out) >= maxCandidateURLs {
		return
	}
	switch current := value.(type) {
	case string:
		appendFromString(out, seen, current)
		trimmed := strings.TrimSpace(current)
		if len(trimmed) == 0 || len(trimmed) > maxCardJSONBytes {
			return
		}
		switch trimmed[0] {
		case '{', '[', '"':
		default:
			return
		}
		var nested any
		if json.Unmarshal([]byte(trimmed), &nested) == nil {
			appendFromValue(out, seen, nested, depth+1)
		}
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendFromValue(out, seen, current[key], depth+1)
		}
	case map[string]string:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			appendFromValue(out, seen, current[key], depth+1)
		}
	case []any:
		for _, item := range current {
			appendFromValue(out, seen, item, depth+1)
		}
	case []string:
		for _, item := range current {
			appendFromValue(out, seen, item, depth+1)
		}
	}
}

func appendFromString(out *[]string, seen map[string]struct{}, value string) {
	if len(*out) >= maxCandidateURLs {
		return
	}
	value = html.UnescapeString(value)
	for _, match := range httpURLPattern.FindAllString(value, maxCandidateURLs-len(*out)) {
		candidate := strings.TrimRight(match, ".,;:!?)]}>，。；：！？）】》")
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		*out = append(*out, candidate)
	}
}

type hostKind int

const (
	hostUnsupported hostKind = iota
	hostB23
	hostBilibili
	hostXHSLink
	hostXiaohongshu
)

func classifyHost(host string) hostKind {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	switch {
	case hostMatches(host, "b23.tv"):
		return hostB23
	case hostMatches(host, "bilibili.com"):
		return hostBilibili
	case hostMatches(host, "xhslink.com"):
		return hostXHSLink
	case hostMatches(host, "xiaohongshu.com"):
		return hostXiaohongshu
	default:
		return hostUnsupported
	}
}

func hostMatches(host, root string) bool {
	return host == root || strings.HasSuffix(host, "."+root)
}

func validateRedirectTarget(target *url.URL, shortKind hostKind) error {
	if err := validateNetworkURL(target); err != nil {
		return fmt.Errorf("unsafe redirect: %w", err)
	}
	kind := classifyHost(target.Hostname())
	if shortKind == hostB23 && kind != hostB23 && kind != hostBilibili {
		return fmt.Errorf("b23.tv redirected to unsupported host %q", target.Hostname())
	}
	if shortKind == hostXHSLink && kind != hostXHSLink && kind != hostXiaohongshu {
		return fmt.Errorf("xhslink.com redirected to unsupported host %q", target.Hostname())
	}
	return nil
}

func validateNetworkURL(value *url.URL) error {
	if value == nil || (value.Scheme != "http" && value.Scheme != "https") || value.Hostname() == "" {
		return fmt.Errorf("URL must use http or https and include a host")
	}
	if value.User != nil {
		return fmt.Errorf("URL credentials are not allowed")
	}
	if port := value.Port(); port != "" && port != "80" && port != "443" {
		return fmt.Errorf("URL port %q is not allowed", port)
	}
	return nil
}

func cloneURL(value *url.URL) *url.URL {
	copy := *value
	return &copy
}
