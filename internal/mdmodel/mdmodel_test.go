package mdmodel

import (
	"strings"
	"testing"
)

func TestRoundTripLossless(t *testing.T) {
	cases := []string{
		"# Title\n\nsome body\n\n## Section A\n\ncontent a\n\n## Section B\n\ncontent b\n",
		"---\ntype: roadmap\ntags: [a, b]\n---\n\n# Roadmap\n\nintro\n\n## Phase 1\n\n- [x] done\n- [ ] todo\n",
		"no headings at all, just text\nsecond line\n",
		"## Only H2\n\nbody\n",
	}
	for i, in := range cases {
		d := Parse(in)
		out := d.String()
		if out != in {
			t.Errorf("case %d NOT lossless:\n--- IN ---\n%q\n--- OUT ---\n%q", i, in, out)
		}
	}
}

func TestStructuralOps(t *testing.T) {
	in := "---\ntype: note\ntags: [x]\n---\n\n# Doc\n\n## Risks\n\nold risk\n\n## Plan\n\nold plan\n"
	d := Parse(in)
	// read section
	if body, ok := d.GetSection("Risks"); !ok || !strings.Contains(body, "old risk") {
		t.Fatalf("GetSection Risks: %q %v", body, ok)
	}
	// replace section
	if !d.ReplaceSection("Risks", "new risk content\n") {
		t.Fatal("ReplaceSection failed")
	}
	if b, _ := d.GetSection("Risks"); !strings.Contains(b, "new risk") {
		t.Errorf("replace didn't take: %q", b)
	}
	// append to existing
	d.AppendToSection("Plan", "extra plan line", 2)
	if b, _ := d.GetSection("Plan"); !strings.Contains(b, "extra plan line") || !strings.Contains(b, "old plan") {
		t.Errorf("append lost content: %q", b)
	}
	// append to NEW section (creates it)
	d.AppendToSection("Decisions", "decided X", 2)
	if b, ok := d.GetSection("Decisions"); !ok || !strings.Contains(b, "decided X") {
		t.Errorf("new section: %q %v", b, ok)
	}
	// meta ops
	d.SetMeta("status", "active")
	if v, _ := d.GetMeta("status"); v != "active" {
		t.Errorf("SetMeta failed: %v", v)
	}
	if tags := d.Tags(); len(tags) != 1 || tags[0] != "x" {
		t.Errorf("Tags: %v", tags)
	}
	d.SetTags([]string{"x", "y"})
	if tags := d.Tags(); len(tags) != 2 {
		t.Errorf("SetTags: %v", tags)
	}
	// title from H1
	if d.Title() != "Doc" {
		t.Errorf("Title: %q", d.Title())
	}
	// still serializes with all changes
	out := d.String()
	if !strings.Contains(out, "new risk") || !strings.Contains(out, "decided X") || !strings.Contains(out, "status: active") {
		t.Errorf("serialize missing edits:\n%s", out)
	}
	t.Logf("final doc:\n%s", out)
}
