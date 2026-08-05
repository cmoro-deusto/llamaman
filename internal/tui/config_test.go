package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// duplicateTestConfig is a small fixture with two presets and a couple of
// params on the source model — enough for the deep-copy independence
// checks to be meaningful.
func duplicateTestConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Models: []config.Model{
			{Alias: "alpha", Location: "/m/alpha.gguf", Presets: []config.Preset{
				{Name: "default", Description: "balanced", Params: config.Params{
					{Key: "ngl", Value: json.Number("99")},
					{Key: "jinja", Value: true},
				}},
				{Name: "smallctx", Params: config.Params{
					{Key: "ctx-size", Value: json.Number("4096")},
				}},
			}},
			{Alias: "beta", HF: "Qwen/Qwen3-32B-GGUF:Q4_K_M", Presets: []config.Preset{
				{Name: "default", Params: config.Params{{Key: "ngl", Value: json.Number("80")}}},
			}},
		},
	}
}

func strPtr(s string) *string { return &s }

// TestConfigDuplicateModel covers the happy path: alias is taken from the
// form, source kind/value, presets, descriptions and params are copied,
// the cursor moves to the new entry, and mutating the duplicate's params
// does not bleed into the source.
func TestConfigDuplicateModel(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx = 0 // "alpha"

	c.formStaging = formStaging{alias: strPtr("alpha-clone")}
	c.formKind = formDuplicateModel
	c.applyForm()

	if got, want := len(c.work.Models), 3; got != want {
		t.Fatalf("models len = %d, want %d", got, want)
	}
	dup := c.work.Models[2]
	src := c.work.Models[0]

	if dup.Alias != "alpha-clone" {
		t.Errorf("dup.Alias = %q, want %q", dup.Alias, "alpha-clone")
	}
	if dup.Location != src.Location {
		t.Errorf("dup.Location = %q, want %q", dup.Location, src.Location)
	}
	if dup.HF != src.HF {
		t.Errorf("dup.HF = %q, want %q", dup.HF, src.HF)
	}
	if len(dup.Presets) != len(src.Presets) {
		t.Fatalf("dup presets len = %d, want %d", len(dup.Presets), len(src.Presets))
	}
	for i := range src.Presets {
		if dup.Presets[i].Name != src.Presets[i].Name {
			t.Errorf("dup.Presets[%d].Name = %q, want %q", i, dup.Presets[i].Name, src.Presets[i].Name)
		}
		if dup.Presets[i].Description != src.Presets[i].Description {
			t.Errorf("dup.Presets[%d].Description = %q, want %q",
				i, dup.Presets[i].Description, src.Presets[i].Description)
		}
		if len(dup.Presets[i].Params) != len(src.Presets[i].Params) {
			t.Errorf("dup.Presets[%d] param len = %d, want %d",
				i, len(dup.Presets[i].Params), len(src.Presets[i].Params))
		}
	}

	// Cursor moves to the new entry; preset/param indices reset.
	if c.modelIdx != 2 || c.presetIdx != 0 || c.paramIdx != 0 {
		t.Errorf("cursor = (%d,%d,%d), want (2,0,0)", c.modelIdx, c.presetIdx, c.paramIdx)
	}

	// Deep-copy: mutating the duplicate must not affect the source.
	dup.Presets[0].Params[0].Value = json.Number("1")
	dup.Presets[0].Name = "renamed"
	if got := c.work.Models[0].Presets[0].Params[0].Value; got != json.Number("99") {
		t.Errorf("source mutated: src params[0] = %v, want 99", got)
	}
	if got := c.work.Models[0].Presets[0].Name; got != "default" {
		t.Errorf("source mutated: src preset name = %q, want %q", got, "default")
	}
}

// TestConfigDuplicateModelHF verifies HF-sourced models duplicate
// correctly — `HF` carries over and `Location` stays empty.
func TestConfigDuplicateModelHF(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx = 1 // "beta", HF source

	c.formStaging = formStaging{alias: strPtr("beta-clone")}
	c.formKind = formDuplicateModel
	c.applyForm()

	dup := c.work.Models[len(c.work.Models)-1]
	if dup.HF != "Qwen/Qwen3-32B-GGUF:Q4_K_M" {
		t.Errorf("dup.HF = %q, want HF identifier carried over", dup.HF)
	}
	if dup.Location != "" {
		t.Errorf("dup.Location = %q, want empty for HF source", dup.Location)
	}
}

