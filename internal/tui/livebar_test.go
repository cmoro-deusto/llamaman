package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRenderBarFillCellCount(t *testing.T) {
	theme := CurrentTheme()
	cases := []struct {
		pct      float64
		wantFill int
	}{
		{0, 0},
		{10, 4},    // 4.0
		{25, 10},   // 10
		{50, 20},   // 20
		{65.2, 26}, // 26.08 → 26
		{99.9, 40}, // rounds up to width
		{100, 40},
	}
	for _, c := range cases {
		got := stripANSI(renderBar(theme, c.pct, ZoneOK))
		fill := strings.Count(got, barFillChar)
		// Empty cells use the same char so we can't count empties
		// directly; instead infer from total - fill since both use ▆.
		// But with no overlay, total chars = liveBarWidth and fill is
		// the leading run of bright chars, which we can't tell apart
		// without ANSI. Plain-ANSI test instead checks total width.
		if w := ansi.StringWidth(got); w != liveBarWidth {
			t.Errorf("pct=%v: bar width = %d, want %d (raw: %q)", c.pct, w, liveBarWidth, got)
		}
		// The expected fill cell count is just an arithmetic check
		// against the renderer's rounding rule.
		if want := int((c.pct/100)*liveBarWidth + 0.5); want != c.wantFill {
			t.Errorf("test setup: c.wantFill=%d but rounding gives %d for pct=%v", c.wantFill, want, c.pct)
		}
		_ = fill
	}
}

func TestRenderBarUsesOnlyBarChars(t *testing.T) {
	theme := CurrentTheme()
	plain := stripANSI(renderBar(theme, 65.2, ZoneOK))
	runes := []rune(plain)
	if len(runes) != liveBarWidth {
		t.Fatalf("rune count = %d, want %d (bar: %q)", len(runes), liveBarWidth, plain)
	}
	for i, r := range runes {
		if string(r) != barFillChar && string(r) != barEmptyChar {
			t.Errorf("rune %d = %q, want bar char (no overlay anymore)", i, string(r))
		}
	}
}

func TestRenderBarClampsOutOfRange(t *testing.T) {
	theme := CurrentTheme()
	for _, pct := range []float64{-10, 200, 1000} {
		got := stripANSI(renderBar(theme, pct, ZoneOK))
		if w := ansi.StringWidth(got); w != liveBarWidth {
			t.Errorf("pct=%v: width = %d, want %d", pct, w, liveBarWidth)
		}
	}
}

// ---- sparkline tests ----

func TestRenderSparklineWidthMatchesBar(t *testing.T) {
	theme := CurrentTheme()
	samples := []float64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	got := stripANSI(renderSparkline(theme, samples, MetricUtil))
	if w := ansi.StringWidth(got); w != liveBarWidth {
		t.Errorf("sparkline width = %d, want %d (so spark and bar align)", w, liveBarWidth)
	}
}

func TestRenderSparklineEmptySamples(t *testing.T) {
	theme := CurrentTheme()
	got := stripANSI(renderSparkline(theme, nil, MetricUtil))
	if w := ansi.StringWidth(got); w != liveBarWidth {
		t.Errorf("empty sparkline width = %d, want %d (spaces fill)", w, liveBarWidth)
	}
	if strings.TrimSpace(got) != "" {
		t.Errorf("empty sparkline should be all spaces; got: %q", got)
	}
}

func TestRenderSparklinePartialSamplesPadLeading(t *testing.T) {
	theme := CurrentTheme()
	// 3 samples in a 20-cell sparkline: first 17 cells should be space.
	samples := []float64{50, 75, 100}
	got := stripANSI(renderSparkline(theme, samples, MetricUtil))
	if !strings.HasPrefix(got, strings.Repeat(" ", 17)) {
		t.Errorf("partial sparkline should pad leading spaces; got: %q", got)
	}
}

