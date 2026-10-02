package tui

import (
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// recordingSpawner captures what the TUI asked for, so tests can assert
// that a one-shot binary override (§3.2a) actually reaches the spawner
// instead of dying inside the TUI.
type recordingSpawner struct {
	spawnModel     config.Model
	spawnPreset    config.Preset
	spawnOverride  string
	routerFile     string
	routerOverride string
	cfg            *config.Config
}

func (s *recordingSpawner) SetConfig(cfg *config.Config) { s.cfg = cfg }

func (s *recordingSpawner) Spawn(model config.Model, preset config.Preset, override string) (RunModeOpts, error) {
	s.spawnModel, s.spawnPreset, s.spawnOverride = model, preset, override
	return RunModeOpts{}, errStubSpawn
}

func (s *recordingSpawner) SpawnRouter(file, override string) (RunModeOpts, error) {
	s.routerFile, s.routerOverride = file, override
	return RunModeOpts{}, errStubSpawn
}

func (s *recordingSpawner) Reattach() (*RunModeOpts, error) { return nil, nil }
func (s *recordingSpawner) RunningAlias() (string, string, int) {
	return "", "", 0
}

func twoBinaryConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Globals: config.Globals{
			Binaries: []config.Binary{
				{Name: "main", Path: "/bin/llama-server"},
				{Name: "fork", Path: "/bin/llama-server-fork"},
			},
			DefaultBinary: "main",
			Host:          "127.0.0.1",
			Port:          9080,
		},
		Models: []config.Model{{
			Alias:    "m",
			Location: "/models/m.gguf",
			Presets:  []config.Preset{{Name: "default"}},
		}},
	}
}

// Regression: the main-mode quick-switch (Shift+B) must hand the chosen
// binary to the spawner. It used to be unreachable — Root consumed `b`
// for the HF browser — and the spawner dropped the override outright.
func TestMainModeQuickSwitchForwardsOverride(t *testing.T) {
	sp := &recordingSpawner{}
	root := NewRoot(twoBinaryConfig(), filepath.Join(t.TempDir(), "c.json"), sp, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50})

	driveRoot(t, root, keyMsg("B"))
	if !root.mainMode.showBinSwitch {
		t.Fatal("Shift+B did not open the binary quick-switch")
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyDown})  // highlight "fork"
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter}) // pick it
	if got := root.mainMode.BinaryOverride(); got != "fork" {
		t.Fatalf("main override=%q, want fork", got)
	}
	if root.mainMode.showBinSwitch {
		t.Fatal("picker should close after a pick")
	}

	// Launch the selected model: the override must ride along, then clear.
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter})
	if sp.spawnOverride != "fork" {
		t.Fatalf("spawn override=%q, want fork (dropped in the TUI)", sp.spawnOverride)
	}
	if sp.spawnModel.Alias != "m" {
		t.Fatalf("spawned model=%q, want m", sp.spawnModel.Alias)
	}
	if got := root.mainMode.BinaryOverride(); got != "" {
		t.Fatalf("override not consumed after spawn: %q", got)
	}
}

// Regression: `b` keeps its pre-existing meaning in main mode (browser) and
// run mode (page up) — the quick-switch must not steal either.
func TestLowercaseBIsNotTheQuickSwitch(t *testing.T) {
	root := NewRoot(twoBinaryConfig(), filepath.Join(t.TempDir(), "c.json"), &recordingSpawner{}, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50})
	driveRoot(t, root, keyMsg("b"))
	if root.mainMode.showBinSwitch {
		t.Fatal("`b` opened the quick-switch; it belongs to the HF browser")
	}
	if root.view != ViewBrowser {
		t.Fatalf("view=%v, want ViewBrowser after `b`", root.view)
	}
}

