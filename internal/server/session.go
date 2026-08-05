package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/cmoro-deusto/llamaman/internal/paths"
)

// Session kinds stored in the `kind` field. KindSingle ("") is a legacy
// single-model session; KindRouter hosts a whole my-models.ini file via
// llama-server --models-preset.
const (
	KindSingle = ""
	KindRouter = "router"
)

// Session is the on-disk record at $XDG_RUNTIME_DIR/llamaman/session.json
// (DESIGN.md §5.2). Reattaching processes parse it to find the live child;
// owners overwrite it on every spawn.
type Session struct {
	PID       int       `json:"pid"`
	Alias     string    `json:"alias"`
	Preset    string    `json:"preset"`
	Kind      string    `json:"kind,omitempty"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	StartedAt time.Time `json:"started_at"`
	Command   []string  `json:"command"`
	LogPath   string    `json:"log_path"`
}

// IsRouter reports whether this session runs llama-server in router mode
// (one process hosting every model of a my-models.ini file).
func (s Session) IsRouter() bool { return s.Kind == KindRouter }

// SessionManager mediates access to session.json. Reads are unlocked.
// Writes are gated by an exclusive flock on the file itself, so racing
// llamaman starters serialize their decisions atomically.
type SessionManager struct {
	path string
	lock *os.File // open + locked while we hold the start lock
}

// ErrAnotherStarter is returned by AcquireStart when another llamaman
// process is currently performing the spawn dance.
var ErrAnotherStarter = errors.New("another llamaman starter holds the session lock")

// NewSessionManager resolves the session path and ensures the directory
// exists.
func NewSessionManager() (*SessionManager, error) {
	dir, err := paths.RuntimeDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir runtime: %w", err)
	}
	return &SessionManager{path: filepath.Join(dir, "session.json")}, nil
}

// Path returns the absolute path of session.json. Useful for diagnostics.
func (m *SessionManager) Path() string { return m.path }

// Read returns the current session if any, treating a stale (PID-dead) or
// corrupt file as no-session and silently cleaning it up.
func (m *SessionManager) Read() (*Session, error) {
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read session: %w", err)
	}
	if len(data) == 0 {
		// File exists but empty (e.g. created during a lock acquisition
		// that never wrote). Treat as no-session.
		return nil, nil
	}
	var s Session
	if err := json.Unmarshal(data, &s); err != nil {
		_ = os.Remove(m.path)
		return nil, nil
	}
	if !IsLive(s.PID) {
		_ = os.Remove(m.path)
		return nil, nil
	}
	return &s, nil
}

// AcquireStart opens session.json, flocks it non-blocking, and returns the
// existing session if one is alive (caller should reattach) or nil if the
// caller should spawn. Holds the lock until WriteAndUnlock or Unlock is
// called.
//
// If another starter holds the lock the call returns ErrAnotherStarter
// without side effects.
func (m *SessionManager) AcquireStart() (*Session, error) {
	if m.lock != nil {
		return nil, fmt.Errorf("session lock already held")
	}
	f, err := os.OpenFile(m.path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAnotherStarter
		}
		return nil, fmt.Errorf("flock session: %w", err)
	}
	// Re-check contents now that we own the lock.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
		return nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		f.Close()
		return nil, err
	}
	m.lock = f
	if len(data) > 0 {
		var s Session
		if err := json.Unmarshal(data, &s); err == nil && IsLive(s.PID) {
			return &s, nil
		}
	}
	return nil, nil
}

// WriteAndUnlock persists the session record and releases the start lock.
// Must be called while the caller holds the lock from AcquireStart.
func (m *SessionManager) WriteAndUnlock(s *Session) error {
	if m.lock == nil {
		return fmt.Errorf("not holding the session lock")
	}
	defer m.releaseLock()

	out, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if _, err := m.lock.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := m.lock.Truncate(0); err != nil {
		return err
	}
	if _, err := m.lock.Write(out); err != nil {
		return err
	}
	return m.lock.Sync()
}

// Unlock drops the start lock without writing. Used when AcquireStart told
// us a session was already running; caller falls through to reattach.
func (m *SessionManager) Unlock() {
	if m.lock != nil {
		m.releaseLock()
	}
}

func (m *SessionManager) releaseLock() {
	_ = unix.Flock(int(m.lock.Fd()), unix.LOCK_UN)
	_ = m.lock.Close()
	m.lock = nil
}

// Clear deletes session.json. Safe to call when no session exists.
func (m *SessionManager) Clear() error {
	err := os.Remove(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// IsLive reports whether the given PID exists and is reachable. Uses
// signal 0, which is a permission check on Linux: returns nil if the PID
// is alive and we can signal it (or if it's a zombie owned by us).
func IsLive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}
