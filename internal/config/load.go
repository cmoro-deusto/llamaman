package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/cmoro-deusto/llamaman/internal/paths"
)

// Load reads a config file from disk, decodes it, applies path expansion
// to every binaries[].path and model location, and migrates the legacy
// globals.llama-server-bin key into globals.binaries (DESIGN §3.2a).
// Unknown schema versions are a hard error per DESIGN.md §3.2.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	dec.DisallowUnknownFields()

	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Version != SchemaVersion {
		return nil, fmt.Errorf("parse config %s: unsupported schema version %d (expected %d)", path, cfg.Version, SchemaVersion)
	}
	// Load-time migration (DESIGN §3.2a): a legacy config with only
	// globals.llama-server-bin becomes one binaries entry named
	// "default", marked default. Bin is cleared so the next Save writes
	// only the new form.
	if len(cfg.Globals.Binaries) == 0 && strings.TrimSpace(cfg.Globals.Bin) != "" {
		cfg.Globals.Binaries = []Binary{{Name: DefaultBinaryName, Path: cfg.Globals.Bin}}
		cfg.Globals.DefaultBinary = DefaultBinaryName
		cfg.Globals.Bin = ""
	}
	for i := range cfg.Globals.Binaries {
		expanded, err := paths.ExpandPath(cfg.Globals.Binaries[i].Path)
		if err != nil {
			return nil, fmt.Errorf("expand globals.binaries[%d].path: %w", i, err)
		}
		cfg.Globals.Binaries[i].Path = expanded
	}
	if cfg.Globals.Bin, err = paths.ExpandPath(cfg.Globals.Bin); err != nil {
		return nil, fmt.Errorf("expand globals.llama-server-bin: %w", err)
	}
	for i := range cfg.Globals.ModelsFiles {
		expanded, err := paths.ExpandPath(cfg.Globals.ModelsFiles[i])
		if err != nil {
			return nil, fmt.Errorf("expand globals.models-files[%d]: %w", i, err)
		}
		cfg.Globals.ModelsFiles[i] = expanded
	}
	for i := range cfg.Models {
		expanded, err := paths.ExpandPath(cfg.Models[i].Location)
		if err != nil {
			return nil, fmt.Errorf("expand models[%d].location: %w", i, err)
		}
		cfg.Models[i].Location = expanded
	}
	if cfg.Preferences != nil {
		expanded, err := paths.ExpandPath(cfg.Preferences.ModelsDir)
		if err != nil {
			return nil, fmt.Errorf("expand preferences.models-dir: %w", err)
		}
		cfg.Preferences.ModelsDir = expanded
	}
	return &cfg, nil
}
