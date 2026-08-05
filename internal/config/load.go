package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/cmoro-deusto/llamaman/internal/paths"
)

// Load reads a config file from disk, decodes it, and applies path expansion
// to globals.llama-server-bin and every model location. Unknown schema
// versions are a hard error per DESIGN.md §3.2.
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
	return &cfg, nil
}
