package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// newTestRunMode constructs a minimal RunMode for testing search state
// and content rendering. Skips the tailer/process plumbing — tests
// poke r.buf directly and exercise the search/render/refresh paths.
func newTestRunMode(initial string) *RunMode {
	r := &RunMode{
		viewport:    viewport.New(80, 24),
		searchInput: textinput.New(),
	}
	r.buf.WriteString(initial)
	return r
}

func TestHighlightOccurrencesEmptyQuery(t *testing.T) {
	in := "hello world\nerror line\n"
	if got := highlightOccurrences(in, ""); got != in {
		t.Errorf("empty query mutated input: got %q", got)
	}
}

func TestHighlightOccurrencesEmptyBuffer(t *testing.T) {
	if got := highlightOccurrences("", "needle"); got != "" {
		t.Errorf("empty buffer = %q, want empty string", got)
	}
}

func TestHighlightOccurrencesQueryLongerThanBuffer(t *testing.T) {
	if got := highlightOccurrences("hi", "needle"); got != "hi" {
		t.Errorf("query > buffer mutated input: got %q", got)
	}
}

// TestHighlightOccurrencesCaseInsensitiveCasePreserved verifies the
// matcher is case-insensitive but the wrapped bytes come from the raw
// input so original case (ERROR vs error) is preserved.
func TestHighlightOccurrencesCaseInsensitiveCasePreserved(t *testing.T) {
	in := "warn: error: ERROR: Error"
	out := highlightOccurrences(in, "error")

	for _, want := range []string{"error", "ERROR", "Error"} {
		expect := highlightOpen + want + highlightClose
		if !strings.Contains(out, expect) {
			t.Errorf("output missing wrapped %q\nfull: %q", want, out)
		}
	}
}

// TestHighlightOccurrencesMultipleMatchesPerLine and multi-line.
func TestHighlightOccurrencesMultipleMatchesPerLine(t *testing.T) {
	in := "foo bar foo\nbaz qux\nfoo foo foo\n"
	out := highlightOccurrences(in, "foo")
	got := strings.Count(out, highlightOpen+"foo"+highlightClose)
	if got != 5 {
		t.Errorf("wrapped count = %d, want 5\nfull: %q", got, out)
	}
}

// TestHighlightOccurrencesNonOverlapping verifies that for a query "aa"
// against input "aaaa" we wrap two non-overlapping matches, not three.
// This matches less/grep behavior and avoids degenerate output.
func TestHighlightOccurrencesNonOverlapping(t *testing.T) {
	out := highlightOccurrences("aaaa", "aa")
	got := strings.Count(out, highlightOpen+"aa"+highlightClose)
	if got != 2 {
		t.Errorf("wrapped count = %d, want 2\nfull: %q", got, out)
	}
}

// TestEffectiveQueryPrefersLiveInput confirms typing in the prompt
// shows live highlights, falling back to applied searchQuery when the
// prompt is closed.
func TestEffectiveQueryPrefersLiveInput(t *testing.T) {
	r := newTestRunMode("")
	r.searchQuery = "applied"

	// Prompt closed: applied query wins.
	if got := r.effectiveQuery(); got != "applied" {
		t.Errorf("closed prompt: effectiveQuery = %q, want %q", got, "applied")
	}

	// Prompt open with text: live input wins.
	r.searchActive = true
	r.searchInput.SetValue("typing")
	if got := r.effectiveQuery(); got != "typing" {
		t.Errorf("open prompt with text: effectiveQuery = %q, want %q", got, "typing")
	}

	// Prompt open with empty input: live input still wins (empty), so
	// the renderer sees "" and emits no highlights — no stale applied
	// query bleeding through.
	r.searchInput.SetValue("")
	if got := r.effectiveQuery(); got != "" {
		t.Errorf("open prompt with empty input: effectiveQuery = %q, want empty", got)
	}
}

