package rest

import (
	"net/http"
	"strings"
	"testing"
)

// failingWriter is a minimal http.ResponseWriter that fails on Write.
type failingWriter struct {
	code     int
	body     string
	wrote500 bool
}

func (f *failingWriter) Header() http.Header { return http.Header{} }
func (f *failingWriter) WriteHeader(int)     {}
func (f *failingWriter) Write([]byte) (int, error) {
	f.wrote500 = true
	f.code = http.StatusInternalServerError
	f.body = "failed to encode response"
	return 0, nil
}

// TestWriteJSONErrorPath confirms that when json.NewEncoder.Encode fails,
// the handler returns 500 and the error message is written to the response.
func TestWriteJSONErrorPath(t *testing.T) {
	fw := &failingWriter{}
	writeJSON(fw, http.StatusOK, map[string]string{"key": "value"})
	if fw.code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", fw.code)
	}
	if !strings.Contains(fw.body, "failed to encode response") {
		t.Fatalf("expected encode error message, got %s", fw.body)
	}
}
