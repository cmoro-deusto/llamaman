package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"log/slog"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/modelsini"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// savedFlashTTL is how long the "● saved" indicator stays in the
// subtitle after a successful save. Long enough that a quick eye
// catches it, short enough that it doesn't read as stale state.
const savedFlashTTL = 5 * time.Second

// savedExpiredMsg is delivered by tea.Tick `savedFlashTTL` after a
// successful save. The generation field guards against race conditions
// when the user saves again within the TTL window: only the latest
// save's tick is allowed to clear the indicator.
type savedExpiredMsg struct{ gen int }

// ConfigFocus identifies which of the three panes is active.
type ConfigFocus int

const (
	FocusModels ConfigFocus = iota
	FocusPresets
	FocusParams
)

type formKind int

const (
	formNone formKind = iota
	formGlobals
	formNewModel
	formEditModel
	formDuplicateModel
	formDeleteModel
	formNewPreset
	formEditPreset
	formDuplicatePreset
	formCloneToModelPreset
	formDeletePreset
	formNewParamPickKey
	formNewParamPickValue
	formEditParam
	formDeleteParam
	formExitPrompt
	formExportIni
)

// returnFromConfigMsg pops back to the previous view (main / selection).
type returnFromConfigMsg struct{}

// formStaging is the scratch area for the active form. huh writes user
// input into these pointers; applyForm reads them on submit.
type formStaging struct {
	bin, host, port    *string
	modelsFiles        *string // one models-file path per line
	alias              *string
	source             *string // "local" | "hf"; for new/edit-model forms
	location, hf       *string // exactly one is populated based on source
	name, desc         *string
	paramKey, paramVal *string
	exportPath         *string
	confirm            *bool
	choice             *string
	targetIdx          *int // for clone-to-model preset form
}

const (
	sourceLocal = "local"
	sourceHF    = "hf"
)

// ConfigMode is the three-pane editor described in DESIGN.md §7.5.
type ConfigMode struct {
	cfgPath  string
	saved    *config.Config // last persisted snapshot (for diff)
	work     *config.Config // editable copy
	registry flags.Registry // optional, for fuzzy picker + type-aware values

	focus     ConfigFocus
	modelIdx  int
	presetIdx int
	paramIdx  int

	form        *huh.Form
	formKind    formKind
	formStaging formStaging
	pendingKey  string       // staged param key between PickKey and PickValue
	picker      *paramPicker // active when adding a new param
	modelPicker *modelPicker // picker overlay for the model form's location/hf inputs
	locField    *pickerInput // picker-assisted location input, for post-pick refresh
	hfField     *pickerInput // picker-assisted HF input, for post-pick refresh

	saveErr error
	flash   string

	firstRunBanner bool   // shown until user presses 'n' in Models pane
	helpOverlay    bool   // ? toggles a centered help reference; any key dismisses
	errorModal     string // when non-empty, a centered error modal is overlaid; any key dismisses
	savedGen       int    // bumped on every successful save; matched by the auto-expire tick

	width, height int
	theme         Theme
}

// NewConfigMode builds an editor over a config + its on-disk path.
// theme is the resolved palette (DESIGN §15.1).
func NewConfigMode(cfgPath string, original *config.Config, theme Theme) ConfigMode {
	return ConfigMode{
		cfgPath: cfgPath,
		saved:   cloneConfig(original),
		work:    cloneConfig(original),
		theme:   theme,
	}
}

// SetRegistry attaches a parsed flag registry. When set, the new-param
// flow uses a fuzzy picker over its keys, and value forms become type-
// aware (boolean toggle, numeric input, enum picker, text).
func (c *ConfigMode) SetRegistry(r flags.Registry) { c.registry = r }

// ShowFirstRunBanner displays the one-time prompt described in DESIGN.md
// §8 step 4. Dismissed on first 'n' in Models pane or on quit.
func (c *ConfigMode) ShowFirstRunBanner() { c.firstRunBanner = true }

// SetSize tracks terminal dimensions for layout. Propagates to the
// active param picker if any.
func (c *ConfigMode) SetSize(w, h int) {
	c.width, c.height = w, h
	if c.picker != nil {
		pw, ph := c.pickerSize()
		c.picker.SetSize(pw, ph)
	}
	if c.modelPicker != nil {
		pw, ph := c.pickerSize()
		c.modelPicker.SetSize(pw, ph)
	}
}

// Modified reports whether the working copy diverges from the last save.
func (c *ConfigMode) Modified() bool {
	a, _ := config.MarshalForDiff(c.work)
	b, _ := config.MarshalForDiff(c.saved)
	return string(a) != string(b)
}

// Saved returns the latest persisted snapshot. Root reads it after exit
// so subsequent run-mode/selection screens see the new config.
func (c *ConfigMode) Saved() *config.Config { return c.saved }

// Update routes keys when no form/picker is active, and forwards to the
// active overlay otherwise.
func (c *ConfigMode) Update(msg tea.Msg) (*ConfigMode, tea.Cmd) {
	// Auto-clear the "saved" subtitle indicator after savedFlashTTL.
	// The gen check prevents a stale tick from clearing the indicator
	// of a *subsequent* save that landed within the TTL window.
	if m, ok := msg.(savedExpiredMsg); ok {
		if m.gen == c.savedGen && c.flash == "saved" {
			c.flash = ""
		}
		return c, nil
	}
	// Error modal takes priority over every other input path: until the
	// user acknowledges the failure, no other key should mutate state
	// (otherwise typing through the modal could trigger destructive
	// actions whose feedback they never saw).
	if c.errorModal != "" {
		if _, ok := msg.(tea.KeyMsg); ok {
			c.errorModal = ""
		}
		return c, nil
	}
	if pm, ok := msg.(paramPickerDoneMsg); ok {
		return c.handlePickerDone(pm)
	}
	if c.picker != nil {
		next, cmd := c.picker.Update(msg)
		c.picker = next
		return c, cmd
	}
	// Model-form pickers: the open/done messages must be consumed on
	// every message (a form left un-updated mid-flow swallows its own
	// nextFieldMsg — §16.4 gotcha), and while the overlay is open the
	// form underneath is shielded from all input.
	if m, ok := msg.(openModelPickerMsg); ok {
		return c.openModelPicker(m.kind)
	}
	if m, ok := msg.(modelPickerDoneMsg); ok {
		return c.handleModelPickerDone(m)
	}
	if c.modelPicker != nil {
		next, cmd := c.modelPicker.Update(msg)
		c.modelPicker = next
		return c, cmd
	}
	if c.form != nil {
		return c.updateForm(msg)
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		if c.helpOverlay {
			c.helpOverlay = false
			return c, nil
		}
		return c.handleKey(k)
	}
	return c, nil
}

// handlePickerDone consumes the picker's selection. On cancel we close
// the picker and stay in the params pane; on selection we advance to
// the type-aware value form for the chosen key.
func (c *ConfigMode) handlePickerDone(msg paramPickerDoneMsg) (*ConfigMode, tea.Cmd) {
	c.picker = nil
	if msg.cancelled || msg.key == "" {
		return c, nil
	}
	c.pendingKey = msg.key
	return c, c.openValueFormFor(msg.key, "")
}

