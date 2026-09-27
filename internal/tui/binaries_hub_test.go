package tui

import (
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// Regression: hub → add → fill → submit must create the entry.
// Guards the ordering bug where dismissForm wiped the chained sub-form
// (every hub action was a silent no-op).
func TestBinariesHubAddFlow(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.json")
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{
			Binaries:      []config.Binary{{Name: "main", Path: "/bin/llama-server"}},
			DefaultBinary: "main", Host: "127.0.0.1", Port: 9080,
		},
		Models: []config.Model{},
	}
	root := NewRoot(cfg, cfgPath, stubSpawner{}, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"), keyMsg("b"))

	cm := root.configMod
	if cm == nil || cm.form == nil {
		t.Fatal("hub not open")
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter}) // pick "add binary"

	if cm.form == nil {
		t.Fatal("add sub-form not installed after hub enter (ordering bug)")
	}
	if cm.formKind != formBinariesNew {
		t.Fatalf("formKind=%v, want formBinariesNew", cm.formKind)
	}

	typeIn := func(s string) {
		for _, r := range s {
			driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		}
	}
	typeIn("myfork")
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyTab})
	typeIn("/bin/llama-server-fork")
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyTab})
	typeIn("my branch")
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter})

	if len(cm.work.Globals.Binaries) != 2 {
		t.Fatalf("binaries=%d, want 2", len(cm.work.Globals.Binaries))
	}
	b := cm.work.Globals.Binaries[1]
	if b.Name != "myfork" || b.Path != "/bin/llama-server-fork" || b.Note != "my branch" {
		t.Fatalf("entry = %+v", b)
	}
}

// Regression: hub → set default → pick → DefaultBinary updated.
func TestBinariesHubSetDefault(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.json")
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{
			Binaries: []config.Binary{
				{Name: "main", Path: "/bin/llama-server"},
				{Name: "fork", Path: "/bin/llama-server-fork"},
			},
			DefaultBinary: "main", Host: "127.0.0.1", Port: 9080,
		},
		Models: []config.Model{},
	}
	root := NewRoot(cfg, cfgPath, stubSpawner{}, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"), keyMsg("b"))

	// Hub: add(0), edit(1), delete(2), set default(3) — huh Select's value
	// syncs on enter, so cursor position (not staged value) is what's picked.
	for i := 0; i < 3; i++ {
		driveRoot(t, root, tea.KeyMsg{Type: tea.KeyDown})
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter}) // "set default"
	if root.configMod.form == nil {
		t.Fatal("set-default sub-form not installed")
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyDown}) // highlight "fork"
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter})
	if root.configMod.work.Globals.DefaultBinary != "fork" {
		t.Fatalf("default=%q, want fork", root.configMod.work.Globals.DefaultBinary)
	}
}

// Regression: hub → delete → confirm removes the entry and reassigns default.
func TestBinariesHubDelete(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "c.json")
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{
			Binaries: []config.Binary{
				{Name: "main", Path: "/bin/llama-server"},
				{Name: "fork", Path: "/bin/llama-server-fork"},
			},
			DefaultBinary: "main", Host: "127.0.0.1", Port: 9080,
		},
		Models: []config.Model{},
	}
	root := NewRoot(cfg, cfgPath, stubSpawner{}, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"), keyMsg("b"))

	// choices: add(0), edit(1), delete(2), set default(3) → two downs = delete
	for i := 0; i < 2; i++ {
		driveRoot(t, root, tea.KeyMsg{Type: tea.KeyDown})
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter})
	if root.configMod.form == nil {
		t.Fatalf("delete confirm not installed (flash=%q, binIdx=%d)",
			root.configMod.flash, root.configMod.binIdx)
	}
	// huh Confirm: "y"/"Y" = Accept (true), "n" = Reject. Enter = Next
	// (advance without setting). Drive "y" to accept the deletion.
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if len(root.configMod.work.Globals.Binaries) != 1 {
		t.Fatalf("binaries=%d, want 1 after delete", len(root.configMod.work.Globals.Binaries))
	}
	if root.configMod.work.Globals.Binaries[0].Name != "fork" {
		t.Fatalf("remaining=%q, want fork", root.configMod.work.Globals.Binaries[0].Name)
	}
	if root.configMod.work.Globals.DefaultBinary != "fork" {
		t.Fatalf("default=%q, want fork (reassigned)", root.configMod.work.Globals.DefaultBinary)
	}
}
