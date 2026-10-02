package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/modelsini"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// withArgs patches os.Args for the duration of the test and restores it.
func withArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	os.Args = args
	t.Cleanup(func() { os.Args = orig })
}

// captureStderr redirects os.Stderr to a temp file and returns it (read
// after the call under test).
func captureStderr(t *testing.T) *os.File {
	t.Helper()
	orig := os.Stderr
	f, err := os.CreateTemp("", "llamaman-stderr-*.log")
	if err != nil {
		t.Fatalf("capture stderr: %v", err)
	}
	os.Stderr = f
	t.Cleanup(func() {
		os.Stderr = orig
		f.Close()
	})
	return f
}

func stderrContains(t *testing.T, f *os.File, want string) bool {
	t.Helper()
	if err := f.Sync(); err != nil {
		t.Fatalf("sync stderr: %v", err)
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	return strings.Contains(string(b), want)
}

func testConfig(t *testing.T) string {
	t.Helper()
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{Binaries: []config.Binary{{Name: "default", Path: "/bin/true"}}, DefaultBinary: "default", Bin: "/bin/true", Host: "127.0.0.1", Port: 9080},
		Models: []config.Model{
			{
				Alias:    "m",
				Location: "/models/m.gguf",
				Presets: []config.Preset{
					{
						Name:        "default",
						Description: "balanced",
						Params:      config.Params{{Key: "ngl", Value: json.Number("99")}},
					},
				},
			},
		},
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("save test config: %v", err)
	}
	return path
}

// TestExportDispatchBeforeKong is the regression for the reported bug:
// `llamaman export -o out.ini` used to fail at the main kong parser
// ("unknown flag -o, did you mean ... -h, -l, -p, -c, -i?") because the
// subcommand dispatch ran after parser.Parse. It must reach the export
// subcommand instead.
func TestExportDispatchBeforeKong(t *testing.T) {
	cfgPath := testConfig(t)
	out := filepath.Join(t.TempDir(), "my-models.ini")

	t.Run("flag -o", func(t *testing.T) {
		withArgs(t, "llamaman", "export", "-o", out, "-c", cfgPath)
		stderr := captureStderr(t)
		if code := run(); code != exitOK {
			t.Fatalf("run() = %d, want 0 (stderr: see log)", code)
		}
		if stderrContains(t, stderr, "unknown flag") {
			t.Fatal("export was routed through the main parser instead of the subcommand")
		}
		f, err := modelsini.ParseFile(out)
		if err != nil {
			t.Fatalf("exported file does not parse: %v", err)
		}
		if len(f.Sections) != 1 || f.Sections[0].Name != "m" {
			t.Fatalf("unexpected sections: %+v", f.Sections)
		}
	})

	t.Run("positional path", func(t *testing.T) {
		out2 := filepath.Join(t.TempDir(), "pos.ini")
		withArgs(t, "llamaman", "export", out2, "-c", cfgPath)
		if code := run(); code != exitOK {
			t.Fatalf("run() = %d, want 0", code)
		}
		if _, err := os.Stat(out2); err != nil {
			t.Fatalf("expected exported file: %v", err)
		}
	})

	t.Run("both paths rejected", func(t *testing.T) {
		withArgs(t, "llamaman", "export", out, "-o", out, "-c", cfgPath)
		stderr := captureStderr(t)
		if code := run(); code != exitConfigErr {
			t.Fatalf("run() = %d, want %d", code, exitConfigErr)
		}
		if !stderrContains(t, stderr, "not both") {
			t.Fatal("expected conflict error for positional + -o")
		}
	})

	t.Run("missing config", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope.json")
		withArgs(t, "llamaman", "export", "-o", out, "-c", missing)
		stderr := captureStderr(t)
		if code := run(); code != exitConfigErr {
			t.Fatalf("run() = %d, want %d", code, exitConfigErr)
		}
		if !stderrContains(t, stderr, "config not found: "+missing) {
			t.Fatal("expected the export subcommand's missing-config error")
		}
	})
}

