package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// View enumerates the top-level TUI screens.
type View int

const (
	ViewMain View = iota
	ViewSelection
	ViewRun
	ViewConfig
	ViewFirstRun
)

// SpawnRequestMsg asks the root to spawn llama-server for (Model, Preset)
// and transition to run mode. Selection mode emits this on Enter.
type SpawnRequestMsg struct {
	Model  config.Model
	Preset config.Preset
}

// returnToMainMsg is dispatched when the user backs out of run mode.
type returnToMainMsg struct{}

// editFromSelectionMsg requests opening config mode focused on the chosen
// model (Presets pane). Selection mode emits this on `e`.
type editFromSelectionMsg struct {
	Alias string
}

// editPresetFromSelectionMsg requests opening config mode focused on a
// specific preset's parameter editor (Params pane). The preset sub-list
// emits this on `e`.
type editPresetFromSelectionMsg struct {
	Alias  string
	Preset string
}

// newModelFromSelectionMsg requests opening config mode in the new-model
// flow. Selection mode emits this on `n`.
type newModelFromSelectionMsg struct{}

// reattachRequestMsg asks the root to drop into run mode by adopting the
// currently-running session (if any). Main mode emits this on `a`.
type reattachRequestMsg struct{}

// Spawner is the bridge between the TUI and the side effects it can't
// handle on its own (translate, session lock, fork/exec). main.go
// implements this; the TUI only knows it can ask for a launched Process.
type Spawner interface {
	Spawn(model config.Model, preset config.Preset) (RunModeOpts, error)
	// Reattach inspects session.json. If a session is live, returns
	// RunModeOpts that adopt it. Returns (nil, nil) when no session is
	// running. Errors propagate to the caller's error path.
	Reattach() (*RunModeOpts, error)
	// RunningAlias returns the alias of the currently running session
	// (if any), used by selection mode for the `(running)` marker and by
	// main mode for the "▶ Detached" line. Empty string when no session.
	RunningAlias() (alias, preset string, port int)
}

// Root is the top-level Bubble Tea model. It owns each sub-model and
// dispatches messages.
type Root struct {
	cfg      *config.Config
	cfgPath  string
	spawner  Spawner
	version  string
	registry flags.Registry
	keys     Keymap

	view      View
	width     int
	height    int
	mainMode  MainMode
	selection SelectionMode
	run       *RunMode
	configMod *ConfigMode
	firstRun  *FirstRunMode

	// initialRun, if non-nil, makes the program jump straight to run mode
	// on Init() (used both for `llamaman <alias>` and for reattach).
	initialRun *RunModeOpts

	quitting bool
	startErr error
}

// NewRootForFirstRun builds a Root that opens directly into the first-run
// flow. The cfg/cfgPath are filled in by FirstRunCompletedMsg.
func NewRootForFirstRun(cfgPath string, version string, fr *FirstRunMode) *Root {
	return &Root{
		cfgPath:  cfgPath,
		version:  version,
		keys:     DefaultKeymap(),
		firstRun: fr,
		view:     ViewFirstRun,
	}
}

// NewRoot constructs the orchestrator. cfgPath is the on-disk location for
// saves; pass an empty string only if save is unreachable from this run.
// registry may be nil to disable the type-aware param editor and fuzzy
// flag picker.
func NewRoot(cfg *config.Config, cfgPath string, spawner Spawner, registry flags.Registry, version string, initialRun *RunModeOpts) *Root {
	r := &Root{
		cfg:        cfg,
		cfgPath:    cfgPath,
		spawner:    spawner,
		registry:   registry,
		version:    version,
		keys:       DefaultKeymap(),
		mainMode:   NewMainMode(cfg, version),
		selection:  NewSelectionMode(cfg),
		initialRun: initialRun,
	}
	r.selection.SetCfgPath(cfgPath)
	if initialRun != nil {
		r.view = ViewRun
	}
	return r
}

// SetRegistry updates the registry post-construction. Used by first-run
// when a fresh config has just been written and the parsed-help flow
// runs only afterward.
func (r *Root) SetRegistry(reg flags.Registry) { r.registry = reg }

func (r *Root) Init() tea.Cmd {
	if r.firstRun != nil {
		return r.firstRun.Init()
	}
	if r.initialRun != nil {
		opts := *r.initialRun
		r.initialRun = nil
		run, cmd, err := NewRunMode(opts)
		if err != nil {
			r.startErr = err
			r.view = ViewMain
			return nil
		}
		r.run = run
		return cmd
	}
	r.refreshSessionState()
	return tickSession()
}

