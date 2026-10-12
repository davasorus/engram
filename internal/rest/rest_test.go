package rest_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/rest"
)

// --- minimal in-memory Store + Embedder for handler tests ------------------

type memStore struct {
	notes map[string]core.Note
	fail  map[string]bool // operation names to fail on
}

func newMem() *memStore { return &memStore{notes: map[string]core.Note{}} }

func (m *memStore) setFail(op string) {
	if m.fail == nil {
		m.fail = map[string]bool{}
	}
	m.fail[op] = true
}

func (m *memStore) clearFail(op string) {
	if m.fail != nil {
		delete(m.fail, op)
	}
}

func (m *memStore) Upsert(_ context.Context, n core.Note) error {
	if m.fail["Upsert"] {
		return fmt.Errorf("store error: Upsert")
	}
	m.notes[n.ID] = n
	return nil
}
func (m *memStore) Get(_ context.Context, id string) (*core.Note, error) {
	if m.fail["Get"] {
		return nil, fmt.Errorf("store error: Get")
	}
	n, ok := m.notes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}
func (m *memStore) Delete(_ context.Context, id string) error {
	if m.fail["Delete"] {
		return fmt.Errorf("store error: Delete")
	}
	delete(m.notes, id)
	return nil
}
func (m *memStore) List(_ context.Context, project string, limit, offset int) ([]core.Note, error) {
	if m.fail["List"] {
		return nil, fmt.Errorf("store error: List")
	}
	var out []core.Note
	for _, n := range m.notes {
		if project != "" && n.Project != project {
			continue
		}
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if offset > len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (m *memStore) Count(_ context.Context) (int, error) {
	if m.fail["Count"] {
		return 0, fmt.Errorf("store error: Count")
	}
	return len(m.notes), nil
}
func (m *memStore) MissingVectorIDs(_ context.Context) ([]string, error) {
	if m.fail["MissingVectorIDs"] {
		return nil, fmt.Errorf("store error: MissingVectorIDs")
	}
	var ids []string
	for id, n := range m.notes {
		if len(n.Vector) == 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}
func (m *memStore) Stats(_ context.Context) (core.Stats, error) {
	if m.fail["Stats"] {
		return core.Stats{}, fmt.Errorf("store error: Stats")
	}
	return core.Stats{TotalNotes: len(m.notes)}, nil
}
func (m *memStore) SearchSemantic(_ context.Context, _ string, _ []float32, limit int) ([]core.SearchHit, error) {
	if m.fail["SearchSemantic"] {
		return nil, fmt.Errorf("store error: SearchSemantic")
	}
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
func (m *memStore) Backlinks(_ context.Context, idOrTitle string) ([]core.Backlink, error) {
	if m.fail["Backlinks"] {
		return nil, fmt.Errorf("store error: Backlinks")
	}
	idOrTitle = strings.ToLower(idOrTitle)
	var out []core.Backlink
	for _, n := range m.notes {
		for _, l := range n.Links {
			if strings.ToLower(l) == idOrTitle {
				out = append(out, core.Backlink{ID: n.ID, Title: n.Title})
				break
			}
		}
	}
	return out, nil
}
func (m *memStore) Close() error { return nil }

type fakeEmbedder struct{}

func (fakeEmbedder) Model() string                                    { return "fake" }
func (fakeEmbedder) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }

func newAPI() http.Handler {
	eng := core.NewEngine(newMem(), fakeEmbedder{})
	return rest.New(eng).Routes()
}

// newAPIWithFail returns an API where the given store operation fails.
// The op name must match the method name on memStore (e.g. "Upsert", "Get").
func newAPIWithFail(op string) http.Handler {
	st := newMem()
	st.setFail(op)
	eng := core.NewEngine(st, fakeEmbedder{})
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

// newAPIWithSummarizerAndStore builds an API from a specific store and
// summarizer, allowing error-injection tests that need both.
func newAPIWithSummarizerAndStore(st *memStore, s core.Summarizer) http.Handler {
	eng := core.NewEngine(st, fakeEmbedder{}).WithSummarizer(s)
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

// --- List endpoint ---

func TestListEndpoint(t *testing.T) {
	api := newAPI()
	// Write a few notes first.
	for _, b := range []string{
		`{"title":"Alpha","body":"first"}`,
		`{"title":"Beta","body":"second"}`,
		`{"title":"Gamma","body":"third"}`,
	} {
		req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var notes []core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &notes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(notes) != 3 {
		t.Fatalf("expected 3 notes, got %d", len(notes))
	}
}

func TestListWithPagination(t *testing.T) {
	api := newAPI()
	for i := 0; i < 5; i++ {
		body := fmt.Sprintf(`{"title":"Note%d","body":"body%d"}`, i, i)
		req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes?limit=2&offset=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var notes []core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &notes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes, got %d", len(notes))
	}
}

func TestListWithProjectFilter(t *testing.T) {
	api := newAPI()
	for _, b := range []string{
		`{"title":"A","body":"a","project":"proj1"}`,
		`{"title":"B","body":"b","project":"proj1"}`,
		`{"title":"C","body":"c","project":"proj2"}`,
	} {
		req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		api.ServeHTTP(httptest.NewRecorder(), req)
	}

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes?project=proj1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rec.Code, rec.Body.String())
	}
	var notes []core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &notes); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(notes) != 2 {
		t.Fatalf("expected 2 notes in proj1, got %d", len(notes))
	}
}

// --- Delete endpoint ---

func TestDeleteEndpoint(t *testing.T) {
	api := newAPI()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"To Delete","body":"gone"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/notes/to-delete", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["deleted"] != "to-delete" {
		t.Fatalf("deleted id: %q", body["deleted"])
	}

	// Verify it's gone
	rec = httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/to-delete", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec.Code)
	}
}

// --- Links endpoint ---

func TestLinksEndpoint(t *testing.T) {
	api := newAPI()
	// Write two notes where one links to the other.
	_, _ = writeNote(api, "Target", "the target")
	_, _ = writeNote(api, "Source", "see [[Target]] for details")

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/target/links", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("links status %d: %s", rec.Code, rec.Body.String())
	}
	var links []core.Backlink
	if err := json.Unmarshal(rec.Body.Bytes(), &links); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(links) != 1 || links[0].Title != "Source" {
		t.Fatalf("expected backlink from Source, got %+v", links)
	}
}

