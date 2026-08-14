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

// TestCardMarkdownBlockSpacing: sections-only policy (owner round) —
// blank lines around headings, breaks, code, tables and lists, but not
// between consecutive paragraphs.
func TestCardMarkdownBlockSpacing(t *testing.T) {
	// Heading → paragraph → paragraph → heading: blanks around the
	// headings only.
	lines := renderCardMarkdown(DefaultTheme(), []byte("# H1\n\npara one\n\npara two\n\n## H2"))
	if len(lines) != 6 {
		t.Fatalf("lines = %d, want 6\n%q", len(lines), lines)
	}
	want := []string{"H1", "", "para one", "para two", "", "H2"}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q (all: %q)", i, lines[i], want[i], lines)
		}
	}
}

// TestCardMarkdownSectionBlanks: breaks, code blocks, tables and lists
// are section blocks — blanks around them, not between paragraphs.
func TestCardMarkdownSectionBlanks(t *testing.T) {
	md := "intro\n\n---\n\ncode below\n\n```\nline1\nline2\n```\n\n- a\n- b\n\n| x | y |\n|---|---|\n| 1 | 2 |\n\noutro"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	// intro | ──── | code below | line1 line2 | • a • b | x │ y 1 │ 2 | outro
	// with blanks around ────, the code block, the list, and the table.
	want := []string{
		"intro", "", "────", "", "code below", "", "line1", "line2", "", "• a", "• b", "", "x │ y │ ", "1 │ 2 │ ", "", "outro",
	}
	if len(lines) != len(want) {
		t.Fatalf("lines = %d, want %d\n%q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q (all: %q)", i, lines[i], want[i], lines)
		}
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

// TestCardMarkdownBlockquoteRun: consecutive quoted lines (separate
// Blockquote nodes in goldmark) flow with a single newline; only the
// last line of the run adds the trailing blank (owner round).
func TestCardMarkdownBlockquoteRun(t *testing.T) {
	md := "> line one\n\n> line two\n\n> line three\n\npara"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	want := []string{"▍ line one", "▍ line two", "▍ line three", "", "para"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %d, want %d\n%q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q (all: %q)", i, lines[i], want[i], lines)
		}
	}
}

// TestCardMarkdownListBlankSeparated: list items separated by blank
// lines in the source are paragraph-wrapped; the paragraph's newline
// already separates them, so no blank appears between bullets (owner
// report: RichardErkhov/microsoft_-_phi-1-gguf). Simple items still
// get their separator from the item handler.
func TestCardMarkdownListBlankSeparated(t *testing.T) {
	md := "- a\n\n- b\n\n- c\n\npara"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	want := []string{"• a", "• b", "• c", "", "para"}
	if len(lines) != len(want) {
		t.Fatalf("lines = %d, want %d\n%q", len(lines), len(want), lines)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q (all: %q)", i, lines[i], want[i], lines)
		}
	}
	// Simple (single-newline) items also stay tight.
	simple := renderCardMarkdown(DefaultTheme(), []byte("- x\n- y"))
	if len(simple) != 2 || simple[0] != "• x" || simple[1] != "• y" {
		t.Errorf("simple list = %q, want [• x • y]", simple)
	}
}

// TestCardMarkdownLinkInHeading: a link inside a heading must keep its
// OSC 8 hyperlink — the heading's lipgloss style used to wrap (and
// mangle) the escape sequence, killing ctrl+click (owner report: the
// "Run DeepSeek-V4-0731 Guide!" link in the unsloth card).
func TestCardMarkdownLinkInHeading(t *testing.T) {
	md := "## Read our How to [Run DeepSeek-V4-0731 Guide!](https://unsloth.ai/docs/models/deepseek-v4)"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	if len(lines) != 1 {
		t.Fatalf("lines = %d\n%q", len(lines), lines)
	}
	ln := lines[0]
	for _, want := range []string{
		"\x1b]8;;https://unsloth.ai/docs/models/deepseek-v4\x1b\\",
		"\x1b]8;;\x1b\\",
	} {
		if !strings.Contains(ln, want) {
			t.Errorf("heading link missing %q\n%s", want, ln)
		}
	}
	if !strings.Contains(stripANSI(ln), "Read our How to Run DeepSeek-V4-0731 Guide!") {
		t.Errorf("heading text wrong\n%s", stripANSI(ln))
	}
}

