package web

import (
	"bytes"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

// md is a goldmark instance with GFM (tables, strikethrough, task lists,
// autolinks) enabled. We keep unsafe HTML rendering ON deliberately so our
// preprocessed callout/mermaid <div>s survive; input is the agent's own notes,
// not untrusted web content.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
)

var (
	wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]+))?\]\]`)
	// Obsidian-style callouts: a blockquote whose first line is > [!TYPE] Title
	calloutRe  = regexp.MustCompile(`(?m)^> \[!(\w+)\]\s*(.*)$`)
	mathBlock  = regexp.MustCompile(`(?s)\$\$(.+?)\$\$`)
	mathInline = regexp.MustCompile(`\$([^$\n]+?)\$`)
	fenceRe    = regexp.MustCompile("(?s)```mermaid\\s*\\n(.+?)```")
)

// RenderMarkdown converts a note body to HTML. It preprocesses:
//   - mermaid fenced blocks -> <div class="mermaid"> (rendered client-side)
//   - $$...$$ / $...$ math   -> spans KaTeX renders client-side
//   - [[wikilinks]]          -> internal <a> links to /note/<slug>
//   - > [!NOTE] callouts     -> styled callout blocks
//
// then runs the result through goldmark.
func RenderMarkdown(body string) string {
	// 1. Extract mermaid fences BEFORE markdown sees them (so it doesn't treat
	//    them as code). Replace with a placeholder div.
	body = fenceRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := fenceRe.FindStringSubmatch(m)
		code := ""
		if len(sub) > 1 {
			code = sub[1]
		}
		return "\n\n<div class=\"mermaid\">" + html.EscapeString(strings.TrimSpace(code)) + "</div>\n\n"
	})

	// 2. Callouts: transform the marker line into an opening styled div. This
	//    is a light touch — full nested callout parsing is out of scope.
	body = calloutRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := calloutRe.FindStringSubmatch(m)
		typ := strings.ToLower(sub[1])
		title := sub[2]
		if title == "" && typ != "" {
			// strings.Title is deprecated; callout types are plain ASCII
			// keywords (note, warning, tip...), so capitalize manually.
			title = strings.ToUpper(typ[:1]) + typ[1:]
		}
		return "> <div class=\"callout callout-" + typ + "\"><div class=\"callout-title\">" + html.EscapeString(title) + "</div>"
	})

	// 3. Wikilinks -> internal anchors.
	body = wikilinkRe.ReplaceAllStringFunc(body, func(m string) string {
		sub := wikilinkRe.FindStringSubmatch(m)
		target := strings.TrimSpace(sub[1])
		label := target
		if len(sub) > 2 && sub[2] != "" {
			label = sub[2]
		}
		return "[" + label + "](/note/" + slugForLink(target) + ")"
	})

	// 4. Math -> placeholders KaTeX picks up. We wrap in spans with a class;
	//    the client script renders them. Escaping the inner TeX minimally.
	body = mathBlock.ReplaceAllStringFunc(body, func(m string) string {
		inner := mathBlock.FindStringSubmatch(m)[1]
		return "\n\n<div class=\"math-block\">" + html.EscapeString(strings.TrimSpace(inner)) + "</div>\n\n"
	})
	body = mathInline.ReplaceAllStringFunc(body, func(m string) string {
		inner := mathInline.FindStringSubmatch(m)[1]
		return "<span class=\"math-inline\">" + html.EscapeString(inner) + "</span>"
	})

	var buf bytes.Buffer
	if err := md.Convert([]byte(body), &buf); err != nil {
		return "<pre>" + html.EscapeString(body) + "</pre>"
	}
	return buf.String()
}

var linkSlugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugForLink(title string) string {
	s := strings.ToLower(strings.TrimSpace(title))
	s = linkSlugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
