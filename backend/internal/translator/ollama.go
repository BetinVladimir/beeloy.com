// Package translator translates articles and UI dictionaries with a local Ollama model. It is run by an operator
// (cmd/translator); it only talks to S3 and Ollama, never to PostgreSQL.
package translator

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

// Chatter is the part of the Ollama client the translator needs (replaced by stubs in tests).
type Chatter interface {
	Chat(ctx context.Context, system, user string, jsonMode bool) (string, error)
}

type Client struct {
	BaseURL string
	Model   string
	HTTP    *http.Client
	Delays  []time.Duration // retry delays for network errors / 5xx (default 2s, 5s)
}

func NewClient(baseURL, model string, timeout time.Duration) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), Model: model, HTTP: &http.Client{Timeout: timeout}}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model     string         `json:"model"`
	Messages  []chatMessage  `json:"messages"`
	Stream    bool           `json:"stream"`
	Format    string         `json:"format,omitempty"`
	Options   map[string]any `json:"options"`
	KeepAlive string         `json:"keep_alive"`
}

// Chat sends one request to POST /api/chat and returns message.content.
func (c *Client) Chat(ctx context.Context, system, user string, jsonMode bool) (string, error) {
	req := chatReq{Model: c.Model, Stream: false, Options: map[string]any{"temperature": 0.2, "num_ctx": 8192}, KeepAlive: "10m",
		Messages: []chatMessage{{"system", system}, {"user", user}}}
	if jsonMode {
		req.Format = "json"
	}
	body, _ := json.Marshal(req)
	delays := c.Delays
	if delays == nil {
		delays = []time.Duration{2 * time.Second, 5 * time.Second}
	}
	var lastErr error
	for attempt := 0; attempt <= len(delays); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delays[attempt-1]):
			}
		}
		hr, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(body))
		hr.Header.Set("Content-Type", "application/json")
		resp, err := c.HTTP.Do(hr)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		resp.Body.Close()
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("ollama: status %d", resp.StatusCode)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("ollama: status %d: %s", resp.StatusCode, truncate(string(raw), 200))
		}
		var out struct {
			Message chatMessage `json:"message"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", fmt.Errorf("ollama: bad response: %w", err)
		}
		if strings.TrimSpace(out.Message.Content) == "" {
			return "", errors.New("ollama: empty answer")
		}
		return out.Message.Content, nil
	}
	return "", fmt.Errorf("ollama: %w", lastErr)
}

// Ping checks that Ollama answers and that the configured model exists.
func (c *Client) Ping(ctx context.Context) error {
	hr, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/tags", nil)
	resp, err := c.HTTP.Do(hr)
	if err != nil {
		return fmt.Errorf("ollama: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama: status %d", resp.StatusCode)
	}
	var tags struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tags); err != nil {
		return fmt.Errorf("ollama: %w", err)
	}
	for _, m := range tags.Models {
		if m.Name == c.Model || strings.TrimSuffix(m.Name, ":latest") == strings.TrimSuffix(c.Model, ":latest") {
			return nil
		}
	}
	return fmt.Errorf("ollama: model %q is not installed", c.Model)
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
