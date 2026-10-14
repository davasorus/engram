package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// withEnv sets env vars for the duration of a test and restores whatever
// was there before (including "unset" for keys that didn't exist), so
// tests can't leak state into each other or into the real environment.
func withEnv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		prev, had := os.LookupEnv(k)
		if err := os.Setenv(k, v); err != nil {
			t.Fatalf("setenv %s: %v", k, err)
		}
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, prev)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}
}

func clearDBEnv(t *testing.T) {
	withEnv(t, map[string]string{
		"ENGRAM_DB_HOST":     "",
		"ENGRAM_DB_PORT":     "",
		"ENGRAM_DB_USER":     "",
		"ENGRAM_DB_PASSWORD": "",
		"ENGRAM_DB_NAME":     "",
	})
	// withEnv sets these to "" rather than truly unsetting them; env()
	// treats "" the same as unset (falls back to its default), so this is
	// equivalent for the purpose of these tests and keeps withEnv simple.
}

func TestRedactDSN_HidesPassword(t *testing.T) {
	in := "postgres://engram:supersecret@localhost:5432/engram?sslmode=disable"
	got := redactDSN(in)
	if strings.Contains(got, "supersecret") {
		t.Fatalf("redactDSN leaked the password: %q", got)
	}
	if !strings.Contains(got, "engram:REDACTED@") {
		t.Fatalf("redactDSN did not mask as expected: %q", got)
	}
}

func TestRedactDSN_NoPasswordIsNoOp(t *testing.T) {
	in := "postgres://engram@localhost:5432/engram?sslmode=disable"
	got := redactDSN(in)
	if got != in {
		t.Fatalf("redactDSN changed a DSN with no password: got %q, want %q", got, in)
	}
}

func TestRedactDSN_InvalidURLPassesThrough(t *testing.T) {
	// Not a real scenario in practice (resolveDSN always produces a valid
	// URL), but redactDSN must not panic or drop the value if it's ever
	// handed something unparseable — logging is not the place to crash.
	in := "not a url at all"
	got := redactDSN(in)
	if got != in {
		t.Fatalf("redactDSN(invalid) = %q, want unchanged %q", got, in)
	}
}

func TestDeployHint(t *testing.T) {
	if deployHint == "" {
		t.Fatal("deployHint must not be empty")
	}
	for _, need := range []string{"ENGRAM_DSN", "ENGRAM_DB_HOST", "ENGRAM_EMBED_URL", "github.com/davasorus/engram"} {
		if !strings.Contains(deployHint, need) {
			t.Fatalf("deployHint missing required text %q", need)
		}
	}
}

func TestRunHealthcheck_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Extract just the port from the httptest URL (e.g. "http://127.0.0.1:12345")
	// and pass it as ":port" so runHealthcheck can prepend "127.0.0.1".
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse test server URL: %v", err)
	}
	code := runHealthcheck(":" + u.Port())
	if code != 0 {
		t.Fatalf("expected exit 0, got %d", code)
	}
}

func TestRunHealthcheck_Failure(t *testing.T) {
	code := runHealthcheck(":1")
	if code != 1 {
		t.Fatalf("expected exit 1 for unreachable server, got %d", code)
	}
}

func TestRunHealthcheck_NonOKStatus(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	code := runHealthcheck(":" + host)
	if code != 1 {
		t.Fatalf("expected exit 1 for non-200 status, got %d", code)
	}
}

func TestLogRequests_Middleware(t *testing.T) {
	var logged bool
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logged = true
		w.WriteHeader(http.StatusOK)
	})
	middleware := logRequests(inner)

	req := httptest.NewRequest("GET", "/api/health", nil)
	rec := httptest.NewRecorder()
	middleware.ServeHTTP(rec, req)

	if !logged {
		t.Fatal("logRequests did not call the inner handler")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}
}

func TestCLI_Reembed(t *testing.T) {
	_, sa := newTestServer(t)
	if _, code := runCLIForTest(t, append([]string{"write", "-title", "ReembedTarget", "-id", "reembed-note"}, sa...)); code != 0 {
		t.Fatalf("setup write failed")
	}
	out, code := runCLIForTest(t, append([]string{"reembed"}, sa...))
	if code != 0 {
		t.Fatalf("reembed: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "reembedded") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestCLI_Reembed_FullFlag(t *testing.T) {
	_, sa := newTestServer(t)
	if _, code := runCLIForTest(t, append([]string{"write", "-title", "FullReembed", "-id", "full-note"}, sa...)); code != 0 {
		t.Fatalf("setup write failed")
	}
	out, code := runCLIForTest(t, append([]string{"reembed", "-full"}, sa...))
	if code != 0 {
		t.Fatalf("reembed -full: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, "reembedded") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestCLI_Stats(t *testing.T) {
	_, sa := newTestServer(t)
	out, code := runCLIForTest(t, append([]string{"stats"}, sa...))
	if code != 0 {
		t.Fatalf("stats: code=%d out=%s", code, out)
	}
	if !strings.Contains(out, `"total_notes"`) {
		t.Fatalf("stats output missing total_notes: %s", out)
	}
}

func TestCLI_Health_ServerDown(t *testing.T) {
	out, code := runCLIForTest(t, []string{"health", "-server", "http://localhost:1"})
	if code == 0 {
		t.Fatalf("expected non-zero exit for unreachable server, got 0")
	}
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected an error message, got: %s", out)
	}
}

func TestCLI_Get_NotFound(t *testing.T) {
	_, sa := newTestServer(t)
	out, code := runCLIForTest(t, append(append([]string{"get"}, sa...), "nonexistent-id"))
	if code == 0 {
		t.Fatalf("expected non-zero exit for missing note, got 0")
	}
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected an error message, got: %s", out)
	}
}

func TestCLI_Delete_Error(t *testing.T) {
	out, code := runCLIForTest(t, []string{"delete", "-server", "http://localhost:1", "any-id"})
	if code == 0 {
		t.Fatalf("expected non-zero exit for server error, got 0")
	}
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected an error message, got: %s", out)
	}
}

func TestCLI_Links_Error(t *testing.T) {
	out, code := runCLIForTest(t, []string{"links", "-server", "http://localhost:1", "any-id"})
	if code == 0 {
		t.Fatalf("expected non-zero exit for server error, got 0")
	}
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected an error message, got: %s", out)
	}
}

func TestCLI_Suggest_Error(t *testing.T) {
	out, code := runCLIForTest(t, []string{"suggest", "-server", "http://localhost:1", "any-id"})
	if code == 0 {
		t.Fatalf("expected non-zero exit for server error, got 0")
	}
	if !strings.Contains(out, "error:") {
		t.Fatalf("expected an error message, got: %s", out)
	}
}

func TestCLI_PrintJSON_ErrorPath(t *testing.T) {
	badWriter := &errorWriter{}
	code := cliPrintJSON(badWriter, map[string]string{"key": "value"})
	if code != 1 {
		t.Fatalf("expected exit 1 for encode error, got %d", code)
	}
}

// errorWriter is an io.Writer that always returns an error.
type errorWriter struct{}

func (*errorWriter) Write(p []byte) (int, error) {
	return 0, fmt.Errorf("write error")
}
