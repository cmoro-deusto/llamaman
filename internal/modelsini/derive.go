package modelsini

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// DefaultModelsIniName is the derived my-models.ini filename written
// next to config.json on every config save. It doubles as the default
// router source (globals.models-files when unset).
const DefaultModelsIniName = "models.ini"

// DefaultModelsFilePath returns the derived ini path for a config
// path: the config's directory plus DefaultModelsIniName ("" for an
// empty cfgPath).
func DefaultModelsFilePath(cfgPath string) string {
	if cfgPath == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(cfgPath), DefaultModelsIniName)
}

// EffectiveModelsFiles returns the router source list: the configured
// globals.models-files, or the derived <config-dir>/models.ini when
// unset. Never mutates the config, so config.json stays clean — the
// default is a load-time view only.
func EffectiveModelsFiles(cfg *config.Config, cfgPath string) []string {
	if len(cfg.Globals.ModelsFiles) > 0 {
		return cfg.Globals.ModelsFiles
	}
	if p := DefaultModelsFilePath(cfgPath); p != "" {
		return []string{p}
	}
	return nil
}

// WriteTo serializes cfg to the my-models.ini format and writes it to
// path. Returns the number of sections written, the export warnings
// (lossy values, comma aliases, ...) and any write error.
func WriteTo(path string, cfg *config.Config) (sections int, warnings []string, err error) {
	f, warnings := Export(cfg)
	if err := os.WriteFile(path, []byte(f.String()), 0o644); err != nil {
		return 0, warnings, fmt.Errorf("write %s: %w", path, err)
	}
	return len(f.Sections), warnings, nil
}

// WriteDerived serializes cfg to the derived my-models.ini next to
// cfgPath (see DefaultModelsFilePath). Transparent side effect of
// every config save; failures are logged by the caller, never fatal.
func WriteDerived(cfgPath string, cfg *config.Config) ([]string, error) {
	_, warnings, err := WriteTo(DefaultModelsFilePath(cfgPath), cfg)
	return warnings, err
}
