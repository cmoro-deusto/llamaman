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
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// stubSpawner satisfies the Spawner interface for tests where we don't
// want to actually fork llama-server.
type stubSpawner struct{ runningAlias string }

func (s stubSpawner) Spawn(config.Model, config.Preset) (RunModeOpts, error) {
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

	for _, want := range []string{
		"llamaman v0.0.0-test",
		"select model",
		"configure",
		"help",
		"quit",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("main mode output missing %q\nout:\n%s", want, out)
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

func TestSnapshotSelectionMode(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("s"),
	)

	for _, want := range []string{"alpha", "beta", "preset"} {
		if !strings.Contains(out, want) {
			t.Errorf("selection output missing %q\nout:\n%s", want, out)
		}
	}
}

func TestSnapshotSelectionShowsRunningMarker(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{runningAlias: "alpha"}, nil, "v0.0.0-test", nil)
	root.refreshSessionState()

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("s"),
	)
	if !strings.Contains(out, "(running)") {
		t.Errorf("expected (running) marker; out:\n%s", out)
	}
}

func TestSnapshotSelectionPivotsToPresetSubList(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)

	// 'beta' has 2 presets; navigate to it then Enter to open sub-list.
	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("s"),
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyEnter},
	)

	// Sub-list shows preset names, including the second one ("smallctx").
	if !strings.Contains(out, "smallctx") {
		t.Errorf("preset sub-list should show smallctx; out:\n%s", out)
	}
	if !strings.Contains(out, "Presets — beta") {
		t.Errorf("preset sub-list should mention parent alias; out:\n%s", out)
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

	// Send WindowSizeMsg so SetSize wires up the viewport.
	root.Update(tea.WindowSizeMsg{Width: 140, Height: 40})

	// Wait for the fakeserver to print the readiness line and feed it
	// into our buffer.
	deadline := time.After(3 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("never saw readiness in run mode buffer; current view:\n%s", stripANSI(root.View()))
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
			if strings.Contains(root.run.buf.String(), "server is listening") {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}

	out := stripANSI(root.View())
	for _, want := range []string{"alpha", "127.0.0.1:9080", "ready"} {
		if !strings.Contains(out, want) {
			t.Errorf("run mode output missing %q\nout:\n%s", want, out)
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
func (failingSpawner) Reattach() (*RunModeOpts, error)     { return nil, nil }
func (failingSpawner) RunningAlias() (string, string, int) { return "", "", 0 }

type failingSpawnError struct{ msg string }

func (e failingSpawnError) Error() string { return e.msg }

// TestSpawnFailureFlashesInSelectionMode reproduces "I press Enter on my
// model and nothing happens" when llama-server isn't installed — every
// failure now lands in the selection-mode flash so the user sees the
// underlying message.
func TestSpawnFailureFlashesInSelectionMode(t *testing.T) {
	cfg := sampleSnapshotConfig()
	root := NewRoot(cfg, "/dev/null", failingSpawner{msg: "fork/exec /usr/bin/llama-server: no such file or directory"}, nil, "v0.0.0-test", nil)

	out := driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("s"),
		tea.KeyMsg{Type: tea.KeyEnter},
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

	// Press `k` directly (no quit prompt). Expect transition to main.
	driveRoot(t, root, keyMsg("k"))
	if root.view != ViewMain {
		t.Fatalf("after k: view = %d, want ViewMain (%d)", root.view, ViewMain)
	}
	// Process should have been signaled.
	select {
	case <-proc.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("k did not stop the child")
	}
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
		"threads",                    // bare name (no --)
		"number of CPU threads",      // description
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
