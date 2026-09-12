// Package complete implements an optional text-completion client against an
// OpenAI-compatible /v1/chat/completions endpoint. It is deliberately
// separate from internal/embed: a deployment can run engram with embeddings
// only (the default) and no completion endpoint at all, since summarization
// is an optional capability, not a required one.
package complete

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Client calls one or more OpenAI-compatible /v1/chat/completions endpoints.
// Like internal/embed.Client, it tries endpoints in order, starting with the
// most recently successful one, and falls back to the next on failure, so a
// secondary completion provider can stand in when the primary is
// unreachable.
type Client struct {
	baseURLs []string
	model    string
	http     *http.Client

	mu       sync.Mutex
	lastGood int
}

// New builds a completion client for a single endpoint.
func New(baseURL, model string) *Client {
	return NewWithFallback([]string{baseURL}, model)
}

// NewWithFallback builds a completion client backed by an ordered list of
// base URLs, with the same semantics as embed.NewWithFallback. Empty entries
// are ignored, so callers can pass a raw split of a comma-separated config
// value. A nil/empty list is valid; Complete then always reports an error,
// since a Client instance without any endpoint carries no capability.
func NewWithFallback(baseURLs []string, model string) *Client {
	cleaned := make([]string, 0, len(baseURLs))
	for _, u := range baseURLs {
		u = strings.TrimSpace(u)
		if u == "" {
			continue
		}
		cleaned = append(cleaned, strings.TrimRight(u, "/"))
	}
	return &Client{
		baseURLs: cleaned,
		model:    model,
		http:     &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *Client) Model() string { return c.model }

// Configured reports whether at least one endpoint is set up. Callers use
// this to decide whether to offer completion-backed features at all.
func (c *Client) Configured() bool { return c != nil && len(c.baseURLs) > 0 }

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatReq struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResp struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Complete sends a single user prompt and returns the model's reply text. It
// tries the configured base URLs in order, starting with the one that most
// recently succeeded, and falls back to the next one when a call fails. It
// reports the last error only if every endpoint fails.
func (c *Client) Complete(ctx context.Context, prompt string) (string, error) {
	if len(c.baseURLs) == 0 {
		return "", fmt.Errorf("completion: no base URL configured")
	}

	c.mu.Lock()
	start := c.lastGood
	c.mu.Unlock()

	var errs []string
	for i := 0; i < len(c.baseURLs); i++ {
		idx := (start + i) % len(c.baseURLs)
		v, err := c.completeOne(ctx, c.baseURLs[idx], prompt)
		if err == nil {
			c.mu.Lock()
			c.lastGood = idx
			c.mu.Unlock()
			return v, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", c.baseURLs[idx], err))
	}
	return "", fmt.Errorf("completion: all endpoints failed: %s", strings.Join(errs, "; "))
}

func (c *Client) completeOne(ctx context.Context, baseURL, prompt string) (string, error) {
	body, _ := json.Marshal(chatReq{
		Model:    c.model,
		Messages: []chatMessage{{Role: "user", Content: prompt}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("completion request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("completion HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var cr chatResp
	if err := json.Unmarshal(raw, &cr); err != nil {
		return "", fmt.Errorf("decode completion: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("completion error: %s", cr.Error.Message)
	}
	if len(cr.Choices) == 0 || strings.TrimSpace(cr.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("completion: empty response")
	}
	return strings.TrimSpace(cr.Choices[0].Message.Content), nil
}
