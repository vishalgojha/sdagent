// Package openrouter provides an OpenAI-compatible OpenRouter chat client.
package openrouter

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/vishalgojha/sdsheetal/internal/config"
)

type Client struct {
	key   string
	model string
	base  string
	http  *http.Client
}

func New(cfg *config.Config) *Client {
	return &Client{key: cfg.OpenRouterAPIKey, model: cfg.OpenRouterModel, base: strings.TrimRight(cfg.OpenRouterBaseURL, "/"), http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Enabled() bool { return strings.TrimSpace(c.key) != "" }

func (c *Client) Chat(ctx context.Context, system, user string) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("OpenRouter not configured")
	}
	body, err := json.Marshal(map[string]any{
		"model":       c.model,
		"messages":    []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": user}},
		"temperature": 0.7,
		"max_tokens":  350,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("HTTP-Referer", "https://sd.vishalojha.me")
	req.Header.Set("X-Title", "Agent V")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("OpenRouter %s: %s", resp.Status, truncate(string(raw), 220))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", err
	}
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("OpenRouter returned no choices")
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
