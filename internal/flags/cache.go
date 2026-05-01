package flags

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cmoro-deusto/llamaman/internal/paths"
)

// Loader resolves the canonical Registry for a given llama-server binary.
// It caches results to disk keyed by binary mtime, falling back to the
// hard-coded short-form set if the binary is missing or its --help output
// can't be parsed (DESIGN.md §6.2).
type Loader struct {
	BinPath  string
	CacheDir string
	// HelpTimeout bounds how long we'll wait for `<bin> --help`. Defaults
	// to 5 seconds when zero.
	HelpTimeout time.Duration
}

// NewLoader is a convenience constructor that resolves the cache directory
// from the standard XDG location.
func NewLoader(binPath string) (*Loader, error) {
	dir, err := paths.CacheDir()
	if err != nil {
		return nil, err
	}
	return &Loader{BinPath: binPath, CacheDir: dir}, nil
}

// Load returns the registry for the configured binary, using the on-disk
// cache when fresh and falling back to the hard-coded set on failure. The
// boolean return reports whether the registry came from a real --help run
// (true) or from the fallback set (false).
func (l *Loader) Load() (Registry, bool) {
	if l.BinPath == "" {
		return fallbackRegistry(), false
	}
	stat, err := os.Stat(l.BinPath)
	if err != nil {
		return fallbackRegistry(), false
	}
	cachePath := l.cachePath(stat.ModTime())
	if reg, ok := readCache(cachePath); ok {
		return reg, true
	}

	out, err := l.runHelp()
	if err != nil {
		return fallbackRegistry(), false
	}
	reg := ParseHelp(out)
	if len(reg) == 0 {
		return fallbackRegistry(), false
	}
	if err := writeCache(cachePath, reg); err != nil {
		// Cache write failure is not fatal; we still serve the parsed
		// registry for this run.
		_ = err
	}
	return reg, true
}

func (l *Loader) cachePath(mtime time.Time) string {
	name := fmt.Sprintf("flags-%d.json", mtime.UnixNano())
	return filepath.Join(l.CacheDir, name)
}

func (l *Loader) runHelp() (string, error) {
	to := l.HelpTimeout
	if to == 0 {
		to = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), to)
	defer cancel()
	cmd := exec.CommandContext(ctx, l.BinPath, "--help")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s --help: %w", l.BinPath, err)
	}
	return string(out), nil
}

func readCache(path string) (Registry, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	reg := make(Registry)
	if err := json.Unmarshal(data, &reg); err != nil {
		return nil, false
	}
	if len(reg) == 0 {
		return nil, false
	}
	return reg, true
}

func writeCache(path string, reg Registry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	data, err := json.Marshal(reg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fallbackRegistry materializes the hard-coded short-form set into a
// Registry so callers can use the same interface regardless of whether
// they got a parsed registry or the fallback. Boolean detection is
// unavailable in fallback mode, so every entry is marked non-bool — the
// translate layer handles `true` values specially via the value type, not
// the registry.
func fallbackRegistry() Registry {
	reg := make(Registry, len(fallbackShort))
	for k := range fallbackShort {
		reg[k] = FlagInfo{Name: k, Form: "-" + k, IsBool: false}
	}
	return reg
}