func (r *Root) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		r.width, r.height = msg.Width, msg.Height
		r.mainMode.SetSize(msg.Width, msg.Height)
		r.selection.SetSize(msg.Width, msg.Height)
		if r.run != nil {
			r.run.SetSize(msg.Width, msg.Height)
		}
		if r.configMod != nil {
			r.configMod.SetSize(msg.Width, msg.Height)
		}
		if r.firstRun != nil {
			r.firstRun.SetSize(msg.Width, msg.Height)
		}
		return r, nil

	case FirstRunCompletedMsg:
		return r.handleFirstRunCompleted(msg)
	case FirstRunQuitMsg:
		r.quitting = true
		return r, tea.Quit

	case sessionTickMsg:
		r.refreshSessionState()
		return r, tickSession()

	case SpawnRequestMsg:
		return r.handleSpawn(msg)

	case editFromSelectionMsg:
		return r.openConfig(configEntry{focusAlias: msg.Alias, focus: FocusPresets})

	case editPresetFromSelectionMsg:
		return r.openConfig(configEntry{focusAlias: msg.Alias, focusPreset: msg.Preset, focus: FocusParams})

	case newModelFromSelectionMsg:
		return r.openConfig(configEntry{focus: FocusModels, openNewModel: true})

	case reattachRequestMsg:
		return r.handleReattach()

	case returnToMainMsg:
		r.run = nil
		r.view = ViewMain
		r.refreshSessionState()
		return r, nil

	case returnFromConfigMsg:
		r.applyConfigChanges()
		r.configMod = nil
		r.view = ViewMain
		return r, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" && r.view != ViewRun && r.view != ViewConfig {
			r.quitting = true
			return r, tea.Quit
		}
		return r.routeKey(msg)
	}
	return r.forward(msg)
}

func (r *Root) handleFirstRunCompleted(msg FirstRunCompletedMsg) (tea.Model, tea.Cmd) {
	r.cfg = msg.Cfg
	r.cfgPath = msg.CfgPath
	r.firstRun = nil
	r.mainMode = NewMainMode(msg.Cfg, r.version)
	r.mainMode.SetSize(r.width, r.height)
	r.selection = NewSelectionMode(msg.Cfg)
	r.selection.SetCfgPath(msg.CfgPath)
	r.selection.SetSize(r.width, r.height)
	cm := NewConfigMode(msg.CfgPath, msg.Cfg)
	cm.SetRegistry(r.registry)
	cm.ShowFirstRunBanner()
	cm.SetSize(r.width, r.height)
	r.configMod = &cm
	r.view = ViewConfig
	return r, nil
}

func (r *Root) routeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch r.view {
	case ViewMain:
		switch msg.String() {
		case "q":
			r.quitting = true
			return r, tea.Quit
		case "s", "enter":
			r.view = ViewSelection
			return r, nil
		case "c":
			return r.openConfig(configEntry{focus: FocusModels})
		case "a":
			if r.mainMode.IsSessionRunning() {
				return r.handleReattach()
			}
			return r, nil
		}
		// Forward ? and any other unclaimed keys to MainMode (help toggle).
		next, cmd := r.mainMode.Update(msg)
		r.mainMode = next
		return r, cmd
	case ViewSelection:
		next, cmd := r.selection.Update(msg)
		r.selection = next
		return r, cmd
	case ViewRun:
		if r.run == nil {
			return r, nil
		}
		next, cmd := r.run.Update(msg)
		r.run = next
		return r, cmd
	case ViewConfig:
		if r.configMod == nil {
			return r, nil
		}
		next, cmd := r.configMod.Update(msg)
		r.configMod = next
		return r, cmd
	case ViewFirstRun:
		if r.firstRun == nil {
			return r, nil
		}
		next, cmd := r.firstRun.Update(msg)
		r.firstRun = next
		return r, cmd
	}
	return r, nil
}

func (r *Root) forward(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch r.view {
	case ViewSelection:
		next, cmd := r.selection.Update(msg)
		r.selection = next
		return r, cmd
	case ViewRun:
		if r.run == nil {
			return r, nil
		}
		next, cmd := r.run.Update(msg)
		r.run = next
		return r, cmd
	case ViewConfig:
		if r.configMod == nil {
			return r, nil
		}
		next, cmd := r.configMod.Update(msg)
		r.configMod = next
		return r, cmd
	case ViewFirstRun:
		if r.firstRun == nil {
			return r, nil
		}
		next, cmd := r.firstRun.Update(msg)
		r.firstRun = next
		return r, cmd
	}
	return r, nil
}

