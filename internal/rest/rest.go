// Package rest exposes the engram engine as a JSON HTTP API. Like the MCP
// adapter, it's thin: handlers call the shared core.Engine so both interfaces
// stay in lockstep.
package rest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/davasorus/engram/internal/core"
)

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
	hits, err := a.eng.Search(r.Context(), q, limit, kind)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, hits)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	ns, err := a.eng.List(r.Context(), limit, offset)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ns)
}

type writeBody struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

func (a *API) write(w http.ResponseWriter, r *http.Request) {
	var b writeBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	n, err := a.eng.Write(r.Context(), core.WriteInput{ID: b.ID, Title: b.Title, Body: b.Body, Tags: b.Tags})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	n, err := a.eng.Read(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
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
	var b patchBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	n, err := a.eng.Patch(r.Context(), r.PathValue("id"), b.OldStr, b.NewStr)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	if err := a.eng.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": r.PathValue("id")})
}

func (a *API) links(w http.ResponseWriter, r *http.Request) {
	bl, err := a.eng.Backlinks(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, bl)
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
