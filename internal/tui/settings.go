package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
)

// SettingsMode edits exactly the top-level `preferences` config object
// (DESIGN §15.1, §16.1): theme, animations, log colors, and the models
// directory, saved through the standard atomic path on submit; Esc
// discards. Quick keys (`t`/`shift+t` in Main) are shortcuts that write
// the same object — Settings is not a second source of truth (P8).
type SettingsMode struct {
	cfgPath string
	cfg     *config.Config
	theme   Theme
	darkBg  bool
	version string

	form       *huh.Form
	themeVal   string
	anim       bool
	logColors  bool
	modelsDir  string
	logoEffect string
	dlConns    string // download connections as typed; "" = default

	// lastThemeVal tracks the select's live value so Update can detect
	// arrow-key changes and re-theme the chrome + preview immediately
	// (DESIGN §15.1: live preview while browsing).
	lastThemeVal string

	applied *config.Preferences // non-nil after a successful submit
	warn    string              // unknown/mismatched theme banner

	width, height int
}

// NewSettingsMode builds the form over the live preferences. Both
// variants of every family are offered (owner decision, DESIGN §15.1):
// the terminal background is a hint, not a filter. A hand-edited
// *unknown* theme is reset to "auto" with a warning (P3); a known but
// background-mismatched theme is kept and warned about, so the user can
// override explicitly.
//
// Returns a pointer: the form binds directly to the mode's fields, so
// the values the user picks land in the same struct the caller and
// snapshot() read (a value-returning constructor would bind the form to
// a dead copy).
func NewSettingsMode(cfgPath string, cfg *config.Config, theme Theme, darkBg bool, version string) *SettingsMode {
	prefs := cfg.Prefs()
	themeVal := prefs.Theme
	if themeVal == "" {
		themeVal = "auto"
	}
	var warn string
	switch {
	case themeVal != "auto" && !lookupKnown(themeVal):
		warn = fmt.Sprintf("unknown theme %q — showing auto", prefs.Theme)
		themeVal = "auto"
	case themeVal != "auto" && !paletteCompatible(themeVal, darkBg):
		warn = fmt.Sprintf("%s — applied anyway", mismatchWarning(themeVal, darkBg))
	}

	dlConns := ""
	if prefs.DownloadConnections != 0 {
		dlConns = strconv.Itoa(prefs.DownloadConnections)
	}
	sm := &SettingsMode{
		cfgPath:      cfgPath,
		cfg:          cfg,
		theme:        theme,
		darkBg:       darkBg,
		version:      version,
		themeVal:     themeVal,
		anim:         prefs.AnimationsEnabled(),
		logColors:    prefs.LogColorsEnabled(),
		modelsDir:    prefs.ModelsDir,
		logoEffect:   prefs.LogoEffectMode(),
		dlConns:      dlConns,
		lastThemeVal: themeVal,
		warn:         warn,
	}

	opts := make([]huh.Option[string], 0, len(palettes)+1)
	opts = append(opts, huh.NewOption("auto (llamaman default)", "auto"))
	for _, p := range cyclePalettes() {
		label := p.Display
		switch p.Background {
		case BackgroundAdaptive:
			label += " — dark or light by terminal"
		case BackgroundDark:
			label += " (dark)"
		case BackgroundLight:
			label += " (light)"
		}
		opts = append(opts, huh.NewOption(label, p.ID))
	}
	sm.form = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("theme").
			Description("both variants shown; pick whichever suits your terminal").
			Options(opts...).
			Value(&sm.themeVal),
		huh.NewConfirm().
			Title("animations").
			Description("subtle animations (dot pulse, badge breathing, wordmark highlight); off in Preferences too").
			Value(&sm.anim),
		huh.NewSelect[string]().
			Title("wordmark highlight").
			Description("specular highlight sweeping the llamaman logo on the main screen (needs animations on)").
			Options(
				huh.NewOption("once — sweep each time the main screen opens", config.LogoEffectOnce),
				huh.NewOption("loop — keep sweeping while the main screen is open", config.LogoEffectLoop),
			).
			Value(&sm.logoEffect),
		huh.NewConfirm().
			Title("log colors").
			Description("render-time line-kind coloring of the run-mode log (also toggled with `o` in run mode)").
			Value(&sm.logColors),
		huh.NewInput().
			Title("models directory").
			Description("llama.cpp cache root for HF models; leave empty for the default ($LLAMA_CACHE → ~/.cache/huggingface/hub), shared with llama-cli").
			Placeholder("e.g. ~/models or /opt/llama-cache").
			CharLimit(1024).
			Value(&sm.modelsDir),
		huh.NewInput().
			Title("download connections").
			Description(fmt.Sprintf("parallel connections per model download (1–%d); empty = default (%d)",
				hf.MaxConnections, hf.DefaultConnections)).
			Placeholder(strconv.Itoa(hf.DefaultConnections)).
			CharLimit(2).
			Validate(validateDownloadConns).
			Value(&sm.dlConns),
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
// minimal per the field-arrival contract) and returns to Main. While
// the user arrows through the theme select, the chrome and the preview
// pane re-theme live with the candidate palette.
func (s *SettingsMode) Update(msg tea.Msg) (*SettingsMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" && s.form.State != huh.StateCompleted {
		return s, func() tea.Msg { return returnFromSettingsMsg{} }
	}
	next, cmd := s.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.form = f
	}
	s.refreshPreview()
	if s.form.State == huh.StateCompleted && s.applied == nil {
		s.applied = s.snapshot()
		return s, func() tea.Msg { return returnFromSettingsMsg{} }
	}
	return s, cmd
}

