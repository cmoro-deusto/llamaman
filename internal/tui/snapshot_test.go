package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
	"github.com/cmoro-deusto/llamaman/internal/modelsini"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// stubSpawner satisfies the Spawner interface for tests where we don't
// want to actually fork llama-server.
type stubSpawner struct{ runningAlias string }

func (s stubSpawner) Spawn(config.Model, config.Preset) (RunModeOpts, error) {
	return RunModeOpts{}, errStubSpawn
}
func (s stubSpawner) SpawnRouter(string) (RunModeOpts, error) {
	return RunModeOpts{}, errStubSpawn
}
func (s stubSpawner) Reattach() (*RunModeOpts, error) { return nil, nil }
func (s stubSpawner) RunningAlias() (string, string, int) {
	if s.runningAlias == "" {
		return "", "", 0
	}
	return s.runningAlias, "default", 9080
}

var errStubSpawn = stubSpawnerError{}

type stubSpawnerError struct{}

func (stubSpawnerError) Error() string { return "stub spawn called" }

func sampleSnapshotConfig() *config.Config {
	return &config.Config{
		Version: 1,
		Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Models: []config.Model{
			{Alias: "alpha", Location: "/m/alpha.gguf", Presets: []config.Preset{
				{Name: "default", Params: config.Params{
					{Key: "ngl", Value: json.Number("99")},
					{Key: "jinja", Value: true},
				}},
			}},
			{Alias: "beta", Location: "/m/beta.gguf", Presets: []config.Preset{
				{Name: "default"}, {Name: "smallctx"},
			}},
		},
	}
}

// driveRoot calls Init then Update with each message, draining the
// resulting tea.Cmds (one level deep) so dispatched messages like
// SpawnRequestMsg actually reach Root.Update on the next round-trip.
// Returns the View after the final message has been processed.
//
// Init is only invoked if root.initialRun was set at construction (or
// will produce a non-nil Cmd) — without that, driving from a fresh
// Root would skip the initial run-mode wiring.
func driveRoot(t *testing.T, root *Root, msgs ...tea.Msg) string {
	t.Helper()
	var m tea.Model = root
	if cmd := root.Init(); cmd != nil {
		for _, sub := range collectCmds(cmd) {
			out := safeCmd(sub)
			if out == nil {
				continue
			}
			next, _ := m.Update(out)
			m = next
		}
	}
	for _, msg := range msgs {
		next, cmd := m.Update(msg)
		m = next
		for _, sub := range collectCmds(cmd) {
			out := safeCmd(sub)
			if out == nil {
				continue
			}
			next, _ = m.Update(out)
			m = next
		}
	}
	return stripANSI(m.View())
}

// collectCmds flattens a tea.Cmd batch into its constituent Cmds. tea
// represents batches as opaque functions, so we have to call the Cmd
// once and inspect its return value.
func collectCmds(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	msg := safeCmd(cmd)
	switch v := msg.(type) {
	case tea.BatchMsg:
		return v
	case nil:
		return nil
	default:
		return []tea.Cmd{func() tea.Msg { return msg }}
	}
}

// safeCmd executes a tea.Cmd and recovers from panics. Bounded by a
// short timeout so blocking Cmds (tea.Tick) don't slow down tests.
func safeCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- nil
				return
			}
		}()
		done <- cmd()
	}()
	select {
	case msg := <-done:
		return msg
	case <-time.After(50 * time.Millisecond):
		return nil
	}
}

// keyMsg builds a single-character key message.
func keyMsg(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestSnapshotMainMode(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	// Main mode now embeds the model selection list, so the alias rows
	// must be visible alongside the version/shortcut chrome.
	for _, want := range []string{
		"llamaman v0.0.0-test",
		"alpha",
		"beta",
		"navigate",
		"select",
		"configure",
		"help",
		"quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("main mode output missing %q\nout:\n%s", want, out)
		}
	}
}

// TestSnapshotMainModeNoModelsHidesList covers the empty-config case:
// when no models are configured, the inline list is not rendered and
// the landing screen reverts to its bare "configure to begin" form.
func TestSnapshotMainModeNoModelsHidesList(t *testing.T) {
	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	if strings.Contains(out, "navigate") {
		t.Errorf("expected no list-related shortcut when no models configured; out:\n%s", out)
	}
	for _, want := range []string{"llamaman v0.0.0-test", "configure", "quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("main mode missing %q in zero-models layout\nout:\n%s", want, out)
		}
	}
}

