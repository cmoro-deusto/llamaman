// Package logging configures the global slog logger to write to llamaman's
// debug log file. INFO is the default level; setting LLAMAMAN_DEBUG=1 raises
// it to DEBUG. The active log file is rotated to llamaman.log.1 when it
// crosses 10 MB, and the previous .1 (if any) is discarded — one prior
// file is kept per DESIGN.md §10.3.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/cmoro-deusto/llamaman/internal/paths"
)

const (
	debugEnvVar     = "LLAMAMAN_DEBUG"
	maxLogSizeBytes = 10 * 1024 * 1024
)

// Init opens the debug log file (rotating it first if it's too large) and
// installs it as slog.Default(). The returned closer should be invoked at
// process exit. If the log file cannot be opened, logging falls back to
// stderr so we never crash on a permissions issue with $XDG_STATE_HOME.
func Init() (io.Closer, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return installFallback(err), nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return installFallback(err), nil
	}
	logPath := filepath.Join(dir, "llamaman.log")
	rotateIfLarge(logPath)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return installFallback(err), nil
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level()})))
	return f, nil
}

// rotateIfLarge moves logPath to logPath+".1" (overwriting any prior .1)
// if the current file is over the size threshold. Best-effort; failures
// are silently ignored so a wedged filesystem doesn't keep the binary
// from starting.
func rotateIfLarge(logPath string) {
	info, err := os.Stat(logPath)
	if err != nil || info.Size() < maxLogSizeBytes {
		return
	}
	prev := logPath + ".1"
	_ = os.Remove(prev)
	_ = os.Rename(logPath, prev)
}

func installFallback(cause error) io.Closer {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level()})))
	slog.Warn("debug log unavailable, falling back to stderr", "err", cause)
	return io.NopCloser(os.Stderr)
}

func level() slog.Level {
	switch os.Getenv(debugEnvVar) {
	case "1", "true", "yes", "on":
		return slog.LevelDebug
	}
	return slog.LevelInfo
}

// LogPath returns the absolute path of the debug log file, or an error
// describing why it could not be resolved. Used for diagnostics.
func LogPath() (string, error) {
	dir, err := paths.StateDir()
	if err != nil {
		return "", fmt.Errorf("state dir: %w", err)
	}
	return filepath.Join(dir, "llamaman.log"), nil
}
