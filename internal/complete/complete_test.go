package complete

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestComplete_RequestShapeAndResponse(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a summary"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "some-model")
	got, err := c.Complete(context.Background(), "summarize this")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "a summary" {
		t.Fatalf("got %q, want %q", got, "a summary")
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q, want /v1/chat/completions", gotPath)
	}
	if gotBody["model"] != "some-model" {
		t.Fatalf("model = %v", gotBody["model"])
	}
}

func TestComplete_HTTPErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	if _, err := c.Complete(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want it to mention 502", err)
	}
}

func TestComplete_EmptyChoicesIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	if _, err := c.Complete(context.Background(), "x"); err == nil {
		t.Fatal("expected an error for empty choices")
	}
}

func TestComplete_APIErrorField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"error":{"message":"model not loaded"}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "m")
	if _, err := c.Complete(context.Background(), "x"); err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Fatalf("err = %v, want it to surface the API error message", err)
	}
}

func TestFallback_SecondEndpointUsedWhenFirstFails(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer good.Close()

	c := NewWithFallback([]string{bad.URL, good.URL}, "m")
	got, err := c.Complete(context.Background(), "x")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got != "ok" {
		t.Fatalf("got %q", got)
	}
}

func TestConfigured(t *testing.T) {
	if (&Client{}).Configured() {
		t.Fatal("zero-value Client should not report Configured")
	}
	if NewWithFallback(nil, "m").Configured() {
		t.Fatal("no-endpoint client should not report Configured")
	}
	if !New("http://example.com", "m").Configured() {
		t.Fatal("single-endpoint client should report Configured")
	}
	var nilClient *Client
	if nilClient.Configured() {
		t.Fatal("nil *Client should not report Configured")
	}
}
