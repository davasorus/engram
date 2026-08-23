package web

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davasorus/engram/internal/core"
)

type bs struct{}

func (bs) Upsert(context.Context, core.Note) error { return nil }
func (bs) Get(_ context.Context, id string) (*core.Note, error) {
	return &core.Note{ID: id, Project: "engram", Title: "T", Body: "b"}, nil
}
func (bs) Delete(context.Context, string) error { return nil }
func (bs) List(_ context.Context, _ string, _, _ int) ([]core.Note, error) {
	return []core.Note{{ID: "n1", Project: "engram", Title: "N1", Body: "b"}}, nil
}
func (bs) Count(context.Context) (int, error)                 { return 1, nil }
func (bs) MissingVectorIDs(context.Context) ([]string, error) { return nil, nil }
func (bs) SearchSemantic(_ context.Context, _ string, _ []float32, _ int) ([]core.SearchHit, error) {
	return []core.SearchHit{{Note: core.Note{ID: "n1", Project: "engram", Title: "N1", Body: "b"}, Score: 0.7, Kind: "semantic"}}, nil
}
func (bs) KeywordSearch(_ context.Context, _ string, _ string, _ int) ([]core.Note, error) {
	return nil, nil
}
func (bs) Backlinks(context.Context, string) ([]core.Backlink, error) { return nil, nil }
func (bs) Close() error                                               { return nil }

type be struct{}

func (be) Model() string                                    { return "f" }
func (be) Embed(context.Context, string) ([]float32, error) { return []float32{1}, nil }
func TestProjectBadgeRenders(t *testing.T) {
	s, err := New(core.NewEngine(bs{}, be{}))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/", "/note/n1", "/search?q=x&kind=semantic"} {
		rec := httptest.NewRecorder()
		s.Routes().ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		if !strings.Contains(rec.Body.String(), "project-badge") || !strings.Contains(rec.Body.String(), "engram") {
			t.Errorf("%s: project badge not rendered", p)
		}
	}
}
