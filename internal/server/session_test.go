package server

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
