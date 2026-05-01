package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleCfg() *Config {
	return &Config{
		Version: 1,
		Globals: Globals{Bin: "/usr/local/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Models: []Model{{
			Alias: "qwen", Location: "/m.gguf",
			Presets: []Preset{{
				Name: "default",
				Params: Params{
					{Key: "ngl", Value: json.Number("99")},
					{Key: "fa", Value: "on"},
					{Key: "jinja", Value: true},
				},
			}},
		}},
	}
}

func TestSaveAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Save(path, sampleCfg()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"alias": "qwen"`) {
		t.Errorf("written config missing alias: %s", data)
	}
	// First save: no .bak yet.
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error(".bak should not exist after first save")
	}
}

func TestSaveCreatesRollingBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Save(path, sampleCfg()); err != nil {
		t.Fatal(err)
	}
	cfg2 := sampleCfg()
	cfg2.Globals.Port = 9999
	if err := Save(path, cfg2); err != nil {
		t.Fatal(err)
	}

	// Backup should hold the original port; primary should hold the new
	// one.
	bak, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("read .bak: %v", err)
	}
	if !strings.Contains(string(bak), `"port": 9080`) {
		t.Errorf(".bak should contain old port: %s", bak)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cur), `"port": 9999`) {
		t.Errorf("config.json should contain new port: %s", cur)
	}
}

func TestSavePreservesParamOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := Save(path, sampleCfg()); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	got := loaded.Models[0].Presets[0].Params
	want := []string{"ngl", "fa", "jinja"}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d", len(got), len(want))
	}
	for i, k := range want {
		if got[i].Key != k {
			t.Errorf("Params[%d] = %q, want %q", i, got[i].Key, k)
		}
	}
}

func TestSameOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	cfg := sampleCfg()
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	same, err := SameOnDisk(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !same {
		t.Error("expected SameOnDisk == true after Save")
	}
	cfg.Globals.Port = 9999
	same, err = SameOnDisk(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if same {
		t.Error("expected SameOnDisk == false after mutating port")
	}
}

func TestValidateCatchesAliasDuplicate(t *testing.T) {
	cfg := sampleCfg()
	cfg.Models = append(cfg.Models, cfg.Models[0]) // duplicate
	issues := Validate(cfg)
	if !issues.HasErrors() {
		t.Fatalf("expected duplicate alias error; issues: %v", issues)
	}
}

func TestValidatePortRange(t *testing.T) {
	cfg := sampleCfg()
	cfg.Globals.Port = 0
	if !Validate(cfg).HasErrors() {
		t.Error("port 0 should be an error")
	}
	cfg.Globals.Port = 70000
	if !Validate(cfg).HasErrors() {
		t.Error("port 70000 should be an error")
	}
}

func TestValidateMissingBinaryIsWarning(t *testing.T) {
	cfg := sampleCfg()
	cfg.Globals.Bin = "/definitely/not/here/llama-server"
	cfg.Models[0].Location = "/definitely/not/here/m.gguf"

	issues := Validate(cfg)
	if issues.HasErrors() {
		t.Errorf("missing binary/model should be warnings, not errors: %v", issues)
	}
	if len(issues) < 2 {
		t.Errorf("expected ≥2 warnings, got %d: %v", len(issues), issues)
	}
}

func TestValidateHostFormats(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1":    true,
		"0.0.0.0":      true,
		"[::1]":        true,
		"[::]":         true,
		"localhost":    true,
		"my-host.lan":  true,
		"":             false,
		"1 2 3":        false,
		"weird/slash":  false,
	}
	for host, ok := range cases {
		if got := ValidHost(host); got != ok {
			t.Errorf("ValidHost(%q) = %v, want %v", host, got, ok)
		}
	}
}