// TestRunModeEscOnMainScreenClearsAppliedQuery covers the new layered
// Esc behavior: with an applied query, pressing Esc on the main run
// screen drops the query, matches and re-renders content without
// highlights.
func TestRunModeEscOnMainScreenClearsAppliedQuery(t *testing.T) {
	r := newTestRunMode("error one\nerror two\n")
	r.searchQuery = "error"
	r.searchMatches = []int{0, 1}
	r.searchIdx = 0
	r.refreshContent()

	if !strings.Contains(r.viewport.View(), highlightOpen) {
		t.Fatal("setup: viewport does not contain highlights before Esc")
	}

	r.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if r.searchQuery != "" {
		t.Errorf("searchQuery = %q, want empty after Esc", r.searchQuery)
	}
	if len(r.searchMatches) != 0 {
		t.Errorf("searchMatches len = %d, want 0", len(r.searchMatches))
	}
	if strings.Contains(r.viewport.View(), highlightOpen) {
		t.Errorf("viewport still contains highlight escape after Esc")
	}
}

// TestRunModeEscOnMainScreenNoOpWhenClean confirms Esc with no applied
// query is a no-op (doesn't crash, doesn't toggle anything weird).
func TestRunModeEscOnMainScreenNoOpWhenClean(t *testing.T) {
	r := newTestRunMode("hello\n")
	if _, _ = r.Update(tea.KeyMsg{Type: tea.KeyEsc}); r.searchQuery != "" {
		t.Errorf("Esc on clean state should remain empty, got %q", r.searchQuery)
	}
}

// TestRunModeEscInPromptClearsWhenInputEmpty exercises the layered Esc
// inside the search prompt: with empty input AND an applied query, Esc
// closes the prompt AND clears the applied query in one press.
func TestRunModeEscInPromptClearsWhenInputEmpty(t *testing.T) {
	r := newTestRunMode("error one\n")
	r.searchQuery = "error"
	r.searchActive = true
	r.searchInput.Focus()
	r.searchInput.SetValue("")

	r.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if r.searchActive {
		t.Error("searchActive still true after Esc")
	}
	if r.searchQuery != "" {
		t.Errorf("searchQuery = %q, want empty (layered Esc)", r.searchQuery)
	}
}

// TestRunModeEscInPromptKeepsAppliedWhenTyping confirms Esc-with-text
// only cancels the typing — the previously-applied query stays so the
// user can recover from a mis-press of `/`.
func TestRunModeEscInPromptKeepsAppliedWhenTyping(t *testing.T) {
	r := newTestRunMode("error one\nwarn two\n")
	r.searchQuery = "error"
	r.searchActive = true
	r.searchInput.Focus()
	r.searchInput.SetValue("warn")

	r.Update(tea.KeyMsg{Type: tea.KeyEsc})

	if r.searchActive {
		t.Error("searchActive still true after Esc")
	}
	if r.searchQuery != "error" {
		t.Errorf("searchQuery = %q, want %q (typing cancelled, applied preserved)",
			r.searchQuery, "error")
	}
}

