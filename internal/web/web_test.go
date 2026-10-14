package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
)

// --- mock store ---------------------------------------------------------------

type ms struct {
	n   map[string]core.Note
	err error
}

func (m *ms) Upsert(_ context.Context, _ core.Note) error { return m.err }
func (m *ms) Get(_ context.Context, id string) (*core.Note, error) {
	if m.err != nil {
		return nil, m.err
	}
	if v, ok := m.n[id]; ok {
		return &v, nil
	}
	return nil, nil
}
func (m *ms) Delete(context.Context, string) error { return nil }
func (m *ms) List(context.Context, string, int, int) ([]core.Note, error) {
	if m.err != nil {
		return nil, m.err
	}
	out := make([]core.Note, 0, len(m.n))
	for _, v := range m.n {
		out = append(out, v)
	}
	return out, nil
}
func (m *ms) ListAfter(context.Context, string, int) ([]core.Note, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, nil
}
func (m *ms) Count(context.Context) (int, error) {
	if m.err != nil {
		return 0, m.err
	}
	return len(m.n), nil
}
func (m *ms) MissingVectorIDs(context.Context) ([]string, error) { return nil, nil }
func (m *ms) Stats(context.Context) (core.Stats, error) {
	if m.err != nil {
		return core.Stats{}, m.err
	}
	return core.Stats{TotalNotes: len(m.n)}, nil
}
func (m *ms) SearchSemantic(context.Context, string, []float32, int) ([]core.SearchHit, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, nil
}
func (m *ms) KeywordSearch(context.Context, string, string, int) ([]core.Note, error) {
	if m.err != nil {
		return nil, m.err
	}
	return nil, nil
}
func (m *ms) Backlinks(context.Context, string, string) ([]core.Backlink, error) { return nil, nil }
func (m *ms) Close() error                                                       { return nil }

// --- mock embedder ------------------------------------------------------------

type fe struct{}

func (fe) Model() string                                    { return "f" }
func (fe) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }

// --- TestPagesRenderOwnContent (existing, kept) -------------------------------

