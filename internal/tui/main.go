package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/modelsini"
)

// mainMode selects which picker the landing screen shows: single-model
// launches from config.json, or router-mode launches from my-models.ini
// files registered in globals.models-files.
type mainMode int

const (
	modeSingle mainMode = iota
	modeRouter
)

// MainMode is the centered landing screen described in DESIGN.md §7.2.
// When the active config has at least one model, the screen embeds a
// 1-line bordered selection list directly between the version line and
// the shortcut row. When the config is empty (first-run or after a
// model delete), the list is hidden and the screen reverts to its bare
// "configure to begin" form.
//
// `tab` toggles between Single Model mode (config.json models) and
// Router mode (globals.models-files entries — each spawns one
// llama-server hosting every model in the file).
type MainMode struct {
	// statusLine is a one-line status hint rendered above the shortcuts
	// (Root uses it to surface an in-flight download on Main).
	statusLine string
	cfg     *config.Config
	cfgPath string // config file path; drives the derived models.ini default
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

	mode mainMode

	models      list.Model
	presets     list.Model
	routerFiles list.Model
	showPresets bool // multi-preset pivot state
}

// NewMainMode constructs the landing-screen model. theme is the
// resolved palette (DESIGN §15.1).
func NewMainMode(cfg *config.Config, version string, theme Theme) MainMode {
	m := MainMode{
		cfg:     cfg,
		keys:    DefaultKeymap(),
		theme:   theme,
		version: version,
	}
	m.rebuildModels()
	m.rebuildRouterFiles()
	return m
}

// SetTheme swaps the active palette and rebuilds the inline list
// delegates (they capture the theme at list construction). Selection
// positions are preserved. Used by Root after a Settings save or a
// quick-key theme cycle.
func (m *MainMode) SetTheme(t Theme) {
	m.theme = t
	// A theme change resets any preset pivot: the sub-list would render
	// against a stale delegate otherwise.
	m.showPresets = false
	modelIdx := m.models.Index()
	routerIdx := m.routerFiles.Index()
	m.models = list.Model{}
	m.routerFiles = list.Model{}
	m.presets = list.Model{}
	m.rebuildModels()
	m.rebuildRouterFiles()
	m.models.Select(modelIdx)
	m.routerFiles.Select(routerIdx)
	m.applyListSize()
}

// SetSize is called by the root model on every WindowSizeMsg.
func (m *MainMode) SetSize(w, h int) {
	m.width, m.height = w, h
	m.applyListSize()
}

// SetCfg replaces the underlying config (after a save in config mode)
// and rebuilds both pickers. The current pivot state is reset because
// the model the user was looking at may no longer exist.
func (m *MainMode) SetCfg(cfg *config.Config) {
	m.cfg = cfg
	m.showPresets = false
	m.rebuildModels()
	m.rebuildRouterFiles()
	m.applyListSize()
}

// SetCfgPath records the config file path, which drives the derived
// models.ini default for the Router source list. Called by Root after
// construction and on first-run completion.
func (m *MainMode) SetCfgPath(cfgPath string) {
	m.cfgPath = cfgPath
	m.rebuildRouterFiles()
	m.applyListSize()
}

// SetRunning records the live session identity for the reattach screen
// (DESIGN §15.2). Called by Root after session state changes. Empty
// alias clears the reattach state. Router sessions report the
// models-file path as their alias.
func (m *MainMode) SetRunning(alias, preset string, port int) {
	m.runningAlias = alias
	m.runningPreset = preset
	m.runningPort = port
	m.rebuildModels()
	m.rebuildRouterFiles()
}

// SetMode switches the Single Model / Router toggle and clears any
// preset pivot. Used by Root when a run session ends so the main menu
// lands on the mode the session belonged to (a killed router returns
// to Router mode even when it was reattached from Single mode).
func (m *MainMode) SetMode(mode mainMode) {
	m.mode = mode
	m.showPresets = false
	m.applyListSize()
}

// IsSessionRunning reports whether Main is in the reattach state (a
// live session). Used by Root to gate `a` and by Main to switch to the
// reattach screen.
func (m MainMode) IsSessionRunning() bool { return m.runningAlias != "" }

// SetFlash sets a short status message shown beneath the list (or
// beneath the shortcut row when the list is hidden). Used by Root to
// surface spawn errors.
// SetStatusLine sets the one-line status hint shown above the shortcuts.
func (m *MainMode) SetStatusLine(line string) { m.statusLine = line }

func (m *MainMode) SetFlash(msg string) { m.flash = msg }