func TestSnapshotMainModeShowsDetachedLineWhenSessionRunning(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{runningAlias: "alpha"}, nil, "v0.0.0-test", nil)
	root.refreshSessionState()

	out := driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})

	for _, want := range []string{"Detached", "alpha", "9080", "press a to attach"} {
		if !strings.Contains(out, want) {
			t.Errorf("main mode output missing %q\nout:\n%s", want, out)
		}
	}
}

// TestSnapshotMainModeShowsRunningMarker verifies the inline list's
// per-row `(running)` suffix when a session for that alias is live.
func TestSnapshotMainModeShowsRunningMarker(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{runningAlias: "alpha"}, nil, "v0.0.0-test", nil)
	root.refreshSessionState()

	out := driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})
	if !strings.Contains(out, "(running)") {
		t.Errorf("expected (running) marker on alpha row; out:\n%s", out)
	}
}

// TestSnapshotMainModePivotsToPresetSubList exercises the multi-preset
// pivot: navigating to `beta` (2 presets) and pressing Enter swaps the
// inline list to show the preset names.
func TestSnapshotMainModePivotsToPresetSubList(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyDown}, // move to beta
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if !strings.Contains(out, "smallctx") {
		t.Errorf("preset sub-list should show smallctx; out:\n%s", out)
	}
	if !strings.Contains(out, "default") {
		t.Errorf("preset sub-list should show default; out:\n%s", out)
	}
}

// TestSnapshotMainModeRouterEmptyState verifies Router mode without any
// registered sources renders guidance instead of a blank screen: it must
// point at config mode's "models files" globals field and at the CLI
// escapes (llamaman -i / import).
// TestSnapshotMainModeRouterDefaultSource verifies Router mode with no
// explicit globals.models-files shows the derived <config-dir>/models.ini
// as the default source (0 models when the ini doesn't exist yet).
func TestSnapshotMainModeRouterDefaultSource(t *testing.T) {
	cfg := sampleSnapshotConfig()
	cfg.Globals.ModelsFiles = nil
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	root := NewRoot(cfg, cfgPath, stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyTab}, // Single → Router
	)

	want := filepath.Join(dir, modelsini.DefaultModelsIniName)
	if !strings.Contains(out, want) {
		t.Errorf("router view missing derived default source %q; out:\n%s", want, out)
	}
	if strings.Contains(out, "alpha") {
		t.Errorf("router view should not list config models; out:\n%s", out)
	}
}

// TestMainModeRouterEmptyStateDirect pins the empty-state guidance that
// shows when no derived default can be computed (no config path) —
// still reachable code, e.g. for MainMode constructed without a path.
func TestMainModeRouterEmptyStateDirect(t *testing.T) {
	cfg := sampleSnapshotConfig()
	cfg.Globals.ModelsFiles = nil
	m := NewMainMode(cfg, "v0.0.0-test") // no cfgPath → no default source
	m.SetSize(120, 40)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})

	out := stripANSI(m.View())
	for _, want := range []string{
		"No router sources yet",
		"models files",
		"llamaman -i <file>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("router empty state missing %q; out:\n%s", want, out)
		}
	}
}

// TestSnapshotMainModeEmptyModels verifies Single mode with a model-less
// config (legal, e.g. after an empty import) points at config mode.
func TestSnapshotMainModeEmptyModels(t *testing.T) {
	cfg := sampleSnapshotConfig()
	cfg.Models = nil
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
	)

	for _, want := range []string{
		"No models configured yet",
		"Press c to open config mode",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("single-mode empty state missing %q; out:\n%s", want, out)
		}
	}
}

