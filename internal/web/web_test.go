package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
)

type ms struct{ n map[string]core.Note }

func (m *ms) Upsert(_ context.Context, x core.Note) error { m.n[x.ID] = x; return nil }
func (m *ms) Get(_ context.Context, id string) (*core.Note, error) {
	if v, ok := m.n[id]; ok {
		return &v, nil
	}
	return nil, nil
}
func (m *ms) Delete(context.Context, string) error                        { return nil }
func (m *ms) List(context.Context, string, int, int) ([]core.Note, error) { return nil, nil }
func (m *ms) Count(context.Context) (int, error)                          { return 0, nil }
func (m *ms) SearchSemantic(context.Context, string, []float32, int) ([]core.SearchHit, error) {
	return nil, nil
}
func (m *ms) KeywordSearch(context.Context, string, string, int) ([]core.Note, error) {
	return nil, nil
}
func (m *ms) Backlinks(context.Context, string) ([]core.Backlink, error) { return nil, nil }
func (m *ms) Close() error                                               { return nil }

type fe struct{}

func (fe) Model() string                                    { return "f" }
func (fe) Embed(context.Context, string) ([]float32, error) { return []float32{1, 0}, nil }
func TestPagesRenderOwnContent(t *testing.T) {
	s, err := New(core.NewEngine(&ms{n: map[string]core.Note{"x": {ID: "x", Title: "T", Body: "hi"}}}, fe{}))
	if err != nil {
		t.Fatal(err)
	}
	mux := s.Routes()
	for _, c := range []struct{ path, want string }{
		{"/new", `id="ed-body"`}, {"/", `notelist`}, {"/note/x", `class="rendered"`}, {"/search", `id="search-results"`},
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
