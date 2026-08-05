package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// liveBarWidth is the universal cell width of every bar and
// sparkline in the run-mode live band. Set wide so each sparkline
// cell can be a single 1s tick (no bucketing needed) and the value
// column can breathe.
const liveBarWidth = 40

// sparkBufferSamples is the per-stream history depth = one tick per
// cell. Window = liveBarWidth seconds at the 1s ticker rate. New
// samples arrive on the right; old samples slide left preserving
// their height and color until evicted off the back. No averaging
// or downsampling, no possibility of mid-life mutation.
// DESIGN.md §7.4.
const sparkBufferSamples = liveBarWidth

// barFillChar / barEmptyChar are BC2 — `▆` (lower 3/4 block) for
// both filled and empty cells. The same character keeps adjacent
// rows visually separated by a 1-cell gap on top of every cell;
// color is what tells filled apart from empty.
const (
	barFillChar  = "▆"
	barEmptyChar = "▆"
)

// sparkLadder is the 8-step Unicode block ladder used for sparkline
// cell heights. Index 0 ≈ 0%, index 7 ≈ 100%; cellAtSparkHeight maps
// a 0–1 normalized value to a glyph.
var sparkLadder = []string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// renderBar draws a 20-cell BC2 bar. pct is 0–100 (the fill ratio).
// zone selects the fill color via zoneColor(theme, …); empty cells
// render in theme.Muted. Bar shape is just `▆` chars throughout —
// color tells filled apart from empty, no overlay. The current/max
// values that previously sat inside the bar now ride in their own
// fixed-width column between the bar and the trailing %.
func renderBar(theme Theme, pct float64, zone Zone) string {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	fillCells := int((pct/100)*liveBarWidth + 0.5)
	if fillCells > liveBarWidth {
		fillCells = liveBarWidth
	}

	fillStyle := lipgloss.NewStyle().Foreground(zoneColor(theme, zone))
	emptyStyle := lipgloss.NewStyle().Foreground(theme.Muted)

	var b strings.Builder
	b.Grow(liveBarWidth * 4)
	for i := 0; i < liveBarWidth; i++ {
		if i < fillCells {
			b.WriteString(fillStyle.Render(barFillChar))
		} else {
			b.WriteString(emptyStyle.Render(barEmptyChar))
		}
	}
	return b.String()
}

// renderSparkline draws a 20-cell sparkline from up to liveBarWidth
// samples (S1: each cell colored by its own value). Values are
// 0–100. When the buffer is partially full, leading cells render as
// blank spaces; new samples arrive on the right and old samples
// slide left as the buffer fills. Each cell shows exactly one
// sample so values never morph mid-slide.
func renderSparkline(theme Theme, samples []float64, kind MetricKind) string {
	cells := alignSamples(samples, liveBarWidth)
	var b strings.Builder
	b.Grow(liveBarWidth * 4)
	for _, v := range cells {
		if v < 0 {
			b.WriteString(" ")
			continue
		}
		ladderIdx := int((v / 100) * float64(len(sparkLadder)-1))
		if ladderIdx < 0 {
			ladderIdx = 0
		}
		if ladderIdx > len(sparkLadder)-1 {
			ladderIdx = len(sparkLadder) - 1
		}
		style := lipgloss.NewStyle().Foreground(zoneColor(theme, zoneFor(kind, v)))
		b.WriteString(style.Render(sparkLadder[ladderIdx]))
	}
	return b.String()
}

// alignSamples right-aligns up to `width` of the most recent samples
// into a fixed-width slot. Leading cells fill with -1 sentinels
// (renderSparkline displays as spaces). Excess samples beyond the
// width are dropped from the front (oldest first) so the rightmost
// cell is always the newest value.
func alignSamples(samples []float64, width int) []float64 {
	out := make([]float64, width)
	if len(samples) == 0 {
		for i := range out {
			out[i] = -1
		}
		return out
	}
	if len(samples) >= width {
		// Take the most recent `width` samples.
		copy(out, samples[len(samples)-width:])
		return out
	}
	// Right-align: pad leading cells with sentinel.
	offset := width - len(samples)
	for i := 0; i < offset; i++ {
		out[i] = -1
	}
	copy(out[offset:], samples)
	return out
}

// ringBuffer is a fixed-capacity ring of float64 samples. Used by
// the live-band (per-device Util history, server-panel token-rate
// history, prompt-eval history). Append rolls oldest off the back.
type ringBuffer struct {
	data []float64
	cap  int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity}
}

func (r *ringBuffer) Append(v float64) {
	if len(r.data) >= r.cap {
		r.data = r.data[1:]
	}
	r.data = append(r.data, v)
}

func (r *ringBuffer) Snapshot() []float64 {
	out := make([]float64, len(r.data))
	copy(out, r.data)
	return out
}
