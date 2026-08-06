package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// SettingsMode edits exactly the top-level `preferences` config object
// (DESIGN §15.1): theme + animations, saved through the standard atomic
// path on submit; Esc discards. Quick keys (`t`/`shift+t` in Main) are
// shortcuts that write the same object — Settings is not a second
// source of truth (P8).
type SettingsMode struct {
	cfgPath string
	cfg     *config.Config
	theme   Theme
	darkBg  bool

	form     *huh.Form
	themeVal string
	anim     bool

	applied *config.Preferences // non-nil after a successful submit
	warn    string              // unknown/incompatible theme banner

	width, height int
}

// NewSettingsMode builds the form over the live preferences. A
// hand-edited unknown or background-incompatible theme is warned about
// and reset to "auto" (P3: degrade with a warning, never block).
//
// Returns a pointer: the form binds directly to the mode's fields, so
// the values the user picks land in the same struct the caller and
// snapshot() read (a value-returning constructor would bind the form to
// a dead copy).
func NewSettingsMode(cfgPath string, cfg *config.Config, theme Theme, darkBg bool) *SettingsMode {
	prefs := cfg.Prefs()
	themeVal := prefs.Theme
	if themeVal == "" {
		themeVal = "auto"
	}
	var warn string
	if themeVal != "auto" {
		switch {
		case !paletteCompatible(themeVal, darkBg):
			if _, known := lookupPalette(themeVal); known {
				warn = fmt.Sprintf("%q does not match this terminal's background — showing auto", prefs.Theme)
			} else {
				warn = fmt.Sprintf("unknown theme %q — showing auto", prefs.Theme)
			}
			themeVal = "auto"
		}
	}

	sm := &SettingsMode{
		cfgPath:  cfgPath,
		cfg:      cfg,
		theme:    theme,
		darkBg:   darkBg,
		themeVal: themeVal,
		anim:     prefs.AnimationsEnabled(),
		warn:     warn,
	}

	opts := make([]huh.Option[string], 0, len(palettes)+1)
	opts = append(opts, huh.NewOption("auto (llamaman default)", "auto"))
	for _, p := range CompatiblePalettes(darkBg) {
		opts = append(opts, huh.NewOption(p.Display, p.ID))
	}
	sm.form = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("theme").
			Description("options match your terminal background").
			Options(opts...).
			Value(&sm.themeVal),
		huh.NewConfirm().
			Title("animations").
			Description("subtle transitional animations (dot pulse, badge breathing); off in Preferences too").
			Value(&sm.anim),
	)).WithTheme(configHuhTheme(theme))
	return sm
}

// Init starts the form.
func (s *SettingsMode) Init() tea.Cmd { return s.form.Init() }

// SetSize tracks terminal dimensions and sizes the form inputs.
func (s *SettingsMode) SetSize(w, h int) {
	s.width, s.height = w, h
	if s.form == nil {
		return
	}
	const popupFrame = 6 // border (2) + horizontal padding (4)
	fw := w - popupFrame
	if fw < 40 {
		fw = 40
	}
	fh := h - 8
	if fh < 6 {
		fh = 6
	}
	_, cmd := s.form.Update(tea.WindowSizeMsg{Width: fw, Height: fh})
	if cmd != nil {
		_ = cmd() // sizing commands are trivial; drain synchronously
	}
}

// Applied returns the preferences snapshot written on submit, or nil
// when the user cancelled or nothing changed. Root applies + saves it.
func (s *SettingsMode) Applied() *config.Preferences { return s.applied }

// Update forwards keys to the form and, on completion, snapshots the
// edited preferences (only non-default values, keeping the object
// minimal per the field-arrival contract) and returns to Main.
func (s *SettingsMode) Update(msg tea.Msg) (*SettingsMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" && s.form.State != huh.StateCompleted {
		return s, func() tea.Msg { return returnFromSettingsMsg{} }
	}
	next, cmd := s.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.form = f
	}
	if s.form.State == huh.StateCompleted && s.applied == nil {
		s.applied = s.snapshot()
		return s, func() tea.Msg { return returnFromSettingsMsg{} }
	}
	return s, cmd
}

// snapshot builds the preferences value to persist. Defaults are left
// unset so the object stays minimal (empty theme == "auto", nil
// animations == true).
func (s *SettingsMode) snapshot() *config.Preferences {
	prefs := s.cfg.Prefs()
	changed := false

	if s.themeVal != "auto" && s.themeVal != prefs.Theme {
		prefs.Theme = s.themeVal
		changed = true
	} else if s.themeVal == "auto" && prefs.Theme != "" {
		prefs.Theme = "" // reverting to auto removes the field
		changed = true
	}

	if s.anim != prefs.AnimationsEnabled() {
		anim := s.anim
		prefs.Animations = &anim
		changed = true
	}

	if !changed {
		return nil
	}
	return &prefs
}

// View renders the settings form in a bordered popup, with an optional
// warning banner when the stored theme was unknown/incompatible.
func (s SettingsMode) View() string {
	if s.form == nil {
		return ""
	}
	parts := []string{
		lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render("Settings"),
		"",
	}
	if s.warn != "" {
		parts = append(parts,
			lipgloss.NewStyle().Foreground(s.theme.StatusStart).Render("⚠ "+s.warn),
			"",
		)
	}
	parts = append(parts, s.form.View())
	body := lipgloss.JoinVertical(lipgloss.Left, parts...)
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(s.theme.Border).
		Padding(1, 2)
	return lipgloss.Place(max(s.width, 1), max(s.height, 1), lipgloss.Center, lipgloss.Center, box.Render(body))
}
