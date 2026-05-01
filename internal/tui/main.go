package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// MainMode is the centered landing screen described in DESIGN.md §7.2.
// When the active config has at least one model, the screen embeds a
// 1-line bordered selection list directly between the version line and
// the shortcut row. When the config is empty (first-run or after a
// model delete), the list is hidden and the screen reverts to its bare
// "configure to begin" form.
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

	flash    string
	showHelp bool

	models      list.Model
	presets     list.Model
	showPresets bool // multi-preset pivot state
}

// NewMainMode constructs the landing-screen model.
func NewMainMode(cfg *config.Config, version string) MainMode {
	m := MainMode{
		cfg:     cfg,
		keys:    DefaultKeymap(),
		theme:   CurrentTheme(),
		version: version,
	}
	m.rebuildModels()
	return m
}

// SetSize is called by the root model on every WindowSizeMsg.
func (m *MainMode) SetSize(w, h int) {
	m.width, m.height = w, h
	m.applyListSize()
}

// SetCfg replaces the underlying config (after a save in config mode)
// and rebuilds the inline list. The current pivot state is reset
// because the model the user was looking at may no longer exist.
func (m *MainMode) SetCfg(cfg *config.Config) {
	m.cfg = cfg
	m.showPresets = false
	m.rebuildModels()
	m.applyListSize()
}

// SetRunning updates the "▶ Detached" line. Called by Root after session
// state changes. Empty alias hides the line and disables `a`.
func (m *MainMode) SetRunning(alias, preset string, port int) {
	m.runningAlias = alias
	m.runningPreset = preset
	m.runningPort = port
	m.rebuildModels()
}

// IsSessionRunning reports whether main mode currently shows the detached
// line / accepts the `a` shortcut. Used by Root to gate `a`.
func (m MainMode) IsSessionRunning() bool { return m.runningAlias != "" }

// SetFlash sets a short status message shown beneath the list (or
// beneath the shortcut row when the list is hidden). Used by Root to
// surface spawn errors.
func (m *MainMode) SetFlash(msg string) { m.flash = msg }

// HasModels reports whether the inline selection list is rendered.
// Root uses this to gate the no-args Enter→spawn shortcut.
func (m MainMode) HasModels() bool { return len(m.cfg.Models) > 0 }

// ---- list construction ----

// modelItem implements list.Item for the inline model list. The Title
// includes a `(running)` suffix when the alias matches the active
// session so users can see at a glance which model is live without a
// separate column.
type modelItem struct {
	model        config.Model
	runningAlias string
}

func (m modelItem) Title() string {
	suffix := ""
	if m.model.Alias == m.runningAlias && m.runningAlias != "" {
		suffix = " (running)"
	}
	return m.model.Alias + suffix
}

func (m modelItem) Description() string {
	n := len(m.model.Presets)
	switch n {
	case 0:
		return "0 presets"
	case 1:
		return "1 preset: " + m.model.Presets[0].Name
	default:
		return fmt.Sprintf("%d presets", n)
	}
}

func (m modelItem) FilterValue() string { return m.model.Alias }

type presetItem struct{ preset config.Preset }

func (p presetItem) Title() string { return p.preset.Name }
func (p presetItem) Description() string {
	if p.preset.Description != "" {
		return p.preset.Description
	}
	return fmt.Sprintf("%d params", len(p.preset.Params))
}
func (p presetItem) FilterValue() string { return p.preset.Name }

// inlineDelegate renders one row per item with reverse video on the
// selected row. Theme-aware via the Subtle color for the trailing meta
// (preset count / preset description). Single row, no spacing — the
// embedded list is a compact picker, not a screen-full menu.
type inlineDelegate struct {
	theme Theme
}

func (d inlineDelegate) Height() int                               { return 1 }
func (d inlineDelegate) Spacing() int                              { return 0 }
func (d inlineDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d inlineDelegate) Render(w io.Writer, lm list.Model, index int, item list.Item) {
	type titled interface {
		Title() string
		Description() string
	}
	t, ok := item.(titled)
	if !ok {
		return
	}
	title := t.Title()
	desc := t.Description()
	subtle := lipgloss.NewStyle().Foreground(d.theme.Subtle)
	row := title + "  " + subtle.Render("· "+desc)
	if index == lm.Index() {
		// Reverse video for the highlighted row. Literal SGR (not
		// lipgloss.Reverse) so it's deterministic without a TTY.
		fmt.Fprint(w, "\x1b[7m"+row+"\x1b[0m")
		return
	}
	fmt.Fprint(w, row)
}

func (m *MainMode) rebuildModels() {
	items := make([]list.Item, len(m.cfg.Models))
	for i, mdl := range m.cfg.Models {
		items[i] = modelItem{model: mdl, runningAlias: m.runningAlias}
	}
	if m.models.Items() == nil {
		l := list.New(items, inlineDelegate{theme: m.theme}, 0, 0)
		l.SetShowTitle(false)
		l.SetShowHelp(false)
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(false)
		l.SetShowPagination(false)
		m.models = l
	} else {
		m.models.SetItems(items)
	}
}

func (m *MainMode) rebuildPresets(model config.Model) {
	items := make([]list.Item, len(model.Presets))
	for i, p := range model.Presets {
		items[i] = presetItem{preset: p}
	}
	l := list.New(items, inlineDelegate{theme: m.theme}, 0, 0)
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(false)
	l.SetShowPagination(false)
	m.presets = l
	m.applyListSize()
}

