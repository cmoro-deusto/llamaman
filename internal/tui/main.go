package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// MainMode is the centered landing screen described in DESIGN.md §7.2.
type MainMode struct {
	cfg     *config.Config
	keys    Keymap
	theme   Theme
	width   int
	height  int
	version string

	runningAlias  string
	runningPreset string
	runningPort   int

	showHelp bool
}

// NewMainMode constructs the landing-screen model.
func NewMainMode(cfg *config.Config, version string) MainMode {
	return MainMode{
		cfg:     cfg,
		keys:    DefaultKeymap(),
		theme:   CurrentTheme(),
		version: version,
	}
}

// SetSize is called by the root model on every WindowSizeMsg.
func (m *MainMode) SetSize(w, h int) {
	m.width, m.height = w, h
}

// SetRunning updates the "▶ Detached" line. Called by Root after session
// state changes. Empty alias hides the line and disables `a`.
func (m *MainMode) SetRunning(alias, preset string, port int) {
	m.runningAlias = alias
	m.runningPreset = preset
	m.runningPort = port
}

// IsSessionRunning reports whether main mode currently shows the detached
// line / accepts the `a` shortcut. Used by Root to gate `a`.
func (m MainMode) IsSessionRunning() bool { return m.runningAlias != "" }

// View renders the main screen, centered in the current terminal window.
func (m MainMode) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	if m.showHelp {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.renderHelp())
	}

	wordmark := lipgloss.NewStyle().
		Foreground(m.theme.Accent).
		Render(strings.TrimRight(Wordmark, "\n"))

	tagline := lipgloss.NewStyle().
		Foreground(m.theme.Subtle).
		Render(fmt.Sprintf("llamaman %s — llama-server manager", m.version))

	shortcuts := []string{
		shortcut("s/Enter", "select model", m.theme),
		shortcut("c", "configure", m.theme),
		shortcut("?", "help", m.theme),
		shortcut("q", "quit", m.theme),
	}
	if m.IsSessionRunning() {
		shortcuts = append([]string{shortcut("a", "attach", m.theme)}, shortcuts...)
	}
	hint := strings.Join(shortcuts, "   ")

	parts := []string{wordmark, "", tagline}
	if m.IsSessionRunning() {
		parts = append(parts, "", m.renderDetached())
	}
	parts = append(parts, "", hint)

	body := lipgloss.JoinVertical(lipgloss.Center, parts...)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

func (m MainMode) renderDetached() string {
	preset := m.runningPreset
	if preset == "" {
		preset = "—"
	}
	line := fmt.Sprintf("▶ Detached: %s/%s listening on :%d — press a to attach",
		m.runningAlias, preset, m.runningPort)
	return lipgloss.NewStyle().Foreground(m.theme.StatusReady).Render(line)
}

func (m MainMode) renderHelp() string {
	keys := []string{
		"s / Enter   open selection mode",
		"c           open configuration mode",
		"a           attach to running session (only when one exists)",
		"?           toggle this help",
		"q / Ctrl+C  quit",
	}
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Accent).
		Padding(1, 3).
		Render("Main mode keys\n\n" + strings.Join(keys, "\n"))
}

func shortcut(key, label string, t Theme) string {
	keyStyle := lipgloss.NewStyle().Foreground(t.Accent).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(t.Subtle)
	return keyStyle.Render(key) + " " + labelStyle.Render(label)
}

// Update handles main-mode key events and toggles the help overlay.
func (m MainMode) Update(msg tea.Msg) (MainMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		if k.String() == "?" {
			m.showHelp = true
		}
	}
	return m, nil
}