// openModelPicker builds and installs the picker overlay for the
// model form's local/HF input (DESIGN §16.5). It is reached from
// openModelPickerMsg, which only the pickerInput fields emit while a
// model form is active; anything else is ignored. Failures are
// non-blocking (P3): an unresolvable cache root, a scan error, or an
// empty cache simply leave the free-type input in place.
func (c *ConfigMode) openModelPicker(kind string) (*ConfigMode, tea.Cmd) {
	if c.formKind != formNewModel && c.formKind != formEditModel {
		return c, nil
	}
	var mp *modelPicker
	if kind == sourceLocal {
		cur := ""
		if c.formStaging.location != nil {
			cur = *c.formStaging.location
		}
		mp = newLocalPicker(pickerStartDir(c.work.Prefs().ModelsDir, cur, c.work.Models))
	} else {
		root, err := storage.CacheRoot(c.work.Prefs().ModelsDir)
		if err != nil {
			return c, nil
		}
		mp, err = newRepoPicker(root, nil)
		if err != nil || mp == nil {
			return c, nil
		}
	}
	// The HF repo list gets essentially the full screen width — long
	// org/repo ids and their quant lists need room to stay on one line
	// (owner feedback). The local filepicker only uses the height.
	pw, ph := c.pickerSize()
	if kind == sourceHF {
		pw = c.width - 6
		if pw < 40 {
			pw = 40
		}
	}
	mp.SetSize(pw, ph)
	c.modelPicker = mp
	return c, mp.Init()
}

// handleModelPickerDone consumes the model-form picker's result:
// cancelled (or the "type a new repo…" row, value "") leaves the field
// untouched; otherwise the chosen value is written into the matching
// staging pointer and the input is refreshed so the user sees it. The
// form is still on the same field afterwards — no field advance. The
// form's cached view is rebuilt so the pre-filled value renders
// immediately (pickerFormRefreshMsg; huh caches group views).
func (c *ConfigMode) handleModelPickerDone(msg modelPickerDoneMsg) (*ConfigMode, tea.Cmd) {
	c.modelPicker = nil
	changed := false
	if !msg.cancelled && msg.value != "" {
		switch msg.kind {
		case sourceLocal:
			if c.formStaging.location != nil {
				*c.formStaging.location = msg.value
				if c.locField != nil {
					c.locField.RefreshValue()
				}
				changed = true
			}
		case sourceHF:
			if c.formStaging.hf != nil {
				*c.formStaging.hf = msg.value
				if c.hfField != nil {
					c.hfField.RefreshValue()
				}
				changed = true
			}
		}
	}
	if changed && c.form != nil {
		_, cmd := c.form.Update(pickerFormRefreshMsg{})
		return c, cmd
	}
	return c, nil
}

func (c *ConfigMode) updateForm(msg tea.Msg) (*ConfigMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" && c.formKind != formExitPrompt {
		c.dismissForm()
		return c, nil
	}
	model, cmd := c.form.Update(msg)
	if f, ok := model.(*huh.Form); ok {
		c.form = f
	}
	if c.form != nil && c.form.State == huh.StateCompleted {
		chained, dismiss := c.applyForm()
		if dismiss {
			c.dismissForm()
		}
		if chained != nil {
			return c, chained
		}
	}
	return c, cmd
}

func (c *ConfigMode) dismissForm() {
	c.form = nil
	c.formKind = formNone
	c.formStaging = formStaging{}
	c.modelPicker = nil
	c.locField, c.hfField = nil, nil
}

// installForm wires a freshly constructed huh.Form into ConfigMode,
// applies our customized huh theme (so the form's help line picks up the
// same accent-bold key + subtle label styling used by the bottom hint
// rows), and drives a WindowSizeMsg through it so the inputs size to
// the column width assigned to the form.
//
// The form is rendered below the Presets pane (see renderPanes) at the
// width of one pane (`paneW`). The bordered popup wrapper consumes 2
// cols of border + 4 cols of horizontal padding, so the inner huh
// content is sized to `paneW - 6`.
func (c *ConfigMode) installForm(form *huh.Form, kind formKind) tea.Cmd {
	form.WithTheme(configHuhTheme(c.theme))
	c.form = form
	c.formKind = kind
	cmds := []tea.Cmd{form.Init()}
	if c.width > 0 && c.height > 0 {
		paneW := (c.width - 4) / 3
		const popupFrame = 6 // border (2) + horizontal padding (4)
		w := paneW - popupFrame
		if w < 20 {
			w = 20
		}
		h := c.height - 6
		if h < 5 {
			h = 5
		}
		_, cmd := form.Update(tea.WindowSizeMsg{Width: w, Height: h})
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

// configHuhTheme starts from huh.ThemeBase() and overrides only the
// help styles so the form's bottom shortcut row matches the two-tone
// styling used by main mode (`shortcut()`) and the config-mode footer
// (`paneShortcut`): accent-bold key + subtle label + subtle separator.
// All other huh styling stays at its base defaults.
func configHuhTheme(t Theme) *huh.Theme {
	th := huh.ThemeBase()
	keyStyle := lipgloss.NewStyle().Foreground(t.Accent).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(t.Subtle)
	sepStyle := lipgloss.NewStyle().Foreground(t.Subtle)
	th.Help.ShortKey = keyStyle
	th.Help.ShortDesc = descStyle
	th.Help.ShortSeparator = sepStyle
	th.Help.FullKey = keyStyle
	th.Help.FullDesc = descStyle
	th.Help.FullSeparator = sepStyle
	th.Help.Ellipsis = descStyle
	return th
}

func (c *ConfigMode) handleKey(k tea.KeyMsg) (*ConfigMode, tea.Cmd) {
	switch k.String() {
	case "esc":
		if c.Modified() {
			return c, c.openExitPrompt()
		}
		return c, func() tea.Msg { return returnFromConfigMsg{} }
	case "tab", "right", "l":
		c.cycleFocus(+1)
		return c, nil
	case "shift+tab", "left", "h":
		c.cycleFocus(-1)
		return c, nil
	case "g":
		return c, c.openGlobalsForm()
	case "s":
		return c, c.save()
	case "?":
		c.helpOverlay = true
		return c, nil
	}
	switch c.focus {
	case FocusModels:
		return c.handleModelsKey(k)
	case FocusPresets:
		return c.handlePresetsKey(k)
	case FocusParams:
		return c.handleParamsKey(k)
	}
	return c, nil
}

func (c *ConfigMode) cycleFocus(delta int) {
	v := int(c.focus) + delta
	if v < 0 {
		v = int(FocusParams)
	}
	if v > int(FocusParams) {
		v = int(FocusModels)
	}
	c.focus = ConfigFocus(v)
}

// ---- pane key handlers ----

func (c *ConfigMode) handleModelsKey(k tea.KeyMsg) (*ConfigMode, tea.Cmd) {
	switch k.String() {
	case "up":
		if c.modelIdx > 0 {
			c.modelIdx--
			c.presetIdx, c.paramIdx = 0, 0
		}
	case "down":
		if c.modelIdx < len(c.work.Models)-1 {
			c.modelIdx++
			c.presetIdx, c.paramIdx = 0, 0
		}
	case "shift+up":
		if c.hasModel() && c.modelIdx > 0 {
			c.work.Models[c.modelIdx-1], c.work.Models[c.modelIdx] =
				c.work.Models[c.modelIdx], c.work.Models[c.modelIdx-1]
			c.modelIdx--
		}
	case "shift+down":
		if c.hasModel() && c.modelIdx < len(c.work.Models)-1 {
			c.work.Models[c.modelIdx+1], c.work.Models[c.modelIdx] =
				c.work.Models[c.modelIdx], c.work.Models[c.modelIdx+1]
			c.modelIdx++
		}
	case "n":
		c.firstRunBanner = false
		return c, c.openNewModelForm()
	case "e", "enter":
		if c.hasModel() {
			return c, c.openEditModelForm()
		}
	case "c":
		if c.hasModel() {
			return c, c.openDuplicateModelForm()
		}
	case "d":
		if c.hasModel() {
			return c, c.openDeleteModelPrompt()
		}
	case "x":
		return c, c.openExportIniForm()
	}
	return c, nil
}

func (c *ConfigMode) handlePresetsKey(k tea.KeyMsg) (*ConfigMode, tea.Cmd) {
	if !c.hasModel() {
		return c, nil
	}
	presets := c.work.Models[c.modelIdx].Presets
	switch k.String() {
	case "up":
		if c.presetIdx > 0 {
			c.presetIdx--
			c.paramIdx = 0
		}
	case "down":
		if c.presetIdx < len(presets)-1 {
			c.presetIdx++
			c.paramIdx = 0
		}
	case "shift+up":
		if c.hasPreset() && c.presetIdx > 0 {
			ps := c.work.Models[c.modelIdx].Presets
			ps[c.presetIdx-1], ps[c.presetIdx] = ps[c.presetIdx], ps[c.presetIdx-1]
			c.presetIdx--
		}
	case "shift+down":
		if c.hasPreset() && c.presetIdx < len(presets)-1 {
			ps := c.work.Models[c.modelIdx].Presets
			ps[c.presetIdx+1], ps[c.presetIdx] = ps[c.presetIdx], ps[c.presetIdx+1]
			c.presetIdx++
		}
	case "n":
		return c, c.openNewPresetForm()
	case "e", "enter":
		if c.hasPreset() {
			return c, c.openEditPresetForm()
		}
	case "c":
		if c.hasPreset() {
			return c, c.openDuplicatePresetForm()
		}
	case "k":
		if c.hasPreset() {
			if len(c.work.Models) < 2 {
				c.flash = "no other model to clone to"
				return c, nil
			}
			return c, c.openClonePresetToModelForm()
		}
	case "d":
		if c.hasPreset() {
			return c, c.openDeletePresetPrompt()
		}
	}
	return c, nil
}

func (c *ConfigMode) handleParamsKey(k tea.KeyMsg) (*ConfigMode, tea.Cmd) {
	if !c.hasPreset() {
		return c, nil
	}
	params := c.work.Models[c.modelIdx].Presets[c.presetIdx].Params
	switch k.String() {
	case "up":
		if c.paramIdx > 0 {
			c.paramIdx--
		}
	case "down":
		if c.paramIdx < len(params)-1 {
			c.paramIdx++
		}
	case "shift+up":
		if len(params) > 0 && c.paramIdx > 0 {
			params[c.paramIdx-1], params[c.paramIdx] = params[c.paramIdx], params[c.paramIdx-1]
			c.paramIdx--
		}
	case "shift+down":
		if len(params) > 0 && c.paramIdx < len(params)-1 {
			params[c.paramIdx+1], params[c.paramIdx] = params[c.paramIdx], params[c.paramIdx+1]
			c.paramIdx++
		}
	case "n":
		return c, c.openNewParamForm()
	case "e", "enter":
		if len(params) > 0 {
			return c, c.openEditParamForm()
		}
	case "d":
		if len(params) > 0 {
			return c, c.openDeleteParamPrompt()
		}
	}
	return c, nil
}

// ---- form openers ----

func (c *ConfigMode) openGlobalsForm() tea.Cmd {
	bin := c.work.Globals.Bin
	host := c.work.Globals.Host
	port := strconv.Itoa(c.work.Globals.Port)
	files := strings.Join(c.work.Globals.ModelsFiles, "\n")
	c.formStaging = formStaging{bin: &bin, host: &host, port: &port, modelsFiles: &files}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("llama-server binary").Value(&bin).Validate(nonEmpty("binary")),
		huh.NewInput().Title("host (IPv4 / [::IPv6] / hostname)").Value(&host).Validate(hostValidator),
		huh.NewInput().Title("port").Value(&port).Validate(numericRange(1, 65535)),
		huh.NewText().Title("models files (my-models.ini — router sources, one per line)").
			Placeholder("(default) "+modelsini.DefaultModelsFilePath(c.cfgPath)).
			Value(&files),
	))
	return c.installForm(form, formGlobals)
}

