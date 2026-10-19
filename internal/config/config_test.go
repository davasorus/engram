package config

import (
	"flag"
	"os"
	"testing"
	"time"
)

func TestEnv(t *testing.T) {
	const key = "TEST_CONFIG_ENV_KEY"

	t.Run("returns env var when set", func(t *testing.T) {
		os.Setenv(key, "from-env")
		defer os.Unsetenv(key)

		got := env(key, "default")
		if got != "from-env" {
			t.Errorf("env(%q, %q) = %q; want %q", key, "default", got, "from-env")
		}
	})

	t.Run("returns default when unset", func(t *testing.T) {
		os.Unsetenv(key)
		got := env(key, "default")
		if got != "default" {
			t.Errorf("env(%q, %q) = %q; want %q", key, "default", got, "default")
		}
	})

	t.Run("returns default when empty", func(t *testing.T) {
		os.Setenv(key, "")
		defer os.Unsetenv(key)
		got := env(key, "default")
		if got != "default" {
			t.Errorf("env(%q, %q) = %q; want %q", key, "default", got, "default")
		}
	})
}

func TestEnvInt(t *testing.T) {
	const key = "TEST_CONFIG_ENVINT_KEY"

	t.Run("returns parsed int when set", func(t *testing.T) {
		os.Setenv(key, "42")
		defer os.Unsetenv(key)

		got := envInt(key, 0)
		if got != 42 {
			t.Errorf("envInt(%q, 0) = %d; want 42", key, got)
		}
	})

	t.Run("returns default when unset", func(t *testing.T) {
		os.Unsetenv(key)
		got := envInt(key, 100)
		if got != 100 {
			t.Errorf("envInt(%q, 100) = %d; want 100", key, got)
		}
	})

	t.Run("returns default when invalid", func(t *testing.T) {
		os.Setenv(key, "not-a-number")
		defer os.Unsetenv(key)
		got := envInt(key, 7)
		if got != 7 {
			t.Errorf("envInt(%q, 7) = %d; want 7", key, got)
		}
	})

	t.Run("returns default when empty", func(t *testing.T) {
		os.Setenv(key, "")
		defer os.Unsetenv(key)
		got := envInt(key, 5)
		if got != 5 {
			t.Errorf("envInt(%q, 5) = %d; want 5", key, got)
		}
	})
}