// TestSnapshotMainModeRouterView covers the Router mode picker: tab
// switches from the model list to the globals.models-files entries,
// each showing its parsed section count.
func TestSnapshotMainModeRouterView(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "my-models.ini")
	if err := os.WriteFile(ini, []byte("[a]\nmodel = a.gguf\n[b]\nhf = org/repo:Q4_0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := sampleSnapshotConfig()
	cfg.Globals.ModelsFiles = []string{ini}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyTab}, // Single → Router
	)

	if !strings.Contains(out, "my-models.ini") {
		t.Errorf("router view missing file entry; out:\n%s", out)
	}
	if !strings.Contains(out, "router · 2 models") {
		t.Errorf("router view missing section count; out:\n%s", out)
	}
	if strings.Contains(out, "alpha") {
		t.Errorf("router view should not list config models, found alpha; out:\n%s", out)
	}
}

// TestMainModeRouterToggleCycles verifies tab toggles between the model
// picker and the router picker, and that a parse-failing models-file
// still renders with a parse-error description.
func TestMainModeRouterToggleCycles(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "good.ini")
	if err := os.WriteFile(good, []byte("[m]\nmodel = m.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := sampleSnapshotConfig()
	cfg.Globals.ModelsFiles = []string{good, filepath.Join(dir, "broken.ini")}
	m := NewMainMode(cfg, "v0.0.0-test")
	m.SetSize(120, 40)

	// Default: single model mode shows model aliases.
	out := stripANSI(m.View())
	if !strings.Contains(out, "alpha") {
		t.Errorf("single mode missing model list; out:\n%s", out)
	}

	// tab → router mode.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	out = stripANSI(m.View())
	if !strings.Contains(out, "good.ini") || !strings.Contains(out, "parse error") {
		t.Errorf("router mode missing entries/parse error; out:\n%s", out)
	}

	// tab → back to single mode.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	out = stripANSI(m.View())
	if !strings.Contains(out, "alpha") {
		t.Errorf("second tab did not return to single mode; out:\n%s", out)
	}
}

// TestMainModeRouterEnterEmitsRouterSpawnRequest verifies Enter in
// Router mode emits RouterSpawnRequestMsg with the selected file path.
func TestMainModeRouterEnterEmitsRouterSpawnRequest(t *testing.T) {
	dir := t.TempDir()
	ini := filepath.Join(dir, "my-models.ini")
	if err := os.WriteFile(ini, []byte("[m]\nmodel = m.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := sampleSnapshotConfig()
	cfg.Globals.ModelsFiles = []string{ini}
	m := NewMainMode(cfg, "v0.0.0-test")
	m.SetSize(120, 40)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab}) // router mode
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter in router mode produced no command")
	}
	msg := cmd()
	rs, ok := msg.(RouterSpawnRequestMsg)
	if !ok {
		t.Fatalf("Enter produced %T, want RouterSpawnRequestMsg", msg)
	}
	if rs.File != ini {
		t.Errorf("RouterSpawnRequestMsg.File = %q, want %q", rs.File, ini)
	}
}

func TestSnapshotConfigMode(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		keyMsg("c"),
	)

	for _, want := range []string{
		"configuration",
		"Models",
		"Presets",
		"Params",
		"alpha",
		"beta",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("config output missing %q\nout:\n%s", want, out)
		}
	}
}

