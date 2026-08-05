package main

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"time"

	"github.com/alecthomas/kong"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
	"github.com/cmoro-deusto/llamaman/internal/logging"
	"github.com/cmoro-deusto/llamaman/internal/modelsini"
	"github.com/cmoro-deusto/llamaman/internal/paths"
	"github.com/cmoro-deusto/llamaman/internal/server"
	"github.com/cmoro-deusto/llamaman/internal/translate"
	"github.com/cmoro-deusto/llamaman/internal/tui"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

type CLI struct {
	List       bool   `short:"l" name:"list" help:"List configured models and router sources, one per line."`
	Presets    bool   `short:"p" name:"presets" help:"Print presets for the given <alias>."`
	Config     string `short:"c" name:"config" placeholder:"PATH" help:"Path to alternate config file."`
	Ini        string `short:"i" name:"ini" placeholder:"PATH" help:"Run a my-models.ini file in router mode (llama-server --models-preset)."`
	Completion string `name:"completion" enum:"bash,zsh,fish," default:"" placeholder:"SHELL" help:"Print shell completion script (bash, zsh, fish)."`
	Version    bool   `name:"version" help:"Print version and exit."`

	Alias  string `arg:"" optional:"" help:"Model alias to launch."`
	Preset string `arg:"" optional:"" help:"Preset name within the alias."`
}

// importArgs / exportArgs are the argument sets of the `import` and
// `export` subcommands. They get their own kong parser (see
// runSubcommand) because kong v1 cannot mix positional args and branching
// cmd: args on one struct.
type importArgs struct {
	File   string `arg:"" name:"file" help:"Path to a my-models.ini file."`
	Config string `short:"c" name:"config" placeholder:"PATH" help:"Path to alternate config file."`
}

type exportArgs struct {
	Path   string `arg:"" optional:"" name:"path" help:"Output file path (default: stdout)."`
	Config string `short:"c" name:"config" placeholder:"PATH" help:"Path to alternate config file."`
}

// Exit codes from DESIGN.md §4.4.
const (
	exitOK          = 0
	exitGeneric     = 1
	exitConfigErr   = 2
	exitPrereqErr   = 3
	exitPortInUse   = 4
	exitInterrupted = 130
)

func main() {
	os.Exit(run())
}

func run() int {
	closer, _ := logging.Init()
	if closer != nil {
		defer closer.Close()
	}

	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("llamaman"),
		kong.Description(fmt.Sprintf("llamaman %s llama-server manager", versionString())),
		kong.UsageOnError(),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitGeneric
	}
	if _, err := parser.Parse(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitConfigErr
	}

	if cli.Version {
		fmt.Printf("llamaman %s (%s, %s)\n", versionString(), commit, date)
		return exitOK
	}

	slog.Debug("startup", "version", versionString(), "commit", commit, "args", os.Args[1:])

	// Subcommands operate without touching the TUI or the session manager.
	// They are dispatched before kong because kong v1 cannot mix positional
	// args and cmd: branches on one struct.
	if len(os.Args) > 1 && (os.Args[1] == "import" || os.Args[1] == "export") {
		return runSubcommand(os.Args[1], os.Args[2:])
	}

	// `-i <file>` runs a my-models.ini in router mode. It works without a
	// config (default globals) and skips the first-run flow.
	if cli.Ini != "" {
		return runRouterCLI(cli.Ini, cli.Config)
	}

	switch {
	case cli.Completion != "":
		return runCompletion(cli.Completion)
	case cli.List:
		return runList(cli.Config)
	case cli.Presets:
		return runPresets(cli.Config, cli.Alias)
	}

	cfg, cfgPath, code, missing := loadConfigOrFirstRun(cli.Config)
	if missing {
		// Default config absent and no -c override → first-run flow.
		fr := tui.NewFirstRunMode(cfgPath)
		root := tui.NewRootForFirstRun(cfgPath, versionString(), fr)
		if _, err := tea.NewProgram(root, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
			fmt.Fprintln(os.Stderr, "tui:", err)
			return exitGeneric
		}
		return exitOK
	}
	if code != 0 {
		return code
	}

	registry, registryReal := loadFlagRegistry(cfg.Globals.Bin)

	sessMgr, err := server.NewSessionManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "session manager:", err)
		return exitGeneric
	}

	initialRun, code := decideEntry(cfg, registry, sessMgr, cli.Alias, cli.Preset)
	if code != 0 {
		return code
	}

	root := tui.NewRoot(cfg, cfgPath, &liveSpawner{cfg: cfg, registry: registry, registryReal: registryReal, sessMgr: sessMgr}, registry, versionString(), initialRun)
	if _, err := tea.NewProgram(root, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		return exitGeneric
	}
	return exitOK
}

