package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/rest"
)

// memStore/fakeEmbedder mirror internal/rest's test doubles: a minimal
// in-memory core.Store and core.Embedder, just enough to run a real
// rest.API (and therefore a real CLI round-trip) without Postgres.
type memStore struct{ notes map[string]core.Note }

func newMem() *memStore { return &memStore{notes: map[string]core.Note{}} }

func (m *memStore) Upsert(_ context.Context, n core.Note) error { m.notes[n.ID] = n; return nil }
func (m *memStore) Get(_ context.Context, id string) (*core.Note, error) {
	n, ok := m.notes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}
func (m *memStore) Delete(_ context.Context, id string) error { delete(m.notes, id); return nil }
func (m *memStore) List(_ context.Context, _ string, limit, offset int) ([]core.Note, error) {
	var out []core.Note
	for _, n := range m.notes {
		out = append(out, n)
	}
	return out, nil
}
func (m *memStore) Count(_ context.Context) (int, error) { return len(m.notes), nil }
func (m *memStore) SearchSemantic(_ context.Context, _ string, _ []float32, _ int) ([]core.SearchHit, error) {
	var out []core.SearchHit
	for _, n := range m.notes {
		out = append(out, core.SearchHit{Note: n, Score: 1, Kind: "semantic"})
	}
	return out, nil
}
func (m *memStore) KeywordSearch(_ context.Context, _ string, q string, _ int) ([]core.Note, error) {
	var out []core.Note
	for _, n := range m.notes {
		if strings.Contains(strings.ToLower(n.Body), strings.ToLower(q)) {
			out = append(out, n)
		}
	}
	return out, nil
}
func (m *memStore) Backlinks(_ context.Context, _ string) ([]core.Backlink, error) { return nil, nil }
func (m *memStore) Close() error                                                   { return nil }

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string                                    { return "fake" }
func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }

// newTestServer starts an httptest server backed by a real rest.API over a
// fresh in-memory store, and returns its base URL plus the -server args a
// subcommand needs to reach it.
func newTestServer(t *testing.T) (url string, serverArgs []string) {
	t.Helper()
	eng := core.NewEngine(newMem(), fakeEmbedder{})
	srv := httptest.NewServer(rest.New(eng).Routes())
	t.Cleanup(srv.Close)
	return srv.URL, []string{"-server", srv.URL}
}

func runCLIForTest(t *testing.T, args []string) (stdout string, code int) {
	t.Helper()
	var buf bytes.Buffer
	code = runCLI(context.Background(), &buf, args)
	return buf.String(), code
}

func TestCLI_HealthAndWriteGetListDeleteRoundTrip(t *testing.T) {
	_, sa := newTestServer(t)

	out, code := runCLIForTest(t, append([]string{"health"}, sa...))
	if code != 0 {
		t.Fatalf("health: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, `"status": "ok"`) {
		t.Fatalf("health output missing status: %s", out)
	}

	out, code = runCLIForTest(t, append([]string{"write", "-title", "Test Note", "-tags", "a,b"}, sa...))
	if code != 0 {
		t.Fatalf("write: code=%d out=%s", code, out)
	}
	var n core.Note
	if err := json.Unmarshal([]byte(out), &n); err != nil {
		t.Fatalf("write output not valid JSON: %v\n%s", err, out)
	}
	if n.ID != "test-note" || n.Title != "Test Note" {
		t.Fatalf("unexpected note from write: %+v", n)
	}

	out, code = runCLIForTest(t, append(append([]string{"get"}, sa...), n.ID))
	if code != 0 {
		t.Fatalf("get: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, `"test-note"`) {
		t.Fatalf("get output missing id: %s", out)
	}

	out, code = runCLIForTest(t, append([]string{"list"}, sa...))
	if code != 0 {
		t.Fatalf("list: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, `"test-note"`) {
		t.Fatalf("list output missing note: %s", out)
	}

	out, code = runCLIForTest(t, append(append([]string{"delete"}, sa...), n.ID))
	if code != 0 {
		t.Fatalf("delete: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "deleted test-note") {
		t.Fatalf("delete output unexpected: %s", out)
	}
}

func TestCLI_Search(t *testing.T) {
	_, sa := newTestServer(t)
	if _, code := runCLIForTest(t, append([]string{"write", "-title", "Alpha"}, sa...)); code != 0 {
		t.Fatalf("setup write failed")
	}
	out, code := runCLIForTest(t, append(append([]string{"search", "-kind", "keyword"}, sa...), "alpha"))
	if code != 0 {
		t.Fatalf("search: code=%d out=%s", code, out)
	}
}

func TestCLI_PatchAndLinksAndSuggest(t *testing.T) {
	_, sa := newTestServer(t)
	if _, code := runCLIForTest(t, append([]string{"write", "-id", "n1", "-title", "N1"}, sa...)); code != 0 {
		t.Fatalf("setup write failed")
	}

	out, code := runCLIForTest(t, append(append([]string{"patch", "-old", "", "-new", "x"}, sa...), "n1"))
	if code != 2 {
		t.Fatalf("patch with empty -old should be a usage error, got code=%d out=%s", code, out)
	}

	out, code = runCLIForTest(t, append(append([]string{"links"}, sa...), "n1"))
	if code != 0 {
		t.Fatalf("links: code=%d out=%s", code, out)
	}

	out, code = runCLIForTest(t, append(append([]string{"suggest"}, sa...), "n1"))
	if code != 0 {
		t.Fatalf("suggest: code=%d out=%s", code, out)
	}
}

func TestCLI_SummaryNotConfigured(t *testing.T) {
	_, sa := newTestServer(t)
	if _, code := runCLIForTest(t, append([]string{"write", "-id", "n1", "-title", "N1"}, sa...)); code != 0 {
		t.Fatalf("setup write failed")
	}
	out, code := runCLIForTest(t, append(append([]string{"summary"}, sa...), "n1"))
	if code == 0 {
		t.Fatalf("expected non-zero exit for unconfigured summarizer, got 0: %s", out)
	}
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected an error message, got: %s", out)
	}
}

func TestCLI_UnknownCommand(t *testing.T) {
	out, code := runCLIForTest(t, []string{"bogus"})
	if code != 1 {
		t.Fatalf("expected exit 1 for unknown command, got %d", code)
	}
	if !strings.Contains(out, "unknown command") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestCLI_HelpAndNoArgs(t *testing.T) {
	out, code := runCLIForTest(t, []string{"help"})
	if code != 0 || !strings.Contains(out, "engram: memory service") {
		t.Fatalf("help: code=%d out=%s", code, out)
	}
	out, code = runCLIForTest(t, nil)
	if code != 0 || !strings.Contains(out, "engram: memory service") {
		t.Fatalf("no-args help: code=%d out=%s", code, out)
	}
}

func TestIsCLICommand(t *testing.T) {
	for _, name := range []string{"health", "search", "list", "get", "write", "patch", "delete", "links", "suggest", "summary", "reembed", "help"} {
		if !isCLICommand(name) {
			t.Fatalf("expected %q to be recognized as a CLI command", name)
		}
	}
	for _, name := range []string{"-dsn", "-addr", "", "notacommand"} {
		if isCLICommand(name) {
			t.Fatalf("did not expect %q to be recognized as a CLI command", name)
		}
	}
}
