// Command engram runs the memory service: one process serving MCP, a REST API,
// and a web UI over a Postgres/pgvector-backed store, with embeddings from an
// OpenAI-compatible endpoint (LM Studio by default).
package main

import (
	"context"
	"flag"
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

func main() {
	// A recognized subcommand as the first argument runs the CLI client
	// against a running server instead of starting one; this must be
	// checked before flag.Parse() touches os.Args, and before any
	// server-only flag (like -dsn) is defined, so "engram search foo"
	// works without colliding with the server's own flag set.
	if len(os.Args) > 1 && isCLICommand(os.Args[1]) {
		os.Exit(runCLI(context.Background(), os.Stdout, os.Args[1:]))
	}

	cfg := config.DefineFlags(flag.CommandLine)
	flag.Parse()
	cfg.Resolve()

	if cfg.Healthcheck {
		os.Exit(runHealthcheck(cfg.Addr))
	}

	ctx := context.Background()

	// Postgres may still be starting when engram launches (compose ordering),
	// so retry the initial connection briefly rather than crashing.
	var st *store.Postgres
	var err error
	// Log the effective config up front (no secrets) so a misconfigured
	// container explains itself instead of just dialing the wrong address.
	log.Printf("engram starting: db=%s embed=%s model=%s dims=%d addr=%s",
		redactDSN(cfg.DSN), strings.Join(cfg.EmbedURLs, ","), cfg.EmbedModel, cfg.Dims, cfg.Addr)

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
	for attempt := 1; attempt <= 30; attempt++ {
		st, err = store.OpenWithPool(ctx, cfg.DSN, cfg.Dims, poolCfg)
		if err == nil {
			break
		}
		if attempt == 1 {
			log.Print(deployHint)
		}
		log.Printf("waiting for postgres (attempt %d/30): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("open store: %v\n%s", err, deployHint)
	}
	defer func() { _ = st.Close() }()

	emb := embed.NewWithFallback(cfg.EmbedURLs, cfg.EmbedModel)
	eng := core.NewEngine(st, emb)
	if len(cfg.CompleteURLs) > 0 {
		comp := complete.NewWithFallback(cfg.CompleteURLs, cfg.CompleteModel)
		eng = eng.WithSummarizer(comp)
		log.Printf("engram: summarization enabled (complete=%s model=%s)", strings.Join(cfg.CompleteURLs, ","), cfg.CompleteModel)
	}
	var allowedTools []string
	if len(cfg.MCPTools) > 0 {
		allowedTools = cfg.MCPTools
	}
	mcpAdapter := emcp.New(eng, allowedTools)

	// Stdio mode: expose ONLY the MCP server over stdin/stdout (for clients
	// that spawn the process as a subprocess).
	if cfg.Stdio {
		log.Printf("engram: MCP over stdio (dsn=%s)", redactDSN(cfg.DSN))
		if err := mcpAdapter.Server().Run(ctx, &mcp.StdioTransport{}); err != nil {
			log.Fatalf("stdio: %v", err)
		}
		return
	}

	// HTTP mode: MCP (streamable) + REST + web UI on one listener.
	restAPI := rest.New(eng)
	webUI, err := web.New(eng)
	if err != nil {
		log.Fatalf("web: %v", err)
	}

	root := http.NewServeMux()
	// MCP under /mcp
	root.Handle("/mcp", mcpAdapter.HTTPHandler())
	root.Handle("/mcp/", mcpAdapter.HTTPHandler())
	// REST under /api (mount its mux)
	root.Handle("/api/", restAPI.Routes())
	// Web UI for everything else
	root.Handle("/", webUI.Routes())

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           logRequests(root),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("engram %s (%s): listening on %s  (dsn set, embed=%s/%s)", version, commit, cfg.Addr, strings.Join(cfg.EmbedURLs, ","), cfg.EmbedModel)
	log.Printf("  UI:   http://localhost%s/", cfg.Addr)
	log.Printf("  MCP:  http://localhost%s/mcp/", cfg.Addr)
	log.Printf("  REST: http://localhost%s/api/", cfg.Addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Fatalf("serve: %v", err)
	}
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

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
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
