package tui

import (
	"strings"
	"testing"
	"time"

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

// TestParseLoadPhase pins the §15.4 classifier on synthetic llama.cpp
// lines and near-misses.
func TestParseLoadPhase(t *testing.T) {
	half := func() *float64 { p := 0.5; return &p }
	frac := func(n, d int) *float64 { p := float64(n) / float64(d); return &p }
	cases := []struct {
		line     string
		phase    string
		progress *float64
	}{
		{"llm_load_tensors: offloaded 16/33 layers to GPU", "offloading layers to GPU", frac(16, 33)},
		{"llm_load_tensors: offloaded 33/33 layers to GPU", "offloading layers to GPU", frac(1, 1)},
		{"llm_load_tensors: offloading 16 repeating layers to GPU", "offloading layers to GPU", nil},
		{"main: downloading model from hf.co/org/repo:quant ... 50%", "downloading model", half()},
		{"downloading model.gguf 50%", "downloading model", half()},
		{"loading model from /m/model.gguf", "loading model file", nil},
		{"llama_model_loader: loading model file with 2 tensors", "loading model file", nil},
		// Near-misses → no phase (tolerant).
		{"main: server is listening on http://127.0.0.1:9080", "", nil},
		{"llm_load_print_meta: format = GGUF v3", "", nil},
		{"slot update_slots: id 0 | task 4", "", nil},
		{"", "", nil},
	}
	for _, c := range cases {
		phase, prog := parseLoadPhase(c.line)
		if phase != c.phase {
			t.Errorf("parseLoadPhase(%q) phase = %q, want %q", c.line, phase, c.phase)
		}
		if c.progress == nil && prog != nil {
			t.Errorf("parseLoadPhase(%q) progress = %v, want nil", c.line, *prog)
		}
		if c.progress != nil {
			if prog == nil {
				t.Errorf("parseLoadPhase(%q) progress = nil, want %v", c.line, *c.progress)
			} else if *prog != *c.progress {
				t.Errorf("parseLoadPhase(%q) progress = %v, want %v", c.line, *prog, *c.progress)
			}
		}
	}
	// Fraction math: 16/33 ≈ 0.4848, clamped to 1 at most.
	phase, prog := parseLoadPhase("llm_load_tensors: offloaded 16/33 layers to GPU")
	if phase == "" || prog == nil {
		t.Fatal("16/33 should yield a progress fraction")
	}
	if got := *prog; got < 0.48 || got > 0.49 {
		t.Errorf("16/33 progress = %v, want ≈0.4848", got)
	}
	_, prog = parseLoadPhase("llm_load_tensors: offloaded 33/33 layers to GPU")
	if prog == nil || *prog != 1.0 {
		t.Errorf("33/33 progress = %v, want 1.0", prog)
	}
}

// TestProgressBar pins the fixed-width block bar (§15.4).
func TestProgressBar(t *testing.T) {
	cases := []struct {
		frac float64
		want string
	}{
		{0, "░░░░░░░░░░░░"},
		{0.41, "▓▓▓▓▓░░░░░░░"},
		{1, "▓▓▓▓▓▓▓▓▓▓▓▓"},
		{1.5, "▓▓▓▓▓▓▓▓▓▓▓▓"}, // clamped
		{-1, "░░░░░░░░░░░░"},
	}
	for _, c := range cases {
		if got := progressBar(c.frac, 12); got != c.want {
			t.Errorf("progressBar(%v) = %q, want %q", c.frac, got, c.want)
		}
	}
}

// TestRunModeIngestLoadChunk covers the chunk hook: phases are captured
// while starting (deadline stamped in the future), ignored once ready.
func TestRunModeIngestLoadChunk(t *testing.T) {
	r := &RunMode{status: StatusStarting}
	r.ingestLoadChunk("llm_load_tensors: offloaded 16/33 layers to GPU\n")
	if r.loadPhase != "offloading layers to GPU" {
		t.Fatalf("loadPhase = %q", r.loadPhase)
	}
	if r.loadPhaseUntil.IsZero() || !time.Now().Before(r.loadPhaseUntil) {
		t.Error("loadPhaseUntil must be stamped in the future (2s minimum-visible)")
	}

	r.status = StatusReady
	r.ingestLoadChunk("llm_load_tensors: offloaded 33/33 layers to GPU\n")
	if r.loadPhase != "offloading layers to GPU" {
		t.Error("chunks after ready must not overwrite the phase")
	}
}

// TestRunModeLoadBlock covers the panel block (§15.4): shown while
// starting (phase + bar), held ≥2s after ready, then replaced by stats.
func TestRunModeLoadBlock(t *testing.T) {
	th, _, _ := ResolveTheme("", true)
	prog := 0.4848
	r := &RunMode{
		theme:          th,
		status:         StatusStarting,
		loadPhase:      "offloading layers to GPU",
		loadProgress:   &prog,
		loadPhaseUntil: time.Now().Add(time.Hour), // pinned future
	}
	panel := stripANSI(r.renderServerPanel(30))
	if !strings.Contains(panel, "offloading layers to GPU") || !strings.Contains(panel, "48%") {
		t.Errorf("starting panel must show the phase + percent\n%s", panel)
	}

	// Ready but still inside the 2s minimum-visible window → block holds.
	r.status = StatusReady
	if !r.showingLoadBlock() {
		t.Error("block must hold for the 2s minimum-visible window after ready")
	}

	// Deadline passed → normal stats return (renderServerRows on a bare
	// RunMode is covered by the fakeserver-backed panel tests).
	r.loadPhaseUntil = time.Now().Add(-time.Second)
	if r.showingLoadBlock() {
		t.Error("block must disappear after the 2s window")
	}
}

// TestRunModeLoadBlockStaticFallback: no parsed phase → "loading…".
func TestRunModeLoadBlockStaticFallback(t *testing.T) {
	th, _, _ := ResolveTheme("", true)
	r := &RunMode{theme: th, status: StatusStarting, loadPhaseUntil: time.Now().Add(time.Hour)}
	panel := stripANSI(r.renderServerPanel(30))
	if !strings.Contains(panel, "loading…") {
		t.Errorf("unparsed starting panel must show the static 'loading…'\n%s", panel)
	}
}

// TestRunModeStatusBadgeGlyphs is covered in loglines_test.go; this
// guards the router panel load block (§15.4).
func TestRunModeRouterPanelLoadBlock(t *testing.T) {
	th, _, _ := ResolveTheme("", true)
	r := &RunMode{theme: th, status: StatusStarting, routerFile: "/tmp/my.ini", loadPhaseUntil: time.Now().Add(time.Hour)}
	panel := stripANSI(r.renderRouterPanel(30))
	if !strings.Contains(panel, "loading…") {
		t.Errorf("router starting panel must show the load block\n%s", panel)
	}
}
