// Command engram runs the memory service: one process serving MCP, a REST API,
// and a web UI over a Postgres/pgvector-backed store, with embeddings from an
// OpenAI-compatible endpoint (LM Studio by default).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/davasorus/engram/internal/complete"
	"github.com/davasorus/engram/internal/config"
	"github.com/davasorus/engram/internal/core"
	"github.com/davasorus/engram/internal/embed"
	emcp "github.com/davasorus/engram/internal/mcp"
	"github.com/davasorus/engram/internal/rest"
	"github.com/davasorus/engram/internal/store"
	"github.com/davasorus/engram/internal/web"
)

// version info, stamped by GoReleaser via -ldflags at release build time.
var (
	version = "dev"
	commit  = "none"
)

// closableStore is a core.Store that can be closed (store.Postgres in
// production, a test double under test).
type closableStore interface {
	core.Store
	Close() error
}

// deps are the process-level side effects run() performs. Production wires
// real implementations in defaultDeps; tests inject fakes so the whole
// startup path is exercised without Postgres, a listener, or real sleeps.
type deps struct {
	openStore func(ctx context.Context, dsn string, dims int, pc store.PoolConfig) (closableStore, error)
	sleep     func(time.Duration)
	stdio     func(ctx context.Context, s *mcp.Server) error
	serve     func(srv *http.Server) error
}

func defaultDeps() deps {
	return deps{
		openStore: func(ctx context.Context, dsn string, dims int, pc store.PoolConfig) (closableStore, error) {
			return store.OpenWithPool(ctx, dsn, dims, pc)
		},
		sleep: time.Sleep,
		stdio: func(ctx context.Context, s *mcp.Server) error {
			return s.Run(ctx, &mcp.StdioTransport{})
		},
		serve: func(srv *http.Server) error { return srv.ListenAndServe() },
	}
}

const (
	dbConnectAttempts = 30
	dbConnectDelay    = 2 * time.Second
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, defaultDeps()))
}

// run is the testable body of main: it returns the process exit code.
func run(ctx context.Context, args []string, stdout io.Writer, d deps) int {
	// A recognized subcommand as the first argument runs the CLI client
	// against a running server instead of starting one; this must be
	// checked before flags are parsed, and before any server-only flag
	// (like -dsn) is defined, so "engram search foo" works without
	// colliding with the server's own flag set.
	if len(args) > 0 && isCLICommand(args[0]) {
		return runCLI(ctx, stdout, args)
	}

	fs := flag.NewFlagSet("engram", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg := config.DefineFlags(fs)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		log.Printf("engram: %v", err)
		return 2
	}
	cfg.Resolve()

	if cfg.Healthcheck {
		return runHealthcheck(cfg.Addr)
	}

	// Log the effective config up front (no secrets) so a misconfigured
	// container explains itself instead of just dialing the wrong address.
	log.Printf("engram starting: db=%s embed=%s model=%s dims=%d addr=%s",
		redactDSN(cfg.DSN), strings.Join(cfg.EmbedURLs, ","), cfg.EmbedModel, cfg.Dims, cfg.Addr)

	st, err := openStoreWithRetry(ctx, d, cfg)
	if err != nil {
		log.Printf("open store: %v\n%s", err, deployHint)
		return 1
	}
	defer func() { _ = st.Close() }()

	eng := newEngine(st, cfg)
	mcpAdapter := emcp.New(eng, cfg.MCPTools)

	// Stdio mode: expose ONLY the MCP server over stdin/stdout (for clients
	// that spawn the process as a subprocess).
	if cfg.Stdio {
		log.Printf("engram: MCP over stdio (dsn=%s)", redactDSN(cfg.DSN))
		if err := d.stdio(ctx, mcpAdapter.Server()); err != nil {
			log.Printf("stdio: %v", err)
			return 1
		}
		return 0
	}

	// HTTP mode: MCP (streamable) + REST + web UI on one listener.
	handler, err := buildHandler(eng, mcpAdapter)
	if err != nil {
		log.Printf("web: %v", err)
		return 1
	}
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           logRequests(handler),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("engram %s (%s): listening on %s  (dsn set, embed=%s/%s)", version, commit, cfg.Addr, strings.Join(cfg.EmbedURLs, ","), cfg.EmbedModel)
	log.Printf("  UI:   http://localhost%s/", cfg.Addr)
	log.Printf("  MCP:  http://localhost%s/mcp/", cfg.Addr)
	log.Printf("  REST: http://localhost%s/api/", cfg.Addr)
	if err := d.serve(srv); err != nil {
		log.Printf("serve: %v", err)
		return 1
	}
	return 0
}

