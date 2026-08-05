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
	"github.com/cmoro-deusto/llamaman/internal/hwinfo"
	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// newTestRunMode constructs a minimal RunMode for testing search state
// and content rendering. Skips the tailer/process plumbing — tests
// poke r.buf directly and exercise the search/render/refresh paths.
func newTestRunMode(initial string) *RunMode {
	r := &RunMode{
		viewport:      viewport.New(80, 24),
		searchInput:   textinput.New(),
		tokensHistory: newRingBuffer(sparkBufferSamples),
		promptHistory: newRingBuffer(sparkBufferSamples),
		utilHistory:   map[string]*ringBuffer{},
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
		cfg:           &config.Config{Globals: config.Globals{Host: "127.0.0.1", Port: 9080}},
		model:         model,
		preset:        preset,
		registry:      reg,
		proc:          &server.Process{Started: time.Now().Add(-90 * time.Second)},
		viewport:      viewport.New(width, 24),
		searchInput:   textinput.New(),
		theme:         CurrentTheme(),
		status:        StatusReady,
		tokensHistory: newRingBuffer(sparkBufferSamples),
		promptHistory: newRingBuffer(sparkBufferSamples),
		utilHistory:   map[string]*ringBuffer{},
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
// the right-column identity content to fit alongside the 31-col
// smblock wordmark without truncation, and that exercise the full
// wide-mode layout (top strip + live band).
const runHeaderWideWidth = 200

// runHeaderCompactWidth is below the wordmark breakpoint: compact
// mode (no wordmark, no live band), identity arranged 3 cells × 2
// rows.
const runHeaderCompactWidth = 60

func TestRunHeaderHasFixedHeightAtWideWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	header := r.renderHeader()
	got := strings.Count(header, "\n") + 1
	want := headerHeightWithWordmark + liveBandHeight
	if got != want {
		t.Errorf("State 1 header height = %d, want %d (top + band)\nheader:\n%s", got, want, header)
	}
}

func TestRunHeaderHasFixedHeightAtNarrowWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha-with-a-deliberately-long-name"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "temp", Value: json.Number("0.7")},
			{Key: "top-p", Value: json.Number("0.9")},
		}},
		nil, runHeaderCompactWidth,
	)
	header := r.renderHeader()
	got := strings.Count(header, "\n") + 1
	if got != headerHeight {
		t.Errorf("compact header height = %d, want %d (truncation should keep height fixed)\nheader:\n%s",
			got, headerHeight, header)
	}
}

// TestTopStripColumnsAlign pins the alignment of identity cells:
// row 2's "Preset" sits under row 1's "Alias", "Uptime" sits under
// "Server", and the status badge sits under "Context Size".
// Without per-column padding the cells drift because labels and
// values have different widths.
func TestTopStripColumnsAlign(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.serverVersion = "8994 (aab68217b)"
	plain := stripANSI(r.renderTopStrip())
	lines := strings.Split(plain, "\n")

	findCol := func(needle string) int {
		for _, line := range lines {
			runes := []rune(line)
			lineStr := string(runes)
			if idx := strings.Index(lineStr, needle); idx >= 0 {
				// Convert byte index to rune index (visual col).
				prefixRunes := []rune(lineStr[:idx])
				return len(prefixRunes)
			}
		}
		return -1
	}
	pairs := []struct {
		row1, row2 string
	}{
		{"Alias:", "Preset:"},
		{"Server:", "Uptime:"},
		{"Context Size:", "[READY]"},
	}
	for _, p := range pairs {
		c1 := findCol(p.row1)
		c2 := findCol(p.row2)
		if c1 < 0 || c2 < 0 {
			t.Errorf("could not find %q or %q in:\n%s", p.row1, p.row2, plain)
			continue
		}
		if c1 != c2 {
			t.Errorf("%q (col %d) does not align with %q (col %d) in:\n%s",
				p.row1, c1, p.row2, c2, plain)
		}
	}
}

// newRouterTestRunMode builds a minimal router-mode RunMode mirroring
// newHeaderTestRunMode: routerFile set, status Starting, a live fetch
// context, and (optionally) a fakeFetcher for polling tests.
func newRouterTestRunMode(fake *fakeFetcher) *RunMode {
	r := &RunMode{
		cfg:                    &config.Config{Globals: config.Globals{Host: "127.0.0.1", Port: 9080}},
		routerFile:             "models.ini",
		proc:                   &server.Process{Started: time.Now().Add(-90 * time.Second)},
		viewport:               viewport.New(120, 30),
		searchInput:            textinput.New(),
		theme:                  CurrentTheme(),
		status:                 StatusStarting,
		fetcher:                fake,
		routerMetricsAvailable: true,
		tokensHistory:          newRingBuffer(sparkBufferSamples),
		promptHistory:          newRingBuffer(sparkBufferSamples),
		utilHistory:            map[string]*ringBuffer{},
	}
	r.fetchCtx, r.fetchCancel = context.WithCancel(context.Background())
	return r
}

// TestRouterPropsNCtxZeroTransitionsReady is the regression for the
// empty router-models panel: llama.cpp's router reports n_ctx = 0 in
// /props by design, and the readiness gate treated nctx <= 0 as "not
// ready", so the live poll (which fetches /models + /health) never
// started and the panel stayed on "(no models reported)".
func TestRouterPropsNCtxZeroTransitionsReady(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.Update(propsFetchedMsg{nctx: 0})
	if r.status != StatusReady {
		t.Errorf("status = %v, want Ready (router /props reports n_ctx=0 by design)", r.status)
	}
	if !r.livePollStarted {
		t.Error("live poll not armed for router mode")
	}
}

// TestSingleModelPropsNCtxZeroStaysStarting pins the single-model
// behavior: an unpopulated n_ctx still means "not ready" there, so the
// live poll must NOT be armed.
func TestSingleModelPropsNCtxZeroStaysStarting(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerFile = "" // single-model mode
	r.Update(propsFetchedMsg{nctx: 0})
	if r.status != StatusStarting {
		t.Errorf("status = %v, want Starting", r.status)
	}
	if r.livePollStarted {
		t.Error("live poll armed although readiness never fired")
	}
}

