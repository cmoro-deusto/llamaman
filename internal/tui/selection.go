package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// SelectionMode is the model picker described in DESIGN.md §7.3. When the
// active model has 2+ presets, pressing Enter pivots to a sub-list of
// presets with the same Enter/e/Esc semantics. Selection-mode-direct
// actions ('n' new model, 'd' delete model) emit messages that the root
// turns into config-mode transitions or in-memory mutations.
type SelectionMode struct {
	cfg           *config.Config
	cfgPath       string
	models        list.Model
	presets       list.Model
	showPresets   bool
	width, height int
	theme         Theme

	runningAlias string

	// confirm modal state when 'd' is pressed.
	delConfirm     *huh.Form
	delConfirmFlag *bool
	flash          string
}

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

// NewSelectionMode builds the model picker. cfgPath is only used when the
// user hits 'd' to delete a model — selection mode persists the deletion
// directly so the change survives across subsequent invocations.
func NewSelectionMode(cfg *config.Config) SelectionMode {
	s := SelectionMode{cfg: cfg, theme: CurrentTheme()}
	s.rebuildModels()
	return s
}

// SetCfgPath supplies the on-disk config path so direct mutators (delete)
// can save. Root calls this right after construction.
func (s *SelectionMode) SetCfgPath(p string) { s.cfgPath = p }

// SetRunningAlias updates the (running) marker. Called by Root whenever
// session state may have changed. No-op on a zero-value SelectionMode.
func (s *SelectionMode) SetRunningAlias(alias string) {
	s.runningAlias = alias
	if !s.initialized() {
		return
	}
	s.rebuildModels()
}

// SetCfg replaces the underlying config (after a save in config mode).
func (s *SelectionMode) SetCfg(cfg *config.Config) {
	s.cfg = cfg
	s.rebuildModels()
}

// SetFlash sets the bottom-of-pane status message (red on errors). Used
// by Root to surface spawn failures so Enter never fails silently.
func (s *SelectionMode) SetFlash(msg string) {
	s.flash = msg
}

func (s *SelectionMode) rebuildModels() {
	models := make([]config.Model, len(s.cfg.Models))
	copy(models, s.cfg.Models)
	sort.Slice(models, func(i, j int) bool { return models[i].Alias < models[j].Alias })

	items := make([]list.Item, len(models))
	for i, m := range models {
		items[i] = modelItem{model: m, runningAlias: s.runningAlias}
	}

	if s.models.Items() == nil {
		l := list.New(items, list.NewDefaultDelegate(), 0, 0)
		l.Title = "Models"
		l.SetShowHelp(false)
		l.SetShowStatusBar(false)
		l.SetFilteringEnabled(true)
		s.models = l
	} else {
		s.models.SetItems(items)
	}
	s.models.SetSize(s.width, max(0, s.height-2))
}

func (s *SelectionMode) rebuildPresets(model config.Model) {
	items := make([]list.Item, len(model.Presets))
	for i, p := range model.Presets {
		items[i] = presetItem{preset: p}
	}
	l := list.New(items, list.NewDefaultDelegate(), s.width, max(0, s.height-2))
	l.Title = "Presets — " + model.Alias
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetFilteringEnabled(true)
	s.presets = l
}

// SetSize tracks terminal dimensions for layout. Safe to call on a
// zero-value SelectionMode (e.g., when Root is in first-run mode and
// selection was never constructed).
func (s *SelectionMode) SetSize(w, h int) {
	s.width, s.height = w, h
	if !s.initialized() {
		return
	}
	s.models.SetSize(w, max(0, h-2))
	if s.showPresets {
		s.presets.SetSize(w, max(0, h-2))
	}
}

// initialized reports whether NewSelectionMode (or SetCfg) has wired up
// the underlying lists. Zero-value SelectionMode returns false.
func (s *SelectionMode) initialized() bool { return s.cfg != nil }

// Update routes selection-mode messages.
func (s SelectionMode) Update(msg tea.Msg) (SelectionMode, tea.Cmd) {
	if s.delConfirm != nil {
		return s.updateDeleteConfirm(msg)
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		if s.showPresets {
			return s.handlePresetKey(k)
		}
		return s.handleModelKey(k)
	}
	if s.showPresets {
		var cmd tea.Cmd
		s.presets, cmd = s.presets.Update(msg)
		return s, cmd
	}
	var cmd tea.Cmd
	s.models, cmd = s.models.Update(msg)
	return s, cmd
}

func (s SelectionMode) updateDeleteConfirm(msg tea.Msg) (SelectionMode, tea.Cmd) {
	model, cmd := s.delConfirm.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		s.delConfirm = f
	}
	if s.delConfirm != nil && s.delConfirm.State == huh.StateCompleted {
		confirm := s.delConfirmFlag != nil && *s.delConfirmFlag
		s.delConfirm = nil
		s.delConfirmFlag = nil
		if confirm {
			s.applyModelDelete()
		}
	}
	return s, cmd
}

