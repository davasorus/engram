// Package config loads engram's configuration from flags and environment
// variables. It centralizes the env→flag resolution logic so it can be
// tested independently of the server bootstrap path.
package config

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the resolved settings for an engram server instance.
type Config struct {
	DSN             string
	Dims            int
	Addr            string
	EmbedURLs       []string
	EmbedModel      string
	CompleteURLs    []string
	CompleteModel   string
	MCPTools        []string // MCP tool allowlist (empty = all)
	Stdio           bool
	Healthcheck     bool
	MaxConns        int32
	MinConns        int32
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// DefineFlags registers all engram server flags on fs and returns a Config
// placeholder for callers to read after flag.Parse().
func DefineFlags(fs *flag.FlagSet) *Config {
	c := &Config{}
	fs.StringVar(&c.DSN, "dsn", "", "Postgres connection string")
	fs.IntVar(&c.Dims, "dims", envInt("ENGRAM_DIMS", 768), "embedding vector dimensionality")
	fs.StringVar(&c.Addr, "addr", env("ENGRAM_ADDR", ":8088"), "HTTP listen address")
	fs.StringVar(&c.EmbedModel, "embed-model", env("ENGRAM_EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5"), "embedding model id")
	fs.StringVar(&c.CompleteModel, "complete-model", env("ENGRAM_COMPLETE_MODEL", ""), "chat-completion model id")
	fs.BoolVar(&c.Stdio, "stdio", false, "run MCP over stdio")
	fs.BoolVar(&c.Healthcheck, "healthcheck", false, "probe /api/health and exit")
	// flag.FlagSet has no Int32Var; use int then cast.
	var maxConns, minConns int
	fs.IntVar(&maxConns, "db-max-conns", envInt("ENGRAM_DB_MAX_CONNS", 0), "max Postgres pool connections")
	fs.IntVar(&minConns, "db-min-conns", envInt("ENGRAM_DB_MIN_CONNS", 0), "min idle Postgres pool connections")
	c.MaxConns = int32(maxConns)
	c.MinConns = int32(minConns)
	fs.DurationVar(&c.ConnMaxLifetime, "db-conn-max-lifetime", envDuration("ENGRAM_DB_CONN_MAX_LIFETIME", 0), "max lifetime of a pooled connection")
	fs.DurationVar(&c.ConnMaxIdleTime, "db-conn-max-idle-time", envDuration("ENGRAM_DB_CONN_MAX_IDLE_TIME", 0), "max idle time of a pooled connection")
	return c
}

// Resolve applies environment-variable fallbacks and returns the final config.
// Call this after flag.Parse().
func (c *Config) Resolve() {
	if c.DSN == "" {
		c.DSN = resolveDSN()
	}
	if c.EmbedModel == "" {
		c.EmbedModel = env("ENGRAM_EMBED_MODEL", "text-embedding-nomic-embed-text-v1.5")
	}
	if c.CompleteModel == "" {
		c.CompleteModel = env("ENGRAM_COMPLETE_MODEL", "")
	}
	c.EmbedURLs = splitCSV(env("ENGRAM_EMBED_URL", "http://127.0.0.1:1234"))
	c.CompleteURLs = splitCSV(env("ENGRAM_COMPLETE_URL", ""))
	c.MCPTools = splitCSV(env("ENGRAM_MCP_TOOLS", ""))
}

// resolveDSN assembles a DSN from discrete ENGRAM_DB_* env vars.
func resolveDSN() string {
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=disable",
		url.QueryEscape(env("ENGRAM_DB_USER", "engram")),
		url.QueryEscape(env("ENGRAM_DB_PASSWORD", "engram")),
		env("ENGRAM_DB_HOST", "localhost"),
		env("ENGRAM_DB_PORT", "5432"),
		env("ENGRAM_DB_NAME", "engram"))
}

// env returns the value of env key k, or def if empty.
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

func envDuration(k string, def time.Duration) time.Duration {
	if v := os.Getenv(k); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
