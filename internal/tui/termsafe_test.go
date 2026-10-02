package tui

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/cmoro-deusto/llamaman/internal/hf"
)

// These tests guard the §16.7 terminal-safe-text invariant: a panel only
// keeps its borders when (1) every escape sequence is well-formed and
// (2) every visible rune has a width all terminals agree on. Width bugs
// kept recurring (emoji, VT controls, nested link styles), each fixed
// after an owner report; these invariants catch the next one in CI.

// hostileCard is a model card stitched from the shapes that broke the
// browser before, plus the usual suspects.
const hostileCard = "# Model ✨ card 🚀\n\n" +
	"| Quantization | File | Size |\n|---|---|---|\n" +
	"| BF16 | [**model-BF16.gguf**](https://hf.co/a/b/blob/main/model-BF16.gguf) | 14.23 GB |\n" +
	"| INT8 | [**model-int8_convrot**](https://hf.co/x) | 6.76 GB |\n" +
	"| 30B\v | *it* `code` [`code link`](https://hf.co/c) | ⚡ |\n\n" +
	"Family: 👨‍👩‍👧 ❤️ ♥ ⚠️ ✔ ⬇ 𝛕 𝐀 é café — naïve\n\n" +
	"CJK: 日本語のモデル 한국어 中文 ｆｕｌｌ　width\n\n" +
	"Controls:\ttab\rcr\x1b[31mred\x1b[0m bell\a nbsp x zwsp​x rtl‮x nerdx\n\n" +
	"> quote with **bold [link](https://hf.co/q)** inside\n\n" +
	"- item *with [em link](https://hf.co/e)*\n"

// allowedEscapes matches the only sequences the TUI may emit: SGR and
// OSC 8 hyperlinks (open and close).
var allowedEscapes = regexp.MustCompile(`\x1b\[[0-9;:]*m|\x1b\]8;[^\x1b\x07]*;[^\x1b\x07]*\x1b\\`)

// assertTerminalSafe checks every line for malformed escapes, stray
// control bytes, and width disagreement between grapheme and wcwidth
// measurement (the two models terminals implement).
func assertTerminalSafe(t *testing.T, label string, lines []string) {
	t.Helper()
	for i, ln := range lines {
		if !utf8.ValidString(ln) {
			t.Errorf("%s line %d: invalid UTF-8: %q", label, i, ln)
			continue
		}
		plain := allowedEscapes.ReplaceAllString(ln, "")
		for _, r := range plain {
			if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
				t.Errorf("%s line %d: stray control %U (malformed escape?) in %q", label, i, r, ln)
				break
			}
		}
		if g, w := ansi.StringWidth(plain), ansi.StringWidthWc(plain); g != w {
			t.Errorf("%s line %d: grapheme width %d != wcwidth %d — terminals will disagree: %q", label, i, g, w, plain)
		}
	}
}

// withTrueColor forces styled output: without a TTY lipgloss emits no
// escapes at all, which hides every escape-corruption bug.
func withTrueColor(t *testing.T) {
	t.Helper()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(termenv.Ascii) })
}