// TestRouterPollFetchesModelsNotSlots drives the router startup path:
// the armed live poll must fetch /models + /health and never touch
// /slots or /metrics (the router does not serve them; they 400).
func TestRouterPollFetchesModelsNotSlots(t *testing.T) {
	fake := &fakeFetcher{
		props:  propsWithNCtx(0),
		models: &llamaapi.Models{Data: []llamaapi.ModelInfo{{ID: "m:fast"}}},
		health: &llamaapi.Health{Status: "ok"},
	}
	r := newRouterTestRunMode(fake)
	_, cmds := r.Update(propsFetchedMsg{nctx: 0})
	for _, sub := range collectCmds(cmds) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if fake.modelsCalls == 0 || fake.healthCalls == 0 {
		t.Errorf("router poll must fetch /models and /health (models=%d health=%d)", fake.modelsCalls, fake.healthCalls)
	}
	if fake.slotsCalls != 0 || fake.metricsCalls != 0 {
		t.Errorf("router poll must not fetch /slots or /metrics (slots=%d metrics=%d)", fake.slotsCalls, fake.metricsCalls)
	}
	if len(r.routerModels) != 1 || r.routerModels[0].ID != "m:fast" {
		t.Errorf("routerModels = %+v, want [m:fast]", r.routerModels)
	}
}

// TestRouterTickFetchesModelsNotSlots covers the per-second tick branch
// (the code path that kept the panel fresh once ready).
func TestRouterTickFetchesModelsNotSlots(t *testing.T) {
	fake := &fakeFetcher{
		props:  propsWithNCtx(0),
		models: &llamaapi.Models{Data: []llamaapi.ModelInfo{{ID: "m:fast"}}},
		health: &llamaapi.Health{Status: "ok"},
	}
	r := newRouterTestRunMode(fake)
	r.livePollStarted = true // simulate an already-armed poll
	_, cmd := r.Update(livePollTickMsg(time.Now()))
	for _, sub := range collectCmds(cmd) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if fake.modelsCalls == 0 || fake.healthCalls == 0 {
		t.Errorf("router tick must fetch /models and /health (models=%d health=%d)", fake.modelsCalls, fake.healthCalls)
	}
	if fake.slotsCalls != 0 || fake.metricsCalls != 0 {
		t.Errorf("router tick must not fetch /slots or /metrics (slots=%d metrics=%d)", fake.slotsCalls, fake.metricsCalls)
	}
}

// TestRouterPanelStatusValue verifies the panel tags each model from
// /models "status.value" and the count uses it ("N total (M loaded)").
func TestRouterPanelStatusValue(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerModels = []llamaapi.ModelInfo{
		{ID: "m:fast", Status: llamaapi.ModelStatus{Value: "loaded"}},
		{ID: "m:slow", Status: llamaapi.ModelStatus{Value: "unloaded"}},
		{ID: "m:loading", Status: llamaapi.ModelStatus{Value: "loading"}},
	}
	got := stripANSI(r.renderRouterPanel(60))
	for _, want := range []string{"● m:fast", "loaded", "○ m:slow", "unloaded", "◐ m:loading", "loading"} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q; panel:\n%s", want, got)
		}
	}
	if got := r.renderRouterModelCount(); got != "3 total (1 loaded)" {
		t.Errorf("count = %q, want \"3 total (1 loaded)\"", got)
	}
}

// TestRouterPanelHealthFallback covers older router builds that report
// loaded ids via GET /health instead of per-model status.value.
func TestRouterPanelHealthFallback(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerModels = []llamaapi.ModelInfo{{ID: "m:fast"}, {ID: "m:slow"}}
	r.routerLoaded = []string{"m:fast"}
	got := stripANSI(r.renderRouterPanel(60))
	for _, want := range []string{"● m:fast", "○ m:slow"} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q; panel:\n%s", want, got)
		}
	}
	if got := r.renderRouterModelCount(); got != "2 total (1 loaded)" {
		t.Errorf("count = %q, want \"2 total (1 loaded)\"", got)
	}
}

// TestRouterPollFetchesPerModelSlots verifies the router poll fetches
// /slots?model=<id> for loaded models only — unloaded models have no
// slots, and the router requires the model name on /slots.
func TestRouterPollFetchesPerModelSlots(t *testing.T) {
	fake := &fakeFetcher{
		props: propsWithNCtx(0),
		models: &llamaapi.Models{Data: []llamaapi.ModelInfo{
			{ID: "m:loaded", Status: llamaapi.ModelStatus{Value: "loaded"}},
			{ID: "m:unloaded", Status: llamaapi.ModelStatus{Value: "unloaded"}},
		}},
		health: &llamaapi.Health{Status: "ok"},
		slotsFor: map[string]*llamaapi.Slots{
			"m:loaded": {ContextUsed: 4222, ContextMax: 65536, BusyCount: 1},
		},
	}
	r := newRouterTestRunMode(fake)
	// Round 1 (startup): populates the model list.
	_, cmds := r.Update(propsFetchedMsg{nctx: 0})
	for _, sub := range collectCmds(cmds) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if fake.slotsForCalls != 0 {
		t.Fatalf("slots fetched in round 1 (model list not known yet): %d", fake.slotsForCalls)
	}
	// Round 2 (tick): per-model slots for the loaded model only.
	_, cmd := r.Update(livePollTickMsg(time.Now()))
	for _, sub := range collectCmds(cmd) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if fake.slotsForCalls != 1 {
		t.Errorf("FetchSlotsFor calls = %d, want 1 (loaded model only)", fake.slotsForCalls)
	}
	st, ok := r.routerStats["m:loaded"]
	if !ok || st == nil || st.contextUsed != 4222 || st.contextMax != 65536 {
		t.Errorf("routerStats[m:loaded] = %+v, want ctx 4222/65536", st)
	}
}

// TestRouterPanelShowsSlotStats verifies the per-model stats suffix:
// context usage and idle/processing activity from /slots?model=<id>.
func TestRouterPanelShowsSlotStats(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerModels = []llamaapi.ModelInfo{
		{ID: "m:busy", Status: llamaapi.ModelStatus{Value: "loaded"}},
		{ID: "m:quiet", Status: llamaapi.ModelStatus{Value: "loaded"}},
	}
	r.routerStats = map[string]*modelStats{
		"m:busy":  {statsView: statsView{contextUsed: 4222, contextMax: 65536, busyCount: 1}},
		"m:quiet": {statsView: statsView{contextUsed: 150000, contextMax: 150000}},
	}
	got := stripANSI(r.renderRouterPanel(60))
	for _, want := range []string{"4.2K/65.5K", "processing", "150K/150K", "idle"} {
		if !strings.Contains(got, want) {
			t.Errorf("panel missing %q; panel:\n%s", want, got)
		}
	}
}

func TestTruncateRune(t *testing.T) {
	if got := truncateRune("short", 40); got != "short" {
		t.Errorf("truncateRune short = %q", got)
	}
	if got := truncateRune("abcdefghij", 5); got != "abcde…" {
		t.Errorf("truncateRune 10->5 = %q", got)
	}
}