func TestSnapshotRunMode(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "llama.log")
	proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(2 * time.Second) })

	cfg := sampleSnapshotConfig()
	model := cfg.Models[0]
	preset := model.Presets[0]
	opts := RunModeOpts{
		Cfg:     cfg,
		Model:   model,
		Preset:  preset,
		Argv:    proc.Argv,
		Process: proc,
		Fetcher: llamaapi.NewClient("127.0.0.1", 9080),
	}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)

	// Drive Init to wire the run mode and its initial Cmds.
	cmd := root.Init()
	for cmd != nil {
		// Resolve any synchronous tea.Cmd that doesn't require the
		// tea.Program runtime. Tick/IO Cmds return nil here, which is
		// fine — we already have the model wired up.
		break
	}

	// Send WindowSizeMsg so SetSize wires up the viewport. Width 200
	// is wide enough that the wordmark + full right-column content
	// (including the [Metrics] indicator) all fit without truncation.
	root.Update(tea.WindowSizeMsg{Width: 200, Height: 40})

	// Wait for the fakeserver to start and /props to succeed.
	// Readiness now triggers on /props success, not log parsing.
	// Feed log chunks and periodically retry /props until [READY].
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("never saw [READY] in run mode; current view:\n%s", stripANSI(root.View()))
		default:
		}
		if root.run != nil {
			// Pull pending log chunks via the tailer.
			select {
			case s, ok := <-root.run.tail.Chunks():
				if ok {
					root.run.Update(logChunkMsg(s))
				}
			default:
			}
			// Retry /props fetch — fakeserver HTTP server starts after
			// a delay, so early attempts will fail.
			if root.run.fetcher != nil {
				if cmd := fetchPropsCmd(root.run.fetchCtx, root.run.fetcher); cmd != nil {
					if msg := cmd(); msg != nil {
						root.run.Update(msg)
					}
				}
			}
			if strings.Contains(stripANSI(root.View()), "[READY]") {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	out := stripANSI(root.View())
	for _, want := range []string{"Alias:", "alpha", "Preset:", "default", "[READY]", "llama-server", "Hardware", "Tokens", "Prompt"} {
		if !strings.Contains(out, want) {
			t.Errorf("run mode output missing %q\nout:\n%s", want, out)
		}
	}
	// Phase 0: the sampling-param row and [Metrics] indicator have been
	// removed from the header. Pin those negatives so a regression that
	// reintroduces them fails this test.
	for _, dont := range []string{"[Metrics]", "Temp:", "Top_P:", "Top_K:", "Min_P:"} {
		if strings.Contains(out, dont) {
			t.Errorf("run mode output should no longer contain %q\nout:\n%s", dont, out)
		}
	}
}

// failingSpawner satisfies Spawner but always errors on Spawn. Used to
// verify that the user actually sees an error message instead of Enter
// feeling like a no-op.
type failingSpawner struct{ msg string }

func (f failingSpawner) Spawn(config.Model, config.Preset) (RunModeOpts, error) {
	return RunModeOpts{}, failingSpawnError{f.msg}
}
func (f failingSpawner) SpawnRouter(string) (RunModeOpts, error) {
	return RunModeOpts{}, failingSpawnError{f.msg}
}
func (failingSpawner) Reattach() (*RunModeOpts, error)     { return nil, nil }
func (failingSpawner) RunningAlias() (string, string, int) { return "", "", 0 }

type failingSpawnError struct{ msg string }

func (e failingSpawnError) Error() string { return e.msg }

// TestSpawnFailureFlashesInMainModeFromInlineList reproduces the
// "I press Enter on my model and nothing happens" scenario when
// llama-server isn't installed — the spawn failure now lands as a
// flash on the main-mode screen (where the inline list lives), so
// users see the underlying error rather than a silent no-op.
func TestSpawnFailureFlashesInMainModeFromInlineList(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", failingSpawner{msg: "fork/exec /usr/bin/llama-server: no such file or directory"}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		tea.KeyMsg{Type: tea.KeyEnter}, // Enter on first row of the inline list
	)

	for _, want := range []string{
		"spawn failed",
		"no such file or directory",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected flash to contain %q; out:\n%s", want, out)
		}
	}
}

// TestSpawnFailureFlashesInMainMode covers the spawn-from-main path.
// flashSpawnError reads r.view to decide which sub-mode receives the
// flash, so we have to make MainMode visible first via a WindowSize.
func TestSpawnFailureFlashesInMainMode(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 120, Height: 40})
	root.flashSpawnError(failingSpawnError{msg: "port 9080 in use"})
	out := stripANSI(root.View())
	if !strings.Contains(out, "spawn failed: port 9080 in use") {
		t.Errorf("expected flash on main view; got:\n%s", out)
	}
}

// TestConfigFormHandlesLongLocationPath covers the original bug: a long
// model location should not break opening the new-model form. We can't
// easily assert the visible scroll behavior without a real terminal, but
// we can verify the form opens cleanly with a long initial value and
// that ConfigMode doesn't panic on the WindowSizeMsg-passthrough path.
func TestConfigFormHandlesLongLocationPath(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("config form with long path panicked: %v", r)
		}
	}()
	cfg := sampleSnapshotConfig()
	// Replace alpha's location with an obnoxiously long path.
	cfg.Models[0].Location = strings.Repeat("/very-long-segment", 30) + ".gguf"

	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 100, Height: 30},
		keyMsg("c"),
		keyMsg("e"), // edit selected (alpha) model
	)
	// Just reaching here without panic is the assertion. installForm
	// drives a synthetic WindowSizeMsg into huh so its inputs size
	// themselves.
}

