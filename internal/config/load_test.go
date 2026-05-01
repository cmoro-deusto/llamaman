package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sampleConfig = `{
  "version": 1,
  "globals": {
    "llama-server-bin": "/usr/local/bin/llama-server",
    "ip_address": "127.0.0.1",
    "port": 9080
  },
  "models": [
    {
      "alias": "qwen3.6-27B",
      "location": "~/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf",
      "presets": [
        {
          "preset": "default",
          "description": "balanced settings",
          "params": {
            "ngl": 99,
            "ctx-size": 262144,
            "fa": "on",
            "temp": 0.6,
            "top-p": 0.95,
            "jinja": true,
            "no-mmproj": true
          }
        }
      ]
    }
  ]
}`

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadSampleConfig(t *testing.T) {
	t.Setenv("HOME", "/h/alice")
	path := writeFile(t, "config.json", sampleConfig)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
	if cfg.Globals.Bin != "/usr/local/bin/llama-server" {
		t.Errorf("Bin = %q", cfg.Globals.Bin)
	}
	if cfg.Globals.Host != "127.0.0.1" {
		t.Errorf("Host = %q", cfg.Globals.Host)
	}
	if cfg.Globals.Port != 9080 {
		t.Errorf("Port = %d", cfg.Globals.Port)
	}
	if len(cfg.Models) != 1 {
		t.Fatalf("Models len = %d", len(cfg.Models))
	}
	m := cfg.Models[0]
	if m.Alias != "qwen3.6-27B" {
		t.Errorf("Alias = %q", m.Alias)
	}
	if m.Location != "/h/alice/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf" {
		t.Errorf("Location not expanded: %q", m.Location)
	}

	// Param order matches source.
	wantOrder := []string{"ngl", "ctx-size", "fa", "temp", "top-p", "jinja", "no-mmproj"}
	got := m.Presets[0].Params
	for i, k := range wantOrder {
		if got[i].Key != k {
			t.Errorf("Params[%d].Key = %q, want %q", i, got[i].Key, k)
		}
	}
}

func TestLoadExpandsBinaryPath(t *testing.T) {
	t.Setenv("HOME", "/h/alice")
	src := strings.Replace(sampleConfig, `"/usr/local/bin/llama-server"`, `"~/bin/llama-server"`, 1)
	path := writeFile(t, "config.json", src)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Globals.Bin != "/h/alice/bin/llama-server" {
		t.Errorf("Bin not expanded: %q", cfg.Globals.Bin)
	}
}

func TestLoadRejectsUnknownVersion(t *testing.T) {
	src := strings.Replace(sampleConfig, `"version": 1`, `"version": 99`, 1)
	path := writeFile(t, "config.json", src)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported schema version") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func TestLoadRejectsZeroVersion(t *testing.T) {
	// A config file without an explicit version field is also rejected
	// (zero is not 1). This guards against silently loading hand-written
	// files that pre-date the version field.
	src := `{"globals": {"llama-server-bin": "/x", "ip_address": "127.0.0.1", "port": 9080}, "models": []}`
	path := writeFile(t, "config.json", src)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unsupported schema version 0") {
		t.Fatalf("expected version error, got %v", err)
	}
}

func TestLoadRejectsMalformedJSON(t *testing.T) {
	path := writeFile(t, "config.json", `{ this is not json `)
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadRejectsObjectParamValue(t *testing.T) {
	src := strings.Replace(sampleConfig, `"ngl": 99`, `"ngl": {"x":1}`, 1)
	path := writeFile(t, "config.json", src)
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "object values are not supported") {
		t.Fatalf("expected object-value error, got %v", err)
	}
}

func TestLoadMissingFile(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.json"))
	if err == nil {
		t.Fatal("expected error")
	}
}

// Ensures Marshal also round-trips at the Config level so the editor in
// Phase 7 can save without losing param order.
func TestConfigMarshalRoundTrip(t *testing.T) {
	t.Setenv("HOME", "/h/alice")
	path := writeFile(t, "config.json", sampleConfig)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Re-decode from the marshaled output and check the params order is
	// still intact.
	var cfg2 Config
	dec := json.NewDecoder(strings.NewReader(string(out)))
	dec.UseNumber()
	if err := dec.Decode(&cfg2); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	wantOrder := []string{"ngl", "ctx-size", "fa", "temp", "top-p", "jinja", "no-mmproj"}
	for i, k := range wantOrder {
		if cfg2.Models[0].Presets[0].Params[i].Key != k {
			t.Fatalf("round-trip lost param order at %d", i)
		}
	}
}
