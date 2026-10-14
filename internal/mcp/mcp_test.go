package mcp_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davasorus/engram/internal/core"
	emcp "github.com/davasorus/engram/internal/mcp"
)

// --- minimal fakes, just enough to construct a core.Engine -----------------

type fakeStore struct{ notes map[string]core.Note }

func newFakeStore() *fakeStore { return &fakeStore{notes: map[string]core.Note{}} }

func (s *fakeStore) Upsert(_ context.Context, n core.Note) error {
	s.notes[n.ID] = n
	return nil
}
func (s *fakeStore) Get(_ context.Context, id string) (*core.Note, error) {
	if n, ok := s.notes[id]; ok {
		return &n, nil
	}
	return nil, nil
}
func (s *fakeStore) Delete(_ context.Context, id string) error {
	delete(s.notes, id)
	return nil
}
func (s *fakeStore) List(_ context.Context, _ string, _, _ int) ([]core.Note, error) { return nil, nil }
func (s *fakeStore) ListAfter(_ context.Context, _ string, _ int) ([]core.Note, error) {
	return nil, nil
}
func (s *fakeStore) Count(_ context.Context) (int, error)                 { return len(s.notes), nil }
func (s *fakeStore) MissingVectorIDs(_ context.Context) ([]string, error) { return nil, nil }
func (s *fakeStore) Stats(_ context.Context) (core.Stats, error) {
	return core.Stats{TotalNotes: len(s.notes)}, nil
}
func (s *fakeStore) SearchSemantic(_ context.Context, _ string, _ []float32, _ int) ([]core.SearchHit, error) {
	return nil, nil
}
func (s *fakeStore) KeywordSearch(_ context.Context, _ string, _ string, _ int) ([]core.Note, error) {
	return nil, nil
}
func (s *fakeStore) Backlinks(_ context.Context, _ string, _ string) ([]core.Backlink, error) {
	return nil, nil
}
func (s *fakeStore) Close() error { return nil }

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string                                    { return "fake" }
func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{0.1}, nil }

// listToolNames connects a real MCP client to the adapter's server over an
// in-memory transport and asks (over the actual protocol, not by peeking at
// internals) which tools it advertises. This is the only way to verify the
// allowlist end-to-end: the SDK's tool registry is private to its Server
// type, so there is no exported introspection API — driving a real
// tools/list exchange is the legitimate black-box test.
func listToolNames(t *testing.T, a *emcp.Adapter) []string {
	t.Helper()
	ctx := context.Background()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var names []string
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	return names
}

func TestNew_EmptyAllowlistExposesAllTools(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	got := listToolNames(t, a)
	want := []string{"mem_delete", "mem_links", "mem_list", "mem_patch", "mem_read", "mem_search", "mem_stats", "mem_suggest_links", "mem_summarize", "mem_write"}
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("got %d tools %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool[%d] = %q, want %q (full: got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

// TestNew_AllowlistRestrictsToolSurface is the regression target: the
// deploy default (compose/.env.example, kube ConfigMap) sets
// ENGRAM_MCP_TOOLS=mem_search,mem_read,mem_write specifically so smaller
// local models see a minimal tool surface. If the allowlist filter ever
// broke (e.g. registered everything regardless of the list), the agent
// would silently get all seven tools instead of three, with no visible
// error — this is exactly the kind of drift that goes unnoticed without a
// test pinning the exact expected set.
func TestNew_AllowlistRestrictsToolSurface(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, []string{"mem_search", "mem_read", "mem_write"})

	got := listToolNames(t, a)
	want := []string{"mem_read", "mem_search", "mem_write"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool[%d] = %q, want %q (full: got=%v want=%v)", i, got[i], want[i], got, want)
		}
	}
}

// TestNew_AllowlistIsCaseInsensitiveAndTrims covers the actual parsing of
// -mcp-tools / ENGRAM_MCP_TOOLS (a comma-split string from an env var or
// flag, so stray casing or whitespace from a human-edited .env file is a
// realistic input, e.g. "mem_search, MEM_READ , mem_write").
func TestNew_AllowlistIsCaseInsensitiveAndTrims(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, []string{" mem_search", "MEM_READ ", "  mem_write  ", ""})

	got := listToolNames(t, a)
	want := []string{"mem_read", "mem_search", "mem_write"}
	if len(got) != len(want) {
		t.Fatalf("got %d tools %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tool[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNew_UnknownAllowlistEntryExposesNothingExtra(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, []string{"mem_search", "not_a_real_tool"})

	got := listToolNames(t, a)
	want := []string{"mem_search"}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestMemWrite_RoundTrip is a light smoke test that a registered tool
// actually calls through to the engine and returns something — catches a
// handler wiring mistake (e.g. calling the wrong Engine method) that the
// allowlist tests above would not catch, since they only check tool NAMES.
func TestMemWrite_RoundTrip(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_write",
		Arguments: map[string]any{"title": "Test Note", "body": "hello from mem_write"},
	})
	if err != nil {
		t.Fatalf("call mem_write: %v", err)
	}
	if res.IsError {
		t.Fatalf("mem_write returned an error result: %+v", res.Content)
	}
}