// decideEntry implements the dispatch table from DESIGN.md §4.3. Returns
// the initial run-mode opts (or nil for main mode), and an exit code if
// the invocation is bogus.
func decideEntry(cfg *config.Config, registry flags.Registry, sessMgr *server.SessionManager, alias, preset string) (*tui.RunModeOpts, int) {
	existing, err := sessMgr.Read()
	if err != nil {
		fmt.Fprintln(os.Stderr, "session read:", err)
		return nil, exitGeneric
	}
	if existing != nil {
		// A session is running. Both no-args and args+session paths
		// reattach. Args are silently ignored per §4.3.
		if alias != "" {
			slog.Info("session already running; ignoring positional args", "alias", alias, "preset", preset)
		}
		opts, err := buildReattachOpts(cfg, registry, *existing, sessMgr)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reattach:", err)
			return nil, exitGeneric
		}
		return opts, 0
	}
	if alias == "" {
		// No positional args, no session → main mode.
		return nil, 0
	}

	// Need to spawn. Validate alias+preset before acquiring the lock so
	// race losers don't pay the cost of spawning.
	model, ok := findModel(cfg, alias)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown model alias: %s\n", alias)
		return nil, exitConfigErr
	}
	chosen, ok := pickPreset(model, preset)
	if !ok {
		fmt.Fprintf(os.Stderr, "model %q has no preset named %q\n", alias, preset)
		return nil, exitConfigErr
	}
	opts, err := acquireAndSpawn(cfg, registry, sessMgr, model, chosen)
	if err != nil {
		if errors.Is(err, server.ErrAnotherStarter) {
			fmt.Fprintln(os.Stderr, "Another llamaman is already running")
			return nil, exitOK
		}
		var pErr ErrPortInUse
		if errors.As(err, &pErr) {
			fmt.Fprintln(os.Stderr, pErr.Underlying)
			return nil, exitPortInUse
		}
		fmt.Fprintln(os.Stderr, "spawn:", err)
		return nil, exitGeneric
	}
	return opts, 0
}

// ErrPortInUse is returned by acquireAndSpawn when the configured listen
// port can't be bound. The Spawner path surfaces this to the TUI; the CLI
// path translates it to exit code 4.
type ErrPortInUse struct {
	Underlying error
}

func (e ErrPortInUse) Error() string { return e.Underlying.Error() }
func (e ErrPortInUse) Unwrap() error { return e.Underlying }

// liveSpawner satisfies tui.Spawner. Spawn does the fork+session-write
// dance; Reattach + RunningAlias expose live session state to the TUI's
// main and selection screens.
type liveSpawner struct {
	cfg          *config.Config
	registry     flags.Registry
	registryReal bool // true when registry came from parsed --help output
	sessMgr      *server.SessionManager
}

func (l *liveSpawner) Spawn(model config.Model, preset config.Preset) (tui.RunModeOpts, error) {
	opts, err := acquireAndSpawn(l.cfg, l.registry, l.sessMgr, model, preset)
	if err != nil {
		return tui.RunModeOpts{}, err
	}
	return *opts, nil
}

func (l *liveSpawner) SpawnRouter(file string) (tui.RunModeOpts, error) {
	opts, err := acquireAndSpawnRouter(l.cfg, l.registry, l.registryReal, l.sessMgr, file)
	if err != nil {
		return tui.RunModeOpts{}, err
	}
	return *opts, nil
}

func (l *liveSpawner) Reattach() (*tui.RunModeOpts, error) {
	sess, err := l.sessMgr.Read()
	if err != nil || sess == nil {
		return nil, err
	}
	return buildReattachOpts(l.cfg, l.registry, *sess, l.sessMgr)
}