// --- Stats endpoint ---

func TestStatsEndpoint(t *testing.T) {
	api := newAPI()
	_, _ = writeNote(api, "Note One", "first")
	_, _ = writeNote(api, "Note Two", "second")

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stats status %d: %s", rec.Code, rec.Body.String())
	}
	var stats core.Stats
	if err := json.Unmarshal(rec.Body.Bytes(), &stats); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if stats.TotalNotes != 2 {
		t.Fatalf("expected 2 total notes, got %d", stats.TotalNotes)
	}
}

// --- Reembed endpoint ---

func TestReembedEndpoint(t *testing.T) {
	api := newAPI()
	_, _ = writeNote(api, "Reembed Me", "content")

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("POST", "/api/reembed?full=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reembed status %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]int
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["reembedded"] != 1 {
		t.Fatalf("expected 1 reembedded, got %d", body["reembedded"])
	}
}

func TestReembedEndpointDefaultMode(t *testing.T) {
	api := newAPI()
	// Default mode (full=0) should still succeed.
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("POST", "/api/reembed", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("reembed status %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Search with project ---

func TestSearchWithProject(t *testing.T) {
	api := newAPI()
	_, _ = writeNote(api, "Alpha", "alpha body", "project", "proj1")
	_, _ = writeNote(api, "Beta", "beta body", "project", "proj2")

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/search?q=alpha&kind=keyword&project=proj1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status %d: %s", rec.Code, rec.Body.String())
	}
	var hits []core.SearchHit
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(hits) != 1 || hits[0].Note.Title != "Alpha" {
		t.Fatalf("expected Alpha hit, got %+v", hits)
	}
}

// --- Write with tags ---

func TestWriteWithTags(t *testing.T) {
	api := newAPI()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Tagged","body":"has tags","tags":["a","b","c"]}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("write status %d: %s", rec.Code, rec.Body.String())
	}
	var n core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(n.Tags) != 3 {
		t.Fatalf("expected 3 tags, got %d", len(n.Tags))
	}
}

// --- Write with project ---

func TestWriteWithProject(t *testing.T) {
	api := newAPI()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{"title":"Scoped","body":"scoped body","project":"my-proj"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("write status %d: %s", rec.Code, rec.Body.String())
	}
	var n core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if n.Project != "my-proj" {
		t.Fatalf("expected project my-proj, got %q", n.Project)
	}
}

// --- Search semantic with project ---

func TestSearchSemanticWithProject(t *testing.T) {
	api := newAPI()
	_, _ = writeNote(api, "Target Note", "relevant content", "project", "proj1")

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/search?q=relevant&project=proj1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("search status %d: %s", rec.Code, rec.Body.String())
	}
	var hits []core.SearchHit
	if err := json.Unmarshal(rec.Body.Bytes(), &hits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(hits))
	}
}