// TestCardMarkdownLinkInBold: same guarantee for a link inside strong
// emphasis.
func TestCardMarkdownLinkInBold(t *testing.T) {
	lines := renderCardMarkdown(DefaultTheme(), []byte("**[bold link](https://example.com/x)** tail"))
	got := strings.Join(lines, "\n")
	if !strings.Contains(got, "\x1b]8;;https://example.com/x\x1b\\") {
		t.Errorf("bold link lost its OSC 8 hyperlink\n%q", got)
	}
	if !strings.Contains(stripANSI(got), "bold link") {
		t.Errorf("bold link text missing\n%s", stripANSI(got))
	}
}

// TestCardMarkdownStripsEmoji: emoji-presentation runes (width-2 in
// emoji-aware terminals, width-1 in runewidth) must not reach the
// card — they break the terminal's column accounting and garble the
// whole view (owner report: the unsloth Qwen3-Coder card's ✨+VS16
// broke the layout after sorting).
func TestCardMarkdownStripsEmoji(t *testing.T) {
	md := "# Model ✨\n\n⚡ Bold text and [link](https://x.com) ♥\n\nCJK keeps 宽度."
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	got := strings.Join(lines, "\n")
	for _, bad := range []string{"✨", "⚡", "♥", "\ufe0f"} {
		if strings.Contains(got, bad) {
			t.Errorf("emoji-capable rune %q leaked into the card\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "Model") || !strings.Contains(got, "Bold text") {
		t.Errorf("card body missing\n%s", got)
	}
	if !strings.Contains(got, "CJK keeps ") {
		t.Errorf("CJK content must survive (width-2 is consistent)\n%s", got)
	}
}

// TestCardMarkdownStripsControlChars: C0/C1 control runes must not
// reach the card — the terminal EXECUTES them instead of printing them
// (a literal VT moves the cursor down a row, CR returns to column 0, a
// tab jumps to the next tab stop), so a single one in a table cell
// garbles the whole panel layout (owner report: the
// unsloth/Muse-Glimmer-30B-GGUF benchmark-table header carries a
// literal VT after "30B" — after a few PgDn pages the layout broke).
// They become spaces (card authors use them as separators); the
// structural \n survives.
func TestCardMarkdownStripsControlChars(t *testing.T) {
	md := "# Muse Glimmer-30B\x0bHigh Reasoning\n\nBody\x0ctext with\ta tab.\r\n"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	got := strings.Join(lines, "\n")
	for _, bad := range []string{"\x0b", "\x0c", "\r", "\x7f", "\t"} {
		if strings.Contains(got, bad) {
			t.Errorf("control char %q leaked into the card\n%q", bad, got)
		}
	}
	if !strings.Contains(got, "Muse Glimmer-30B High Reasoning") {
		t.Errorf("VT must become a space\n%s", stripANSI(got))
	}
	if !strings.Contains(got, "Body text with a tab.") {
		t.Errorf("card body mangled\n%s", stripANSI(got))
	}
}

// TestCardMarkdownStripsMathAlphanumerics: mathematical alphanumeric
// symbols (U+1D400–U+1D7FF) are the same ambiguous-width class as
// emoji — most terminal fonts lack the glyph and the fallback renders
// it at an unpredictable width (owner report: the Muse-Glimmer card's
// 𝛕3-Bench/𝛕3-Banking benchmark names sit in the same scroll region
// as the VT above).
func TestCardMarkdownStripsMathAlphanumerics(t *testing.T) {
	md := "# Benchmarks\n\n𝛕3-Banking and DeepSearch QA.\n"
	lines := renderCardMarkdown(DefaultTheme(), []byte(md))
	got := strings.Join(lines, "\n")
	if strings.Contains(got, "𝛕") {
		t.Errorf("math alphanumeric leaked into the card\n%s", got)
	}
	if !strings.Contains(got, "3-Banking and DeepSearch QA.") {
		t.Errorf("card body mangled\n%s", stripANSI(got))
	}
}