func (l *liveSpawner) RunningAlias() (string, string, int) {
	sess, err := l.sessMgr.Read()
	if err != nil || sess == nil {
		return "", "", 0
	}
	return sess.Alias, sess.Preset, sess.Port
}

// acquireAndSpawn races for the session lock, spawns llama-server (or
// reattaches if another starter beat us to writing session.json), and
// returns the RunModeOpts to feed into NewRunMode. Caller is responsible
// for surfacing race-loser errors (ErrAnotherStarter).
func acquireAndSpawn(cfg *config.Config, registry flags.Registry, sessMgr *server.SessionManager, model config.Model, preset config.Preset) (*tui.RunModeOpts, error) {
	existing, err := sessMgr.AcquireStart()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		// Another starter wrote session.json between our pre-check and
		// lock acquisition. Drop the lock and reattach.
		sessMgr.Unlock()
		return buildReattachOpts(cfg, registry, *existing, sessMgr)
	}

	res, err := translate.Build(cfg.Globals, model, preset, registry)
	if err != nil {
		sessMgr.Unlock()
		return nil, fmt.Errorf("build argv: %w", err)
	}
	if err := server.PortAvailable(cfg.Globals.Host, cfg.Globals.Port); err != nil {
		sessMgr.Unlock()
		return nil, ErrPortInUse{Underlying: err}
	}
	logPath, err := tui.LogFilePath()
	if err != nil {
		sessMgr.Unlock()
		return nil, err
	}
	proc, err := server.Spawn(res.Argv, logPath)
	if err != nil {
		sessMgr.Unlock()
		return nil, fmt.Errorf("server spawn: %w", err)
	}
	rec := server.Session{
		PID:       proc.Pid,
		Alias:     model.Alias,
		Preset:    preset.Name,
		Host:      cfg.Globals.Host,
		Port:      cfg.Globals.Port,
		StartedAt: proc.Started,
		Command:   res.Argv,
		LogPath:   logPath,
	}
	if err := sessMgr.WriteAndUnlock(&rec); err != nil {
		// The child is alive but we couldn't persist; kill it to keep
		// state consistent.
		proc.Stop(2 * time.Second)
		_ = proc.RemoveLog()
		return nil, fmt.Errorf("write session: %w", err)
	}
	return &tui.RunModeOpts{
		Cfg:        cfg,
		Model:      model,
		Preset:     preset,
		Argv:       res.Argv,
		Warnings:   res.Warnings,
		Process:    proc,
		SessionMgr: sessMgr,
		Registry:   registry,
		Fetcher:    fetcherFor(res.Argv, registry),
	}, nil
}

// acquireAndSpawnRouter is the router-mode counterpart of
// acquireAndSpawn: it validates the my-models.ini, version-gates the
// llama-server binary, races for the session lock, spawns llama-server
// with --models-preset <file>, and records a router-kind session.
func acquireAndSpawnRouter(cfg *config.Config, registry flags.Registry, registryReal bool, sessMgr *server.SessionManager, file string) (*tui.RunModeOpts, error) {
	// Validate the file up front so a typo'd path fails fast, before the
	// spawn dance.
	if _, err := modelsini.ParseFile(file); err != nil {
		return nil, fmt.Errorf("router models file: %w", err)
	}
	if registryReal {
		if _, ok := registry.Lookup("models-preset"); !ok {
			return nil, fmt.Errorf("llama-server at %s does not support --models-preset (model presets need a llama.cpp build from Dec 2025 or later)", cfg.Globals.Bin)
		}
	}

	existing, err := sessMgr.AcquireStart()
	if err != nil {
		return nil, err
	}
	if existing != nil {
		sessMgr.Unlock()
		return buildReattachOpts(cfg, registry, *existing, sessMgr)
	}

	res := translate.RouterBuild(cfg.Globals, file, registry)
	if err := server.PortAvailable(cfg.Globals.Host, cfg.Globals.Port); err != nil {
		sessMgr.Unlock()
		return nil, ErrPortInUse{Underlying: err}
	}
	logPath, err := tui.LogFilePath()
	if err != nil {
		sessMgr.Unlock()
		return nil, err
	}
	proc, err := server.Spawn(res.Argv, logPath)
	if err != nil {
		sessMgr.Unlock()
		return nil, fmt.Errorf("server spawn: %w", err)
	}
	rec := server.Session{
		PID:       proc.Pid,
		Alias:     file,
		Kind:      server.KindRouter,
		Host:      cfg.Globals.Host,
		Port:      cfg.Globals.Port,
		StartedAt: proc.Started,
		Command:   res.Argv,
		LogPath:   logPath,
	}
	if err := sessMgr.WriteAndUnlock(&rec); err != nil {
		proc.Stop(2 * time.Second)
		_ = proc.RemoveLog()
		return nil, fmt.Errorf("write session: %w", err)
	}
	return &tui.RunModeOpts{
		Cfg:        cfg,
		RouterFile: file,
		Argv:       res.Argv,
		Process:    proc,
		SessionMgr: sessMgr,
		Registry:   registry,
		Fetcher:    fetcherFor(res.Argv, registry),
	}, nil
}

