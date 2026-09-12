// Package cliclient is a thin HTTP client for engram's REST API. It backs
// the `engram` command-line subcommands, so a human can manage, inspect, or
// debug notes from a terminal without hand-writing curl calls or opening
// the web UI. It talks to a running engram server; it holds no business
// logic of its own — every operation maps to one REST call, keeping the
// CLI, REST, and MCP interfaces in lockstep with internal/core.
package cliclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/davasorus/engram/internal/core"
)

// Client talks to one engram server's REST API.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// New returns a Client for the given server base URL (e.g.
// "http://localhost:8088"). A trailing slash is trimmed.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

// apiError is the shape of an error response from internal/rest.
type apiError struct {
	Error string `json:"error"`
}

// do sends a request and decodes a JSON response into out (if non-nil). A
// non-2xx status is turned into a Go error carrying the server's message.
func (c *Client) do(ctx context.Context, method, path string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("request to %s: %w", c.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 300 {
		var ae apiError
		if json.Unmarshal(raw, &ae) == nil && ae.Error != "" {
			return fmt.Errorf("%s: %s", resp.Status, ae.Error)
		}
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// Health calls GET /api/health.
func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodGet, "/api/health", nil, &out)
	return out, err
}

// Search calls GET /api/search.
func (c *Client) Search(ctx context.Context, project, query string, limit int, kind string) ([]core.SearchHit, error) {
	q := url.Values{}
	q.Set("q", query)
	if project != "" {
		q.Set("project", project)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if kind != "" {
		q.Set("kind", kind)
	}
	var out []core.SearchHit
	err := c.do(ctx, http.MethodGet, "/api/search?"+q.Encode(), nil, &out)
	return out, err
}

// List calls GET /api/notes.
func (c *Client) List(ctx context.Context, project string, limit, offset int) ([]core.Note, error) {
	q := url.Values{}
	if project != "" {
		q.Set("project", project)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	var out []core.Note
	err := c.do(ctx, http.MethodGet, "/api/notes?"+q.Encode(), nil, &out)
	return out, err
}

// Get calls GET /api/notes/{id}.
func (c *Client) Get(ctx context.Context, id string) (*core.Note, error) {
	var out core.Note
	err := c.do(ctx, http.MethodGet, "/api/notes/"+url.PathEscape(id), nil, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// writeBody mirrors internal/rest's request shape.
type writeBody struct {
	ID      string   `json:"id,omitempty"`
	Project string   `json:"project,omitempty"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags,omitempty"`
}

// Write calls POST /api/notes.
func (c *Client) Write(ctx context.Context, in core.WriteInput) (*core.Note, error) {
	payload, err := json.Marshal(writeBody{ID: in.ID, Project: in.Project, Title: in.Title, Body: in.Body, Tags: in.Tags})
	if err != nil {
		return nil, err
	}
	var out core.Note
	err = c.do(ctx, http.MethodPost, "/api/notes", bytes.NewReader(payload), &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// patchBody mirrors internal/rest's request shape.
type patchBody struct {
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
}

// Patch calls PATCH /api/notes/{id}.
func (c *Client) Patch(ctx context.Context, id, oldStr, newStr string) (*core.Note, error) {
	payload, err := json.Marshal(patchBody{OldStr: oldStr, NewStr: newStr})
	if err != nil {
		return nil, err
	}
	var out core.Note
	err = c.do(ctx, http.MethodPatch, "/api/notes/"+url.PathEscape(id), bytes.NewReader(payload), &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete calls DELETE /api/notes/{id}.
func (c *Client) Delete(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/notes/"+url.PathEscape(id), nil, nil)
}

// Links calls GET /api/notes/{id}/links (backlinks).
func (c *Client) Links(ctx context.Context, id string) ([]core.Backlink, error) {
	var out []core.Backlink
	err := c.do(ctx, http.MethodGet, "/api/notes/"+url.PathEscape(id)+"/links", nil, &out)
	return out, err
}

// Suggestions calls GET /api/notes/{id}/suggestions.
func (c *Client) Suggestions(ctx context.Context, id string, limit int) ([]core.SearchHit, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	var out []core.SearchHit
	err := c.do(ctx, http.MethodGet, "/api/notes/"+url.PathEscape(id)+"/suggestions?"+q.Encode(), nil, &out)
	return out, err
}

// summaryResponse mirrors internal/rest's response shape.
type summaryResponse struct {
	Summary string `json:"summary"`
}

// Summary calls GET /api/notes/{id}/summary.
func (c *Client) Summary(ctx context.Context, id string) (string, error) {
	var out summaryResponse
	err := c.do(ctx, http.MethodGet, "/api/notes/"+url.PathEscape(id)+"/summary", nil, &out)
	return out.Summary, err
}

// reembedResponse mirrors internal/rest's response shape.
type reembedResponse struct {
	Reembedded int `json:"reembedded"`
}

// Reembed calls POST /api/reembed. When full is true, it rebuilds every
// note's vector (?full=1); otherwise it only backfills notes missing one.
func (c *Client) Reembed(ctx context.Context, full bool) (int, error) {
	path := "/api/reembed"
	if full {
		path += "?full=1"
	}
	var out reembedResponse
	err := c.do(ctx, http.MethodPost, path, nil, &out)
	return out.Reembedded, err
}

// Stats calls GET /api/stats.
func (c *Client) Stats(ctx context.Context) (core.Stats, error) {
	var out core.Stats
	err := c.do(ctx, http.MethodGet, "/api/stats", nil, &out)
	return out, err
}