func (s SelectionMode) handleModelKey(k tea.KeyMsg) (SelectionMode, tea.Cmd) {
	switch k.String() {
	case "esc":
		return s, func() tea.Msg { return returnToMainMsg{} }
	case "enter":
		it, ok := s.models.SelectedItem().(modelItem)
		if !ok {
			return s, nil
		}
		switch len(it.model.Presets) {
		case 0:
			return s, func() tea.Msg {
				return SpawnRequestMsg{Model: it.model, Preset: config.Preset{Name: "default"}}
			}
		case 1:
			return s, func() tea.Msg {
				return SpawnRequestMsg{Model: it.model, Preset: it.model.Presets[0]}
			}
		default:
			s.rebuildPresets(it.model)
			s.showPresets = true
			return s, nil
		}
	case "e":
		if it, ok := s.models.SelectedItem().(modelItem); ok {
			alias := it.model.Alias
			return s, func() tea.Msg { return editFromSelectionMsg{Alias: alias} }
		}
	case "n":
		return s, func() tea.Msg { return newModelFromSelectionMsg{} }
	case "d":
		if it, ok := s.models.SelectedItem().(modelItem); ok {
			s.openDeleteConfirm(it.model)
		}
		return s, nil
	}
	var cmd tea.Cmd
	s.models, cmd = s.models.Update(k)
	return s, cmd
}

func (s SelectionMode) handlePresetKey(k tea.KeyMsg) (SelectionMode, tea.Cmd) {
	parent, ok := s.models.SelectedItem().(modelItem)
	if !ok {
		s.showPresets = false
		return s, nil
	}
	switch k.String() {
	case "esc":
		s.showPresets = false
		return s, nil
	case "enter":
		if it, ok := s.presets.SelectedItem().(presetItem); ok {
			return s, func() tea.Msg {
				return SpawnRequestMsg{Model: parent.model, Preset: it.preset}
			}
		}
	case "e":
		if it, ok := s.presets.SelectedItem().(presetItem); ok {
			alias := parent.model.Alias
			name := it.preset.Name
			return s, func() tea.Msg {
				return editPresetFromSelectionMsg{Alias: alias, Preset: name}
			}
		}
	}
	var cmd tea.Cmd
	s.presets, cmd = s.presets.Update(k)
	return s, cmd
}

func (s *SelectionMode) openDeleteConfirm(m config.Model) {
	confirm := false
	s.delConfirmFlag = &confirm
	prompt := fmt.Sprintf("Delete model %q (%d preset%s)?",
		m.Alias, len(m.Presets), pluralS(len(m.Presets)))
	s.delConfirm = huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).
			Affirmative("Delete").Negative("Cancel").
			Value(&confirm),
	)).WithTheme(huh.ThemeBase())
	_ = s.delConfirm.Init()
}

func (s *SelectionMode) applyModelDelete() {
	it, ok := s.models.SelectedItem().(modelItem)
	if !ok {
		return
	}
	for i, m := range s.cfg.Models {
		if m.Alias == it.model.Alias {
			s.cfg.Models = append(s.cfg.Models[:i], s.cfg.Models[i+1:]...)
			break
		}
	}
	s.rebuildModels()
	if s.cfgPath != "" {
		if err := config.Save(s.cfgPath, s.cfg); err != nil {
			s.flash = fmt.Sprintf("save failed: %v", err)
			return
		}
		s.flash = fmt.Sprintf("deleted %q", it.model.Alias)
	}
}

// View renders either the models list or the presets sub-list.
func (s SelectionMode) View() string {
	if s.delConfirm != nil {
		box := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(s.theme.Accent).
			Padding(1, 3).
			Render(s.delConfirm.View())
		return lipgloss.Place(s.width, s.height, lipgloss.Center, lipgloss.Center, box)
	}
	if s.width == 0 {
		return ""
	}
	footer := lipgloss.NewStyle().Foreground(s.theme.Subtle).
		Render("enter: select · e: edit · n: new model · d: delete · /: filter · esc: back")
	if s.showPresets {
		footer = lipgloss.NewStyle().Foreground(s.theme.Subtle).
			Render("enter: run preset · e: edit preset · esc: back to models")
		body := s.presets.View()
		if s.flash != "" {
			body = lipgloss.JoinVertical(lipgloss.Left,
				lipgloss.NewStyle().Foreground(s.theme.StatusReady).Render(s.flash), body)
		}
		return lipgloss.JoinVertical(lipgloss.Left, body, footer)
	}
	body := s.models.View()
	if s.flash != "" {
		body = lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Foreground(s.flashColor()).Render(s.flash), body)
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, footer)
}

// flashColor picks red for failures, green for confirmations.
func (s SelectionMode) flashColor() lipgloss.Color {
	low := strings.ToLower(s.flash)
	if strings.HasPrefix(low, "fail") || strings.Contains(low, "error") || strings.HasPrefix(low, "spawn failed") {
		return s.theme.StatusErr
	}
	return s.theme.StatusReady
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