// refreshPreview re-resolves the selected theme when the picker value
// changed, re-theming the Settings chrome and the form itself so the
// user sees the candidate palette live (DESIGN §15.1).
func (s *SettingsMode) refreshPreview() {
	if s.themeVal == s.lastThemeVal {
		return
	}
	s.lastThemeVal = s.themeVal
	t, _, _ := ResolveTheme(s.themeVal, s.darkBg)
	s.theme = t
	s.form.WithTheme(configHuhTheme(t))
}

// previewPane renders the actual Main landing screen with the candidate
// palette — a true live preview of what the theme will look like. It is
// a throwaway MainMode (no side effects, deterministic); skipped on
// short terminals where the popup would overflow.
func (s SettingsMode) previewPane() string {
	if s.height < 24 || s.cfg == nil {
		return ""
	}
	pm := NewMainMode(s.cfg, s.version, s.theme)
	pw := min(60, max(20, s.width-14))
	pm.SetSize(pw, min(14, s.height-16))
	return pm.View()
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

	if s.logColors != prefs.LogColorsEnabled() {
		logColors := s.logColors
		prefs.LogColors = &logColors
		changed = true
	}

	if s.logoEffect != prefs.LogoEffectMode() {
		prefs.LogoEffect = s.logoEffect
		if s.logoEffect == config.LogoEffectOnce {
			prefs.LogoEffect = "" // reverting to the default removes the field
		}
		changed = true
	}

	if s.modelsDir != prefs.ModelsDir {
		prefs.ModelsDir = s.modelsDir // empty == absent (omitempty)
		changed = true
	}

	dlConns := 0
	if raw := strings.TrimSpace(s.dlConns); raw != "" {
		dlConns, _ = strconv.Atoi(raw) // the form validated it
	}
	if dlConns == hf.DefaultConnections {
		dlConns = 0 // an explicit default stays absent (minimal object)
	}
	if dlConns != prefs.DownloadConnections {
		prefs.DownloadConnections = dlConns
		changed = true
	}

	if !changed {
		return nil
	}
	return &prefs
}

// validateDownloadConns accepts an empty value (the default) or an
// integer within the downloader's [1, MaxConnections] range.
func validateDownloadConns(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > hf.MaxConnections {
		return fmt.Errorf("1–%d, or empty for the default (%d)", hf.MaxConnections, hf.DefaultConnections)
	}
	return nil
}

// View renders the settings form in a bordered popup, with the detected
// terminal background and an optional warning banner when the stored
// theme was unknown or background-mismatched.
func (s SettingsMode) View() string {
	if s.form == nil {
		return ""
	}
	term := "light"
	if s.darkBg {
		term = "dark"
	}
	parts := []string{
		lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render("Settings"),
		"",
		lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(
			fmt.Sprintf("terminal background: %s — pick any palette; mismatched ones warn", term)),
		"",
	}
	if s.warn != "" {
		parts = append(parts,
			lipgloss.NewStyle().Foreground(s.theme.StatusStart).Render("⚠ "+s.warn),
			"",
		)
	}
	if preview := s.previewPane(); preview != "" {
		parts = append(parts,
			lipgloss.NewStyle().
				Foreground(s.theme.Muted).
				Render("preview (main screen with selected theme):"),
			preview,
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
