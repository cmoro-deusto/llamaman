package tui

import (
	"encoding/json"
	"path/filepath"
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