func TestHumanTokens(t *testing.T) {
	cases := map[int]string{
		0: "0", 950: "950", 4222: "4.2K", 65536: "65.5K", 150000: "150K",
	}
	for in, want := range cases {
		if got := humanTokens(in); got != want {
			t.Errorf("humanTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

// TestRouterPollFetchesMetricsForLoadedModels verifies the router poll
// fetches /metrics?model=<id> for loaded models while metrics are
// available (llamaman always spawns routers with --metrics now).
func TestRouterPollFetchesMetricsForLoadedModels(t *testing.T) {
	fake := &fakeFetcher{
		props: propsWithNCtx(0),
		models: &llamaapi.Models{Data: []llamaapi.ModelInfo{
			{ID: "m:loaded", Status: llamaapi.ModelStatus{Value: "loaded"}},
			{ID: "m:unloaded", Status: llamaapi.ModelStatus{Value: "unloaded"}},
		}},
		health: &llamaapi.Health{Status: "ok"},
		slotsFor: map[string]*llamaapi.Slots{
			"m:loaded": {ContextUsed: 4222, ContextMax: 65536, BusyCount: 1},
		},
		metricsFor: map[string]*llamaapi.Metrics{
			"m:loaded": {PredictedTokensSecondsAvg: 12.3},
		},
	}
	r := newRouterTestRunMode(fake)
	// Round 1 (startup): populates the model list.
	_, cmds := r.Update(propsFetchedMsg{nctx: 0})
	for _, sub := range collectCmds(cmds) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	// Round 2 (tick): per-model slots + metrics for the loaded model.
	_, cmd := r.Update(livePollTickMsg(time.Now()))
	for _, sub := range collectCmds(cmd) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if fake.metricsForCalls != 1 {
		t.Errorf("FetchMetricsFor calls = %d, want 1 (loaded model only)", fake.metricsForCalls)
	}
	mm, ok := r.routerStats["m:loaded"]
	if !ok || mm == nil || mm.avgTokensPerSec != 12.3 {
		t.Errorf("routerStats[m:loaded] = %+v", mm)
	}
	// Rate shows in the panel.
	got := stripANSI(r.renderRouterPanel(60))
	if !strings.Contains(got, "12.3 tok/s") {
		t.Errorf("panel missing rate; panel:\n%s", got)
	}
}

// TestRouterMetricsNotEnabledStopsPolling verifies the 501 sentinel
// (router spawned without --metrics, e.g. an older process) stops the
// per-model metrics polling while slots-based stats keep working.
func TestRouterMetricsNotEnabledStopsPolling(t *testing.T) {
	fake := &fakeFetcher{
		props: propsWithNCtx(0),
		models: &llamaapi.Models{Data: []llamaapi.ModelInfo{
			{ID: "m:loaded", Status: llamaapi.ModelStatus{Value: "loaded"}},
		}},
		health: &llamaapi.Health{Status: "ok"},
		slotsFor: map[string]*llamaapi.Slots{
			"m:loaded": {ContextUsed: 10, ContextMax: 100},
		},
		metricsForErr: llamaapi.ErrMetricsNotEnabled,
	}
	r := newRouterTestRunMode(fake)
	_, cmds := r.Update(propsFetchedMsg{nctx: 0})
	for _, sub := range collectCmds(cmds) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	_, cmd := r.Update(livePollTickMsg(time.Now()))
	for _, sub := range collectCmds(cmd) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if r.routerMetricsAvailable {
		t.Error("routerMetricsAvailable still true after ErrMetricsNotEnabled")
	}
	// One more tick: no further metrics fetches.
	before := fake.metricsForCalls
	_, cmd = r.Update(livePollTickMsg(time.Now()))
	for _, sub := range collectCmds(cmd) {
		if msg := safeCmd(sub); msg != nil {
			r, _ = r.Update(msg)
		}
	}
	if fake.metricsForCalls != before {
		t.Errorf("metrics fetched after sentinel: %d -> %d", before, fake.metricsForCalls)
	}
	if fake.slotsForCalls == 0 {
		t.Error("slots polling must continue after metrics sentinel")
	}
}

// TestRouterPanelShowsRateOnlyWhenMetricsPresent pins that the tok/s
// suffix appears only when the model has metrics data.
func TestRouterPanelShowsRateOnlyWhenMetricsPresent(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerModels = []llamaapi.ModelInfo{
		{ID: "m:with-metrics", Status: llamaapi.ModelStatus{Value: "loaded"}},
		{ID: "m:no-metrics", Status: llamaapi.ModelStatus{Value: "loaded"}},
	}
	r.routerStats = map[string]*modelStats{
		"m:with-metrics": {statsView: statsView{contextUsed: 1000, contextMax: 2000, avgTokensPerSec: 27.5}},
		"m:no-metrics":   {statsView: statsView{contextUsed: 1000, contextMax: 2000}},
	}
	got := stripANSI(r.renderRouterPanel(60))
	if !strings.Contains(got, "27.5 tok/s") {
		t.Errorf("panel missing rate for m:with-metrics; panel:\n%s", got)
	}
	if strings.Count(got, "tok/s") != 1 {
		t.Errorf("tok/s shown %d times, want 1; panel:\n%s", strings.Count(got, "tok/s"), got)
	}
}

// TestRouterFocusCyclesModels verifies `m` cycles the focused model
// through loaded models (wrapping) and Esc clears the focus.
func TestRouterFocusCyclesModels(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerModels = []llamaapi.ModelInfo{
		{ID: "a", Status: llamaapi.ModelStatus{Value: "loaded"}},
		{ID: "b", Status: llamaapi.ModelStatus{Value: "unloaded"}},
		{ID: "c", Status: llamaapi.ModelStatus{Value: "loaded"}},
	}
	r.cycleRouterFocus()
	if r.routerFocus != "a" {
		t.Errorf("focus after first m = %q, want a", r.routerFocus)
	}
	r.cycleRouterFocus()
	if r.routerFocus != "c" {
		t.Errorf("focus after second m = %q, want c (skips unloaded b)", r.routerFocus)
	}
	r.cycleRouterFocus()
	if r.routerFocus != "a" {
		t.Errorf("focus after third m = %q, want a (wraps)", r.routerFocus)
	}
	// Esc clears the focus back to the model list.
	r.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if r.routerFocus != "" {
		t.Errorf("focus after Esc = %q, want cleared", r.routerFocus)
	}
	// No loaded models → no-op.
	r2 := newRouterTestRunMode(&fakeFetcher{})
	r2.routerModels = []llamaapi.ModelInfo{{ID: "x", Status: llamaapi.ModelStatus{Value: "unloaded"}}}
	r2.cycleRouterFocus()
	if r2.routerFocus != "" {
		t.Errorf("focus with no loaded model = %q, want empty", r2.routerFocus)
	}
}

// TestRouterStatsPanelFullParity verifies the focused model's panel
// renders the full seven-row statistics — full parity with the
// single-model server panel, including TTFT and sparkline rows.
func TestRouterStatsPanelFullParity(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.routerFocus = "m:big"
	r.routerMetricsAvailable = true
	st := &modelStats{statsView: statsView{
		metricsAvailable:    true,
		tokensHistory:       newRingBuffer(sparkBufferSamples),
		promptHistory:       newRingBuffer(sparkBufferSamples),
		currentTokensPerSec: 12.3,
		avgTokensPerSec:     10.0,
		tokensSeen:          true,
		avgPromptPerSec:     500,
		promptSeen:          true,
		busyCount:           1,
		totalSlots:          1,
		queuedCount:         0,
		contextUsed:         4263,
		contextMax:          65536,
		contextCacheHit:     4000,
		contextPromptToks:   4222,
		contextGenToks:      41,
		genDecoded:          41,
		genRemain:           100,
		promptToksTotal:     4222,
		promptToksProcessed: 4181,
		ttft:                1200 * time.Millisecond,
	}}
	r.routerStats = map[string]*modelStats{"m:big": st}

	got := stripANSI(r.renderRouterStatsPanel(120))
	for _, want := range []string{
		"Tokens", "Prompt", "Process", "Context", "Breakdown", "Cache", "Gen",
		"12.3 tps", "10.0 avg", "500.0 avg", "Busy 1/1 slots",
		"4181/4222", "4263/65K", "4222 prompt", "41 gen",
		"TTFT", "1.2s",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("stats panel missing %q; panel:\n%s", want, got)
		}
	}
	// The list view is replaced while focused.
	if list := stripANSI(r.renderRouterPanel(120)); !strings.Contains(list, "(no models reported)") {
		t.Errorf("list view should be the fallback only; got:\n%s", list)
	}
}

// TestRouterApplyMetricsDeltas verifies per-model current-rate deltas
// and sparkline history accumulate exactly like the single-model path.
func TestRouterApplyMetricsDeltas(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	r.applyRouterMetrics("m", &llamaapi.Metrics{
		PredictedTokensSecondsAvg: 10,
		TokensPredictedTotal:      1000, TokensPredictedSecondsTotal: 100,
		PromptTokensTotal: 500, PromptSecondsTotal: 50,
	})
	st := r.routerStats["m"]
	if st == nil || st.avgTokensPerSec != 10 {
		t.Fatalf("first metrics not applied: %+v", st)
	}
	if st.currentTokensPerSec != 0 || len(st.tokensHistory.Snapshot()) != 0 {
		t.Errorf("first tick must not compute deltas; current=%v", st.currentTokensPerSec)
	}
	r.applyRouterMetrics("m", &llamaapi.Metrics{
		PredictedTokensSecondsAvg: 12,
		TokensPredictedTotal:      2000, TokensPredictedSecondsTotal: 150,
		PromptTokensTotal: 700, PromptSecondsTotal: 60,
	})
	if st.currentTokensPerSec != 20 { // 1000 tokens / 50 server-seconds
		t.Errorf("currentTokensPerSec = %v, want 20", st.currentTokensPerSec)
	}
	if st.currentPromptPerSec != 20 { // 200 tokens / 10 server-seconds
		t.Errorf("currentPromptPerSec = %v, want 20", st.currentPromptPerSec)
	}
	if !st.tokensSeen || len(st.tokensHistory.Snapshot()) == 0 || len(st.promptHistory.Snapshot()) == 0 {
		t.Errorf("history not accumulated: tokensSeen=%v", st.tokensSeen)
	}
}

// TestRouterApplySlotsTTFT verifies per-model TTFT tracking mirrors the
// single-model handler: measured from request start to first token.
func TestRouterApplySlotsTTFT(t *testing.T) {
	r := newRouterTestRunMode(&fakeFetcher{})
	// Request starts: prompt tokens appear, no gen yet.
	r.applyRouterSlots("m", &llamaapi.Slots{PromptTokensTotal: 100, PromptTokensProcessed: 0})
	st := r.routerStats["m"]
	if st.ttftStart.IsZero() {
		t.Fatal("ttftStart not armed on new request")
	}
	// First token appears.
	r.applyRouterSlots("m", &llamaapi.Slots{PromptTokensTotal: 100, PromptTokensProcessed: 100, GenDecoded: 1})
	if st.ttft <= 0 {
		t.Errorf("ttft = %v, want > 0", st.ttft)
	}
	// Request completes → reset.
	r.applyRouterSlots("m", &llamaapi.Slots{})
	if !st.ttftStart.IsZero() || st.ttft != 0 || st.ttftPrevPromptToks != 0 {
		t.Errorf("ttft state not reset: start=%v ttft=%v prev=%d", st.ttftStart, st.ttft, st.ttftPrevPromptToks)
	}
}

// TestRunHeaderStateMachine exercises the two width breakpoints in
// one place and pins the cell count + live-band visibility at each.
// 6 identity cells stay in the same source order across modes; only
// the wordmark and live band toggle.
func TestRunHeaderStateMachine(t *testing.T) {
	model := config.Model{Alias: "alpha"}
	preset := config.Preset{Name: "default"}

	cases := []struct {
		name     string
		width    int
		wantBand bool
		wantWM   bool
		wantH    int
	}{
		{"wide", runHeaderWideWidth, true, true, headerHeightWithWordmark + liveBandHeight},
		{"compact", runHeaderCompactWidth, false, false, headerHeight},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newHeaderTestRunMode(model, preset, nil, tc.width)
			header := r.renderHeader()
			plain := stripANSI(header)
			gotH := strings.Count(header, "\n") + 1
			if gotH != tc.wantH {
				t.Errorf("height = %d, want %d\nheader:\n%s", gotH, tc.wantH, header)
			}
			gotWM := strings.Contains(plain, "▐  ▐")
			if gotWM != tc.wantWM {
				t.Errorf("wordmark visible = %v, want %v\nheader:\n%s", gotWM, tc.wantWM, plain)
			}
			gotBand := strings.Contains(plain, "llama-server") && strings.Contains(plain, "Hardware")
			if gotBand != tc.wantBand {
				t.Errorf("live band visible = %v, want %v\nheader:\n%s", gotBand, tc.wantBand, plain)
			}
			// Identity cells must be present in every state.
			for _, want := range []string{"alpha", "default", "Context Size", "[READY]"} {
				if !strings.Contains(plain, want) {
					t.Errorf("identity cell %q missing\nheader:\n%s", want, plain)
				}
			}
		})
	}
}

// TestRunHeaderDropsSamplingParamsAndMetricsIndicator pins Phase 0:
// the four sampling-param cells (Temp/Top_P/Top_K/Min_P) and the
// [Metrics] indicator have been removed from the header. They live in
// the `i` info overlay (Phase 1) and the live-band server panel
// (Phase 3) respectively.
func TestRunHeaderDropsSamplingParamsAndMetricsIndicator(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "temp", Value: json.Number("0.7")},
			{Key: "top-p", Value: json.Number("0.9")},
			{Key: "top-k", Value: json.Number("40")},
			{Key: "min-p", Value: json.Number("0.05")},
			{Key: "metrics", Value: true},
		}},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderHeader())
	for _, dont := range []string{"Temp:", "Top_P:", "Top_K:", "Min_P:", "[Metrics]"} {
		if strings.Contains(plain, dont) {
			t.Errorf("header should not contain %q after Phase 0; got:\n%s", dont, plain)
		}
	}
}

