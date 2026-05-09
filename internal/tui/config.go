package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

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
	formDeletePreset
	formNewParamPickKey
	formNewParamPickValue
	formEditParam
	formDeleteParam
	formExitPrompt
)

// returnFromConfigMsg pops back to the previous view (main / selection).
type returnFromConfigMsg struct{}

// formStaging is the scratch area for the active form. huh writes user
// input into these pointers; applyForm reads them on submit.
type formStaging struct {
	bin, host, port    *string
	alias              *string
	source             *string // "local" | "hf"; for new/edit-model forms
	location, hf       *string // exactly one is populated based on source
	name, desc         *string
	paramKey, paramVal *string
	confirm            *bool
	choice             *string
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
	pendingKey  string // staged param key between PickKey and PickValue
	picker      *paramPicker // active when adding a new param

	saveErr error
	flash   string

	firstRunBanner bool // shown until user presses 'n' in Models pane
	helpOverlay    bool // ? toggles a centered help reference; any key dismisses

	width, height int
	theme         Theme
}

// NewConfigMode builds an editor over a config + its on-disk path.
func NewConfigMode(cfgPath string, original *config.Config) ConfigMode {
	return ConfigMode{
		cfgPath: cfgPath,
		saved:   cloneConfig(original),
		work:    cloneConfig(original),
		theme:   CurrentTheme(),
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
	if pm, ok := msg.(paramPickerDoneMsg); ok {
		return c.handlePickerDone(pm)
	}
	if c.picker != nil {
		next, cmd := c.picker.Update(msg)
		c.picker = next
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
}

// installForm wires a freshly constructed huh.Form into ConfigMode and
// drives a WindowSizeMsg through it so its inputs size to the available
// width. Without this, huh defaults to a tiny width and long values
// (e.g. model paths) scroll off the right edge invisibly.
//
// The form is rendered inside a centered, bordered, padded box; we
// subtract a generous frame allowance to keep the inputs comfortably
// inside the box.
func (c *ConfigMode) installForm(form *huh.Form, kind formKind) tea.Cmd {
	c.form = form
	c.formKind = kind
	cmds := []tea.Cmd{form.Init()}
	if c.width > 0 && c.height > 0 {
		const frame = 12 // border + padding + breathing room
		w := c.width - frame
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
		c.save()
		return c, nil
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
	case "up", "k":
		if c.modelIdx > 0 {
			c.modelIdx--
			c.presetIdx, c.paramIdx = 0, 0
		}
	case "down", "j":
		if c.modelIdx < len(c.work.Models)-1 {
			c.modelIdx++
			c.presetIdx, c.paramIdx = 0, 0
		}
	case "shift+up":
		if c.hasModel() && c.modelIdx > 0 {
			c.work.Models[c.modelIdx-1], c.work.Models[c.modelIdx] =
				c.work.Models[c.modelIdx], c.work.Models[c.modelIdx-1]
			c.modelIdx--
			c.flash = "moved up"
		}
	case "shift+down":
		if c.hasModel() && c.modelIdx < len(c.work.Models)-1 {
			c.work.Models[c.modelIdx+1], c.work.Models[c.modelIdx] =
				c.work.Models[c.modelIdx], c.work.Models[c.modelIdx+1]
			c.modelIdx++
			c.flash = "moved down"
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
	}
	return c, nil
}

func (c *ConfigMode) handlePresetsKey(k tea.KeyMsg) (*ConfigMode, tea.Cmd) {
	if !c.hasModel() {
		return c, nil
	}
	presets := c.work.Models[c.modelIdx].Presets
	switch k.String() {
	case "up", "k":
		if c.presetIdx > 0 {
			c.presetIdx--
			c.paramIdx = 0
		}
	case "down", "j":
		if c.presetIdx < len(presets)-1 {
			c.presetIdx++
			c.paramIdx = 0
		}
	case "shift+up":
		if c.hasPreset() && c.presetIdx > 0 {
			ps := c.work.Models[c.modelIdx].Presets
			ps[c.presetIdx-1], ps[c.presetIdx] = ps[c.presetIdx], ps[c.presetIdx-1]
			c.presetIdx--
			c.flash = "moved up"
		}
	case "shift+down":
		if c.hasPreset() && c.presetIdx < len(presets)-1 {
			ps := c.work.Models[c.modelIdx].Presets
			ps[c.presetIdx+1], ps[c.presetIdx] = ps[c.presetIdx], ps[c.presetIdx+1]
			c.presetIdx++
			c.flash = "moved down"
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
	case "up", "k":
		if c.paramIdx > 0 {
			c.paramIdx--
		}
	case "down", "j":
		if c.paramIdx < len(params)-1 {
			c.paramIdx++
		}
	case "shift+up":
		if len(params) > 0 && c.paramIdx > 0 {
			params[c.paramIdx-1], params[c.paramIdx] = params[c.paramIdx], params[c.paramIdx-1]
			c.paramIdx--
			c.flash = "moved up"
		}
	case "shift+down":
		if len(params) > 0 && c.paramIdx < len(params)-1 {
			params[c.paramIdx+1], params[c.paramIdx] = params[c.paramIdx], params[c.paramIdx+1]
			c.paramIdx++
			c.flash = "moved down"
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
	c.formStaging = formStaging{bin: &bin, host: &host, port: &port}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("llama-server binary").Value(&bin).Validate(nonEmpty("binary")),
		huh.NewInput().Title("host (IPv4 / [::IPv6] / hostname)").Value(&host).Validate(hostValidator),
		huh.NewInput().Title("port").Value(&port).Validate(numericRange(1, 65535)),
	)).WithTheme(huh.ThemeBase())
	return c.installForm(form, formGlobals)
}

func (c *ConfigMode) openNewModelForm() tea.Cmd {
	alias, location, hf := "", "", ""
	source := sourceLocal
	c.formStaging = formStaging{alias: &alias, source: &source, location: &location, hf: &hf}
	return c.installForm(buildModelForm(&alias, &source, &location, &hf), formNewModel)
}

func (c *ConfigMode) openEditModelForm() tea.Cmd {
	m := c.work.Models[c.modelIdx]
	alias, location, hf := m.Alias, m.Location, m.HF
	source := sourceLocal
	if m.IsHF() {
		source = sourceHF
	}
	c.formStaging = formStaging{alias: &alias, source: &source, location: &location, hf: &hf}
	return c.installForm(buildModelForm(&alias, &source, &location, &hf), formEditModel)
}

// buildModelForm assembles the alias + source + value form used by both
// new-model and edit-model flows. huh only supports per-Group hide
// functions (not per-Field), so the two value inputs live in their own
// hidden-by-default groups gated on the source select. huh advances
// from group 1 to whichever group 2/3 is currently visible on submit,
// then to the next non-hidden group, etc.
func buildModelForm(alias, source, location, hf *string) *huh.Form {
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
	g2Local := huh.NewGroup(
		huh.NewInput().
			Title("model location (path)").
			Description("expanded ~ and $VAR at load time").
			Value(location).
			CharLimit(2048).
			Validate(nonEmpty("location")),
	).WithHideFunc(func() bool { return *source != sourceLocal })
	g2HF := huh.NewGroup(
		huh.NewInput().
			Title("HF identifier").
			Description("org/model[:quant], e.g. Qwen/Qwen3-32B-GGUF:Q4_K_M").
			Value(hf).
			CharLimit(256).
			Validate(hfFormValidator),
	).WithHideFunc(func() bool { return *source != sourceHF })
	return huh.NewForm(g1, g2Local, g2HF).WithTheme(huh.ThemeBase())
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
	)).WithTheme(huh.ThemeBase())
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
	)).WithTheme(huh.ThemeBase())
	return c.installForm(form, formDeleteModel)
}

