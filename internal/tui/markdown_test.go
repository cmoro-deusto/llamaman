package tui

import (
	"strings"
	"testing"
)

// renderCardMarkdown unit tests (DESIGN §16.7): the model card panel's
// markdown renderer.

func stripCard(ln string) string { return stripANSI(ln) }

func TestCardMarkdownTable(t *testing.T) {
	lines := renderCardMarkdown(DefaultTheme(), []byte("| a | b |\n|---|---|\n| 1 | 2 |"))
	// The GFM extension registers its own HTML table renderer at a
	// priority that used to override ours — raw <table>/<th> leaked.
	// Cells must render with separators instead.
	got := strings.Join(lines, "\n")
	for _, bad := range []string{"<table>", "<th>", "<td>", "<thead>"} {
		if strings.Contains(got, bad) {
			t.Errorf("raw table HTML leaked: %q\n%s", bad, got)
		}
	}
	for _, want := range []string{"a │ b", "1 │ 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("table cells missing %q\n%s", want, got)
		}
	}
}

func TestCardMarkdownBlockSpacing(t *testing.T) {
	lines := renderCardMarkdown(DefaultTheme(), []byte("# H1\n\npara one\n\n## H2\n\npara two"))
	if len(lines) != 7 {
		t.Fatalf("lines = %d, want 7 (blocks separated by blanks)\n%q", len(lines), lines)
	}
	if lines[0] != "H1" || lines[1] != "" || lines[2] != "para one" || lines[4] != "H2" {
		t.Errorf("spacing wrong: %q", lines)
	}
}

func TestCardMarkdownFrontmatterTrimmed(t *testing.T) {
	// Frontmatter after leading HTML comments (unsloth-style cards).
	md := "<!-- markdownlint-disable -->\n\n---\nlicense: mit\ntags:\n- gguf\n---\n# Model\n\ntext"
	lines := renderCardMarkdown(DefaultTheme(), []byte(trimCardFrontmatter(md)))
	got := strings.Join(lines, "\n")
	for _, bad := range []string{"license: mit", "tags:", "---"} {
		if strings.Contains(got, bad) {
			t.Errorf("frontmatter leaked %q\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "Model") || !strings.Contains(got, "text") {
		t.Errorf("card body missing\n%s", got)
	}
}

func TestCardMarkdownOSC8Links(t *testing.T) {
	lines := renderCardMarkdown(DefaultTheme(), []byte("[open HF](https://huggingface.co/x)"))
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	ln := lines[0]
	if !strings.Contains(ln, "\x1b]8;;https://huggingface.co/x\x1b\\") {
		t.Errorf("missing OSC 8 open sequence")
	}
	if !strings.Contains(ln, "\x1b]8;;\x1b\\") {
		t.Errorf("missing OSC 8 close sequence")
	}
	// The visible text survives stripping.
	if !strings.Contains(stripANSI(ln), "open HF") {
		t.Errorf("link text missing")
	}
}

func TestCardMarkdownAutolinkOSC8(t *testing.T) {
	lines := renderCardMarkdown(DefaultTheme(), []byte("see https://huggingface.co/x now"))
	if len(lines) != 1 {
		t.Fatalf("lines = %d", len(lines))
	}
	if !strings.Contains(lines[0], "\x1b]8;;https://huggingface.co/x\x1b\\") {
		t.Errorf("autolink not wrapped in OSC 8")
	}
}

func TestCardMarkdownHTMLSkipped(t *testing.T) {
	md := "<table><tr><td>x</td></tr></table>\n\nreal text"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	if len(lines) != 1 || !strings.Contains(lines[0], "real text") {
		t.Fatalf("HTML block must be skipped: %q", lines)
	}
}
