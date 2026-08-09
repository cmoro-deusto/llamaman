package tui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/hf"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// View enumerates the top-level TUI screens.
type View int

const (
	ViewMain View = iota
	ViewRun
	ViewConfig
	ViewFirstRun
	ViewSettings
	ViewStorage
	ViewBrowser
)

// SpawnRequestMsg asks the root to spawn llama-server for (Model, Preset)
// and transition to run mode. Main mode emits this on Enter.
type SpawnRequestMsg struct {
	Model  config.Model
	Preset config.Preset
}

// RouterSpawnRequestMsg asks the root to spawn llama-server in router
// mode for a my-models.ini file (one process hosting every model in the
// file). Main mode's Router view emits this on Enter.
type RouterSpawnRequestMsg struct {
	File string
}

// returnToMainMsg is dispatched when the user backs out of run mode.
type returnToMainMsg struct{}

// reattachRequestMsg asks the root to drop into run mode by adopting the
// currently-running session (if any). Main mode emits this on `a`.
type reattachRequestMsg struct{}

// Spawner is the bridge between the TUI and the side effects it can't
// handle on its own (translate, session lock, fork/exec). main.go
// implements this; the TUI only knows it can ask for a launched Process.
type Spawner interface {
	Spawn(model config.Model, preset config.Preset) (RunModeOpts, error)
	// SpawnRouter launches llama-server in router mode for a my-models.ini
	// file (one process hosting every model in the file).
	SpawnRouter(file string) (RunModeOpts, error)
	// Reattach inspects session.json. If a session is live, returns
	// RunModeOpts that adopt it. Returns (nil, nil) when no session is
	// running. Errors propagate to the caller's error path.
	Reattach() (*RunModeOpts, error)
	// RunningAlias returns the alias of the currently running session
	// (if any), used by main mode for the inline list's `(running)`
	// marker and the "▶ Detached" line. Empty string when no session.
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
	theme     Theme
	mainMode  MainMode
	run       *RunMode
	configMod *ConfigMode
	firstRun  *FirstRunMode
	settings  *SettingsMode
	storage   *StorageMode
	dlEngine  downloadEngine
	browser   *BrowserMode

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
	theme, resolved, ok := ResolveTheme(cfg.Prefs().Theme, lipgloss.HasDarkBackground())
	if !ok {
		slog.Warn("unknown theme, falling back to auto", "theme", cfg.Prefs().Theme, "resolved", resolved)
	}
	r := &Root{
		cfg:        cfg,
		cfgPath:    cfgPath,
		spawner:    spawner,
		registry:   registry,
		version:    version,
		keys:       DefaultKeymap(),
		theme:      theme,
		mainMode:   NewMainMode(cfg, version, theme),
		initialRun: initialRun,
	}
	r.mainMode.SetCfgPath(cfgPath)
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
		opts.CfgPath = r.cfgPath // enable run-mode preference quick keys (§15.3)
		r.initialRun = nil
		run, cmd, err := NewRunMode(opts, r.theme)
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
		if r.run != nil {
			r.run.SetSize(msg.Width, msg.Height)
		}
		if r.configMod != nil {
			r.configMod.SetSize(msg.Width, msg.Height)
		}
		if r.firstRun != nil {
			r.firstRun.SetSize(msg.Width, msg.Height)
		}
		if r.settings != nil {
			r.settings.SetSize(msg.Width, msg.Height)
		}
		if r.storage != nil {
			r.storage.SetSize(msg.Width, msg.Height)
		}
		if r.browser != nil {
			r.browser.SetSize(msg.Width, msg.Height)
		}
		return r, nil

	case FirstRunCompletedMsg:
		return r.handleFirstRunCompleted(msg)
	case FirstRunQuitMsg:
		r.quitting = true
		return r, tea.Quit

	case sessionTickMsg:
		r.refreshSessionState()
		r.refreshDlStatusLine()
		return r, tickSession()

	case dlMainTickMsg:
		if r.view != ViewMain || r.storage == nil || !r.storage.hasRunning() {
			r.refreshDlStatusLine()
			return r, nil
		}
		r.storage.spinner, _ = r.storage.spinner.Update(r.storage.spinner.Tick())
		r.refreshDlStatusLine()
		interval := r.storage.spinner.Spinner.FPS
		if interval <= 0 {
			interval = 100 * time.Millisecond
		}
		return r, tea.Tick(interval, func(time.Time) tea.Msg { return dlMainTickMsg{} })

	case SpawnRequestMsg:
		return r.handleSpawn(msg)

	case RouterSpawnRequestMsg:
		return r.handleRouterSpawn(msg)

	case reattachRequestMsg:
		return r.handleReattach()

	case returnToMainMsg:
		// Land the main menu on the mode the ended session belonged to:
		// a killed router returns to Router mode even when it was
		// reattached from Single mode (and vice versa).
		if r.run != nil {
			if r.run.IsRouter() {
				r.mainMode.SetMode(modeRouter)
			} else {
				r.mainMode.SetMode(modeSingle)
			}
		}
		r.run = nil
		r.view = ViewMain
		r.refreshSessionState()
		return r, nil

	case returnFromConfigMsg:
		r.applyConfigChanges()
		r.configMod = nil
		r.view = ViewMain
		return r, nil

	case returnFromSettingsMsg:
		if r.settings != nil && r.settings.Applied() != nil {
			r.applyPreferences(r.settings.Applied())
		}
		r.settings = nil
		r.view = ViewMain
		return r, nil

	case returnFromStorageMsg:
		// the download (if any) keeps running; Main surfaces its status
		r.view = ViewMain
		r.refreshDlStatusLine()
		return r, r.armDlMainTick()

	case returnFromBrowserMsg:
		r.view = ViewMain
		r.refreshDlStatusLine() // a download may be running while browsing
		return r, nil

	case browserConfigHandoffMsg:
		return r.openConfig(configEntry{openNewModel: true, prefillHF: msg.id})

	case browserDownloadHandoffMsg:
		return r.handleBrowserDownloadHandoff(msg)

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
	r.mainMode = NewMainMode(msg.Cfg, r.version, r.theme)
	r.mainMode.SetCfgPath(r.cfgPath)
	r.mainMode.SetSize(r.width, r.height)
	cm := NewConfigMode(msg.CfgPath, msg.Cfg, r.theme)
	cm.SetRegistry(r.registry)
	cm.SetHFCheckRunner(r.hfCheckRunner())
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
		case "c":
			return r.openConfig(configEntry{focus: FocusModels})
		case "s":
			return r.openStorage()
		case "b":
			return r.openBrowser()
		case "p":
			return r.openSettings()
		case "t":
			r.cycleTheme(+1)
			return r, nil
		case "T": // shift+t — cycle backward
			r.cycleTheme(-1)
			return r, nil
		case "a":
			if r.mainMode.IsSessionRunning() {
				return r.handleReattach()
			}
			return r, nil
		}
		// Everything else (Enter, ↑/↓, Esc, ?) is owned by MainMode now
		// that the model selection list is embedded in the landing
		// screen.
		next, cmd := r.mainMode.Update(msg)
		r.mainMode = next
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
	case ViewSettings:
		if r.settings == nil {
			return r, nil
		}
		next, cmd := r.settings.Update(msg)
		r.settings = next
		return r, cmd
	case ViewStorage:
		if r.storage == nil {
			return r, nil
		}
		next, cmd := r.storage.Update(msg)
		r.storage = next
		return r, cmd
	case ViewBrowser:
		if r.browser == nil {
			return r, nil
		}
		next, cmd := r.browser.Update(msg)
		r.browser = next
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
	case ViewSettings:
		if r.settings == nil {
			return r, nil
		}
		next, cmd := r.settings.Update(msg)
		r.settings = next
		return r, cmd
	case ViewStorage:
		if r.storage == nil {
			return r, nil
		}
		next, cmd := r.storage.Update(msg)
		r.storage = next
		return r, cmd
	case ViewBrowser:
		if r.browser == nil {
			return r, nil
		}
		next, cmd := r.browser.Update(msg)
		r.browser = next
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
	return r.enterRunMode(opts)
}