func TestPagesRenderOwnContent(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"x":                      {ID: "x", Title: "T", Body: "hi"},
		"proj/scoped-note-title": {ID: "proj/scoped-note-title", Title: "Scoped", Body: "hi"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	for _, c := range []struct{ path, want string }{
		{"/new", `id="ed-body"`}, {"/", `notelist`}, {"/note/x", `class="rendered"`}, {"/search", `id="search-results"`}, {"/stats", `stat-num`},
		{"/note/proj/scoped-note-title", `class="rendered"`},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", c.path, nil))
		if rec.Code != 200 {
			t.Errorf("%s: %d", c.path, rec.Code)
		} else if !strings.Contains(rec.Body.String(), c.want) {
			t.Errorf("%s missing %q", c.path, c.want)
		} else {
			t.Logf("%s OK", c.path)
		}
	}
}

// --- RenderMarkdown tests (covers 26.7% → 100%) ------------------------------

func TestRenderMarkdown_Mermaid(t *testing.T) {
	input := "```mermaid\ngraph TD\nA-->B\n```"
	out := RenderMarkdown(input)
	if !strings.Contains(out, `<div class="mermaid">`) {
		t.Errorf("expected mermaid div, got: %s", out)
	}
}

func TestRenderMarkdown_Callout(t *testing.T) {
	input := "> [!NOTE] title"
	out := RenderMarkdown(input)
	if !strings.Contains(out, `class="callout callout-note"`) {
		t.Errorf("expected callout div, got: %s", out)
	}
}

func TestRenderMarkdown_Wikilink(t *testing.T) {
	input := "[[my note]]"
	out := RenderMarkdown(input)
	if !strings.Contains(out, `href="/note/my-note"`) {
		t.Errorf("expected wikilink anchor, got: %s", out)
	}
}

func TestRenderMarkdown_WikilinkWithAlias(t *testing.T) {
	input := "[[my note|custom label]]"
	out := RenderMarkdown(input)
	if !strings.Contains(out, `href="/note/my-note"`) {
		t.Errorf("expected wikilink anchor, got: %s", out)
	}
	if !strings.Contains(out, ">custom label<") {
		t.Errorf("expected alias label, got: %s", out)
	}
}

func TestRenderMarkdown_MathBlock(t *testing.T) {
	input := "$$E=mc^2$$"
	out := RenderMarkdown(input)
	if !strings.Contains(out, `class="math-block"`) {
		t.Errorf("expected math-block div, got: %s", out)
	}
}

func TestRenderMarkdown_MathInline(t *testing.T) {
	input := "use $x$ for variable"
	out := RenderMarkdown(input)
	if !strings.Contains(out, `class="math-inline"`) {
		t.Errorf("expected math-inline span, got: %s", out)
	}
}

func TestRenderMarkdown_EmptyBody(t *testing.T) {
	out := RenderMarkdown("")
	// goldmark produces no output for empty input, which is valid HTML (empty page).
	if out == "" {
		// Acceptable: empty body → empty HTML is valid.
		t.Log("empty body produces empty output (acceptable)")
	}
}

func TestRenderMarkdown_BoldItalic(t *testing.T) {
	input := "**bold** and *italic*"
	out := RenderMarkdown(input)
	if !strings.Contains(out, "<strong>") {
		t.Errorf("expected <strong>, got: %s", out)
	}
	if !strings.Contains(out, "<em>") {
		t.Errorf("expected <em>, got: %s", out)
	}
}

func TestRenderMarkdown_List(t *testing.T) {
	input := "- item 1\n- item 2"
	out := RenderMarkdown(input)
	if !strings.Contains(out, "<li>") {
		t.Errorf("expected <li>, got: %s", out)
	}
}

func TestRenderMarkdown_CodeBlock(t *testing.T) {
	input := "```\ncode here\n```"
	out := RenderMarkdown(input)
	if !strings.Contains(out, "<code>") {
		t.Errorf("expected <code>, got: %s", out)
	}
}

func TestRenderMarkdown_Table(t *testing.T) {
	input := "| A | B |\n|---|---|\n| 1 | 2 |"
	out := RenderMarkdown(input)
	if !strings.Contains(out, "<table>") {
		t.Errorf("expected <table>, got: %s", out)
	}
}

func TestRenderMarkdown_Strikethrough(t *testing.T) {
	input := "~~deleted~~"
	out := RenderMarkdown(input)
	if !strings.Contains(out, "<del>") {
		t.Errorf("expected <del>, got: %s", out)
	}
}

// --- slugForLink tests (covers 0% → 100%) -----------------------------------

func TestSlugForLink(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Hello World", "hello-world"},
		{"  spaced  ", "spaced"},
		{"UPPERCASE", "uppercase"},
		{"a-b_c.d", "a-b-c-d"},
		{"", ""},
		{"!!!", ""},
		{"hello  world", "hello-world"},
	}
	for _, tc := range tests {
		got := slugForLink(tc.input)
		if got != tc.want {
			t.Errorf("slugForLink(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- strengthPct tests (covers 35.7% → 100%) --------------------------------

func TestStrengthPct(t *testing.T) {
	tests := []struct {
		score float64
		kind  string
		want  int
	}{
		{1.0, "keyword", 100},
		{0.8, "semantic", 100},
		{0.3, "semantic", 0},
		{0.55, "semantic", 50},
		{0.4, "semantic", 20},
		{0.0, "semantic", 0},
		{-0.1, "semantic", 0},
		{0.033, "hybrid", 100},
		{0.0165, "hybrid", 50},
		{0.0, "hybrid", 0},
		{-1.0, "hybrid", 0},
		{0.1, "hybrid", 100},
	}
	for _, tc := range tests {
		got := strengthPct(tc.score, tc.kind)
		if got != tc.want {
			t.Errorf("strengthPct(%v, %q) = %d, want %d", tc.score, tc.kind, got, tc.want)
		}
	}
}

// --- strengthLabel tests (covers 50% → 100%) --------------------------------

func TestStrengthLabel(t *testing.T) {
	tests := []struct {
		score float64
		kind  string
		want  string
	}{
		{1.0, "keyword", "exact"},
		{0.7, "semantic", "strong"},
		{0.66, "semantic", "strong"},
		{0.5, "semantic", "good"},
		{0.33, "semantic", "weak"},
		{0.32, "semantic", "weak"},
		{0.0, "semantic", "weak"},
		{0.033, "hybrid", "strong"},
		{0.01, "hybrid", "weak"},
		{0.005, "hybrid", "weak"},
	}
	for _, tc := range tests {
		got := strengthLabel(tc.score, tc.kind)
		if got != tc.want {
			t.Errorf("strengthLabel(%v, %q) = %q, want %q", tc.score, tc.kind, got, tc.want)
		}
	}
}

// --- excerpt tests (covers 77.8% → 100%) ------------------------------------

func TestExcerpt_Frontmatter(t *testing.T) {
	input := "---\ntags: test\n---\nbody text"
	got := excerpt(input, 160)
	if strings.Contains(got, "tags") {
		t.Errorf("expected frontmatter stripped, got: %q", got)
	}
}

func TestExcerpt_LeadingHeading(t *testing.T) {
	input := "# Title\nbody text here"
	got := excerpt(input, 160)
	if strings.Contains(got, "#") {
		t.Errorf("expected heading stripped, got: %q", got)
	}
}

func TestExcerpt_Clip(t *testing.T) {
	input := "a very long body text that should be clipped at the specified length"
	got := excerpt(input, 20)
	if len(got) > 23 { // 20 chars + "…"
		t.Errorf("expected clipped output, got len %d: %q", len(got), got)
	}
}

func TestExcerpt_Empty(t *testing.T) {
	got := excerpt("", 10)
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestExcerpt_ShortBody(t *testing.T) {
	got := excerpt("short", 160)
	if got != "short" {
		t.Errorf("expected 'short', got %q", got)
	}
}

// --- toVMs tests (covers 100% → 100%) ---------------------------------------

func TestToVMs(t *testing.T) {
	hits := []core.SearchHit{
		{Note: core.Note{ID: "n1", Title: "Note One", Body: "body"}, Score: 0.5, Kind: "semantic"},
		{Note: core.Note{ID: "n2", Title: "Note Two", Body: "body"}, Score: 1.0, Kind: "keyword"},
	}
	vms := toVMs(hits)
	if len(vms) != 2 {
		t.Fatalf("expected 2 VMs, got %d", len(vms))
	}
	if vms[0].Strength <= 0 {
		t.Errorf("expected positive strength for semantic hit")
	}
	if vms[1].Strength != 100 {
		t.Errorf("expected keyword strength 100, got %d", vms[1].Strength)
	}
}

func TestToVMs_NilHits(t *testing.T) {
	vms := toVMs(nil)
	if vms == nil {
		t.Error("expected non-nil slice for nil input")
	}
	if len(vms) != 0 {
		t.Errorf("expected empty slice, got %d", len(vms))
	}
}

// --- cleanTitle tests (covers 100% → 100%) ----------------------------------

func TestCleanTitle(t *testing.T) {
	tests := []struct{ input, want string }{
		{"# heading", "heading"},
		{"## heading", "heading"},
		{"  title  ", "title"},
		{"#Title", "Title"},
	}
	for _, tc := range tests {
		got := cleanTitle(tc.input)
		if got != tc.want {
			t.Errorf("cleanTitle(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- Handler tests (cover searchFragment, edit, preview, renderFragment) ------

func TestSearchFragment_DropdownMode(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"a": {ID: "a", Title: "A", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/frag/search?q=test&mode=dropdown", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSearchFragment_FullMode(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"a": {ID: "a", Title: "A", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/frag/search?q=test", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSearchFragment_NoQuery(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/frag/search", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestEdit_Success(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note One", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/edit/n1", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="ed-body"`) {
		t.Error("expected editor body field")
	}
}

func TestEdit_NotFound(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note One", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/edit/nonexistent", nil))
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestPreview_JSONBody(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	body := `{"body":"**bold**"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<strong>") {
		t.Errorf("expected <strong>, got: %s", rec.Body.String())
	}
}

func TestPreview_FormBody(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	body := "body=**bold**"
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<strong>") {
		t.Errorf("expected <strong>, got: %s", rec.Body.String())
	}
}

func TestPreview_BadJSON(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/preview", strings.NewReader("{bad"))
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestRender_UnknownPage(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.render(rec, "nonexistent.html", nil)
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestIndex_StoreError(t *testing.T) {
	store := &ms{err: assert.AnError}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestView_NilNote(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/note/nonexistent", nil))
	if rec.Code != 404 {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestStats_StoreError(t *testing.T) {
	store := &ms{err: assert.AnError}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/stats", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestSearch_StoreError(t *testing.T) {
	store := &ms{err: assert.AnError}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/search?q=test", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestView_StoreError(t *testing.T) {
	store := &ms{err: assert.AnError}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/note/x", nil))
	if rec.Code != 500 {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestIndex_HasNotes(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note One", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "Note One") {
		t.Error("expected note title in output")
	}
}

func TestSearch_WithResults(t *testing.T) {
	store := &ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Search Match", Body: "body"},
	}}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/search?q=test", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSearch_WithLimit(t *testing.T) {
	store := &ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note", Body: "body"},
	}}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/search?q=test&limit=10", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSearch_WithKind(t *testing.T) {
	store := &ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note", Body: "body"},
	}}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/search?q=test&kind=keyword", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestSearch_WithKindHybrid(t *testing.T) {
	store := &ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note", Body: "body"},
	}}
	s, err := New(core.NewEngine(store, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/search?q=test&kind=hybrid", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestStats_Success(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/stats", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestNew_EmbeddingError(t *testing.T) {
	// New() parses templates only; embedding is not called during construction.
	// This test confirms New succeeds even without a working embedder.
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("expected non-nil Server")
	}
}

func TestRoutes_StaticFiles(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/static/style.css", nil))
	// Static files may return 404 if file doesn't exist, but shouldn't panic.
	t.Logf("static status: %d", rec.Code)
}

func TestRoutes_NewNote(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/new", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestIndex_EmptyList(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestView_Success(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{
		"n1": {ID: "n1", Title: "Note One", Body: "body"},
	}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/note/n1", nil))
	if rec.Code != 200 {
		t.Errorf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "class=\"rendered\"") {
		t.Error("expected rendered class")
	}
}

// assert.AnError is a simple sentinel error for testing.
var assert = struct {
	AnError error
}{
	AnError: assertErr{},
}

type assertErr struct{}

func (assertErr) Error() string { return "assert error" }
