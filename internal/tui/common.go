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

// Theme holds the resolved color palette for the current run. Two
// hard-coded variants in v1: dark and light. NO_COLOR is honored
// automatically by lipgloss when termenv detects it.
//
// StatusIdle/Ready/Start/Err double as the live-band metric color
// zones (see Zone* constants in zones.go): the same palette that
// drives the run-mode status badge tints bars, sparklines, and
// trailing values when their value crosses the threshold cuts.
type Theme struct {
	Accent      lipgloss.Color
	Subtle      lipgloss.Color
	Muted       lipgloss.Color
	StatusIdle  lipgloss.Color // light blue — low/idle utilization (Q6a Blue zone)
	StatusReady lipgloss.Color // green   — healthy operating range
	StatusStart lipgloss.Color // yellow  — elevated; warning
	StatusErr   lipgloss.Color // red     — saturated; danger
	StatusGone  lipgloss.Color
	BorderFocus lipgloss.Color
	Border      lipgloss.Color
}

// CurrentTheme picks the palette based on terminal background.
func CurrentTheme() Theme {
	if lipgloss.HasDarkBackground() {
		return Theme{
			Accent:      lipgloss.Color("#E8A33D"), // soft orange (DESIGN §10.4)
			Subtle:      lipgloss.Color("#9A9A9A"),
			Muted:       lipgloss.Color("#5C5C5C"),
			StatusIdle:  lipgloss.Color("#7DC4E4"), // steel-blue / soft cyan
			StatusReady: lipgloss.Color("#7BC96F"),
			StatusStart: lipgloss.Color("#E8C547"),
			StatusErr:   lipgloss.Color("#E06C75"),
			StatusGone:  lipgloss.Color("#7C7C7C"),
			BorderFocus: lipgloss.Color("#E8A33D"),
			Border:      lipgloss.Color("#444444"),
		}
	}
	return Theme{
		Accent:      lipgloss.Color("#C26B11"),
		Subtle:      lipgloss.Color("#5A5A5A"),
		Muted:       lipgloss.Color("#9A9A9A"),
		StatusIdle:  lipgloss.Color("#3A7AAB"), // medium blue — readable on light bg
		StatusReady: lipgloss.Color("#1F7A28"),
		StatusStart: lipgloss.Color("#A06B00"),
		StatusErr:   lipgloss.Color("#B22222"),
		StatusGone:  lipgloss.Color("#7C7C7C"),
		BorderFocus: lipgloss.Color("#C26B11"),
		Border:      lipgloss.Color("#BBBBBB"),
	}
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