// runRouterCLI implements `llamaman -i <file>`: it loads the config (or
// uses default globals when none exists), spawns router mode, and drops
// straight into run mode.
func runRouterCLI(file, cfgOverride string) int {
	cfg := &config.Config{Version: config.SchemaVersion, Globals: defaultGlobals()}
	cfgPath := cfgOverride
	if cfgPath == "" {
		p, err := paths.ConfigPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve config path:", err)
			return exitGeneric
		}
		cfgPath = p
	}
	if loaded, err := config.Load(cfgPath); err == nil {
		cfg = loaded
	} else if !errors.Is(err, os.ErrNotExist) || cfgOverride != "" {
		// Corrupt config, or an explicit -c path that does not exist.
		fmt.Fprintln(os.Stderr, err)
		return exitConfigErr
	}

	registry, registryReal := loadFlagRegistry(cfg.Globals.Bin)
	sessMgr, err := server.NewSessionManager()
	if err != nil {
		fmt.Fprintln(os.Stderr, "session manager:", err)
		return exitGeneric
	}

	opts, err := acquireAndSpawnRouter(cfg, registry, registryReal, sessMgr, file)
	if err != nil {
		if errors.Is(err, server.ErrAnotherStarter) {
			fmt.Fprintln(os.Stderr, "Another llamaman is already running")
			return exitOK
		}
		var pErr ErrPortInUse
		if errors.As(err, &pErr) {
			fmt.Fprintln(os.Stderr, pErr.Underlying)
			return exitPortInUse
		}
		fmt.Fprintln(os.Stderr, "spawn:", err)
		return exitGeneric
	}

	root := tui.NewRoot(cfg, cfgPath, &liveSpawner{cfg: cfg, registry: registry, registryReal: registryReal, sessMgr: sessMgr}, registry, versionString(), opts)
	if _, err := tea.NewProgram(root, tea.WithAltScreen(), tea.WithMouseCellMotion()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "tui:", err)
		return exitGeneric
	}
	return exitOK
}

// fetcherFor constructs the live /props HTTP client from an already-built
// argv. The host:port live in argv (whether auto-added by translate.Build
// or preset-overridden), so a preset that retargets the listen address
// dials the right place. ExtractAddr returns ok=false only if the argv
// invariant is violated; callers leave Fetcher nil in that case and the
// run-mode header gracefully degrades to the preset value.
func fetcherFor(argv []string, registry flags.Registry) tui.Fetcher {
	host, port, ok := flags.ExtractAddr(argv, registry)
	if !ok {
		slog.Warn("could not extract host:port from argv; live ctx-size disabled")
		return nil
	}
	return llamaapi.NewClient(host, port)
}