func (c *ConfigMode) openNewPresetForm() tea.Cmd {
	name, desc := "", ""
	c.formStaging = formStaging{name: &name, desc: &desc}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("preset name").Value(&name).Validate(nonEmpty("name")),
		huh.NewInput().Title("description").Value(&desc),
	)).WithTheme(huh.ThemeBase())
	return c.installForm(form, formNewPreset)
}

func (c *ConfigMode) openEditPresetForm() tea.Cmd {
	p := c.work.Models[c.modelIdx].Presets[c.presetIdx]
	name, desc := p.Name, p.Description
	c.formStaging = formStaging{name: &name, desc: &desc}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("preset name").Value(&name).Validate(nonEmpty("name")),
		huh.NewInput().Title("description").Value(&desc),
	)).WithTheme(huh.ThemeBase())
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
	)).WithTheme(huh.ThemeBase())
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
	)).WithTheme(huh.ThemeBase())
	return c.installForm(form, formDeletePreset)
}

func (c *ConfigMode) openDuplicatePresetForm() tea.Cmd {
	src := c.work.Models[c.modelIdx].Presets[c.presetIdx]
	name := src.Name + "-copy"
	c.formStaging = formStaging{name: &name}
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("new preset name").Value(&name).Validate(nonEmpty("name")),
	)).WithTheme(huh.ThemeBase())
	return c.installForm(form, formDuplicatePreset)
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
		)).WithTheme(huh.ThemeBase())
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
	form := huh.NewForm(huh.NewGroup(field)).WithTheme(huh.ThemeBase())
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
	)).WithTheme(huh.ThemeBase())
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
		c.flash = "globals updated"
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

