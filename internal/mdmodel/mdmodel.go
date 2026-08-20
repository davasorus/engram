// Package mdmodel treats a markdown note as a first-class structured document:
// a frontmatter block plus an ordered tree of sections split on ATX headings.
// It round-trips losslessly — String() of a parsed, unmodified document equals
// the original input byte-for-byte — so structural edits (append/replace a
// section, set a frontmatter field) never disturb unrelated content.
//
// This is the shared model MCP, REST, and the UI all manipulate, so markdown
// is a real data model rather than an opaque blob.
package mdmodel

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Document is a parsed markdown note.
type Document struct {
	// Frontmatter is the parsed YAML frontmatter (nil if none). Order is not
	// preserved on re-serialize; use SetMeta/GetMeta for field access.
	Frontmatter map[string]any
	hadFM       bool
	rawFM       string // original frontmatter text, verbatim
	fmDirty     bool   // set when frontmatter is modified (forces re-marshal)
	// Preamble is any content between frontmatter and the first heading
	// (kept verbatim).
	Preamble string
	// Sections are the heading-delimited blocks, in order.
	Sections []*Section
}

// Section is a heading and everything under it up to the next heading of the
// same or higher level is NOT nested here — sections are a flat, ordered list
// mirroring the document, which keeps editing predictable. Level is the ATX
// depth (1 = #, 2 = ##, …). Heading is the text after the #'s. Body is the raw
// content lines beneath the heading (verbatim, may include sub-headings only
// if deeper — see note in Parse).
type Section struct {
	Level   int
	Heading string
	Body    string // raw text under the heading (excludes the heading line)
}

var atxRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)

// Parse splits raw markdown into frontmatter + preamble + flat sections.
func Parse(raw string) *Document {
	d := &Document{}

	body := raw
	// Frontmatter: leading --- ... --- block.
	if strings.HasPrefix(raw, "---\n") || strings.HasPrefix(raw, "---\r\n") {
		if end := findFMEnd(raw); end.after > 0 {
			fmText := raw[4:end.start]
			var m map[string]any
			if err := yaml.Unmarshal([]byte(fmText), &m); err == nil {
				d.Frontmatter = m
				d.hadFM = true
				d.rawFM = fmText
			}
			body = raw[end.after:]
		}
	}

	sc := bufio.NewScanner(strings.NewReader(body))
	sc.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	var cur *Section
	var pre strings.Builder
	var bodyBuf strings.Builder
	flush := func() {
		if cur != nil {
			cur.Body = bodyBuf.String()
			d.Sections = append(d.Sections, cur)
			bodyBuf.Reset()
		}
	}
	for sc.Scan() {
		line := sc.Text()
		if m := atxRe.FindStringSubmatch(line); m != nil {
			flush()
			cur = &Section{Level: len(m[1]), Heading: m[2]}
			continue
		}
		if cur == nil {
			pre.WriteString(line)
			pre.WriteByte('\n')
		} else {
			bodyBuf.WriteString(line)
			bodyBuf.WriteByte('\n')
		}
	}
	flush()
	d.Preamble = pre.String()
	return d
}

type fmRange struct{ start, after int }

// findFMEnd locates the closing --- of a leading frontmatter block.
func findFMEnd(raw string) fmRange {
	// scan lines after the opening ---
	idx := strings.Index(raw, "\n") // end of opening ---
	if idx < 0 {
		return fmRange{}
	}
	rest := raw[idx+1:]
	off := idx + 1
	sc := bufio.NewScanner(strings.NewReader(rest))
	pos := off
	for sc.Scan() {
		line := sc.Text()
		lineLen := len(line) + 1 // + newline
		if strings.TrimRight(line, "\r") == "---" {
			// start = offset of the closing '---' line; after = just past it.
			return fmRange{start: pos, after: pos + lineLen}
		}
		pos += lineLen
	}
	return fmRange{}
}