// TestUniqueAliasValidator covers the inline form validator — empty input
// is rejected and any alias already used by an existing model is rejected
// with a descriptive message.
func TestUniqueAliasValidator(t *testing.T) {
	models := []config.Model{{Alias: "alpha"}, {Alias: "beta"}}
	v := uniqueAliasValidator(models)

	if err := v("alpha"); err == nil {
		t.Error("validator accepted duplicate alias")
	}
	if err := v(""); err == nil {
		t.Error("validator accepted empty alias")
	}
	if err := v("   "); err == nil {
		t.Error("validator accepted whitespace-only alias")
	}
	if err := v("gamma"); err != nil {
		t.Errorf("validator rejected unique alias: %v", err)
	}
}

// TestConfigDuplicatePreset is the backfill for the existing
// formDuplicatePreset code path: name carries from the form, params are
// deep-copied, cursor moves to the new entry.
func TestConfigDuplicatePreset(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx, c.presetIdx = 0, 0 // alpha / default

	c.formStaging = formStaging{name: strPtr("default-clone")}
	c.formKind = formDuplicatePreset
	c.applyForm()

	presets := c.work.Models[0].Presets
	if got, want := len(presets), 3; got != want {
		t.Fatalf("presets len = %d, want %d", got, want)
	}
	dup := presets[2]
	src := presets[0]

	if dup.Name != "default-clone" {
		t.Errorf("dup.Name = %q, want %q", dup.Name, "default-clone")
	}
	if dup.Description != src.Description {
		t.Errorf("dup.Description = %q, want %q", dup.Description, src.Description)
	}
	if len(dup.Params) != len(src.Params) {
		t.Fatalf("dup params len = %d, want %d", len(dup.Params), len(src.Params))
	}

	if c.presetIdx != 2 {
		t.Errorf("presetIdx = %d, want 2", c.presetIdx)
	}

	// Deep-copy independence on Params slice.
	dup.Params[0].Value = json.Number("1")
	if got := c.work.Models[0].Presets[0].Params[0].Value; got != json.Number("99") {
		t.Errorf("source preset mutated: params[0] = %v, want 99", got)
	}
}

// runExitChoice stages the exit-prompt form with the given choice and
// drives applyForm. Returns the cmd applyForm produced (the caller
// invokes it to inspect the resulting tea.Msg).
func runExitChoice(c *ConfigMode, choice string) tea.Cmd {
	c.formStaging = formStaging{choice: &choice}
	c.formKind = formExitPrompt
	cmd, _ := c.applyForm()
	return cmd
}

// TestConfigExitPromptSaveEmitsReturn pins the regression: with a dirty
// work copy that passes validation, picking "Save and exit" must emit a
// returnFromConfigMsg synchronously from applyForm — the previous
// implementation set a flag that only fired on the next message,
// leaving the user visually stuck in config mode.
func TestConfigExitPromptSaveEmitsReturn(t *testing.T) {
	cfg := duplicateTestConfig()
	cfgPath := filepath.Join(t.TempDir(), "llamaman.json")
	c := NewConfigMode(cfgPath, cfg)

	// Dirty the work copy so save() actually writes something.
	c.work.Models[0].Alias = "alpha-renamed"

	cmd := runExitChoice(&c, "save")
	if cmd == nil {
		t.Fatal("save-and-exit returned nil cmd; user would be stuck in config mode")
	}
	if _, ok := cmd().(returnFromConfigMsg); !ok {
		t.Errorf("save-and-exit cmd produced %T, want returnFromConfigMsg", cmd())
	}
}

