package knowledge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

type WPSClient struct {
	ShareURL string
	SID      string
	Timeout  time.Duration
	HTTP     *http.Client
}

func (c WPSClient) Download(ctx context.Context) ([]byte, error) {
	client := c.HTTP
	if client == nil {
		timeout := c.Timeout
		if timeout <= 0 {
			timeout = 120 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.ShareURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	if c.SID != "" {
		req.AddCookie(&http.Cookie{Name: "wps_sid", Value: c.SID})
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, wrapWPSError("share request", client.Timeout, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wps share request failed: %s", resp.Status)
	}
	var meta struct {
		DownloadURL string `json:"download_url"`
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &meta); err != nil || meta.DownloadURL == "" {
		if err := ensureXLSX(body); err != nil {
			return nil, err
		}
		return body, nil
	}
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, meta.DownloadURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err = client.Do(req)
	if err != nil {
		return nil, wrapWPSError("file download", client.Timeout, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("wps download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if err := ensureXLSX(data); err != nil {
		return nil, err
	}
	return data, nil
}

func wrapWPSError(stage string, timeout time.Duration, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		if timeout > 0 {
			return fmt.Errorf("wps %s timed out after %s; WPS may be slow or temporarily unavailable, retry later or increase wps.timeout_sec: %w", stage, timeout, err)
		}
		return fmt.Errorf("wps %s timed out; WPS may be slow or temporarily unavailable: %w", stage, err)
	}
	return err
}

func ensureXLSX(data []byte) error {
	if len(data) >= 4 && bytes.Equal(data[:4], []byte{'P', 'K', 0x03, 0x04}) {
		return nil
	}
	preview := string(bytes.TrimSpace(data))
	if len(preview) > 120 {
		preview = preview[:120]
	}
	return fmt.Errorf("wps download is not an xlsx file; share_url must be a WPS 导出文档链接 or direct xlsx URL, and protected documents need a valid wps_sid; normal 365.kdocs.cn/l share pages return HTML, response preview: %q", preview)
}
