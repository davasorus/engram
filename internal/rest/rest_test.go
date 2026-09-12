package rest_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/rest"
)

// --- minimal in-memory Store + Embedder for handler tests ------------------

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
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (m *memStore) Count(_ context.Context) (int, error) { return len(m.notes), nil }
func (m *memStore) MissingVectorIDs(_ context.Context) ([]string, error) {
	var ids []string
	for id, n := range m.notes {
		if len(n.Vector) == 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (m *memStore) SearchSemantic(_ context.Context, _ string, _ []float32, limit int) ([]core.SearchHit, error) {
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

func newAPI() http.Handler {
	eng := core.NewEngine(newMem(), fakeEmbedder{})
	return rest.New(eng).Routes()
}

type fakeSummarizer struct {
	reply string
	err   error
}

func (f fakeSummarizer) Configured() bool { return true }
func (f fakeSummarizer) Complete(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

func newAPIWithSummarizer(s core.Summarizer) http.Handler {
	eng := core.NewEngine(newMem(), fakeEmbedder{}).WithSummarizer(s)
	return rest.New(eng).Routes()
}

// --- tests ------------------------------------------------------------------

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newAPI().ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal health body: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("health body: %v", body)
	}
}

func TestWriteThenGet(t *testing.T) {
	api := newAPI()
	// write
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Hello","body":"world [[x]]"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("write status %d: %s", rec.Code, rec.Body.String())
	}
	var n core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("failed to unmarshal note body: %v", err)
	}
	if n.ID != "hello" {
		t.Fatalf("id: %q", n.ID)
	}
	// get
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/hello", nil))
	if rec.Code != 200 {
		t.Fatalf("get status %d", rec.Code)
	}
	var got core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("failed to unmarshal note body: %v", err)
	}
	if got.Title != "Hello" {
		t.Fatalf("get title: %q", got.Title)
	}
}

func TestGetMissing404(t *testing.T) {
	rec := httptest.NewRecorder()
	newAPI().ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestWriteMissingTitle(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"body":"no title"}`))
	req.Header.Set("Content-Type", "application/json")
	newAPI().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

func TestSearchEndpoint(t *testing.T) {
	api := newAPI()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Doc","body":"podman notes"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/search?q=podman&kind=keyword", nil))
	if rec.Code != 200 {
		t.Fatalf("search status %d", rec.Code)
	}
	var hits []core.SearchHit
	_ = json.Unmarshal(rec.Body.Bytes(), &hits)
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
}

// TestSearchHybridEndpoint confirms kind=hybrid is accepted end-to-end and
// returns fused hits tagged with kind "hybrid".
func TestSearchHybridEndpoint(t *testing.T) {
	api := newAPI()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Doc","body":"podman notes"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/search?q=podman&kind=hybrid", nil))
	if rec.Code != 200 {
		t.Fatalf("search status %d: %s", rec.Code, rec.Body.String())
	}
	var hits []core.SearchHit
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatalf("failed to unmarshal hits: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one hybrid hit")
	}
	if hits[0].Kind != "hybrid" {
		t.Fatalf("expected kind=hybrid, got %q", hits[0].Kind)
	}
}

// TestSuggestionsEndpoint confirms GET /api/notes/{id}/suggestions returns
// hits tagged "suggestion" and rejects a missing note with 404.
func TestSuggestionsEndpoint(t *testing.T) {
	api := newAPI()
	for _, body := range []string{
		`{"title":"Postgres backups","body":"pg_dump and WAL archiving"}`,
		`{"title":"Postgres tuning","body":"pg_dump and WAL archiving tips"}`,
	} {
		req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/postgres-backups/suggestions", nil))
	if rec.Code != 200 {
		t.Fatalf("suggestions status %d: %s", rec.Code, rec.Body.String())
	}
	var hits []core.SearchHit
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatalf("failed to unmarshal hits: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected at least one suggestion")
	}
	if hits[0].Kind != "suggestion" {
		t.Fatalf("expected kind=suggestion, got %q", hits[0].Kind)
	}

	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/nope/suggestions", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSearchMissingQuery(t *testing.T) {
	rec := httptest.NewRecorder()
	newAPI().ServeHTTP(rec, httptest.NewRequest("GET", "/api/search", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}

// TestGetInvalidID400 confirms an invalid id (e.g. path traversal) is a
// client error, not a 500: writeEngErr must map core.ErrInvalidInput to 400.
func TestGetInvalidID400(t *testing.T) {
	rec := httptest.NewRecorder()
	newAPI().ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/..%2Fetc%2Fpasswd", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestPatchMissingNote404 confirms a patch against a note that does not
// exist reports 404 (core.ErrNotFound), not 400 or 500.
func TestPatchMissingNote404(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/notes/nope", strings.NewReader(`{"old_str":"a","new_str":"b"}`))
	req.Header.Set("Content-Type", "application/json")
	newAPI().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSummaryEndpoint_NotConfigured confirms a deployment with no
// completion endpoint reports 501, not a generic 500.
func TestSummaryEndpoint_NotConfigured(t *testing.T) {
	api := newAPI()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Doc","body":"some body text"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/doc/summary", nil))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSummaryEndpoint_Configured confirms a configured summarizer returns
// its completion text through GET /api/notes/{id}/summary.
func TestSummaryEndpoint_Configured(t *testing.T) {
	api := newAPIWithSummarizer(fakeSummarizer{reply: "a short summary"})
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Doc","body":"some body text"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/doc/summary", nil))
	if rec.Code != 200 {
		t.Fatalf("summary status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to unmarshal summary body: %v", err)
	}
	if body["summary"] != "a short summary" {
		t.Fatalf("summary: %q", body["summary"])
	}
}

// TestWriteBodyTooLarge confirms an oversized request body is rejected
// before it reaches JSON decoding or the engine.
func TestWriteBodyTooLarge(t *testing.T) {
	huge := strings.Repeat("a", 5*1024*1024)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"t","body":"`+huge+`"}`))
	req.Header.Set("Content-Type", "application/json")
	newAPI().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}
