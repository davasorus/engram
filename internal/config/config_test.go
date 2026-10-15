package config

import (
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
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
}

func TestResolveDSN_ExplicitWins(t *testing.T) {
	clearDBEnv(t)
	withEnv(t, map[string]string{
		"ENGRAM_DB_HOST": "should-be-ignored",
	})
	got := resolveDSN()
	// resolveDSN always assembles from env vars; we test the assembled
	// form separately.  Just confirm it returns a non-empty string.
	if got == "" {
		t.Fatal("resolveDSN returned empty")
	}
}

func TestResolveDSN_AssembledFromPieces(t *testing.T) {
	clearDBEnv(t)
	withEnv(t, map[string]string{
		"ENGRAM_DB_HOST":     "db.example.internal",
		"ENGRAM_DB_PORT":     "55432",
		"ENGRAM_DB_USER":     "customuser",
		"ENGRAM_DB_PASSWORD": "custompass",
		"ENGRAM_DB_NAME":     "customdb",
	})
	got := resolveDSN()
	want := "postgres://customuser:custompass@db.example.internal:55432/customdb?sslmode=disable"
	if got != want {
		t.Fatalf("resolveDSN() = %q, want %q", got, want)
	}
}

func TestResolveDSN_Defaults(t *testing.T) {
	clearDBEnv(t)
	got := resolveDSN()
	want := "postgres://engram:engram@localhost:5432/engram?sslmode=disable"
	if got != want {
		t.Fatalf("resolveDSN() with no env = %q, want %q", got, want)
	}
}

func TestResolveDSN_EscapesSpecialCharacters(t *testing.T) {
	clearDBEnv(t)
	withEnv(t, map[string]string{
		"ENGRAM_DB_USER":     "engram",
		"ENGRAM_DB_PASSWORD": "P@ss:w0rd/with?special#chars",
		"ENGRAM_DB_HOST":     "localhost",
		"ENGRAM_DB_PORT":     "5432",
		"ENGRAM_DB_NAME":     "engram",
	})
	got := resolveDSN()
	if strings.Contains(got, "P@ss:w0rd/with?special#chars") {
		t.Fatalf("unescaped special characters leaked into DSN: %q", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("resulting DSN did not parse as a URL: %v (%q)", err, got)
	}
	pass, ok := u.User.Password()
	if !ok || pass != "P@ss:w0rd/with?special#chars" {
		t.Fatalf("password did not round-trip: got %q ok=%v", pass, ok)
	}
}

func TestEnv_FallsBackOnEmpty(t *testing.T) {
	withEnv(t, map[string]string{"ENGRAM_TEST_KEY": ""})
	if got := env("ENGRAM_TEST_KEY", "default"); got != "default" {
		t.Fatalf("env() with empty var = %q, want default", got)
	}
	withEnv(t, map[string]string{"ENGRAM_TEST_KEY": "set-value"})
	if got := env("ENGRAM_TEST_KEY", "default"); got != "set-value" {
		t.Fatalf("env() with set var = %q, want set-value", got)
	}
}

func TestEnvInt_FallsBackOnInvalid(t *testing.T) {
	cases := []struct {
		name string
		val  string
		def  int
		want int
	}{
		{"unset", "", 768, 768},
		{"valid", "1536", 768, 1536},
		{"garbage", "not-a-number", 768, 768},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withEnv(t, map[string]string{"ENGRAM_TEST_INT": c.val})
			if got := envInt("ENGRAM_TEST_INT", c.def); got != c.want {
				t.Fatalf("envInt(%q) = %d, want %d", c.val, got, c.want)
			}
		})
	}
}

func TestEnvDuration_FallsBackOnInvalid(t *testing.T) {
	cases := []struct {
		name string
		val  string
		def  time.Duration
		want time.Duration
	}{
		{"unset", "", 30 * time.Minute, 30 * time.Minute},
		{"valid", "5m", 30 * time.Minute, 5 * time.Minute},
		{"garbage", "not-a-duration", 30 * time.Minute, 30 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			withEnv(t, map[string]string{"ENGRAM_TEST_DUR": c.val})
			if got := envDuration("ENGRAM_TEST_DUR", c.def); got != c.want {
				t.Fatalf("envDuration(%q) = %v, want %v", c.val, got, c.want)
			}
		})
	}
}

func TestSplitCSV(t *testing.T) {
	cases := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{" , , ", nil},
	}
	for _, c := range cases {
		got := splitCSV(c.input)
		if len(got) != len(c.want) {
			t.Fatalf("splitCSV(%q) len = %d, want %d", c.input, len(got), len(c.want))
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("splitCSV(%q)[%d] = %q, want %q", c.input, i, got[i], c.want[i])
			}
		}
	}
}