// TestImportDispatchBeforeKong covers the import side of the moved
// dispatch: flags after the subcommand name must be parsed by the import
// subparser, not the main one.
func TestImportDispatchBeforeKong(t *testing.T) {
	dir := t.TempDir()
	iniPath := filepath.Join(dir, "models.ini")
	if err := os.WriteFile(iniPath, []byte("[m]\nmodel = /models/m.gguf\nngl = 99\n"), 0o644); err != nil {
		t.Fatalf("write ini: %v", err)
	}
	cfgPath := filepath.Join(dir, "new.json")
	// Seed a config with a valid bin so the import doesn't depend on
	// defaultGlobals() discovering a llama-server on the machine (CI
	// runners have none — binary path is required otherwise).
	if err := config.Save(cfgPath, &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{Binaries: []config.Binary{{Name: "default", Path: "/bin/true"}}, DefaultBinary: "default", Bin: "/bin/true", Host: "127.0.0.1", Port: 9080},
	}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	withArgs(t, "llamaman", "import", iniPath, "-c", cfgPath)
	if code := run(); code != exitOK {
		t.Fatalf("run() = %d, want 0", code)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load imported config: %v", err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Alias != "m" {
		t.Fatalf("unexpected imported models: %+v", cfg.Models)
	}
}

// liveSession writes a session.json with a live PID (the test process)
// into an isolated XDG_RUNTIME_DIR and returns the manager.
func liveSession(t *testing.T) *server.SessionManager {
	t.Helper()
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	mgr, err := server.NewSessionManager()
	if err != nil {
		t.Fatal(err)
	}
	sess := server.Session{
		PID:       os.Getpid(), // live for the duration of the test
		Alias:     "alpha",
		Preset:    "default",
		Host:      "127.0.0.1",
		Port:      9080,
		StartedAt: time.Now(),
		Command:   []string{"/usr/bin/llama-server", "-m", "/m/alpha.gguf"},
		LogPath:   filepath.Join(t.TempDir(), "llama.log"),
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mgr.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return mgr
}

// TestDecideEntryNoArgsLandsOnMainWhenSessionRunning pins §4.3/§15.2:
// a no-args launch with a live session returns nil (Main mode) so the
// session header strip shows and `a` attaches — no auto-bypass.
func TestDecideEntryNoArgsLandsOnMainWhenSessionRunning(t *testing.T) {
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{Binaries: []config.Binary{{Name: "default", Path: "/usr/bin/llama-server"}}, DefaultBinary: "default", Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
	}
	opts, code := decideEntry(cfg, nil, liveSession(t), "", "", "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if opts != nil {
		t.Fatalf("no-args launch with a live session must land on Main (opts=nil), got %+v", opts)
	}
}

// TestDecideEntryAliasStillReattaches: `llamaman <alias>` with a live
// session keeps the direct reattach path (arguments ignored, §4.3).
func TestDecideEntryAliasStillReattaches(t *testing.T) {
	cfg := &config.Config{
		Version: 1,
		Globals: config.Globals{Binaries: []config.Binary{{Name: "default", Path: "/usr/bin/llama-server"}}, DefaultBinary: "default", Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
	}
	opts, code := decideEntry(cfg, nil, liveSession(t), "alpha", "default", "")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if opts == nil {
		t.Fatal("alias launch with a live session must still reattach (opts != nil)")
	}
}

// Regression: the one-shot override must reach the real spawner. The TUI
// quick-switch set it, Root passed it as an argument, and liveSpawner.Spawn
// threw it away in favour of an always-empty struct field — so the user was
// told "next spawn uses X" and got the default binary instead.
func TestLiveSpawnerForwardsOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", dir) // per-binary registry probes stay hermetic

	mkBin := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	mainBin, forkBin := mkBin("llama-server"), mkBin("llama-server-fork")

	cfg := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Binaries: []config.Binary{
				{Name: "main", Path: mainBin},
				{Name: "fork", Path: forkBin},
			},
			DefaultBinary: "main",
			Host:          "127.0.0.1",
			Port:          0, // ephemeral: PortAvailable(0) always succeeds
		},
		Models: []config.Model{{
			Alias:    "m",
			Location: "/models/m.gguf",
			// The preset pins "main", so an empty override must resolve to
			// the pin and a non-empty one must win over it.
			Presets: []config.Preset{{Name: "default", Binary: "main"}},
		}},
	}
	mgr, err := server.NewSessionManager()
	if err != nil {
		t.Fatal(err)
	}
	l := &liveSpawner{cfg: cfg, defaultBin: cfg.Globals.Binaries[0], sessMgr: mgr}
	model, preset := cfg.Models[0], cfg.Models[0].Presets[0]

	opts, err := l.Spawn(model, preset, "fork")
	if err != nil {
		t.Fatalf("Spawn with override: %v", err)
	}
	t.Cleanup(func() { opts.Process.Stop(2 * time.Second) })
	if opts.BinaryName != "fork" {
		t.Fatalf("RunModeOpts.BinaryName=%q, want fork (header would hide the binary)", opts.BinaryName)
	}
	sess, err := mgr.Read()
	if err != nil || sess == nil {
		t.Fatalf("read session: %v", err)
	}
	if sess.BinaryName != "fork" {
		t.Fatalf("session records binary %q, want fork (override dropped)", sess.BinaryName)
	}
	if len(sess.Command) == 0 || sess.Command[0] != forkBin {
		t.Fatalf("argv[0]=%v, want %q", sess.Command, forkBin)
	}

	// No override: the preset pin still decides.
	if err := mgr.Clear(); err != nil {
		t.Fatal(err)
	}
	opts2, err := l.Spawn(model, preset, "")
	if err != nil {
		t.Fatalf("Spawn without override: %v", err)
	}
	t.Cleanup(func() { opts2.Process.Stop(2 * time.Second) })
	sess2, err := mgr.Read()
	if err != nil || sess2 == nil {
		t.Fatalf("read session: %v", err)
	}
	if sess2.BinaryName != "main" {
		t.Fatalf("session records binary %q, want the preset pin main", sess2.BinaryName)
	}
}

