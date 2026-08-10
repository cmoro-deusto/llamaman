// Package storage resolves and reads llama.cpp's HF cache layout
// (DESIGN §16.1). It is the storage foundation of Release 2 (Storage
// Manager): the cache root, the two known on-disk layouts, and the
// model files they contain. It never mutates anything (P8) and never
// blocks on unrecognized entries — those are warnings (P3, P6).
package storage

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/cmoro-deusto/llamaman/internal/paths"
)

// CacheRoot resolves the effective llama.cpp HF cache root, first match
// wins (DESIGN §16.1 cache-path resolution table):
//
//  1. preferences.models-dir (the caller passes the value; wins over
//     every environment variable — P8)
//  2. $LLAMA_CACHE
//  3. $HF_HUB_CACHE
//  4. $HUGGINGFACE_HUB_CACHE
//  5. $HF_HOME → <HF_HOME>/hub
//  6. $XDG_CACHE_HOME → <XDG_CACHE_HOME>/huggingface/hub
//  7. $HOME → ~/.cache/huggingface/hub
//
// This mirrors llama.cpp's get_cache_directory() in common/hf-cache.cpp
// (verified; llama.cpp's getpwuid fallback is covered by paths.HomeDir).
// modelsDir is used verbatim — expansion happens at config load time
// (DESIGN §3.3), and relative values are passed through.
func CacheRoot(modelsDir string) (string, error) {
	if modelsDir != "" {
		return modelsDir, nil
	}
	type entry struct{ env, sub string }
	for _, e := range []entry{
		{env: "LLAMA_CACHE"},
		{env: "HF_HUB_CACHE"},
		{env: "HUGGINGFACE_HUB_CACHE"},
		{env: "HF_HOME", sub: "hub"},
		{env: "XDG_CACHE_HOME", sub: filepath.Join("huggingface", "hub")},
		{env: "HOME", sub: filepath.Join(".cache", "huggingface", "hub")},
	} {
		if v := os.Getenv(e.env); v != "" {
			return filepath.Join(v, e.sub), nil
		}
	}
	home, err := paths.HomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve HF cache root: %w", err)
	}
	return filepath.Join(home, ".cache", "huggingface", "hub"), nil
}
