package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// forceTrueColor renders hex colors verbatim (38;2;r;g;b) so sweep tests
// can assert exact colors, and pins a dark background so the llamaman
// dark accent (#E8A33D) is resolved deterministically (P9). Both are
// restored to the ambient values on cleanup.
func forceTrueColor(t *testing.T) {
	t.Helper()
	lipgloss.SetColorProfile(termenv.TrueColor)
	lipgloss.SetHasDarkBackground(true)
	t.Cleanup(func() {
		lipgloss.SetColorProfile(termenv.ColorProfile())
		lipgloss.SetHasDarkBackground(termenv.HasDarkBackground())
	})
}

// hasTrueColor reports whether s carries the exact truecolor fg SGR
// lipgloss produces for the given hex color. The code is derived by
// rendering a probe with lipgloss rather than computed from the hex:
// termenv converts hex through the xterm-256 cube, so the emitted RGB
// can differ from the literal hex (e.g. #E8A33D → 232;163;60).
func hasTrueColor(s, hex string) bool {
	probe := lipgloss.NewStyle().Foreground(lipgloss.Color(hex)).Render("X")
	start := strings.Index(probe, "38;2;")
	end := strings.Index(probe, "m")
	if start < 0 || end < 0 {
		return false
	}
	return strings.Contains(s, "\x1b["+probe[start:end]+"m")
}

// TestWordmarkRampShape pins the Python Gradient layout of a character's
// highlight scene: base → bright → bright → base with (3, width, 3)
// steps, i.e. 14 colors whose first and last are the base color and
// whose core is the specular color.
func TestWordmarkRampShape(t *testing.T) {
	base := lipgloss.Color("#e8a33d")
	bright := lipgloss.Color("#ffffff")
	ramp := wordmarkRamp(base, bright)
	if len(ramp) != 3+highlightWidth+3 {
		t.Fatalf("ramp length = %d, want %d", len(ramp), 3+highlightWidth+3)
	}
	if ramp[0] != base {
		t.Errorf("ramp[0] = %v, want base", ramp[0])
	}
	for i, c := range ramp {
		if i >= 3 && i < 3+highlightWidth && c != bright {
			t.Errorf("ramp[%d] = %v, want bright core", i, c)
		}
	}
	if ramp[len(ramp)-1] != base {
		t.Errorf("ramp[last] = %v, want base", ramp[len(ramp)-1])
	}
	// Segments interpolate: ramp[1] must sit strictly between base and
	// bright (not equal to either) on a color whose endpoints differ.
	if ramp[1] == base || ramp[1] == bright {
		t.Errorf("ramp[1] = %v, want an intermediate color", ramp[1])
	}
}

// TestBrightenColorScalesLightness verifies the specular color: HSL
// lightness is scaled by the brightness factor (clamped at white) and
// hue/saturation survive.
func TestBrightenColorScalesLightness(t *testing.T) {
	// #808080: HSL lightness 0.502 ×1.75 = 0.878 → #e0e0e0 (scaled, not
	// clamped).
	if got := brightenColor(lipgloss.Color("#808080"), highlightBrightness); got != lipgloss.Color("#e0e0e0") {
		t.Errorf("brighten #808080 = %v, want #e0e0e0", got)
	}
	// #d0d0d0: lightness 0.816 ×1.75 clamps at 1 → white.
	if got := brightenColor(lipgloss.Color("#d0d0d0"), highlightBrightness); got != lipgloss.Color("#ffffff") {
		t.Errorf("brighten #d0d0d0 = %v, want white", got)
	}
	// A color that does not clamp must keep its hue family: orange
	// #E8A33D brightens toward a pale orange/white, never green.
	got := brightenColor(lipgloss.Color("#E8A33D"), highlightBrightness)
	if got == lipgloss.Color("#E8A33D") {
		t.Errorf("brighten #E8A33D unchanged, want lighter")
	}
	// Non-hex colors pass through (graceful degradation, P3).
	if got := brightenColor(lipgloss.Color("not-a-color"), 1.75); got != lipgloss.Color("not-a-color") {
		t.Errorf("non-hex = %v, want passthrough", got)
	}
}

