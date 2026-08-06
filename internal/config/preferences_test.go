package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestPreferencesAbsentMeansDefaults verifies the field-arrival
// contract: a config without a `preferences` object behaves exactly
// like the defaults (theme "auto", animations true), and the object
// stays absent from the file on save.
func TestPreferencesAbsentMeansDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := &Config{
		Version: 1,
		Globals: Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if contains := string(data); containsSubstring(contains, `"preferences"`) {
		t.Errorf("untouched config should not write a preferences object:\n%s", data)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Preferences != nil {
		t.Fatalf("Preferences should stay nil after load, got %+v", loaded.Preferences)
	}
	p := loaded.Prefs()
	if p.Theme != "" {
		t.Errorf("default theme = %q, want empty (= auto)", p.Theme)
	}
	if !p.AnimationsEnabled() {
		t.Error("default animations should be enabled")
	}
}

// TestPreferencesRoundTrip pins the full field-arrival contract: an
// explicit `"animations": false` must be distinct from absent and must
// survive a save round-trip, and an explicit theme must round-trip.
func TestPreferencesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	animOff := false
	cfg := &Config{
		Version: 1,
		Globals: Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &Preferences{
			Theme:      "nord",
			Animations: &animOff,
		},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := loaded.Prefs()
	if p.Theme != "nord" {
		t.Errorf("theme = %q, want %q", p.Theme, "nord")
	}
	if p.AnimationsEnabled() {
		t.Error("explicit animations=false must survive a save round-trip")
	}
	if loaded.Preferences == nil || loaded.Preferences.Animations == nil || *loaded.Preferences.Animations {
		t.Fatalf("Animations pointer must be non-nil and false, got %+v", loaded.Preferences)
	}
}

// TestPreferencesExplicitTrueIsSerialized verifies that a user who
// explicitly confirms animations writes `"animations": true` (not the
// absent form), so the file records their choice.
func TestPreferencesExplicitTrueIsSerialized(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	animOn := true
	cfg := &Config{
		Version: 1,
		Globals: Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &Preferences{
			Animations: &animOn,
		},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSubstring(string(data), `"animations": true`) {
		t.Errorf("explicit true should be written verbatim:\n%s", data)
	}
	if containsSubstring(string(data), `"theme"`) {
		t.Errorf("empty theme should stay omitted:\n%s", data)
	}
}

// TestPreferencesLoadAccepted verifies the additive contract: a config
// carrying `preferences` parses under the current schema (older
// binaries reject it with `json: unknown field` — that is the accepted
// P2 behavior, covered by the decoder's DisallowUnknownFields).
func TestPreferencesLoadAccepted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	raw := `{
	  "version": 1,
	  "globals": {"llama-server-bin": "/usr/bin/llama-server", "ip_address": "127.0.0.1", "port": 9080},
	  "preferences": {"theme": "catppuccin-mocha"},
	  "models": []
	}`
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Prefs().Theme; got != "catppuccin-mocha" {
		t.Errorf("theme = %q, want catppuccin-mocha", got)
	}
}

// TestPreferencesValidateIsShapeOnly verifies validation never Blocks on
// preferences: unknown theme names and any animations value produce no
// issues at config level (the unknown-theme Warning is a TUI-resolver
// concern, DESIGN §15.1).
func TestPreferencesValidateIsShapeOnly(t *testing.T) {
	cfg := &Config{
		Version: 1,
		Globals: Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &Preferences{
			Theme: "definitely-not-a-real-palette",
		},
	}
	issues := Validate(cfg)
	if issues.HasErrors() {
		t.Fatalf("preferences must never Block, got %+v", issues)
	}
	for _, it := range issues {
		if it.Path == "preferences.theme" {
			t.Errorf("config-level validation should not flag theme names, got %+v", it)
		}
	}
}

// TestMarshalForDiffIncludesPreferences guards the "● modified"
// indicator path: a preference change must show up in the marshaled
// diff bytes.
func TestMarshalForDiffIncludesPreferences(t *testing.T) {
	before, err := MarshalForDiff(&Config{Version: 1, Globals: Globals{Bin: "b", Host: "h", Port: 1}})
	if err != nil {
		t.Fatal(err)
	}
	animOff := false
	after, err := MarshalForDiff(&Config{Version: 1, Globals: Globals{Bin: "b", Host: "h", Port: 1}, Preferences: &Preferences{Theme: "nord", Animations: &animOff}})
	if err != nil {
		t.Fatal(err)
	}
	if string(before) == string(after) {
		t.Error("preference change should alter the marshaled diff")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(after, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["preferences"]; !ok {
		t.Error("after marshaling with preferences, top-level `preferences` key missing")
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
