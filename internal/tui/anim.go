// Animation infrastructure for Release 1 item 5 (DESIGN §15.5):
// injectable clock, sine phases, hex color lerp with a 256-color
// discrete fallback (P1), one-shot strength, and the tick plumbing.
// Everything is render-time and derived from the clock, so a frozen
// clock in tests keeps frames deterministic (P9).
package tui

import (
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// clock is the injectable animation clock. Tests replace it with a
// frozen time so rendered frames stay stable (P9).
var clock = time.Now

// animTickInterval is the animation frame period — 60 fps (owner
// decision after trying 10/15/30/60). Overridable at runtime via
// LLAMAMAN_ANIM_FPS (see animFrameInterval).
const animTickInterval = time.Second / 60

// animFrameInterval returns the animation frame period. The single
// place the frame rate is decided (owner request): edit animTickInterval
// or set LLAMAMAN_ANIM_FPS (e.g. 60, 30, 15) at runtime to experiment
// without rebuilding.
func animFrameInterval() time.Duration {
	if v := os.Getenv("LLAMAMAN_ANIM_FPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Second / time.Duration(n)
		}
	}
	return animTickInterval
}

// animTickMsg triggers a re-render so animated elements move.
type animTickMsg struct{}

// animFrameTick returns one animation frame tick (the period from
// animFrameInterval, LLAMAMAN_ANIM_FPS override included). The single
// place a frame tick is built — run mode, the wordmark sweep, ...
func animFrameTick() tea.Cmd {
	return tea.Tick(animFrameInterval(), func(time.Time) tea.Msg { return animTickMsg{} })
}

// animationsEnabled reports the effective preferences.animations value
// (nil-safe — no config means the default, on).
func animationsEnabled(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return cfg.Prefs().AnimationsEnabled()
}

// animPhase returns a 0..1 sine-wave phase for the given period, so
// animations oscillate smoothly: (sin(2π · t / period) + 1) / 2.
func animPhase(period time.Duration) float64 {
	ms := period.Milliseconds()
	if ms <= 0 {
		return 0
	}
	t := float64(clock().UnixMilli()%ms) / float64(ms)
	return (math.Sin(2*math.Pi*t) + 1) / 2
}

// quantizePhase snaps t to 6 discrete levels on 256-color (or fewer)
// terminals so the visible color steps smoothly rather than smearing
// (P1 — owner-amended from 3 to 6 steps for less jerky breathing);
// truecolor terminals keep the continuous value.
func quantizePhase(t float64) float64 {
	if lipgloss.ColorProfile() == termenv.TrueColor {
		return t
	}
	return math.Round(t*6) / 6
}

// animColor is lerpColor with the phase quantized per the terminal
// profile (P1): smooth on truecolor, 6 discrete steps otherwise.
func animColor(a, b lipgloss.Color, period time.Duration) lipgloss.Color {
	return lerpColor(a, b, quantizePhase(animPhase(period)))
}

// lerpColor interpolates two hex colors by t (0..1). Non-hex colors
// fall back to a.
func lerpColor(a, b lipgloss.Color, t float64) lipgloss.Color {
	ar, ag, ab, okA := parseHex(string(a))
	br, bg, bb, okB := parseHex(string(b))
	if !okA || !okB {
		return a
	}
	r := ar + int(float64(br-ar)*t)
	g := ag + int(float64(bg-ag)*t)
	bl := ab + int(float64(bb-ab)*t)
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", clamp8(r), clamp8(g), clamp8(bl)))
}

// lighten shifts a hex color a fraction of the way toward white.
func lighten(c lipgloss.Color, f float64) lipgloss.Color {
	return lerpColor(c, lipgloss.Color("#FFFFFF"), f)
}

// oneShotStrength returns 1→0 over dur since at (0 when past or zero),
// for one-shot transition effects like the ready glow (§15.5).
func oneShotStrength(at time.Time, dur time.Duration) float64 {
	if at.IsZero() {
		return 0
	}
	el := clock().Sub(at)
	if el <= 0 {
		return 1
	}
	if el >= dur {
		return 0
	}
	return 1 - el.Seconds()/dur.Seconds()
}

// oneShotColor brightens base by strength (a one-shot fade from bright
// to settled).
func oneShotColor(base lipgloss.Color, strength float64) lipgloss.Color {
	return lerpColor(base, lighten(base, 0.35), strength)
}

// smoothVal eases a displayed fraction toward its latest target over
// animSmoothDur, so data-driven bars (Gen/Process) fill smoothly
// instead of jumping between polls (§15.5). Mutating on read is fine:
// renders are idempotent per frame and the clock is injectable.
type smoothVal struct {
	shown   float64
	shownAt time.Time
	target  float64
}

const animSmoothDur = 500 * time.Millisecond

func lerpF(a, b, t float64) float64 { return a + (b-a)*t }

// display returns the eased fraction for the current frame.
func (s *smoothVal) display() float64 {
	el := clock().Sub(s.shownAt)
	if el >= animSmoothDur {
		s.shown = s.target
		s.shownAt = clock()
		return s.shown
	}
	return lerpF(s.shown, s.target, el.Seconds()/animSmoothDur.Seconds())
}

// set records a new target, committing the current eased value first.
func (s *smoothVal) set(target float64) {
	if target == s.target {
		return
	}
	s.shown = s.display()
	s.target = target
	s.shownAt = clock()
}

// animCmd returns the frame tick when animations are enabled AND
// something animated is visible; nil otherwise — steady state stays
// static (§2.4 cost note).
// animCmd returns the run-mode frame tick, or nil in steady state:
// nothing animated is visible and no wordmark sweep is due (§2.4 cost
// note). While anything is animated the frame tick re-renders every
// frame, and the clock-derived wordmark sweep (§15.5a) animates along
// for free; in steady state only the sweep can need a tick (once-mode
// active window, or loop mode with its hold-end wake).
func (r *RunMode) animCmd() tea.Cmd {
	if !animationsEnabled(r.cfg) {
		return nil
	}
	if r.anythingAnimated() {
		return animFrameTick()
	}
	return wordmarkSweepTick(r.cfg, r.wordmarkStart)
}

// anythingAnimated reports whether any §15.5 element is currently
// visible: the load window, starting, busy/generating, or a one-shot
// effect still within its window.
func (r *RunMode) anythingAnimated() bool {
	if r.showingLoadBlock() || r.status == StatusStarting {
		return true
	}
	if r.busyCount > 0 {
		return true
	}
	return oneShotStrength(r.readyAt, readyGlowDur) > 0 ||
		oneShotStrength(r.errAt, errFlashDur) > 0 ||
		oneShotStrength(r.lastJumpAt, jumpPulseDur) > 0 ||
		oneShotStrength(r.ttftAt, ttftGlowDur) > 0
}

const (
	readyGlowDur   = 600 * time.Millisecond
	errFlashDur    = 600 * time.Millisecond
	jumpPulseDur   = 600 * time.Millisecond
	ttftGlowDur    = 1000 * time.Millisecond
	routerFlashDur = 1200 * time.Millisecond
)

func parseHex(s string) (r, g, b int, ok bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return 0, 0, 0, false
	}
	var rr, gg, bb int
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &rr, &gg, &bb); err != nil {
		return 0, 0, 0, false
	}
	return rr, gg, bb, true
}

func clamp8(v int) int {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return v
}
