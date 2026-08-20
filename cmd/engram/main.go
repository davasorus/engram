// Command engram runs the memory service: one process serving MCP, a REST API,
// and a web UI over a Postgres/pgvector-backed store, with embeddings from an
// OpenAI-compatible endpoint (LM Studio by default).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
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
		dsn        = flag.String("dsn", env("ENGRAM_DSN", ""), "Postgres connection string (overrides the ENGRAM_DB_* pieces)")
		dims       = flag.Int("dims", envInt("ENGRAM_DIMS", 768), "embedding vector dimensionality (must match the embed model)")
		addr       = flag.String("addr", env("ENGRAM_ADDR", ":8088"), "HTTP listen address")
		embedURL   = flag.String("embed-url", env("ENGRAM_EMBED_URL", "http://127.0.0.1:1234"), "OpenAI-compatible embeddings base URL")
		embedModel = flag.String("embed-model", env("ENGRAM_EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5"), "embedding model id")
		mcpTools   = flag.String("mcp-tools", env("ENGRAM_MCP_TOOLS", ""), "comma-separated MCP tool allowlist, e.g. mem_search,mem_read,mem_write (empty = all tools)")
		stdio      = flag.Bool("stdio", false, "run the MCP server over stdio instead of HTTP")
		healthck   = flag.Bool("healthcheck", false, "probe the local /api/health endpoint and exit 0/1 (for container HEALTHCHECK)")
	)
	flag.Parse()

	// ENGRAM_DSN wins when set; otherwise assemble it from discrete pieces so
	// the password can arrive via a real secret (kube secretKeyRef, podman
	// secret) instead of living inside a connection string in a manifest.
	*dsn = resolveDSN(*dsn)

	if *healthck {
		os.Exit(runHealthcheck(*addr))
	}

	ctx := context.Background()

	// Postgres may still be starting when engram launches (compose ordering),
	// so retry the initial connection briefly rather than crashing.
	var st *store.Postgres
	var err error
	// Log the effective config up front (no secrets) so a misconfigured
	// container explains itself instead of just dialing the wrong address.
	log.Printf("engram starting: db=%s embed=%s model=%s dims=%d addr=%s",
		redactDSN(*dsn), *embedURL, *embedModel, *dims, *addr)

	for attempt := 1; attempt <= 30; attempt++ {
		st, err = store.Open(ctx, *dsn, *dims)
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

	emb := embed.New(*embedURL, *embedModel)
	eng := core.NewEngine(st, emb)
	var allowedTools []string
	if *mcpTools != "" {
		allowedTools = strings.Split(*mcpTools, ",")
	}
	mcpAdapter := emcp.New(eng, allowedTools)

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

// resolveDSN returns explicit if non-empty (the ENGRAM_DSN flag/env case);
// otherwise it assembles a DSN from the discrete ENGRAM_DB_* pieces. This is
// what lets a kube Secret hold only the password (via secretKeyRef) while
// the rest of the connection info stays in a plain ConfigMap — no full
// connection string, password included, needs to live in a manifest.
// Pulled into its own function (rather than left inline in main) so it has
// direct test coverage: this exact assembly was the point of failure in a
// real incident (an older binary silently ignored ENGRAM_DB_* and fell back
// to its own hardcoded default DSN instead of erroring, which produced a
// confusing "password authentication failed" against the wrong port for a
// long time before the mismatch was traced to a stale image).
func resolveDSN(explicit string) string {
	if explicit != "" {
		return explicit
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(env("ENGRAM_DB_USER", "engram")),
		url.QueryEscape(env("ENGRAM_DB_PASSWORD", "engram")),
		env("ENGRAM_DB_HOST", "localhost"),
		env("ENGRAM_DB_PORT", "5432"),
		env("ENGRAM_DB_NAME", "engram"))
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

// deployHint is printed when the database is unreachable — the most common
// state for someone who ran the bare image with no configuration. It has to
// carry them to a working deployment on its own, because a `podman run` user
// has no compose file, no .env, and no repo checked out.
const deployHint = `
engram needs a pgvector-enabled Postgres. This image does not bundle one.

  Required env (one of):
    ENGRAM_DSN           full postgres:// connection string, or
    ENGRAM_DB_HOST/PORT/USER/PASSWORD/NAME   discrete pieces (password can
                         then come from a real secret, not a string in git)
  Plus:
    ENGRAM_EMBED_URL     OpenAI-compatible embeddings endpoint (e.g. LM Studio)

  Kube deploy, no clone (secret is generated locally, never committed;
  all tunables live in the ConfigMap at the top of the manifest):
    curl -LO https://github.com/davasorus/engram/releases/latest/download/engram-kube.yaml
    curl -LO https://github.com/davasorus/engram/releases/latest/download/secret.example.yaml
    sed "s/REPLACE_ME/$(openssl rand -hex 20)/" secret.example.yaml | podman kube play -
    $EDITOR engram-kube.yaml    # ConfigMap: embed URL/port, dims, db names
    podman kube play engram-kube.yaml

  Or compose (fetch the two files, edit .env, up):
    curl -LO  https://github.com/davasorus/engram/releases/latest/download/compose.yml
    curl -Lo .env https://github.com/davasorus/engram/releases/latest/download/env.example
    podman-compose up -d

  Docs: https://github.com/davasorus/engram
`

// redactDSN hides the password in a postgres URL for logging. Uses the
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