func TestTermSafe(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"ascii untouched", "Qwen3-27B Q4_K_M (8.4 GB)", "Qwen3-27B Q4_K_M (8.4 GB)"},
		{"latin and punctuation kept", "café — naïve · §2 → x … ©", "café — naïve · §2 → x … ©"},
		{"cjk kept", "日本語 한국어 ｆｕｌｌ", "日本語 한국어 ｆｕｌｌ"},
		{"emoji dropped", "a🚀b✨c", "abc"},
		{"zwj family dropped", "a👨‍👩‍👧b", "ab"},
		{"vs16 heart dropped", "a❤️b♥c", "abc"},
		{"emoji-capable narrow symbols dropped", "a⚠b✔c⬇d", "abcd"},
		{"math alphanumerics dropped", "τ𝛕𝐀", "τ"},
		{"combining mark dropped", "é", "e"},
		{"controls become spaces", "a\tb\vc\rd\x1be", "a b c d e"},
		{"newline to space by default", "a\nb", "a b"},
		{"exotic spaces normalized", "a b　c", "a b c"},
		{"invisible format chars dropped", "a​b‮c⁠d", "abcd"},
		{"private use dropped", "ab", "ab"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := termSafeLine(tt.in); got != tt.want {
				t.Errorf("termSafeLine(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
	if got := termSafe("a\nb\tc", true); got != "a\nb c" {
		t.Errorf("termSafe keepNewlines = %q, want %q", got, "a\nb c")
	}
}

// FuzzTermSafe: whatever goes in, the output is controls-free (bar
// kept newlines) and measures the same under both width models.
func FuzzTermSafe(f *testing.F) {
	for _, s := range []string{hostileCard, "plain", "日本", "🚀‍", "\x1b[1m", "é"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, in string) {
		if !utf8.ValidString(in) {
			return
		}
		out := termSafe(in, true)
		for _, ln := range strings.Split(out, "\n") {
			for _, r := range ln {
				if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
					t.Fatalf("control %U survived termSafe(%q)", r, in)
				}
			}
			if g, w := ansi.StringWidth(ln), ansi.StringWidthWc(ln); g != w {
				t.Fatalf("termSafe(%q) line %q: grapheme width %d != wcwidth %d", in, ln, g, w)
			}
		}
	})
}

// Regression: a bold run inside a link was rendered through the link's
// underline style, which lipgloss applies rune by rune — splitting the
// bold escape into a lone ESC plus literal "[1m" and shifting every
// panel border (owner report: the top trending card's file table).
func TestCardMarkdownTerminalSafe(t *testing.T) {
	withTrueColor(t)

	lines := renderCardMarkdown(DefaultTheme(), []byte(hostileCard))

	if len(lines) == 0 {
		t.Fatal("card rendered nothing")
	}
	assertTerminalSafe(t, "card", lines)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "\x1b]8;;https://hf.co/a/b/blob/main/model-BF16.gguf\x1b\\") {
		t.Error("bold link lost its OSC 8 hyperlink")
	}
	if strings.Contains(allowedEscapes.ReplaceAllString(joined, ""), "[1m") {
		t.Error("literal [1m leaked: a style was applied over already-styled text")
	}
}

// FuzzCardMarkdown: arbitrary markdown never yields malformed escapes
// or width-ambiguous output.
func FuzzCardMarkdown(f *testing.F) {
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(termenv.Ascii)
	for _, s := range []string{hostileCard, "**[a](u)**", "[**a** _b_ `c`](u)", "# [x](u)", "| a | [**b**](u) |\n|-|-|\n| c | d |"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		assertTerminalSafe(t, "card", renderCardMarkdown(DefaultTheme(), []byte(src)))
	})
}

// TestBrowserViewTerminalSafe: with hostile HF data in every field the
// browser renders, each view line is well-formed and exactly as wide as
// the terminal — i.e. every panel border lands in its column.
func TestBrowserViewTerminalSafe(t *testing.T) {
	withTrueColor(t)
	stub := &stubBrowserRunner{
		results: []hf.SearchResult{{
			ID: "org/model🚀✨", Downloads: 1400000, Likes: 2795,
			Tags:        []string{"gguf", "en", "zh", "license:other❤️", "base_model:quantized:org/base​👨‍👩‍👧"},
			PipelineTag: "text-to-image⚡",
		}},
		opts: []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}},
		card: hostileCard,
	}
	for _, w := range []int{200, 160, 120, 80} {
		r, _ := openBrowserRoot(t, stub)
		driveRoot(t, r, tea.WindowSizeMsg{Width: w, Height: 50})
		searchDrive(t, r, "q")

		lines := strings.Split(r.browser.View(), "\n")

		assertTerminalSafe(t, "browser", lines)
		want := lipgloss.Width(lines[0])
		for i, ln := range lines {
			if got := lipgloss.Width(ln); got != want {
				t.Errorf("width %d: line %d is %d cells, line 0 is %d — border misaligned: %q", w, i, got, want, ansi.Strip(ln))
			}
		}
	}
}