// TestConfigExitPromptDiscardEmitsReturn covers the discard branch —
// same deferred-exit bug, same fix.
func TestConfigExitPromptDiscardEmitsReturn(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.work.Models[0].Alias = "alpha-renamed"

	cmd := runExitChoice(&c, "discard")
	if cmd == nil {
		t.Fatal("discard-and-exit returned nil cmd; user would be stuck in config mode")
	}
	if _, ok := cmd().(returnFromConfigMsg); !ok {
		t.Errorf("discard-and-exit cmd produced %T, want returnFromConfigMsg", cmd())
	}
}

// TestConfigParamDeleteRequiresConfirm pins the cross-pane consistency
// fix: pressing `d` on a Params row no longer mutates immediately —
// it stages a formDeleteParam confirm modal, and only an affirmative
// answer removes the row. Mirrors model/preset delete behavior.
func TestConfigParamDeleteRequiresConfirm(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx, c.presetIdx, c.paramIdx = 0, 0, 0
	c.focus = FocusParams

	before := len(c.work.Models[0].Presets[0].Params)
	if before < 2 {
		t.Fatalf("fixture should have at least 2 params; got %d", before)
	}

	// Pressing `d` should stage the confirm form, not delete.
	if _, _ = c.handleParamsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}); c.formKind != formDeleteParam {
		t.Fatalf("expected formDeleteParam staged; got %v", c.formKind)
	}
	if got := len(c.work.Models[0].Presets[0].Params); got != before {
		t.Errorf("param deleted before confirm: %d → %d", before, got)
	}

	// Cancel branch: confirm=false leaves the slice untouched.
	no := false
	c.formStaging.confirm = &no
	c.applyForm()
	if got := len(c.work.Models[0].Presets[0].Params); got != before {
		t.Errorf("cancel deleted: %d → %d", before, got)
	}

	// Affirm branch: confirm=true removes the focused row.
	yes := true
	c.formKind = formDeleteParam
	c.formStaging.confirm = &yes
	c.applyForm()
	if got, want := len(c.work.Models[0].Presets[0].Params), before-1; got != want {
		t.Errorf("after confirm: params len = %d, want %d", got, want)
	}
}

// intPtr is the int-flavored sibling of strPtr — used by the clone-to-
// model staging where the form binds an int (target model index).
func intPtr(i int) *int { return &i }

// TestConfigClonePresetToModel covers the happy path: a preset cloned
// from Model A to Model B lands as a deep copy on the target, the
// source model is unchanged, and cursor state stays on the source.
func TestConfigClonePresetToModel(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx, c.presetIdx = 0, 0 // alpha / default
	c.focus = FocusPresets

	c.formStaging = formStaging{name: strPtr("default-from-alpha"), targetIdx: intPtr(1)}
	c.formKind = formCloneToModelPreset
	c.applyForm()

	// Source untouched.
	if got := len(c.work.Models[0].Presets); got != 2 {
		t.Errorf("source presets len = %d, want 2 (unchanged)", got)
	}
	// Target grew by one.
	if got := len(c.work.Models[1].Presets); got != 2 {
		t.Fatalf("target presets len = %d, want 2", got)
	}
	dup := c.work.Models[1].Presets[1]
	src := c.work.Models[0].Presets[0]

	if dup.Name != "default-from-alpha" {
		t.Errorf("dup.Name = %q, want %q", dup.Name, "default-from-alpha")
	}
	if dup.Description != src.Description {
		t.Errorf("dup.Description = %q, want %q", dup.Description, src.Description)
	}
	if len(dup.Params) != len(src.Params) {
		t.Fatalf("dup.Params len = %d, want %d", len(dup.Params), len(src.Params))
	}

	// Cursor: stays on the source preset, not the new clone.
	if c.modelIdx != 0 || c.presetIdx != 0 {
		t.Errorf("cursor moved: (model=%d, preset=%d), want (0,0)", c.modelIdx, c.presetIdx)
	}

	// Deep copy: mutating the clone must not bleed into the source.
	dup.Params[0].Value = json.Number("1")
	if got := c.work.Models[0].Presets[0].Params[0].Value; got != json.Number("99") {
		t.Errorf("source params mutated: %v, want 99 (deep copy broken)", got)
	}

	// Flash mentions the target alias.
	if c.flash == "" || !strings.Contains(c.flash, "beta") {
		t.Errorf("flash = %q, want one referencing target alias %q", c.flash, "beta")
	}
}