// Regression: the run-mode quick-switch feeds the restart path, and a
// router session restarts as a router (it used to emit a single-model
// spawn with an empty model).
func TestRunModeQuickSwitchOnRestart(t *testing.T) {
	newRun := func(t *testing.T, routerFile string) *RunMode {
		t.Helper()
		proc, err := server.Spawn([]string{"/bin/sleep", "30"}, filepath.Join(t.TempDir(), "llama.log"))
		if err != nil {
			t.Fatalf("spawn helper: %v", err)
		}
		t.Cleanup(func() { proc.Stop(2 * time.Second) })
		cfg := twoBinaryConfig()
		r, _, err := NewRunMode(RunModeOpts{
			Cfg:        cfg,
			Model:      cfg.Models[0],
			Preset:     cfg.Models[0].Presets[0],
			Argv:       proc.Argv,
			Process:    proc,
			RouterFile: routerFile,
		}, DefaultTheme())
		if err != nil {
			t.Fatalf("NewRunMode: %v", err)
		}
		return r
	}

	t.Run("single model", func(t *testing.T) {
		r := newRun(t, "")
		var cmd tea.Cmd
		r, cmd = r.Update(keyMsg("B"))
		if cmd != nil {
			t.Fatal("B must not emit a command")
		}
		r, _ = r.Update(tea.KeyMsg{Type: tea.KeyDown})
		r, _ = r.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if r.pendingBinOverride != "fork" {
			t.Fatalf("pendingBinOverride=%q, want fork", r.pendingBinOverride)
		}
		msg := r.requestRestart()()
		spawn, ok := msg.(SpawnRequestMsg)
		if !ok {
			t.Fatalf("msg=%T, want SpawnRequestMsg", msg)
		}
		if spawn.BinaryOverride != "fork" {
			t.Fatalf("restart override=%q, want fork", spawn.BinaryOverride)
		}
	})

	t.Run("router", func(t *testing.T) {
		r := newRun(t, "/tmp/my-models.ini")
		r, _ = r.Update(keyMsg("B"))
		r, _ = r.Update(tea.KeyMsg{Type: tea.KeyDown})
		r, _ = r.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if r.pendingBinOverride != "fork" {
			t.Fatalf("pendingBinOverride=%q, want fork", r.pendingBinOverride)
		}
		msg := r.requestRestart()()
		router, ok := msg.(RouterSpawnRequestMsg)
		if !ok {
			t.Fatalf("msg=%T, want RouterSpawnRequestMsg (router restart must stay router mode)", msg)
		}
		if router.File != "/tmp/my-models.ini" || router.BinaryOverride != "fork" {
			t.Fatalf("router restart = %+v", router)
		}
	})
}

// Regression: `llamaman --binary NAME` with no positional alias lands on
// the landing screen; the flag must still govern the first launch from
// there (it used to be dropped entirely).
func TestSeededOverrideAppliesToNextLaunch(t *testing.T) {
	sp := &recordingSpawner{}
	root := NewRoot(twoBinaryConfig(), filepath.Join(t.TempDir(), "c.json"), sp, nil, "v", nil)
	root.SeedBinaryOverride("fork")
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50})

	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter}) // launch the selected model
	if sp.spawnOverride != "fork" {
		t.Fatalf("spawn override=%q, want fork (seeded from --binary)", sp.spawnOverride)
	}
	if got := root.mainMode.BinaryOverride(); got != "" {
		t.Fatalf("seed not consumed: %q", got)
	}
	// Second launch must be back to the default resolution.
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyEnter})
	if sp.spawnOverride != "" {
		t.Fatalf("override leaked into the next launch: %q", sp.spawnOverride)
	}
}

// Regression: with a single configured binary there is nothing to switch,
// so Shift+B does nothing (and `b` stays page-up).
func TestQuickSwitchInertWithOneBinary(t *testing.T) {
	cfg := twoBinaryConfig()
	cfg.Globals.Binaries = cfg.Globals.Binaries[:1]
	cfg.Globals.DefaultBinary = "main"
	root := NewRoot(cfg, filepath.Join(t.TempDir(), "c.json"), &recordingSpawner{}, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50})
	driveRoot(t, root, keyMsg("B"))
	if root.mainMode.showBinSwitch {
		t.Fatal("quick-switch opened with a single binary")
	}
}