// openExportIniForm prompts for an output path (pre-filled with the
// derived models.ini location) and exports the working config to it.
func (c *ConfigMode) openExportIniForm() tea.Cmd {
	path := modelsini.DefaultModelsFilePath(c.cfgPath)
	if path == "" {
		path = "my-models.ini"
	}
	c.formStaging = formStaging{exportPath: &path}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("export my-models.ini to").
			Value(&path).
			Validate(nonEmpty("export path")),
	))
	return c.installForm(form, formExportIni)
}

func (c *ConfigMode) openNewModelForm() tea.Cmd {
	alias, location, hf := "", "", ""
	source := sourceLocal
	c.formStaging = formStaging{alias: &alias, source: &source, location: &location, hf: &hf}
	form, locField, hfField := buildModelForm(&alias, &source, &location, &hf)
	c.locField, c.hfField = locField, hfField
	return c.installForm(form, formNewModel)
}

func (c *ConfigMode) openEditModelForm() tea.Cmd {
	m := c.work.Models[c.modelIdx]
	alias, location, hf := m.Alias, m.Location, m.HF
	source := sourceLocal
	if m.IsHF() {
		source = sourceHF
	}
	c.formStaging = formStaging{alias: &alias, source: &source, location: &location, hf: &hf}
	form, locField, hfField := buildModelForm(&alias, &source, &location, &hf)
	c.locField, c.hfField = locField, hfField
	return c.installForm(form, formEditModel)
}

// buildModelForm assembles the alias + source + value form used by both
// new-model and edit-model flows. huh only supports per-Group hide
// functions (not per-Field), so the two value inputs live in their own
// hidden-by-default groups gated on the source select. huh advances
// from group 1 to whichever group 2/3 is currently visible on submit,
// then to the next non-hidden group, etc. The two value inputs are
// pickerInput fields (DESIGN §16.5): free-type with a ctrl+o hotkey;
// the returned field references let ConfigMode refresh the rendered
// value after a picker pre-fill.
func buildModelForm(alias, source, location, hf *string) (*huh.Form, *pickerInput, *pickerInput) {
	// The builder chain runs on the raw *huh.Input *before* wrapping —
	// the promoted builder methods return *huh.Input and would unwrap
	// the pickerInput if chained after.
	locIn := huh.NewInput().
		Title("model location (path)").
		Description("expanded ~ and $VAR at load time · ctrl+o: browse .gguf").
		Value(location).
		CharLimit(2048).
		Validate(nonEmpty("location"))
	hfIn := huh.NewInput().
		Title("HF identifier").
		Description("org/model[:quant], e.g. Qwen/Qwen3-32B-GGUF:Q4_K_M · ctrl+o: cached repos").
		Value(hf).
		CharLimit(256).
		Validate(hfFormValidator)
	locField := wrapPickerInput(locIn, sourceLocal, location)
	hfField := wrapPickerInput(hfIn, sourceHF, hf)
	g1 := huh.NewGroup(
		huh.NewInput().Title("alias").Value(alias).Validate(nonEmpty("alias")),
		huh.NewSelect[string]().
			Title("source").
			Description("local file or Hugging Face repository").
			Options(
				huh.NewOption("local file (.gguf)", sourceLocal),
				huh.NewOption("Hugging Face (downloaded by llama-server)", sourceHF),
			).
			Value(source),
	)
	g2Local := huh.NewGroup(locField).WithHideFunc(func() bool { return *source != sourceLocal })
	g2HF := huh.NewGroup(hfField).WithHideFunc(func() bool { return *source != sourceHF })
	return huh.NewForm(g1, g2Local, g2HF), locField, hfField
}

// hfFormValidator combines the non-empty check with the format check
// used by config.Validate, so the user gets immediate feedback before
// save.
func hfFormValidator(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("hf identifier is required")
	}
	if !config.ValidHFIdentifier(s) {
		return fmt.Errorf("expected org/repo[:quant]")
	}
	return nil
}

