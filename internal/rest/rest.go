// Package rest exposes the engram engine as a JSON HTTP API. Like the MCP
// adapter, it is thin: handlers call the shared core.Engine so both
// interfaces stay in lockstep.
package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/davasorus/engram/internal/core"
)

// maxBodyBytes bounds the size of a request body the server will read. It
// protects the server from an oversized POST/PATCH before it ever reaches
// core validation.
const maxBodyBytes = 4 * 1024 * 1024

type API struct {
	eng *core.Engine
}

func New(eng *core.Engine) *API { return &API{eng: eng} }

// Routes returns a mux with the REST endpoints mounted under /api/.
func (a *API) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/search", a.search)
	mux.HandleFunc("GET /api/notes", a.list)
	mux.HandleFunc("POST /api/notes", a.write)
	mux.HandleFunc("GET /api/notes/{id}", a.get)
	mux.HandleFunc("PATCH /api/notes/{id}", a.patch)
	mux.HandleFunc("DELETE /api/notes/{id}", a.delete)
	mux.HandleFunc("GET /api/notes/{id}/links", a.links)
	mux.HandleFunc("GET /api/notes/{id}/suggestions", a.suggestions)
	mux.HandleFunc("GET /api/notes/{id}/summary", a.summary)
	mux.HandleFunc("POST /api/reembed", a.reembed)
	mux.HandleFunc("GET /api/health", a.health)
	return mux
}

func (a *API) health(w http.ResponseWriter, r *http.Request) {
	n, err := a.eng.Count(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "notes": n})
}

func (a *API) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeErr(w, http.StatusBadRequest, "missing ?q=")
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	kind := r.URL.Query().Get("kind")
	project := r.URL.Query().Get("project")
	hits, err := a.eng.Search(r.Context(), project, q, limit, kind)
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	project := r.URL.Query().Get("project")
	ns, err := a.eng.List(r.Context(), project, limit, offset)
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

type writeBody struct {
	ID      string   `json:"id"`
	Project string   `json:"project"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
}

func (a *API) write(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var b writeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, bodyDecodeErr(err))
		return
	}
	n, err := a.eng.Write(r.Context(), core.WriteInput{ID: b.ID, Project: b.Project, Title: b.Title, Body: b.Body, Tags: b.Tags})
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	n, err := a.eng.Read(r.Context(), r.PathValue("id"))
	if err != nil {
		writeEngErr(w, err)
		return
	}
	if n == nil {
		writeErr(w, http.StatusNotFound, "note not found")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

type patchBody struct {
	OldStr string `json:"old_str"`
	NewStr string `json:"new_str"`
}

func (a *API) patch(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	var b patchBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, bodyDecodeErr(err))
		return
	}
	n, err := a.eng.Patch(r.Context(), r.PathValue("id"), b.OldStr, b.NewStr)
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	if err := a.eng.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("id")})
}

func (a *API) links(w http.ResponseWriter, r *http.Request) {
	bl, err := a.eng.Backlinks(r.Context(), r.PathValue("id"))
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, bl)
}

func (a *API) suggestions(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hits, err := a.eng.SuggestLinks(r.Context(), r.PathValue("id"), limit)
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (a *API) summary(w http.ResponseWriter, r *http.Request) {
	s, err := a.eng.Summarize(r.Context(), r.PathValue("id"))
	if err != nil {
		writeEngErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"summary": s})
}

func (a *API) reembed(w http.ResponseWriter, r *http.Request) {
	n, err := a.eng.Reembed(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"reembedded": n})
}

// --- helpers ----------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": strings.TrimSpace(msg)})
}

// writeEngErr maps a core.Engine error to the correct HTTP status: 400 for
// bad input, 404 for a missing note, 500 for everything else (store or
// embedding failures).
func writeEngErr(w http.ResponseWriter, err error) {
	switch {
	case core.IsInvalidInput(err):
		writeErr(w, http.StatusBadRequest, err.Error())
	case core.IsNotFound(err):
		writeErr(w, http.StatusNotFound, err.Error())
	case core.IsNotConfigured(err):
		writeErr(w, http.StatusNotImplemented, err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
}

// bodyDecodeErr turns a JSON-decode error into a client-facing message. A
// request that was rejected for exceeding maxBodyBytes gets a specific
// message instead of a generic "invalid JSON body".
func bodyDecodeErr(err error) string {
	if err.Error() == "http: request body too large" {
		return "request body too large"
	}
	return "invalid JSON body"
}
