// Package web serves the human-facing UI: browse, read (rendered markdown),
// semantic search, and a live-preview editor. It renders server-side via
// goldmark; mermaid and KaTeX render client-side. It's a window into the
// agent's brain, not a full PKM app.
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/davasorus/engram/internal/core"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Server struct {
	eng   *core.Engine
	pages map[string]*template.Template // page name -> base+page+fragments
	frags *template.Template            // standalone fragments (search_results)
}

func New(eng *core.Engine) (*Server, error) {
	funcs := template.FuncMap{
		"rendermd": func(v any) template.HTML {
			s, _ := v.(string)
			return template.HTML(RenderMarkdown(s))
		},
	}

	// Each page template defines {{"content"}} and {{"title"}}. Because those
	// names are shared across page files, parsing all pages into ONE template
	// set makes the last-parsed page silently overwrite the others' blocks.
	// So we build a SEPARATE template set per page, each containing only
	// base.html + shared partials + that ONE page. This isolates the content
	// block so every page renders its own.
	shared := []string{"templates/base.html", "templates/search_results.html", "templates/search_dropdown.html"}
	pageFiles := []string{"list.html", "view.html", "search.html", "edit.html"}

	pages := map[string]*template.Template{}
	for _, p := range pageFiles {
		files := append(append([]string{}, shared...), "templates/"+p)
		t, err := template.New("").Funcs(funcs).ParseFS(assets, files...)
		if err != nil {
			return nil, err
		}
		pages[p] = t
	}

	// Fragments render standalone (no base shell) for HTMX swaps.
	frags, err := template.New("").Funcs(funcs).ParseFS(assets, "templates/search_results.html", "templates/search_dropdown.html")
	if err != nil {
		return nil, err
	}

	return &Server{eng: eng, pages: pages, frags: frags}, nil
}

func (s *Server) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	staticFS, _ := fs.Sub(assets, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticFS))))
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("GET /note/{id}", s.view)
	mux.HandleFunc("GET /search", s.search)
	mux.HandleFunc("GET /frag/search", s.searchFragment)
	mux.HandleFunc("GET /edit/{id}", s.edit)
	mux.HandleFunc("GET /new", s.newNote)
	mux.HandleFunc("POST /preview", s.preview)
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	notes, err := s.eng.List(r.Context(), "", 100, 0)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	count, _ := s.eng.Count(r.Context())
	s.render(w, "list.html", map[string]any{"Notes": notes, "Count": count})
}

func (s *Server) view(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	n, err := s.eng.Read(r.Context(), id)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	if n == nil {
		http.NotFound(w, r)
		return
	}
	bl, _ := s.eng.Backlinks(r.Context(), id)
	s.render(w, "view.html", map[string]any{"Note": n, "Backlinks": bl})
}

func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	var hits []core.SearchHit
	if q != "" {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		hits, _ = s.eng.Search(r.Context(), "", q, limit, kind)
	}
	vms := toVMs(hits)
	s.render(w, "search.html", map[string]any{"Query": q, "Kind": kind, "Results": vms, "Count": len(vms)})
}

// searchFragment serves result fragments for HTMX. mode=dropdown renders the
// compact nav quick-search; otherwise the full /search results list.
func (s *Server) searchFragment(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	kind := r.URL.Query().Get("kind")
	mode := r.URL.Query().Get("mode")
	var hits []core.SearchHit
	if q != "" {
		limit := 0
		if mode == "dropdown" {
			limit = 6
		}
		hits, _ = s.eng.Search(r.Context(), "", q, limit, kind)
	}
	vms := toVMs(hits)
	data := map[string]any{"Query": q, "Kind": kind, "Results": vms, "Count": len(vms)}
	if mode == "dropdown" {
		s.renderFragment(w, "search_dropdown", data)
		return
	}
	s.renderFragment(w, "search_results", data)
}

func (s *Server) edit(w http.ResponseWriter, r *http.Request) {
	n, err := s.eng.Read(r.Context(), r.PathValue("id"))
	if err != nil || n == nil {
		http.NotFound(w, r)
		return
	}
	s.render(w, "edit.html", map[string]any{"Note": n})
}

func (s *Server) newNote(w http.ResponseWriter, r *http.Request) {
	s.render(w, "edit.html", map[string]any{"Note": &core.Note{}})
}

// preview renders posted markdown to HTML for the editor's live preview.
// Accepts either a form field 'body' (HTMX default encoding) or a JSON body.
func (s *Server) preview(w http.ResponseWriter, r *http.Request) {
	var md string
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		var body struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		md = body.Body
	} else {
		_ = r.ParseForm()
		md = r.FormValue("body")
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(RenderMarkdown(md)))
}

func (s *Server) render(w http.ResponseWriter, name string, data any) {
	t, ok := s.pages[name]
	if !ok {
		http.Error(w, "unknown page: "+name, http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	// The page file's top level is {{template "base" .}}, and base pulls in the
	// page's {{"content"}}/{{"title"}} blocks. So the entry point is the page
	// file itself.
	if err := t.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

// renderFragment renders a single {{define}}'d template by name (an HTML
// fragment, no page shell) for HTMX swaps.
func (s *Server) renderFragment(w http.ResponseWriter, name string, data any) {
	var buf bytes.Buffer
	if err := s.frags.ExecuteTemplate(&buf, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}