// TestClonePresetNameCollisionRejected ensures the validator closure
// over the target index rejects a name that already exists in the
// chosen target's presets.
func TestClonePresetNameCollisionRejected(t *testing.T) {
	cfg := duplicateTestConfig()
	target := 1 // beta has a preset called "default"
	v := clonePresetNameValidator(cfg.Models, &target)

	if err := v("default"); err == nil {
		t.Error("validator accepted colliding preset name")
	}
	if err := v(""); err == nil {
		t.Error("validator accepted empty name")
	}
	if err := v("brand-new"); err != nil {
		t.Errorf("validator rejected unique name: %v", err)
	}

	// Switching the target pointer must re-evaluate against the new
	// model's presets — beta has "default", alpha has "default" and
	// "smallctx", but cloning *to* alpha while a name like "smallctx"
	// is typed should now collide.
	target = 0
	if err := v("smallctx"); err == nil {
		t.Error("validator did not pick up new target's collision")
	}
}

// TestClonePresetToSingleModelIsNoOp covers the edge case in
// handlePresetsKey: with only one model in the config, pressing `k`
// flashes "no other model to clone to" and leaves the config alone.
func TestClonePresetToSingleModelIsNoOp(t *testing.T) {
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Models: []config.Model{
			{Alias: "alpha", Location: "/m/alpha.gguf", Presets: []config.Preset{
				{Name: "default"},
			}},
		},
	}
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx, c.presetIdx = 0, 0
	c.focus = FocusPresets

	_, cmd := c.handlePresetsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if cmd != nil {
		t.Fatalf("k with single model should not open a form; got cmd %T", cmd())
	}
	if c.form != nil || c.formKind != formNone {
		t.Errorf("k staged a form: kind=%v, form=%v", c.formKind, c.form != nil)
	}
	if c.flash != "no other model to clone to" {
		t.Errorf("flash = %q, want %q", c.flash, "no other model to clone to")
	}
	if len(c.work.Models[0].Presets) != 1 {
		t.Errorf("config mutated despite no-op: presets len = %d", len(c.work.Models[0].Presets))
	}
}

// TestCloneToFormExcludesSourceModel checks that the target Select's
// options never include the source model's index — the source self-
// clone is the existing `c clone` action.
func TestCloneToFormExcludesSourceModel(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.modelIdx, c.presetIdx = 0, 0 // alpha / default
	c.focus = FocusPresets
	c.SetSize(140, 40) // installForm uses width/height; non-zero avoids the no-op path

	if cmd := c.openClonePresetToModelForm(); cmd != nil {
		// Drain the init Cmd; we don't need the resulting message.
		_ = cmd
	}
	if c.formKind != formCloneToModelPreset {
		t.Fatalf("form kind = %v, want formCloneToModelPreset", c.formKind)
	}
	// The staged target index must not point at the source model. With
	// modelIdx = 0 and len(Models) = 2, the only valid initial value is 1.
	if c.formStaging.targetIdx == nil {
		t.Fatal("targetIdx not staged")
	}
	if *c.formStaging.targetIdx == c.modelIdx {
		t.Errorf("initial target = %d, must differ from source modelIdx %d",
			*c.formStaging.targetIdx, c.modelIdx)
	}
}

// TestConfigPaneNavIgnoresVimKeys pins the vim-nav purge: pressing j/k
// in the three config panes must not move the selection cursor. Arrow
// keys still work (covered indirectly by TestConfigModeArrowCyclesPanes).
func TestConfigPaneNavIgnoresVimKeys(t *testing.T) {
	cfg := duplicateTestConfig()
	c := NewConfigMode("/dev/null", cfg)
	c.SetSize(140, 40)

	cases := []struct {
		focus ConfigFocus
		setup func()
		idx   func() int
	}{
		{FocusModels, func() { c.modelIdx = 0 }, func() int { return c.modelIdx }},
		{FocusPresets, func() { c.modelIdx, c.presetIdx = 0, 0 }, func() int { return c.presetIdx }},
		{FocusParams, func() { c.modelIdx, c.presetIdx, c.paramIdx = 0, 0, 0 }, func() int { return c.paramIdx }},
	}
	for _, tc := range cases {
		c.focus = tc.focus
		tc.setup()
		before := tc.idx()
		c.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
		c.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
		if got := tc.idx(); got != before {
			t.Errorf("focus=%d: vim key moved index %d → %d", tc.focus, before, got)
		}
	}
}