// TestRunHeaderShowsWordmarkAtWideWidth confirms the wordmark is
// rendered on the left side of the box when the terminal can fit it.
// The smblock wordmark uses several distinct quad-pixel block glyphs;
// we look for a substring stable across the asset (the doubled "▐  ▐"
// L-pair on row 2 is unambiguous).
func TestRunHeaderShowsWordmarkAtWideWidth(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "▐  ▐") {
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
		nil, 60,
	)
	plain := stripANSI(r.renderHeader())
	if strings.Contains(plain, "▐  ▐") {
		t.Errorf("expected no wordmark on narrow terminal; got:\n%s", plain)
	}
}

func TestRunHeaderShowsNAForMissingCtxSize(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"}, // no params at all
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderHeader())
	if !strings.Contains(plain, "Context Size: n/a") {
		t.Errorf("expected Context Size: n/a fallback in header; got:\n%s", plain)
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
		cfg:           cfg,
		model:         config.Model{Alias: "alpha"},
		preset:        config.Preset{Name: "default"},
		proc:          &server.Process{Started: time.Now()},
		viewport:      viewport.New(0, 0),
		searchInput:   textinput.New(),
		theme:         CurrentTheme(),
		status:        StatusReady,
		tokensHistory: newRingBuffer(sparkBufferSamples),
		promptHistory: newRingBuffer(sparkBufferSamples),
		utilHistory:   map[string]*ringBuffer{},
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

// ---- Phase 4: hardware-panel tests ----

// TestHardwarePanelRendersCPUOnly covers the minimum case: one CPU
// device, no GPUs (the typical CI / non-NVIDIA dev box). Both the
// device header line and the value row should appear.
func TestHardwarePanelRendersCPUOnly(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.hardware = []hwinfo.Device{
		{
			Class: hwinfo.ClassCPU, Index: 0, Name: "AMD Ryzen 9 7950X",
			UtilPct: 23, MemPct: 65,
			MemUsedBytes: 41 * 1024 * 1024 * 1024, MemTotalBytes: 64 * 1024 * 1024 * 1024,
			PowerW: 120, PowerMaxW: 125, HasPower: true,
			TempC: 68, TempMaxC: 100, HasTemp: true,
			FanRPM: 1200, HasFan: true,
		},
	}
	plain := stripANSI(r.renderHardwarePanel(120))
	// T4 layout: name row, then 4 metric rows (Util / RAM / Power / Temp).
	for _, want := range []string{"[0]", "AMD Ryzen 9 7950X", "Util", "RAM", "Power", "Temp", "120W", "125W", "68°C", "100°C", "1200rpm"} {
		if !strings.Contains(plain, want) {
			t.Errorf("Hardware panel missing %q\nout:\n%s", want, plain)
		}
	}
}

// TestHardwarePanelRendersCPUAndGPU covers the typical desktop
// rendering with 1 CPU + 1 GPU. Each device gets its own [N] index
// within its class, so we expect [0] twice.
func TestHardwarePanelRendersCPUAndGPU(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.hardware = []hwinfo.Device{
		{
			Class: hwinfo.ClassCPU, Index: 0, Name: "AMD Ryzen 9 7950X",
			UtilPct: 23, MemPct: 65,
			MemUsedBytes: 41 * 1024 * 1024 * 1024, MemTotalBytes: 64 * 1024 * 1024 * 1024,
		},
		{
			Class: hwinfo.ClassGPU, Index: 0, Name: "NVIDIA GeForce RTX 4090",
			UtilPct: 89, MemPct: 78,
			MemUsedBytes: 18 * 1024 * 1024 * 1024, MemTotalBytes: 24 * 1024 * 1024 * 1024,
			PowerW: 320, PowerMaxW: 450, HasPower: true,
			TempC: 72, TempMaxC: 83, HasTemp: true,
			FanPct: 65, HasFan: true,
		},
	}
	plain := stripANSI(r.renderHardwarePanel(120))
	for _, want := range []string{"AMD Ryzen 9 7950X", "NVIDIA GeForce RTX 4090", "RAM", "VRAM", "320W", "450W", "72°C", "Fan "} {
		if !strings.Contains(plain, want) {
			t.Errorf("Hardware panel missing %q\nout:\n%s", want, plain)
		}
	}
	// Two devices = two [0] markers (per-class indexing).
	if got := strings.Count(plain, "[0]"); got != 2 {
		t.Errorf("expected per-class [0] markers (one per device); got %d\nout:\n%s", got, plain)
	}
}

// TestHardwarePanelOmitsFanWhenUnavailable covers Bug 6: when a CPU
// reports no fan reading, the Fan slot is omitted entirely (not
// rendered as "n/a"). The Temp row's tail just stops after the bar.
func TestHardwarePanelOmitsFanWhenUnavailable(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.hardware = []hwinfo.Device{
		{
			Class: hwinfo.ClassCPU, Index: 0, Name: "Generic CPU",
			UtilPct: 5, MemPct: 12,
			MemUsedBytes: 8 * 1024 * 1024 * 1024, MemTotalBytes: 64 * 1024 * 1024 * 1024,
			TempC: 50, TempMaxC: 100, HasTemp: true,
			// no fan, no power
		},
	}
	plain := stripANSI(r.renderHardwarePanel(120))
	if strings.Contains(plain, "n/a") {
		t.Errorf("missing fields must be omitted, not rendered as n/a; out:\n%s", plain)
	}
	if strings.Contains(plain, "Fan ") {
		t.Errorf("Fan slot must be entirely omitted when no fan reading; out:\n%s", plain)
	}
}

// TestViewHeightStableAndNoLineExceedsWidth is the regression for
// the "live band duplicates and shifts upward as new log lines
// arrive" bug. The user reported it after the T4 redesign; root
// cause was that long log lines wrapped inside the bordered
// log-frame box (lipgloss auto-wraps when Width is set), growing
// the rendered chrome past r.height. Bubble Tea then overwrote
// frames at the wrong row, leaving the band visually drifting up
// each tick. We now pre-truncate viewport content to
// viewport.Width (so lipgloss can't wrap it) and clamp every View
// line to r.width as a defensive last pass.
//
// The test feeds long log lines into the buffer and asserts
// (1) View() always returns exactly r.height lines and (2) no
// individual line exceeds r.width visual cols.
func TestViewHeightStableAndNoLineExceedsWidth(t *testing.T) {
	cfg := &config.Config{Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	r := &RunMode{
		cfg:           cfg,
		model:         config.Model{Alias: "alpha"},
		preset:        config.Preset{Name: "default"},
		proc:          &server.Process{Started: time.Now()},
		viewport:      viewport.New(0, 0),
		searchInput:   textinput.New(),
		theme:         CurrentTheme(),
		status:        StatusReady,
		tokensHistory: newRingBuffer(sparkBufferSamples),
		promptHistory: newRingBuffer(sparkBufferSamples),
		utilHistory:   map[string]*ringBuffer{},
	}
	const W, H = 200, 40
	r.SetSize(W, H)
	// Hardware populated with both CPU + GPU.
	r.hardware = []hwinfo.Device{
		{
			Class: hwinfo.ClassCPU, Index: 0, Name: "AMD Ryzen 9 7950X",
			UtilPct: 23, MemPct: 65,
			MemUsedBytes: 41 * 1024 * 1024 * 1024, MemTotalBytes: 64 * 1024 * 1024 * 1024,
			PowerW: 32, PowerMaxW: 125, HasPower: true,
			TempC: 68, TempMaxC: 100, HasTemp: true,
		},
		{
			Class: hwinfo.ClassGPU, Index: 0, Name: "NVIDIA GeForce RTX 4090",
			UtilPct: 89, MemPct: 78,
			MemUsedBytes: 18 * 1024 * 1024 * 1024, MemTotalBytes: 24 * 1024 * 1024 * 1024,
			PowerW: 320, PowerMaxW: 450, HasPower: true,
			TempC: 72, TempMaxC: 83, HasTemp: true,
			FanPct: 65, HasFan: true,
		},
	}
	for i := 0; i < 10; i++ {
		// Log lines of varying lengths, including one wider than
		// viewport.Width to trigger the historical wrap bug.
		r.buf.WriteString("main: " + strings.Repeat("x", 250) + " #" + string(rune('a'+i%26)) + "\n")
		r.viewport.SetContent(r.buf.String())
		r.viewport.GotoBottom()
		v := r.View()
		gotH := strings.Count(v, "\n") + 1
		if gotH != H {
			t.Fatalf("after log chunk %d: View height = %d, want %d (panel-duplication regression)",
				i, gotH, H)
		}
		for j, line := range strings.Split(v, "\n") {
			if w := len([]rune(stripANSI(line))); w > W {
				t.Fatalf("after log chunk %d, line %d width = %d > terminal W = %d (will trigger terminal wrap)",
					i, j, w, W)
			}
		}
	}
}

// TestHardwarePanelAlignsBarsAndSparks pins the alignment guarantee:
// every metric row's viz column starts at the same character index.
// Regression catch — the user explicitly called out misaligned bars
// in the previous design.
func TestHardwarePanelAlignsBarsAndSparks(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.hardware = []hwinfo.Device{
		{
			Class: hwinfo.ClassCPU, Index: 0, Name: "CPU",
			UtilPct: 23, MemPct: 65,
			MemUsedBytes: 41 * 1024 * 1024 * 1024, MemTotalBytes: 64 * 1024 * 1024 * 1024,
			PowerW: 32, PowerMaxW: 125, HasPower: true,
			TempC: 68, TempMaxC: 100, HasTemp: true,
		},
	}
	plain := stripANSI(r.renderHardwarePanel(120))
	lines := strings.Split(plain, "\n")
	// Find the Util / RAM / Power / Temp lines. Each one's viz column
	// (the bar/spark) should start at the same byte offset relative
	// to the metric label (which is right-padded to the same width).
	want := map[string]int{}
	for _, label := range []string{"Util ", "RAM  ", "Power", "Temp "} {
		for _, line := range lines {
			if idx := strings.Index(line, label); idx >= 0 {
				want[label] = idx
				break
			}
		}
	}
	if len(want) != 4 {
		t.Fatalf("could not find all metric labels in rendered panel:\n%s", plain)
	}
	first := -1
	for _, idx := range want {
		if first == -1 {
			first = idx
			continue
		}
		if idx != first {
			t.Errorf("metric labels not column-aligned: got positions %v in panel:\n%s", want, plain)
			break
		}
	}
}

// TestHardwarePanelValueColumnAligned pins the new "values to the
// right of the bar, to the left of the %" requirement (last
// grilling round). The current/max value (RAM bytes, Power W, Temp
// °C) sits in a fixed-width column; the trailing % must therefore
// land at the same column on every metric row.
func TestHardwarePanelValueColumnAligned(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.hardware = []hwinfo.Device{
		{
			Class: hwinfo.ClassCPU, Index: 0, Name: "CPU",
			UtilPct: 23, MemPct: 65,
			MemUsedBytes: 41 * 1024 * 1024 * 1024, MemTotalBytes: 64 * 1024 * 1024 * 1024,
			PowerW: 32, PowerMaxW: 125, HasPower: true,
			TempC: 68, TempMaxC: 100, HasTemp: true,
		},
	}
	plain := stripANSI(r.renderHardwarePanel(120))
	lines := strings.Split(plain, "\n")
	// Each metric row should end (sans trailing whitespace) with the
	// trailing %. Find the % position on each row in VISUAL columns
	// (not bytes — ▆ is 3 bytes UTF-8 but 1 visual col) and assert
	// they line up.
	pctVisualCol := map[string]int{}
	for _, label := range []string{"Util ", "RAM  ", "Power", "Temp "} {
		for _, line := range lines {
			if !strings.Contains(line, label) {
				continue
			}
			runes := []rune(line)
			lastPct := -1
			for i, r := range runes {
				if r == '%' {
					lastPct = i
				}
			}
			if lastPct >= 0 {
				pctVisualCol[label] = lastPct
			}
			break
		}
	}
	if len(pctVisualCol) != 4 {
		t.Fatalf("could not find percent positions on all rows: %v\npanel:\n%s", pctVisualCol, plain)
	}
	first := -1
	for _, p := range pctVisualCol {
		if first == -1 {
			first = p
			continue
		}
		if p != first {
			t.Errorf("percent column not aligned across metric rows: %v\npanel:\n%s", pctVisualCol, plain)
			break
		}
	}
}

// TestHardwarePanelEmptyShowsPlaceholder pins the no-devices fallback
// (gopsutil failed entirely + no NVML). Renders a placeholder so the
// panel's bordered shape stays consistent.
func TestHardwarePanelEmptyShowsPlaceholder(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.hardware = nil
	plain := stripANSI(r.renderHardwarePanel(80))
	if !strings.Contains(plain, "no devices") {
		t.Errorf("expected (no devices…) placeholder; out:\n%s", plain)
	}
}

// TestHwSnapshotMsgUpdatesField pins the wiring path: hwSnapshotMsg
// hits Update and lands on r.hardware.
func TestHwSnapshotMsgUpdatesField(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	devs := []hwinfo.Device{{Class: hwinfo.ClassCPU, Index: 0, Name: "Probe"}}
	r.Update(hwSnapshotMsg{devices: devs})
	if len(r.hardware) != 1 || r.hardware[0].Name != "Probe" {
		t.Errorf("hwSnapshotMsg did not land in r.hardware; got %+v", r.hardware)
	}
}

// ---- Phase 3: live server-panel tests ----

// TestApplyMetricsFirstTickPublishesGaugesOnly pins the rule that the
// first /metrics fetch after startup has no prev to delta against, so
// we publish only the lifetime gauges. tokensSeen stays false until
// the second tick yields a non-zero delta — the renderer shows "—"
// in that window (Bug 3 semantics).
func TestApplyMetricsFirstTickPublishesGaugesOnly(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	r.applyMetrics(&llamaapi.Metrics{
		TokensPredictedTotal:        100,
		TokensPredictedSecondsTotal: 2,
		PredictedTokensSecondsAvg:   60,
		PromptTokensSecondsAvg:      2300,
		RequestsDeferred:            3,
	})
	if r.tokensSeen {
		t.Error("tokensSeen = true after first tick; want false (no delta yet)")
	}
	if r.avgTokensPerSec != 60 {
		t.Errorf("avgTokensPerSec = %v, want 60", r.avgTokensPerSec)
	}
	if r.queuedCount != 3 {
		t.Errorf("queuedCount = %d, want 3", r.queuedCount)
	}
}

// TestApplyMetricsSecondTickComputesRate is the core delta math: the
// second tick should produce currentTokensPerSec = ΔTokens / ΔSeconds
// based on the prev counters, not the cumulative absolutes.
func TestApplyMetricsSecondTickComputesRate(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	r.applyMetrics(&llamaapi.Metrics{
		TokensPredictedTotal:        100,
		TokensPredictedSecondsTotal: 2,
	})
	r.applyMetrics(&llamaapi.Metrics{
		TokensPredictedTotal:        180,
		TokensPredictedSecondsTotal: 3,
	})
	// Δtokens = 80, Δsecs = 1 → 80 tokens/s
	if r.currentTokensPerSec != 80 {
		t.Errorf("currentTokensPerSec = %v, want 80", r.currentTokensPerSec)
	}
	if !r.tokensSeen {
		t.Error("tokensSeen = false after non-zero delta; want true (latched)")
	}
}

// TestApplyMetricsFallsBackToWallClockWhenSecondsCounterStuck pins
// the responsiveness fix: some llama-server builds only increment
// `tokens_predicted_seconds_total` at slot completion. During a long
// generation, dTokens grows but dTokenSecs stays zero — and our
// rate stayed `—` until the response finished. The fallback uses
// wall-clock seconds (the 1s poll interval) so a value appears the
// next tick even when the seconds counter is lagging.
func TestApplyMetricsFallsBackToWallClockWhenSecondsCounterStuck(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	r.applyMetrics(&llamaapi.Metrics{TokensPredictedTotal: 100, TokensPredictedSecondsTotal: 2})
	// Tokens advance by 50 but the seconds counter is stuck at 2
	// (mid-generation, slot hasn't completed yet).
	r.applyMetrics(&llamaapi.Metrics{TokensPredictedTotal: 150, TokensPredictedSecondsTotal: 2})
	if !r.tokensSeen {
		t.Error("tokensSeen = false; want true via wall-clock fallback")
	}
	// Wall-clock fallback: 50 tokens / 1s poll interval = 50 tokens/s.
	if r.currentTokensPerSec != 50 {
		t.Errorf("currentTokensPerSec = %v, want 50 (wall-clock)", r.currentTokensPerSec)
	}
}

// TestApplyMetricsPersistsLastValueOnZeroDelta covers Bug 3: once
// tokensSeen latches true, subsequent zero-delta ticks must NOT
// reset currentTokensPerSec to 0. The user wants the last-known
// value to persist so the trailing column doesn't flash to "—" in
// between bursts.
func TestApplyMetricsPersistsLastValueOnZeroDelta(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	r.applyMetrics(&llamaapi.Metrics{TokensPredictedTotal: 100, TokensPredictedSecondsTotal: 2})
	r.applyMetrics(&llamaapi.Metrics{TokensPredictedTotal: 180, TokensPredictedSecondsTotal: 3})
	// Now feed a zero-delta tick (server idle for one second).
	r.applyMetrics(&llamaapi.Metrics{TokensPredictedTotal: 180, TokensPredictedSecondsTotal: 3})
	if !r.tokensSeen {
		t.Error("tokensSeen got cleared on zero-delta tick; want it to remain latched")
	}
	if r.currentTokensPerSec != 80 {
		t.Errorf("currentTokensPerSec = %v on zero-delta tick; want 80 (persist last-known)", r.currentTokensPerSec)
	}
}

// TestServerPanelRendersLiveData covers the SP3 layout: rate cell
// (current / avg /s) shows after the spark, secondary scalar
// (Busy/Queued) at the row end. With tokensSeen + currentTokensPerSec
// + avgTokensPerSec set, the panel reads both rate values cleanly.
func TestServerPanelRendersLiveData(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	r.currentTokensPerSec = 80
	r.avgTokensPerSec = 60
	r.tokensSeen = true
	r.busyCount = 2
	r.totalSlots = 4
	r.queuedCount = 1
	// Width 140 fits the 40-cell spark + label + rate cell + busy
	// scalar without truncating any content.
	plain := stripANSI(r.renderServerPanel(140))
	for _, want := range []string{"Tokens", "80.0", "60.0", "Busy", "2/4 slots", "Queued", "1"} {
		if !strings.Contains(plain, want) {
			t.Errorf("server panel missing %q\nout:\n%s", want, plain)
		}
	}
}

// TestServerPanelMetricsDisabledShowsNA covers the --metrics-off
// fallback: rate value reads n/a; busy still works from /slots.
func TestServerPanelMetricsDisabledShowsNA(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = false
	r.busyCount = 1
	r.totalSlots = 2
	plain := stripANSI(r.renderServerPanel(140))
	if !strings.Contains(plain, "n/a") {
		t.Errorf("expected n/a for tokens/s when --metrics off; out:\n%s", plain)
	}
	if !strings.Contains(plain, "1/2 slots") {
		t.Errorf("Busy slots should still render from /slots; out:\n%s", plain)
	}
}

// TestServerPanelShowsDashBeforeFirstNonZero covers Bug 3 init: when
// metrics are available but we've never observed a non-zero rate,
// the trailing rate cell shows "—".
func TestServerPanelShowsDashBeforeFirstNonZero(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	// tokensSeen and promptSeen both default false.
	plain := stripANSI(r.renderServerPanel(140))
	if !strings.Contains(plain, "—") {
		t.Errorf("expected dash before first non-zero rate; out:\n%s", plain)
	}
}

// TestRunModeMetricsNotEnabledFlipsFlag confirms the
// metricsFetchedMsg handler stops polling /metrics on the sentinel
// error and emits the one-time INFO log.
func TestRunModeMetricsNotEnabledFlipsFlag(t *testing.T) {
	logs := captureSlog(t)
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.metricsAvailable = true
	r.Update(metricsFetchedMsg{err: llamaapi.ErrMetricsNotEnabled})
	if r.metricsAvailable {
		t.Error("metricsAvailable still true after ErrMetricsNotEnabled")
	}
	assertLogged(t, logs, slog.LevelInfo, "/metrics endpoint disabled — preset lacks metrics: true")
}

// TestRunModeSlotsFetchedUpdatesCounts pins the slotsFetchedMsg
// handler: busy/total land on the run-mode struct.
func TestRunModeSlotsFetchedUpdatesCounts(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.Update(slotsFetchedMsg{s: &llamaapi.Slots{Total: 4, BusyCount: 3}})
	if r.busyCount != 3 || r.totalSlots != 4 {
		t.Errorf("busy/total = %d/%d, want 3/4", r.busyCount, r.totalSlots)
	}
}

// ---- i info overlay tests ----

// TestRunInfoOverlayTogglesOnIKey covers the basic press-i-shows,
// press-any-shows-closes flow. Direct field assertions (showInfo)
// rather than view scraping so the test is independent of the
// overlay's pixel layout.
func TestRunInfoOverlayTogglesOnIKey(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha", Location: "/m/alpha.gguf"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "ctx-size", Value: json.Number("8192")},
		}},
		nil, runHeaderWideWidth,
	)
	r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	if !r.showInfo {
		t.Fatal("`i` did not set showInfo")
	}
	// Any subsequent key closes.
	r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if r.showInfo {
		t.Fatal("subsequent key did not clear showInfo")
	}
}