// HasModels reports whether the inline selection list is rendered in
// the current mode (models in Single mode, router sources in Router
// mode). Root uses this to gate the no-args Enter→spawn shortcut.
func (m MainMode) HasModels() bool {
	if m.mode == modeRouter {
		return len(modelsini.EffectiveModelsFiles(m.cfg, m.cfgPath)) > 0
	}
	return len(m.cfg.Models) > 0
}

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
// (preset count / preset description). Each row carries a leading
// source tag in Muted — local / hf / router (DESIGN §15.2) — and the
// highlighted model row with 2+ presets previews the preset names in
// its description, ellipsized to the row width. Single row, no spacing
// — the embedded list is a compact picker, not a screen-full menu.
type inlineDelegate struct {
	theme Theme
}

func (d inlineDelegate) Height() int                             { return 1 }
func (d inlineDelegate) Spacing() int                            { return 0 }
func (d inlineDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

func (d inlineDelegate) Render(w io.Writer, lm list.Model, index int, item list.Item) {
	subtle := lipgloss.NewStyle().Foreground(d.theme.Subtle)
	muted := lipgloss.NewStyle().Foreground(d.theme.Muted)

	var tag, title, desc string
	switch it := item.(type) {
	case modelItem:
		tag = it.model.SourceLabel() // "local" | "hf"
		title = it.Title()
		desc = it.Description()
		if index == lm.Index() && len(it.model.Presets) >= 2 {
			desc = previewPresets(it.model, lm.Width())
		}
	case routerItem:
		tag = "router"
		title = it.Title()
		desc = it.Description()
	case presetItem: // preset sub-list rows carry no source tag
		title = it.Title()
		desc = it.Description()
	default:
		return
	}

	// Clamp the description so the row never exceeds the list width
	// (long router paths, previews): bubbles/list does not pad custom
	// delegate output, so an unclamped row would widen the enclosing
	// box (DESIGN §15.2).
	prefix := title
	if tag != "" {
		prefix = tag + strings.Repeat(" ", 6-len(tag)) + " " + title
	}
	const sep = "  · " // visible separator before the description
	if avail := lm.Width() - lipgloss.Width(prefix) - len(sep); avail > 8 {
		desc = truncateRunes(desc, avail)
	}

	row := title + sep + desc
	if tag != "" {
		row = tag + strings.Repeat(" ", 6-len(tag)) + " " + row
	}

	if index == lm.Index() {
		// Highlighted row: plain text wrapped in a single reverse-video
		// SGR pair. Styling the substrings would inject their own
		// `\x1b[0m` resets and break the reverse video mid-row (owner
		// feedback), so the highlighted row carries no inner colors.
		if pad := lm.Width() - lipgloss.Width(row); pad > 0 {
			row += strings.Repeat(" ", pad)
		}
		// Literal SGR (not lipgloss.Reverse) so it's deterministic
		// without a TTY.
		fmt.Fprint(w, "\x1b[7m"+row+"\x1b[0m")
		return
	}

	// Non-highlighted row: styled tag + description, padded to the list
	// width so the enclosing box keeps a stable size regardless of which
	// row is highlighted.
	row = title + sep + subtle.Render(desc)
	if tag != "" {
		row = muted.Render(tag+strings.Repeat(" ", 6-len(tag))) + " " + row
	}
	if pad := lm.Width() - lipgloss.Width(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	fmt.Fprint(w, row)
}

// previewPresets builds the highlighted-row description for a model
// with 2+ presets: the count plus the actual preset names, ellipsized
// to fit the remaining row width (DESIGN §15.2 item 4).
func previewPresets(m config.Model, rowWidth int) string {
	names := make([]string, 0, len(m.Presets))
	for _, p := range m.Presets {
		names = append(names, p.Name)
	}
	s := fmt.Sprintf("%d presets: %s", len(m.Presets), strings.Join(names, " · "))
	// The leading tag column (7), title, and "· " separator consume the
	// rest of the row; a conservative overhead keeps the ellipsis sane.
	const overhead = 14
	if avail := rowWidth - overhead; avail > 0 && len([]rune(s)) > avail {
		return truncateRunes(s, avail)
	}
	return s
}

// truncateRunes cuts s to at most max runes, appending an ellipsis.
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return "…"
	}
	return string(r[:max-1]) + "…"
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

// routerItem implements list.Item for the Router-mode picker: one entry
// per globals.models-files path. The section count comes from parsing
// the file; parse failures surface as a warning instead of hiding the
// entry.
type routerItem struct {
	path         string
	runningAlias string
	sectionCount int
	parseErr     string
}

func (r routerItem) Title() string {
	suffix := ""
	if r.path == r.runningAlias && r.runningAlias != "" {
		suffix = " (running)"
	}
	return filepath.Base(r.path) + suffix
}

func (r routerItem) Description() string {
	if r.parseErr != "" {
		return "parse error — " + r.path
	}
	return fmt.Sprintf("router · %d model%s — %s", r.sectionCount, plural(r.sectionCount), r.path)
}

func (r routerItem) FilterValue() string { return r.path }

func (m *MainMode) rebuildRouterFiles() {
	files := modelsini.EffectiveModelsFiles(m.cfg, m.cfgPath)
	items := make([]list.Item, len(files))
	for i, path := range files {
		it := routerItem{path: path, runningAlias: m.runningAlias}
		if f, err := modelsini.ParseFile(path); err == nil {
			it.sectionCount = len(f.Sections)
		} else {
			it.parseErr = err.Error()
		}
		items[i] = it
	}
	if m.routerFiles.Items() == nil {
		l := list.New(items, inlineDelegate{theme: m.theme}, 0, 0)
		l.SetShowTitle(false)
		l.SetShowHelp(false)
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(false)
		l.SetShowPagination(false)
		m.routerFiles = l
	} else {
		m.routerFiles.SetItems(items)
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
// rather than "edge to edge"). Grows with wide terminals (cap 90,
// DESIGN §15.2) so rows can show more; capped so single-line rows
// don't stretch uncomfortably.
func (m *MainMode) listWidth() int {
	const max = 90
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
	if m.mode == modeRouter {
		m.routerFiles.SetSize(w, height(len(m.cfg.Globals.ModelsFiles)))
		return
	}
	m.models.SetSize(w, height(len(m.cfg.Models)))
	if m.showPresets {
		if it, ok := m.models.SelectedItem().(modelItem); ok {
			m.presets.SetSize(w, height(len(it.model.Presets)))
		}
	}
}

// ---- rendering ----

// View renders the main screen, centered in the current terminal
// window. When a session is running, Main becomes a single reattach
// entry (DESIGN §15.2); otherwise the centered column shows the model
// list (or the empty state) as usual.
func (m MainMode) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	height := m.height
	if m.showHelp {
		return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, m.renderHelp())
	}

	wordmark := lipgloss.NewStyle().
		Foreground(m.theme.Accent).
		Render(strings.TrimRight(Wordmark, "\n"))

	tagline := lipgloss.NewStyle().
		Foreground(m.theme.Subtle).
		Render(fmt.Sprintf("llamaman %s — llama-server manager", m.version))

	parts := []string{wordmark, "", tagline}

	running := m.IsSessionRunning()
	hasModels := m.HasModels()
	switch {
	case running:
		// A session is live: Main becomes a single reattach entry — the
		// model list is hidden until the session ends (DESIGN §15.2).
		parts = append(parts, "", m.renderReattachBox())
	case hasModels:
		parts = append(parts, "", m.renderListBox())
	default:
		parts = append(parts, "", m.renderEmptyState())
	}

	if (running || hasModels) && m.flash != "" {
		parts = append(parts, "", m.renderFlash())
	}

	if m.statusLine != "" {
		parts = append(parts, "",
			lipgloss.NewStyle().Foreground(m.theme.StatusStart).Render(m.statusLine))
	}

	parts = append(parts, "", m.renderShortcuts())

	if !running && !hasModels && m.flash != "" {
		parts = append(parts, "", m.renderFlash())
	}

	body := lipgloss.JoinVertical(lipgloss.Center, parts...)
	return lipgloss.Place(m.width, height, lipgloss.Center, lipgloss.Center, body)
}

// renderReattachBox is the single-entry screen shown while a session is
// running (DESIGN §15.2): one highlighted row carrying the session
// identity, Enter/a attach. The model list returns when the session
// ends.
func (m MainMode) renderReattachBox() string {
	preset := m.runningPreset
	if preset == "" {
		preset = "—"
	}
	row := fmt.Sprintf("running  %s/%s · listening on :%d",
		m.runningAlias, preset, m.runningPort)
	if pad := m.listWidth() - lipgloss.Width(row); pad > 0 {
		row += strings.Repeat(" ", pad)
	}
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Border).
		Padding(0, 1)
	// Highlighted like the model-list selection: plain text in a single
	// reverse-video SGR pair (no inner colors, DESIGN §15.2).
	return box.Render("\x1b[7m" + row + "\x1b[0m")
}