func (c *ConfigMode) openDuplicateModelForm() tea.Cmd {
	src := c.work.Models[c.modelIdx]
	alias := src.Alias + "-copy"
	c.formStaging = formStaging{alias: &alias}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("new alias").Value(&alias).Validate(uniqueAliasValidator(c.work.Models)),
	))
	return c.installForm(form, formDuplicateModel)
}

// uniqueAliasValidator rejects empty input and any alias already used by
// an existing model. Used inline by the duplicate-model form so collision
// surfaces at submit time instead of at save-time validation.
func uniqueAliasValidator(existing []config.Model) func(string) error {
	return func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("alias is required")
		}
		for _, m := range existing {
			if m.Alias == s {
				return fmt.Errorf("alias %q already exists", s)
			}
		}
		return nil
	}
}

func (c *ConfigMode) openDeleteModelPrompt() tea.Cmd {
	confirm := false
	c.formStaging = formStaging{confirm: &confirm}
	m := c.work.Models[c.modelIdx]
	prompt := fmt.Sprintf("Delete model %q (%d preset%s)? Cannot be undone.",
		m.Alias, len(m.Presets), plural(len(m.Presets)))
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).Affirmative("Delete").Negative("Cancel").Value(&confirm),
	))
	return c.installForm(form, formDeleteModel)
}

func (c *ConfigMode) openNewPresetForm() tea.Cmd {
	name, desc := "", ""
	c.formStaging = formStaging{name: &name, desc: &desc}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("preset name").Value(&name).Validate(nonEmpty("name")),
		huh.NewInput().Title("description").Value(&desc),
	))
	return c.installForm(form, formNewPreset)
}

func (c *ConfigMode) openEditPresetForm() tea.Cmd {
	p := c.work.Models[c.modelIdx].Presets[c.presetIdx]
	name, desc := p.Name, p.Description
	c.formStaging = formStaging{name: &name, desc: &desc}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("preset name").Value(&name).Validate(nonEmpty("name")),
		huh.NewInput().Title("description").Value(&desc),
	))
	return c.installForm(form, formEditPreset)
}

// openDeleteParamPrompt mirrors the model/preset delete confirms so the
// behavior across all three panes is identical: `d` always pops a modal
// before any destructive change. Without this, Params silently deleted
// the focused row, which surprised first-time users.
func (c *ConfigMode) openDeleteParamPrompt() tea.Cmd {
	confirm := false
	c.formStaging = formStaging{confirm: &confirm}
	p := c.work.Models[c.modelIdx].Presets[c.presetIdx].Params[c.paramIdx]
	prompt := fmt.Sprintf("Delete param %q?", p.Key)
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).
			Affirmative("Delete").Negative("Cancel").
			Value(&confirm),
	))
	return c.installForm(form, formDeleteParam)
}

func (c *ConfigMode) openDeletePresetPrompt() tea.Cmd {
	confirm := false
	c.formStaging = formStaging{confirm: &confirm}
	p := c.work.Models[c.modelIdx].Presets[c.presetIdx]
	prompt := fmt.Sprintf("Delete preset %q?", p.Name)
	form := huh.NewForm(huh.NewGroup(
		huh.NewConfirm().Title(prompt).
			Affirmative("Delete").Negative("Cancel").
			Value(&confirm),
	))
	return c.installForm(form, formDeletePreset)
}

func (c *ConfigMode) openDuplicatePresetForm() tea.Cmd {
	src := c.work.Models[c.modelIdx].Presets[c.presetIdx]
	name := src.Name + "-copy"
	c.formStaging = formStaging{name: &name}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("new preset name").Value(&name).Validate(nonEmpty("name")),
	))
	return c.installForm(form, formDuplicatePreset)
}

// openClonePresetToModelForm opens the cross-model preset clone form:
// a target-model select (every model except the source) plus a new
// preset name (default `<src>-copy`) with a collision check that runs
// against the *currently selected* target's presets — so flipping the
// select between models re-evaluates the name on submit.
func (c *ConfigMode) openClonePresetToModelForm() tea.Cmd {
	src := c.work.Models[c.modelIdx].Presets[c.presetIdx]
	name := src.Name + "-copy"
	// Pick the first non-source model as the initial target. The select
	// excludes the source itself — that's the existing `c clone` action.
	target := 0
	if target == c.modelIdx {
		target = 1
	}
	c.formStaging = formStaging{name: &name, targetIdx: &target}

	opts := make([]huh.Option[int], 0, len(c.work.Models)-1)
	for i, m := range c.work.Models {
		if i == c.modelIdx {
			continue
		}
		opts = append(opts, huh.NewOption(m.Alias, i))
	}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[int]().
			Title("target model").
			Options(opts...).
			Value(&target),
		huh.NewInput().
			Title("new preset name").
			Value(&name).
			Validate(clonePresetNameValidator(c.work.Models, &target)),
	))
	return c.installForm(form, formCloneToModelPreset)
}

// clonePresetNameValidator rejects empty input and any name already used
// by a preset in the currently-selected target model. Closes over the
// target pointer so a Tab back to the Select and a different choice
// re-evaluates against the new target's presets at submit time.
func clonePresetNameValidator(models []config.Model, target *int) func(string) error {
	return func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return fmt.Errorf("name is required")
		}
		idx := 0
		if target != nil {
			idx = *target
		}
		if idx < 0 || idx >= len(models) {
			return nil
		}
		for _, p := range models[idx].Presets {
			if p.Name == s {
				return fmt.Errorf("preset %q already exists in %q", s, models[idx].Alias)
			}
		}
		return nil
	}
}

// openNewParamForm starts a two-step flow: pick key (registry picker
// with name + parsed help description, free-text fallback) → type-aware
// value form. Step 1 is rendered via the bubbles/list-based paramPicker
// so each row is highlightable and shows what the flag does.
func (c *ConfigMode) openNewParamForm() tea.Cmd {
	c.pendingKey = ""
	if len(c.registry) == 0 {
		// No registry: fall back to a free-text huh.Input. applyForm's
		// formNewParamPickKey branch advances to the value form.
		c.formStaging = formStaging{paramKey: &c.pendingKey}
		form := huh.NewForm(huh.NewGroup(
			huh.NewInput().
				Title("flag key (without leading dashes)").
				Value(&c.pendingKey).
				Validate(nonEmpty("key")),
		))
		return c.installForm(form, formNewParamPickKey)
	}
	c.picker = newParamPicker(c.registry)
	pw, ph := c.pickerSize()
	c.picker.SetSize(pw, ph)
	return nil
}

// pickerSize returns the inner dimensions of the picker box, matching
// the frame allowance used in installForm so overlays look consistent.
func (c *ConfigMode) pickerSize() (int, int) {
	const frame = 12
	w := c.width - frame
	if w < 30 {
		w = 30
	}
	h := c.height - 8
	if h < 6 {
		h = 6
	}
	return w, h
}

// openValueFormFor builds the value-input form for the given key.
// Branches on the registry's ValueKind: bool / numeric / enum / string.
// staging.paramVal is set to the initial value (existing or empty).
func (c *ConfigMode) openValueFormFor(key, initial string) tea.Cmd {
	val := initial
	c.formStaging = formStaging{paramKey: &key, paramVal: &val}

	fi, _ := c.registry.Lookup(key)
	title := fmt.Sprintf("value for %s", fi.Form)
	if fi.Form == "" {
		title = fmt.Sprintf("value for --%s", key)
	}

	var field huh.Field
	switch fi.Kind {
	case flags.KindBool:
		boolVal := val == "true"
		c.formStaging.confirm = &boolVal
		c.formStaging.paramVal = nil
		field = huh.NewConfirm().
			Title(title).
			Affirmative("true (emit flag)").
			Negative("false (omit flag)").
			Value(&boolVal)
	case flags.KindNumeric:
		field = huh.NewInput().
			Title(title + " (numeric)").
			Value(&val).
			Validate(numericValueValidator)
	case flags.KindEnum:
		opts := make([]huh.Option[string], 0, len(fi.Enum))
		for _, e := range fi.Enum {
			opts = append(opts, huh.NewOption(e, e))
		}
		if val == "" && len(opts) > 0 {
			val = opts[0].Value
		}
		field = huh.NewSelect[string]().
			Title(title).
			Options(opts...).
			Value(&val)
	default:
		field = huh.NewInput().
			Title(title).
			Value(&val)
	}
	form := huh.NewForm(huh.NewGroup(field))
	return c.installForm(form, formNewParamPickValue)
}