// openStoreWithRetry connects to Postgres, retrying briefly because the
// database may still be starting when engram launches (compose ordering).
func openStoreWithRetry(ctx context.Context, d deps, cfg *config.Config) (closableStore, error) {
	poolCfg := store.PoolConfig{
		MaxConns:        cfg.MaxConns,
		MinConns:        cfg.MinConns,
		MaxConnLifetime: cfg.ConnMaxLifetime,
		MaxConnIdleTime: cfg.ConnMaxIdleTime,
	}
	if poolCfg.MaxConns > 0 || poolCfg.MinConns > 0 || poolCfg.MaxConnLifetime > 0 || poolCfg.MaxConnIdleTime > 0 {
		log.Printf("engram: db pool tuning: max-conns=%d min-conns=%d conn-max-lifetime=%s conn-max-idle-time=%s",
			poolCfg.MaxConns, poolCfg.MinConns, poolCfg.MaxConnLifetime, poolCfg.MaxConnIdleTime)
	}
	var lastErr error
	for attempt := 1; attempt <= dbConnectAttempts; attempt++ {
		st, err := d.openStore(ctx, cfg.DSN, cfg.Dims, poolCfg)
		if err == nil {
			return st, nil
		}
		lastErr = err
		if attempt == 1 {
			log.Print(deployHint)
		}
		log.Printf("waiting for postgres (attempt %d/%d): %v", attempt, dbConnectAttempts, err)
		d.sleep(dbConnectDelay)
	}
	return nil, lastErr
}

// newEngine builds the core engine, enabling summarization when a
// completion endpoint is configured.
func newEngine(st core.Store, cfg *config.Config) *core.Engine {
	eng := core.NewEngine(st, embed.NewWithFallback(cfg.EmbedURLs, cfg.EmbedModel))
	if len(cfg.CompleteURLs) > 0 {
		eng = eng.WithSummarizer(complete.NewWithFallback(cfg.CompleteURLs, cfg.CompleteModel))
		log.Printf("engram: summarization enabled (complete=%s model=%s)", strings.Join(cfg.CompleteURLs, ","), cfg.CompleteModel)
	}
	return eng
}

// buildHandler mounts MCP, REST and the web UI on one mux.
func buildHandler(eng *core.Engine, mcpAdapter *emcp.Adapter) (http.Handler, error) {
	webUI, err := web.New(eng)
	if err != nil {
		return nil, err
	}
	root := http.NewServeMux()
	root.Handle("/mcp", mcpAdapter.HTTPHandler())
	root.Handle("/mcp/", mcpAdapter.HTTPHandler())
	root.Handle("/api/", rest.New(eng).Routes())
	root.Handle("/", webUI.Routes())
	return root, nil
}

// runHealthcheck probes the local health endpoint; used as the container
// HEALTHCHECK since the distroless image has no shell or curl.
func runHealthcheck(addr string) int {
	host := addr
	if len(host) > 0 && host[0] == ':' {
		host = "127.0.0.1" + host
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + host + "/api/health")
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// logRequests logs each request method, path, and duration. It uses a
// buffered channel so the HTTP handler never blocks on log output — under
// load the channel may drop entries rather than stall the response.
func logRequests(next http.Handler) http.Handler {
	ch := make(chan string, 64)
	go func() {
		for msg := range ch {
			log.Print(msg)
		}
	}()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		msg := fmt.Sprintf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
		select {
		case ch <- msg:
		default:
			// Channel full — drop the log entry rather than block.
		}
	})
}

// redactDSN hides the password in a postgres URL for logging.
// word REDACTED rather than a symbol like "****" — url.UserPassword followed
// by .String() percent-encodes reserved userinfo characters (including '*'),
// so a symbol placeholder came out as "%2A%2A%2A%2A" in the log instead of
// "****". A plain word has no characters requiring escaping.
func redactDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return dsn
	}
	if _, has := u.User.Password(); has {
		u.User = url.UserPassword(u.User.Username(), "REDACTED")
	}
	return u.String()
}