func (m MainMode) renderShortcuts() string {
	if m.IsSessionRunning() {
		// Reattach screen: a single entry, no list navigation. `q` quits
		// llamaman leaving the server running (DESIGN §15.2).
		parts := []string{
			shortcut("Enter", "attach", m.theme),
			shortcut("a", "attach", m.theme),
			shortcut("c", "configure", m.theme),
			shortcut("s", "storage", m.theme),
			shortcut("b", "browse", m.theme),
			shortcut("p", "preferences", m.theme),
			shortcut("?", "help", m.theme),
			shortcut("q", "quit", m.theme),
		}
		return strings.Join(parts, "   ")
	}
	hasModels := m.HasModels()
	var parts []string
	if hasModels {
		parts = append(parts, shortcut("↑/↓", "navigate", m.theme))
		parts = append(parts, shortcut("Enter", "select", m.theme))
	}
	modeLabel := "router"
	if m.mode == modeRouter {
		modeLabel = "single"
	}
	parts = append(parts, shortcut("tab", modeLabel, m.theme))
	parts = append(parts, shortcut("c", "configure", m.theme))
	parts = append(parts, shortcut("s", "storage", m.theme))
	parts = append(parts, shortcut("b", "browse", m.theme))
	parts = append(parts, shortcut("p", "preferences", m.theme))
	parts = append(parts, shortcut("t", "theme", m.theme))
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
	if m.mode == modeRouter {
		return box.Render(m.routerFiles.View())
	}
	if m.showPresets {
		return box.Render(m.presets.View())
	}
	return box.Render(m.models.View())
}