// TestRunModeDirectKillReturnsToMain verifies the new direct `k`
// shortcut: stops the child, frees the session, and pops back to main
// without exiting llamaman. (Pressing `k` previously bubbled to the
// viewport's vi-style up-scroll; we now intercept it.)
func TestRunModeDirectKillReturnsToMain(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "llama.log")
	proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(2 * time.Second) })

	cfg := sampleSnapshotConfig()
	opts := RunModeOpts{
		Cfg: cfg, Model: cfg.Models[0], Preset: cfg.Models[0].Presets[0],
		Argv: proc.Argv, Process: proc,
	}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 140, Height: 40})

	// Press `k` to open the confirm dialog (no immediate kill).
	driveRoot(t, root, keyMsg("k"))
	if root.run == nil || !root.run.killPrompt {
		t.Fatal("k should open the kill confirm prompt")
	}
	if root.view != ViewRun {
		t.Fatalf("k should not transition before confirm; view = %d", root.view)
	}
	// Confirm with y → kill + return to main.
	driveRoot(t, root, keyMsg("y"))
	if root.view != ViewMain {
		t.Fatalf("after k+y: view = %d, want ViewMain (%d)", root.view, ViewMain)
	}
	select {
	case <-proc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("y did not stop the child")
	}
}

// TestRunModeKillReturnsToMatchingMainMode is the regression for the
// reattach-kill bug: killing a router session must return the main menu
// to Router mode even when the session was attached from Single mode
// (and vice versa), not whatever mode the toggle happened to be in.
func TestRunModeKillReturnsToMatchingMainMode(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	cfg := sampleSnapshotConfig()

	t.Run("router session returns to router mode", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "llama.log")
		proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { proc.Stop(2 * time.Second) })

		opts := RunModeOpts{
			Cfg: cfg, RouterFile: "models.ini", Argv: proc.Argv, Process: proc,
		}
		root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)
		driveRoot(t, root, tea.WindowSizeMsg{Width: 140, Height: 40})
		root.mainMode.SetMode(modeSingle) // simulate reattaching from Single mode

		driveRoot(t, root, keyMsg("k"), keyMsg("y"))
		if root.view != ViewMain {
			t.Fatalf("view = %d, want ViewMain", root.view)
		}
		if root.mainMode.mode != modeRouter {
			t.Errorf("main mode = %v, want modeRouter after killing a router session", root.mainMode.mode)
		}
	})

	t.Run("single session returns to single mode", func(t *testing.T) {
		logPath := filepath.Join(t.TempDir(), "llama.log")
		proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { proc.Stop(2 * time.Second) })

		opts := RunModeOpts{
			Cfg: cfg, Model: cfg.Models[0], Preset: cfg.Models[0].Presets[0],
			Argv: proc.Argv, Process: proc,
		}
		root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)
		driveRoot(t, root, tea.WindowSizeMsg{Width: 140, Height: 40})
		root.mainMode.SetMode(modeRouter) // simulate reattaching from Router mode

		driveRoot(t, root, keyMsg("k"), keyMsg("y"))
		if root.view != ViewMain {
			t.Fatalf("view = %d, want ViewMain", root.view)
		}
		if root.mainMode.mode != modeSingle {
			t.Errorf("main mode = %v, want modeSingle after killing a single-model session", root.mainMode.mode)
		}
	})
}

