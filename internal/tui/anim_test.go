package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// freezeClock replaces the injectable animation clock for the duration
// of a test (P9).
func freezeClock(t *testing.T, at time.Time) {
	t.Helper()
	orig := clock
	clock = func() time.Time { return at }
	t.Cleanup(func() { clock = orig })
}

// TestAnimPhase: the sine phase is 0..1 and period-boundary aware.
func TestAnimPhase(t *testing.T) {
	base := time.UnixMilli(1_000_000_000_000)
	freezeClock(t, base)
	if p := animPhase(2000 * time.Millisecond); p != 0.5 {
		t.Errorf("animPhase at t=0 of a 2s period = %v, want 0.5", p)
	}
	freezeClock(t, base.Add(500*time.Millisecond))
	if p := animPhase(2000 * time.Millisecond); p != 1.0 {
		t.Errorf("animPhase at quarter period = %v, want 1.0", p)
	}
	freezeClock(t, base.Add(1000*time.Millisecond))
	if p := animPhase(2000 * time.Millisecond); p < 0.499 || p > 0.501 {
		t.Errorf("animPhase at half period = %v, want ≈0.5", p)
	}
}

// TestLerpColor: midpoint of black↔white is #808080; clamps at edges.
func TestLerpColor(t *testing.T) {
	if got := lerpColor(lipgloss.Color("#000000"), lipgloss.Color("#FFFFFF"), 0.5); got != lipgloss.Color("#7f7f7f") {
		t.Errorf("midpoint = %v, want #7f7f7f (int truncation)", got)
	}
	if got := lerpColor(lipgloss.Color("#000000"), lipgloss.Color("#FFFFFF"), 0); got != lipgloss.Color("#000000") {
		t.Errorf("t=0 = %v, want #000000", got)
	}
	if got := lerpColor(lipgloss.Color("#FF0000"), lipgloss.Color("#0000FF"), 1); got != lipgloss.Color("#0000ff") {
		t.Errorf("t=1 = %v, want #0000ff", got)
	}
	if got := lighten(lipgloss.Color("#000000"), 1); got != lipgloss.Color("#ffffff") {
		t.Errorf("lighten black by 1 = %v, want #ffffff", got)
	}
}

// TestQuantizePhase: 256-color profile snaps to 6 discrete steps;
// truecolor is continuous.
func TestQuantizePhase(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.ColorProfile())
	if got := quantizePhase(0.4); got != 0.3333333333333333 {
		t.Errorf("ANSI256 quantize(0.4) = %v, want 1/3", got)
	}
	// At most 7 distinct levels across a full period (6 steps + both
	// endpoints 0 and 1).
	seen := map[float64]bool{}
	for i := 0; i < 100; i++ {
		seen[quantizePhase(float64(i)/100)] = true
	}
	if len(seen) > 7 {
		t.Errorf("ANSI256 quantization produced %d distinct levels, want ≤ 7 (6 steps + endpoints)", len(seen))
	}
	lipgloss.SetColorProfile(termenv.TrueColor)
	if got := quantizePhase(0.4); got != 0.4 {
		t.Errorf("truecolor quantize(0.4) = %v, want 0.4 (continuous)", got)
	}
}

// TestIndeterminateBar pins the comet (owner's design): a solid █ head
// leading with the fragment tail behind it — left when moving right,
// right when moving left — merging into a solid block at the far edge,
// dissolving back to the head, then reversing. No gaps, no phantom
// fragments, no teleports.
func TestIndeterminateBar(t *testing.T) {
	cases := []struct {
		phase   float64
		forward bool
		want    string
	}{
		{0, true, "▏▎▍▌▋▊▉█░░░░"},    // comet at the left, tail trailing
		{0.25, true, "░░░░░▎▍▌▋▊▉█"}, // drain: far fragment (▏) gone
		{0.55, true, "░░░░░░░░░░░█"}, // drain complete: just the head
		{0.8, true, "░░░░░░░░░░░█"},  // hold: pinned at the right edge
		{0.8, false, "░░░░░░░█▉▊▋▌"}, // backward: tail on the right
		{0.5, false, "░█▉▊▋▌▍▎▏░░░"}, // backward mid-slide
		{0.1, false, "█░░░░░░░░░░░"}, // backward drain complete
		{0.0, false, "█░░░░░░░░░░░"},
	}
	for _, c := range cases {
		if got := indeterminateBar(12, c.phase, c.forward); got != c.want {
			t.Errorf("indeterminateBar(12, %v, forward=%v) = %q, want %q", c.phase, c.forward, got, c.want)
		}
	}
}

