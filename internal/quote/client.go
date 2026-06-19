package quote

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type MessageSegment struct {
	Type string `json:"type"`
	Kind string `json:"kind,omitempty"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
}

type Message struct {
	UserID       int64  `json:"user_id"`
	UserNickname string `json:"user_nickname"`
	Avatar       string `json:"avatar,omitempty"`
	Message      any    `json:"message"`
}

type Payload []Message

type Client struct {
	baseURL     string
	client      *http.Client
	avatarCache sync.Map
}

const maxAvatarBytes = 1 << 20

var qqAvatarURL = func(userID int64) string {
	return "https://q1.qlogo.cn/g?b=qq&nk=" + strconv.FormatInt(userID, 10) + "&s=100"
}

func NewClient(baseURL string, client *http.Client) *Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}

func (c *Client) Generate(ctx context.Context, payload Payload) (string, error) {
	payload = c.withDefaultAvatars(ctx, payload)
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/base64/", bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("quote server returned %s: %s", resp.Status, string(body))
	}
	return string(body), nil
}

func (c *Client) withDefaultAvatars(ctx context.Context, payload Payload) Payload {
	out := make(Payload, len(payload))
	copy(out, payload)
	for i, message := range out {
		if strings.TrimSpace(message.Avatar) != "" || message.UserID <= 0 {
			continue
		}
		if avatar := c.avatarDataURI(ctx, message.UserID); avatar != "" {
			out[i].Avatar = avatar
			continue
		}
		out[i].Avatar = qqAvatarURL(message.UserID)
	}
	return out
}

func (c *Client) avatarDataURI(ctx context.Context, userID int64) string {
	if cached, ok := c.avatarCache.Load(userID); ok {
		return cached.(string)
	}
	avatarCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	dataURI := c.fetchAvatarDataURI(avatarCtx, qqAvatarURL(userID))
	if dataURI != "" {
		c.avatarCache.Store(userID, dataURI)
	}
	return dataURI
}

func (c *Client) fetchAvatarDataURI(ctx context.Context, url string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAvatarBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxAvatarBytes {
		return ""
	}
	contentType := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return ""
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}