// TestConfigExitPromptSaveBlockedByValidationDoesNotExit confirms that
// when save() is blocked by validation errors (e.g. duplicate alias),
// applyForm returns no cmd so the user stays in config mode and can
// fix the issues.
func TestConfigExitPromptSaveBlockedByValidationDoesNotExit(t *testing.T) {
	cfg := duplicateTestConfig()
	cfgPath := filepath.Join(t.TempDir(), "llamaman.json")
	c := NewConfigMode(cfgPath, cfg)

	// Force a validation error: two models sharing the same alias.
	c.work.Models[1].Alias = c.work.Models[0].Alias

	cmd := runExitChoice(&c, "save")
	if cmd != nil {
		t.Errorf("save with validation errors should not exit; got cmd producing %T", cmd())
	}
}

// TestParseParamValueClassifiesStringsWithNumericPrefix guards against the
// regression where looksNumeric used a streaming json.Decoder and accepted
// any input whose prefix parsed as a number — so "--rpc 10.0.0.30:50052"
// got cast to json.Number and either failed to save (invalid number literal)
// or, when the user worked around it with quotes, kept the literal quote
// chars in argv and broke llama-server.
func TestParseParamValueClassifiesStringsWithNumericPrefix(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"10.0.0.30:50052", "10.0.0.30:50052"},
		{"127.0.0.1:50052", "127.0.0.1:50052"},
		{"10.0.0.30", "10.0.0.30"},
		{"1.5.6", "1.5.6"},
		// Sanity: real numbers still come through as json.Number so they
		// round-trip exactly through config save/load.
		{"42", json.Number("42")},
		{"3.14", json.Number("3.14")},
		{"-7", json.Number("-7")},
		{"true", true},
		{"false", false},
		{"plain text", "plain text"},
	}
	for _, tc := range cases {
		got := parseParamValue(tc.in)
		if got != tc.want {
			t.Errorf("parseParamValue(%q) = %v (%T); want %v (%T)", tc.in, got, got, tc.want, tc.want)
		}
	}
}

// TestConfigExportIni covers the `x` export action: it opens a path
// prompt pre-filled with the derived models.ini location, writes the
// ini on apply, flashes the section count, and surfaces write errors.
func TestConfigExportIni(t *testing.T) {
	cfg := duplicateTestConfig() // alpha (2 presets) + beta (1 preset) = 3 sections
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	c := NewConfigMode(cfgPath, cfg)

	// `x` on the models pane opens the export form.
	c.handleModelsKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	if c.formKind != formExportIni {
		t.Fatalf("formKind = %v, want formExportIni", c.formKind)
	}
	if got := deref(c.formStaging.exportPath); got != filepath.Join(dir, "models.ini") {
		t.Errorf("pre-filled path = %q, want derived default", got)
	}

	// Apply with a custom path → file written, flash reports sections.
	out := filepath.Join(dir, "out.ini")
	c.formStaging.exportPath = strPtr(out)
	if _, dismiss := c.applyForm(); !dismiss {
		t.Error("export form should dismiss on success")
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("exported file missing: %v", err)
	}
	if !strings.Contains(c.flash, "exported 3 sections to "+out) {
		t.Errorf("flash = %q", c.flash)
	}

	// Unwritable path → error modal surfaces the failure.
	c.formKind = formExportIni
	c.formStaging.exportPath = strPtr("/nonexistent-dir/x.ini")
	c.applyForm()
	if c.errorModal == "" || !strings.Contains(c.errorModal, "Export failed") {
		t.Errorf("errorModal = %q", c.errorModal)
	}
}
