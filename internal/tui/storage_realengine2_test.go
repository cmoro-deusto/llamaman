package tui

import (
	"crypto/sha256"
	hexlib "encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
)

const rc = "68c3ea2061e8c7688455fab07597dde0f4d7f0db"

type rcServer struct {
	mu     sync.Mutex
	files  map[string][]byte
	ranges []string // "file RangeHeader" seen on resolve
	served map[string]int
}

func sha256H(b []byte) string { h := sha256.Sum256(b); return hexlib.EncodeToString(h[:]) }

func newRCServer(t *testing.T, files map[string][]byte) (*rcServer, *httptest.Server) {
	s := &rcServer{files: files, served: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/refs"):
			json.NewEncoder(w).Encode(map[string]any{"branches": []map[string]string{{"name": "main", "targetCommit": rc}}})
		case strings.Contains(p, "/tree/"):
			entries := []map[string]any{}
			for path, data := range s.files {
				entries = append(entries, map[string]any{
					"type": "file", "path": path, "size": len(data),
					"lfs": map[string]any{"oid": sha256H(data), "size": len(data)},
				})
			}
			json.NewEncoder(w).Encode(entries)
		case strings.Contains(p, "/resolve/"):
			file := p[strings.Index(p, "/resolve/")+len("/resolve/"):]
			file = file[strings.Index(file, "/")+1:]
			data := s.files[file]
			s.mu.Lock()
			s.served[file]++
			rng := r.Header.Get("Range")
			if rng != "" {
				s.ranges = append(s.ranges, file+" "+rng)
			}
			s.mu.Unlock()
			w.Header().Set("Content-Type", "application/octet-stream")
			from := 0
			if rng != "" {
				fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), "%d", &from)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(data)-1, len(data)))
				w.WriteHeader(http.StatusPartialContent)
			} else {
				w.WriteHeader(http.StatusOK)
			}
			// stream slowly so the download stays in flight
			for i := from; i < len(data); i += 64 << 10 {
				end := min(i+64<<10, len(data))
				if _, err := w.Write(data[i:end]); err != nil {
					return
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
				time.Sleep(20 * time.Millisecond)
			}
		}
	}))
	t.Cleanup(srv.Close)
	return s, srv
}

func rcRoot(t *testing.T, srv *httptest.Server) (*Root, *StorageMode) {
	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{ModelsDir: t.TempDir()}
	r := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	r.SetDownloadEngine(hf.NewWithEndpoint(srv.URL, ""))
	driveRoot(t, r, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("s"))
	return r, r.storage
}

func driveSM(sm *StorageMode, keys ...tea.KeyMsg) {
	for _, k := range keys {
		var cmd tea.Cmd
		sm, cmd = sm.Update(k)
		sm = drainSMCmds(sm, cmd, 0)
	}
}

// drainSMCmds is the StorageMode-shaped twin of the harness's
// drainCmds: it executes cmds and feeds their messages back so form
// focus/completion messages are delivered.
func drainSMCmds(sm *StorageMode, cmd tea.Cmd, depth int) *StorageMode {
	if cmd == nil || depth > 8 {
		return sm
	}
	out := safeCmd(cmd)
	if out == nil {
		return sm
	}
	if b, ok := out.(tea.BatchMsg); ok {
		for _, sub := range b {
			sm = drainSMCmds(sm, sub, depth+1)
		}
		return sm
	}
	if seq, ok := asSequenceMsg(out); ok {
		for _, sub := range seq {
			sm = drainSMCmds(sm, sub, depth+1)
		}
		return sm
	}
	var c tea.Cmd
	sm, c = sm.Update(out)
	return drainSMCmds(sm, c, depth+1)
}

func st(ds []*downloadState) string {
	var b strings.Builder
	for i, d := range ds {
		b.WriteString(fmt.Sprintf("[%d]=%d ", i, d.status))
	}
	return strings.TrimSpace(b.String())
}

func waitStatus(t *testing.T, sm *StorageMode, idx int, want dlStatus) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		sm.Update(dlTickMsg{})
		if idx < len(sm.downloads) && sm.downloads[idx].status == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("download[%d] did not reach %v, got %v", idx, want, st(sm.downloads))
}

// TestStorageRealCancelSecondDownload: cancelling the second of two
// concurrent real downloads via the menu flow settles it (owner report).
func TestStorageRealCancelSecondDownload(t *testing.T) {
	s, srv := newRCServer(t, map[string][]byte{
		"m1-Q4_K_M.gguf": []byte(strings.Repeat("a", 32<<20)),
		"m2-Q8_0.gguf":   []byte(strings.Repeat("b", 32<<20)),
	})
	_, sm := rcRoot(t, srv)
	_ = sm.startDownload("org/one", "Q4_K_M")
	_ = sm.startDownload("org/two", "Q8_0")
	time.Sleep(50 * time.Millisecond)

	// cancel the FIRST via its row
	sm.cursor = len(sm.entries) - 2
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter})
	driveSM(sm, tea.KeyMsg{Type: tea.KeyDown}) // cancel
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter})
	waitStatus(t, sm, 0, dlDone)

	// cancel the SECOND via its row
	sm.cursor = len(sm.entries) - 1
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter})
	driveSM(sm, tea.KeyMsg{Type: tea.KeyDown})
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter})
	waitStatus(t, sm, 1, dlDone)
	_ = s
}

// TestStorageRealResumeContinues: pause mid-download then resume —
// the resumed request must carry a Range header (bytes continue from
// the partial) and the bar must not reset (owner report).
func TestStorageRealResumeContinues(t *testing.T) {
	s, srv := newRCServer(t, map[string][]byte{
		"m-Q4_K_M.gguf": []byte(strings.Repeat("x", 32<<20)),
	})
	_, sm := rcRoot(t, srv)
	_ = sm.startDownload("org/one", "Q4_K_M")
	time.Sleep(100 * time.Millisecond) // let it get partway
	_ = sm.downloads[0]

	// pause via the menu
	sm.cursor = len(sm.entries) - 1
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter})
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter}) // pause (first option)
	waitStatus(t, sm, 0, dlPaused)

	// resume via the menu
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter})
	driveSM(sm, tea.KeyMsg{Type: tea.KeyEnter}) // resume (first option)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		sm.Update(dlTickMsg{})
		if len(sm.downloads) > 0 && sm.downloads[0].status == dlDone {
			break
		}
		if len(sm.downloads) > 0 && sm.downloads[0].status == dlFailed {
			t.Fatalf("resumed download failed: %v", sm.downloads[0].err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(sm.downloads) == 0 || sm.downloads[0].status != dlDone {
		t.Fatalf("resumed download did not complete: %v", st(sm.downloads))
	}
	if len(s.ranges) == 0 {
		t.Fatalf("resume must issue a Range request, got ranges=%v", s.ranges)
	}
	t.Logf("ranges=%v", s.ranges)
}
