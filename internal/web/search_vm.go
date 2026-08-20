package web

import (
	"regexp"
	"strings"

	"github.com/davasorus/engram/internal/core"
)

// SearchVM is a UI-friendly view of a search hit: the raw cosine score is
// turned into a strength percentage + label, and the body into a clean excerpt
// (frontmatter and markdown noise stripped) so results read well.
type SearchVM struct {
	ID       string
	Title    string
	Excerpt  string
	Kind     string  // "semantic" | "keyword"
	Score    float64 // raw, kept for the title tooltip
	Strength int     // 0..100 for the bar
	Label    string  // "strong" | "good" | "weak"
}

// toVMs maps engine hits to view models.
func toVMs(hits []core.SearchHit) []SearchVM {
	out := make([]SearchVM, 0, len(hits))
	for _, h := range hits {
		out = append(out, SearchVM{
			ID:       h.Note.ID,
			Title:    cleanTitle(h.Note.Title),
			Excerpt:  excerpt(h.Note.Body, 160),
			Kind:     h.Kind,
			Score:    h.Score,
			Strength: strengthPct(h.Score, h.Kind),
			Label:    strengthLabel(h.Score, h.Kind),
		})
	}
	return out
}

// strengthPct maps a score to a 0..100 bar width. Keyword hits are exact
// matches (score 1.0) so they read as full strength. Semantic cosine scores
// in practice cluster in ~0.3..0.8; we stretch that range so the bar is
// visually meaningful rather than everything sitting near half.
func strengthPct(score float64, kind string) int {
	if kind == "keyword" {
		return 100
	}
	// Map 0.30 -> 0%, 0.80 -> 100%, clamp.
	p := (score - 0.30) / (0.80 - 0.30) * 100
	if p < 0 {
		p = 0
	}
	if p > 100 {
		p = 100
	}
	return int(p)
}

func strengthLabel(score float64, kind string) string {
	if kind == "keyword" {
		return "exact"
	}
	switch p := strengthPct(score, kind); {
	case p >= 66:
		return "strong"
	case p >= 33:
		return "good"
	default:
		return "weak"
	}
}

// cleanTitle strips a leading markdown heading marker some notes carry in the
// title field (e.g. "# proj9_roadmap" -> "proj9_roadmap").
func cleanTitle(t string) string {
	return strings.TrimSpace(strings.TrimLeft(t, "# "))
}

var (
	frontmatterRe = regexp.MustCompile(`(?s)^\s*---.*?---\s*`)
	mdNoiseRe     = regexp.MustCompile("[#*`>\\[\\]]+")
	wsRe          = regexp.MustCompile(`\s+`)
)

// excerpt produces a clean one-line snippet: drop YAML frontmatter, a leading
// duplicated title heading, and markdown punctuation, then collapse whitespace.
func excerpt(body string, n int) string {
	b := frontmatterRe.ReplaceAllString(body, "")
	// drop a leading "# Heading" line
	if i := strings.IndexByte(b, '\n'); i >= 0 && strings.HasPrefix(strings.TrimSpace(b), "#") {
		b = b[i+1:]
	}
	b = mdNoiseRe.ReplaceAllString(b, " ")
	b = wsRe.ReplaceAllString(b, " ")
	b = strings.TrimSpace(b)
	if len(b) > n {
		b = strings.TrimSpace(b[:n]) + "…"
	}
	return b
}
