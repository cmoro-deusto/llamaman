// Wordmark highlight sweep (§15.5a) — a Go port of the Highlight effect
// from terminaltexteffects (github.com/ChrisBuilds/terminaltexteffects,
// effects/effect_highlight.py) applied to the Main-mode ascii-art logo.
//
// The Python effect runs a "specular highlight" across the text: a band
// of characters travels along a diagonal with ease-in-out-circ timing;
// each character briefly brightens (its base color scaled toward white
// by the highlight brightness) and settles back to its base color. This
// file keeps the same algorithm but is time-based rather than
// frame-based, so the sweep speed is independent of the effective frame
// rate (LLAMAMAN_ANIM_FPS only changes smoothness). Everything is
// derived from the injectable clock, so a frozen clock keeps rendered
// frames deterministic (P9).
package tui

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// Sweep timing and colors (Python defaults unless noted).
const (
	// highlightWidth is the number of ramp steps the fully-bright core
	// of the band spans (Python default: 8).
	highlightWidth = 1
	// highlightBrightness is the specular lightness multiplier applied
	// to the base color (Python default: 1.75).
	highlightBrightness = 1.75
	// wordmarkSweepDur is the time the band takes to travel from the
	// bottom-left corner of the logo to the top-right. Owner-tuned:
	// started at 1500 ms, then 1200, 800; 400 ms is the final pace.
	wordmarkSweepDur = 400 * time.Millisecond
	// wordmarkRampStep is how long each of a character's ramp colors is
	// held. Python holds every frame 2 ticks at 60 fps; fixing it in
	// time (2 * 1/60 s) keeps the speed FPS-independent.
	wordmarkRampStep = 2 * time.Second / 60
	// wordmarkSceneDur is a character's full base→bright→base scene:
	// (3 + width + 3) ramp colors, the first of which is the base color
	// itself, so 13 steps after activation.
	wordmarkSceneDur = (3 + highlightWidth + 3 - 1) * wordmarkRampStep
	// wordmarkLoopHold is the pause between sweeps in loop mode.
	wordmarkLoopHold = 4000 * time.Millisecond
)

// wordmarkGrid is the precomputed sweep geometry of the embedded
// wordmark (DESIGN §7.2): for every non-space glyph cell the diagonal
// band it belongs to, plus the band count and each band's activation
// delay. Bands follow Python's DIAGONAL_BOTTOM_LEFT_TO_TOP_RIGHT: the
// band key is col−row, ranked ascending so band 0 starts at the
// bottom-left corner and the last band ends at the top-right.
type wordmarkGrid struct {
	bands [][]int         // per line, per rune: band rank, or -1 for spaces
	count int             // number of distinct bands
	acts  []time.Duration // activation delay of each band, relative to the sweep start
}

// wordmark is the grid of the embedded logo, computed once.
var wordmark = buildWordmarkGrid()