// TestRunModeSearchEnterRefreshesHighlights is the end-to-end happy
// path: press /, type a query, press Enter, viewport content has
// highlights wrapped around the matches.
func TestRunModeSearchEnterRefreshesHighlights(t *testing.T) {
	r := newTestRunMode("error: failed\nokay\nERROR: again\n")
	// Open the search prompt.
	r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	if !r.searchActive {
		t.Fatal("/ did not enter search mode")
	}
	// Type the query character by character through the textinput.
	for _, ch := range "error" {
		r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	// Apply.
	r.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if r.searchQuery != "error" {
		t.Errorf("searchQuery = %q, want %q", r.searchQuery, "error")
	}
	got := r.viewport.View()
	wantWrap := highlightOpen + "error" + highlightClose
	if !strings.Contains(got, wantWrap) {
		t.Errorf("viewport missing wrapped lowercase match\nview: %q", got)
	}
	wantWrapUpper := highlightOpen + "ERROR" + highlightClose
	if !strings.Contains(got, wantWrapUpper) {
		t.Errorf("viewport missing wrapped uppercase match\nview: %q", got)
	}
}

// ---- header tests ----

// newHeaderTestRunMode builds the minimal RunMode state needed to call
// renderHeader. proc.Started is set so formatUptime can compute against
// a fixed delta; viewport, search input and keymap are stubbed so
// other code paths don't trip over zero values.
func newHeaderTestRunMode(model config.Model, preset config.Preset, reg flags.Registry, width int) *RunMode {
	r := &RunMode{
		cfg:         &config.Config{Globals: config.Globals{Host: "127.0.0.1", Port: 9080}},
		model:       model,
		preset:      preset,
		registry:    reg,
		proc:        &server.Process{Started: time.Now().Add(-90 * time.Second)},
		viewport:    viewport.New(width, 24),
		searchInput: textinput.New(),
		theme:       CurrentTheme(),
		status:      StatusReady,
	}
	r.SetSize(width, 30)
	return r
}

// TestCanonicalParamsTranslatesShortForm pins the alias-aware lookup:
// a preset that uses `c` (the short form) must surface as `ctx-size`
// when the header asks for it.
func TestCanonicalParamsTranslatesShortForm(t *testing.T) {
	preset := config.Preset{Params: config.Params{
		{Key: "c", Value: json.Number("8192")},
	}}
	got := canonicalParams(preset, nil)
	if v, ok := got["ctx-size"]; !ok || v != json.Number("8192") {
		t.Errorf("expected canonicalParams to surface ctx-size=8192; got %v", got)
	}
	if _, present := got["c"]; present {
		t.Errorf("expected short form `c` to be remapped, not duplicated; got %v", got)
	}
}

// TestCanonicalParamsKeepsUnknownKeys verifies that flags the registry
// has never seen still appear in the output (under their literal key)
// so users can see misconfigured params rather than silently lose them.
func TestCanonicalParamsKeepsUnknownKeys(t *testing.T) {
	preset := config.Preset{Params: config.Params{
		{Key: "totally-fake-flag", Value: "yes"},
	}}
	got := canonicalParams(preset, flags.Registry{})
	if v, ok := got["totally-fake-flag"]; !ok || v != "yes" {
		t.Errorf("unknown key dropped; got %v", got)
	}
}

func TestRunHeaderHasFixedHeightAtWideWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, 140,
	)
	header := r.renderHeader()
	got := strings.Count(header, "\n") + 1
	if got != headerHeight {
		t.Errorf("header height = %d, want %d\nheader:\n%s", got, headerHeight, header)
	}
}

func TestRunHeaderHasFixedHeightAtNarrowWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha-with-a-deliberately-long-name"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "temp", Value: json.Number("0.7")},
			{Key: "top-p", Value: json.Number("0.9")},
		}},
		nil, 60,
	)
	header := r.renderHeader()
	got := strings.Count(header, "\n") + 1
	if got != headerHeight {
		t.Errorf("narrow header height = %d, want %d (truncation should keep height fixed)\nheader:\n%s",
			got, headerHeight, header)
	}
}

func TestRunHeaderMetricsOnReversesIndicator(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "metrics", Value: true},
		}},
		nil, 140,
	)
	header := r.renderHeader()
	// lipgloss emits the SGR for reverse+bold as `\x1b[1;7m`.
	if !strings.Contains(header, "\x1b[1;7m") {
		t.Errorf("expected reverse+bold SGR around [Metrics]; got header:\n%q", header)
	}
}

func TestRunHeaderMetricsOffNoReverse(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no metrics param
		nil, 140,
	)
	header := r.renderHeader()
	if strings.Contains(header, "\x1b[1;7m") {
		t.Errorf("expected no reverse+bold SGR when metrics is absent; got header:\n%q", header)
	}
	// And [Metrics] is still rendered (subtle/dim, not reversed).
	if !strings.Contains(stripANSI(header), "[Metrics]") {
		t.Error("expected [Metrics] indicator to be rendered even when off")
	}
}

func TestRunHeaderShowsNAForMissingParams(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no params at all
		nil, 140,
	)
	plain := stripANSI(r.renderHeader())
	for _, want := range []string{"Temp: n/a", "Top_P: n/a", "Top_K: n/a", "Min_P: n/a", "Context Size: n/a"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected %q in header; got:\n%s", want, plain)
		}
	}
}

func TestRunHeaderShowsCanonicalCtxSizeFromShortForm(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "c", Value: json.Number("16384")},
		}},
		nil, 140,
	)
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: 16384") {
		t.Errorf("expected ctx-size value from short-form `c`; got header:\n%s", plain)
	}
}
