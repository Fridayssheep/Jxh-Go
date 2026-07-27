package quote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type MessageSegment struct {
	Type string `json:"type"`
	Kind string `json:"kind,omitempty"`
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
	ID   string `json:"id,omitempty"`
}

type Message struct {
	UserID       int64            `json:"user_id"`
	UserNickname string           `json:"user_nickname"`
	Avatar       string           `json:"avatar,omitempty"`
	Message      []MessageSegment `json:"message"`
}

type Payload []Message

type Client struct {
	baseURL string
	client  *http.Client
	observe func(Observation)
}

type Outcome string

const (
	OutcomeGIFSuccess  Outcome = "gif_success"
	OutcomePNGFallback Outcome = "png_fallback"
	OutcomeFailure     Outcome = "failure"
)

type Observation struct {
	Outcome    Outcome
	OccurredAt time.Time
	Latency    time.Duration
}

func NewClient(baseURL string, client *http.Client, observers ...func(Observation)) *Client {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	result := &Client{baseURL: strings.TrimRight(baseURL, "/"), client: client}
	if len(observers) > 0 {
		result.observe = observers[0]
	}
	return result
}

func (c *Client) Generate(ctx context.Context, payload Payload) (string, error) {
	startedAt := time.Now()
	data, err := json.Marshal(payload)
	if err != nil {
		c.record(OutcomeFailure, startedAt)
		return "", fmt.Errorf("marshal quote payload: %w", err)
	}
	image, gifErr := c.generate(ctx, data, "/gif/base64/")
	if gifErr == nil {
		c.record(OutcomeGIFSuccess, startedAt)
		return image, nil
	}
	image, pngErr := c.generate(ctx, data, "/png/base64/")
	if pngErr != nil {
		c.record(OutcomeFailure, startedAt)
		return "", errors.Join(fmt.Errorf("generate GIF quote: %w", gifErr), fmt.Errorf("generate PNG fallback: %w", pngErr))
	}
	c.record(OutcomePNGFallback, startedAt)
	return image, nil
}

func (c *Client) record(outcome Outcome, startedAt time.Time) {
	if c.observe == nil {
		return
	}
	completedAt := time.Now()
	latency := completedAt.Sub(startedAt)
	if latency < 0 {
		latency = 0
	}
	c.observe(Observation{Outcome: outcome, OccurredAt: completedAt.UTC(), Latency: latency})
}

func (c *Client) generate(ctx context.Context, payload []byte, path string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("create quote request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request quote image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The upstream body may contain request data or implementation details.
		// Drain only a bounded prefix for connection reuse and keep it out of errors.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
		return "", fmt.Errorf("quote server returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read quote response: %w", err)
	}
	return string(body), nil
}