// --- Phase 5: HTTPHandler (5.8) ---

// TestHTTPHandler_ReturnsHandler confirms HTTPHandler returns a non-nil
// http.Handler that can be used with httptest.Server.
func TestHTTPHandler_ReturnsHandler(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	h := a.HTTPHandler()
	if h == nil {
		t.Fatal("HTTPHandler returned nil")
	}

	// Verify the handler does not panic on a minimal request.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec, req)
	// We do not assert status code here — streamable HTTP may return 405
	// for GET. We only care that it does not panic or hang.
}

// TestHTTPHandler_PostRequest verifies the handler accepts POST requests
// without panicking. The exact response depends on MCP transport negotiation.
func TestHTTPHandler_PostRequest(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	a.HTTPHandler().ServeHTTP(rec, req)
	// Again: no status assertion — transport negotiation may vary.
	// Just verify no panic.
}

// --- Phase 5: error-returning tool handlers (5.9) ---

// TestMemRead_MissingNote returns an error result when reading a note
// that does not exist.
func TestMemRead_MissingNote(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_read",
		Arguments: map[string]any{"id": "nonexistent"},
	})
	if err != nil {
		t.Fatalf("call mem_read: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for missing note, got success")
	}
}

// TestMemRead_RoundTrip writes a note then reads it back to cover the
// mem_read success path in registerTools (which the allowlist tests
// only verify by name, not by execution).
func TestMemRead_RoundTrip(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	// Write a note first.
	_, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_write",
		Arguments: map[string]any{"id": "read-test", "title": "Read Test", "body": "test body"},
	})
	if err != nil {
		t.Fatalf("call mem_write: %v", err)
	}

	// Read it back.
	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_read",
		Arguments: map[string]any{"id": "read-test"},
	})
	if err != nil {
		t.Fatalf("call mem_read: %v", err)
	}
	if res.IsError {
		t.Fatalf("mem_read returned an error result: %+v", res.Content)
	}
}

// TestMemList_RoundTrip exercises the mem_list success path in
// registerTools, covering a different tool handler than the mem_write
// round-trip test above.
func TestMemList_RoundTrip(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	// Write a note so list returns something.
	_, err = clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_write",
		Arguments: map[string]any{"id": "list-test", "title": "List Test", "body": "test"},
	})
	if err != nil {
		t.Fatalf("call mem_write: %v", err)
	}

	// List notes.
	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "mem_list",
	})
	if err != nil {
		t.Fatalf("call mem_list: %v", err)
	}
	if res.IsError {
		t.Fatalf("mem_list returned an error result: %+v", res.Content)
	}
}

// TestMemStats_RoundTrip exercises the mem_stats success path in
// registerTools.
func TestMemStats_RoundTrip(t *testing.T) {
	eng := core.NewEngine(newFakeStore(), fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "mem_stats",
	})
	if err != nil {
		t.Fatalf("call mem_stats: %v", err)
	}
	if res.IsError {
		t.Fatalf("mem_stats returned an error result: %+v", res.Content)
	}
}