// TestEaseInverseRoundTrip verifies invEaseInOutCirc is the inverse of
// easeInOutCirc across the curve, so band activation times map
// correctly.
func TestEaseInverseRoundTrip(t *testing.T) {
	for _, x := range []float64{0, 0.1, 0.25, 0.5, 0.75, 0.9, 1} {
		got := invEaseInOutCirc(easeInOutCirc(x))
		if got < x-0.001 || got > x+0.001 {
			t.Errorf("inv(ease(%v)) = %v, want ≈%v", x, got, x)
		}
	}
	if got := easeInOutCirc(0); got != 0 {
		t.Errorf("ease(0) = %v, want 0", got)
	}
	if got := easeInOutCirc(1); got != 1 {
		t.Errorf("ease(1) = %v, want 1", got)
	}
}

// sweepWordmark renders just the wordmark at the frozen instant `at`, a
// sweep anchored at a fixed base instant (P9 determinism). Truecolor is
// forced so exact hex colors are assertable.
func sweepWordmark(t *testing.T, cfg *config.Config, at time.Time) string {
	t.Helper()
	forceTrueColor(t)
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	freezeClock(t, base)
	m := NewMainMode(cfg, "v0.0.0-test", DefaultTheme())
	m.RestartWordmark() // anchors at the fixed base instant
	freezeClock(t, at)  // then render at `at` (elapsed = at − base)
	return m.renderWordmark()
}

