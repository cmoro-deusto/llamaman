package server

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestSpawnAndTailWithFakeServer runs the fakeserver binary, tails its log
// file, and verifies the readiness line appears.
func TestSpawnAndTailWithFakeServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Setsid is Linux-only")
	}
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	requireBuilt(t, bin)

	logPath := filepath.Join(t.TempDir(), "llama.log")
	p, err := Spawn([]string{bin, "--ready-delay=50ms"}, logPath)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { p.Stop(2 * time.Second) })

	tail, err := NewTailer(logPath)
	if err != nil {
		t.Fatalf("NewTailer: %v", err)
	}
	t.Cleanup(tail.Close)

	deadline := time.After(5 * time.Second)
	var seen strings.Builder
	for {
		select {
		case chunk := <-tail.Chunks():
			seen.WriteString(chunk)
			if strings.Contains(seen.String(), "server is listening") {
				return
			}
		case <-deadline:
			t.Fatalf("did not see readiness line within 5s; got:\n%s", seen.String())
		}
	}
}

func TestStopSendsSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Setsid is Linux-only")
	}
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	requireBuilt(t, bin)

	logPath := filepath.Join(t.TempDir(), "llama.log")
	p, err := Spawn([]string{bin, "--ready-delay=10ms"}, logPath)
	if err != nil {
		t.Fatal(err)
	}
	// Give it a tick to enter the main loop, then ask it to stop.
	time.Sleep(100 * time.Millisecond)
	p.Stop(2 * time.Second)

	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("process did not exit within 3s of Stop")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Walk up from the package directory until we find go.mod.
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := filepath.Glob(filepath.Join(dir, "go.mod")); err == nil {
			matches, _ := filepath.Glob(filepath.Join(dir, "go.mod"))
			if len(matches) > 0 {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find go.mod above %s", dir)
		}
		dir = parent
	}
}

func requireBuilt(t *testing.T, bin string) {
	t.Helper()
	if _, err := filepath.Abs(bin); err != nil {
		t.Fatalf("invalid path %s: %v", bin, err)
	}
	matches, _ := filepath.Glob(bin)
	if len(matches) == 0 {
		t.Skipf("%s not built; run: go build -o bin/llamaman-fakeserver ./cmd/llamaman-fakeserver", bin)
	}
}