// buildReattachOpts wraps an existing session in a RunModeOpts. The model
// and preset are looked up from the live config so the header still
// renders condensed param info; if the user has edited the config since
// the session started, the header shows the current preset definition,
// which is acceptable for v1. Router-kind sessions carry the models-file
// path instead (RouterFile), leaving model/preset empty.
func buildReattachOpts(cfg *config.Config, registry flags.Registry, sess server.Session, sessMgr *server.SessionManager) (*tui.RunModeOpts, error) {
	if sess.IsRouter() {
		return &tui.RunModeOpts{
			Cfg:        cfg,
			RouterFile: sess.Alias,
			Argv:       sess.Command,
			Process:    server.Adopt(sess),
			SessionMgr: sessMgr,
			Registry:   registry,
			Fetcher:    fetcherFor(sess.Command, registry),
		}, nil
	}
	model, _ := findModel(cfg, sess.Alias)
	if model.Alias == "" {
		// Config no longer has this alias. Fall back to a stub model so
		// the header still renders identity info.
		model = config.Model{Alias: sess.Alias}
	}
	var preset config.Preset
	if sess.Preset != "" {
		if p, ok := findPreset(model, sess.Preset); ok {
			preset = p
		} else {
			preset = config.Preset{Name: sess.Preset}
		}
	}
	return &tui.RunModeOpts{
		Cfg:        cfg,
		Model:      model,
		Preset:     preset,
		Argv:       sess.Command,
		Process:    server.Adopt(sess),
		SessionMgr: sessMgr,
		Registry:   registry,
		Fetcher:    fetcherFor(sess.Command, registry),
	}, nil
}

// loadConfigOrFirstRun opens the config at the given path or the default
// XDG location, then runs cross-field validation per DESIGN.md §3.5/§4.3.
// Returns:
//
//	cfg:     the loaded config (nil on error or first-run case)
//	path:    the resolved config path (always set)
//	code:    non-zero exit code if loading failed in a non-first-run way
//	missing: true if default path is absent and -c was not given (caller
//	         should drop into first-run flow)
//
// `-c <path>` with a missing file is a hard error per DESIGN.md §8.
// Validation errors print every problem to stderr and exit 2 (§9).
func loadConfigOrFirstRun(override string) (*config.Config, string, int, bool) {
	path := override
	if path == "" {
		p, err := paths.ConfigPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve config path:", err)
			return nil, "", exitGeneric, false
		}
		path = p
	}
	cfg, err := config.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if override == "" {
				return nil, path, 0, true
			}
			fmt.Fprintf(os.Stderr, "config not found: %s\n", path)
			return nil, path, exitConfigErr, false
		}
		fmt.Fprintln(os.Stderr, err)
		return nil, path, exitConfigErr, false
	}
	issues := config.Validate(cfg)
	if issues.HasErrors() {
		fmt.Fprintf(os.Stderr, "config %s has errors:\n", path)
		for _, it := range issues {
			if it.Severity == config.Error {
				fmt.Fprintf(os.Stderr, "  ERROR  %s: %s\n", it.Path, it.Message)
			}
		}
		// Surface warnings too so the user can fix them in one pass.
		for _, it := range issues {
			if it.Severity == config.Warning {
				fmt.Fprintf(os.Stderr, "  warn   %s: %s\n", it.Path, it.Message)
			}
		}
		return nil, path, exitConfigErr, false
	}
	for _, it := range issues {
		slog.Warn("config validation warning", "path", it.Path, "msg", it.Message)
	}
	return cfg, path, 0, false
}

// loadFlagRegistry parses `<bin> --help` (or returns the cached registry),
// falling back to the hard-coded short-form set when unavailable. The
// bool reports whether the returned registry came from real parsed help
// output (false = fallback set) — used to gate router-mode spawns on
// llama-server's --models-preset support. Errors here are non-fatal: the
// user just gets fewer canonical-form benefits.
func loadFlagRegistry(bin string) (flags.Registry, bool) {
	loader, err := flags.NewLoader(bin)
	if err != nil {
		slog.Warn("flag loader init failed; using fallback set", "err", err)
		return nil, false
	}
	reg, real := loader.Load()
	if !real {
		slog.Info("flag registry: using fallback short-form set", "bin", bin)
	}
	return reg, real
}

// pickPreset returns the preset to launch for an (alias, preset?) pair,
// matching the TUI's heuristic in selection mode.
func pickPreset(m config.Model, name string) (config.Preset, bool) {
	if name != "" {
		return findPreset(m, name)
	}
	switch len(m.Presets) {
	case 0:
		return config.Preset{Name: "default"}, true
	case 1:
		return m.Presets[0], true
	default:
		for _, p := range m.Presets {
			if p.Name == "default" {
				return p, true
			}
		}
		return m.Presets[0], true
	}
}

