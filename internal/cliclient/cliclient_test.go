package cliclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/davasorus/engram/internal/core"
)

// newTestClient returns a Client pointing at ts, and closes the server on test exit.
func newTestClient(t *testing.T, ts *httptest.Server) *Client {
	t.Helper()
	c := New(ts.URL)
	t.Cleanup(func() { ts.Close() })
	return c
}

func TestNew_TrimsTrailingSlash(t *testing.T) {
	c := New("http://example.com/")
	if c.BaseURL != "http://example.com" {
		t.Errorf("BaseURL = %q; want http://example.com", c.BaseURL)
	}
}

func TestNew_NoTrailingSlash(t *testing.T) {
	c := New("http://example.com")
	if c.BaseURL != "http://example.com" {
		t.Errorf("BaseURL = %q; want http://example.com", c.BaseURL)
	}
}

func TestNew_DefaultTimeout(t *testing.T) {
	c := New("http://example.com")
	if c.HTTP.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v; want 30s", c.HTTP.Timeout)
	}
}

func TestDo_ConnectionRefused(t *testing.T) {
	c := &Client{
		BaseURL: "http://127.0.0.1:1", // unlikely to be listening
		HTTP:    &http.Client{Timeout: 100 * time.Millisecond},
	}
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error on connection refused")
	}
	if !strings.Contains(err.Error(), "request to") {
		t.Errorf("error = %v; want 'request to'", err)
	}
}

func TestDo_ContextTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer ts.Close()
	c := New(ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Health(ctx)
	if err == nil {
		t.Fatal("expected context timeout error")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error = %v; want 'context deadline exceeded'", err)
	}
}

func TestDo_ContextCancellation(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer ts.Close()
	c := New(ts.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err := c.Health(ctx)
	if err == nil {
		t.Fatal("expected context cancelled error")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Errorf("error = %v; want 'canceled'", err)
	}
}

func TestDo_Non2xxJSONBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad request"})
		// ignore encode error in test
	}))
	defer ts.Close()
	c := New(ts.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error on non-2xx")
	}
	if !strings.Contains(err.Error(), "bad request") {
		t.Errorf("error = %v; want 'bad request'", err)
	}
}

func TestDo_Non2xxNoJSONBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("not found"))
	}))
	defer ts.Close()
	c := New(ts.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error on non-2xx")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error = %v; want '404'", err)
	}
}

func TestDo_2xxMalformedJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer ts.Close()
	c := New(ts.URL)
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected error on malformed JSON")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("error = %v; want 'decode response'", err)
	}
}

