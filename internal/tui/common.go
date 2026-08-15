// Package tui contains all Bubble Tea models for llamaman: main, selection,
// run, and configuration mode (built incrementally across phases).
package tui

import (
	_ "embed"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

//go:embed wordmark.txt
var Wordmark string

// Theme holds the resolved color palette for the current run. The
// palette table and resolver live in theme.go (DESIGN §15.1): 23
// curated palettes + "auto", resolved from preferences.theme.
// NO_COLOR is honored automatically by lipgloss when termenv detects
// it.
//
// StatusIdle/Ready/Start/Err double as the live-band metric color
// zones (see Zone* constants in zones.go): the same palette that
// drives the run-mode status badge tints bars, sparklines, and
// trailing values when their value crosses the threshold cuts.
type Theme struct {
	Accent lipgloss.Color
	// SegmentPrompt and SegmentGen tint the context-breakdown bar
	// (run.go renderContextBreakdownRow / renderSegmentedBar): prompt
	// tokens purple, generated tokens orange. Per-palette values are
	// each family's canonical purple/orange (or a light/dark-adapted
	// variant for custom light themes).
	SegmentPrompt lipgloss.Color
	SegmentGen    lipgloss.Color
	Subtle        lipgloss.Color
	Muted         lipgloss.Color
	StatusIdle    lipgloss.Color // light blue — low/idle utilization (Q6a Blue zone)
	StatusReady   lipgloss.Color // green   — healthy operating range
	StatusStart   lipgloss.Color // yellow  — elevated; warning
	StatusErr     lipgloss.Color // red     — saturated; danger
	StatusGone    lipgloss.Color
	BorderFocus   lipgloss.Color
	Border        lipgloss.Color
}

// Keymap groups the key bindings shared across modes. Mode-specific
// bindings live in each mode's file.
type Keymap struct {
	Quit      key.Binding
	Help      key.Binding
	Back      key.Binding
	Up        key.Binding
	Down      key.Binding
	Enter     key.Binding
	Filter    key.Binding
	Selection key.Binding
	Config    key.Binding
}

// DefaultKeymap returns the canonical bindings from DESIGN.md §7.1.
func DefaultKeymap() Keymap {
	return Keymap{
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		Up:        key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:      key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		Filter:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Selection: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "select model")),
		Config:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "configure")),
	}
}
