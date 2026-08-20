// Command engram runs the memory service: one process serving MCP, a REST API,
// and a web UI over a single SQLite-backed store, with embeddings from an
// OpenAI-compatible endpoint (LM Studio by default).
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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
	var (
		dsn        = flag.String("dsn", env("ENGRAM_DSN", "postgres://engram:engram@localhost:5432/engram?sslmode=disable"), "Postgres connection string")
		dims       = flag.Int("dims", envInt("ENGRAM_DIMS", 768), "embedding vector dimensionality (must match the embed model)")
		addr       = flag.String("addr", env("ENGRAM_ADDR", ":8088"), "HTTP listen address")
		embedURL   = flag.String("embed-url", env("ENGRAM_EMBED_URL", "http://127.0.0.1:1234"), "OpenAI-compatible embeddings base URL")
		embedModel = flag.String("embed-model", env("ENGRAM_EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5"), "embedding model id")
		stdio      = flag.Bool("stdio", false, "run the MCP server over stdio instead of HTTP")
		healthck   = flag.Bool("healthcheck", false, "probe the local /api/health endpoint and exit 0/1 (for container HEALTHCHECK)")
	)
	flag.Parse()

	if *healthck {
		os.Exit(runHealthcheck(*addr))
	}

	ctx := context.Background()

	// Postgres may still be starting when engram launches (compose ordering),
	// so retry the initial connection briefly rather than crashing.
	var st *store.Postgres
	var err error
	for attempt := 1; attempt <= 30; attempt++ {
		st, err = store.Open(ctx, *dsn, *dims)
		if err == nil {
			break
		}
		log.Printf("waiting for postgres (attempt %d/30): %v", attempt, err)
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer func() { _ = st.Close() }()

	emb := embed.New(*embedURL, *embedModel)
	eng := core.NewEngine(st, emb)
	mcpAdapter := emcp.New(eng)

	// Stdio mode: expose ONLY the MCP server over stdin/stdout (for clients
	// that spawn the process as a subprocess).
	if *stdio {
		log.Printf("engram: MCP over stdio (dsn=%s)", *dsn)
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
		Addr:              *addr,
		Handler:           logRequests(root),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("engram %s (%s): listening on %s  (dsn set, embed=%s/%s)", version, commit, *addr, *embedURL, *embedModel)
	log.Printf("  UI:   http://localhost%s/", *addr)
	log.Printf("  MCP:  http://localhost%s/mcp/", *addr)
	log.Printf("  REST: http://localhost%s/api/", *addr)
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

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