// TestMemDelete_StoreError returns an error result when the store fails.
func TestMemDelete_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("Delete")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_delete",
		Arguments: map[string]any{"id": "foo"},
	})
	if err != nil {
		t.Fatalf("call mem_delete: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemSearch_StoreError returns an error result when Search fails.
func TestMemSearch_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("SearchSemantic")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_search",
		Arguments: map[string]any{"query": "test"},
	})
	if err != nil {
		t.Fatalf("call mem_search: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemList_StoreError returns an error result when List fails.
func TestMemList_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("List")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "mem_list",
	})
	if err != nil {
		t.Fatalf("call mem_list: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemStats_StoreError returns an error result when Stats fails.
func TestMemStats_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("Stats")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name: "mem_stats",
	})
	if err != nil {
		t.Fatalf("call mem_stats: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemLinks_StoreError returns an error result when Backlinks fails.
func TestMemLinks_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("Backlinks")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_links",
		Arguments: map[string]any{"id": "foo"},
	})
	if err != nil {
		t.Fatalf("call mem_links: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemSuggestLinks_StoreError returns an error result when SuggestLinks fails.
func TestMemSuggestLinks_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("SearchSemantic")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_suggest_links",
		Arguments: map[string]any{"id": "foo"},
	})
	if err != nil {
		t.Fatalf("call mem_suggest_links: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemPatch_StoreError returns an error result when Patch fails.
// Patch calls Get then Write (which calls Upsert). We test the Upsert
// failure path via TestMemWrite_StoreError since the store interface
// does not distinguish between Write and Upsert.

// TestMemWrite_StoreError returns an error result when Write fails.
func TestMemWrite_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("Upsert")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_write",
		Arguments: map[string]any{"title": "FailNote", "body": "body"},
	})
	if err != nil {
		t.Fatalf("call mem_write: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// TestMemSummarize_StoreError returns an error result when Summarize's Get fails.
func TestMemSummarize_StoreError(t *testing.T) {
	st := newFakeStoreWithFail("Get")
	eng := core.NewEngine(st, fakeEmbedder{})
	a := emcp.New(eng, nil)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.1"}, nil)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()

	serverSession, err := a.Server().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = serverSession.Close() }()

	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = clientSession.Close() }()

	res, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "mem_summarize",
		Arguments: map[string]any{"id": "foo"},
	})
	if err != nil {
		t.Fatalf("call mem_summarize: %v", err)
	}
	if !res.IsError {
		t.Fatal("expected error result for store failure, got success")
	}
}

// --- helper: fake store that can fail specific methods ---

type fakeStoreWithFail struct {
	fails map[string]bool
	notes map[string]core.Note
}

func newFakeStoreWithFail(failMethod string) *fakeStoreWithFail {
	s := &fakeStoreWithFail{
		fails: map[string]bool{},
		notes: map[string]core.Note{},
	}
	if failMethod != "" {
		s.fails[failMethod] = true
	}
	return s
}

func (s *fakeStoreWithFail) Upsert(_ context.Context, n core.Note) error {
	if s.fails["Upsert"] {
		return fmt.Errorf("store error: Upsert")
	}
	s.notes[n.ID] = n
	return nil
}
func (s *fakeStoreWithFail) Get(_ context.Context, id string) (*core.Note, error) {
	if s.fails["Get"] {
		return nil, fmt.Errorf("store error: Get")
	}
	if n, ok := s.notes[id]; ok {
		return &n, nil
	}
	return nil, nil
}
func (s *fakeStoreWithFail) Delete(_ context.Context, id string) error {
	if s.fails["Delete"] {
		return fmt.Errorf("store error: Delete")
	}
	delete(s.notes, id)
	return nil
}
func (s *fakeStoreWithFail) List(_ context.Context, _ string, _, _ int) ([]core.Note, error) {
	if s.fails["List"] {
		return nil, fmt.Errorf("store error: List")
	}
	return nil, nil
}
func (s *fakeStoreWithFail) ListAfter(_ context.Context, _ string, _ int) ([]core.Note, error) {
	if s.fails["List"] {
		return nil, fmt.Errorf("store error: List")
	}
	return nil, nil
}
func (s *fakeStoreWithFail) Count(_ context.Context) (int, error) { return len(s.notes), nil }
func (s *fakeStoreWithFail) MissingVectorIDs(_ context.Context) ([]string, error) {
	if s.fails["MissingVectorIDs"] {
		return nil, fmt.Errorf("store error: MissingVectorIDs")
	}
	return nil, nil
}
func (s *fakeStoreWithFail) Stats(_ context.Context) (core.Stats, error) {
	if s.fails["Stats"] {
		return core.Stats{}, fmt.Errorf("store error: Stats")
	}
	return core.Stats{TotalNotes: len(s.notes)}, nil
}
func (s *fakeStoreWithFail) SearchSemantic(_ context.Context, _ string, _ []float32, _ int) ([]core.SearchHit, error) {
	if s.fails["SearchSemantic"] {
		return nil, fmt.Errorf("store error: SearchSemantic")
	}
	return nil, nil
}
func (s *fakeStoreWithFail) KeywordSearch(_ context.Context, _ string, _ string, _ int) ([]core.Note, error) {
	return nil, nil
}
func (s *fakeStoreWithFail) Backlinks(_ context.Context, _ string, _ string) ([]core.Backlink, error) {
	if s.fails["Backlinks"] {
		return nil, fmt.Errorf("store error: Backlinks")
	}
	return nil, nil
}
func (s *fakeStoreWithFail) Close() error { return nil }