// openEditParamForm reuses openValueFormFor with the existing key+value.
// Editing the key itself is intentionally not part of this flow — to
// rename a flag the user deletes and re-adds, matching the design's
// "inline edit value" semantics (DESIGN.md §7.5).
func (c *ConfigMode) openEditParamForm() tea.Cmd {
	p := c.work.Models[c.modelIdx].Presets[c.presetIdx].Params[c.paramIdx]
	c.pendingKey = p.Key
	cmd := c.openValueFormFor(p.Key, paramValueAsString(p.Value))
	// openValueFormFor stamped the formKind as formNewParamPickValue;
	// override to formEditParam so applyForm dispatches to the edit
	// branch instead of the new-param branch.
	c.formKind = formEditParam
	return cmd
}

// kindLabel returns a short description for the registry picker.
func kindLabel(fi flags.FlagInfo) string {
	switch fi.Kind {
	case flags.KindBool:
		return "(bool)"
	case flags.KindNumeric:
		return "(numeric)"
	case flags.KindEnum:
		return "(enum: " + strings.Join(fi.Enum, ",") + ")"
	case flags.KindString:
		return "(string)"
	default:
		return ""
	}
}

func numericValueValidator(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return fmt.Errorf("value is required")
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return fmt.Errorf("not a number: %s", s)
	}
	if _, ok := v.(json.Number); !ok {
		return fmt.Errorf("not a number: %s", s)
	}
	return nil
}

func (c *ConfigMode) openExitPrompt() tea.Cmd {
	choice := "save"
	c.formStaging = formStaging{choice: &choice}
	form := huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("Unsaved changes").
			Options(
				huh.NewOption("Save and exit", "save"),
				huh.NewOption("Discard changes and exit", "discard"),
				huh.NewOption("Cancel", "cancel"),
			).
			Value(&choice),
	))
	return c.installForm(form, formExitPrompt)
}

// applyForm consumes the just-completed form. The bool return tells the
// caller whether the form should be torn down: false means we kicked off
// a chained form (e.g., new-param key-pick → value-pick) and the new
// form is already installed in c.form.
func (c *ConfigMode) applyForm() (tea.Cmd, bool) {
	switch c.formKind {
	case formGlobals:
		port, err := strconv.Atoi(deref(c.formStaging.port))
		if err != nil {
			c.flash = fmt.Sprintf("invalid port %q", deref(c.formStaging.port))
			return nil, true
		}
		c.work.Globals.Bin = deref(c.formStaging.bin)
		c.work.Globals.Host = deref(c.formStaging.host)
		c.work.Globals.Port = port
		c.work.Globals.ModelsFiles = parseLines(deref(c.formStaging.modelsFiles))
		c.flash = "globals updated"
	case formExportIni:
		path := deref(c.formStaging.exportPath)
		sections, warnings, err := modelsini.WriteTo(path, c.work)
		if err != nil {
			c.errorModal = fmt.Sprintf("Export failed:\n\n%v", err)
			c.flash = ""
			return nil, true
		}
		c.flash = fmt.Sprintf("exported %d sections to %s", sections, path)
		if len(warnings) > 0 {
			for _, w := range warnings {
				slog.Warn("export warning", "warn", w)
			}
		}
	case formNewModel:
		m := config.Model{Alias: deref(c.formStaging.alias)}
		applyModelSourceFromStaging(&m, c.formStaging)
		c.work.Models = append(c.work.Models, m)
		c.modelIdx = len(c.work.Models) - 1
		c.flash = "model added"
	case formEditModel:
		c.work.Models[c.modelIdx].Alias = deref(c.formStaging.alias)
		applyModelSourceFromStaging(&c.work.Models[c.modelIdx], c.formStaging)
		c.flash = "model updated"
	case formDuplicateModel:
		src := c.work.Models[c.modelIdx]
		presets := make([]config.Preset, len(src.Presets))
		for j, p := range src.Presets {
			pp := p
			pp.Params = append(config.Params(nil), p.Params...)
			presets[j] = pp
		}
		dup := config.Model{
			Alias:    deref(c.formStaging.alias),
			Location: src.Location,
			HF:       src.HF,
			Presets:  presets,
		}
		c.work.Models = append(c.work.Models, dup)
		c.modelIdx = len(c.work.Models) - 1
		c.presetIdx, c.paramIdx = 0, 0
		c.flash = "model duplicated"
	case formDeleteModel:
		if c.formStaging.confirm != nil && *c.formStaging.confirm {
			c.work.Models = append(c.work.Models[:c.modelIdx], c.work.Models[c.modelIdx+1:]...)
			if c.modelIdx >= len(c.work.Models) && c.modelIdx > 0 {
				c.modelIdx--
			}
			c.presetIdx, c.paramIdx = 0, 0
			c.flash = "model deleted"
		}
	case formNewPreset:
		c.work.Models[c.modelIdx].Presets = append(c.work.Models[c.modelIdx].Presets, config.Preset{
			Name:        deref(c.formStaging.name),
			Description: deref(c.formStaging.desc),
		})
		c.presetIdx = len(c.work.Models[c.modelIdx].Presets) - 1
		c.flash = "preset added"
	case formEditPreset:
		c.work.Models[c.modelIdx].Presets[c.presetIdx].Name = deref(c.formStaging.name)
		c.work.Models[c.modelIdx].Presets[c.presetIdx].Description = deref(c.formStaging.desc)
		c.flash = "preset updated"
	case formDuplicatePreset:
		src := c.work.Models[c.modelIdx].Presets[c.presetIdx]
		dup := config.Preset{
			Name:        deref(c.formStaging.name),
			Description: src.Description,
			Params:      append(config.Params(nil), src.Params...),
		}
		c.work.Models[c.modelIdx].Presets = append(c.work.Models[c.modelIdx].Presets, dup)
		c.presetIdx = len(c.work.Models[c.modelIdx].Presets) - 1
		c.flash = "preset duplicated"
	case formCloneToModelPreset:
		// Cursor stays on the source preset — the target model isn't
		// focused after clone. The flash names the target alias so the
		// user has confirmation that the right model received it.
		src := c.work.Models[c.modelIdx].Presets[c.presetIdx]
		target := 0
		if c.formStaging.targetIdx != nil {
			target = *c.formStaging.targetIdx
		}
		if target < 0 || target >= len(c.work.Models) || target == c.modelIdx {
			c.flash = "invalid target model"
			return nil, true
		}
		dup := config.Preset{
			Name:        deref(c.formStaging.name),
			Description: src.Description,
			Params:      append(config.Params(nil), src.Params...),
		}
		c.work.Models[target].Presets = append(c.work.Models[target].Presets, dup)
		c.flash = fmt.Sprintf("preset cloned to %q", c.work.Models[target].Alias)
	case formDeletePreset:
		if c.formStaging.confirm != nil && *c.formStaging.confirm {
			c.deletePresetNow()
		}
	case formNewParamPickKey:
		key := strings.TrimSpace(deref(c.formStaging.paramKey))
		if key == "" {
			c.flash = "no key chosen"
			return nil, true
		}
		c.pendingKey = key
		// Open value form (chained); leave c.form replaced.
		cmd := c.openValueFormFor(key, "")
		return cmd, false
	case formNewParamPickValue:
		val := c.applyParamValueFromStaging()
		c.work.Models[c.modelIdx].Presets[c.presetIdx].Params = append(
			c.work.Models[c.modelIdx].Presets[c.presetIdx].Params,
			config.Param{Key: c.pendingKey, Value: val},
		)
		c.paramIdx = len(c.work.Models[c.modelIdx].Presets[c.presetIdx].Params) - 1
		c.flash = "param added"
	case formEditParam:
		p := &c.work.Models[c.modelIdx].Presets[c.presetIdx].Params[c.paramIdx]
		p.Value = c.applyParamValueFromStaging()
		c.flash = "param updated"
	case formDeleteParam:
		if c.formStaging.confirm != nil && *c.formStaging.confirm {
			c.deleteParam()
		}
	case formExitPrompt:
		switch deref(c.formStaging.choice) {
		case "save":
			c.save()
			if c.saveErr == nil && !config.Issues(config.Validate(c.work)).HasErrors() {
				return returnFromConfigCmd, true
			}
		case "discard":
			return returnFromConfigCmd, true
		}
	}
	return nil, true
}

