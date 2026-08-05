package server

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func newManagerInTempDir(t *testing.T) *SessionManager {
	t.Helper()
	dir := t.TempDir()
	return &SessionManager{path: filepath.Join(dir, "session.json")}
}

func TestReadEmptyReturnsNil(t *testing.T) {
	m := newManagerInTempDir(t)
	got, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil session, got %+v", got)
	}
}

func TestAcquireWriteUnlockRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock is POSIX-only")
	}
	m := newManagerInTempDir(t)

	existing, err := m.AcquireStart()
	if err != nil {
		t.Fatalf("AcquireStart: %v", err)
	}
	if existing != nil {
		t.Fatalf("expected nil existing, got %+v", existing)
	}

	rec := &Session{
		PID:       os.Getpid(), // alive (this test process)
		Alias:     "qwen",
		Preset:    "default",
		Host:      "127.0.0.1",
		Port:      9080,
		StartedAt: time.Now().UTC(),
		Command:   []string{"/bin/echo"},
		LogPath:   "/tmp/x.log",
	}
	if err := m.WriteAndUnlock(rec); err != nil {
		t.Fatalf("WriteAndUnlock: %v", err)
	}

	got, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected session record after write")
	}
	if got.Alias != "qwen" || got.Port != 9080 || got.PID != os.Getpid() {
		t.Errorf("read mismatch: %+v", got)
	}
}

func TestAcquireFailsWhenAnotherStarterHoldsLock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock is POSIX-only")
	}
	m := newManagerInTempDir(t)

	if _, err := m.AcquireStart(); err != nil {
		t.Fatalf("first AcquireStart: %v", err)
	}
	defer m.Unlock()

	// A second SessionManager pointing at the same file simulates a
	// racing llamaman process.
	other := &SessionManager{path: m.path}
	if _, err := other.AcquireStart(); !errors.Is(err, ErrAnotherStarter) {
		t.Fatalf("second AcquireStart: got %v, want ErrAnotherStarter", err)
	}
}

func TestReadStaleSessionIsCleanedSilently(t *testing.T) {
	m := newManagerInTempDir(t)
	pid := pickDeadPID(t)

	rec := &Session{
		PID:       pid,
		Alias:     "qwen",
		Host:      "127.0.0.1",
		Port:      9080,
		StartedAt: time.Now(),
	}
	// Write directly without locking; mimic a previous run that exited.
	if _, err := m.AcquireStart(); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteAndUnlock(rec); err != nil {
		t.Fatal(err)
	}

	got, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected stale session to be cleaned; got %+v", got)
	}
	if _, err := os.Stat(m.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("session.json should have been removed; stat err=%v", err)
	}
}

func TestIsLive(t *testing.T) {
	if !IsLive(os.Getpid()) {
		t.Fatal("our own PID should be live")
	}
	if IsLive(0) || IsLive(-1) {
		t.Fatal("non-positive PIDs should not be live")
	}
	if IsLive(pickDeadPID(t)) {
		t.Fatal("dead PID should not be live")
	}
}

// pickDeadPID spawns and reaps a no-op child, returning a PID that is
// guaranteed not to belong to a live process at the time of return.
// PID reuse is theoretically possible but extremely unlikely in a CI run.
func pickDeadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("/bin/true")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	return cmd.ProcessState.Pid()
}

func TestSessionKindRoundTrip(t *testing.T) {
	m := newManagerInTempDir(t)
	rec := &Session{
		PID:       os.Getpid(),
		Alias:     "/home/me/my-models.ini",
		Kind:      KindRouter,
		Host:      "127.0.0.1",
		Port:      9080,
		StartedAt: time.Now().UTC(),
		Command:   []string{"/bin/llama-server", "--models-preset", "/home/me/my-models.ini"},
		LogPath:   "/tmp/x.log",
	}
	if _, err := m.AcquireStart(); err != nil {
		t.Fatal(err)
	}
	if err := m.WriteAndUnlock(rec); err != nil {
		t.Fatal(err)
	}
	got, err := m.Read()
	if err != nil || got == nil {
		t.Fatalf("Read: %v, %v", got, err)
	}
	if !got.IsRouter() {
		t.Errorf("IsRouter() = false for Kind=%q", got.Kind)
	}
	if got.Alias != rec.Alias {
		t.Errorf("Alias = %q, want %q", got.Alias, rec.Alias)
	}
}

// TestSessionLegacyWithoutKind verifies old session.json files (written
// before the kind field existed) still parse as single-model sessions.
func TestSessionLegacyWithoutKind(t *testing.T) {
	m := newManagerInTempDir(t)
	legacy := `{"pid": ` + strconv.Itoa(os.Getpid()) + `, "alias": "qwen", "preset": "default", "host": "127.0.0.1", "port": 9080, "started_at": "2026-01-01T00:00:00Z", "command": ["/bin/llama-server", "-m", "m.gguf"], "log_path": "/tmp/x.log"}`
	if _, err := m.AcquireStart(); err != nil {
		t.Fatal(err)
	}
	if err := m.lock.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.lock.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.lock.Write([]byte(legacy)); err != nil {
		t.Fatal(err)
	}
	m.releaseLock()

	got, err := m.Read()
	if err != nil || got == nil {
		t.Fatalf("Read: %v, %v", got, err)
	}
	if got.IsRouter() {
		t.Error("legacy session without kind must not be treated as router")
	}
	if got.Alias != "qwen" || got.Kind != KindSingle {
		t.Errorf("Alias/Kind = %q/%q", got.Alias, got.Kind)
	}
}
