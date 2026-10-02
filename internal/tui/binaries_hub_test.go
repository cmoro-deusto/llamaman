package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// hubRoot builds a Root whose config has the given binaries and models,
// then opens the binaries hub (c → b).
func hubRoot(t *testing.T, binaries []config.Binary, def string, models ...config.Model) *Root {
	t.Helper()
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{
			Binaries:      binaries,
			DefaultBinary: def,
			Host:          "127.0.0.1",
			Port:          9080,
		},
		Models: models,
	}
	root := NewRoot(cfg, filepath.Join(t.TempDir(), "c.json"), stubSpawner{}, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"), keyMsg("b"))
	return root
}

func hubDown(t *testing.T, root *Root, n int) {
	t.Helper()
	for range n {
		driveRoot(t, root, tea.KeyMsg{Type: tea.KeyDown})
	}
}

func hubEnter(t *testing.T, root *Root) {
	t.Helper()
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter})
}

func hubType(t *testing.T, root *Root, s string) {
	t.Helper()
	for _, r := range s {
		driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
}

// Regression: hub → add → fill → submit must create the entry. Guards the
// ordering bug where dismissForm wiped the chained sub-form (every hub
// action was a silent no-op).
func TestBinariesHubAddFlow(t *testing.T) {
	root := hubRoot(t,
		[]config.Binary{{Name: "main", Path: "/bin/llama-server"}}, "main")
	cm := root.configMod

	// Hub rows: entry "main" (0), "＋ add binary" (1).
	hubDown(t, root, 1)
	hubEnter(t, root)
	if cm.form == nil {
		t.Fatal("add sub-form not installed after hub enter (ordering bug)")
	}
	if cm.formKind != formBinariesNew {
		t.Fatalf("formKind=%v, want formBinariesNew", cm.formKind)
	}

	hubType(t, root, "myfork")
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyTab})
	hubType(t, root, "/bin/llama-server-fork")
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyTab})
	hubType(t, root, "my branch")
	hubEnter(t, root)

	if len(cm.work.Globals.Binaries) != 2 {
		t.Fatalf("binaries=%d, want 2", len(cm.work.Globals.Binaries))
	}
	b := cm.work.Globals.Binaries[1]
	if b.Name != "myfork" || b.Path != "/bin/llama-server-fork" || b.Note != "my branch" {
		t.Fatalf("entry = %+v", b)
	}
}

// Regression: the hub must act on the entry the user highlighted, not on a
// remembered index — picking the third entry opens that entry's action menu
// and its edit form is pre-filled from it.
func TestBinariesHubTargetsSelectedEntry(t *testing.T) {
	root := hubRoot(t, []config.Binary{
		{Name: "main", Path: "/bin/llama-server"},
		{Name: "fork", Path: "/bin/llama-server-fork", Note: "branch"},
		{Name: "rocm", Path: "/opt/rocm/llama-server"},
	}, "main")
	cm := root.configMod

	// Entry rows in order: main (0), fork (1), rocm (2), add (3).
	hubDown(t, root, 2)
	hubEnter(t, root)
	if cm.formKind != formBinariesEntryMenu {
		t.Fatalf("formKind=%v, want formBinariesEntryMenu", cm.formKind)
	}
	if cm.binIdx != 2 {
		t.Fatalf("binIdx=%d, want 2 (the highlighted entry)", cm.binIdx)
	}
	hubEnter(t, root) // "edit" is the first action
	if cm.formKind != formBinariesEdit {
		t.Fatalf("formKind=%v, want formBinariesEdit", cm.formKind)
	}
	if cm.formStaging.binName == nil || *cm.formStaging.binName != "rocm" {
		t.Fatalf("edit form not pre-filled from entry 2: %+v", cm.formStaging.binName)
	}
	if cm.formStaging.binPath == nil || *cm.formStaging.binPath != "/opt/rocm/llama-server" {
		t.Fatalf("edit form path = %v, want entry 2's path", cm.formStaging.binPath)
	}
}