func (c *ConfigMode) save() {
	issues := config.Validate(c.work)
	if issues.HasErrors() {
		c.flash = "save blocked: validation errors — fix and retry"
		c.saveErr = nil
		return
	}
	if err := config.Save(c.cfgPath, c.work); err != nil {
		c.saveErr = err
		c.flash = fmt.Sprintf("save failed: %v", err)
		return
	}
	c.saveErr = nil
	c.saved = cloneConfig(c.work)
	c.flash = "saved"
}

// ---- view ----

func (c *ConfigMode) View() string {
	if c.width == 0 {
		return ""
	}
	bg := c.renderPanes()
	if c.picker != nil {
		return overlayCenter(bg, c.picker.View(c.theme), c.width, c.height)
	}
	if c.form != nil {
		popup := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(c.theme.Accent).
			Padding(1, 2).
			Render(c.form.View())
		return overlayCenter(bg, popup, c.width, c.height)
	}
	if c.helpOverlay {
		return overlayCenter(bg, c.renderHelpOverlay(), c.width, c.height)
	}
	return bg
}

// renderPanes draws the three-pane editor without any overlay.
func (c *ConfigMode) renderPanes() string {
	headerStyle := lipgloss.NewStyle().Foreground(c.theme.Accent).Bold(true)
	header := headerStyle.Render("llamaman — configuration")
	if c.Modified() {
		header += lipgloss.NewStyle().Foreground(c.theme.StatusStart).Render("  ● modified")
	}
	if c.firstRunBanner {
		banner := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(c.theme.Accent).
			Padding(0, 2).
			Render("First-time setup — globals saved. Press n in the Models pane to add your first model.")
		header = lipgloss.JoinVertical(lipgloss.Left, header, banner)
	}

	paneW := (c.width - 4) / 3
	left := c.renderPane(FocusModels, "Models", paneW, c.renderModels())
	mid := c.renderPane(FocusPresets, "Presets", paneW, c.renderPresets())
	right := c.renderPane(FocusParams, "Params", c.width-2*paneW-2, c.renderParams())

	row := lipgloss.JoinHorizontal(lipgloss.Top, left, mid, right)
	footer := c.renderFooter()
	return lipgloss.JoinVertical(lipgloss.Left, header, row, footer)
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

func (c *ConfigMode) renderModels() string {
	if len(c.work.Models) == 0 {
		return lipgloss.NewStyle().Foreground(c.theme.Muted).Render("(none — n to add)")
	}
	var lines []string
	for i, m := range c.work.Models {
		marker := "  "
		if i == c.modelIdx {
			marker = "▶ "
		}
		lines = append(lines, marker+m.Alias)
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
		marker := "  "
		if i == c.presetIdx {
			marker = "▶ "
		}
		lines = append(lines, marker+p.Name)
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
			marker := "  "
			if i == c.paramIdx {
				marker = "▶ "
			}
			line := fmt.Sprintf("%s%-22s %s", marker, p.Key, paramValueAsString(p.Value))
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
// Closes #12: the previous one-line `e/n/d` hint hid `D` (now `c`)
// duplicate, the shift-arrow reorder, and `?` itself.
func (c *ConfigMode) renderFooter() string {
	flash := ""
	if c.flash != "" {
		col := c.theme.Subtle
		if strings.HasPrefix(c.flash, "save failed") || strings.HasPrefix(c.flash, "save blocked") {
			col = c.theme.StatusErr
		} else if strings.HasPrefix(c.flash, "saved") {
			col = c.theme.StatusReady
		}
		flash = lipgloss.NewStyle().Foreground(col).Render(c.flash)
	}
	paneLine := c.renderPaneHint()
	globalLine := lipgloss.NewStyle().Foreground(c.theme.Subtle).
		Render("↑↓ select · ⇧↑⇧↓ reorder · tab pane · g globals · s save · ? help · esc back")
	lines := []string{paneLine, globalLine}
	if flash != "" {
		lines = append([]string{flash}, lines...)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderPaneHint produces the per-pane CRUD line. Verbs that operate on
// the currently-focused row (e/c/d) are greyed out when the pane is
// empty, since pressing them is a no-op there.
func (c *ConfigMode) renderPaneHint() string {
	on := lipgloss.NewStyle().Foreground(c.theme.Subtle)
	off := lipgloss.NewStyle().Foreground(c.theme.Muted)
	label := on.Render
	tag := func(s string, available bool) string {
		if available {
			return on.Render(s)
		}
		return off.Render(s)
	}
	sep := on.Render(" · ")

	switch c.focus {
	case FocusModels:
		has := c.hasModel()
		return strings.Join([]string{
			label("[Models] "),
			tag("n new", true),
			sep,
			tag("e/⏎ edit", has),
			sep,
			tag("c clone", has),
			sep,
			tag("d delete", has),
		}, "")
	case FocusPresets:
		has := c.hasPreset()
		return strings.Join([]string{
			label("[Presets] "),
			tag("n new", c.hasModel()),
			sep,
			tag("e/⏎ edit", has),
			sep,
			tag("c clone", has),
			sep,
			tag("d delete", has),
		}, "")
	case FocusParams:
		hasParam := false
		if c.hasPreset() {
			hasParam = len(c.work.Models[c.modelIdx].Presets[c.presetIdx].Params) > 0
		}
		return strings.Join([]string{
			label("[Params] "),
			tag("n new", c.hasPreset()),
			sep,
			tag("e/⏎ edit", hasParam),
			sep,
			tag("d delete", hasParam),
		}, "")
	}
	return ""
}

// renderHelpOverlay is the manual surfaced by `?`: full key map plus the
// non-obvious nuances (param edit can't rename; clone is intentionally
// absent on Params; esc on a modified config prompts to save/discard).
func (c *ConfigMode) renderHelpOverlay() string {
	heading := lipgloss.NewStyle().Foreground(c.theme.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(c.theme.Muted)
	body := strings.Join([]string{
		heading.Render("NAVIGATION"),
		"  ↑/k  ↓/j        select item in focused pane",
		"  ⇧↑   ⇧↓         reorder (changes argv order on next launch)",
		"  tab / → / l     next pane",
		"  shift+tab / ← / h   previous pane",
		"",
		heading.Render("MODELS / PRESETS"),
		"  n new      e or ⏎ edit      c clone      d delete (confirms)",
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

func looksNumeric(s string) bool {
	if s == "" {
		return false
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return false
	}
	_, ok := v.(json.Number)
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

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