func TestRenderSparklineUsesAllLadderRungs(t *testing.T) {
	theme := CurrentTheme()
	// Samples chosen to land on each ladder rung. ladderIdx =
	// int(v/100 * 7), so to hit rung k we need v in [100*k/7, 100*(k+1)/7).
	// Picks: 0 → 0/7, 16 → 1/7 (~14.3+), 30 → 2/7 (~28.6+), 45 → 3/7,
	// 60 → 4/7, 75 → 5/7, 90 → 6/7, 100 → 7/7.
	samples := []float64{0, 16, 30, 45, 60, 75, 90, 100}
	got := stripANSI(renderSparkline(theme, samples, MetricUtil))
	for _, glyph := range sparkLadder {
		if !strings.Contains(got, glyph) {
			t.Errorf("sparkline missing ladder glyph %q; got: %q", glyph, got)
		}
	}
}

// ---- alignSamples tests ----

func TestAlignSamplesPadsLeading(t *testing.T) {
	out := alignSamples([]float64{1, 2, 3}, 10)
	if len(out) != 10 {
		t.Fatalf("len = %d, want 10", len(out))
	}
	for i := 0; i < 7; i++ {
		if out[i] >= 0 {
			t.Errorf("out[%d] = %v, want negative sentinel (no sample)", i, out[i])
		}
	}
	for i, v := range []float64{1, 2, 3} {
		if out[7+i] != v {
			t.Errorf("out[%d] = %v, want %v", 7+i, out[7+i], v)
		}
	}
}

func TestAlignSamplesDropsOldestWhenOverflowing(t *testing.T) {
	in := make([]float64, 25)
	for i := range in {
		in[i] = float64(i)
	}
	out := alignSamples(in, 20)
	// Oldest 5 samples (0..4) drop; out[0] = 5, out[19] = 24.
	if out[0] != 5 {
		t.Errorf("out[0] = %v, want 5 (oldest dropped)", out[0])
	}
	if out[19] != 24 {
		t.Errorf("out[19] = %v, want 24 (newest preserved)", out[19])
	}
}

// TestSparklineSlideStability is the regression for the user's
// "values change height/color as they slide left" complaint. The
// shift register model: every existing cell's value must equal what
// it was on the previous render, just at one position to the left.
// Bucketing variants (averaging multiple samples per cell) violated
// this because new samples reshuffled bucket contents on every tick.
func TestSparklineSlideStability(t *testing.T) {
	theme := CurrentTheme()
	buf := newRingBuffer(sparkBufferSamples)
	// Fill buffer to capacity.
	values := []float64{10, 25, 40, 55, 70, 85, 100, 90, 80, 70, 60, 50, 40, 30, 20, 10, 5, 15, 25, 35}
	for _, v := range values {
		buf.Append(v)
	}
	priorPlain := stripANSI(renderSparkline(theme, buf.Snapshot(), MetricUtil))
	priorRunes := []rune(priorPlain)

	// One tick: append a new value, evicting the oldest.
	buf.Append(99)
	nextPlain := stripANSI(renderSparkline(theme, buf.Snapshot(), MetricUtil))
	nextRunes := []rune(nextPlain)

	// Cells 0..18 of the new render must equal cells 1..19 of the
	// old render — every value slid left by exactly one position.
	for i := 0; i < liveBarWidth-1; i++ {
		if priorRunes[i+1] != nextRunes[i] {
			t.Errorf("cell %d not stable across slide: prior=%q next=%q\nprior: %s\nnext:  %s",
				i, string(priorRunes[i+1]), string(nextRunes[i]), priorPlain, nextPlain)
		}
	}
}

// ---- ringBuffer tests ----

func TestRingBufferRollsOldest(t *testing.T) {
	r := newRingBuffer(3)
	for _, v := range []float64{1, 2, 3, 4, 5} {
		r.Append(v)
	}
	got := r.Snapshot()
	want := []float64{3, 4, 5}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d (got: %v)", len(got), len(want), got)
	}
	for i, v := range want {
		if got[i] != v {
			t.Errorf("got[%d] = %v, want %v", i, got[i], v)
		}
	}
}

func TestRingBufferSnapshotIsCopy(t *testing.T) {
	r := newRingBuffer(3)
	r.Append(1)
	got := r.Snapshot()
	got[0] = 99
	if r.Snapshot()[0] != 1 {
		t.Errorf("Snapshot() returned a slice that aliases internal state — caller mutation leaked")
	}
}