// TestRunModeQuitPromptKillQuitsLlamaman verifies the q→k path: the
// quit prompt's (k)ill option must kill the server AND quit llamaman,
// not just return to main mode (the direct `k` shortcut handles
// that). A regression here would trick users into thinking they quit
// when llamaman is actually still up on the main screen.
func TestRunModeQuitPromptKillQuitsLlamaman(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "llama.log")
	proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(2 * time.Second) })

	cfg := sampleSnapshotConfig()
	opts := RunModeOpts{
		Cfg: cfg, Model: cfg.Models[0], Preset: cfg.Models[0].Presets[0],
		Argv: proc.Argv, Process: proc,
	}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 140, Height: 40})

	// q opens the quit prompt; k inside the prompt kills + quits.
	driveRoot(t, root, keyMsg("q"))
	if root.run == nil || !root.run.showQuit {
		t.Fatal("q should open the quit prompt")
	}
	driveRoot(t, root, keyMsg("k"))

	// Run mode should still hold the model — we're quitting the whole
	// program, not bouncing back to main.
	if root.view != ViewRun {
		t.Errorf("after q+k the view should not have switched to main (it should be exiting); got view=%d", root.view)
	}
	if !root.quitting {
		// `quitting` is set by Root when it receives tea.Quit. Without
		// the tea.Program runtime in the test we drive only the model
		// layer; check that the kill cleanup ran instead.
		select {
		case <-proc.Done():
		case <-time.After(3 * time.Second):
			t.Fatal("q+k did not stop the child — quit prompt's kill is wired to the wrong cmd")
		}
	}
}

// TestRunModeKillPromptCancel covers the n/esc paths — the user can
// dismiss the confirm dialog and stay in run mode without affecting
// the child.
func TestRunModeKillPromptCancel(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "llama.log")
	proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(2 * time.Second) })

	cfg := sampleSnapshotConfig()
	opts := RunModeOpts{
		Cfg: cfg, Model: cfg.Models[0], Preset: cfg.Models[0].Presets[0],
		Argv: proc.Argv, Process: proc,
	}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 140, Height: 40})
	driveRoot(t, root, keyMsg("k"), keyMsg("n"))
	if root.run == nil || root.run.killPrompt {
		t.Fatal("n should dismiss the kill prompt")
	}
	if root.view != ViewRun {
		t.Fatalf("after k+n: view = %d, want ViewRun", root.view)
	}
	if !server.IsLive(proc.Pid) {
		t.Fatal("cancel should not have killed the child")
	}
	root.run.killAndReturn()
}

