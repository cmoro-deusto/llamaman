package tui

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// runHeaderWideWidth is the terminal width used by tests that need
// the right-column info (`[Metrics]`, `Min_P:`, etc.) to fit
// alongside the 72-col wordmark without truncation. Below ~170 cols
// the right column gets clipped — that degradation is intentional
// for real users at narrow widths but breaks substring assertions.
const runHeaderWideWidth = 200

func TestRunHeaderHasFixedHeightAtWideWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	header := r.renderHeader()
	got := strings.Count(header, "\n") + 1
	if got != headerHeightWithWordmark {
		t.Errorf("header height = %d, want %d\nheader:\n%s", got, headerHeightWithWordmark, header)
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

// TestRunHeaderShowsWordmarkAtWideWidth confirms the wordmark is
// rendered on the left side of the box when the terminal can fit it.
func TestRunHeaderShowsWordmarkAtWideWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderHeader())
	// Each wordmark line starts with the same character cells; check
	// for a substring stable across font/asset edits.
	if !strings.Contains(plain, "█") {
		t.Errorf("expected wordmark cells in header; got:\n%s", plain)
	}
}

// TestRunHeaderHidesWordmarkAtNarrowWidth covers the graceful
// degradation: terminals narrower than wordmarkMinWidth use the
// compact 6-row layout with no wordmark.
func TestRunHeaderHidesWordmarkAtNarrowWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, 80,
	)
	plain := stripANSI(r.renderHeader())
	if strings.Contains(plain, "█") {
		t.Errorf("expected no wordmark on narrow terminal; got:\n%s", plain)
	}
}

func TestRunHeaderMetricsOnUsesGreenBackground(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "metrics", Value: true},
		}},
		nil, runHeaderWideWidth,
	)
	header := r.renderHeader()
	// `\x1b[30;42m` = black fg on green bg (no bold — bold renders as
	// gray on many terminals, killing contrast against the green).
	// Pinned literally rather than via lipgloss.Render so it's
	// deterministic in tests where lipgloss has no TTY to detect.
	if !strings.Contains(header, "\x1b[30;42m") {
		t.Errorf("expected black-on-green SGR around [Metrics]; got header:\n%q", header)
	}
}

func TestRunHeaderMetricsOffNoGreenBackground(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no metrics param
		nil, runHeaderWideWidth,
	)
	header := r.renderHeader()
	if strings.Contains(header, "\x1b[30;42m") {
		t.Errorf("expected no green-bg SGR when metrics is absent; got header:\n%q", header)
	}
	// And [Metrics] is still rendered (subtle/dim, not lit).
	if !strings.Contains(stripANSI(header), "[Metrics]") {
		t.Error("expected [Metrics] indicator to be rendered even when off")
	}
}

func TestRunHeaderShowsNAForMissingParams(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no params at all
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderHeader())
	for _, want := range []string{"Temp: n/a", "Top_P: n/a", "Top_K: n/a", "Min_P: n/a", "Context Size: n/a"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expected %q in header; got:\n%s", want, plain)
		}
	}
}

// TestRunHeaderShowsServerVersion verifies row 1 surfaces the parsed
// llama-server version after Alias when serverVersion is populated.
func TestRunHeaderShowsServerVersion(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.serverVersion = "8994 (aab68217b)"
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Server: 8994 (aab68217b)") {
		t.Errorf("expected Server: <version> in header; got:\n%s", plain)
	}
}

// TestRunHeaderShowsNAWhenServerVersionMissing covers the failure
// fallback: an empty serverVersion (binary missing or --version
// unparseable) renders as `Server: n/a` rather than blank.
func TestRunHeaderShowsNAWhenServerVersionMissing(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	// serverVersion left empty by default
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Server: n/a") {
		t.Errorf("expected Server: n/a fallback; got:\n%s", plain)
	}
}

// TestRunModeLogFrameRendersWithBorder pins the log-frame border: the
// rendered View must include rounded-border top corners around the
// viewport content (in addition to the top box's). Catches regressions
// that would silently drop the log-side border.
func TestRunModeLogFrameRendersWithBorder(t *testing.T) {
	cfg := &config.Config{Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	r := &RunMode{
		cfg:         cfg,
		model:       config.Model{Alias: "alpha"},
		preset:      config.Preset{Name: "default"},
		proc:        &server.Process{Started: time.Now()},
		viewport:    viewport.New(0, 0),
		searchInput: textinput.New(),
		theme:       CurrentTheme(),
		status:      StatusReady,
	}
	r.SetSize(runHeaderWideWidth, 30)
	r.buf.WriteString("hello\n")
	r.viewport.SetContent(r.buf.String())
	plain := stripANSI(r.View())
	// `╭` and `╰` appear twice in a healthy render: once for the top
	// box, once for the log frame. If only one set is present, the
	// log frame is missing.
	if got := strings.Count(plain, "╭"); got < 2 {
		t.Errorf("expected at least 2 rounded-top-left corners (top box + log frame); got %d\nview:\n%s", got, plain)
	}
	if got := strings.Count(plain, "╰"); got < 2 {
		t.Errorf("expected at least 2 rounded-bottom-left corners; got %d", got)
	}
}

// TestLoadServerVersionMissingBinary returns an empty string when the
// configured binary doesn't exist or isn't executable, so the renderer
// can fall back to "n/a" without blocking startup.
func TestLoadServerVersionMissingBinary(t *testing.T) {
	if got := loadServerVersion("/this/binary/does/not/exist"); got != "" {
		t.Errorf("expected empty for missing binary; got %q", got)
	}
	if got := loadServerVersion(""); got != "" {
		t.Errorf("expected empty for empty bin path; got %q", got)
	}
}

func TestRunHeaderShowsCanonicalCtxSizeFromShortForm(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "c", Value: json.Number("16384")},
		}},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: 16384") {
		t.Errorf("expected ctx-size value from short-form `c`; got header:\n%s", plain)
	}
}