// returnFromConfigCmd is the Cmd that pops back to the previous view.
// Returned synchronously from applyForm's exit-prompt branches so the
// runtime dispatches the message in the same Update cycle the form
// completes — without it the exit was deferred until the next message
// arrived, leaving the user visually stuck in config mode.
var returnFromConfigCmd tea.Cmd = func() tea.Msg { return returnFromConfigMsg{} }

// applyModelSourceFromStaging mirrors the form's source select onto the
// Model's Location/HF fields, clearing whichever is unused so the
// schema invariant (exactly one) holds even after switching source on
// an existing model.
func applyModelSourceFromStaging(m *config.Model, s formStaging) {
	switch deref(s.source) {
	case sourceHF:
		m.Location = ""
		m.HF = strings.TrimSpace(deref(s.hf))
	default:
		m.HF = ""
		m.Location = deref(s.location)
	}
}

// applyParamValueFromStaging interprets the form's staged value according
// to which staging field was used (confirm for bool, paramVal for text/
// numeric/enum). Booleans are stored as bool; numbers as json.Number;
// other strings as string — matching params.go's value type discipline.
func (c *ConfigMode) applyParamValueFromStaging() any {
	if c.formStaging.confirm != nil {
		return *c.formStaging.confirm
	}
	return parseParamValue(deref(c.formStaging.paramVal))
}

// deletePresetNow performs the actual removal; called after the confirm
// modal is accepted.
func (c *ConfigMode) deletePresetNow() {
	c.work.Models[c.modelIdx].Presets = append(
		c.work.Models[c.modelIdx].Presets[:c.presetIdx],
		c.work.Models[c.modelIdx].Presets[c.presetIdx+1:]...,
	)
	if c.presetIdx >= len(c.work.Models[c.modelIdx].Presets) && c.presetIdx > 0 {
		c.presetIdx--
	}
	c.paramIdx = 0
	c.flash = "preset deleted"
}

// ---- direct mutators ----

func (c *ConfigMode) deleteParam() {
	params := c.work.Models[c.modelIdx].Presets[c.presetIdx].Params
	c.work.Models[c.modelIdx].Presets[c.presetIdx].Params = append(
		params[:c.paramIdx], params[c.paramIdx+1:]...,
	)
	pp := c.work.Models[c.modelIdx].Presets[c.presetIdx].Params
	if c.paramIdx >= len(pp) && c.paramIdx > 0 {
		c.paramIdx--
	}
	c.flash = "param deleted"
}

// save validates, writes, and on success sets the "saved" subtitle
// indicator plus returns a tea.Cmd that clears the indicator after
// `savedFlashTTL`. Failure paths surface in the error modal and return
// nil — there's nothing to expire when the indicator never appeared.
func (c *ConfigMode) save() tea.Cmd {
	issues := config.Validate(c.work)
	if issues.HasErrors() {
		var bullets []string
		for _, iss := range issues {
			if iss.Severity == config.Error {
				bullets = append(bullets, fmt.Sprintf("  • %s — %s", iss.Path, iss.Message))
			}
		}
		c.errorModal = "Cannot save — fix these validation errors:\n\n" +
			strings.Join(bullets, "\n")
		c.saveErr = nil
		c.flash = ""
		return nil
	}
	if err := config.Save(c.cfgPath, c.work); err != nil {
		c.saveErr = err
		c.errorModal = fmt.Sprintf("Save failed:\n\n%v", err)
		c.flash = ""
		return nil
	}
	// Derived artifact: keep <config-dir>/models.ini in sync so the
	// Router source default stays current. Never fatal — the config
	// save itself succeeded.
	derivedWarnings := 0
	if warnings, err := modelsini.WriteDerived(c.cfgPath, c.work); err != nil {
		slog.Warn("derived models.ini write failed", "err", err)
	} else {
		derivedWarnings = len(warnings)
		for _, w := range warnings {
			slog.Warn("export warning", "warn", w)
		}
	}
	c.saveErr = nil
	c.saved = cloneConfig(c.work)
	if derivedWarnings > 0 {
		c.flash = fmt.Sprintf("saved (%d export warnings)", derivedWarnings)
	} else {
		c.flash = "saved"
	}
	c.savedGen++
	gen := c.savedGen
	return tea.Tick(savedFlashTTL, func(time.Time) tea.Msg {
		return savedExpiredMsg{gen: gen}
	})
}

// ---- view ----

func (c *ConfigMode) View() string {
	if c.width == 0 {
		return ""
	}
	bg := c.renderPanes()
	// Error modal stacks above any other overlay so a failure surfaces
	// even if a form / picker / help overlay was open when it fired.
	if c.errorModal != "" {
		return overlayCenter(bg, c.renderErrorModal(), c.width, c.height)
	}
	if c.picker != nil {
		return overlayCenter(bg, c.picker.View(c.theme), c.width, c.height)
	}
	if c.modelPicker != nil {
		return overlayCenter(bg, c.modelPicker.View(c.theme), c.width, c.height)
	}
	// The form is no longer overlaid here — renderPanes() inlines it
	// below the Presets pane at the same column width, so the panes
	// stay visible above it. View() only handles the truly-modal
	// overlays (picker, help, error).
	if c.helpOverlay {
		return overlayCenter(bg, c.renderHelpOverlay(), c.width, c.height)
	}
	return bg
}