// Regression: "set as default" acts on the highlighted entry, and renaming
// an entry carries preset pins (and the default marker) along.
func TestBinariesHubRenameRetargetsPins(t *testing.T) {
	model := config.Model{
		Alias:    "m",
		Location: "/models/m.gguf",
		Presets: []config.Preset{
			{Name: "fast", Binary: "fork"},
			{Name: "plain"},
		},
	}
	binaries := []config.Binary{
		{Name: "main", Path: "/bin/llama-server"},
		{Name: "fork", Path: "/bin/llama-server-fork"},
	}
	root := hubRoot(t, binaries, "main", model)
	cm := root.configMod

	// Entry "fork" (row 1) → "set as default" (second action, since fork is
	// not the default yet).
	hubDown(t, root, 1)
	hubEnter(t, root)
	hubDown(t, root, 1)
	hubEnter(t, root)
	if cm.work.Globals.DefaultBinary != "fork" {
		t.Fatalf("default=%q, want fork", cm.work.Globals.DefaultBinary)
	}

	// Re-open the hub, edit "fork", rename it to "fork2".
	driveRoot(t, root, keyMsg("b"))
	hubDown(t, root, 1)
	hubEnter(t, root)
	hubEnter(t, root) // edit
	for range len("fork") {
		driveRoot(t, root, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	hubType(t, root, "fork2")
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyTab})
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyTab})
	hubEnter(t, root)

	if got := cm.work.Globals.Binaries[1].Name; got != "fork2" {
		t.Fatalf("entry 1 name=%q, want fork2", got)
	}
	if got := cm.work.Globals.DefaultBinary; got != "fork2" {
		t.Fatalf("default=%q, want fork2 (retargeted)", got)
	}
	if got := cm.work.Models[0].Presets[0].Binary; got != "fork2" {
		t.Fatalf("preset pin=%q, want fork2 (retargeted)", got)
	}
	if got := cm.work.Models[0].Presets[1].Binary; got != "" {
		t.Fatalf("unpinned preset changed: %q", got)
	}
}

// Regression: hub → delete → confirm removes the entry, reassigns the
// default, and reports orphaned preset pins instead of rewriting them.
func TestBinariesHubDelete(t *testing.T) {
	model := config.Model{
		Alias:    "m",
		Location: "/models/m.gguf",
		Presets:  []config.Preset{{Name: "fast", Binary: "main"}},
	}
	root := hubRoot(t, []config.Binary{
		{Name: "main", Path: "/bin/llama-server"},
		{Name: "fork", Path: "/bin/llama-server-fork"},
	}, "main", model)
	cm := root.configMod

	// Entry "main" (row 0) → action menu: edit, delete, cancel (no "set as
	// default" — main already is the default).
	hubEnter(t, root)
	hubDown(t, root, 1)
	hubEnter(t, root)
	if cm.form == nil {
		t.Fatalf("delete confirm not installed (flash=%q, binIdx=%d)", cm.flash, cm.binIdx)
	}
	// huh Confirm: "y" = Accept, Enter = Next (advance without setting).
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if len(cm.work.Globals.Binaries) != 1 {
		t.Fatalf("binaries=%d, want 1 after delete", len(cm.work.Globals.Binaries))
	}
	if cm.work.Globals.Binaries[0].Name != "fork" {
		t.Fatalf("remaining=%q, want fork", cm.work.Globals.Binaries[0].Name)
	}
	if cm.work.Globals.DefaultBinary != "fork" {
		t.Fatalf("default=%q, want fork (reassigned)", cm.work.Globals.DefaultBinary)
	}
	// The pin is user data: kept, and the flash says so.
	if got := cm.work.Models[0].Presets[0].Binary; got != "main" {
		t.Fatalf("pin=%q, want main (left dangling on purpose)", got)
	}
	if !strings.Contains(cm.flash, "1 preset still pins it") {
		t.Fatalf("flash=%q, want a singular orphaned-pin notice", cm.flash)
	}
}

// Regression: the orphaned-pin notice used the noun's plural for the verb
// too ("2 presets still pins it").
func TestBinariesHubDeletePluralNotice(t *testing.T) {
	model := config.Model{
		Alias:    "m",
		Location: "/models/m.gguf",
		Presets: []config.Preset{
			{Name: "fast", Binary: "main"},
			{Name: "slow", Binary: "main"},
		},
	}
	root := hubRoot(t, []config.Binary{
		{Name: "main", Path: "/bin/llama-server"},
		{Name: "fork", Path: "/bin/llama-server-fork"},
	}, "main", model)
	cm := root.configMod

	hubEnter(t, root)
	hubDown(t, root, 1)
	hubEnter(t, root)
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})

	if !strings.Contains(cm.flash, "2 presets still pin it") {
		t.Fatalf("flash=%q, want a plural orphaned-pin notice", cm.flash)
	}
}
