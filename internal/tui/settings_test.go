package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// forceDarkBg pins the terminal background for deterministic cycle
// tests (P9); the caller must defer forceDarkBgRestore().
func forceDarkBg(t *testing.T) {
	t.Helper()
	lipgloss.SetHasDarkBackground(true)
	lipgloss.SetColorProfile(termenv.ANSI256)
}

func forceDarkBgRestore() {
	lipgloss.SetHasDarkBackground(termenv.HasDarkBackground())
	lipgloss.SetColorProfile(termenv.ColorProfile())
}

func writeSnapshotConfig(t *testing.T) (string, *config.Config) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := sampleSnapshotConfig()
	if err := os.WriteFile(path, []byte(`{"version":1,"globals":{"llama-server-bin":"/usr/bin/llama-server","ip_address":"127.0.0.1","port":9080},"models":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

// TestSnapshotMainShowsSettingsAndThemeShortcuts pins the new shortcut
// row entries (s = storage, p = preferences, t = theme) in Main mode.
func TestSnapshotMainShowsSettingsAndThemeShortcuts(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, want := range []string{"storage", "preferences", "theme"} {
		if !strings.Contains(out, want) {
			t.Errorf("main mode shortcut row missing %q\nout:\n%s", want, out)
		}
	}
}

// TestQuickKeyTCyclesThemeForwardAndPersists: `t` in Main steps the
// cycle from auto → llamaman, writes preferences.theme, and re-renders
// with the new theme (P8 — the quick key writes the same object).
func TestQuickKeyTCyclesThemeForwardAndPersists(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	driveRoot(t, root, keyMsg("t"))
	if got := root.cfg.Prefs().Theme; got != "llamaman" {
		t.Fatalf("after t: theme = %q, want llamaman", got)
	}
	if got := root.mainMode.theme.Accent; got != lipgloss.Color("#E8A33D") {
		t.Errorf("main mode accent = %v, want llamaman dark accent", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"llamaman"`) {
		t.Errorf("theme not persisted:\n%s", data)
	}
	// The flash confirms the cycle with the palette's display name.
	if !strings.Contains(root.mainMode.flash, "llamaman (default)") {
		t.Errorf("flash = %q, want theme name", root.mainMode.flash)
	}
}

// TestQuickKeyTAndShiftTWrapTheCycle: repeated `t` walks the whole
// dark-terminal cycle (auto + 12 palettes) and wraps; `shift+t`
// reverses.
func TestQuickKeyTAndShiftTWrapTheCycle(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	seq := themeCycle()
	// Step forward through the whole cycle from auto (24 entries).
	for i := 1; i < len(seq); i++ {
		driveRoot(t, root, keyMsg("t"))
		if got := root.cfg.Prefs().Theme; got != seq[i] {
			t.Fatalf("press %d: theme = %q, want %q", i, got, seq[i])
		}
	}
	// One more press wraps back to auto (the cycle start), then on to
	// llamaman.
	driveRoot(t, root, keyMsg("t"))
	if got := root.cfg.Prefs().Theme; got != seq[0] {
		t.Fatalf("wrap press: theme = %q, want %q", got, seq[0])
	}
	driveRoot(t, root, keyMsg("t"))
	if got := root.cfg.Prefs().Theme; got != seq[1] {
		t.Fatalf("second wrap press: theme = %q, want %q", got, seq[1])
	}

	// shift+t reverses: from llamaman back to auto.
	driveRoot(t, root, keyMsg("T"))
	if got := root.cfg.Prefs().Theme; got != "auto" {
		t.Fatalf("shift+t from llamaman: theme = %q, want auto", got)
	}
}

// TestSettingsOpensFromMain: `s` switches to the Settings view with the
// preferences form (theme select + animations confirm).
func TestSettingsOpensFromMain(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
	)

	if root.view != ViewSettings {
		t.Fatalf("view = %d, want ViewSettings", root.view)
	}
	for _, want := range []string{"Settings", "theme", "animations", "auto (llamaman default)"} {
		if !strings.Contains(out, want) {
			t.Errorf("settings view missing %q\nout:\n%s", want, out)
		}
	}
}

// TestSettingsEscDiscards: Esc returns to Main without touching the
// config.
func TestSettingsEscDiscards(t *testing.T) {
	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
		keyMsg("esc"),
	)
	if root.view != ViewMain {
		t.Fatalf("after esc: view = %d, want ViewMain", root.view)
	}
	if root.cfg.Preferences != nil {
		t.Error("esc must not write preferences")
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "preferences") {
		t.Errorf("esc must not save preferences:\n%s", data)
	}
}

