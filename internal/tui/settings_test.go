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
// row entries (s = settings, t = theme) in Main mode.
func TestSnapshotMainShowsSettingsAndThemeShortcuts(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, want := range []string{"settings", "theme"} {
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

	seq := themeCycle(true)
	// Step forward through the whole cycle from auto.
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
		keyMsg("s"),
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
		keyMsg("s"),
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
		keyMsg("s"),
		keyMsg("enter"),
		keyMsg("enter"),
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
		keyMsg("s"),
		tea.KeyMsg{Type: tea.KeyDown}, // select llamaman (first option after auto)
		keyMsg("enter"),
		keyMsg("enter"),
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
		keyMsg("s"),
		keyMsg("enter"),               // select → confirm
		tea.KeyMsg{Type: tea.KeyLeft}, // toggle confirm to "no"
		keyMsg("enter"),               // complete form
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

// TestSettingsWarnsOnUnknownStoredTheme: a hand-edited unknown theme
// shows the warning banner and the form defaults to auto (P3: degrade
// with a warning, never block).
func TestSettingsWarnsOnUnknownStoredTheme(t *testing.T) {
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{Theme: "not-a-real-theme"}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("s"),
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