// Regression (§3.2a): reattaching must surface the binary the session runs
// on. session.json recorded binary_name but nothing read it, so the run
// header lost the chip exactly when it is most useful (after a llamaman
// restart).
func TestReattachCarriesBinaryName(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())

	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755)

	cfg := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Binaries:      []config.Binary{{Name: "main", Path: bin}, {Name: "fork", Path: bin + "-fork"}},
			DefaultBinary: "main", Host: "127.0.0.1", Port: 9080,
		},
		Models: []config.Model{{Alias: "alpha", Location: "/m.gguf", Presets: []config.Preset{{Name: "default"}}}},
	}
	mgr, err := server.NewSessionManager()
	if err != nil {
		t.Fatal(err)
	}
	sess := server.Session{
		PID: os.Getpid(), Alias: "alpha", Preset: "default",
		Host: "127.0.0.1", Port: 9080, StartedAt: time.Now(),
		Command: []string{bin, "-m", "/m.gguf"}, LogPath: filepath.Join(dir, "llama.log"),
		BinaryName: "fork",
	}
	data, err := json.Marshal(sess)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mgr.Path(), data, 0o600); err != nil {
		t.Fatal(err)
	}

	opts, err := buildReattachOpts(cfg, nil, sess, mgr)
	if err != nil {
		t.Fatalf("buildReattachOpts: %v", err)
	}
	if opts.BinaryName != "fork" {
		t.Fatalf("reattach BinaryName=%q, want fork", opts.BinaryName)
	}
}