// TestRunModeKDoesNotScrollViewport sanity-checks that the viewport's
// j/k vi-style bindings are gone — pressing j or k should not move the
// cursor inside the log viewport. We can't directly inspect viewport
// internals, but we can verify the keymap was overridden.
func TestRunModeKDoesNotScrollViewport(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	logPath := filepath.Join(t.TempDir(), "llama.log")
	proc, err := server.Spawn([]string{bin, "--ready-delay=20ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { proc.Stop(2 * time.Second) })

	cfg := sampleSnapshotConfig()
	opts := RunModeOpts{
		Cfg: cfg, Model: cfg.Models[0], Preset: cfg.Models[0].Presets[0],
		Argv: proc.Argv, Process: proc,
	}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", &opts)
	driveRoot(t, root, tea.WindowSizeMsg{Width: 140, Height: 40})
	if root.run == nil {
		t.Fatal("run mode not initialized")
	}
	// Up/Down should have only the arrow keys; j/k must be unbound.
	upKeys := root.run.viewport.KeyMap.Up.Keys()
	downKeys := root.run.viewport.KeyMap.Down.Keys()
	for _, k := range append(upKeys, downKeys...) {
		if k == "j" || k == "k" {
			t.Errorf("viewport.KeyMap still binds %q", k)
		}
	}
	// Cleanup to not leave a child running.
	root.run.killAndReturn()
}

// TestParamPickerShowsNamesWithoutDashesAndDescriptions covers two
// related complaints: the new-param flag chooser should not show
// "--threads" but "threads", and each row should display the parsed
// help description on the right.
func TestParamPickerShowsNamesWithoutDashesAndDescriptions(t *testing.T) {
	reg := flags.Registry{
		"threads":    {Name: "threads", Form: "--threads", IsBool: false, Kind: flags.KindNumeric, Description: "number of CPU threads to use"},
		"jinja":      {Name: "jinja", Form: "--jinja", IsBool: true, Kind: flags.KindBool, Description: "use jinja templates"},
		"flash-attn": {Name: "flash-attn", Form: "--flash-attn", Kind: flags.KindEnum, Enum: []string{"on", "off", "auto"}, Description: "set Flash Attention use"},
	}
	p := newParamPicker(reg)
	p.SetSize(100, 30)
	out := stripANSI(p.View(CurrentTheme()))

	for _, want := range []string{
		"threads",               // bare name (no --)
		"number of CPU threads", // description
		"jinja",
		"use jinja templates",
		"flash-attn",
		"set Flash Attention use",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("picker output missing %q\nout:\n%s", want, out)
		}
	}
	for _, dont := range []string{"--threads", "--jinja", "--flash-attn"} {
		if strings.Contains(out, dont) {
			t.Errorf("picker output should not contain %q\nout:\n%s", dont, out)
		}
	}
}

// TestParamPickerAutoFiltersOnType verifies the user can just start
// typing a flag name without first pressing `/` — a printable rune in
// non-filter state should switch the list into Filtering and forward
// the rune.
func TestParamPickerAutoFiltersOnType(t *testing.T) {
	reg := flags.Registry{
		"threads":    {Name: "threads", Form: "--threads", Description: "CPU threads"},
		"flash-attn": {Name: "flash-attn", Form: "--flash-attn", Description: "FA"},
		"jinja":      {Name: "jinja", Form: "--jinja", Description: "jinja templates"},
	}
	p := newParamPicker(reg)
	p.SetSize(80, 20)
	if state := p.list.FilterState(); state != list.Unfiltered {
		t.Fatalf("initial filter state = %v, want Unfiltered", state)
	}
	// Press 't' — picker should drop into filtering mode and the input
	// should contain "t" (or be in the process of accepting it).
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	if state := p.list.FilterState(); state != list.Filtering {
		t.Fatalf("after 't': filter state = %v, want Filtering", state)
	}
	if got := p.list.FilterInput.Value(); got != "t" {
		t.Errorf("FilterInput.Value() = %q, want %q", got, "t")
	}
}

// TestParamPickerFilterRendersInlineNotFullWidth guards against the
// "screen-wide filter window" regression: the list's built-in filter
// input pads to full Width with trailing spaces. We render our own
// compact "filter: <pat>" line and disable the list's own filter view.
func TestParamPickerFilterRendersInlineNotFullWidth(t *testing.T) {
	reg := flags.Registry{
		"threads":    {Name: "threads", Form: "--threads", Description: "CPU threads"},
		"flash-attn": {Name: "flash-attn", Form: "--flash-attn", Description: "Flash attention"},
	}
	p := newParamPicker(reg)
	p.SetSize(80, 20)

	// Type a few letters to enter filter mode.
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("t")})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("h")})
	p, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	view := stripANSI(p.View(CurrentTheme()))

	if !strings.Contains(view, "filter: thr") {
		t.Fatalf("expected compact 'filter: thr' line; view:\n%s", view)
	}
	// The list's built-in filter would render "Filter: " (capital F).
	// Make sure we're NOT showing two filter rows.
	if strings.Count(view, "filter") != strings.Count(strings.ToLower(view), "filter") {
		t.Errorf("inconsistent filter casing in view:\n%s", view)
	}
	if strings.Contains(view, "Filter: ") {
		t.Errorf("bubbles/list's default 'Filter: ' rendering leaked through; view:\n%s", view)
	}
}

// TestParamPickerEscClearsFilterBeforeClosingPicker covers the layered
// Esc behavior: in FilterApplied state, Esc resets the filter and
// stays in the picker; from Unfiltered, Esc closes.
func TestParamPickerEscClearsFilterBeforeClosingPicker(t *testing.T) {
	reg := flags.Registry{
		"threads": {Name: "threads", Form: "--threads", Description: "CPU threads"},
	}
	p := newParamPicker(reg)
	p.SetSize(80, 20)

	// Use SetFilterText for a synchronous transition into FilterApplied
	// (the keypress path runs filterItems as an async Cmd that we'd
	// have to drain through the runtime).
	p.list.SetFilterText("t")
	if state := p.list.FilterState(); state != list.FilterApplied {
		t.Fatalf("after SetFilterText: state = %v, want FilterApplied", state)
	}

	// Esc should clear, not close.
	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd != nil {
		if msg := cmd(); msg != nil {
			if done, ok := msg.(paramPickerDoneMsg); ok {
				t.Fatalf("Esc in FilterApplied should not close picker; got %+v", done)
			}
		}
	}
	if state := p.list.FilterState(); state != list.Unfiltered {
		t.Errorf("Esc should reset to Unfiltered; got %v", state)
	}

	// Second Esc closes the picker.
	_, cmd = p.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("second Esc should close the picker")
	}
	msg := cmd()
	done, ok := msg.(paramPickerDoneMsg)
	if !ok || !done.cancelled {
		t.Fatalf("second Esc should emit cancelled paramPickerDoneMsg; got %+v", msg)
	}
}

