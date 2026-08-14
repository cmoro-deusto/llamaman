package tui

import (
	"encoding/json"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/cmdline"
	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/hf"
)

// pasteTestConfig is a small fixture with one local model — enough to
// exercise new-model, preset-only, picker, and collision paths.
func pasteTestConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Models: []config.Model{
			{Alias: "alpha", Location: "/m/alpha.gguf", Presets: []config.Preset{
				{Name: "default", Params: config.Params{{Key: "ngl", Value: json.Number("99")}}},
			}},
			{Alias: "beta", HF: "org/repo:Q4_K_M", Presets: []config.Preset{
				{Name: "default"},
			}},
		},
	}
}

// pasteMode builds a ConfigMode over the paste fixture with a small
// registry attached, so known flags type correctly during Parse.
func pasteMode() ConfigMode {
	c := NewConfigMode("/dev/null", pasteTestConfig(), DefaultTheme())
	c.SetRegistry(flags.Registry{
		"ctx-size": {Name: "ctx-size", Form: "--ctx-size", Kind: flags.KindNumeric, Placeholder: "N"},
		"ngl":      {Name: "ngl", Form: "-ngl", Kind: flags.KindNumeric, Placeholder: "N"},
		"temp":     {Name: "temp", Form: "--temp", Kind: flags.KindString, Placeholder: "FLOAT"},
	})
	return c
}

// runPasteCmd drives the paste box to completion: sets the staging text,
// applies the form, and returns the (possibly chained) cmd.
func runPasteCmd(c *ConfigMode, text string) tea.Cmd {
	c.formStaging = formStaging{pasteText: strPtr(text)}
	c.formKind = formPasteCmd
	cmd, _ := c.applyForm()
	return cmd
}

// confirmPaste accepts (or cancels) the confirm step.
func confirmPaste(c *ConfigMode, accept bool) {
	c.formStaging.confirm = strBoolPtr(accept)
	_, _ = c.applyForm()
}

func strBoolPtr(b bool) *bool { return &b }

func TestPasteNewModelLocal(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "llama-server -m /m/new.gguf -ngl 99 --ctx-size 8192")

	if c.paste == nil || c.pasteMode != pasteNew {
		t.Fatalf("paste state = mode %d, want pasteNew with a parsed result", c.pasteMode)
	}
	if got, want := c.pasteAlias, "new"; got != want {
		t.Errorf("derived alias = %q, want %q", got, want)
	}
	if c.formKind != formPasteConfirm {
		t.Fatalf("formKind = %d, want formPasteConfirm", c.formKind)
	}

	confirmPaste(&c, true)

	if got := len(c.work.Models); got != 3 {
		t.Fatalf("models len = %d, want 3", got)
	}
	m := c.work.Models[2]
	if m.Alias != "new" || m.Location != "/m/new.gguf" {
		t.Errorf("model = %+v", m)
	}
	if got := len(m.Presets); got != 1 {
		t.Fatalf("presets = %d, want 1", got)
	}
	p := m.Presets[0]
	if p.Name != "pasted" {
		t.Errorf("preset name = %q, want pasted", p.Name)
	}
	if _, ok := p.Params.Get("ngl"); !ok {
		t.Errorf("params missing ngl: %v", p.Params)
	}
	if _, ok := p.Params.Get("alias"); ok {
		t.Errorf("alias param must not be stored on a new model: %v", p.Params)
	}
	if c.modelIdx != 2 || c.presetIdx != 0 {
		t.Errorf("cursor = %d/%d, want 2/0", c.modelIdx, c.presetIdx)
	}
	if c.paste != nil {
		t.Error("paste staging not cleared after commit")
	}
}

