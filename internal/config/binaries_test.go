package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolveBinaries builds Globals from a list of names for ResolveBinary
// tests; the first entry is not automatically the default unless
// defaultName says so.
func resolveGlobals(names []string, defaultName string) Globals {
	g := Globals{Host: "127.0.0.1", Port: 9080, DefaultBinary: defaultName}
	for _, n := range names {
		g.Binaries = append(g.Binaries, Binary{Name: n, Path: "/bin/" + n})
	}
	return g
}

func TestResolveBinaryPrecedence(t *testing.T) {
	g := resolveGlobals([]string{"default", "fork"}, "default")

	// 1. Override wins.
	b, src := g.ResolveBinary("fork", "")
	if src != "override" || b.Name != "fork" {
		t.Errorf("override: got (%s, %s), want (fork, override)", b.Name, src)
	}

	// 2. Preset pin when no override.
	b, src = g.ResolveBinary("", "fork")
	if src != "preset" || b.Name != "fork" {
		t.Errorf("preset pin: got (%s, %s), want (fork, preset)", b.Name, src)
	}

	// 3. Default when neither.
	b, src = g.ResolveBinary("", "")
	if src != "default" || b.Name != "default" {
		t.Errorf("default: got (%s, %s), want (default, default)", b.Name, src)
	}

	// Override beats preset pin.
	b, src = g.ResolveBinary("default", "fork")
	if src != "override" || b.Name != "default" {
		t.Errorf("override over preset: got (%s, %s)", b.Name, src)
	}
}

func TestResolveBinaryUnknownPresetPinFallsThrough(t *testing.T) {
	g := resolveGlobals([]string{"default", "fork"}, "default")
	b, src := g.ResolveBinary("", "ghost")
	if src != "default" || b.Name != "default" {
		t.Errorf("unknown pin: got (%s, %s), want fall-through to default", b.Name, src)
	}
}

func TestResolveBinaryUnknownDefaultFallsBackToFirst(t *testing.T) {
	g := resolveGlobals([]string{"main", "fork"}, "ghost")
	b, src := g.ResolveBinary("", "")
	if src != "default" || b.Name != "main" {
		t.Errorf("unknown default: got (%s, %s), want first entry (main)", b.Name, src)
	}
}

func TestResolveBinaryNoDefaultUsesFirst(t *testing.T) {
	g := resolveGlobals([]string{"main", "fork"}, "")
	b, src := g.ResolveBinary("", "")
	if src != "default" || b.Name != "main" {
		t.Errorf("empty default: got (%s, %s), want first entry (main)", b.Name, src)
	}
}

func TestResolveBinaryUnknownOverrideKeepsFallingThrough(t *testing.T) {
	g := resolveGlobals([]string{"default"}, "default")
	b, src := g.ResolveBinary("ghost", "")
	if src == "override" {
		t.Errorf("unknown override must not resolve as override; got (%s, %s)", b.Name, src)
	}
	if src != "default" || b.Name != "default" {
		t.Errorf("unknown override: got (%s, %s), want default", b.Name, src)
	}
}

func TestResolveBinaryNoBinariesUsesLegacyPlaceholder(t *testing.T) {
	g := Globals{Bin: "/legacy/llama-server", Host: "127.0.0.1", Port: 9080}
	b, src := g.ResolveBinary("", "")
	if src != "default" || b.Path != "/legacy/llama-server" || b.Name != DefaultBinaryName {
		t.Errorf("legacy placeholder: got (%s, %s, %s)", b.Name, b.Path, src)
	}
}

func TestValidateBinariesMatrix(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "real-llama-server")
	if err := os.WriteFile(existing, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	nonExec := filepath.Join(dir, "not-exec")
	if err := os.WriteFile(nonExec, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		globals   func() Globals
		wantErrs  []string // substrings of error messages
		wantWarns []string // substrings of warning messages
	}{
		{
			name: "valid two entries",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080, DefaultBinary: "main"}
				g.Binaries = []Binary{
					{Name: "main", Path: existing},
					{Name: "fork", Path: existing, Note: "branch"},
				}
				return g
			},
		},
		{
			name: "no binaries is an error",
			globals: func() Globals {
				return Globals{Host: "127.0.0.1", Port: 9080}
			},
			wantErrs: []string{"at least one llama-server binary is required"},
		},
		{
			name: "empty name is an error",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080}
				g.Binaries = []Binary{{Name: "", Path: existing}}
				return g
			},
			wantErrs: []string{"binary name is required"},
		},
		{
			name: "duplicate names are an error",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080}
				g.Binaries = []Binary{
					{Name: "main", Path: existing},
					{Name: "main", Path: existing},
				}
				return g
			},
			wantErrs: []string{`duplicate binary name "main"`},
		},
		{
			name: "empty path is an error",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080}
				g.Binaries = []Binary{{Name: "main", Path: ""}}
				return g
			},
			wantErrs: []string{"binary path is required"},
		},
		{
			name: "missing path is a warning",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080}
				g.Binaries = []Binary{{Name: "main", Path: "/definitely/not/here/llama-server"}}
				return g
			},
			wantWarns: []string{"does not exist"},
		},
		{
			name: "non-executable path is a warning",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080}
				g.Binaries = []Binary{{Name: "main", Path: nonExec}}
				return g
			},
			wantWarns: []string{"is not executable"},
		},
		{
			name: "unknown default is a warning",
			globals: func() Globals {
				g := Globals{Host: "127.0.0.1", Port: 9080}
				g.Binaries = []Binary{{Name: "main", Path: existing}}
				g.DefaultBinary = "ghost"
				return g
			},
			wantWarns: []string{"names no entry"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			issues := Validate(&Config{Version: SchemaVersion, Globals: tt.globals()})
			errs := issues.FilterBySeverity(Error)
			for _, want := range tt.wantErrs {
				found := false
				for _, is := range errs {
					if strings.Contains(is.Message, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("want error containing %q; got %+v", want, issues)
				}
			}
			for _, want := range tt.wantWarns {
				found := false
				for _, is := range issues {
					if is.Severity == Error {
						continue
					}
					if strings.Contains(is.Message, want) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("want warning containing %q; got %+v", want, issues)
				}
			}
		})
	}
}