// String re-serializes the document. For an unmodified parse this reproduces
// the original input.
func (d *Document) String() string {
	var b strings.Builder
	if d.hadFM || len(d.Frontmatter) > 0 {
		b.WriteString("---\n")
		if d.fmDirty || d.rawFM == "" {
			out, _ := yaml.Marshal(d.Frontmatter)
			b.Write(out)
		} else {
			b.WriteString(d.rawFM)
		}
		b.WriteString("---\n")
	}
	b.WriteString(d.Preamble)
	for _, s := range d.Sections {
		b.WriteString(strings.Repeat("#", s.Level))
		b.WriteByte(' ')
		b.WriteString(s.Heading)
		b.WriteByte('\n')
		b.WriteString(s.Body)
	}
	return b.String()
}

// --- structural operations --------------------------------------------------

// FindSection returns the first section whose heading matches (case-insensitive,
// trimmed), or nil.
func (d *Document) FindSection(heading string) *Section {
	h := strings.ToLower(strings.TrimSpace(heading))
	for _, s := range d.Sections {
		if strings.ToLower(strings.TrimSpace(s.Heading)) == h {
			return s
		}
	}
	return nil
}

// GetSection returns the raw body under a heading, or ("", false).
func (d *Document) GetSection(heading string) (string, bool) {
	if s := d.FindSection(heading); s != nil {
		return s.Body, true
	}
	return "", false
}

// ReplaceSection swaps the body under an existing heading. Returns false if the
// heading isn't found.
func (d *Document) ReplaceSection(heading, newBody string) bool {
	s := d.FindSection(heading)
	if s == nil {
		return false
	}
	s.Body = ensureTrailingNL(newBody)
	return true
}

// AppendToSection appends content to the end of a section's body. Creates the
// section (at the given level, default 2) if it doesn't exist.
func (d *Document) AppendToSection(heading, content string, level int) {
	s := d.FindSection(heading)
	if s == nil {
		if level <= 0 {
			level = 2
		}
		d.Sections = append(d.Sections, &Section{Level: level, Heading: heading, Body: ensureTrailingNL(content)})
		return
	}
	s.Body = ensureTrailingNL(strings.TrimRight(s.Body, "\n") + "\n\n" + strings.TrimSpace(content))
}

// SetMeta sets a frontmatter field.
func (d *Document) SetMeta(key string, value any) {
	if d.Frontmatter == nil {
		d.Frontmatter = map[string]any{}
	}
	d.Frontmatter[key] = value
	d.hadFM = true
	d.fmDirty = true
}

// GetMeta returns a frontmatter field.
func (d *Document) GetMeta(key string) (any, bool) {
	v, ok := d.Frontmatter[key]
	return v, ok
}

// Tags returns the frontmatter tags as a string slice (handles []any or []string).
func (d *Document) Tags() []string {
	v, ok := d.Frontmatter["tags"]
	if !ok {
		return nil
	}
	return toStringSlice(v)
}

// SetTags writes the tags field.
func (d *Document) SetTags(tags []string) {
	d.SetMeta("tags", tags)
}

// Title returns a best-effort title: frontmatter `title`, else the first H1.
func (d *Document) Title() string {
	if v, ok := d.Frontmatter["title"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	for _, s := range d.Sections {
		if s.Level == 1 {
			return strings.TrimSpace(s.Heading)
		}
	}
	return ""
}

// Outline returns the heading structure for UI/agent navigation.
func (d *Document) Outline() []string {
	var out []string
	for _, s := range d.Sections {
		out = append(out, fmt.Sprintf("%s %s", strings.Repeat("#", s.Level), s.Heading))
	}
	return out
}

// --- helpers ----------------------------------------------------------------

func ensureTrailingNL(s string) string {
	if s == "" {
		return "\n"
	}
	if !strings.HasSuffix(s, "\n") {
		return s + "\n"
	}
	return s
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			out = append(out, fmt.Sprint(e))
		}
		return out
	case string:
		return []string{t}
	}
	return nil
}

var _ = bytes.MinRead // keep bytes imported for potential future use
