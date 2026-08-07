package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// stubEngine is the injectable download engine (DESIGN §16.4 P9): it
// records calls and lets tests control Choose results, Download
// behavior, and completion.
type stubEngine struct {
	chosen   []hf.QuantOption
	dlErr    error
	calls    []string // "Download repo quant" records
	progress []int64
	// blockCh, when non-nil, makes Download wait until it is closed
	// (or the ctx is cancelled).
	blockCh chan struct{}
}

func (e *stubEngine) Choose(_ context.Context, repo string) ([]hf.QuantOption, error) {
	e.calls = append(e.calls, "Choose "+repo)
	return e.chosen, nil
}

func (e *stubEngine) Download(ctx context.Context, root, repo, quant string, progress func(done, total int64)) error {
	e.calls = append(e.calls, "Download "+repo+" "+quant)
	if e.blockCh != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-e.blockCh:
		}
	}
	if e.progress != nil {
		progress(0, 100)
		progress(50, 100)
		progress(100, 100)
	}
	return e.dlErr
}

// storageTestConfig returns a config with a local model and a temp
// cache root with one hub-layout repo (so the listing has rows).
func storageTestConfig(t *testing.T) (*config.Config, string) {
	t.Helper()
	root := t.TempDir()
	// fake hub-layout cached repo
	repoDir := filepath.Join(root, storage.RepoFolderNames("org/cachedrepo")[0])
	if err := os.MkdirAll(filepath.Join(repoDir, "snapshots", "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("model-bytes")
	blobDir := filepath.Join(repoDir, "blobs")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}
	oid := "50d019817c2626eb9e8a41f361ff5bfa538757e6f708a3076cd3356354a75694"
	if err := os.WriteFile(filepath.Join(blobDir, oid), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", oid), filepath.Join(repoDir, "snapshots", "aa", "model-Q4_K_M.gguf")); err != nil {
		t.Fatal(err)
	}

	cfg := sampleSnapshotConfig()
	cfg.Preferences = &config.Preferences{ModelsDir: root}
	return cfg, root
}

func openStorageRoot(t *testing.T, eng downloadEngine) (*Root, *StorageMode) {
	t.Helper()
	cfg, root := storageTestConfig(t)
	r := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	r.SetDownloadEngine(eng)
	driveRoot(t, r, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("s"))
	if r.view != ViewStorage || r.storage == nil {
		t.Fatalf("view = %d, storage nil? %v", r.view, r.storage == nil)
	}
	if r.storage.root != root {
		t.Fatalf("cache root = %q, want %q", r.storage.root, root)
	}
	return r, r.storage
}

// TestStorageOpensFromMain: `s` switches to the manager, listing the
// cached repo, the local model, and the free-disk line.
func TestStorageOpensFromMain(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	out := stripANSI(sm.View())
	for _, want := range []string{
		"Storage & Downloads",
		"org/cachedrepo",
		"Q4_K_M",
		"alpha", // local config model
		"free disk:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("manager view missing %q\nout:\n%s", want, out)
		}
	}
}

// TestStorageEscReturnsToMain: esc leaves the manager.
func TestStorageEscReturnsToMain(t *testing.T) {
	r, _ := openStorageRoot(t, &stubEngine{})
	driveRoot(t, r, keyMsg("esc"))
	if r.view != ViewMain {
		t.Fatalf("view = %d, want ViewMain", r.view)
	}
}

// TestStorageDownloadNow: d → repo input → submit → quant picker →
// select → engine.Download called with the right root/repo/quant, and
// the row becomes an in-flight download.
func TestStorageDownloadNow(t *testing.T) {
	eng := &stubEngine{
		chosen: []hf.QuantOption{
			{Tag: "Q4_K_M", Size: 100},
			{Tag: "Q8_0", Size: 200},
		},
	}
	r, sm := openStorageRoot(t, eng)

	driveRoot(t, r,
		keyMsg("d"),
		keyMsg("org/newrepo"),
		tea.KeyMsg{Type: tea.KeyEnter}, // submit repo input
		tea.KeyMsg{Type: tea.KeyEnter}, // quant select: choose Q4_K_M
		tea.KeyMsg{Type: tea.KeyEnter}, // quant select: submit form
	)

	want := "Download org/newrepo Q4_K_M"
	if len(eng.calls) == 0 || eng.calls[len(eng.calls)-1] != want {
		t.Fatalf("engine calls = %v, want last %q", eng.calls, want)
	}
	if sm.dl == nil || sm.dl.repo != "org/newrepo" || sm.dl.quant != "Q4_K_M" {
		t.Fatalf("download state = %+v", sm.dl)
	}
	if !strings.Contains(stripANSI(sm.View()), "org/newrepo:Q4_K_M") {
		t.Errorf("download row missing from view:\n%s", stripANSI(sm.View()))
	}
}