// TestSettingsSubmitNoChangePersistsNothing: completing the form with
// defaults (theme auto, animations on) writes nothing — the object
// stays absent until the user actually changes a preference.
func TestSettingsSubmitNoChangePersistsNothing(t *testing.T) {
	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
		keyMsg("enter"),
		keyMsg("enter"),
		keyMsg("enter"),
		tea.KeyMsg{Type: tea.KeyEnter}, // complete on the input field
	)
	if root.view != ViewMain {
		t.Fatalf("after submit: view = %d, want ViewMain", root.view)
	}
	if root.cfg.Preferences != nil {
		t.Errorf("submitting defaults should not write preferences (nothing changed), got %+v", root.cfg.Preferences)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "preferences") {
		t.Errorf("no-change submit must not persist a preferences object:\n%s", data)
	}
}

// TestSettingsSubmitPersistsThemeChange: choosing a real palette in the
// form persists preferences.theme, re-resolves the theme, and returns
// to Main. The theme Select's first option after "auto" is llamaman, so
// one Down + Enter lands there; the second Enter submits the Confirm.
func TestSettingsSubmitPersistsThemeChange(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
		tea.KeyMsg{Type: tea.KeyDown}, // select llamaman (first option after auto)
		keyMsg("enter"),
		keyMsg("enter"),
		keyMsg("enter"),
		tea.KeyMsg{Type: tea.KeyEnter}, // complete on the input field
	)

	if root.view != ViewMain {
		t.Fatalf("after submit: view = %d, want ViewMain", root.view)
	}
	if got := root.cfg.Prefs().Theme; got != "llamaman" {
		t.Fatalf("theme = %q, want llamaman", got)
	}
	if got := root.mainMode.theme.Accent; got != lipgloss.Color("#E8A33D") {
		t.Errorf("main mode accent = %v, want llamaman dark accent", got)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"theme": "llamaman"`) {
		t.Errorf("theme not persisted:\n%s", data)
	}
}

// TestSettingsSubmitAnimationsOff: flipping the Confirm field to "no"
// persists `"animations": false` (distinct from absent) through the
// form → snapshot → atomic-save path.
func TestSettingsSubmitAnimationsOff(t *testing.T) {
	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
		keyMsg("enter"),               // select → confirm
		tea.KeyMsg{Type: tea.KeyLeft}, // toggle confirm to "no"
		keyMsg("enter"),               // confirm → log colors
		keyMsg("enter"),               // log colors → models dir
		tea.KeyMsg{Type: tea.KeyEnter}, // complete form on the input field
	)
	if root.view != ViewMain {
		t.Fatalf("after submit: view = %d, want ViewMain", root.view)
	}
	if root.cfg.Prefs().AnimationsEnabled() {
		t.Error("animations should be off after the form toggle")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"animations": false`) {
		t.Errorf("animations=false not persisted:\n%s", data)
	}
}

// TestThemeCycleResetsPresetPivot guards the SetTheme edge: cycling the
// theme while the preset sub-list is open must drop the pivot instead
// of rendering a stale delegate.
func TestThemeCycleResetsPresetPivot(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyDown},  // move to beta (2 presets)
		tea.KeyMsg{Type: tea.KeyEnter}, // pivot to preset list
	)
	if !root.mainMode.showPresets {
		t.Fatal("expected preset pivot to be open")
	}
	driveRoot(t, root, keyMsg("t"))
	if root.mainMode.showPresets {
		t.Error("theme cycle must close the preset pivot")
	}
	if out := stripANSI(root.mainMode.View()); !strings.Contains(out, "alpha") {
		t.Errorf("model list should be visible again after pivot reset:\n%s", out)
	}
}

// TestSettingsShowsBothVariants: on a dark terminal the picker still
// offers the light palettes, explicitly labeled (owner decision: the
// background is a hint, not a filter).
func TestSettingsShowsBothVariants(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
	)
	for _, want := range []string{
		"terminal background: dark",
		"Catppuccin Latte (light)",
		"Solarized Light (light)",
		"Catppuccin Mocha (dark)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("settings picker missing %q (both variants must be visible)\nout:\n%s", want, out)
		}
	}
}

// TestSettingsMismatchedThemeAppliesWithWarning: a stored light palette
// on a dark terminal is kept (not reset to auto), warned about, and
// applied on submit — the explicit-override path. The user also toggles
// animations off so the submit carries a change through the full
// apply-and-save path (incl. the Main-mode warning flash).
func TestSettingsMismatchedThemeAppliesWithWarning(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{Theme: "solarized-light"}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
	)
	if root.settings == nil || root.settings.warn == "" {
		t.Fatalf("expected a mismatch warning, got warn=%q", root.settings.warn)
	}
	if !strings.Contains(out, "hard to read") {
		t.Errorf("settings view missing mismatch warning\nout:\n%s", out)
	}
	if root.settings.themeVal != "solarized-light" {
		t.Errorf("mismatched stored theme must be kept for explicit override, got %q", root.settings.themeVal)
	}

	// Submit with a change (animations off): the mismatch applies, not auto.
	driveRoot(t, root, keyMsg("enter"), tea.KeyMsg{Type: tea.KeyLeft}, keyMsg("enter"), keyMsg("enter"), tea.KeyMsg{Type: tea.KeyEnter})
	if got := root.cfg.Prefs().Theme; got != "solarized-light" {
		t.Fatalf("after submit: theme = %q, want solarized-light", got)
	}
	if got := root.mainMode.theme; got != mustPalette("solarized-light") {
		t.Errorf("main mode did not apply the light palette on the dark terminal")
	}
	if !strings.Contains(root.mainMode.flash, "hard to read") {
		t.Errorf("Main flash should carry the mismatch warning, got %q", root.mainMode.flash)
	}
	if root.cfg.Prefs().AnimationsEnabled() {
		t.Error("animations should be off after the form toggle")
	}
}