// --- Helper: write a note and return the response body ---

func writeNote(api http.Handler, title, body string, kv ...string) (string, error) {
	params := map[string]string{"title": title, "body": body}
	for i := 0; i < len(kv); i += 2 {
		params[kv[i]] = kv[i+1]
	}
	data, _ := json.Marshal(params)
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("write failed: %s", rec.Body.String())
	}
	return rec.Body.String(), nil
}

// --- Phase 5: PATCH success path (task 5.1) ---

func TestPatchSuccess(t *testing.T) {
	api := newAPI()
	writeNote(api, "Patch Test", "hello world")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/notes/patch-test",
		strings.NewReader(`{"old_str":"hello","new_str":"goodbye"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status %d: %s", rec.Code, rec.Body.String())
	}
	var n core.Note
	if err := json.Unmarshal(rec.Body.Bytes(), &n); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(n.Body, "goodbye") {
		t.Fatalf("expected body to contain 'goodbye', got %q", n.Body)
	}
}

// TestPatchAmbiguousOldStr returns 400 when oldStr appears multiple times.
func TestPatchAmbiguousOldStr(t *testing.T) {
	api := newAPI()
	writeNote(api, "Ambiguous", "foo bar foo")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/notes/ambiguous",
		strings.NewReader(`{"old_str":"foo","new_str":"baz"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: bodyDecodeErr sentinel comparison (task 5.7) ---

// TestBodyDecodeErrOversized confirms the oversized-body message is used
// when the request exceeds maxBodyBytes, rather than the generic message.
func TestBodyDecodeErrOversized(t *testing.T) {
	huge := strings.Repeat("a", 5*1024*1024)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes",
		strings.NewReader(`{"title":"t","body":"`+huge+`"}`))
	req.Header.Set("Content-Type", "application/json")
	rest.New(core.NewEngine(newMem(), fakeEmbedder{})).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "request body too large" {
		t.Fatalf("expected 'request body too large', got %q", body["error"])
	}
}

// TestBodyDecodeErrInvalidJSON confirms the generic message is used for
// malformed JSON that is not oversized.
func TestBodyDecodeErrInvalidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes", strings.NewReader(`{not json}`))
	req.Header.Set("Content-Type", "application/json")
	rest.New(core.NewEngine(newMem(), fakeEmbedder{})).Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body["error"] != "invalid JSON body" {
		t.Fatalf("expected 'invalid JSON body', got %q", body["error"])
	}
}

// --- Phase 5: health error path ---

// TestHealthStoreError confirms a store failure in Count returns 500.
func TestHealthStoreError(t *testing.T) {
	api := newAPIWithFail("Count")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: search error path ---

// TestSearchStoreError confirms a store failure in SearchSemantic returns 500.
func TestSearchStoreError(t *testing.T) {
	// SearchSemantic is called after Embed succeeds (fakeEmbedder always works).
	// The store must fail on SearchSemantic.
	st := newMem()
	st.setFail("SearchSemantic")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/search?q=test", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: list error path ---

// TestListStoreError confirms a store failure in List returns 500.
func TestListStoreError(t *testing.T) {
	api := newAPIWithFail("List")
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: delete error path ---

// TestDeleteStoreError confirms a store failure in Delete returns 500.
func TestDeleteStoreError(t *testing.T) {
	api := newAPI()
	writeNote(api, "Delete Error", "body")

	api2 := newAPIWithFail("Delete")
	// The newAPIWithFail creates a fresh store, so we need to write first.
	st := newMem()
	st.setFail("Delete")
	eng := core.NewEngine(st, fakeEmbedder{})
	_, _ = eng.Write(context.Background(), core.WriteInput{Title: "Delete Error", Body: "body"})
	api2 = rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api2.ServeHTTP(rec, httptest.NewRequest("DELETE", "/api/notes/delete-error", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: links error path ---

// TestLinksStoreError confirms a store failure in Backlinks returns 500.
func TestLinksStoreError(t *testing.T) {
	st := newMem()
	st.setFail("Backlinks")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/foo/links", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: stats error path ---

// TestStatsStoreError confirms a store failure in Stats returns 500.
func TestStatsStoreError(t *testing.T) {
	st := newMem()
	st.setFail("Stats")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/stats", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: reembed error path ---

// TestReembedStoreError confirms a store failure in Reembed returns 500.
func TestReembedStoreError(t *testing.T) {
	st := newMem()
	st.setFail("MissingVectorIDs")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("POST", "/api/reembed", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: write error path ---

// TestWriteStoreError confirms a store failure in Write returns 500.
func TestWriteStoreError(t *testing.T) {
	st := newMem()
	st.setFail("Upsert")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/notes",
		strings.NewReader(`{"title":"Fail","body":"body"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: get error path ---

// TestGetStoreError confirms a store failure in Read returns 500.
func TestGetStoreError(t *testing.T) {
	st := newMem()
	st.setFail("Get")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/foo", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: patch error path ---

// TestPatchStoreError confirms a store failure in Patch returns 500.
func TestPatchStoreError(t *testing.T) {
	// Patch calls Get first (to read the note), then Write (which calls Upsert).
	// We need a note to exist so Get doesn't return 404 first.
	st := newMem()
	_ = st.Upsert(context.Background(), core.Note{ID: "foo", Title: "Foo", Body: "hello world"})
	st.setFail("Upsert")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/notes/foo",
		strings.NewReader(`{"old_str":"hello","new_str":"goodbye"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: suggestions error path ---

// TestSuggestionsStoreError confirms a store failure in SuggestLinks returns 500.
func TestSuggestionsStoreError(t *testing.T) {
	// SuggestLinks calls Get first, then SearchSemantic.
	// Write a note first, then fail on SearchSemantic.
	st := newMem()
	_ = st.Upsert(context.Background(), core.Note{ID: "foo", Title: "Foo", Body: "content", Vector: []float32{1, 0}})
	st.setFail("SearchSemantic")
	eng := core.NewEngine(st, fakeEmbedder{})
	api := rest.New(eng).Routes()

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/foo/suggestions", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: summary error path ---

// TestSummaryStoreError confirms a store failure in Summarize returns 500.
func TestSummaryStoreError(t *testing.T) {
	st := newMem()
	st.setFail("Get")
	api := newAPIWithSummarizerAndStore(st, fakeSummarizer{})

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/foo/summary", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Phase 5: summary completion error path ---

// TestSummaryEndpoint_CompletionError confirms a Summarizer completion
// failure returns 500, not a different status.
func TestSummaryEndpoint_CompletionError(t *testing.T) {
	api := newAPIWithSummarizer(fakeSummarizer{err: fmt.Errorf("model timeout")})
	req := httptest.NewRequest("POST", "/api/notes",
		strings.NewReader(`{"title":"Doc","body":"some body text"}`))
	req.Header.Set("Content-Type", "application/json")
	api.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest("GET", "/api/notes/doc/summary", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
	}
}