// TestStorageDownloadWithQuantSuffixSkipsPicker: repo input with an
// explicit :quant starts the download directly.
func TestStorageDownloadWithQuantSuffixSkipsPicker(t *testing.T) {
	eng := &stubEngine{}
	r, _ := openStorageRoot(t, eng)
	driveRoot(t, r,
		keyMsg("d"),
		keyMsg("org/newrepo:Q8_0"),
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if len(eng.calls) == 0 || eng.calls[len(eng.calls)-1] != "Download org/newrepo Q8_0" {
		t.Fatalf("engine calls = %v", eng.calls)
	}
}

// TestStorageDownloadPauseResumeCancel: a blocking download pauses
// (ctx cancel), resumes (new Download call), and cancels (partials
// removed).
func TestStorageDownloadPauseResumeCancel(t *testing.T) {
	eng := &stubEngine{blockCh: make(chan struct{})}
	r, sm := openStorageRoot(t, eng)
	driveRoot(t, r,
		keyMsg("d"),
		keyMsg("org/newrepo:Q4_K_M"),
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if sm.dl == nil || sm.dl.status != dlRunning {
		t.Fatalf("download not running: %+v", sm.dl)
	}

	// pause via the download row's action menu (select: choose + submit)
	sm.cursor = len(sm.entries) - 1 // the download row (sorted last)
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // open menu
		tea.KeyMsg{Type: tea.KeyEnter}, // choose pause
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu
	)
	// the blocking Download sees the cancel and finishes as paused
	sm.handleDlFinished(context.Canceled)
	if sm.dl == nil || sm.dl.status != dlPaused {
		t.Fatalf("after pause: %+v", sm.dl)
	}

	// resume (menu now offers resume for the paused row)
	sm.cursor = len(sm.entries) - 1
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // open menu
		tea.KeyMsg{Type: tea.KeyEnter}, // choose resume
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu
	)
	if len(eng.calls) < 2 || eng.calls[len(eng.calls)-1] != "Download org/newrepo Q4_K_M" {
		t.Fatalf("resume must re-invoke Download: %v", eng.calls)
	}

	// cancel: seed a partial first, then cancel
	partial := filepath.Join(sm.root, storage.RepoFolderNames("org/newrepo")[0], "blobs", "x.incomplete")
	if err := os.MkdirAll(filepath.Dir(partial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	sm.cancelDownload()
	sm.handleDlFinished(context.Canceled)
	if sm.dl != nil {
		t.Fatalf("after cancel dl = %+v, want nil", sm.dl)
	}
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Errorf("cancel must remove partials, stat err = %v", err)
	}
}

// TestStorageDelete: deleting a cache repo with confirmation removes
// the hub folder and the row disappears.
func TestStorageDelete(t *testing.T) {
	eng := &stubEngine{}
	r, sm := openStorageRoot(t, eng)
	hubDir := filepath.Join(sm.root, storage.RepoFolderNames("org/cachedrepo")[0])
	if _, err := os.Stat(hubDir); err != nil {
		t.Fatalf("fixture repo missing: %v", err)
	}

	// cursor starts on the first (cache repo) row — delete it
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // open action menu
		tea.KeyMsg{Type: tea.KeyDown},  // choose delete
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu (confirm opens)
		tea.KeyMsg{Type: tea.KeyRight}, // toggle confirm to Yes
		tea.KeyMsg{Type: tea.KeyEnter}, // confirm: choose Yes
		tea.KeyMsg{Type: tea.KeyEnter}, // confirm: submit
	)
	if _, err := os.Stat(hubDir); !os.IsNotExist(err) {
		t.Errorf("hub dir must be removed after delete, stat err = %v", err)
	}
	if strings.Contains(stripANSI(sm.View()), "> org/cachedrepo") {
		t.Errorf("deleted repo row should be gone from the listing:\n%s", stripANSI(sm.View()))
	}
}

// TestStorageDeleteDeclined: answering no keeps the repo.
func TestStorageDeleteDeclined(t *testing.T) {
	eng := &stubEngine{}
	r, sm := openStorageRoot(t, eng)
	hubDir := filepath.Join(sm.root, storage.RepoFolderNames("org/cachedrepo")[0])

	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // open action menu
		tea.KeyMsg{Type: tea.KeyDown},  // choose delete
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu (confirm opens)
		tea.KeyMsg{Type: tea.KeyEnter}, // confirm: keep No
		tea.KeyMsg{Type: tea.KeyEnter}, // confirm: submit
	)
	if _, err := os.Stat(hubDir); err != nil {
		t.Errorf("declined delete must keep the repo, stat err = %v", err)
	}
}

// TestDownloadLineWidthStable pins the flicker report: the download
// progress line must keep a constant rendered width as done grows (the
// centered view re-centers on every width change, which flickers).
func TestDownloadLineWidthStable(t *testing.T) {
	cfg := sampleSnapshotConfig()
	sm := NewStorageMode(cfg, DefaultTheme(), t.TempDir())
	sm.dl = &downloadState{repo: "org/repo", quant: "Q4_K_M", status: dlRunning, total: 15 << 30}
	widths := map[int64]int{}
	for _, done := range []int64{0, 1 << 10, 512 << 20, 999 << 20, 1 << 30, 15 << 30} {
		sm.dl.done = done
		widths[done] = len(stripANSI(sm.renderDownload()))
	}
	first := widths[0]
	for done, w := range widths {
		if w != first {
			t.Errorf("width at done=%d is %d, want %d (stable)", done, w, first)
		}
	}
}