func TestPasteNewModelHF(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "-hf acme/Widget:Q4_K_M --ctx-size 4096")

	if c.pasteMode != pasteNew {
		t.Fatalf("mode = %d, want pasteNew", c.pasteMode)
	}
	if got, want := c.pasteAlias, "Widget"; got != want {
		t.Errorf("alias = %q, want %q", got, want)
	}
	confirmPaste(&c, true)

	m := c.work.Models[2]
	if m.HF != "acme/Widget:Q4_K_M" {
		t.Errorf("HF = %q", m.HF)
	}
	if v, ok := m.Presets[0].Params.Get("ctx-size"); !ok || v != json.Number("4096") {
		t.Errorf("ctx-size = %v, %v", v, ok)
	}
}

func TestPastePresetOnlyExisting(t *testing.T) {
	c := pasteMode()

	// Same Location as "alpha" → preset-only path (pasteTarget 0).
	runPasteCmd(&c, "-m /m/alpha.gguf --temp 0.6")

	if c.pasteMode != pasteExisting || c.pasteModel != 0 {
		t.Fatalf("mode/model = %d/%d, want pasteExisting/0", c.pasteMode, c.pasteModel)
	}
	confirmPaste(&c, true)

	if got := len(c.work.Models); got != 2 {
		t.Fatalf("models len = %d, want 2 (no new model)", got)
	}
	alpha := c.work.Models[0]
	if got := len(alpha.Presets); got != 2 {
		t.Fatalf("alpha presets = %d, want 2", got)
	}
	p := alpha.Presets[1]
	if p.Name != "pasted" {
		t.Errorf("preset name = %q", p.Name)
	}
	if _, ok := p.Params.Get("temp"); !ok {
		t.Errorf("params missing temp: %v", p.Params)
	}
}

func TestPasteExistingHFWithDifferentQuantIsNew(t *testing.T) {
	c := pasteMode()

	// "beta" has org/repo:Q4_K_M; a different quant is a different model.
	runPasteCmd(&c, "-hf org/repo:Q8_0")

	if c.pasteMode != pasteNew {
		t.Fatalf("mode = %d, want pasteNew (quant differs)", c.pasteMode)
	}
}

func TestPasteNoModelPickerExisting(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "--ctx-size 4096")

	if c.formKind != formPastePickModel {
		t.Fatalf("formKind = %d, want formPastePickModel", c.formKind)
	}
	// Pick model 1 ("beta").
	c.formStaging = formStaging{choice: strPtr("1")}
	c.formKind = formPastePickModel
	cmd, _ := c.applyForm()
	_ = cmd

	if c.pasteMode != pastePickExisting || c.pasteModel != 1 {
		t.Fatalf("mode/model = %d/%d, want pastePickExisting/1", c.pasteMode, c.pasteModel)
	}
	confirmPaste(&c, true)

	beta := c.work.Models[1]
	if got := len(beta.Presets); got != 2 {
		t.Fatalf("beta presets = %d, want 2", got)
	}
	if _, ok := beta.Presets[1].Params.Get("ctx-size"); !ok {
		t.Errorf("params missing ctx-size")
	}
}

func TestPasteNoModelPickerCreateNew(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "--ctx-size 4096")

	// Choose "＋ create new model…" → the standard model form opens.
	c.formStaging = formStaging{choice: strPtr("new")}
	c.formKind = formPastePickModel
	if cmd, _ := c.applyForm(); cmd == nil {
		t.Fatal("expected the new-model form cmd")
	}
	if c.pasteMode != pastePickNew {
		t.Fatalf("mode = %d, want pastePickNew", c.pasteMode)
	}

	// Simulate completing the model form.
	c.formStaging = formStaging{alias: strPtr("brand-new"), source: strPtr(sourceLocal), location: strPtr("/m/new.gguf")}
	c.formKind = formNewModel
	cmd, _ := c.applyForm()
	_ = cmd

	if c.formKind != formPasteConfirm {
		t.Fatalf("formKind = %d, want formPasteConfirm after model creation", c.formKind)
	}
	if c.pasteModel != 2 {
		t.Fatalf("pasteModel = %d, want 2 (the new model)", c.pasteModel)
	}
	confirmPaste(&c, true)

	if got := len(c.work.Models); got != 3 {
		t.Fatalf("models = %d, want 3", got)
	}
	m := c.work.Models[2]
	if m.Alias != "brand-new" || len(m.Presets) != 1 {
		t.Errorf("new model = %+v", m)
	}
	if _, ok := m.Presets[0].Params.Get("ctx-size"); !ok {
		t.Errorf("preset missing ctx-size: %v", m.Presets[0].Params)
	}
}