// renderPanes draws the three-pane editor without any overlay.
//
// Vertical layout: 8 fixed blank rows on top, then the wordmark + subtitle
// + panes block (horizontally centered above the panes row), then the
// footer pinned to the bottom of the screen with whatever vertical space
// is left in between. The fixed top padding plus a bottom-anchored footer
// gives a stable position across resizes — the panes don't drift, and the
// hint rows always sit on the last terminal lines like a status bar.
func (c *ConfigMode) renderPanes() string {
	wordmark := lipgloss.NewStyle().
		Foreground(c.theme.Accent).
		Render(strings.TrimRight(Wordmark, "\n"))

	header := lipgloss.NewStyle().Foreground(c.theme.Subtle).
		Render("llamaman — configuration")
	// Save state lives in the subtitle so the indicator doesn't jump
	// between the top and the bottom of the screen on save: "● modified"
	// (yellow) while dirty, "● saved" (green) right after a clean save.
	// Save errors stay in the footer flash because Modified() is still
	// true after a failed save, so the subtitle correctly keeps showing
	// "● modified" and the footer surfaces the detailed error message.
	if c.Modified() {
		header += lipgloss.NewStyle().Foreground(c.theme.StatusStart).Render("  ● modified")
	} else if c.flash == "saved" {
		header += lipgloss.NewStyle().Foreground(c.theme.StatusReady).Render("  ● saved")
	}
	if c.firstRunBanner {
		banner := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(c.theme.Accent).
			Padding(0, 2).
			Render("First-time setup — globals saved. Press n in the Models pane to add your first model.")
		header = lipgloss.JoinVertical(lipgloss.Center, header, banner)
	}

	paneW := (c.width - 4) / 3
	left := c.renderPane(FocusModels, "Models", paneW, c.renderModels())
	mid := c.renderPane(FocusPresets, "Presets", paneW, c.renderPresets())
	right := c.renderPane(FocusParams, "Params", c.width-2*paneW-2, c.renderParams())
	row := lipgloss.JoinHorizontal(lipgloss.Top, left, mid, right)

	const topPadding = 8
	topParts := make([]string, 0, topPadding+7)
	for i := 0; i < topPadding; i++ {
		topParts = append(topParts, "")
	}
	topParts = append(topParts, wordmark, "", header, "", row)

	// Edit form (when open) renders inline below the Presets pane at
	// the same column width. The padded line must match the panes
	// row width exactly (rowW, typically c.width - 2) — padding to
	// c.width instead would make JoinVertical(Center, …) shift the
	// narrower panes row right by 1 column to center it within the
	// wider form line.
	rowW := lipgloss.Width(row)
	if c.form != nil {
		popup := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(c.theme.Accent).
			Padding(1, 2).
			Render(c.form.View())
		topParts = append(topParts, "", padToColumn(popup, paneW, rowW))
	}
	top := lipgloss.JoinVertical(lipgloss.Center, topParts...)

	// Footer is rendered separately and pinned to the bottom of the
	// terminal via a calculated filler. Each footer line is centered
	// horizontally across the full width.
	// Footer width matches the panes-row width too, so the wordmark,
	// header, panes, form, and footer all share one canonical block
	// width and stay aligned.
	footer := lipgloss.NewStyle().Width(rowW).Align(lipgloss.Center).
		Render(c.renderFooter())

	gap := c.height - lipgloss.Height(top) - lipgloss.Height(footer)
	if gap < 1 {
		gap = 1
	}
	return top + strings.Repeat("\n", gap) + footer
}

func (c *ConfigMode) renderPane(focus ConfigFocus, title string, w int, body string) string {
	border := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(c.theme.Border).
		Padding(0, 1).
		Width(w - 2)
	if c.focus == focus {
		border = border.BorderForeground(c.theme.BorderFocus)
	}
	titleLine := lipgloss.NewStyle().Foreground(c.theme.Subtle).Render(title)
	return border.Render(titleLine + "\n" + body)
}

// padToColumn shifts a multi-line string so each line starts at column
// `leftCol` and the whole thing is padded with trailing spaces out to
// `totalW`. Used to anchor the inline form below the Presets pane: the
// surrounding JoinVertical(Center, …) would otherwise re-center the
// form's narrower lines across the full terminal width.
func padToColumn(s string, leftCol, totalW int) string {
	lines := strings.Split(s, "\n")
	leftPad := strings.Repeat(" ", leftCol)
	out := make([]string, len(lines))
	for i, line := range lines {
		w := lipgloss.Width(line)
		rightPad := totalW - leftCol - w
		if rightPad < 0 {
			rightPad = 0
		}
		out[i] = leftPad + line + strings.Repeat(" ", rightPad)
	}
	return strings.Join(out, "\n")
}

// reverseSelected wraps the row text in ANSI reverse-video SGR when the
// row is selected. Same literal sequence main mode's inlineDelegate uses
// (cmd: \x1b[7m … \x1b[0m), so selection styling is uniform across main,
// the three config panes, and the param-picker delegate.
func reverseSelected(row string, selected bool) string {
	if !selected {
		return row
	}
	return "\x1b[7m" + row + "\x1b[0m"
}

func (c *ConfigMode) renderModels() string {
	if len(c.work.Models) == 0 {
		return lipgloss.NewStyle().Foreground(c.theme.Muted).Render("(none — n to add)")
	}
	var lines []string
	for i, m := range c.work.Models {
		lines = append(lines, reverseSelected(m.Alias, i == c.modelIdx))
	}
	return strings.Join(lines, "\n")
}

func (c *ConfigMode) renderPresets() string {
	if !c.hasModel() {
		return ""
	}
	presets := c.work.Models[c.modelIdx].Presets
	if len(presets) == 0 {
		return lipgloss.NewStyle().Foreground(c.theme.Muted).Render("(none — n to add)")
	}
	var lines []string
	for i, p := range presets {
		lines = append(lines, reverseSelected(p.Name, i == c.presetIdx))
	}
	return strings.Join(lines, "\n")
}

func (c *ConfigMode) renderParams() string {
	if !c.hasPreset() {
		return ""
	}
	params := c.work.Models[c.modelIdx].Presets[c.presetIdx].Params

	var unknownFlags []string
	if len(c.registry) > 0 {
		for _, p := range params {
			if _, ok := c.registry.Lookup(p.Key); !ok {
				unknownFlags = append(unknownFlags, p.Key)
			}
		}
	}

	var lines []string
	if len(params) == 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(c.theme.Muted).Render("(no params — n to add)"))
	} else {
		for i, p := range params {
			// Reverse-video wraps the key/value text only — the
			// trailing yellow `(?)` warning marker keeps its own
			// color so its meaning still reads on a selected row.
			keyVal := reverseSelected(
				fmt.Sprintf("%-22s %s", p.Key, paramValueAsString(p.Value)),
				i == c.paramIdx,
			)
			line := keyVal
			if len(c.registry) > 0 {
				if _, ok := c.registry.Lookup(p.Key); !ok {
					line += lipgloss.NewStyle().Foreground(c.theme.StatusStart).Render("  (?)")
				}
			}
			lines = append(lines, line)
		}
	}
	if len(unknownFlags) > 0 {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Foreground(c.theme.StatusStart).
			Render("warn: unknown flag"+plural(len(unknownFlags))+": "+strings.Join(unknownFlags, ", ")))
	}
	return strings.Join(lines, "\n")
}

// renderFooter draws the two-line hint area: a pane-specific CRUD line
// (verbs greyed out when the focused pane has no rows to act on), then a
// global line for navigation + meta keys that work regardless of focus.
// Each token is rendered via the main-mode `shortcut()` helper so keys
// pop in accent-bold and labels fall back to subtle grey, matching
// main's two-tone shortcut row exactly. Closes #12: the previous
// one-line `e/n/d` hint hid `D` (now `c`) duplicate, the shift-arrow
// reorder, and `?` itself.
func (c *ConfigMode) renderFooter() string {
	flash := ""
	// Save state ("saved") lives in the subtitle, not here — see
	// renderPanes(). Save failures surface as a centered error modal,
	// not as flash text. Footer flash is reserved for non-save action
	// confirmations ("model added", "preset deleted", …).
	if c.flash != "" && c.flash != "saved" {
		flash = lipgloss.NewStyle().Foreground(c.theme.Subtle).Render(c.flash)
	}
	paneLine := c.renderPaneHint()
	sep := lipgloss.NewStyle().Foreground(c.theme.Subtle).Render(" · ")
	globalParts := []string{
		paneShortcut("↑↓ select", c.theme, true),
		paneShortcut("⇧↑⇧↓ reorder", c.theme, true),
		paneShortcut("tab pane", c.theme, true),
		paneShortcut("g globals", c.theme, true),
		paneShortcut("s save", c.theme, true),
		paneShortcut("? help", c.theme, true),
		paneShortcut("esc back", c.theme, true),
	}
	globalLine := strings.Join(globalParts, sep)
	lines := []string{paneLine, globalLine}
	if flash != "" {
		lines = append([]string{flash}, lines...)
	}
	return lipgloss.JoinVertical(lipgloss.Center, lines...)
}