func TestEnvDuration(t *testing.T) {
	const key = "TEST_CONFIG_ENVDUR_KEY"

	t.Run("returns parsed duration when set", func(t *testing.T) {
		os.Setenv(key, "5m30s")
		defer os.Unsetenv(key)

		got := envDuration(key, 0)
		want := 5*time.Minute + 30*time.Second
		if got != want {
			t.Errorf("envDuration(%q, 0) = %v; want %v", key, got, want)
		}
	})

	t.Run("returns default when unset", func(t *testing.T) {
		os.Unsetenv(key)
		got := envDuration(key, time.Hour)
		if got != time.Hour {
			t.Errorf("envDuration(%q, 1h) = %v; want 1h", key, got)
		}
	})

	t.Run("returns default when invalid", func(t *testing.T) {
		os.Setenv(key, "invalid")
		defer os.Unsetenv(key)
		got := envDuration(key, time.Minute)
		if got != time.Minute {
			t.Errorf("envDuration(%q, 1m) = %v; want 1m", key, got)
		}
	})

	t.Run("returns default when empty", func(t *testing.T) {
		os.Setenv(key, "")
		defer os.Unsetenv(key)
		got := envDuration(key, 10*time.Second)
		if got != 10*time.Second {
			t.Errorf("envDuration(%q, 10s) = %v; want 10s", key, got)
		}
	})
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "single value",
			input: "one",
			want:  []string{"one"},
		},
		{
			name:  "multiple values",
			input: "one,two,three",
			want:  []string{"one", "two", "three"},
		},
		{
			name:  "values with spaces",
			input: " one , two , three ",
			want:  []string{"one", "two", "three"},
		},
		{
			name:  "empty values skipped",
			input: "one,,two",
			want:  []string{"one", "two"},
		},
		{
			name:  "all empty",
			input: ",,,",
			want:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitCSV(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("splitCSV(%q) length = %d; want %d", tt.input, len(got), len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitCSV(%q)[%d] = %q; want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestDefineFlags(t *testing.T) {
	os.Unsetenv("ENGRAM_DSN")
	os.Unsetenv("ENGRAM_DIMS")
	os.Unsetenv("ENGRAM_ADDR")
	os.Unsetenv("ENGRAM_EMBED_MODEL")
	os.Unsetenv("ENGRAM_COMPLETE_MODEL")
	os.Unsetenv("ENGRAM_EMBED_URL")
	os.Unsetenv("ENGRAM_COMPLETE_URL")
	os.Unsetenv("ENGRAM_MCP_TOOLS")
	os.Unsetenv("ENGRAM_DB_MAX_CONNS")
	os.Unsetenv("ENGRAM_DB_MIN_CONNS")
	os.Unsetenv("ENGRAM_DB_CONN_MAX_LIFETIME")
	os.Unsetenv("ENGRAM_DB_CONN_MAX_IDLE_TIME")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := DefineFlags(fs)

	// Verify defaults before parsing
	if c.DSN != "" {
		t.Errorf("DSN default = %q; want empty", c.DSN)
	}
	if c.Dims != 768 {
		t.Errorf("Dims default = %d; want 768", c.Dims)
	}
	if c.Addr != ":8088" {
		t.Errorf("Addr default = %q; want %q", c.Addr, ":8088")
	}
	if c.EmbedModel != "text-embedding-nomic-embed-text-v1.5" {
		t.Errorf("EmbedModel default = %q; want %q", c.EmbedModel, "text-embedding-nomic-embed-text-v1.5")
	}
	if c.CompleteModel != "" {
		t.Errorf("CompleteModel default = %q; want empty", c.CompleteModel)
	}
	if c.MaxConns != 0 {
		t.Errorf("MaxConns default = %d; want 0", c.MaxConns)
	}
	if c.MinConns != 0 {
		t.Errorf("MinConns default = %d; want 0", c.MinConns)
	}
	if c.ConnMaxLifetime != 0 {
		t.Errorf("ConnMaxLifetime default = %v; want 0", c.ConnMaxLifetime)
	}
	if c.ConnMaxIdleTime != 0 {
		t.Errorf("ConnMaxIdleTime default = %v; want 0", c.ConnMaxIdleTime)
	}

	// Verify flags are registered
	err := fs.Parse([]string{
		"-dsn", "postgres://test:pass@localhost:5432/testdb",
		"-dims", "1536",
		"-addr", "localhost:9090",
		"-embed-model", "custom-embed-model",
		"-complete-model", "gpt-4",
		"-db-max-conns", "100",
		"-db-min-conns", "10",
		"-db-conn-max-lifetime", "30m",
		"-db-conn-max-idle-time", "10m",
	})
	if err != nil {
		t.Fatalf("flag.Parse() error: %v", err)
	}

	// Resolve must be called after flag.Parse() to finalize config
	c.Resolve()

	if c.DSN != "postgres://test:pass@localhost:5432/testdb" {
		t.Errorf("DSN = %q; want %q", c.DSN, "postgres://test:pass@localhost:5432/testdb")
	}
	if c.Dims != 1536 {
		t.Errorf("Dims = %d; want 1536", c.Dims)
	}
	if c.Addr != "localhost:9090" {
		t.Errorf("Addr = %q; want %q", c.Addr, "localhost:9090")
	}
	if c.EmbedModel != "custom-embed-model" {
		t.Errorf("EmbedModel = %q; want %q", c.EmbedModel, "custom-embed-model")
	}
	if c.CompleteModel != "gpt-4" {
		t.Errorf("CompleteModel = %q; want %q", c.CompleteModel, "gpt-4")
	}
	if c.MaxConns != 100 {
		t.Errorf("MaxConns = %d; want 100", c.MaxConns)
	}
	if c.MinConns != 10 {
		t.Errorf("MinConns = %d; want 10", c.MinConns)
	}
	if c.ConnMaxLifetime != 30*time.Minute {
		t.Errorf("ConnMaxLifetime = %v; want 30m", c.ConnMaxLifetime)
	}
	if c.ConnMaxIdleTime != 10*time.Minute {
		t.Errorf("ConnMaxIdleTime = %v; want 10m", c.ConnMaxIdleTime)
	}
}

func TestResolve(t *testing.T) {
	// Clear all engram env vars
	for _, k := range []string{
		"ENGRAM_DSN", "ENGRAM_DIMS", "ENGRAM_ADDR",
		"ENGRAM_EMBED_MODEL", "ENGRAM_COMPLETE_MODEL",
		"ENGRAM_EMBED_URL", "ENGRAM_COMPLETE_URL",
		"ENGRAM_MCP_TOOLS",
		"ENGRAM_DB_USER", "ENGRAM_DB_PASSWORD",
		"ENGRAM_DB_HOST", "ENGRAM_DB_PORT", "ENGRAM_DB_NAME",
	} {
		os.Unsetenv(k)
	}

	c := &Config{
		DSN:           "",
		EmbedModel:    "",
		CompleteModel: "",
	}

	c.Resolve()

	// DSN should be resolved from defaults
	wantDSN := "postgres://engram:engram@localhost:5432/engram?sslmode=disable"
	if c.DSN != wantDSN {
		t.Errorf("DSN = %q; want %q", c.DSN, wantDSN)
	}

	// EmbedModel should be the default
	if c.EmbedModel != "text-embedding-nomic-embed-text-v1.5" {
		t.Errorf("EmbedModel = %q; want %q", c.EmbedModel, "text-embedding-nomic-embed-text-v1.5")
	}

	// CompleteModel should be empty (no default)
	if c.CompleteModel != "" {
		t.Errorf("CompleteModel = %q; want empty", c.CompleteModel)
	}

	// EmbedURLs should default to http://127.0.0.1:1234
	if len(c.EmbedURLs) != 1 || c.EmbedURLs[0] != "http://127.0.0.1:1234" {
		t.Errorf("EmbedURLs = %v; want [http://127.0.0.1:1234]", c.EmbedURLs)
	}

	// CompleteURLs should be nil (empty default)
	if c.CompleteURLs != nil {
		t.Errorf("CompleteURLs = %v; want nil", c.CompleteURLs)
	}

	// MCPTools should be nil (empty default)
	if c.MCPTools != nil {
		t.Errorf("MCPTools = %v; want nil", c.MCPTools)
	}
}

func TestResolveWithEnv(t *testing.T) {
	// Set all env vars
	os.Setenv("ENGRAM_DSN", "postgres://envuser:envpass@envhost:5433/envdb?sslmode=enable")
	os.Setenv("ENGRAM_EMBED_MODEL", "env-embed-model")
	os.Setenv("ENGRAM_COMPLETE_MODEL", "env-complete-model")
	os.Setenv("ENGRAM_EMBED_URL", "http://embed1:1234,http://embed2:1234")
	os.Setenv("ENGRAM_COMPLETE_URL", "http://complete:8000")
	os.Setenv("ENGRAM_MCP_TOOLS", "tool1,tool2,tool3")
	os.Setenv("ENGRAM_DB_USER", "dbuser")
	os.Setenv("ENGRAM_DB_PASSWORD", "dbpass")
	os.Setenv("ENGRAM_DB_HOST", "dbhost")
	os.Setenv("ENGRAM_DB_PORT", "5433")
	os.Setenv("ENGRAM_DB_NAME", "dbname")
	defer func() {
		for _, k := range []string{
			"ENGRAM_DSN", "ENGRAM_EMBED_MODEL", "ENGRAM_COMPLETE_MODEL",
			"ENGRAM_EMBED_URL", "ENGRAM_COMPLETE_URL",
			"ENGRAM_MCP_TOOLS",
			"ENGRAM_DB_USER", "ENGRAM_DB_PASSWORD",
			"ENGRAM_DB_HOST", "ENGRAM_DB_PORT", "ENGRAM_DB_NAME",
		} {
			os.Unsetenv(k)
		}
	}()

	c := &Config{
		DSN:           "",
		EmbedModel:    "",
		CompleteModel: "",
	}

	c.Resolve()

	if c.DSN != "postgres://envuser:envpass@envhost:5433/envdb?sslmode=enable" {
		t.Errorf("DSN = %q; want postgres://envuser:envpass@envhost:5433/envdb?sslmode=enable", c.DSN)
	}
	if c.EmbedModel != "env-embed-model" {
		t.Errorf("EmbedModel = %q; want env-embed-model", c.EmbedModel)
	}
	if c.CompleteModel != "env-complete-model" {
		t.Errorf("CompleteModel = %q; want env-complete-model", c.CompleteModel)
	}
	if len(c.EmbedURLs) != 2 || c.EmbedURLs[0] != "http://embed1:1234" || c.EmbedURLs[1] != "http://embed2:1234" {
		t.Errorf("EmbedURLs = %v; want [http://embed1:1234 http://embed2:1234]", c.EmbedURLs)
	}
	if len(c.CompleteURLs) != 1 || c.CompleteURLs[0] != "http://complete:8000" {
		t.Errorf("CompleteURLs = %v; want [http://complete:8000]", c.CompleteURLs)
	}
	if len(c.MCPTools) != 3 || c.MCPTools[0] != "tool1" || c.MCPTools[1] != "tool2" || c.MCPTools[2] != "tool3" {
		t.Errorf("MCPTools = %v; want [tool1 tool2 tool3]", c.MCPTools)
	}
}

func TestResolveDSN(t *testing.T) {
	// Clear env vars
	for _, k := range []string{
		"ENGRAM_DB_USER", "ENGRAM_DB_PASSWORD",
		"ENGRAM_DB_HOST", "ENGRAM_DB_PORT", "ENGRAM_DB_NAME",
	} {
		os.Unsetenv(k)
	}

	got := resolveDSN()
	want := "postgres://engram:engram@localhost:5432/engram?sslmode=disable"
	if got != want {
		t.Errorf("resolveDSN() = %q; want %q", got, want)
	}
}

func TestResolveDSNWithEnv(t *testing.T) {
	os.Setenv("ENGRAM_DB_USER", "myuser")
	os.Setenv("ENGRAM_DB_PASSWORD", "mypass")
	os.Setenv("ENGRAM_DB_HOST", "myhost")
	os.Setenv("ENGRAM_DB_PORT", "5433")
	os.Setenv("ENGRAM_DB_NAME", "mydb")
	defer func() {
		for _, k := range []string{
			"ENGRAM_DB_USER", "ENGRAM_DB_PASSWORD",
			"ENGRAM_DB_HOST", "ENGRAM_DB_PORT", "ENGRAM_DB_NAME",
		} {
			os.Unsetenv(k)
		}
	}()

	got := resolveDSN()
	want := "postgres://myuser:mypass@myhost:5433/mydb?sslmode=disable"
	if got != want {
		t.Errorf("resolveDSN() = %q; want %q", got, want)
	}
}

func TestResolveDSNWithSpecialChars(t *testing.T) {
	os.Setenv("ENGRAM_DB_USER", "user@domain")
	os.Setenv("ENGRAM_DB_PASSWORD", "p@ss:w0rd")
	defer func() {
		os.Unsetenv("ENGRAM_DB_USER")
		os.Unsetenv("ENGRAM_DB_PASSWORD")
	}()

	got := resolveDSN()
	// QueryEscape should encode @ as %40 and : as %3A
	if got != "postgres://user%40domain:p%40ss%3Aw0rd@localhost:5432/engram?sslmode=disable" {
		t.Errorf("resolveDSN() = %q; want postgres://user%%40domain:p%%40ss%%3Aw0rd@localhost:5432/engram?sslmode=disable", got)
	}
}

func TestDefineFlagsWithEnvDefaults(t *testing.T) {
	os.Setenv("ENGRAM_DIMS", "1536")
	os.Setenv("ENGRAM_ADDR", "0.0.0.0:9999")
	os.Setenv("ENGRAM_EMBED_MODEL", "env-embed")
	os.Setenv("ENGRAM_COMPLETE_MODEL", "env-complete")
	os.Setenv("ENGRAM_DB_MAX_CONNS", "50")
	os.Setenv("ENGRAM_DB_MIN_CONNS", "5")
	os.Setenv("ENGRAM_DB_CONN_MAX_LIFETIME", "1h")
	os.Setenv("ENGRAM_DB_CONN_MAX_IDLE_TIME", "30m")
	defer func() {
		for _, k := range []string{
			"ENGRAM_DIMS", "ENGRAM_ADDR", "ENGRAM_EMBED_MODEL",
			"ENGRAM_COMPLETE_MODEL", "ENGRAM_DB_MAX_CONNS",
			"ENGRAM_DB_MIN_CONNS", "ENGRAM_DB_CONN_MAX_LIFETIME",
			"ENGRAM_DB_CONN_MAX_IDLE_TIME",
		} {
			os.Unsetenv(k)
		}
	}()

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	c := DefineFlags(fs)
	err := fs.Parse([]string{})
	if err != nil {
		t.Fatalf("flag.Parse() error: %v", err)
	}

	// Resolve must be called after flag.Parse() to finalize config
	c.Resolve()

	if c.Dims != 1536 {
		t.Errorf("Dims = %d; want 1536", c.Dims)
	}
	if c.Addr != "0.0.0.0:9999" {
		t.Errorf("Addr = %q; want 0.0.0.0:9999", c.Addr)
	}
	if c.EmbedModel != "env-embed" {
		t.Errorf("EmbedModel = %q; want env-embed", c.EmbedModel)
	}
	if c.CompleteModel != "env-complete" {
		t.Errorf("CompleteModel = %q; want env-complete", c.CompleteModel)
	}
	if c.MaxConns != 50 {
		t.Errorf("MaxConns = %d; want 50", c.MaxConns)
	}
	if c.MinConns != 5 {
		t.Errorf("MinConns = %d; want 5", c.MinConns)
	}
	if c.ConnMaxLifetime != time.Hour {
		t.Errorf("ConnMaxLifetime = %v; want 1h", c.ConnMaxLifetime)
	}
	if c.ConnMaxIdleTime != 30*time.Minute {
		t.Errorf("ConnMaxIdleTime = %v; want 30m", c.ConnMaxIdleTime)
	}
}