// TestCometPhase: constant-speed triangle, direction flips at the
// halfway point.
func TestCometPhase(t *testing.T) {
	base := time.UnixMilli(3_000_000_000_000)
	freezeClock(t, base)
	p, fwd := cometPhase(1200 * time.Millisecond)
	if p != 0 || !fwd {
		t.Errorf("cometPhase at t=0 = (%v, %v), want (0, forward)", p, fwd)
	}
	freezeClock(t, base.Add(300*time.Millisecond))
	p, fwd = cometPhase(1200 * time.Millisecond)
	if p != 0.5 || !fwd {
		t.Errorf("cometPhase at 1/4 period = (%v, %v), want (0.5, forward)", p, fwd)
	}
	freezeClock(t, base.Add(600*time.Millisecond))
	p, fwd = cometPhase(1200 * time.Millisecond)
	if p != 1 || fwd {
		t.Errorf("cometPhase at half period = (%v, %v), want (1, backward)", p, fwd)
	}
	freezeClock(t, base.Add(900*time.Millisecond))
	p, fwd = cometPhase(1200 * time.Millisecond)
	if p != 0.5 || fwd {
		t.Errorf("cometPhase at 3/4 period = (%v, %v), want (0.5, backward)", p, fwd)
	}
}

// TestBadgeColorAnimations: with a frozen clock + animations on, the
// STARTING badge color breathes (differs across phases) and the ready
// glow brightens the READY badge right after the transition; with
// animations off everything is static.
func TestBadgeColorAnimations(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.ColorProfile())
	base := time.UnixMilli(2_000_000_000_000)

	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "b", Host: "h", Port: 1}}
	r := &RunMode{cfg: cfg, status: StatusStarting, theme: DefaultTheme()}
	freezeClock(t, base)
	c1 := r.badgeColor()
	freezeClock(t, base.Add(500*time.Millisecond))
	c2 := r.badgeColor()
	if c1 == c2 {
		t.Errorf("STARTING badge must breathe across phases, got %v == %v", c1, c2)
	}

	// Ready glow: right after the transition the badge is brighter than
	// the settled color.
	r.status = StatusReady
	r.readyAt = base
	freezeClock(t, base.Add(100*time.Millisecond))
	glow := r.badgeColor()
	r.readyAt = base.Add(-time.Hour) // long past the glow window
	settled := r.badgeColor()
	if glow == settled {
		t.Errorf("ready glow must differ from the settled color")
	}

	// Animations off → static base color.
	off := false
	cfg.Preferences = &config.Preferences{Animations: &off}
	r.status = StatusStarting
	freezeClock(t, base)
	s1 := r.badgeColor()
	freezeClock(t, base.Add(500*time.Millisecond))
	s2 := r.badgeColor()
	if s1 != s2 {
		t.Errorf("animations off must keep the badge static, got %v != %v", s1, s2)
	}
}

// TestAnimCmdScheduling: the frame tick is scheduled only when
// animations are on AND something animated is visible.
func TestAnimCmdScheduling(t *testing.T) {
	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "b", Host: "h", Port: 1}}
	r := &RunMode{cfg: cfg, status: StatusReady} // idle
	if cmd := r.animCmd(); cmd != nil {
		t.Error("steady-state READY must not schedule the animation tick")
	}
	r.status = StatusStarting
	if cmd := r.animCmd(); cmd == nil {
		t.Error("starting must schedule the animation tick")
	}
	off := false
	cfg.Preferences = &config.Preferences{Animations: &off}
	r.status = StatusStarting
	if cmd := r.animCmd(); cmd != nil {
		t.Error("animations off must not schedule the tick")
	}
}

// TestRunModeToggleAnimations pins the `a` quick key: it flips the
// preference in memory, persists it, and stops the tick.
func TestRunModeToggleAnimations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := sampleSnapshotConfig()
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	r := &RunMode{cfg: cfg, cfgPath: path, status: StatusStarting}
	if !cfg.Prefs().AnimationsEnabled() {
		t.Fatal("animations must default on")
	}
	next, _ := r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	r = next
	if cfg.Prefs().AnimationsEnabled() {
		t.Error("a must flip animations off")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"animations": false`) {
		t.Errorf("animations=false not persisted:\n%s", data)
	}
	if cmd := r.animCmd(); cmd != nil {
		t.Error("after toggling off, the animation tick must not be scheduled")
	}
}

// TestAnimFrameInterval: the frame rate is decided in one place and
// can be overridden at runtime via LLAMAMAN_ANIM_FPS (owner request —
// experimentation without rebuilding).
func TestAnimFrameInterval(t *testing.T) {
	t.Setenv("LLAMAMAN_ANIM_FPS", "30")
	if got := animFrameInterval(); got != time.Second/30 {
		t.Errorf("30 fps interval = %v, want %v", got, time.Second/30)
	}
	t.Setenv("LLAMAMAN_ANIM_FPS", "60")
	if got := animFrameInterval(); got != time.Second/60 {
		t.Errorf("60 fps interval = %v, want %v", got, time.Second/60)
	}
	t.Setenv("LLAMAMAN_ANIM_FPS", "not-a-number")
	if got := animFrameInterval(); got != animTickInterval {
		t.Errorf("invalid override must fall back to the default, got %v", got)
	}
	t.Setenv("LLAMAMAN_ANIM_FPS", "")
	if got := animFrameInterval(); got != animTickInterval {
		t.Errorf("unset override must use the default, got %v", got)
	}
}