// listInnerWidth is the inner content width for the bordered list (a
// little narrower than the terminal so the box reads as "inside"
// rather than "edge to edge"). Capped so very wide terminals don't
// stretch single-line rows uncomfortably.
func (m *MainMode) listWidth() int {
	const max = 60
	w := m.width - 8
	if w > max {
		w = max
	}
	if w < 20 {
		w = 20
	}
	return w
}

// applyListSize sizes the visible list to fit its rows. bubbles/list
// reserves an internal row for pagination state even when pagination
// is hidden, so the height passed to SetSize must be `len(items) + 1`
// or items get clipped. Capped at 12 visible rows for very long lists;
// bubbles/list then handles its own scrolling.
func (m *MainMode) applyListSize() {
	if m.cfg == nil {
		return
	}
	w := m.listWidth()
	const visibleCap = 12
	height := func(n int) int {
		h := n + 1
		if h > visibleCap+1 {
			h = visibleCap + 1
		}
		if h < 2 {
			h = 2
		}
		return h
	}
	m.models.SetSize(w, height(len(m.cfg.Models)))
	if m.showPresets {
		if it, ok := m.models.SelectedItem().(modelItem); ok {
			m.presets.SetSize(w, height(len(it.model.Presets)))
		}
	}
}

// ---- rendering ----

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

	parts := []string{wordmark, "", tagline}

	if m.IsSessionRunning() {
		parts = append(parts, "", m.renderDetached())
	}

	hasModels := m.HasModels()
	if hasModels {
		parts = append(parts, "", m.renderListBox())
	}

	if hasModels && m.flash != "" {
		parts = append(parts, "", m.renderFlash())
	}

	parts = append(parts, "", m.renderShortcuts())

	if !hasModels && m.flash != "" {
		parts = append(parts, "", m.renderFlash())
	}

	body := lipgloss.JoinVertical(lipgloss.Center, parts...)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

func (m MainMode) renderShortcuts() string {
	hasModels := m.HasModels()
	var parts []string
	if hasModels {
		parts = append(parts, shortcut("↑/↓", "navigate", m.theme))
		parts = append(parts, shortcut("Enter", "select", m.theme))
	}
	parts = append(parts, shortcut("c", "configure", m.theme))
	parts = append(parts, shortcut("?", "help", m.theme))
	parts = append(parts, shortcut("q", "quit", m.theme))
	if m.IsSessionRunning() {
		parts = append([]string{shortcut("a", "attach", m.theme)}, parts...)
	}
	return strings.Join(parts, "   ")
}

func (m MainMode) renderListBox() string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Border).
		Padding(0, 1)
	if m.showPresets {
		return box.Render(m.presets.View())
	}
	return box.Render(m.models.View())
}

func (m MainMode) renderFlash() string {
	col := m.theme.StatusErr
	low := strings.ToLower(m.flash)
	if !strings.Contains(low, "fail") && !strings.Contains(low, "error") {
		col = m.theme.StatusReady
	}
	return lipgloss.NewStyle().Foreground(col).Render(m.flash)
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
		"↑ / ↓       move selection",
		"Enter       run selected model (pivot to preset list when 2+)",
		"Esc         back out of preset pivot",
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

// ---- update ----

// Update handles main-mode key events. The inline list owns ↑/↓; Enter
// either spawns directly (0 / 1 preset) or pivots to the preset
// sub-list (2+ presets); Esc backs out of the preset pivot. The
// help-overlay toggle is preserved. `c`, `a`, `q` continue to be
// caught by Root before this handler runs.
func (m MainMode) Update(msg tea.Msg) (MainMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if m.showHelp {
			m.showHelp = false
			return m, nil
		}
		switch k.String() {
		case "?":
			m.showHelp = true
			return m, nil
		case "esc":
			if m.showPresets {
				m.showPresets = false
				return m, nil
			}
			return m, nil
		case "enter":
			if !m.HasModels() {
				return m, nil
			}
			return m.handleEnter()
		}
	}
	if !m.HasModels() {
		return m, nil
	}
	if m.showPresets {
		var cmd tea.Cmd
		m.presets, cmd = m.presets.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.models, cmd = m.models.Update(msg)
	return m, cmd
}

// handleEnter routes the spawn/pivot decision: 0 presets emits a
// SpawnRequestMsg with a synthetic "default" preset name; 1 preset
// emits with that single preset; 2+ presets pivots to the preset
// sub-list. Mirrors the previous selection-mode behavior so users with
// existing muscle memory aren't surprised.
func (m MainMode) handleEnter() (MainMode, tea.Cmd) {
	if m.showPresets {
		parent, parentOK := m.models.SelectedItem().(modelItem)
		it, ok := m.presets.SelectedItem().(presetItem)
		if !ok || !parentOK {
			return m, nil
		}
		return m, func() tea.Msg {
			return SpawnRequestMsg{Model: parent.model, Preset: it.preset}
		}
	}
	it, ok := m.models.SelectedItem().(modelItem)
	if !ok {
		return m, nil
	}
	switch len(it.model.Presets) {
	case 0:
		return m, func() tea.Msg {
			return SpawnRequestMsg{Model: it.model, Preset: config.Preset{Name: "default"}}
		}
	case 1:
		return m, func() tea.Msg {
			return SpawnRequestMsg{Model: it.model, Preset: it.model.Presets[0]}
		}
	default:
		m.showPresets = true
		m.rebuildPresets(it.model)
		return m, nil
	}
}
