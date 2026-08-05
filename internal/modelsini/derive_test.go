package modelsini

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

func TestDefaultModelsFilePath(t *testing.T) {
	got := DefaultModelsFilePath("/home/u/.config/llamaman/config.json")
	want := "/home/u/.config/llamaman/models.ini"
	if got != want {
		t.Errorf("DefaultModelsFilePath = %q, want %q", got, want)
	}
	if got := DefaultModelsFilePath(""); got != "" {
		t.Errorf("DefaultModelsFilePath(\"\") = %q, want empty", got)
	}
}

func TestEffectiveModelsFiles(t *testing.T) {
	cfg := &config.Config{Version: 1, Globals: config.Globals{}}
	// Unset → derived default.
	got := EffectiveModelsFiles(cfg, "/home/u/cfg.json")
	want := []string{"/home/u/models.ini"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("effective (unset) = %v, want %v", got, want)
	}
	// The config itself must stay untouched (never written back).
	if len(cfg.Globals.ModelsFiles) != 0 {
		t.Errorf("config mutated: %v", cfg.Globals.ModelsFiles)
	}
	// Explicit list wins.
	cfg.Globals.ModelsFiles = []string{"/a.ini", "/b.ini"}
	got = EffectiveModelsFiles(cfg, "/home/u/cfg.json")
	if !reflect.DeepEqual(got, cfg.Globals.ModelsFiles) {
		t.Errorf("effective (set) = %v, want %v", got, cfg.Globals.ModelsFiles)
	}
	// No config path → nothing.
	got = EffectiveModelsFiles(&config.Config{}, "")
	if got != nil {
		t.Errorf("effective (no path) = %v, want nil", got)
	}
}

func testDerivedCfg() *config.Config {
	return &config.Config{
		Version: 1,
		Globals: config.Globals{Bin: "/bin/true", Host: "127.0.0.1", Port: 9080},
		Models: []config.Model{{
			Alias: "m", Location: "/m.gguf",
			Presets: []config.Preset{{
				Name: "default", Params: config.Params{
					{Key: "ngl", Value: json.Number("99")},
				},
			}},
		}},
	}
}

func TestWriteTo(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "custom.ini")
	sections, warnings, err := WriteTo(out, testDerivedCfg())
	if err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	if sections != 1 || len(warnings) != 0 {
		t.Errorf("sections=%d warnings=%v", sections, warnings)
	}
	f, err := ParseFile(out)
	if err != nil {
		t.Fatalf("parse exported file: %v", err)
	}
	if len(f.Sections) != 1 || f.Sections[0].Name != "m" {
		t.Errorf("sections = %+v", f.Sections)
	}
}

func TestWriteDerived(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")
	if _, err := WriteDerived(cfgPath, testDerivedCfg()); err != nil {
		t.Fatalf("WriteDerived: %v", err)
	}
	derived := filepath.Join(dir, DefaultModelsIniName)
	if _, err := os.Stat(derived); err != nil {
		t.Fatalf("derived ini not written: %v", err)
	}
	if _, err := ParseFile(derived); err != nil {
		t.Fatalf("derived ini does not parse: %v", err)
	}
	// WriteDerived on an unwritable path must return an error, not panic.
	if _, err := WriteDerived("/nonexistent-dir/x/config.json", testDerivedCfg()); err == nil {
		t.Error("expected error for unwritable derived path")
	}
}
