package linkcleaner

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zjutjh/jxh-go/internal/bot"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestServiceCleansBilibiliTrackingParameters(t *testing.T) {
	service := NewService(Options{})
	raw := "https://www.bilibili.com/video/BV1F4411p7Nd/?share_source=copy_web&vd_source=f52d0f7d403bf8730cd0202dee8bfd6f"

	cleaned, changed, err := service.CleanURL(context.Background(), raw)

	if err != nil {
		t.Fatalf("CleanURL returned error: %v", err)
	}
	if !changed || cleaned != "https://www.bilibili.com/video/BV1F4411p7Nd/" {
		t.Fatalf("CleanURL = %q, changed %v", cleaned, changed)
	}
}

func TestServiceDoesNotReplyForCleanOrLookalikeURLs(t *testing.T) {
	service := NewService(Options{})
	for _, raw := range []string{
		"https://www.bilibili.com/video/BV1F4411p7Nd/",
		"https://evil-bilibili.com/video/BV1F4411p7Nd/?vd_source=track",
		"http://example.com:8080/?utm_source=track",
	} {
		cleaned, changed, err := service.CleanURL(context.Background(), raw)
		if err != nil {
			t.Fatalf("CleanURL(%q) returned error: %v", raw, err)
		}
		if changed || cleaned != raw {
			t.Fatalf("CleanURL(%q) = %q, changed %v", raw, cleaned, changed)
		}
	}
}

func TestServiceExpandsSupportedShortLinks(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		location := ""
		switch req.URL.Hostname() {
		case "b23.tv":
			location = "https://www.bilibili.com/video/BV1short/?spm_id_from=333.999&vd_source=track"
		case "xhslink.com":
			location = "https://www.xiaohongshu.com/explore/item123?xsec_token=secret&xsec_source=pc_share&utm_source=share"
		default:
			t.Fatalf("unexpected request host %q", req.URL.Hostname())
		}
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{location}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	service := NewService(Options{Client: client})

	tests := []struct {
		raw  string
		want string
	}{
		{"https://b23.tv/abcd", "https://www.bilibili.com/video/BV1short/"},
		{"https://xhslink.com/a1b2", "https://www.xiaohongshu.com/explore/item123?xsec_token=secret"},
	}
	for _, test := range tests {
		cleaned, changed, err := service.CleanURL(context.Background(), test.raw)
		if err != nil {
			t.Fatalf("CleanURL(%q) returned error: %v", test.raw, err)
		}
		if !changed || cleaned != test.want {
			t.Fatalf("CleanURL(%q) = %q, changed %v, want %q", test.raw, cleaned, changed, test.want)
		}
	}
}

func TestServiceRejectsShortLinkRedirectToUntrustedHost(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": []string{"http://127.0.0.1/private"}},
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    req,
		}, nil
	})}
	service := NewService(Options{Client: client})

	if _, _, err := service.CleanURL(context.Background(), "https://b23.tv/unsafe"); err == nil || !strings.Contains(err.Error(), "unsupported host") {
		t.Fatalf("CleanURL error = %v, want unsupported host", err)
	}
}

func TestServiceExtractsTrackedURLFromQQCardJSON(t *testing.T) {
	service := NewService(Options{})
	card := `{"app":"com.tencent.structmsg","meta":{"detail_1":{"preview":"https://i0.hdslb.com/cover.jpg","qqdocurl":"https://www.bilibili.com/video/BV1card/?share_source=qq&amp;vd_source=track&quot;"}}}`

	cleaned, err := service.CleanMessage(context.Background(), "", []bot.MessageSegment{{
		Type: "json",
		Data: map[string]any{"data": card},
	}})

	if err != nil {
		t.Fatalf("CleanMessage returned error: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "https://www.bilibili.com/video/BV1card/" {
		t.Fatalf("CleanMessage = %v", cleaned)
	}
}

func TestServiceExtractsURLFromMiniAppAndDeduplicates(t *testing.T) {
	service := NewService(Options{})
	tracked := "https://www.bilibili.com/video/BV1mini/?vd_source=track"

	cleaned, err := service.CleanMessage(context.Background(), tracked, []bot.MessageSegment{{
		Type: "miniapp",
		Data: map[string]any{"data": map[string]any{"jumpUrl": tracked}},
	}})

	if err != nil {
		t.Fatalf("CleanMessage returned error: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "https://www.bilibili.com/video/BV1mini/" {
		t.Fatalf("CleanMessage = %v", cleaned)
	}
}
