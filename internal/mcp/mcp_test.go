package mcp_test

import (
	"context"
	"sort"
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
func (s *fakeStore) List(_ context.Context, _, _ int) ([]core.Note, error) { return nil, nil }
func (s *fakeStore) Count(_ context.Context) (int, error)                  { return len(s.notes), nil }
func (s *fakeStore) MissingVectorIDs(_ context.Context) ([]string, error)  { return nil, nil }
func (s *fakeStore) SearchSemantic(_ context.Context, _ []float32, _ int) ([]core.SearchHit, error) {
	return nil, nil
}
func (s *fakeStore) KeywordSearch(_ context.Context, _ string, _ int) ([]core.Note, error) {
	return nil, nil
}
func (s *fakeStore) Backlinks(_ context.Context, _ string) ([]core.Backlink, error) {
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
	want := []string{"mem_delete", "mem_links", "mem_list", "mem_patch", "mem_read", "mem_search", "mem_write"}
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
// allowlist tests above wouldn't, since they only check tool NAMES.
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