func (r *Root) handleSpawn(msg SpawnRequestMsg) (tea.Model, tea.Cmd) {
	if r.spawner == nil {
		r.flashSpawnError(errSpawnerMissing)
		return r, nil
	}
	opts, err := r.spawner.Spawn(msg.Model, msg.Preset)
	if err != nil {
		r.flashSpawnError(err)
		return r, nil
	}
	run, cmd, err := NewRunMode(opts)
	if err != nil {
		r.flashSpawnError(err)
		return r, nil
	}
	run.SetSize(r.width, r.height)
	r.run = run
	r.view = ViewRun
	return r, cmd
}

// flashSpawnError records the error and forwards it to the visible
// sub-mode so the user actually sees what went wrong instead of the
// keypress feeling like a no-op (e.g. when llama-server isn't installed
// at the configured path, or the port is already bound).
func (r *Root) flashSpawnError(err error) {
	r.startErr = err
	msg := "spawn failed: " + err.Error()
	switch r.view {
	case ViewSelection:
		r.selection.SetFlash(msg)
	case ViewMain:
		r.mainMode.SetFlash(msg)
	}
}

// configEntry bundles the optional state for openConfig. The fields are
// all optional; defaults land on the Models pane with no model focused.
type configEntry struct {
	focusAlias   string
	focusPreset  string
	focus        ConfigFocus
	openNewModel bool
}

func (r *Root) openConfig(entry configEntry) (tea.Model, tea.Cmd) {
	cm := NewConfigMode(r.cfgPath, r.cfg)
	cm.SetRegistry(r.registry)
	cm.SetSize(r.width, r.height)
	if entry.focusAlias != "" {
		for i, m := range r.cfg.Models {
			if m.Alias == entry.focusAlias {
				cm.modelIdx = i
				if entry.focusPreset != "" {
					for j, p := range m.Presets {
						if p.Name == entry.focusPreset {
							cm.presetIdx = j
							break
						}
					}
				}
				break
			}
		}
	}
	cm.focus = entry.focus
	r.configMod = &cm
	r.view = ViewConfig
	if entry.openNewModel {
		// Defer running the form until the next tick so SetSize has been
		// applied first.
		return r, cm.openNewModelForm()
	}
	return r, nil
}

func (r *Root) handleReattach() (tea.Model, tea.Cmd) {
	if r.spawner == nil {
		return r, nil
	}
	opts, err := r.spawner.Reattach()
	if err != nil || opts == nil {
		// No session to attach to; refresh state and stay where we are.
		r.refreshSessionState()
		return r, nil
	}
	run, cmd, err := NewRunMode(*opts)
	if err != nil {
		r.startErr = err
		return r, nil
	}
	run.SetSize(r.width, r.height)
	r.run = run
	r.view = ViewRun
	return r, cmd
}

// refreshSessionState polls session.json via the spawner so main and
// selection modes can show the (running) marker and the "▶ Detached" line.
func (r *Root) refreshSessionState() {
	if r.spawner == nil {
		return
	}
	alias, preset, port := r.spawner.RunningAlias()
	r.mainMode.SetRunning(alias, preset, port)
	r.selection.SetRunningAlias(alias)
}

// applyConfigChanges replaces the in-memory config with the saved
// snapshot from the editor and refreshes downstream sub-models that hold
// derived state (selection list, etc.).
func (r *Root) applyConfigChanges() {
	if r.configMod == nil {
		return
	}
	saved := r.configMod.Saved()
	if saved != nil {
		r.cfg = saved
		r.selection = NewSelectionMode(saved)
		r.selection.SetSize(r.width, r.height)
		r.mainMode = NewMainMode(saved, r.version)
		r.mainMode.SetSize(r.width, r.height)
	}
}

func (r *Root) View() string {
	if r.quitting {
		return ""
	}
	switch r.view {
	case ViewMain:
		return r.mainMode.View()
	case ViewSelection:
		return r.selection.View()
	case ViewRun:
		if r.run == nil {
			return ""
		}
		return r.run.View()
	case ViewConfig:
		if r.configMod == nil {
			return ""
		}
		return r.configMod.View()
	case ViewFirstRun:
		if r.firstRun == nil {
			return ""
		}
		return r.firstRun.View()
	}
	return ""
}

// errSpawnerMissing is a sentinel for the impossible case of arriving at
// SpawnRequestMsg without a configured Spawner.
var errSpawnerMissing = spawnerMissingError{}

type spawnerMissingError struct{}

func (spawnerMissingError) Error() string { return "TUI: Spawner not configured" }

// sessionTickMsg fires every few seconds so main/selection can refresh
// the (running) marker and the "▶ Detached" line if another process
// changes the session state out-of-band.
type sessionTickMsg struct{}

func tickSession() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return sessionTickMsg{} })
}
