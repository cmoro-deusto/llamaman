package tui

import "github.com/charmbracelet/lipgloss"

// Zone is the live-band color tier a metric value falls into. The
// thresholds are metric-specific (see zoneFor) but the four colors
// are always pulled from the active Theme: blue → idle, green →
// healthy, yellow → warning, red → danger.
type Zone int

const (
	ZoneIdle Zone = iota
	ZoneOK
	ZoneWarn
	ZoneDanger
)

// MetricKind selects the threshold ladder. Different metrics have
// different "healthy ranges" (Util's wide green band vs. Memory's
// tighter one — see Q6a in DESIGN §7.4).
type MetricKind int

const (
	MetricUtil MetricKind = iota
	MetricMem
	MetricPower
	MetricTemp
)

// zoneFor classifies a metric value (as a percentage, 0–100) into one
// of four threshold zones. Inputs outside [0, 100] clamp to the
// nearest endpoint — defensive against transient sensor glitches that
// would otherwise flash red on a nonsense reading.
func zoneFor(kind MetricKind, pct float64) Zone {
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	switch kind {
	case MetricUtil:
		// 0–30 idle, 30–60 ok, 60–85 warn, >85 danger
		switch {
		case pct < 30:
			return ZoneIdle
		case pct < 60:
			return ZoneOK
		case pct < 85:
			return ZoneWarn
		default:
			return ZoneDanger
		}
	case MetricMem, MetricPower:
		// 0–30 idle, 30–70 ok, 70–90 warn, >90 danger
		switch {
		case pct < 30:
			return ZoneIdle
		case pct < 70:
			return ZoneOK
		case pct < 90:
			return ZoneWarn
		default:
			return ZoneDanger
		}
	case MetricTemp:
		// Temp uses % of throttle ceiling. Same cuts as Util.
		switch {
		case pct < 30:
			return ZoneIdle
		case pct < 70:
			return ZoneOK
		case pct < 85:
			return ZoneWarn
		default:
			return ZoneDanger
		}
	}
	return ZoneOK
}

// zoneColor maps a Zone to the corresponding palette color from the
// current Theme. Centralized here so the live-band renderers don't
// have to keep the StatusIdle/Ready/Start/Err mapping in mind — they
// just call zoneColor(theme, zone).
func zoneColor(theme Theme, z Zone) lipgloss.Color {
	switch z {
	case ZoneIdle:
		return theme.StatusIdle
	case ZoneOK:
		return theme.StatusReady
	case ZoneWarn:
		return theme.StatusStart
	case ZoneDanger:
		return theme.StatusErr
	}
	return theme.Subtle
}