// ---- propsFetchedMsg handler tests ----

// TestRunHeaderLiveCtxSizeWins confirms the live value from /props is
// rendered as `Context Size:` once propsFetchedMsg lands, even when the
// preset declared no ctx-size at all.
func TestRunHeaderLiveCtxSizeWins(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no ctx-size
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{nctx: 4096})
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: 4096") {
		t.Errorf("expected Context Size: 4096; got header:\n%s", plain)
	}
}

// TestRunHeaderLiveCtxSizeOverridesPreset confirms that the live value
// wins over a preset's declared ctx-size when they disagree (typical
// case: llama-server clamped a too-large request). captureSlog absorbs
// the disagreement-INFO line that the handler emits — its content is
// covered by TestRunHeaderDisagreementLogsInfo and we don't want a
// noisy stderr trail from this test.
func TestRunHeaderLiveCtxSizeOverridesPreset(t *testing.T) {
	_ = captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "ctx-size", Value: json.Number("8192")},
		}},
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{nctx: 4096})
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: 4096") {
		t.Errorf("expected live value 4096 to win over preset 8192; got header:\n%s", plain)
	}
	if strings.Contains(plain, "Context Size: 8192") {
		t.Errorf("preset value 8192 leaked into header:\n%s", plain)
	}
}

// TestRunHeaderFetchFailureFallsBackAndLogsWarn pins the rule that a
// hard /props failure does NOT flash to the TUI (header stays at the
// preset value or n/a) but DOES land a slog.Warn record so the user
// has a paper trail.
func TestRunHeaderFetchFailureFallsBackAndLogsWarn(t *testing.T) {
	logs := captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no ctx-size — fallback should be n/a
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{err: errors.New("connection refused")})
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: n/a") {
		t.Errorf("expected Context Size: n/a fallback; got header:\n%s", plain)
	}
	assertLogged(t, logs, slog.LevelWarn, "/props fetch failed")
}

// TestRunHeaderFetchCancelledNoWarn covers the kill-during-fetch path:
// a context.Canceled error is the user closing the run mode, not an
// actionable failure, so the handler must suppress the WARN line.
func TestRunHeaderFetchCancelledNoWarn(t *testing.T) {
	logs := captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{err: context.Canceled})
	assertNotLogged(t, logs, slog.LevelWarn, "/props fetch failed")
}

// TestRunHeaderDisagreementLogsInfo confirms the diagnostic path:
// when preset and live disagree, slog.Info is emitted with the
// "ctx-size mismatch" message so users can correlate later.
func TestRunHeaderDisagreementLogsInfo(t *testing.T) {
	logs := captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "ctx-size", Value: json.Number("8192")},
		}},
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{nctx: 4096})
	assertLogged(t, logs, slog.LevelInfo, "ctx-size mismatch")
}

// TestRunHeaderAgreementNoInfo: when preset and live match, the
// disagreement diagnostic must NOT fire. This guards against the
// log-spam regression risk: every successful fetch with a declared
// preset value would otherwise emit a record.
func TestRunHeaderAgreementNoInfo(t *testing.T) {
	logs := captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "ctx-size", Value: json.Number("4096")},
		}},
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{nctx: 4096})
	assertNotLogged(t, logs, slog.LevelInfo, "ctx-size mismatch")
}

// TestRunHeaderNCtxZeroIgnored covers the "/props returned 0" path
// (server speaks /props but hasn't populated n_ctx yet): treat as
// unavailable, fall back to the preset value, do NOT log.
func TestRunHeaderNCtxZeroIgnored(t *testing.T) {
	logs := captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "ctx-size", Value: json.Number("16384")},
		}},
		nil, runHeaderWideWidth,
	)
	r.Update(propsFetchedMsg{nctx: 0})
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: 16384") {
		t.Errorf("expected fall-back to preset value 16384 when n_ctx is 0; got:\n%s", plain)
	}
	assertNotLogged(t, logs, slog.LevelWarn, "/props fetch failed")
	assertNotLogged(t, logs, slog.LevelInfo, "ctx-size mismatch")
}