// writeFakeServer writes an executable that prints the captured
// llama-server --help fixture, so registry loading can be exercised
// without a real llama.cpp build.
func writeFakeServer(t *testing.T, dir, name string) string {
	t.Helper()
	help, err := filepath.Abs(filepath.Join("internal", "flags", "testdata", "llama-server-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(help); err != nil {
		t.Fatalf("help fixture: %v", err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\ncat '"+help+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// Regression (§3.2a): the flag registry must be loaded for the *resolved*
// binary. It used to come from globals.llama-server-bin, which the
// load-time migration clears — so every real config parsed zero flags and
// silently fell back to the 20-entry short-form set.
func TestFlagRegistryFollowsResolvedBinary(t *testing.T) {
	dir := t.TempDir()
	fake := writeFakeServer(t, dir, "llama-server-fork")

	cfgPath := filepath.Join(dir, "config.json")
	src := `{"version":1,"globals":{"llama-server-bin":"` + fake + `","ip_address":"127.0.0.1","port":9080},"models":[]}`
	if err := os.WriteFile(cfgPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Globals.Bin != "" {
		t.Fatalf("legacy Bin must be cleared by the migration, got %q", cfg.Globals.Bin)
	}

	// The cleared legacy key is unusable as a registry source…
	if _, real := loadFlagRegistry(cfg.Globals.Bin); real {
		t.Fatal("the cleared legacy key must not produce a parsed registry")
	}
	// …the resolved binaries[] entry is the right source.
	defBin, _ := cfg.Globals.ResolveBinary("", "")
	if defBin.Path != fake {
		t.Fatalf("resolved path=%q, want %q", defBin.Path, fake)
	}
	reg, real := loadFlagRegistry(defBin.Path)
	if !real {
		t.Fatal("parsed registry expected for an existing binary")
	}
	if _, ok := reg.Lookup("ctx-size"); !ok {
		t.Fatalf("registry has no ctx-size entry (entries=%d)", len(reg))
	}
}

// Regression: a --binary name that matches no binaries[] entry is a hard
// error on every CLI path that can launch, including `-i` (which used to
// ignore the flag entirely).
func TestBinaryOverrideValidation(t *testing.T) {
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Binaries:      []config.Binary{{Name: "main", Path: "/bin/true"}},
			DefaultBinary: "main", Host: "127.0.0.1", Port: 9080,
		},
	}
	if err := validateBinaryOverride(cfg, ""); err != nil {
		t.Fatalf("empty override must be valid: %v", err)
	}
	if err := validateBinaryOverride(cfg, "main"); err != nil {
		t.Fatalf("known override must be valid: %v", err)
	}
	err := validateBinaryOverride(cfg, "ghost")
	if err == nil {
		t.Fatal("unknown override must be rejected")
	}
	if !strings.Contains(err.Error(), "main") {
		t.Fatalf("error should list configured names, got %q", err)
	}

	t.Run("router CLI", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "c.json")
		if err := config.Save(cfgPath, cfg); err != nil {
			t.Fatal(err)
		}
		withArgs(t, "llamaman", "-i", "/tmp/does-not-matter.ini", "-c", cfgPath, "--binary", "ghost")
		stderr := captureStderr(t)
		if code := run(); code != exitConfigErr {
			t.Fatalf("run() = %d, want %d", code, exitConfigErr)
		}
		if !stderrContains(t, stderr, "unknown binary") {
			t.Fatal("the -i path must reject an unknown --binary name")
		}
	})
}

// Regression: with no config file, `llamaman import` must bootstrap a
// config in the new schema shape. It used to build globals from the legacy
// Bin only and then fail validation ("at least one llama-server binary is
// required").
func TestImportBootstrapsConfigWithoutFile(t *testing.T) {
	dir := t.TempDir()
	fakeDir := t.TempDir()
	fake := filepath.Join(fakeDir, "llama-server")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDir) // deterministic autodetect for defaultGlobals

	iniPath := filepath.Join(dir, "models.ini")
	if err := os.WriteFile(iniPath, []byte("[m]\nmodel = /models/m.gguf\nngl = 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "new.json") // deliberately absent

	if code := runImport(iniPath, cfgPath); code != exitOK {
		t.Fatalf("runImport = %d, want 0", code)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("the bootstrapped config must load: %v", err)
	}
	if len(cfg.Globals.Binaries) != 1 || cfg.Globals.Binaries[0].Name != config.DefaultBinaryName {
		t.Fatalf("bootstrapped binaries = %+v", cfg.Globals.Binaries)
	}
	if cfg.Globals.Binaries[0].Path != fake {
		t.Fatalf("bootstrapped path = %q, want the autodetected %q", cfg.Globals.Binaries[0].Path, fake)
	}
	if cfg.Globals.DefaultBinary != config.DefaultBinaryName || cfg.Globals.Bin != "" {
		t.Fatalf("legacy shape leaked: default=%q bin=%q", cfg.Globals.DefaultBinary, cfg.Globals.Bin)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].Alias != "m" {
		t.Fatalf("imported models = %+v", cfg.Models)
	}
}

// Regression (§4.4): a launch that cannot find the resolved binary is a
// missing prerequisite → exit 3, not the generic 1.
func TestMissingBinaryExitsPrereq(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir()) // no live session
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Binaries:      []config.Binary{{Name: "default", Path: filepath.Join(dir, "gone-llama-server")}},
			DefaultBinary: "default", Host: "127.0.0.1", Port: 19090,
		},
		Models: []config.Model{{
			Alias:    "m",
			Location: "/models/m.gguf",
			Presets:  []config.Preset{{Name: "default"}},
		}},
	}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}

	withArgs(t, "llamaman", "m", "default", "-c", cfgPath)
	stderr := captureStderr(t)
	if code := run(); code != exitPrereqErr {
		t.Fatalf("run() = %d, want %d (prereq)", code, exitPrereqErr)
	}
	if !stderrContains(t, stderr, "not found") {
		t.Fatal("expected the resolved-binary error on stderr")
	}
}

