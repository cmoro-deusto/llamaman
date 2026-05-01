package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Save writes cfg to path atomically per DESIGN.md §3.4:
//
//  1. Create path.tmp
//  2. fsync the tmp file
//  3. If path already exists, rename it to path.bak (one rolling backup)
//  4. Rename tmp to path
//
// An exclusive flock on path serializes writers; concurrent reads are
// unaffected. Returns once the new file is on disk and the lock is
// released.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("save config: mkdir: %w", err)
	}

	// Lock the destination file (created on first write) for the duration
	// of the save. The lock file is the same path; we never delete it,
	// just truncate/rewrite. flock(2) is per-FD on Linux so closing the
	// FD releases the lock.
	lockPath := path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return fmt.Errorf("save config: open lock: %w", err)
	}
	defer lf.Close()
	if err := unix.Flock(int(lf.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("save config: flock: %w", err)
	}
	defer func() { _ = unix.Flock(int(lf.Fd()), unix.LOCK_UN) }()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("save config: marshal: %w", err)
	}
	data = append(data, '\n')

	tmpPath := path + ".tmp"
	tf, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("save config: create tmp: %w", err)
	}
	if _, err := tf.Write(data); err != nil {
		tf.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save config: write tmp: %w", err)
	}
	if err := tf.Sync(); err != nil {
		tf.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save config: fsync tmp: %w", err)
	}
	if err := tf.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save config: close tmp: %w", err)
	}

	bakPath := path + ".bak"
	if _, err := os.Stat(path); err == nil {
		// Rolling backup: overwrite any prior .bak.
		if err := os.Rename(path, bakPath); err != nil {
			_ = os.Remove(tmpPath)
			return fmt.Errorf("save config: rotate bak: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("save config: stat: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		// Best-effort recovery: try to restore .bak so the user is not
		// left without a config.
		if _, statErr := os.Stat(bakPath); statErr == nil {
			_ = os.Rename(bakPath, path)
		}
		return fmt.Errorf("save config: rename tmp: %w", err)
	}
	return nil
}

// MarshalForDiff returns the JSON bytes that *would* be written by Save,
// without touching disk. Used by the TUI's "● modified" indicator.
func MarshalForDiff(cfg *Config) ([]byte, error) {
	return json.MarshalIndent(cfg, "", "  ")
}

// SameOnDisk reports whether the in-memory config matches the on-disk
// content at path. Returns (true, nil) when path doesn't exist and cfg is
// considered freshly empty by the caller.
func SameOnDisk(path string, cfg *Config) (bool, error) {
	have, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer have.Close()
	disk, err := io.ReadAll(have)
	if err != nil {
		return false, err
	}
	want, err := MarshalForDiff(cfg)
	if err != nil {
		return false, err
	}
	want = append(want, '\n')
	if len(disk) != len(want) {
		return false, nil
	}
	for i := range disk {
		if disk[i] != want[i] {
			return false, nil
		}
	}
	return true, nil
}