func TestDo_NoOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	c := New(ts.URL)
	// Delete passes out=nil; it should not error on empty body.
	err := c.do(context.Background(), http.MethodDelete, "/api/notes/id", nil, nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDo_SetsContentType(t *testing.T) {
	var gotContentType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer ts.Close()
	c := New(ts.URL)
	_, err := c.Write(context.Background(), core.WriteInput{ID: "x", Title: "t", Body: "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q; want application/json", gotContentType)
	}
}

func TestDo_URLPathEscape(t *testing.T) {
	var gotRequestURI string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer ts.Close()
	c := New(ts.URL)
	id := "note/with spaces&special"
	_, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	escaped := url.PathEscape(id)
	expected := "/api/notes/" + escaped
	if gotRequestURI != expected {
		t.Errorf("RequestURI = %q; want %q", gotRequestURI, expected)
	}
}

func TestHealth(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s; want GET", r.Method)
		}
		if r.URL.Path != "/api/health" {
			t.Errorf("path = %s; want /api/health", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	out, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["status"] != "ok" {
		t.Errorf("status = %v; want ok", out["status"])
	}
}

func TestSearch_AllParams(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("q") != "hello" {
			t.Errorf("q param = %q; want hello", q.Get("q"))
		}
		if q.Get("project") != "myproj" {
			t.Errorf("project param = %q; want myproj", q.Get("project"))
		}
		if q.Get("limit") != "10" {
			t.Errorf("limit param = %q; want 10", q.Get("limit"))
		}
		if q.Get("kind") != "semantic" {
			t.Errorf("kind param = %q; want semantic", q.Get("kind"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.SearchHit{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	hits, err := c.Search(context.Background(), "myproj", "hello", 10, "semantic")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits == nil {
		t.Error("hits is nil; want empty slice")
	}
}

func TestSearch_MinimalParams(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("project") != "" {
			t.Errorf("project param = %q; want empty", q.Get("project"))
		}
		if q.Get("limit") != "" {
			t.Errorf("limit param = %q; want empty", q.Get("limit"))
		}
		if q.Get("kind") != "" {
			t.Errorf("kind param = %q; want empty", q.Get("kind"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.SearchHit{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	hits, err := c.Search(context.Background(), "", "query", 0, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits == nil {
		t.Error("hits is nil; want empty slice")
	}
}

func TestList_AllParams(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("project") != "myproj" {
			t.Errorf("project param = %q; want myproj", q.Get("project"))
		}
		if q.Get("limit") != "20" {
			t.Errorf("limit param = %q; want 20", q.Get("limit"))
		}
		if q.Get("offset") != "5" {
			t.Errorf("offset param = %q; want 5", q.Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.Note{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	notes, err := c.List(context.Background(), "myproj", 20, 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notes == nil {
		t.Error("notes is nil; want empty slice")
	}
}

func TestList_MinimalParams(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("project") != "" {
			t.Errorf("project param = %q; want empty", q.Get("project"))
		}
		if q.Get("limit") != "" {
			t.Errorf("limit param = %q; want empty", q.Get("limit"))
		}
		if q.Get("offset") != "" {
			t.Errorf("offset param = %q; want empty", q.Get("offset"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.Note{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	notes, err := c.List(context.Background(), "", 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if notes == nil {
		t.Error("notes is nil; want empty slice")
	}
}

func TestGet_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/notes/my-id" {
			t.Errorf("path = %s; want /api/notes/my-id", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.Note{ID: "my-id", Title: "Test"})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	note, err := c.Get(context.Background(), "my-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if note == nil || note.ID != "my-id" {
		t.Errorf("ID = %q; want my-id", note.ID)
	}
}

func TestGet_NotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	note, err := c.Get(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error on 404")
	}
	if note != nil {
		t.Error("note should be nil on error")
	}
}

func TestWrite_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s; want POST", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.Note{ID: "new-id", Title: "Test"})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	note, err := c.Write(context.Background(), core.WriteInput{
		ID:      "new-id",
		Project: "proj",
		Title:   "Test",
		Body:    "body",
		Tags:    []string{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if note.ID != "new-id" {
		t.Errorf("ID = %q; want new-id", note.ID)
	}
}

func TestWrite_Minimal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.Note{ID: "x", Title: "T"})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	note, err := c.Write(context.Background(), core.WriteInput{ID: "x", Title: "T", Body: "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if note == nil {
		t.Fatal("note is nil; want non-nil")
	}
}

func TestPatch_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Errorf("method = %s; want PATCH", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.Note{ID: "patched-id", Title: "Updated"})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	note, err := c.Patch(context.Background(), "patched-id", "old", "new")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if note == nil {
		t.Fatal("note is nil; want non-nil")
	}
}

func TestPatch_URLPathEscape(t *testing.T) {
	var gotRequestURI string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotRequestURI = r.RequestURI
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	id := "note/with spaces&special"
	_, err := c.Patch(context.Background(), id, "a", "b")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	escaped := url.PathEscape(id)
	expected := "/api/notes/" + escaped
	if gotRequestURI != expected {
		t.Errorf("RequestURI = %q; want %q", gotRequestURI, expected)
	}
}

func TestDelete_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %s; want DELETE", r.Method)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	err := c.Delete(context.Background(), "del-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLinks_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/notes/link-id/links" {
			t.Errorf("path = %s; want /api/notes/link-id/links", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.Backlink{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	links, err := c.Links(context.Background(), "link-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if links == nil {
		t.Error("links is nil; want empty slice")
	}
}

func TestSuggestions_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "5" {
			t.Errorf("limit param = %q; want 5", q.Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.SearchHit{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	hits, err := c.Suggestions(context.Background(), "sug-id", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits == nil {
		t.Error("hits is nil; want empty slice")
	}
}

func TestSuggestions_NoLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("limit") != "" {
			t.Errorf("limit param = %q; want empty", q.Get("limit"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]core.SearchHit{})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	hits, err := c.Suggestions(context.Background(), "sug-id", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hits == nil {
		t.Error("hits is nil; want empty slice")
	}
}

func TestSummary_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(summaryResponse{Summary: "A short summary."})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	summary, err := c.Summary(context.Background(), "sum-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if summary != "A short summary." {
		t.Errorf("summary = %q; want 'A short summary.'", summary)
	}
}

func TestReembed_Full(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/reembed" || r.URL.RawQuery != "full=1" {
			t.Errorf("path/query = %s?%s; want /api/reembed?full=1", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reembedResponse{Reembedded: 42})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	count, err := c.Reembed(context.Background(), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 42 {
		t.Errorf("count = %d; want 42", count)
	}
}

func TestReembed_NotFull(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/reembed" {
			t.Errorf("path = %s; want /api/reembed", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reembedResponse{Reembedded: 0})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	count, err := c.Reembed(context.Background(), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if count != 0 {
		t.Errorf("count = %d; want 0", count)
	}
}

func TestStats_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(core.Stats{TotalNotes: 10, TotalProjects: 2, TotalLinks: 3, NotesMissingVector: 0, UpdatedLast7Days: 5})
	}))
	defer ts.Close()
	c := newTestClient(t, ts)
	stats, err := c.Stats(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.TotalNotes != 10 {
		t.Errorf("TotalNotes = %d; want 10", stats.TotalNotes)
	}
	if stats.TotalLinks != 3 {
		t.Errorf("TotalLinks = %d; want 3", stats.TotalLinks)
	}
}