// paneShortcut renders a "<key> <label>" token in main mode's two-tone
// style: accent-bold key + subtle label when available, muted overall
// when not. Splits on the first space, so multi-character keys like
// `⇧↑⇧↓` and `e/⏎` stay on the key side. Falls back to a single-token
// render when there's no space (caller passed only a key).
func paneShortcut(s string, t Theme, available bool) string {
	if !available {
		return lipgloss.NewStyle().Foreground(t.Muted).Render(s)
	}
	parts := strings.SplitN(s, " ", 2)
	if len(parts) < 2 {
		return lipgloss.NewStyle().Foreground(t.Accent).Bold(true).Render(s)
	}
	return shortcut(parts[0], parts[1], t)
}

// renderPaneHint produces the per-pane CRUD line. Verbs that operate on
// the currently-focused row (e/c/d) are greyed out when the pane is
// empty, since pressing them is a no-op there.
func (c *ConfigMode) renderPaneHint() string {
	subtle := lipgloss.NewStyle().Foreground(c.theme.Subtle)
	sep := subtle.Render(" · ")

	switch c.focus {
	case FocusModels:
		has := c.hasModel()
		return subtle.Render("[Models] ") + strings.Join([]string{
			paneShortcut("n new", c.theme, true),
			paneShortcut("e/⏎ edit", c.theme, has),
			paneShortcut("c clone", c.theme, has),
			paneShortcut("d delete", c.theme, has),
			paneShortcut("x export .ini", c.theme, true),
		}, sep)
	case FocusPresets:
		has := c.hasPreset()
		// `k clone-to` is grayed when there's only one model — pressing
		// it then is a no-op with a flash, and dimming the hint matches
		// the available/grayed pattern used by the other context-gated
		// shortcuts above.
		canCloneTo := has && len(c.work.Models) > 1
		return subtle.Render("[Presets] ") + strings.Join([]string{
			paneShortcut("n new", c.theme, c.hasModel()),
			paneShortcut("e/⏎ edit", c.theme, has),
			paneShortcut("c clone", c.theme, has),
			paneShortcut("k clone-to", c.theme, canCloneTo),
			paneShortcut("d delete", c.theme, has),
		}, sep)
	case FocusParams:
		hasParam := false
		if c.hasPreset() {
			hasParam = len(c.work.Models[c.modelIdx].Presets[c.presetIdx].Params) > 0
		}
		return subtle.Render("[Params] ") + strings.Join([]string{
			paneShortcut("n new", c.theme, c.hasPreset()),
			paneShortcut("e/⏎ edit", c.theme, hasParam),
			paneShortcut("d delete", c.theme, hasParam),
		}, sep)
	}
	return ""
}

// renderErrorModal draws the centered error popup with a focused
// [Dismiss] button. Any key dismisses; the button styling (reverse
// video) signals that hitting Enter / Space / Esc — anything, really —
// closes the modal. Border + title use StatusErr so the failure mode
// reads at a glance.
func (c *ConfigMode) renderErrorModal() string {
	title := lipgloss.NewStyle().
		Foreground(c.theme.StatusErr).
		Bold(true).
		Render("⚠  Error")
	msg := lipgloss.NewStyle().Foreground(c.theme.Subtle).Render(c.errorModal)
	button := lipgloss.NewStyle().Reverse(true).Padding(0, 2).Render(" Dismiss ")
	hint := lipgloss.NewStyle().Foreground(c.theme.Muted).Render("(any key)")
	body := strings.Join([]string{
		title,
		"",
		msg,
		"",
		button + "  " + hint,
	}, "\n")
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(c.theme.StatusErr).
		Padding(1, 2).
		Render(body)
}

// renderHelpOverlay is the manual surfaced by `?`: full key map plus the
// non-obvious nuances (param edit can't rename; clone is intentionally
// absent on Params; esc on a modified config prompts to save/discard).
func (c *ConfigMode) renderHelpOverlay() string {
	heading := lipgloss.NewStyle().Foreground(c.theme.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(c.theme.Muted)
	body := strings.Join([]string{
		heading.Render("NAVIGATION"),
		"  ↑    ↓          select item in focused pane",
		"  ⇧↑   ⇧↓         reorder (changes argv order on next launch)",
		"  tab / → / l     next pane",
		"  shift+tab / ← / h   previous pane",
		"",
		heading.Render("MODELS / PRESETS"),
		"  n new      e or ⏎ edit      c clone      d delete (confirms)      x export to .ini",
		muted.Render("  Presets only:  k clone-to (copy preset to another model)."),
		"",
		heading.Render("PARAMS"),
		"  n new      e or ⏎ edit value      d delete (confirms)",
		muted.Render("  Note: editing a param can't rename it — delete and re-add to change the key."),
		muted.Render("  Note: clone is intentionally absent — two flags with the same key produce invalid argv."),
		"",
		heading.Render("GLOBAL"),
		"  g globals      s save      esc back (prompts on unsaved changes)",
		"",
		muted.Render("(press any key to dismiss)"),
	}, "\n")
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(c.theme.Accent).
		Padding(1, 2).
		Render(body)
}

// ---- helpers ----

func (c *ConfigMode) hasModel() bool {
	return c.modelIdx >= 0 && c.modelIdx < len(c.work.Models)
}

func (c *ConfigMode) hasPreset() bool {
	return c.hasModel() && c.presetIdx >= 0 && c.presetIdx < len(c.work.Models[c.modelIdx].Presets)
}

func cloneConfig(in *config.Config) *config.Config {
	if in == nil {
		return nil
	}
	out := *in
	out.Models = make([]config.Model, len(in.Models))
	for i, m := range in.Models {
		mm := m
		mm.Presets = make([]config.Preset, len(m.Presets))
		for j, p := range m.Presets {
			pp := p
			pp.Params = append(config.Params(nil), p.Params...)
			mm.Presets[j] = pp
		}
		out.Models[i] = mm
	}
	return &out
}

func paramValueAsString(v any) string {
	switch x := v.(type) {
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		return x.String()
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// parseParamValue applies cheap heuristics to convert a typed value into
// the right runtime type: "true"/"false" → bool, numeric → json.Number,
// otherwise string. Phase 7c can swap this for a type-aware picker driven
// by the parsed --help registry.
func parseParamValue(s string) any {
	s = strings.TrimSpace(s)
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	if looksNumeric(s) {
		return json.Number(s)
	}
	return s
}

// looksNumeric reports whether s is a single, complete JSON number literal.
// json.Unmarshal (unlike Decoder.Decode) requires the entire input to be one
// valid JSON value, so trailing garbage like "10.0.0.30:50052" — which a
// Decoder would happily accept by consuming only the "10.0" prefix — is
// correctly rejected here.
func looksNumeric(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return false
	}
	_, ok := v.(float64)
	return ok
}

func nonEmpty(field string) func(string) error {
	return func(v string) error {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("%s is required", field)
		}
		return nil
	}
}

// hostValidator runs on form blur (huh's Validate is invoked as you type
// + on submit). Accepts IPv4 / [::IPv6] / hostname per DESIGN.md §7.5.
func hostValidator(s string) error {
	if !config.ValidHost(strings.TrimSpace(s)) {
		return fmt.Errorf("expected IPv4, [::IPv6], or hostname")
	}
	return nil
}

func numericRange(lo, hi int) func(string) error {
	return func(v string) error {
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return fmt.Errorf("expected an integer")
		}
		if n < lo || n > hi {
			return fmt.Errorf("must be between %d and %d", lo, hi)
		}
		return nil
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// parseLines splits a multi-line form value into trimmed, non-empty
// entries — used for globals.models-files (one path per line).
func parseLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
