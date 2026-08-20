package main

import (
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

func TestResolveDSN_ExplicitWins(t *testing.T) {
	clearDBEnv(t)
	withEnv(t, map[string]string{
		"ENGRAM_DB_HOST": "should-be-ignored",
	})
	got := resolveDSN("postgres://explicit:dsn@example.com:5432/db?sslmode=disable")
	want := "postgres://explicit:dsn@example.com:5432/db?sslmode=disable"
	if got != want {
		t.Fatalf("resolveDSN(explicit) = %q, want %q", got, want)
	}
}

// TestResolveDSN_AssembledFromPieces is the regression test for the actual
// incident: an older binary build silently ignored ENGRAM_DB_* entirely and
// fell back to a hardcoded "localhost:5432" DSN even when the ConfigMap set
// a different host/port. That failure mode showed up as a confusing
// "password authentication failed" against the WRONG port, which took a
// long debugging session to trace to a stale image rather than a config
// bug. This test pins down that every piece — not just some — is honored.
func TestResolveDSN_AssembledFromPieces(t *testing.T) {
	clearDBEnv(t)
	withEnv(t, map[string]string{
		"ENGRAM_DB_HOST":     "db.example.internal",
		"ENGRAM_DB_PORT":     "55432", // deliberately non-default, per the incident
		"ENGRAM_DB_USER":     "customuser",
		"ENGRAM_DB_PASSWORD": "custompass",
		"ENGRAM_DB_NAME":     "customdb",
	})
	got := resolveDSN("")
	want := "postgres://customuser:custompass@db.example.internal:55432/customdb?sslmode=disable"
	if got != want {
		t.Fatalf("resolveDSN(\"\") = %q, want %q", got, want)
	}
}

func TestResolveDSN_Defaults(t *testing.T) {
	clearDBEnv(t)
	got := resolveDSN("")
	want := "postgres://engram:engram@localhost:5432/engram?sslmode=disable"
	if got != want {
		t.Fatalf("resolveDSN(\"\") with no env = %q, want %q", got, want)
	}
}

// TestResolveDSN_EscapesSpecialCharacters guards against a password (or
// username) containing characters that are meaningful in a URL — '@', ':',
// '/', '?', '#', etc. — corrupting the DSN's structure. A generated password
// like the ones this project's deploy docs suggest (openssl rand -hex is
// safe, but a person hand-typing "P@ssw0rd" is a realistic case seen this
// session) must round-trip through net/url correctly.
func TestResolveDSN_EscapesSpecialCharacters(t *testing.T) {
	clearDBEnv(t)
	withEnv(t, map[string]string{
		"ENGRAM_DB_USER":     "engram",
		"ENGRAM_DB_PASSWORD": "P@ss:w0rd/with?special#chars",
		"ENGRAM_DB_HOST":     "localhost",
		"ENGRAM_DB_PORT":     "5432",
		"ENGRAM_DB_NAME":     "engram",
	})
	got := resolveDSN("")
	// The raw password must NOT appear unescaped in the DSN (it would break
	// URL parsing downstream in pgx), but it must be present in escaped form.
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