// Regression: leaving the config editor must hand the saved config to the
// spawner, or binaries added/renamed there resolve against the startup
// snapshot ("unknown binary" on launch, pins silently on the default).
func TestConfigSaveReachesSpawner(t *testing.T) {
	sp := &recordingSpawner{}
	root := NewRoot(twoBinaryConfig(), filepath.Join(t.TempDir(), "c.json"), sp, nil, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"))
	if root.configMod == nil {
		t.Fatal("config editor not open")
	}
	saved := twoBinaryConfig()
	saved.Globals.Binaries = append(saved.Globals.Binaries, config.Binary{Name: "rocm", Path: "/bin/llama-server-rocm"})
	root.configMod.saved = saved

	driveRoot(t, root, returnFromConfigMsg{})

	if sp.cfg != saved {
		t.Fatalf("spawner config not replaced with the saved snapshot (got %p, want %p)", sp.cfg, saved)
	}
}

// Regression (§3.2a): `r` with no pending quick-switch must restart on the
// binary the session runs now — a --binary launch or main-screen switch
// used to fall back to the preset pin/default on restart.
func TestRestartKeepsRunningBinary(t *testing.T) {
	cases := []struct {
		name       string
		binaryName string
		want       string
	}{
		{"running override kept", "fork", "fork"},
		{"name gone from binaries[] falls back", "deleted", ""},
		{"unknown running binary", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proc, err := server.Spawn([]string{"/bin/sleep", "30"}, filepath.Join(t.TempDir(), "llama.log"))
			if err != nil {
				t.Fatalf("spawn helper: %v", err)
			}
			t.Cleanup(func() { proc.Stop(2 * time.Second) })
			cfg := twoBinaryConfig()
			r, _, err := NewRunMode(RunModeOpts{
				Cfg:        cfg,
				Model:      cfg.Models[0],
				Preset:     cfg.Models[0].Presets[0],
				Argv:       proc.Argv,
				Process:    proc,
				BinaryName: tc.binaryName,
			}, DefaultTheme())
			if err != nil {
				t.Fatalf("NewRunMode: %v", err)
			}

			msg := r.requestRestart()()

			spawn, ok := msg.(SpawnRequestMsg)
			if !ok {
				t.Fatalf("msg=%T, want SpawnRequestMsg", msg)
			}
			if spawn.BinaryOverride != tc.want {
				t.Fatalf("restart override=%q, want %q", spawn.BinaryOverride, tc.want)
			}
		})
	}
}

// resolvingSpawner is a recordingSpawner that also resolves a distinct
// one-flag registry per binary (§3.2a), named after the binary.
type resolvingSpawner struct {
	recordingSpawner
}

func (s *resolvingSpawner) RegistryFor(bin config.Binary) flags.Registry {
	return flags.Registry{bin.Name + "-only": {Name: bin.Name + "-only", Form: "--" + bin.Name + "-only"}}
}

// Regression (review §2.8): the config editor used the startup registry
// for every preset. A pinned preset must use its pin's build, an
// unpinned one the working config's default — including a default
// changed in this editor session.
func TestConfigEditorRegistryFollowsPresetBinary(t *testing.T) {
	cfg := twoBinaryConfig()
	cfg.Models[0].Presets = []config.Preset{{Name: "default"}, {Name: "pinned", Binary: "fork"}}
	root := NewRoot(cfg, filepath.Join(t.TempDir(), "c.json"), &resolvingSpawner{}, flags.Registry{"startup-only": {Name: "startup-only"}}, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"))
	cm := root.configMod
	if cm == nil || cm.registryFor == nil {
		t.Fatal("config editor not wired to the spawner's registry resolver")
	}
	has := func(key string) bool { _, ok := cm.activeRegistry().Lookup(key); return ok }

	cm.modelIdx, cm.presetIdx = 0, 0
	unpinnedDefault := has("main-only")
	cm.presetIdx = 1
	pinnedFork := has("fork-only")
	cm.presetIdx = 0
	cm.work.Globals.DefaultBinary = "fork"
	followsEditedDefault := has("fork-only")

	if !unpinnedDefault {
		t.Error("unpinned preset did not use the default binary's registry")
	}
	if !pinnedFork {
		t.Error("preset pinned to fork did not use fork's registry")
	}
	if !followsEditedDefault {
		t.Error("unpinned preset ignored the default changed in the editor")
	}
}

// Without a resolver (e.g. a spawner that cannot parse --help), the
// editor keeps the startup registry.
func TestConfigEditorRegistryWithoutResolver(t *testing.T) {
	startup := flags.Registry{"startup-only": {Name: "startup-only"}}
	root := NewRoot(twoBinaryConfig(), filepath.Join(t.TempDir(), "c.json"), &recordingSpawner{}, startup, "v", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 180, Height: 50}, keyMsg("c"))

	_, ok := root.configMod.activeRegistry().Lookup("startup-only")

	if !ok {
		t.Error("editor without a resolver must use the startup registry")
	}
}