// TestSettingsLivePreviewReThemes: arrowing through the theme select
// re-themes the Settings chrome and the preview pane live — the raw
// view must show the candidate palette's accent (ANSI-256 SGR) both in
// the chrome and in the real Main-screen preview.
func TestSettingsLivePreviewReThemes(t *testing.T) {
	forceDarkBg(t)
	defer forceDarkBgRestore()

	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
	)
	if root.settings == nil {
		t.Fatal("settings not open")
	}

	raw := root.settings.View()
	if !strings.Contains(raw, "preview (main screen with selected theme)") {
		t.Error("preview pane missing")
	}
	// Initial: auto → llamaman dark accent #E8A33D (xterm 179).
	if !containsSequence(raw, "\x1b[38;5;179m") {
		t.Errorf("initial preview should use llamaman dark accent (179):\n%.400s", raw)
	}

	// Arrow to Catppuccin Mocha (peach #FAB387, xterm 216).
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})
	if got := root.settings.theme.Accent; got != lipgloss.Color("#FAB387") {
		t.Fatalf("chrome accent = %v, want catppuccin-mocha peach", got)
	}
	raw = root.settings.View()
	if !containsSequence(raw, "\x1b[38;5;216m") {
		t.Errorf("preview must show the candidate palette accent (216):\n%.400s", raw)
	}
}

// TestSettingsWarnsOnUnknownStoredTheme: a hand-edited unknown theme
// shows the warning banner and the form defaults to auto (P3: degrade
// with a warning, never block).
func TestSettingsWarnsOnUnknownStoredTheme(t *testing.T) {
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{Theme: "not-a-real-theme"}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
	)

	if root.settings == nil || root.settings.warn == "" {
		t.Fatalf("expected a warning banner for unknown theme, got warn=%q", root.settings.warn)
	}
	if !strings.Contains(out, "unknown theme") {
		t.Errorf("settings view missing warning text\nout:\n%s", out)
	}
	// The form must have reset to auto.
	if root.settings.themeVal != "auto" {
		t.Errorf("form theme = %q, want auto fallback", root.settings.themeVal)
	}
}

// TestSettingsSubmitModelsDir: entering a models directory in the new
// form field persists preferences.models-dir; clearing it removes the
// field (empty == absent, DESIGN §16.1 field-arrival contract).
func TestSettingsSubmitModelsDir(t *testing.T) {
	path, cfg := writeSnapshotConfig(t)
	root := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
		keyMsg("enter"), // theme → animations
		keyMsg("enter"), // animations → log colors
		keyMsg("enter"), // log colors → models dir
		keyMsg("/opt/llama-models"),
		tea.KeyMsg{Type: tea.KeyEnter}, // complete form
	)
	if root.view != ViewMain {
		t.Fatalf("after submit: view = %d, want ViewMain", root.view)
	}
	if got := root.cfg.Prefs().ModelsDir; got != "/opt/llama-models" {
		t.Fatalf("models-dir = %q, want /opt/llama-models", got)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"models-dir": "/opt/llama-models"`) {
		t.Errorf("models-dir not persisted:\n%s", data)
	}

	// Re-open, clear the field, submit: the field must disappear.
	root = NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
		keyMsg("enter"),
		keyMsg("enter"),
		keyMsg("enter"),
		tea.KeyMsg{Type: tea.KeyCtrlA}, // line start
		tea.KeyMsg{Type: tea.KeyCtrlK}, // delete after cursor → empty input
		tea.KeyMsg{Type: tea.KeyEnter}, // complete form
	)
	if got := root.cfg.Prefs().ModelsDir; got != "" {
		t.Fatalf("cleared models-dir = %q, want empty", got)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "models-dir") {
		t.Errorf("cleared models-dir must be omitted on save:\n%s", data)
	}
}

// TestSettingsOpensWithModelsDirFromConfig: the settings form binds the
// models directory input to the stored preference (DESIGN §16.1 P2
// editor support); the end-to-end submit round trip is covered by
// TestSettingsSubmitModelsDir.
func TestSettingsOpensWithModelsDirFromConfig(t *testing.T) {
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{ModelsDir: "/opt/llama-models"}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("p"),
	)
	if root.view != ViewSettings {
		t.Fatalf("view = %d, want ViewSettings", root.view)
	}
	if root.settings == nil {
		t.Fatal("settings mode not open")
	}
	if got := root.settings.modelsDir; got != "/opt/llama-models" {
		t.Errorf("form models-dir = %q, want the stored value", got)
	}
}
