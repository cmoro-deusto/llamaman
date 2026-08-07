package tui

import (
	"crypto/sha256"
	hexlib "encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
)

const raceCommit = "68c3ea2061e8c7688455fab07597dde0f4d7f0db"

// TestStorageDownloadRealEngineThroughManager drives a real download
// through the manager while rendering views — a regression guard for
// the bar-render panic (byte-vs-cell width) and for data races
// (run with -race in CI).
func TestStorageDownloadRealEngineThroughManager(t *testing.T) {
	content := strings.Repeat("x", 8<<20) // 8 MiB
	h := sha256.Sum256([]byte(content))
	oid := hexlib.EncodeToString(h[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/refs"):
			json.NewEncoder(w).Encode(map[string]any{
				"branches": []map[string]string{{"name": "main", "targetCommit": raceCommit}},
			})
		case strings.Contains(p, "/tree/"):
			json.NewEncoder(w).Encode([]map[string]any{{
				"type": "file", "path": "model-Q4_K_M.gguf", "size": len(content),
				"lfs": map[string]any{"oid": oid, "size": len(content)},
			}})
		case strings.Contains(p, "/resolve/"):
			flusher, _ := w.(http.Flusher)
			w.Header().Set("Content-Type", "application/octet-stream")
			data := []byte(content)
			for i := 0; i < len(data); i += 64 << 10 {
				end := min(i+64<<10, len(data))
				w.Write(data[i:end])
				if flusher != nil {
					flusher.Flush()
				}
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{ModelsDir: t.TempDir()}
	root := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	root.SetDownloadEngine(hf.NewWithEndpoint(srv.URL, ""))

	driveRoot(t, root,
		tea.WindowSizeMsg{Width: 120, Height: 40},
		keyMsg("s"),
	)
	sm := root.storage

	// start the download directly (skip the input form)
	cmd := sm.startDownload("org/repo", "Q4_K_M")
	_ = cmd
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		sm.Update(dlTickMsg{})
		_ = sm.View() // render while downloading
		if sm.dl != nil && sm.dl.status == dlDone {
			break
		}
		if sm.dl != nil && sm.dl.status == dlFailed {
			t.Fatalf("download failed: %v", sm.dl.err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if sm.dl == nil || sm.dl.status != dlDone {
		t.Fatalf("download did not complete: %+v", sm.dl)
	}
	_ = sm.View()
	_ = fmt.Sprintf
}