// TestWordmarkSweepOneShot pins the one-shot behavior with a frozen
// clock: at the start and after completion every glyph is the accent
// base color; mid-sweep a specular band is visible and settled cells
// remain base. #E8A33D ×1.75 clamps to white, so the band is pure
// white on the llamaman default palette.
func TestWordmarkSweepOneShot(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cfg := sampleSnapshotConfig()
	accent := "#e8a33d"

	at := sweepWordmark(t, cfg, base)
	if !hasTrueColor(at, accent) {
		t.Errorf("start frame: wordmark should be all base accent %s", accent)
	}
	if hasTrueColor(at, "#ffffff") {
		t.Errorf("start frame: no specular yet, got bright colors")
	}

	// Mid-sweep: the bright core of the ramp is visible while settled
	// cells still show the accent.
	mid := sweepWordmark(t, cfg, base.Add(wordmarkSweepDur/2))
	if !hasTrueColor(mid, "#ffffff") {
		t.Errorf("mid sweep: expected a specular white band\n%.200s", mid)
	}
	if !hasTrueColor(mid, accent) {
		t.Errorf("mid sweep: settled cells should stay accent %s\n%.200s", accent, mid)
	}

	// After the scene tail the sweep is done: back to flat accent.
	done := sweepWordmark(t, cfg, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	if !hasTrueColor(done, accent) {
		t.Errorf("after completion: wordmark should be all accent %s", accent)
	}
	if hasTrueColor(done, "#ffffff") {
		t.Errorf("after completion: no specular left")
	}

	// The tick follows the same lifecycle: armed while active, nil once
	// the one-shot finishes.
	freezeClock(t, base.Add(time.Millisecond))
	m := NewMainMode(cfg, "v0.0.0-test", DefaultTheme())
	m.RestartWordmark()
	if m.wordmarkTickCmd() == nil {
		t.Error("active sweep should arm a tick")
	}
	freezeClock(t, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	if m.wordmarkTickCmd() != nil {
		t.Error("finished one-shot should stop ticking")
	}
}

// TestWordmarkSweepLoopMode pins loop behavior: the sweep re-animates
// every cycle and rests during the hold window; the tick never stops.
func TestWordmarkSweepLoopMode(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{LogoEffect: config.LogoEffectLoop}
	accent := "#e8a33d"

	// Hold window (between scene tail and next cycle): static accent.
	hold := sweepWordmark(t, cfg, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	if !hasTrueColor(hold, accent) {
		t.Errorf("hold window: expected flat accent")
	}
	if hasTrueColor(hold, "#ffffff") {
		t.Errorf("hold window: no specular expected")
	}

	// One full cycle later, mid-sweep again: animated.
	cycle := wordmarkSweepDur + wordmarkSceneDur + wordmarkLoopHold
	mid2 := sweepWordmark(t, cfg, base.Add(cycle+wordmarkSweepDur/2))
	if !hasTrueColor(mid2, "#ffffff") {
		t.Errorf("second cycle mid-sweep: expected a specular band")
	}

	// The tick stays armed in loop mode.
	freezeClock(t, base.Add(cycle+wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	m := NewMainMode(cfg, "v0.0.0-test", DefaultTheme())
	m.RestartWordmark()
	if m.wordmarkTickCmd() == nil {
		t.Error("loop mode should keep ticking")
	}
}

// TestWordmarkSweepGates pins the off switches: animations disabled or
// a never-started sweep both render the static accent wordmark and arm
// no tick.
func TestWordmarkSweepGates(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	accent := "#e8a33d"

	// animations off: even after a restart, static + no tick.
	off := false
	cfgOff := sampleSnapshotConfig()
	cfgOff.Preferences = &config.Preferences{Animations: &off}
	at := sweepWordmark(t, cfgOff, base.Add(wordmarkSweepDur/2))
	if hasTrueColor(at, "#ffffff") {
		t.Errorf("animations off: wordmark must stay flat accent")
	}
	freezeClock(t, base)
	mOff := NewMainMode(cfgOff, "v0.0.0-test", DefaultTheme())
	mOff.RestartWordmark()
	if mOff.wordmarkTickCmd() != nil {
		t.Error("animations off: no tick expected")
	}

	// Never started (zero wordmarkStart — the state NewMainMode leaves
	// and snapshot tests rely on): static + no tick.
	never := NewMainMode(sampleSnapshotConfig(), "v0.0.0-test", DefaultTheme())
	never.SetSize(120, 40)
	out := never.renderWordmark()
	if !hasTrueColor(out, accent) {
		t.Errorf("never-started sweep: wordmark should be static accent")
	}
	if never.wordmarkTickCmd() != nil {
		t.Error("never-started sweep: no tick expected")
	}
}

// TestWordmarkTickCmdType pins the message the sweep tick carries: an
// animTickMsg that the root forward path routes back into MainMode.
func TestWordmarkTickCmdType(t *testing.T) {
	freezeClock(t, time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC))
	m := NewMainMode(sampleSnapshotConfig(), "v0.0.0-test", DefaultTheme())
	m.RestartWordmark()
	cmd := m.wordmarkTickCmd()
	if cmd == nil {
		t.Fatal("expected an armed tick")
	}
	if _, ok := cmd().(animTickMsg); !ok {
		t.Fatalf("tick msg = %T, want animTickMsg", cmd())
	}
}

// TestWordmarkRestartReanchors verifies RestartWordmark re-anchors the
// sweep: a completed one-shot renders static, and after a restart the
// same absolute clock window shows the band again.
func TestWordmarkRestartReanchors(t *testing.T) {
	forceTrueColor(t)
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cfg := sampleSnapshotConfig()
	accent := "#e8a33d"
	total := wordmarkSweepDur + wordmarkSceneDur

	// One-shot anchored at base is finished at base+total: flat accent.
	freezeClock(t, base)
	m := NewMainMode(cfg, "v0.0.0-test", DefaultTheme())
	m.RestartWordmark()
	freezeClock(t, base.Add(total+time.Millisecond))
	if hasTrueColor(m.renderWordmark(), "#ffffff") {
		t.Fatal("one-shot should be finished at this instant")
	}

	// Restart at base+total, then render 750ms into the fresh sweep: the
	// specular band is back and settled cells stay accent.
	freezeClock(t, base.Add(total+wordmarkSweepDur/2))
	m.RestartWordmark()
	freezeClock(t, base.Add(total+wordmarkSweepDur))
	out := m.renderWordmark()
	if !hasTrueColor(out, "#ffffff") {
		t.Errorf("after restart: specular band should be back\n%.200s", out)
	}
	if !hasTrueColor(out, accent) {
		t.Errorf("after restart: settled cells stay accent")
	}
}

// runSweepWordmark renders the run-header wordmark at the frozen instant
// `at`, a sweep anchored at a fixed base instant (P9 determinism), with
// the run mode in steady state (StatusReady).
func runSweepWordmark(t *testing.T, cfg *config.Config, at time.Time) string {
	t.Helper()
	forceTrueColor(t)
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	freezeClock(t, base)
	r := &RunMode{cfg: cfg, theme: DefaultTheme(), status: StatusReady}
	r.RestartWordmark() // anchors at the fixed base instant
	freezeClock(t, at)
	return r.renderRunWordmark()
}

// TestRunWordmarkSweepOneShot pins the run-header sweep with a frozen
// clock: flat subtle at start and after completion, a specular band
// mid-sweep. The llamaman subtle #9A9A9A ×1.75 clamps to white.
func TestRunWordmarkSweepOneShot(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cfg := sampleSnapshotConfig()
	subtle := "#9a9a9a"

	at := runSweepWordmark(t, cfg, base)
	if !hasTrueColor(at, subtle) {
		t.Errorf("start frame: wordmark should be all subtle %s", subtle)
	}
	if hasTrueColor(at, "#ffffff") {
		t.Errorf("start frame: no specular yet")
	}

	mid := runSweepWordmark(t, cfg, base.Add(wordmarkSweepDur/2))
	if !hasTrueColor(mid, "#ffffff") {
		t.Errorf("mid sweep: expected a specular white band\n%.200s", mid)
	}
	if !hasTrueColor(mid, subtle) {
		t.Errorf("mid sweep: settled cells should stay subtle %s", subtle)
	}

	done := runSweepWordmark(t, cfg, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	if !hasTrueColor(done, subtle) {
		t.Errorf("after completion: wordmark should be all subtle %s", subtle)
	}
	if hasTrueColor(done, "#ffffff") {
		t.Errorf("after completion: no specular left")
	}
}

// TestRunWordmarkSweepLoopHonored verifies the run header honors the
// logo-effect preference: in loop mode it rests in the hold window and
// re-animates on the next cycle.
func TestRunWordmarkSweepLoopHonored(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{LogoEffect: config.LogoEffectLoop}

	hold := runSweepWordmark(t, cfg, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	if hasTrueColor(hold, "#ffffff") {
		t.Errorf("hold window: no specular expected")
	}
	cycle := wordmarkSweepDur + wordmarkSceneDur + wordmarkLoopHold
	mid2 := runSweepWordmark(t, cfg, base.Add(cycle+wordmarkSweepDur/2))
	if !hasTrueColor(mid2, "#ffffff") {
		t.Errorf("second cycle mid-sweep: expected a specular band")
	}
}

// TestRunWordmarkSweepGates pins the run-header off switches:
// animations disabled or a never-started sweep both render the flat
// subtle wordmark.
func TestRunWordmarkSweepGates(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	subtle := "#9a9a9a"

	off := false
	cfgOff := sampleSnapshotConfig()
	cfgOff.Preferences = &config.Preferences{Animations: &off}
	at := runSweepWordmark(t, cfgOff, base.Add(wordmarkSweepDur/2))
	if hasTrueColor(at, "#ffffff") {
		t.Errorf("animations off: wordmark must stay flat subtle")
	}

	never := &RunMode{cfg: sampleSnapshotConfig(), theme: DefaultTheme(), status: StatusReady}
	out := never.renderRunWordmark()
	if !hasTrueColor(out, subtle) {
		t.Errorf("never-started sweep: wordmark should be static subtle")
	}
}

// TestRunWordmarkSweepTickGating pins the run-mode tick lifecycle in
// steady state (StatusReady, nothing else animated): no tick before the
// sweep starts, frame ticks while a one-shot is active, nil once it
// completes, and loop mode keeps a (hold-end wake) tick armed forever.
func TestRunWordmarkSweepTickGating(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	cfg := sampleSnapshotConfig()

	freezeClock(t, base)
	r := &RunMode{cfg: cfg, theme: DefaultTheme(), status: StatusReady}
	if cmd := r.animCmd(); cmd != nil {
		t.Error("never-started sweep: no tick in steady state")
	}
	r.RestartWordmark()
	freezeClock(t, base.Add(time.Millisecond))
	if cmd := r.animCmd(); cmd == nil {
		t.Error("active sweep in steady state must arm a tick")
	}
	freezeClock(t, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond))
	if cmd := r.animCmd(); cmd != nil {
		t.Error("finished one-shot: no tick")
	}

	cfgLoop := sampleSnapshotConfig()
	cfgLoop.Preferences = &config.Preferences{LogoEffect: config.LogoEffectLoop}
	freezeClock(t, base)
	r2 := &RunMode{cfg: cfgLoop, theme: DefaultTheme(), status: StatusReady}
	r2.RestartWordmark()
	freezeClock(t, base.Add(wordmarkSweepDur+wordmarkSceneDur+time.Millisecond)) // hold
	if cmd := r2.animCmd(); cmd == nil {
		t.Error("loop hold must keep a wake tick armed")
	}
}

// TestRunModeAnchorsWordmarkSweep verifies NewRunMode anchors the sweep
// at construction, so a fresh run session (launch, router, reattach)
// always gets a sweep on open.
func TestRunModeAnchorsWordmarkSweep(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "llama.log")
	proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(2 * time.Second) })

	cfg := sampleSnapshotConfig()
	opts := RunModeOpts{
		Cfg: cfg, Model: cfg.Models[0], Preset: cfg.Models[0].Presets[0],
		Argv: proc.Argv, Process: proc,
	}
	run, _, err := NewRunMode(opts, DefaultTheme())
	if err != nil {
		t.Fatal(err)
	}
	if run.wordmarkStart.IsZero() {
		t.Error("NewRunMode must anchor the wordmark sweep")
	}
}

// TestWordmarkGridBandsSanity checks the sweep geometry: every glyph
// cell belongs to a band, activation times are monotonic from the sweep
// start (first band immediate) to its end (last band at wordmarkSweepDur).
func TestWordmarkGridBandsSanity(t *testing.T) {
	if wordmark.count < 2 {
		t.Fatalf("wordmark should span several bands, got %d", wordmark.count)
	}
	seen := map[int]bool{}
	for _, line := range wordmark.bands {
		for _, b := range line {
			if b >= 0 {
				seen[b] = true
			}
		}
	}
	if len(seen) != wordmark.count {
		t.Errorf("bands referenced = %d, want %d", len(seen), wordmark.count)
	}
	if wordmark.acts[0] != 0 {
		t.Errorf("first band activation = %v, want 0", wordmark.acts[0])
	}
	if wordmark.acts[wordmark.count-1] != wordmarkSweepDur {
		t.Errorf("last band activation = %v, want %v", wordmark.acts[wordmark.count-1], wordmarkSweepDur)
	}
	for i := 1; i < wordmark.count; i++ {
		if wordmark.acts[i] < wordmark.acts[i-1] {
			t.Fatalf("band %d activates before band %d", i, i-1)
		}
	}
}
