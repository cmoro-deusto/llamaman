package main

import (
	"encoding/json"
	"os"
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
		Globals: config.Globals{Bin: "/bin/true", Host: "127.0.0.1", Port: 9080},
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
		Globals: config.Globals{Bin: "/bin/true", Host: "127.0.0.1", Port: 9080},
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
		Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
	}
	opts, code := decideEntry(cfg, nil, liveSession(t), "", "")
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
		Globals: config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080},
	}
	opts, code := decideEntry(cfg, nil, liveSession(t), "alpha", "default")
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if opts == nil {
		t.Fatal("alias launch with a live session must still reattach (opts != nil)")
	}
}