func TestValidateUnknownPresetBinaryWarns(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "llama-server")
	if err := os.WriteFile(existing, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	g := Globals{Host: "127.0.0.1", Port: 9080, DefaultBinary: "main"}
	g.Binaries = []Binary{{Name: "main", Path: existing}}
	cfg := &Config{
		Version: SchemaVersion,
		Globals: g,
		Models: []Model{{
			Alias:    "qwen",
			Location: "/m.gguf",
			Presets: []Preset{
				{Name: "pinned", Binary: "ghost"},
				{Name: "unpinned"},
			},
		}},
	}
	issues := Validate(cfg)
	if issues.HasErrors() {
		t.Fatalf("unknown preset pin must warn, not error: %+v", issues)
	}
	found := false
	for _, is := range issues {
		if strings.Contains(is.Message, "falling back to the default binary") {
			found = true
		}
	}
	if !found {
		t.Errorf("want fallback warning; got %+v", issues)
	}
}

func TestLoadMigratesLegacyBinOnSave(t *testing.T) {
	// A legacy config (only llama-server-bin) loads migrated and saves in
	// the new form: the legacy key is gone, binaries[] is present.
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	src := `{"version":1,"globals":{"llama-server-bin":"/x/llama-server","ip_address":"127.0.0.1","port":9080},"models":[]}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Globals.Binaries) != 1 || cfg.Globals.Binaries[0].Name != DefaultBinaryName ||
		cfg.Globals.Binaries[0].Path != "/x/llama-server" {
		t.Fatalf("migration produced %+v", cfg.Globals.Binaries)
	}
	if cfg.Globals.DefaultBinary != DefaultBinaryName || cfg.Globals.Bin != "" {
		t.Fatalf("migration state wrong: default=%q bin=%q", cfg.Globals.DefaultBinary, cfg.Globals.Bin)
	}

	// Save writes the new form only.
	if err := Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(after, &raw); err != nil {
		t.Fatal(err)
	}
	globals := raw["globals"].(map[string]any)
	if _, has := globals["llama-server-bin"]; has {
		t.Error("legacy llama-server-bin key must be dropped on save")
	}
	binaries, ok := globals["binaries"].([]any)
	if !ok || len(binaries) != 1 {
		t.Fatalf("binaries not written: %v", globals)
	}
	if globals["default-binary"] != DefaultBinaryName {
		t.Errorf("default-binary = %v", globals["default-binary"])
	}

	// The rewritten file loads cleanly.
	if _, err := Load(path); err != nil {
		t.Fatalf("re-load: %v", err)
	}
}

func TestLoadKeepsExplicitBinaries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	src := `{"version":1,"globals":{"binaries":[{"name":"main","path":"/a"},{"name":"fork","path":"/b","note":"x"}],"default-binary":"fork","ip_address":"127.0.0.1","port":9080},"models":[]}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Globals.Binaries) != 2 {
		t.Fatalf("Binaries = %+v", cfg.Globals.Binaries)
	}
	if cfg.Globals.DefaultBinary != "fork" {
		t.Errorf("DefaultBinary = %q", cfg.Globals.DefaultBinary)
	}
	if cfg.Globals.Binaries[1].Note != "x" {
		t.Errorf("note lost: %+v", cfg.Globals.Binaries[1])
	}
}

func TestLoadExpandsBinariesPaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	src := `{"version":1,"globals":{"binaries":[{"name":"main","path":"~/bin/llama-server"}],"ip_address":"127.0.0.1","port":9080},"models":[]}`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "/h/alice")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Globals.Binaries[0].Path != "/h/alice/bin/llama-server" {
		t.Errorf("path not expanded: %q", cfg.Globals.Binaries[0].Path)
	}
}

func TestFindBinary(t *testing.T) {
	g := resolveGlobals([]string{"default", "fork"}, "default")
	tests := []struct {
		name     string
		wantOK   bool
		wantPath string
	}{
		{"fork", true, "/bin/fork"},
		{"default", true, "/bin/default"},
		{"missing", false, ""},
		{"", false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, ok := g.FindBinary(tt.name)

			if ok != tt.wantOK || b.Path != tt.wantPath {
				t.Errorf("FindBinary(%q) = %+v, %v; want path %q, %v", tt.name, b, ok, tt.wantPath, tt.wantOK)
			}
		})
	}
}