func findModel(cfg *config.Config, alias string) (config.Model, bool) {
	for _, m := range cfg.Models {
		if m.Alias == alias {
			return m, true
		}
	}
	return config.Model{}, false
}

func findPreset(m config.Model, name string) (config.Preset, bool) {
	for _, p := range m.Presets {
		if p.Name == name {
			return p, true
		}
	}
	return config.Preset{}, false
}

// runList implements --list / -l. Output is one model per line with
// preset count and a "(running)" marker if a session for that alias is
// currently live, followed by one line per configured router source
// (globals.models-files) with its parsed section count.
func runList(cfgOverride string) int {
	cfg, _, code, missing := loadConfigOrFirstRun(cfgOverride)
	if missing {
		fmt.Fprintln(os.Stderr, "no config; run llamaman without flags to set up")
		return exitConfigErr
	}
	if code != 0 {
		return code
	}
	runningAlias := ""
	if mgr, err := server.NewSessionManager(); err == nil {
		if s, _ := mgr.Read(); s != nil {
			runningAlias = s.Alias
		}
	}
	for _, m := range cfg.Models {
		marker := ""
		if m.Alias == runningAlias {
			marker = " (running)"
		}
		fmt.Printf("%s\t(%s)\t%d preset%s%s\n",
			m.Alias, m.SourceLabel(), len(m.Presets), plural(len(m.Presets)), marker)
	}
	for _, mf := range cfg.Globals.ModelsFiles {
		marker := ""
		if mf == runningAlias {
			marker = " (running)"
		}
		count := "?"
		if f, err := modelsini.ParseFile(mf); err == nil {
			count = strconv.Itoa(len(f.Sections))
		}
		fmt.Printf("%s\t(router)\t%s model%s%s\n", mf, count, pluralAtLeast1(count), marker)
	}
	return exitOK
}

// pluralAtLeast1 returns "s" unless the count string is exactly "1".
func pluralAtLeast1(count string) string {
	if count == "1" {
		return ""
	}
	return "s"
}

// runPresets implements --presets / -p. Requires a positional alias.
func runPresets(cfgOverride, alias string) int {
	if alias == "" {
		fmt.Fprintln(os.Stderr, "--presets requires a model alias argument")
		return exitConfigErr
	}
	cfg, _, code, missing := loadConfigOrFirstRun(cfgOverride)
	if missing {
		fmt.Fprintln(os.Stderr, "no config; run llamaman without flags to set up")
		return exitConfigErr
	}
	if code != 0 {
		return code
	}
	model, ok := findModel(cfg, alias)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown model alias: %s\n", alias)
		return exitConfigErr
	}
	if len(model.Presets) == 0 {
		fmt.Println("(no presets)")
		return exitOK
	}
	for _, p := range model.Presets {
		if p.Description != "" {
			fmt.Printf("%s\t%s\n", p.Name, p.Description)
		} else {
			fmt.Println(p.Name)
		}
	}
	return exitOK
}

// runSubcommand parses and dispatches `import`/`export` with their own
// kong parser, so --help and -c keep working for both.
func runSubcommand(name string, args []string) int {
	switch name {
	case "import":
		var in importArgs
		p, err := kong.New(&in, kong.Name("llamaman import"), kong.UsageOnError())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitGeneric
		}
		if _, err := p.Parse(args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitConfigErr
		}
		return runImport(in.File, in.Config)
	case "export":
		var ex exportArgs
		p, err := kong.New(&ex, kong.Name("llamaman export"), kong.UsageOnError())
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitGeneric
		}
		if _, err := p.Parse(args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return exitConfigErr
		}
		return runExport(ex.Path, ex.Config)
	}
	fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", name)
	return exitConfigErr
}

