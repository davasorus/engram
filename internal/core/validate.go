package core

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ErrInvalidInput marks an error as caused by bad caller input, rather than
// a storage or embedding failure. Adapters (REST, MCP) use this to choose a
// 400-class response instead of a 500-class one.
var ErrInvalidInput = errors.New("invalid input")

// ErrNotFound marks an error as "the requested note does not exist".
// Adapters use this to choose a 404-class response.
var ErrNotFound = errors.New("not found")

// notFoundErr wraps msg so errors.Is(err, ErrNotFound) is true.
type notFoundErr struct{ msg string }

func (e *notFoundErr) Error() string        { return e.msg }
func (e *notFoundErr) Is(target error) bool { return target == ErrNotFound }

// notFoundf builds a not-found error with a formatted message.
func notFoundf(format string, args ...any) error {
	return &notFoundErr{msg: fmt.Sprintf(format, args...)}
}

// IsNotFound reports whether err represents a missing note.
func IsNotFound(err error) bool {
	return errors.Is(err, ErrNotFound)
}

// invalidInput wraps msg so errors.Is(err, ErrInvalidInput) is true, while
// still rendering as msg to callers and logs.
type invalidInput struct{ msg string }

func (e *invalidInput) Error() string        { return e.msg }
func (e *invalidInput) Is(target error) bool { return target == ErrInvalidInput }

// invalidf builds a validation error with a formatted message.
func invalidf(format string, args ...any) error {
	return &invalidInput{msg: fmt.Sprintf(format, args...)}
}

// IsInvalidInput reports whether err represents a caller input problem.
func IsInvalidInput(err error) bool {
	return errors.Is(err, ErrInvalidInput)
}

// Limits bound the size and shape of caller input. They exist to keep a
// single bad request from producing an oversized row, an unbounded query,
// or an ID that breaks routing or storage.
const (
	MaxTitleLen   = 500             // runes
	MaxBodyLen    = 2 * 1024 * 1024 // bytes
	MaxTags       = 32              // count
	MaxTagLen     = 64              // runes per tag
	MaxIDLen      = 200             // runes
	MaxProjectLen = 100             // runes

	DefaultSearchLimit = 10
	MaxSearchLimit     = 200
	MaxListLimit       = 500
)

// idRe allows slug-like IDs, optionally namespaced by a project segment
// ("project/slug"). It rejects path-traversal segments and empty segments.
var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(/[A-Za-z0-9][A-Za-z0-9._-]*)*$`)

// validateID checks a caller-supplied ID (path segment, wikilink target, or
// explicit WriteInput.ID). Empty is rejected; callers that treat an ID as
// optional must check for emptiness first.
func validateID(id string) error {
	if strings.TrimSpace(id) == "" {
		return invalidf("id is required")
	}
	if len([]rune(id)) > MaxIDLen {
		return invalidf("id exceeds %d characters", MaxIDLen)
	}
	if strings.Contains(id, "..") {
		return invalidf("id must not contain \"..\"")
	}
	if !idRe.MatchString(id) {
		return invalidf("id must contain only letters, digits, '.', '_', '-', and '/' as a segment separator")
	}
	return nil
}

// validateWriteInput checks a WriteInput before it reaches the store.
func validateWriteInput(in WriteInput) error {
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return invalidf("title is required")
	}
	if len([]rune(title)) > MaxTitleLen {
		return invalidf("title exceeds %d characters", MaxTitleLen)
	}
	if len(in.Body) > MaxBodyLen {
		return invalidf("body exceeds %d bytes", MaxBodyLen)
	}
	if in.ID != "" {
		if err := validateID(in.ID); err != nil {
			return invalidf("invalid id: %s", err.Error())
		}
	}
	if in.Project != "" {
		if len([]rune(in.Project)) > MaxProjectLen {
			return invalidf("project exceeds %d characters", MaxProjectLen)
		}
		if err := validateID(in.Project); err != nil {
			return invalidf("invalid project: %s", err.Error())
		}
	}
	if len(in.Tags) > MaxTags {
		return invalidf("too many tags (max %d)", MaxTags)
	}
	for _, t := range in.Tags {
		if strings.TrimSpace(t) == "" {
			return invalidf("tags must not be empty")
		}
		if len([]rune(t)) > MaxTagLen {
			return invalidf("tag %q exceeds %d characters", t, MaxTagLen)
		}
	}
	return nil
}

// clampLimit normalizes a caller-supplied limit: non-positive falls back to
// def, and anything above max is capped to max, so a single request cannot
// force an unbounded scan or result set.
func clampLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

// clampOffset normalizes a caller-supplied offset: negative becomes 0.
func clampOffset(offset int) int {
	if offset < 0 {
		return 0
	}
	return offset
}
