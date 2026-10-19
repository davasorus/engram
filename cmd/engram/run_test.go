package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davasorus/engram/internal/store"
)

// closeMem adds Close to the package's in-memory store so it satisfies
// closableStore.
type closeMem struct {
	*memStore
	closed *bool
}

func (c closeMem) Close() error { *c.closed = true; return nil }

// testDeps returns deps whose side effects are all fakes. opens counts
// openStore calls; sleeps counts retry sleeps.
type testDeps struct {
	deps
	opens, sleeps *int
	closed        *bool
	servedSrv     **http.Server
}

func newTestDeps(failOpens int) testDeps {
	opens, sleeps, closed := new(int), new(int), new(bool)
	var served *http.Server
	td := testDeps{opens: opens, sleeps: sleeps, closed: closed, servedSrv: &served}
	td.deps = deps{
		openStore: func(_ context.Context, _ string, _ int, _ store.PoolConfig) (closableStore, error) {
			*opens++
			if *opens <= failOpens {
				return nil, errors.New("db not ready")
			}
			return closeMem{newMem(), closed}, nil
		},
		sleep: func(time.Duration) { *sleeps++ },
		stdio: func(context.Context, *mcp.Server) error { return nil },
		serve: func(srv *http.Server) error { served = srv; return nil },
	}
	return td
}

func TestRun_CLISubcommandDispatch(t *testing.T) {
	var out bytes.Buffer
	if code := run(context.Background(), []string{"help"}, &out, newTestDeps(0).deps); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if out.Len() == 0 {
		t.Fatal("expected CLI help output")
	}
}

func TestRun_BadFlagExits2(t *testing.T) {
	td := newTestDeps(0)
	if code := run(context.Background(), []string{"-bogus"}, &bytes.Buffer{}, td.deps); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if *td.opens != 0 {
		t.Fatal("store must not be opened on a flag error")
	}
}

func TestRun_HelpFlagExits0(t *testing.T) {
	if code := run(context.Background(), []string{"-h"}, &bytes.Buffer{}, newTestDeps(0).deps); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
}

func TestRun_Healthcheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	td := newTestDeps(0)
	if code := run(context.Background(), []string{"-healthcheck", "-addr", addr}, &bytes.Buffer{}, td.deps); code != 0 {
		t.Fatalf("healthy exit = %d, want 0", code)
	}
	if *td.opens != 0 {
		t.Fatal("healthcheck must not open the store")
	}

	// A port with nothing listening fails the probe.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := l.Addr().String()
	_ = l.Close()
	if code := run(context.Background(), []string{"-healthcheck", "-addr", dead}, &bytes.Buffer{}, td.deps); code != 1 {
		t.Fatalf("unhealthy exit = %d, want 1", code)
	}
}

func TestRun_StoreRetriesThenGivesUp(t *testing.T) {
	td := newTestDeps(1 << 30)
	if code := run(context.Background(), nil, &bytes.Buffer{}, td.deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if *td.opens != dbConnectAttempts {
		t.Fatalf("opens = %d, want %d", *td.opens, dbConnectAttempts)
	}
	if *td.sleeps != dbConnectAttempts {
		t.Fatalf("sleeps = %d, want %d", *td.sleeps, dbConnectAttempts)
	}
	if *td.servedSrv != nil {
		t.Fatal("server must not start without a store")
	}
}

func TestRun_StoreRecoversAfterRetries(t *testing.T) {
	td := newTestDeps(2)
	t.Setenv("ENGRAM_DB_MAX_CONNS", "7") // also exercises the pool-tuning log path
	if code := run(context.Background(), nil, &bytes.Buffer{}, td.deps); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if *td.opens != 3 || *td.sleeps != 2 {
		t.Fatalf("opens=%d sleeps=%d, want 3 and 2", *td.opens, *td.sleeps)
	}
	if !*td.closed {
		t.Fatal("store must be closed on exit")
	}
}

func TestRun_StdioMode(t *testing.T) {
	td := newTestDeps(0)
	called := false
	td.stdio = func(context.Context, *mcp.Server) error { called = true; return nil }
	if code := run(context.Background(), []string{"-stdio"}, &bytes.Buffer{}, td.deps); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	if !called {
		t.Fatal("stdio transport was not started")
	}
	if *td.servedSrv != nil {
		t.Fatal("stdio mode must not start the HTTP server")
	}
}

func TestRun_StdioError(t *testing.T) {
	td := newTestDeps(0)
	td.stdio = func(context.Context, *mcp.Server) error { return errors.New("pipe closed") }
	if code := run(context.Background(), []string{"-stdio"}, &bytes.Buffer{}, td.deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRun_ServeError(t *testing.T) {
	td := newTestDeps(0)
	td.serve = func(*http.Server) error { return errors.New("address in use") }
	if code := run(context.Background(), nil, &bytes.Buffer{}, td.deps); code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
}

func TestRun_HTTPRoutesWired(t *testing.T) {
	t.Setenv("ENGRAM_COMPLETE_URL", "http://127.0.0.1:1") // enables the summarizer branch
	t.Setenv("ENGRAM_COMPLETE_MODEL", "m")
	t.Setenv("ENGRAM_MCP_TOOLS", "memory_search")
	td := newTestDeps(0)
	if code := run(context.Background(), []string{"-addr", ":0"}, &bytes.Buffer{}, td.deps); code != 0 {
		t.Fatalf("exit = %d, want 0", code)
	}
	srv := *td.servedSrv
	if srv == nil {
		t.Fatal("serve was not called")
	}
	if srv.ReadHeaderTimeout == 0 {
		t.Fatal("server must set ReadHeaderTimeout")
	}

	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/health", http.StatusOK}, // REST
		{"/", http.StatusOK},           // web UI
		{"/stats", http.StatusOK},      // web UI
	} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.want {
			t.Errorf("GET %s = %d, want %d", tc.path, rec.Code, tc.want)
		}
	}

	// MCP is mounted: a plain GET without the streamable-HTTP headers is
	// rejected by the MCP handler rather than falling through to the web UI's 404.
	for _, p := range []string{"/mcp", "/mcp/"} {
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, p, nil))
		if rec.Code == http.StatusOK || rec.Code == http.StatusNotFound {
			t.Errorf("GET %s = %d, want an MCP-handler rejection", p, rec.Code)
		}
	}
}

func TestDefaultDeps_Wired(t *testing.T) {
	d := defaultDeps()
	if d.openStore == nil || d.sleep == nil || d.stdio == nil || d.serve == nil {
		t.Fatal("defaultDeps left a dependency nil")
	}
	// A bad DSN must surface as an error, not a panic or a nil store.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if st, err := d.openStore(ctx, "postgres://u:p@127.0.0.1:1/x?sslmode=disable&connect_timeout=1", 4, store.PoolConfig{}); err == nil {
		_ = st.Close()
		t.Fatal("expected an error opening a store at a dead address")
	}
	// Serving on an invalid address returns an error immediately.
	if err := d.serve(&http.Server{Addr: "bad::addr"}); err == nil {
		t.Fatal("expected ListenAndServe to fail on an invalid address")
	}
}