// TestRunInfoOverlayContentLocalModel pins the rendered body for a
// local-file model: alias + Source path + preset name + every preset
// param surfaces in source order.
func TestRunInfoOverlayContentLocalModel(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha", Location: "/models/alpha.gguf"},
		config.Preset{Name: "fast", Params: config.Params{
			{Key: "ctx-size", Value: json.Number("8192")},
			{Key: "temp", Value: json.Number("0.7")},
			{Key: "jinja", Value: true},
		}},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderInfoOverlay())
	for _, want := range []string{
		"alpha",
		"/models/alpha.gguf",
		"fast",
		"ctx-size",
		"8192",
		"temp",
		"0.7",
		"jinja",
		"true",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("info overlay missing %q\nout:\n%s", want, plain)
		}
	}
}

// TestRunInfoOverlayContentHFModel covers the HF-sourced model branch:
// the overlay shows `HF :` rather than `Source :`.
func TestRunInfoOverlayContentHFModel(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "qwen", HF: "Qwen/Qwen2.5-7B-Instruct-GGUF"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderInfoOverlay())
	if !strings.Contains(plain, "HF") {
		t.Errorf("HF model overlay missing `HF` label; out:\n%s", plain)
	}
	if !strings.Contains(plain, "Qwen/Qwen2.5-7B-Instruct-GGUF") {
		t.Errorf("HF model overlay missing identifier; out:\n%s", plain)
	}
	if strings.Contains(plain, "Source ") {
		t.Errorf("HF model overlay should not show Source line; out:\n%s", plain)
	}
}

