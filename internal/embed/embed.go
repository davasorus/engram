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
	"time"
)

type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// New builds an embeddings client. baseURL is e.g.
// "http://172.22.208.1:1235" (LM Studio); the /v1/embeddings path is appended.
func New(baseURL, model string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http:    &http.Client{Timeout: 60 * time.Second},
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

func (c *Client) Embed(ctx context.Context, text string) ([]float32, error) {
	body, _ := json.Marshal(embedReq{Model: c.model, Input: text})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embeddings request: %w", err)
	}
	defer resp.Body.Close()
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
