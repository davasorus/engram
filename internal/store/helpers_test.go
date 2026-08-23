// Unit tests for pure helpers with no DB dependency — deliberately NOT
// behind the "integration" build tag, unlike postgres_integration_test.go,
// so `go test ./...` (no tags) exercises at least this much of the
// package. Before this file, `internal/store` reported "[no test files]"
// under a plain test run, meaning CI's non-integration jobs (and anyone
// running `go test ./...` locally without a Docker/Podman socket) verified
// nothing in this package at all.
package store

import "testing"

func TestOrEmptyMap(t *testing.T) {
	if got := orEmptyMap(nil); got == nil || len(got) != 0 {
		t.Errorf("orEmptyMap(nil) = %#v, want an empty non-nil map", got)
	}
	in := map[string]any{"k": "v"}
	if got := orEmptyMap(in); got["k"] != "v" || len(got) != 1 {
		t.Errorf("orEmptyMap(non-nil) = %#v, want unchanged %#v", got, in)
	}
	// Frontmatter round-trips through JSON marshal/unmarshal (Upsert/scan);
	// a nil map there would marshal to JSON "null" rather than "{}", which
	// is a real behavior difference for anything consuming the field.
	nonNilEmpty := map[string]any{}
	if got := orEmptyMap(nonNilEmpty); len(got) != 0 {
		t.Errorf("orEmptyMap(non-nil empty) = %#v, want empty", got)
	}
}

func TestOrEmptySlice(t *testing.T) {
	if got := orEmptySlice(nil); got == nil || len(got) != 0 {
		t.Errorf("orEmptySlice(nil) = %#v, want an empty non-nil slice", got)
	}
	in := []string{"a", "b"}
	got := orEmptySlice(in)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("orEmptySlice(non-nil) = %#v, want unchanged %#v", got, in)
	}
	nonNilEmpty := []string{}
	if got := orEmptySlice(nonNilEmpty); len(got) != 0 {
		t.Errorf("orEmptySlice(non-nil empty) = %#v, want empty", got)
	}
}
