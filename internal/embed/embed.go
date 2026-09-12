// Package embed implements core.Embedder against an OpenAI-compatible
// /v1/embeddings endpoint — by default the user's local LM Studio, so the whole
// memory system stays local and offline-capable.
package embed

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

// Client calls one or more OpenAI-compatible /v1/embeddings endpoints. When
// more than one base URL is configured, Embed tries them in order and falls
// back to the next one on failure, so a secondary embedding provider can
// stand in when the primary is unreachable (for example, LM Studio
// restarting). All configured endpoints must serve the same model.
type Client struct {
	baseURLs []string
	model    string
	http     *http.Client

	mu       sync.Mutex
	lastGood int // index into baseURLs most recently used with success
}

// New builds an embeddings client for a single endpoint. baseURL is e.g.
// "http://172.22.208.1:1235" (LM Studio); the /v1/embeddings path is appended.
func New(baseURL, model string) *Client {
	return NewWithFallback([]string{baseURL}, model)
}

// NewWithFallback builds an embeddings client backed by an ordered list of
// base URLs. Embed tries the most recently successful endpoint first, then
// the rest in the given order, and returns the first successful result.
// Empty entries are ignored, so callers can pass a raw split of a
// comma-separated config value.
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
		http:     &http.Client{Timeout: 60 * time.Second},
	}
}

func (c *Client) Model() string { return c.model }

type embedReq struct {
	Model string `json:"model"`
	Input string `json:"input"`
}

type embedResp struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Embed returns the embedding for a single piece of text. It tries the
// configured base URLs in order, starting with the one that most recently
// succeeded, and falls back to the next one when a call fails. It reports
// the last error only if every endpoint fails, so a transient outage on the
// primary provider does not block writes or search as long as a fallback
// answers.
func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	if len(c.baseURLs) == 0 {
		return nil, fmt.Errorf("embeddings: no base URL configured")
	}

	c.mu.Lock()
	start := c.lastGood
	c.mu.Unlock()

	var errs []string
	for i := 0; i < len(c.baseURLs); i++ {
		idx := (start + i) % len(c.baseURLs)
		v, err := c.embedOne(ctx, c.baseURLs[idx], text)
		if err == nil {
			c.mu.Lock()
			c.lastGood = idx
			c.mu.Unlock()
			return v, nil
		}
		errs = append(errs, fmt.Sprintf("%s: %v", c.baseURLs[idx], err))
	}
	return nil, fmt.Errorf("embeddings: all endpoints failed: %s", strings.Join(errs, "; "))
}

func (c *Client) embedOne(ctx context.Context, baseURL, text string) ([]float32, error) {
	body, _ := json.Marshal(embedReq{Model: c.model, Input: text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("embeddings HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var er embedResp
	if err := json.Unmarshal(raw, &er); err != nil {
		return nil, fmt.Errorf("decode embeddings: %w", err)
	}
	if er.Error != nil {
		return nil, fmt.Errorf("embeddings error: %s", er.Error.Message)
	}
	if len(er.Data) == 0 || len(er.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embeddings: empty response")
	}
	return er.Data[0].Embedding, nil
}
