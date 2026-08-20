package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEmbed_RequestShape is a regression test for the "/v1" duplication bug
// hit this session: someone set ENGRAM_EMBED_URL to LM Studio's displayed
// base ("http://host:1234/v1") instead of the bare host:port New() expects,
// and the client silently called ".../v1/v1/embeddings" — a real 404 that
// looked like a network problem rather than a URL-format mistake. This
// pins down the exact path New() builds and the request body shape.
func TestEmbed_RequestShape(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", r.Header.Get("Content-Type"))
		}
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "text-embedding-nomic-embed-text-v1.5")
	if _, err := c.Embed(context.Background(), "hello world"); err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if gotPath != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings", gotPath)
	}
	if gotBody["model"] != "text-embedding-nomic-embed-text-v1.5" {
		t.Fatalf("request model = %v, want the configured model", gotBody["model"])
	}
	if gotBody["input"] != "hello world" {
		t.Fatalf("request input = %v, want %q", gotBody["input"], "hello world")
	}
}

// TestEmbed_TrimsTrailingSlashFromBaseURL guards against a base URL like
// "http://localhost:1234/" (trailing slash) producing a doubled-up path
// ("//v1/embeddings") — a plausible copy-paste mistake, same family as the
// "/v1" duplication bug.
func TestEmbed_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "m")
	if _, err := c.Embed(context.Background(), "x"); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if gotPath != "/v1/embeddings" {
		t.Fatalf("request path = %q, want /v1/embeddings (no doubled slash)", gotPath)
	}
}

func TestEmbed_ReturnsVector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3,0.4]}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	got, err := c.Embed(context.Background(), "x")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	want := []float32{0.1, 0.2, 0.3, 0.4}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

// TestEmbed_HTTPErrorStatus is the case this session's "unreachable" and
// "invalid_json" errors from LM Studio fell into — a non-200 with a JSON
// error body. The error returned to the caller must include the status
// code and body so it's actionable in a log line (health probe, deployHint
// context), not just "something went wrong".
func TestEmbed_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid_json"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	_, err := c.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error for HTTP 400, got nil")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error %q does not mention the status code", err.Error())
	}
}

// TestEmbed_APIErrorField covers a 200 response that still carries an
// "error" field in the JSON body (some OpenAI-compatible servers do this
// instead of a non-200 status) — must not be silently treated as success.
func TestEmbed_APIErrorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	_, err := c.Embed(context.Background(), "x")
	if err == nil {
		t.Fatal("expected an error when the response body has an error field")
	}
	if !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("error %q does not surface the API's error message", err.Error())
	}
}

func TestEmbed_EmptyDataIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected an error for an empty data array")
	}
}

func TestEmbed_MalformedJSONIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json at all`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	if _, err := c.Embed(context.Background(), "x"); err == nil {
		t.Fatal("expected an error for a malformed JSON body")
	}
}

// TestEmbed_ContextCancellation ensures a cancelled context actually aborts
// the HTTP call rather than the timeout silently swallowing it — relevant
// since the real client has a 60s timeout; a caller-side cancel (e.g. a
// health probe with its own shorter deadline) must take effect.
func TestEmbed_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // hang until the client gives up
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := New(srv.URL, "m")
	if _, err := c.Embed(ctx, "x"); err == nil {
		t.Fatal("expected an error from an already-cancelled context")
	}
}

func TestModel_ReturnsConfiguredModel(t *testing.T) {
	c := New("http://example.com", "my-model")
	if got := c.Model(); got != "my-model" {
		t.Fatalf("Model() = %q, want %q", got, "my-model")
	}
}
