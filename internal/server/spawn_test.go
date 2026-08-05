package server

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
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

// TestSpawnRouterWithFakeServer spawns the fakeserver in router mode
// (--models-preset), verifies the /models and /health endpoints serve
// the router's model list, and stops the process.
func TestSpawnRouterWithFakeServer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Setsid is Linux-only")
	}
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	requireBuilt(t, bin)

	ini := filepath.Join(t.TempDir(), "my-models.ini")
	if err := os.WriteFile(ini, []byte("[model-a]\nmodel = a.gguf\n[model-b]\nhf = org/repo:Q4_0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	logPath := filepath.Join(t.TempDir(), "llama.log")
	argv := []string{bin, "--models-preset", ini, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--ready-delay=50ms"}
	p, err := Spawn(argv, logPath)
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	t.Cleanup(func() { p.Stop(2 * time.Second) })

	base := "http://127.0.0.1:" + strconv.Itoa(port)
	waitForHTTP(t, base+"/models")

	// /models → OpenAI-style list with both router models.
	var models struct {
		Object string `json:"object"`
		Data   []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	getJSON(t, base+"/models", &models)
	if models.Object != "list" || len(models.Data) != 2 {
		t.Fatalf("/models = %+v, want object=list with 2 entries", models)
	}
	if models.Data[0].ID != "model-a" || models.Data[1].ID != "model-b" {
		t.Errorf("/models ids = %q, %q", models.Data[0].ID, models.Data[1].ID)
	}

	// /health → status ok with the router's model ids.
	var health struct {
		Status string   `json:"status"`
		Models []string `json:"models"`
	}
	getJSON(t, base+"/health", &health)
	if health.Status != "ok" || len(health.Models) != 2 {
		t.Fatalf("/health = %+v, want status=ok with 2 models", health)
	}

	p.Stop(2 * time.Second)
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("router fakeserver did not exit within 3s of Stop")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func waitForHTTP(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("endpoint %s did not become ready within 5s", url)
}

func getJSON(t *testing.T, url string, dst any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(dst); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
}