// handleRouterSpawn mirrors handleSpawn for RouterSpawnRequestMsg: it
// asks the spawner to launch router mode and transitions to run mode.
func (r *Root) handleRouterSpawn(msg RouterSpawnRequestMsg) (tea.Model, tea.Cmd) {
	if r.spawner == nil {
		r.flashSpawnError(errSpawnerMissing)
		return r, nil
	}
	opts, err := r.spawner.SpawnRouter(msg.File)
	if err != nil {
		r.flashSpawnError(err)
		return r, nil
	}
	return r.enterRunMode(opts)
}

// enterRunMode wraps an already-spawned process in a RunMode and flips
// the view. Shared by the single-model and router spawn paths.
func (r *Root) enterRunMode(opts RunModeOpts) (tea.Model, tea.Cmd) {
	opts.CfgPath = r.cfgPath // enable run-mode preference quick keys (§15.3)
	run, cmd, err := NewRunMode(opts, r.theme)
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
	if r.view == ViewMain {
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
	prefillHF    string // §16.7 browser hand-off: seeds the new-model form's HF id
}

func (r *Root) openConfig(entry configEntry) (tea.Model, tea.Cmd) {
	cm := NewConfigMode(r.cfgPath, r.cfg, r.theme)
	cm.SetRegistry(r.registry)
	cm.SetHFCheckRunner(r.hfCheckRunner())
	cm.prefillHF = entry.prefillHF
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
	opts.CfgPath = r.cfgPath // enable run-mode preference quick keys (§15.3)
	run, cmd, err := NewRunMode(*opts, r.theme)
	if err != nil {
		r.startErr = err
		return r, nil
	}
	run.SetSize(r.width, r.height)
	r.run = run
	r.view = ViewRun
	return r, cmd
}

// refreshSessionState polls session.json via the spawner so main mode
// can show the (running) marker on the inline list and the "▶ Detached"
// line.
func (r *Root) refreshSessionState() {
	if r.spawner == nil {
		return
	}
	alias, preset, port := r.spawner.RunningAlias()
	r.mainMode.SetRunning(alias, preset, port)
}

// applyConfigChanges replaces the in-memory config with the saved
// snapshot from the editor and rebuilds main mode so the embedded
// selection list reflects the new config without losing per-mode state
// (running session, flash) that lives on the existing MainMode.
func (r *Root) applyConfigChanges() {
	if r.configMod == nil {
		return
	}
	saved := r.configMod.Saved()
	if saved != nil {
		r.cfg = saved
		r.mainMode.SetCfg(saved)
		r.mainMode.SetSize(r.width, r.height)
	}
}

// returnFromSettingsMsg is dispatched when the user backs out of the
// Settings mode (submit or cancel).
type returnFromSettingsMsg struct{}

// openSettings switches to the Settings mode, which edits exactly the
// preferences object (DESIGN §15.1).
// SetDownloadEngine attaches the download engine used by the Storage
// manager (DESIGN §16.4). Tests inject a stub; the default is a real
// *hf.Client built lazily in openStorage.
func (r *Root) SetDownloadEngine(e downloadEngine) { r.dlEngine = e }

// hfCheckRunner returns the §16.6 typed-repo check runner for the
// config editor: a real *hf.Client-backed adapter when the env is
// usable, nil otherwise (the check is then disabled, P3 — the model
// form advances exactly as before).
func (r *Root) hfCheckRunner() hfCheckRunner {
	if c, err := hf.New(); err == nil {
		return hfCheckClient{c}
	}
	return nil
}

// browserRunner returns the §16.7 browser runner (search + repo check):
// the same lazy, nil-safe pattern as hfCheckRunner.
func (r *Root) browserRunner() browserRunner {
	if c, err := hf.New(); err == nil {
		return browserClient{c}
	}
	return nil
}

// openBrowser switches to the HF browser. A live browser is reused —
// leaving with Esc keeps the query, results, and loaded quants, and
// re-entering shows them again (the §16.4 re-entry discipline).
func (r *Root) openBrowser() (tea.Model, tea.Cmd) {
	if r.cfg == nil {
		return r, nil
	}
	if r.browser == nil {
		root, err := storage.CacheRoot(r.cfg.Prefs().ModelsDir)
		if err != nil {
			root = "" // (cached) markers disabled (P3 — never a block)
		}
		b := NewBrowserMode(r.theme, root)
		b.SetBrowserRunner(r.browserRunner())
		r.browser = &b
	}
	r.browser.SetSize(r.width, r.height)
	r.browser.flash = "" // no stale announcements on re-entry
	r.mainMode.SetStatusLine("")
	r.view = ViewBrowser
	return r, nil
}

// handleBrowserDownloadHandoff lands a browser hand-off in the Storage
// manager: downloads stay in the manager — the single place downloads
// are managed (§16.4) — so Root reopens it and starts the download
// there (a quant is always present: bare ids never offer download).
func (r *Root) handleBrowserDownloadHandoff(msg browserDownloadHandoffMsg) (tea.Model, tea.Cmd) {
	if r.cfg == nil {
		return r, nil
	}
	if _, _ = r.openStorage(); r.storage == nil {
		return r, nil
	}
	repo, quant := splitRepoQuant(msg.id)
	if quant == "" {
		r.storage.flash = "download needs a quant — pick one in the browser"
		return r, r.storage.tickCmd()
	}
	r.storage.flash = ""
	r.storage.startDownload(repo, quant)
	r.storage.rebuild()
	r.storage.focusDownloadRow()
	return r, r.storage.tickCmd()
}

// openStorage switches to the Storage & Downloads manager. A live
// manager is reused — leaving with Esc keeps any running download
// alive, and re-entering shows it again (owner flow).
func (r *Root) openStorage() (tea.Model, tea.Cmd) {
	if r.cfg == nil {
		return r, nil
	}
	if r.storage == nil {
		root, err := storage.CacheRoot(r.cfg.Prefs().ModelsDir)
		if err != nil {
			r.mainMode.SetFlash("could not resolve cache root: " + err.Error())
			return r, nil
		}
		sm := NewStorageMode(r.cfg, r.theme, root)
		sm.cfgPath = r.cfgPath
		if r.dlEngine != nil {
			sm.SetEngine(r.dlEngine)
		} else if c, err := hf.New(); err == nil {
			sm.SetEngine(c)
		}
		r.storage = sm
	}
	r.storage.SetSize(r.width, r.height)
	r.storage.flash = "" // no stale announcements on re-entry
	r.storage.rebuild()
	if len(r.storage.downloads) > 0 {
		// re-entry mid-download: resume the progress tick and land the
		// cursor on a download row (pause/cancel one Enter away).
		r.storage.focusDownloadRow()
	}
	r.mainMode.SetStatusLine("")
	r.view = ViewStorage
	return r, r.storage.tickCmd()
}

// refreshDlStatusLine surfaces an in-flight download on the Main screen
// so leaving the manager never orphans it invisibly (owner flow).
func (r *Root) refreshDlStatusLine() {
	if r.storage == nil || len(r.storage.downloads) == 0 {
		r.mainMode.SetStatusLine("")
		return
	}
	var running, paused, failed, done []string
	for _, d := range r.storage.downloads {
		// cancelled downloads are marked dlDone for auto-dismiss but
		// must never surface as "downloaded" (owner report).
		if d.discard {
			continue
		}
		name := d.repo + ":" + d.quant
		switch d.status {
		case dlRunning:
			running = append(running, name)
		case dlPaused:
			paused = append(paused, name)
		case dlFailed:
			failed = append(failed, name)
		case dlDone:
			done = append(done, name)
		}
	}
	var label string
	join := func(ns []string) string {
		j := strings.Join(ns, ", ")
		if len(j) > 48 {
			j = j[:48] + "…"
		}
		return j
	}
	switch {
	case len(running) == 1:
		label = r.storage.spinner.View() + " downloading " + running[0]
	case len(running) > 1:
		label = fmt.Sprintf("%s %d downloads: %s", r.storage.spinner.View(), len(running), join(running))
	case len(paused) > 0:
		label = "⏸ download paused: " + join(paused)
	case len(failed) > 0:
		label = "✕ download failed: " + join(failed)
	case len(done) > 0:
		label = "⬇ download finished: " + join(done)
	default:
		r.mainMode.SetStatusLine("")
		return
	}
	r.mainMode.SetStatusLine(label + " — s to view")
}

// armDlMainTick starts the Main spinner animation while a download is
// running; call it when leaving the storage view.
func (r *Root) armDlMainTick() tea.Cmd {
	if r.view == ViewMain && r.storage != nil && r.storage.hasRunning() {
		return func() tea.Msg { return dlMainTickMsg{} }
	}
	return nil
}

func (r *Root) openSettings() (tea.Model, tea.Cmd) {
	r.settings = NewSettingsMode(r.cfgPath, r.cfg, r.theme, lipgloss.HasDarkBackground(), r.version)
	r.settings.SetSize(r.width, r.height)
	r.view = ViewSettings
	return r, r.settings.Init()
}

// applyPreferences persists the preferences snapshot from Settings and
// re-resolves the theme so the TUI re-renders with the new palette. On
// save failure the in-memory config is left untouched (no memory/disk
// divergence).
func (r *Root) applyPreferences(p *config.Preferences) {
	if p == nil || r.cfgPath == "" {
		return
	}
	prev := r.cfg.Preferences
	r.cfg.Preferences = p
	if err := config.Save(r.cfgPath, r.cfg); err != nil {
		r.cfg.Preferences = prev
		slog.Error("save preferences", "err", err)
		r.mainMode.SetFlash("could not save preferences: " + err.Error())
		return
	}
	r.applyTheme()
}

// cycleTheme steps the theme cycle forward (+1) or backward (-1),
// persists preferences.theme, and re-renders live. The quick key is a
// shortcut writing the same object Settings edits (P8).
func (r *Root) cycleTheme(dir int) {
	if r.cfg == nil || r.cfgPath == "" {
		return
	}
	next := nextTheme(r.cfg.Prefs().Theme, dir)
	prefs := r.cfg.Prefs()
	prefs.Theme = next
	prev := r.cfg.Preferences
	r.cfg.Preferences = &prefs
	if err := config.Save(r.cfgPath, r.cfg); err != nil {
		// Roll back: never leave memory/disk/UI diverged.
		r.cfg.Preferences = prev
		slog.Error("save theme", "err", err)
		r.mainMode.SetFlash("could not save theme: " + err.Error())
		return
	}
	r.applyTheme()
	display := next
	if p, ok := lookupPalette(next); ok {
		display = p.Display
	}
	flash := "theme: " + display
	if w := mismatchWarning(next, lipgloss.HasDarkBackground()); w != "" {
		flash = "⚠ " + w + " — " + flash
	}
	r.mainMode.SetFlash(flash)
}

// applyTheme re-resolves the theme from preferences and pushes it into
// main mode (the only mode alive while Settings/the cycle run). A
// background-mismatched palette surfaces as a Main-mode flash warning
// (the user picked it explicitly, so it applies — DESIGN §15.1).
func (r *Root) applyTheme() {
	theme, resolved, ok := ResolveTheme(r.cfg.Prefs().Theme, lipgloss.HasDarkBackground())
	if !ok {
		slog.Warn("unknown theme, falling back to auto", "theme", r.cfg.Prefs().Theme, "resolved", resolved)
	}
	r.theme = theme
	r.mainMode.SetTheme(theme)
	if w := mismatchWarning(r.cfg.Prefs().Theme, lipgloss.HasDarkBackground()); w != "" {
		r.mainMode.SetFlash("⚠ " + w)
	}
}

func (r *Root) View() string {
	if r.quitting {
		return ""
	}
	switch r.view {
	case ViewMain:
		return r.mainMode.View()
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
	case ViewSettings:
		if r.settings == nil {
			return ""
		}
		return r.settings.View()
	case ViewStorage:
		if r.storage == nil {
			return ""
		}
		return r.storage.View()
	case ViewBrowser:
		if r.browser == nil {
			return ""
		}
		return r.browser.View()
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

// sessionTickMsg fires every few seconds so main mode can refresh
// the (running) marker and the "▶ Detached" line if another process
// changes the session state out-of-band.
type sessionTickMsg struct{}

// dlMainTickMsg advances the download spinner shown in Main's status
// line while a download runs (the storage tick does not run there).
type dlMainTickMsg struct{}

func tickSession() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return sessionTickMsg{} })
}