// runImport implements `llamaman import <file>`. It parses a my-models.ini
// file, maps its sections to models/presets (modelsini.Import), and merges
// them into the config — creating the config file if it does not exist yet.
// Aliases that collide with existing config models are renamed with an
// "-ini" suffix; every other mapping problem is a warning, not an error.
func runImport(file, cfgOverride string) int {
	f, err := modelsini.ParseFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitConfigErr
	}

	cfgPath := cfgOverride
	if cfgPath == "" {
		p, err := paths.ConfigPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "resolve config path:", err)
			return exitGeneric
		}
		cfgPath = p
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(os.Stderr, err)
			return exitConfigErr
		}
		// No config yet: bootstrap one from the import alone.
		cfg = &config.Config{
			Version: config.SchemaVersion,
			Globals: defaultGlobals(),
			Models:  []config.Model{},
		}
	}

	reg, _ := loadFlagRegistry(cfg.Globals.Bin)
	imported, warnings := modelsini.Import(f, cfg.Models, reg)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warn:", w)
	}

	merged := &config.Config{Version: cfg.Version, Globals: cfg.Globals}
	merged.Models = append(merged.Models, cfg.Models...)
	merged.Models = append(merged.Models, imported...)
	if issues := config.Validate(merged); issues.HasErrors() {
		for _, it := range issues {
			if it.Severity == config.Error {
				fmt.Fprintf(os.Stderr, "  ERROR  %s: %s\n", it.Path, it.Message)
			}
		}
		fmt.Fprintln(os.Stderr, "import aborted; fix the config before retrying")
		return exitConfigErr
	}

	if err := config.Save(cfgPath, merged); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitGeneric
	}
	fmt.Printf("imported %d model%s from %s into %s\n",
		len(imported), plural(len(imported)), file, cfgPath)
	return exitOK
}

// runExport implements `llamaman export [path]`. It serializes every
// (model, preset) as a my-models.ini section, writing to stdout when no
// path is given. The output is valid input for llama-server
// --models-preset as long as all param keys are known to that server.
func runExport(path, cfgOverride string) int {
	cfg, _, code, missing := loadConfigOrFirstRun(cfgOverride)
	if missing {
		fmt.Fprintln(os.Stderr, "no config; nothing to export")
		return exitConfigErr
	}
	if code != 0 {
		return code
	}

	f, warnings := modelsini.Export(cfg)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warn:", w)
	}

	if path == "" {
		fmt.Print(f.String())
		return exitOK
	}
	if err := os.WriteFile(path, []byte(f.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "export:", err)
		return exitGeneric
	}
	fmt.Printf("exported %d section%s to %s\n",
		len(f.Sections), plural(len(f.Sections)), path)
	return exitOK
}

// defaultGlobals is used when import has to bootstrap a config that does
// not exist yet. It mirrors the first-run defaults (DESIGN.md §8): the
// autodetected binary and a loopback listen address.
func defaultGlobals() config.Globals {
	bin := ""
	if p, err := exec.LookPath("llama-server"); err == nil {
		bin = p
	} else {
		for _, c := range []string{
			"/usr/local/bin/llama-server",
			"/usr/local/llama.cpp/bin/llama-server",
			"/opt/llama.cpp/bin/llama-server",
		} {
			if info, err := os.Stat(c); err == nil && info.Mode()&0o111 != 0 {
				bin = c
				break
			}
		}
	}
	return config.Globals{Bin: bin, Host: "127.0.0.1", Port: 9080}
}

// runCompletion implements --completion bash|zsh|fish. Kong v1's
// completion API exposes a CompletionInstall plugin, but for our
// purposes the simplest reliable thing is to ship hand-rolled scripts
// that wrap kong's runtime completer. The scripts are static — kong
// completes flags by re-invoking the binary at runtime.
func runCompletion(shell string) int {
	switch shell {
	case "bash":
		fmt.Print(bashCompletionScript)
	case "zsh":
		fmt.Print(zshCompletionScript)
	case "fish":
		fmt.Print(fishCompletionScript)
	default:
		fmt.Fprintf(os.Stderr, "unknown shell %q (expected bash, zsh, or fish)\n", shell)
		return exitConfigErr
	}
	return exitOK
}

// plural returns "s" unless n == 1.
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func versionString() string {
	if version == "" {
		return "vdev"
	}
	if version[0] == 'v' {
		return version
	}
	return "v" + version
}
