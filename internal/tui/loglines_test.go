package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// TestClassifyLine pins the §15.3 classifier table on synthetic lines
// and near-misses.
func TestClassifyLine(t *testing.T) {
	cases := []struct {
		line string
		want LineKind
	}{
		// READY first (regardless of other content).
		{"main: server is listening on http://127.0.0.1:9080", LineReady},
		{"llama_server: listening on port 9080", LineReady},
		// ERROR.
		{"error: failed to load model", LineError},
		{"llama_model_loader: fatal error reading file", LineError},
		{"load failed", LineError},
		{"CUDA error: out of memory", LineError},
		// WARN.
		{"warn: unknown parameter", LineWarn},
		{"llama: warning: not enough memory", LineWarn},
		// TIMING.
		{"llama_perf_context_print: prompt eval time = 12.3 ms", LineTiming},
		{"llama_perf_context_print: 34 tokens per second", LineTiming},
		{"load time = 412.5 ms", LineTiming},
		// llama.cpp default-logger severity letters (owner feedback).
		{"0.00.177.074 W DEPRECATED: argument '--top-k' specified multiple times", LineWarn},
		{"0.00.573.859 I cmn  common_param: common_params_print_info: verbosity = 3", LineInfo},
		{"0.00.002.001 E error initializing model", LineError},
		{"0.00.004.000 D debug detail line", LineInfo},
		// INFO default.
		{"llm_load_print_meta: format          = GGUF v3", LineInfo},
		{"slot update_slots: id  0 | task 4 | n_tokens = 16", LineInfo},
		{"", LineInfo},
	}
	for _, c := range cases {
		if got := classifyLine(c.line); got != c.want {
			t.Errorf("classifyLine(%q) = %v, want %v", c.line, got, c.want)
		}
	}
}

// TestColorizeLineANSI256 forces the ANSI-256 profile and asserts the
// rendered SGR per kind, with INFO lines left plain (P1/P9).
func TestColorizeLineANSI256(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.ColorProfile())

	r := &RunMode{theme: DefaultTheme()}
	th, _, _ := ResolveTheme("", true) // dark pair for known indices
	_ = th

	errLine := r.colorizeLine("error: boom")
	if !containsSequence(errLine, "\x1b[38;5;") {
		t.Errorf("error line must be colored, got %q", errLine)
	}
	readyLine := r.colorizeLine("server is listening on :9080")
	if !containsSequence(readyLine, "\x1b[1;38;5;") {
		t.Errorf("ready line must be colored + bold, got %q", readyLine)
	}
	if got := r.colorizeLine("llm_load_print_meta: format = GGUF"); got != "llm_load_print_meta: format = GGUF" {
		t.Errorf("info line must stay plain, got %q", got)
	}
	// Different kinds render different colors.
	errC := r.colorizeLine("error: x")
	warnC := r.colorizeLine("warn: x")
	if errC == warnC {
		t.Error("error and warn lines must render different SGR")
	}
}

// TestStatusBadgeGlyphs pins the §15.3 glyph prefixes: the badge block
// is "<glyph> [LABEL]" and the [STARTING] text/format is preserved.
func TestStatusBadgeGlyphs(t *testing.T) {
	th, _, _ := ResolveTheme("", true)
	cases := []struct {
		label, want string
	}{
		{"ready", "● [READY]"},
		{"starting", "◌ [STARTING]"},
		{"error", "✕ [ERROR]"},
		{"exited", "◌ [EXITED]"},
	}
	for _, c := range cases {
		got := stripANSI(statusBadge(c.label, th.StatusReady))
		if got != c.want {
			t.Errorf("statusBadge(%q) = %q, want %q", c.label, got, c.want)
		}
	}
	// The STARTING badge still carries the bracketed uppercase label.
	if !strings.Contains(stripANSI(statusBadge("starting", th.StatusStart)), "[STARTING]") {
		t.Error("STARTING badge must keep its [STARTING] text (§2.3)")
	}
}

// TestRunModeRunTitle pins the OSC title content (§15.3) for single
// and router sessions.
func TestRunModeRunTitle(t *testing.T) {
	single := &RunMode{model: config.Model{Alias: "alpha"}}
	if got := single.runTitle("READY"); got != "llamaman — alpha [READY]" {
		t.Errorf("single runTitle = %q", got)
	}
	router := &RunMode{routerFile: "/tmp/my-models.ini"}
	if got := router.runTitle("STARTING"); got != "llamaman — my-models.ini [STARTING]" {
		t.Errorf("router runTitle = %q", got)
	}
}