// TestParamPickerEnterEmitsKey verifies pressing Enter on a highlighted
// row dispatches paramPickerDoneMsg with the bare key.
func TestParamPickerEnterEmitsKey(t *testing.T) {
	reg := flags.Registry{
		"threads": {Name: "threads", Form: "--threads", Description: "..."},
	}
	p := newParamPicker(reg)
	p.SetSize(80, 20)

	_, cmd := p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a Cmd from Enter")
	}
	msg := cmd()
	done, ok := msg.(paramPickerDoneMsg)
	if !ok {
		t.Fatalf("expected paramPickerDoneMsg, got %T", msg)
	}
	if done.cancelled || done.key != "threads" {
		t.Fatalf("got %+v", done)
	}
}

// TestQuitOverlayPreservesBackground renders a synthetic background and
// asserts that overlayCenter keeps the background characters that lie
// outside the popup box. Guards against regressions of the
// "lipgloss.Place wipes the screen" UX problem. The popup IS allowed to
// overwrite the columns it occupies — what we care about is that the
// rest of each background row survives.
func TestQuitOverlayPreservesBackground(t *testing.T) {
	// 30-col-wide background; a "LL" marker sits at the far left and
	// "RR" at the far right of every row so we can detect either side
	// being clobbered.
	bg := strings.Join([]string{
		"LL........................RR",
		"LL........................RR",
		"LL........................RR",
	}, "\n")
	popup := strings.Join([]string{
		"┌──────┐",
		"│ MENU │",
		"└──────┘",
	}, "\n")
	out := overlayCenter(bg, popup, 30, 3)
	for _, want := range []string{"LL", "RR", "MENU"} {
		if strings.Count(out, want) < 1 {
			t.Errorf("overlay output missing %q; got:\n%s", want, out)
		}
	}
	// Each background row's LL/RR should still survive on the same row.
	for i, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "LL") {
			t.Errorf("row %d lost left edge: %q", i, line)
		}
		if !strings.HasSuffix(line, "RR") {
			t.Errorf("row %d lost right edge: %q", i, line)
		}
	}
}

// TestConfigModeArrowCyclesPanes verifies left/right (and h/l) cycle
// pane focus the same way Tab / Shift+Tab do — the user explicitly
// asked for arrow navigation.
func TestConfigModeArrowCyclesPanes(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 140, Height: 40},
		keyMsg("c"),
	)
	if root.configMod == nil {
		t.Fatal("config mode not opened")
	}
	if root.configMod.focus != FocusModels {
		t.Fatalf("initial focus = %d, want FocusModels", root.configMod.focus)
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRight})
	if root.configMod.focus != FocusPresets {
		t.Errorf("after Right: focus = %d, want FocusPresets", root.configMod.focus)
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyRight})
	if root.configMod.focus != FocusParams {
		t.Errorf("after Right Right: focus = %d, want FocusParams", root.configMod.focus)
	}
	driveRoot(t, root, tea.KeyMsg{Type: tea.KeyLeft})
	if root.configMod.focus != FocusPresets {
		t.Errorf("after Left: focus = %d, want FocusPresets", root.configMod.focus)
	}
}

// TestFirstRunWindowSizeDoesNotPanic guards the regression reported in
// v0.1.0: when llamaman launched without a config, the first WindowSize
// message reached an uninitialized SelectionMode and crashed inside
// bubbles/list.updatePagination on a zero-value list.Model.
func TestFirstRunWindowSizeDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("first-run WindowSizeMsg panicked: %v", r)
		}
	}()
	fr := NewFirstRunMode("/tmp/nonexistent/config.json")
	root := NewRootForFirstRun("/tmp/nonexistent/config.json", "v0.0.0-test", fr)
	// Send a sequence of messages that exercise every uninitialized
	// sub-model path: window size, session tick, then more sizing.
	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		sessionTickMsg{},
		tea.WindowSizeMsg{Width: 80, Height: 24},
	)
}

// repoRoot walks up from CWD until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}