// renderEmptyState is shown when the current mode has nothing to list.
// It must lead the user to the next step instead of leaving a blank
// screen: Router mode points at globals "models files" (config mode)
// and the CLI escapes hatch; Single mode points at config mode.
func (m MainMode) renderEmptyState() string {
	var lines []string
	if m.mode == modeRouter {
		lines = []string{
			"No router sources yet.",
			"",
			"A router source is a my-models.ini file (llama.cpp model presets).",
			"Add one in config mode: press c, then edit \"models files\" under globals",
			"(one file path per line).",
			"",
			"Or run a file ad-hoc without registering it:  llamaman -i <file>",
			"Or ingest one as single-model presets:     llamaman import <file>",
		}
	} else {
		lines = []string{
			"No models configured yet.",
			"",
			"Press c to open config mode and add a model.",
		}
	}
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Border).
		Padding(0, 1)
	body := lipgloss.NewStyle().Foreground(m.theme.Subtle).Render(strings.Join(lines, "\n"))
	return box.Render(body)
}

func (m MainMode) renderFlash() string {
	col := m.theme.StatusErr
	low := strings.ToLower(m.flash)
	if !strings.Contains(low, "fail") && !strings.Contains(low, "error") {
		col = m.theme.StatusReady
	}
	return lipgloss.NewStyle().Foreground(col).Render(m.flash)
}

func (m MainMode) renderHelp() string {
	if m.IsSessionRunning() {
		keys := []string{
			"Enter / a attach to the running session",
			"c           open configuration mode",
			"s           open storage manager (cache, downloads)",
			"p           open preferences (theme, animations, models-dir)",
			"t / Shift+t cycle theme (forward / backward)",
			"?           toggle this help",
			"q / Ctrl+C  quit (server keeps running)",
		}
		return lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(m.theme.Accent).
			Padding(1, 3).
			Render("Main mode keys (session running)\n\n" + strings.Join(keys, "\n"))
	}
	keys := []string{
		"↑ / ↓       move selection",
		"Enter       run selected model (pivot to preset list when 2+)",
		"tab         toggle Single Model / Router mode",
		"Esc         back out of preset pivot",
		"c           open configuration mode",
		"s           open storage manager (cache, downloads)",
		"b           open Hugging Face browser (search, filters)",
		"p           open preferences (theme, animations, models-dir)",
		"t / Shift+t cycle theme (forward / backward)",
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
		case "enter":
			if m.IsSessionRunning() {
				// Reattach screen: Enter attaches to the running session.
				return m, func() tea.Msg { return reattachRequestMsg{} }
			}
			if !m.HasModels() {
				return m, nil
			}
			return m.handleEnter()
		case "tab", "esc":
			if m.IsSessionRunning() {
				return m, nil // no list to toggle/escape on the reattach screen
			}
			if k.String() == "tab" {
				m.showPresets = false
				if m.mode == modeRouter {
					m.mode = modeSingle
				} else {
					m.mode = modeRouter
				}
				m.applyListSize()
				return m, nil
			}
			if m.showPresets {
				m.showPresets = false
				return m, nil
			}
			return m, nil
		}
	}
	if m.IsSessionRunning() || !m.HasModels() {
		return m, nil
	}
	if m.mode == modeRouter {
		var cmd tea.Cmd
		m.routerFiles, cmd = m.routerFiles.Update(msg)
		return m, cmd
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
	if m.mode == modeRouter {
		it, ok := m.routerFiles.SelectedItem().(routerItem)
		if !ok {
			return m, nil
		}
		return m, func() tea.Msg {
			return RouterSpawnRequestMsg{File: it.path}
		}
	}
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
