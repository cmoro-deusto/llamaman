package tui

import "testing"

func TestZoneForUtil(t *testing.T) {
	cases := []struct {
		pct  float64
		want Zone
	}{
		{0, ZoneIdle},
		{29.9, ZoneIdle},
		{30, ZoneOK},
		{59.9, ZoneOK},
		{60, ZoneWarn},
		{84.9, ZoneWarn},
		{85, ZoneDanger},
		{100, ZoneDanger},
		{-5, ZoneIdle},    // clamps low
		{120, ZoneDanger}, // clamps high
	}
	for _, c := range cases {
		if got := zoneFor(MetricUtil, c.pct); got != c.want {
			t.Errorf("zoneFor(Util, %v) = %v, want %v", c.pct, got, c.want)
		}
	}
}

func TestZoneForMemory(t *testing.T) {
	cases := []struct {
		pct  float64
		want Zone
	}{
		{0, ZoneIdle},
		{29.9, ZoneIdle},
		{30, ZoneOK},
		{69.9, ZoneOK},
		{70, ZoneWarn},
		{89.9, ZoneWarn},
		{90, ZoneDanger},
	}
	for _, c := range cases {
		if got := zoneFor(MetricMem, c.pct); got != c.want {
			t.Errorf("zoneFor(Mem, %v) = %v, want %v", c.pct, got, c.want)
		}
	}
}

func TestZoneForPowerMatchesMemory(t *testing.T) {
	// Q6a says Power and Memory share the same threshold cuts.
	for pct := 0.0; pct <= 100; pct += 5 {
		mem := zoneFor(MetricMem, pct)
		pwr := zoneFor(MetricPower, pct)
		if mem != pwr {
			t.Errorf("Power vs Mem mismatch at %v%%: power=%v mem=%v", pct, pwr, mem)
		}
	}
}

func TestZoneForTemp(t *testing.T) {
	cases := []struct {
		pct  float64
		want Zone
	}{
		{0, ZoneIdle},
		{30, ZoneOK},
		{70, ZoneWarn},
		{85, ZoneDanger},
	}
	for _, c := range cases {
		if got := zoneFor(MetricTemp, c.pct); got != c.want {
			t.Errorf("zoneFor(Temp, %v) = %v, want %v", c.pct, got, c.want)
		}
	}
}

func TestZoneColorPicksTheRightThemeField(t *testing.T) {
	theme := CurrentTheme()
	cases := []struct {
		z    Zone
		want any
	}{
		{ZoneIdle, theme.StatusIdle},
		{ZoneOK, theme.StatusReady},
		{ZoneWarn, theme.StatusStart},
		{ZoneDanger, theme.StatusErr},
	}
	for _, c := range cases {
		if got := zoneColor(theme, c.z); got != c.want {
			t.Errorf("zoneColor(%v) = %v, want %v", c.z, got, c.want)
		}
	}
}