func TestPasteParseErrorsBlock(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "llama-server -m") // missing value

	if c.errorModal == "" {
		t.Fatal("expected an error modal")
	}
	if !strings.Contains(c.errorModal, "missing value") {
		t.Errorf("errorModal = %q", c.errorModal)
	}
	if c.formKind == formPasteConfirm {
		t.Error("confirm must not open on parse errors")
	}
}

func TestPasteBareHFWithoutRunnerCommitsBare(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "-hf org/repo")
	if c.pasteMode != pasteNew {
		t.Fatalf("mode = %d, want pasteNew", c.pasteMode)
	}
	// No hfRunner attached → the bare id is committed directly (P3).
	confirmPaste(&c, true)

	if len(c.work.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(c.work.Models))
	}
	if m := c.work.Models[2]; m.HF != "org/repo" {
		t.Errorf("HF = %q, want bare org/repo", m.HF)
	}
	if c.pastePending {
		t.Error("pastePending must be cleared")
	}
}

func TestPasteQuantChainArms(t *testing.T) {
	c := pasteMode()
	c.SetHFCheckRunner(&stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M"}}})

	runPasteCmd(&c, "-hf org/repo")
	if c.pasteMode != pasteNew {
		t.Fatalf("mode = %d, want pasteNew", c.pasteMode)
	}
	c.formStaging.confirm = strBoolPtr(true)
	c.formKind = formPasteConfirm
	cmd, _ := c.applyForm()
	if cmd == nil {
		t.Fatal("expected the hf-check cmd")
	}
	if !c.pastePending || c.hfCheck == nil {
		t.Fatalf("quant chain not armed: pending=%v check=%v", c.pastePending, c.hfCheck)
	}
	if c.pasteHF == nil || *c.pasteHF != "org/repo" {
		t.Fatalf("pasteHF = %v", c.pasteHF)
	}
	if len(c.work.Models) != 2 {
		t.Errorf("model must not be committed while the chain is armed: %d models", len(c.work.Models))
	}
}

func TestPasteQuantChoiceCommits(t *testing.T) {
	c := pasteMode()

	// Armed chain state (as left after the chooser resolves).
	c.paste = &cmdline.Result{Source: cmdline.Source{HF: "acme/Widget"}}
	c.pasteMode = pasteNew
	c.pasteName = "pasted"
	c.pastePending = true
	c.pasteHFVal = "acme/Widget"
	c.pasteHF = &c.pasteHFVal
	c.hfQuantVal = "Q4_K_M"
	c.applyHFQuantChoice(true)

	if len(c.work.Models) != 3 {
		t.Fatalf("models = %d, want 3", len(c.work.Models))
	}
	if m := c.work.Models[2]; m.HF != "acme/Widget:Q4_K_M" {
		t.Errorf("HF = %q, want acme/Widget:Q4_K_M", m.HF)
	}
	if c.pastePending || c.paste != nil {
		t.Error("paste state must be cleared after the quant chain")
	}
}

// TestPasteQuantChoiceRematch guards the re-match: a bare -hf whose
// quant resolves to an existing model's exact id becomes a preset-only
// import instead of a duplicate model.
func TestPasteQuantChoiceRematch(t *testing.T) {
	c := pasteMode()

	// "beta" already has org/repo:Q4_K_M; the bare repo + Q4_K_M
	// chooser pick must merge into it.
	c.paste = &cmdline.Result{Source: cmdline.Source{HF: "org/repo"}}
	c.pasteMode = pasteNew
	c.pasteName = "pasted"
	c.pastePending = true
	c.pasteHFVal = "org/repo"
	c.pasteHF = &c.pasteHFVal
	c.hfQuantVal = "Q4_K_M"
	c.applyHFQuantChoice(true)

	if len(c.work.Models) != 2 {
		t.Fatalf("models = %d, want 2 (merged into beta)", len(c.work.Models))
	}
	beta := c.work.Models[1]
	if got := len(beta.Presets); got != 2 {
		t.Fatalf("beta presets = %d, want 2", got)
	}
	if beta.Presets[1].Name != "pasted" {
		t.Errorf("preset name = %q", beta.Presets[1].Name)
	}
}

func TestPasteAliasDerivation(t *testing.T) {
	c := pasteMode()

	// --alias wins over the basename.
	c.paste = &cmdline.Result{Source: cmdline.Source{Location: "/m/x.gguf", Alias: "custom"}}
	if got := c.derivePasteAlias(c.paste.Source); got != "custom" {
		t.Errorf("alias = %q, want custom", got)
	}

	// Basename with extension stripped.
	c.paste = &cmdline.Result{Source: cmdline.Source{Location: "/m/My.Model.gguf"}}
	if got := c.derivePasteAlias(c.paste.Source); got != "My.Model" {
		t.Errorf("alias = %q, want My.Model", got)
	}

	// HF repo name.
	c.paste = &cmdline.Result{Source: cmdline.Source{HF: "org/My-Repo:Q8_0"}}
	if got := c.derivePasteAlias(c.paste.Source); got != "My-Repo" {
		t.Errorf("alias = %q, want My-Repo", got)
	}

	// Collision with "alpha" → suffixed.
	c.paste = &cmdline.Result{Source: cmdline.Source{Location: "/m/alpha.gguf"}}
	if got := c.derivePasteAlias(c.paste.Source); got != "alpha-2" {
		t.Errorf("alias = %q, want alpha-2", got)
	}
}

// TestPasteThroughMessageLoop drives the paste box through the real
// Update path (updateForm → applyForm → chained form), guarding the
// contract that a chained form survives completion — dismissForm would
// destroy the freshly-installed confirm form (the formNewParamPickKey
// pattern).
func TestPasteThroughMessageLoop(t *testing.T) {
	cm := pasteMode()
	c := &cm

	// Models pane 'p' opens the paste box (drive = Update + drainCmds,
	// mirroring the tea loop).
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	if c.formKind != formPasteCmd || c.form == nil {
		t.Fatalf("after p: formKind=%d form=%v, want formPasteCmd with a live form", c.formKind, c.form)
	}

	// Type the command line and submit.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-m /m/new.gguf -ngl 99")})
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})

	if c.formKind != formPasteConfirm || c.form == nil {
		t.Fatalf("after submit: formKind=%d form=%v — the chained confirm form must survive", c.formKind, c.form)
	}
	if c.paste == nil || c.pasteMode != pasteNew {
		t.Fatalf("paste state: mode=%d paste=%v", c.pasteMode, c.paste)
	}
}