// TestRunInfoOverlayPreservesParamOrder pins the source-order
// invariant: keys appear in the order they were declared, not
// alphabetical or any other rearrangement (CLAUDE.md: "Param order
// matters end-to-end").
func TestRunInfoOverlayPreservesParamOrder(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha", Location: "/m/alpha.gguf"},
		config.Preset{Name: "default", Params: config.Params{
			{Key: "zeta", Value: "z"},
			{Key: "alpha-flag", Value: "a"},
			{Key: "mu", Value: "m"},
		}},
		nil, runHeaderWideWidth,
	)
	plain := stripANSI(r.renderInfoOverlay())
	zIdx := strings.Index(plain, "zeta")
	aIdx := strings.Index(plain, "alpha-flag")
	mIdx := strings.Index(plain, "mu")
	if zIdx < 0 || aIdx < 0 || mIdx < 0 {
		t.Fatalf("missing one or more keys; out:\n%s", plain)
	}
	if !(zIdx < aIdx && aIdx < mIdx) {
		t.Errorf("expected source order zeta < alpha-flag < mu; got positions %d, %d, %d", zIdx, aIdx, mIdx)
	}
}

// TestRunInfoOverlayRenderedInView covers the integration: with
// showInfo set the View output contains the overlay text alongside
// the header (which the overlay floats on top of, not replaces).
func TestRunInfoOverlayRenderedInView(t *testing.T) {
	r := newHeaderTestRunMode(
		config.Model{Alias: "alpha", Location: "/m/alpha.gguf"},
		config.Preset{Name: "default"},
		nil, runHeaderWideWidth,
	)
	r.height = 30
	r.showInfo = true
	view := stripANSI(r.View())
	if !strings.Contains(view, "Model & preset") {
		t.Errorf("View did not include info overlay header; out:\n%s", view)
	}
	if !strings.Contains(view, "Alias") {
		t.Errorf("View overlay missing Alias label; out:\n%s", view)
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