// The legacy key alongside binaries[] is a silent no-op, so validation
// says so instead of letting the edit vanish.
func TestValidateWarnsOnLegacyKeyWithBinaries(t *testing.T) {
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Bin:           "/legacy/llama-server",
			Binaries:      []config.Binary{{Name: "main", Path: "/bin/true"}},
			DefaultBinary: "main", Host: "127.0.0.1", Port: 9080,
		},
	}
	issues := config.Validate(cfg)
	if issues.HasErrors() {
		t.Fatalf("must warn, not error: %+v", issues)
	}
	found := false
	for _, it := range issues {
		if it.Severity == config.Warning && strings.Contains(it.Message, "ignored") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an 'ignored' warning for globals.llama-server-bin; got %+v", issues)
	}
}

// Regression (§3.2a): when the resolved binary's --help can't be read, the
// spawner borrows the startup registry for canonicalization but must not
// call it real — router mode would otherwise check --models-preset
// support against a different build.
func TestRegistryForUnreadableBinaryIsNotReal(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	mainBin := writeFakeServer(t, dir, "llama-server")
	silent := filepath.Join(dir, "llama-server-silent")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	startReg, startReal := loadFlagRegistry(mainBin)
	if !startReal {
		t.Fatal("startup registry fixture must parse")
	}
	cases := []struct{ name, path string }{
		{"missing binary", filepath.Join(dir, "does-not-exist")},
		{"unparseable help", silent},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				Version: config.SchemaVersion,
				Globals: config.Globals{
					Binaries:      []config.Binary{{Name: "main", Path: mainBin}, {Name: "fork", Path: tc.path}},
					DefaultBinary: "main",
				},
			}
			l := &liveSpawner{cfg: cfg, defaultBin: cfg.Globals.Binaries[0], registry: startReg, registryReal: startReal}

			_, real := l.registryFor("fork", "")

			if real {
				t.Fatal("registry for an unreadable binary reported real")
			}
		})
	}
}

// Regression: the spawner must resolve against the config the TUI saved,
// not the startup snapshot — a binary added in the config screen used to
// fail with "unknown binary" until llamaman restarted.
func TestLiveSpawnerSetConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", dir)
	rocm := writeFakeServer(t, dir, "llama-server-rocm")
	old := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Binaries:      []config.Binary{{Name: "main", Path: filepath.Join(dir, "llama-server")}},
			DefaultBinary: "main",
		},
	}
	l := &liveSpawner{cfg: old, defaultBin: old.Globals.Binaries[0]}
	if _, real := l.registryFor("rocm", ""); real {
		t.Fatal("precondition: rocm must be unknown to the startup config")
	}
	saved := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Binaries: []config.Binary{
				{Name: "main", Path: filepath.Join(dir, "llama-server")},
				{Name: "rocm", Path: rocm},
			},
			DefaultBinary: "main",
		},
	}

	l.SetConfig(saved)
	_, real := l.registryFor("rocm", "")

	if !real {
		t.Fatal("registryFor(rocm) did not resolve against the saved config")
	}
}

// Regression: --binary completion filtered binaries by an absolute or ~
// path, hiding entries with relative paths that launch accepts.
func TestCompletionBinariesIncludeRelativePaths(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash not available: %v", err)
	}
	list := "alpha\t(local)\t1 preset\n" +
		"beta\t(hf)\t2 presets\n" +
		"main\t/usr/bin/llama-server\t(default)\n" +
		"home\t~/llama.cpp/build/bin/llama-server\n" +
		"rel\t./build/bin/llama-server\t(warning: missing)\n" +
		"/tmp/my-models.ini\t(router)\t3 models\n"
	script := "llamaman() { printf '%s' \"$LIST\"; }\n" + bashCompletionScript +
		"\necho \"bin:$(_llamaman_binaries | tr '\\n' ' ')\"\necho \"alias:$(_llamaman_aliases | tr '\\n' ' ')\"\n"
	cmd := exec.Command(bash, "-c", script)
	cmd.Env = append(os.Environ(), "LIST="+list)

	out, err := cmd.CombinedOutput()

	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	got := string(out)
	if !strings.Contains(got, "bin:main home rel \n") {
		t.Fatalf("binaries completion wrong:\n%s", got)
	}
	if !strings.Contains(got, "alias:alpha beta \n") {
		t.Fatalf("alias completion wrong:\n%s", got)
	}
	// zsh and fish must use the same filter.
	const filter = `awk -F'\t' '$2 != "" && $2 !~ /^\(/ {print $1}'`
	for name, s := range map[string]string{"zsh": zshCompletionScript, "fish": fishCompletionScript} {
		if !strings.Contains(s, filter) {
			t.Errorf("%s completion does not use the binaries filter", name)
		}
	}
}