// TestPasteConfirmEscClearsStaging guards the esc path on the confirm
// form: the import must be fully abandoned (no stale paste state that a
// later quant chooser could resurrect).
func TestPasteConfirmEscClearsStaging(t *testing.T) {
	cm := pasteMode()
	c := &cm

	runPasteCmd(c, "-hf org/repo")
	if c.pasteMode != pasteNew {
		t.Fatalf("mode = %d, want pasteNew", c.pasteMode)
	}
	// esc on the confirm form goes through updateForm → dismissForm.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEsc})

	if c.paste != nil || c.pasteMode != pasteNone {
		t.Fatalf("paste staging must be cleared on esc: mode=%d paste=%v", c.pasteMode, c.paste)
	}
	if c.form != nil || c.formKind != formNone {
		t.Fatalf("form must be dismissed: kind=%d form=%v", c.formKind, c.form)
	}
}

// TestPasteQuantChainSurvivesDismiss guards the review-found regression:
// dismissing the completed confirm form (updateForm → dismissForm) must
// NOT destroy the armed quant chain — hfCheck is its only live
// reference, and paste staging is still needed for the commit.
func TestPasteQuantChainSurvivesDismiss(t *testing.T) {
	cm := pasteMode()
	c := &cm
	c.SetHFCheckRunner(&stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M"}}})

	runPasteCmd(c, "-hf org/repo")
	c.formStaging.confirm = strBoolPtr(true)
	c.formKind = formPasteConfirm
	if cmd, _ := c.applyForm(); cmd == nil {
		t.Fatal("expected the hf-check cmd")
	}
	if !c.pastePending || c.hfCheck == nil {
		t.Fatalf("chain not armed: pending=%v check=%v", c.pastePending, c.hfCheck)
	}

	c.dismissForm()
	if c.hfCheck == nil {
		t.Fatal("dismissForm must not destroy the armed quant chain")
	}
	if !c.pastePending || c.paste == nil {
		t.Fatal("paste staging must survive while the chain is pending")
	}
}

// TestPasteFullFlowThroughLoop drives the ENTIRE paste flow through the
// real message loop, including accepting the Confirm dialog with the
// affirmative key — guards the .Value(&confirm) binding (a missing
// accessor left the staging bool false and produced a spurious
// "paste canceled" after Add).
func TestPasteFullFlowThroughLoop(t *testing.T) {
	cm := pasteMode()
	c := &cm

	c = drive(t, c, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	// Multi-line paste with backslash continuations (shell style).
	text := "-hf acme/Widget:Q4_K_M \\\n    -ngl 99 \\\n    --ctx-size 8192"
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(text)})
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // submit the paste box

	if c.formKind != formPasteConfirm || c.form == nil {
		t.Fatalf("after paste submit: formKind=%d form=%v", c.formKind, c.form)
	}
	if c.pasteMode != pasteNew || c.pasteAlias != "Widget" {
		t.Fatalf("paste state: mode=%d alias=%q", c.pasteMode, c.pasteAlias)
	}

	// alias (pre-filled "Widget") → preset name (pre-filled "pasted") → Confirm.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	// Accept with the affirmative key — on the last field, huh's Accept
	// completes the form in the same keypress.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	if len(c.work.Models) != 3 {
		t.Fatalf("models = %d, want 3 (committed)", len(c.work.Models))
	}
	m := c.work.Models[2]
	if m.Alias != "Widget" || m.HF != "acme/Widget:Q4_K_M" {
		t.Errorf("model = %+v", m)
	}
	if len(m.Presets) != 1 || m.Presets[0].Name != "pasted" {
		t.Errorf("presets = %+v", m.Presets)
	}
	if _, ok := m.Presets[0].Params.Get("ngl"); !ok {
		t.Errorf("params missing ngl: %v", m.Presets[0].Params)
	}
	if c.flash == "paste canceled" {
		t.Error("flash = paste canceled — the confirm Add was not honored")
	}
	if c.paste != nil || c.formKind != formNone {
		t.Errorf("paste state not cleared after commit: kind=%d paste=%v", c.formKind, c.paste)
	}
}

func TestPasteConfirmCancel(t *testing.T) {
	c := pasteMode()

	runPasteCmd(&c, "-m /m/new.gguf")
	confirmPaste(&c, false)

	if c.paste != nil {
		t.Error("paste staging must be cleared on cancel")
	}
	if len(c.work.Models) != 2 {
		t.Errorf("models = %d, want unchanged 2", len(c.work.Models))
	}
}