func buildWordmarkGrid() wordmarkGrid {
	lines := strings.Split(strings.TrimRight(Wordmark, "\n"), "\n")
	type cell struct{ r, c, key int }
	var cells []cell
	keySet := map[int]bool{}
	for r, line := range lines {
		for c, rn := range line {
			if rn == ' ' {
				continue
			}
			cells = append(cells, cell{r, c, c - r})
			keySet[c-r] = true
		}
	}
	keys := make([]int, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	rank := make(map[int]int, len(keys))
	for i, k := range keys {
		rank[k] = i
	}
	bands := make([][]int, len(lines))
	for i := range bands {
		bands[i] = make([]int, len(lines[i]))
		for j := range bands[i] {
			bands[i][j] = -1
		}
	}
	for _, cl := range cells {
		bands[cl.r][cl.c] = rank[cl.key]
	}
	acts := make([]time.Duration, len(keys))
	last := len(keys) - 1
	for i := range keys {
		if last == 0 {
			continue // single band: activates immediately
		}
		acts[i] = time.Duration(float64(wordmarkSweepDur) * invEaseInOutCirc(float64(i)/float64(last)))
	}
	return wordmarkGrid{bands: bands, count: len(keys), acts: acts}
}

// easeInOutCirc is Python's easing.in_out_circ, the timing curve of the
// band's travel.
func easeInOutCirc(t float64) float64 {
	t = clamp01(t)
	if t < 0.5 {
		return (1 - math.Sqrt(1-(2*t)*(2*t))) / 2
	}
	return (math.Sqrt(1-((-2*t+2)*(-2*t+2))) + 1) / 2
}

// invEaseInOutCirc inverts easeInOutCirc: given a band's eased progress
// fraction it returns the time fraction at which that band activates.
func invEaseInOutCirc(p float64) float64 {
	p = clamp01(p)
	if p < 0.5 {
		return 0.5 * math.Sqrt(1-(1-2*p)*(1-2*p))
	}
	return 1 - 0.5*math.Sqrt(1-(2*p-1)*(2*p-1))
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

// wordmarkRamp builds a character's highlight scene: base → bright →
// bright → base with (3, width, 3) gradient steps, exactly like the
// Python Gradient. The first color is base and the last color is base,
// so a character returns to its settled color when the scene ends.
func wordmarkRamp(base, bright lipgloss.Color) []lipgloss.Color {
	ramp := make([]lipgloss.Color, 0, 3+highlightWidth+3)
	for i := 0; i < 3; i++ {
		ramp = append(ramp, lerpColor(base, bright, float64(i)/2))
	}
	for i := 0; i < highlightWidth; i++ {
		ramp = append(ramp, bright)
	}
	for i := 0; i < 3; i++ {
		ramp = append(ramp, lerpColor(bright, base, float64(i)/2))
	}
	return ramp
}

// brightenColor scales a hex color's HSL lightness by f, clamping at 1
// — the specular color of the Highlight effect (Python's
// adjust_color_brightness). Non-hex colors pass through unchanged (the
// sweep then degrades to the base color, P3).
func brightenColor(c lipgloss.Color, f float64) lipgloss.Color {
	r, g, b, ok := parseHex(string(c))
	if !ok {
		return c
	}
	h, s, l := rgbToHSL(r, g, b)
	l = math.Min(1, l*f)
	rr, gg, bb := hslToRGB(h, s, l)
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", clamp8(rr), clamp8(gg), clamp8(bb)))
}

func rgbToHSL(r, g, b int) (h, s, l float64) {
	rf, gf, bf := float64(r)/255, float64(g)/255, float64(b)/255
	max := math.Max(rf, math.Max(gf, bf))
	min := math.Min(rf, math.Min(gf, bf))
	l = (max + min) / 2
	if max == min {
		return 0, 0, l
	}
	d := max - min
	if l > 0.5 {
		s = d / (2 - max - min)
	} else {
		s = d / (max + min)
	}
	switch max {
	case rf:
		h = (gf - bf) / d
		if gf < bf {
			h += 6
		}
	case gf:
		h = (bf-rf)/d + 2
	case bf:
		h = (rf-gf)/d + 4
	}
	h /= 6
	return h, s, l
}

func hslToRGB(h, s, l float64) (r, g, b int) {
	if s == 0 {
		v := int(math.Round(l * 255))
		return v, v, v
	}
	var q float64
	if l < 0.5 {
		q = l * (1 + s)
	} else {
		q = l + s - l*s
	}
	p := 2*l - q
	hue := func(p, q, t float64) float64 {
		if t < 0 {
			t++
		}
		if t > 1 {
			t--
		}
		if t < 1.0/6 {
			return p + (q-p)*6*t
		}
		if t < 0.5 {
			return q
		}
		if t < 2.0/3 {
			return p + (q-p)*(2.0/3-t)*6
		}
		return p
	}
	return int(math.Round(hue(p, q, h+1.0/3) * 255)),
		int(math.Round(hue(p, q, h) * 255)),
		int(math.Round(hue(p, q, h-1.0/3) * 255))
}

// wordmarkSweepTick returns the animation tick that drives the wordmark
// highlight sweep anchored at start under the given config, or nil when
// nothing should animate: animations off, sweep never started, or a
// finished one-shot. In loop mode it ticks at frame rate mid-sweep and
// once at the hold-end to wake the next sweep (the hold itself renders
// static — no 60 fps burn, §2.4 cost note). Callers re-arm it when an
// animTickMsg lands (the pending timer covers any intervening
// messages), so the sweep runs while the screen is visible and stops
// (in once mode) once it completes.
func wordmarkSweepTick(cfg *config.Config, start time.Time) tea.Cmd {
	if cfg == nil || !animationsEnabled(cfg) || start.IsZero() {
		return nil
	}
	el := clock().Sub(start)
	if cfg.Prefs().LogoEffectMode() == config.LogoEffectLoop {
		cycle := wordmarkSweepDur + wordmarkSceneDur + wordmarkLoopHold
		phase := el % cycle
		if phase < 0 {
			phase += cycle // backward clock: clamp into the cycle
		}
		if phase < wordmarkSweepDur+wordmarkSceneDur {
			return animFrameTick() // mid-sweep: animate at frame rate
		}
		// Hold window: the wordmark is static; wake once when the next
		// sweep begins.
		return tea.Tick(cycle-phase, func(time.Time) tea.Msg { return animTickMsg{} })
	}
	if el < wordmarkSweepDur+wordmarkSceneDur {
		return animFrameTick()
	}
	return nil
}

// RestartWordmark (re)starts the wordmark highlight sweep (§15.5a).
// Root calls it every time the main screen becomes visible, so a
// one-shot sweep runs once per visit (and a loop restarts its cycle).
// A zero wordmarkStart means the sweep has never been started — the
// wordmark renders static, which is also what snapshot tests rely on.
func (m *MainMode) RestartWordmark() { m.wordmarkStart = clock() }

// wordmarkTickCmd returns the Main-mode sweep tick (§15.5a).
func (m MainMode) wordmarkTickCmd() tea.Cmd {
	return wordmarkSweepTick(m.cfg, m.wordmarkStart)
}

// renderWordmarkSweep renders the wordmark with the §15.5a highlight
// sweep applied, or the flat base render when the sweep is off (never
// started, loop hold, or a completed one-shot). base is the settled
// color — the theme accent on Main, the theme subtle on the run header;
// start anchors the sweep; mode is the logo-effect preference. The
// sweep is entirely clock-derived, so a frozen clock keeps frames
// deterministic (P9).
func renderWordmarkSweep(base lipgloss.Color, start time.Time, mode string) string {
	static := lipgloss.NewStyle().Foreground(base).Render(strings.TrimRight(Wordmark, "\n"))
	if start.IsZero() {
		return static
	}
	el := clock().Sub(start)
	if mode == config.LogoEffectLoop {
		el %= wordmarkSweepDur + wordmarkSceneDur + wordmarkLoopHold
	}
	if el < 0 || el >= wordmarkSweepDur+wordmarkSceneDur {
		return static
	}
	ramp := wordmarkRamp(base, brightenColor(base, highlightBrightness))
	lines := strings.Split(strings.TrimRight(Wordmark, "\n"), "\n")
	out := make([]string, len(lines))
	for r, line := range lines {
		var sb strings.Builder
		for c, rn := range line {
			col := base
			if band := wordmark.bands[r][c]; band >= 0 {
				if elapsed := el - wordmark.acts[band]; elapsed >= 0 {
					idx := int(elapsed / wordmarkRampStep)
					if idx >= len(ramp)-1 {
						idx = len(ramp) - 1
					}
					col = ramp[idx]
				}
			}
			sb.WriteString(lipgloss.NewStyle().Foreground(col).Render(string(rn)))
		}
		out[r] = sb.String()
	}
	return strings.Join(out, "\n")
}

// renderWordmark renders the Main-screen wordmark: the §15.5a sweep
// over the theme accent, or the flat accent render when the sweep is
// off (animations disabled, never started, loop hold, or a completed
// one-shot).
func (m MainMode) renderWordmark() string {
	if m.cfg == nil || !animationsEnabled(m.cfg) || m.wordmarkStart.IsZero() {
		return lipgloss.NewStyle().Foreground(m.theme.Accent).Render(strings.TrimRight(Wordmark, "\n"))
	}
	return renderWordmarkSweep(m.theme.Accent, m.wordmarkStart, m.cfg.Prefs().LogoEffectMode())
}

// RestartWordmark (re)starts the run-header wordmark highlight sweep
// (§15.5a). NewRunMode anchors it at construction, so a one-shot sweep
// runs once per session (launch, router, reattach) — or continuously
// when preferences.logo-effect is "loop".
func (r *RunMode) RestartWordmark() { r.wordmarkStart = clock() }

// renderRunWordmark renders the run-header wordmark: the §15.5a sweep
// over the theme subtle, or the flat subtle render when the sweep is
// off (animations disabled, never started, loop hold, or a completed
// one-shot).
func (r *RunMode) renderRunWordmark() string {
	if r.cfg == nil || !animationsEnabled(r.cfg) || r.wordmarkStart.IsZero() {
		return lipgloss.NewStyle().Foreground(r.theme.Subtle).Render(strings.TrimRight(Wordmark, "\n"))
	}
	return renderWordmarkSweep(r.theme.Subtle, r.wordmarkStart, r.cfg.Prefs().LogoEffectMode())
}
