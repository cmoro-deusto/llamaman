package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		chosen:   []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}, {Tag: "Q8_0", Size: 200}},
		progress: []int64{}, // non-nil: the stub fires the progress callback
	}
	r, sm := openStorageRoot(t, eng)

	driveRoot(t, r, keyMsg("d"))
	driveRoot(t, r, keyMsg("org/newrepo"))
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // submit repo input
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // pick Q4_K_M (picker completes)
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // drain the completion tick

	want := "Download org/newrepo Q4_K_M"
	if len(eng.calls) == 0 || eng.calls[len(eng.calls)-1] != want {
		t.Fatalf("engine calls = %v, want last %q", eng.calls, want)
	}
	if sm.dl == nil || sm.dl.repo != "org/newrepo" || sm.dl.quant != "Q4_K_M" {
		t.Fatalf("download state = %+v", sm.dl)
	}
	// the stub engine completes instantly: the download is done, the
	// row auto-dismisses, and the flash announces it.
	if sm.dl.status != dlDone {
		t.Fatalf("download status = %v, want done", sm.dl.status)
	}
	if !strings.Contains(stripANSI(sm.View()), "downloaded org/newrepo") {
		t.Errorf("done flash missing from view:\n%s", stripANSI(sm.View()))
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

// TestStorageDoneDownloadBecomesCacheRow: after a successful download
// the manager lists the repo as a normal cache row (quants, size,
// actions) and no longer renders a download row or a done line.
func TestStorageDoneDownloadBecomesCacheRow(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	// simulate a finished download of a second repo
	repoDir := filepath.Join(sm.root, storage.RepoFolderNames("org/finished")[0])
	oid := "50d019817c2626eb9e8a41f361ff5bfa538757e6f708a3076cd3356354a75694"
	if err := os.MkdirAll(filepath.Join(repoDir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "blobs", oid), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repoDir, "snapshots", "aa"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", "..", "blobs", oid), filepath.Join(repoDir, "snapshots", "aa", "finished-Q8_0.gguf")); err != nil {
		t.Fatal(err)
	}

	sm.dl = &downloadState{repo: "org/finished", quant: "Q8_0", status: dlDone, total: 1, doneAt: time.Now()}
	sm.rebuild()

	out := stripANSI(sm.View())
	if !strings.Contains(out, "org/finished") || !strings.Contains(out, "Q8_0") {
		t.Errorf("finished repo must appear as a cache row with its quant:\n%s", out)
	}
	if strings.Contains(out, "org/finished:Q8_0") {
		t.Errorf("download row must be gone after completion:\n%s", out)
	}
	if strings.Contains(out, "— done") {
		t.Errorf("done line must not render:\n%s", out)
	}
	// the cache row has actions (openMenu on it offers download/delete)
	sm.cursor = 0 // org/cachedrepo sorts first; find the finished row
	for i, e := range sm.entries {
		if e.title == "org/finished" {
			sm.cursor = i
		}
	}
	cmd := sm.openMenu()
	if cmd == nil {
		t.Fatal("finished cache row must open an action menu")
	}
}

// TestStorageNavDismissesDoneDownload: pressing a navigation key after
// a finished download clears the download line and its flash.
func TestStorageNavDismissesDoneDownload(t *testing.T) {
	eng := &stubEngine{}
	r, sm := openStorageRoot(t, eng)
	sm.dl = &downloadState{repo: "org/finished", quant: "Q8_0", status: dlDone, doneAt: time.Now()}
	sm.flash = "downloaded org/finished:Q8_0"
	sm.flashAt = time.Now()
	sm.rebuild()
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyDown})
	if sm.dl != nil {
		t.Errorf("done download must be dismissed on navigation, got %+v", sm.dl)
	}
	if sm.flash != "" {
		t.Errorf("flash must clear on navigation, got %q", sm.flash)
	}
}

// TestStorageFooterOrder: the footer shows cache root on the left and
// free disk on the right.
func TestStorageFooterOrder(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	out := stripANSI(sm.View())
	rootIdx := strings.Index(out, "cache root:")
	freeIdx := strings.Index(out, "free disk:")
	if rootIdx < 0 || freeIdx < 0 || rootIdx > freeIdx {
		t.Errorf("footer must be 'cache root: … · free disk: …':\n%s", out)
	}
}

// TestStorageMissingModelShowsWarning: a local model whose file is
// absent renders "missing" (and the reveal target is the path, not the
// whole detail line).
func TestStorageMissingModelShowsWarning(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	out := stripANSI(sm.View())
	if !strings.Contains(out, "missing") {
		t.Errorf("missing model must show 'missing':\n%s", out)
	}
	// find a missing local-model entry and check its reveal path
	for _, e := range sm.entries {
		if e.kind == entryLocalModel && e.missing {
			if e.path == "" || !strings.Contains(e.path, ".gguf") {
				t.Errorf("missing entry must carry its path, got %+v", e)
			}
			return
		}
	}
	t.Error("no missing local model entry found")
}

// TestStorageCachedQuantMarked: the quant picker labels quants already
// on disk with "(cached)".
func TestStorageCachedQuantMarked(t *testing.T) {
	eng := &stubEngine{
		chosen: []hf.QuantOption{
			{Tag: "Q4_K_M", Size: 100}, // cached (fixture repo)
			{Tag: "Q8_0", Size: 200},   // not cached
		},
	}
	r, sm := openStorageRoot(t, eng)
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // open action menu on the cached repo row
		tea.KeyMsg{Type: tea.KeyEnter}, // choose "download now" (picker opens)
	)
	if sm.quantForm == nil {
		t.Fatalf("quant picker did not open:\n%s", stripANSI(sm.View()))
	}
	out := stripANSI(sm.View())
	if !strings.Contains(out, "Q4_K_M —") || !strings.Contains(out, "(cached)") {
		t.Errorf("cached quant must be marked:\n%s", out)
	}
	if strings.Contains(out, "Q8_0 — 200 (cached)") {
		t.Errorf("uncached quant must not be marked cached:\n%s", out)
	}
}

// TestStorageDeleteMissingModelFromConfig: a missing local model offers
// "delete from config"; confirming removes the entry and persists.
func TestStorageDeleteMissingModelFromConfig(t *testing.T) {
	cfg, _ := storageTestConfig(t) // alpha/beta locations do not exist → missing
	path := filepath.Join(t.TempDir(), "config.json")
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	r := NewRoot(cfg, path, stubSpawner{}, nil, "v0.0.0-test", nil)
	r.SetDownloadEngine(&stubEngine{})
	driveRoot(t, r, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("s"))
	sm := r.storage

	// move the cursor to the first missing local model (alpha)
	idx := -1
	for i, e := range sm.entries {
		if e.kind == entryLocalModel && e.title == "alpha" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("alpha (missing local model) not found in the listing")
	}
	sm.cursor = idx

	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // open the action menu
	out := stripANSI(sm.View())
	if !strings.Contains(out, "delete from config") {
		t.Fatalf("missing model menu must offer 'delete from config':\n%s", out)
	}
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // choose → confirm opens
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyRight}) // confirm: move to Yes
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // confirm: submit Yes

	for _, m := range r.cfg.Models {
		if m.Alias == "alpha" {
			t.Errorf("alpha must be removed from the live config")
		}
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range loaded.Models {
		if m.Alias == "alpha" {
			t.Errorf("alpha must be removed on disk")
		}
	}
	if !strings.Contains(stripANSI(sm.View()), "removed alpha from config") {
		t.Errorf("confirmation flash missing:\n%s", stripANSI(sm.View()))
	}
}

// TestStorageRepoFormShowsLongIdTail: the download-now input is sized
// to the window; typing a ~90-char repo id must keep the typed tail
// visible (the textinput scrolls to the caret).
func TestStorageRepoFormShowsLongIdTail(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	cmd := sm.openRepoForm()
	_ = cmd
	long := "DavidAU/Qwen3.6-27B-Fable-Fusion-711-Uncensored-Heretic-NM-DAU-NEO-MAX-MTP-GGUF:Q4_K_M"
	sm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(long)})
	rendered := sm.form.View()
	tail := long[len(long)-12:]
	if !strings.Contains(rendered, tail) {
		t.Errorf("typed tail %q must be visible in the input:\n%s", tail, rendered)
	}
}

// TestStorageFlashExpiresWithoutDownload: a status flash (e.g. "removed
// from config") auto-expires via its own timer even when no download is
// running, and navigation dismisses it immediately.
func TestStorageFlashExpiresWithoutDownload(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	cmd := sm.setFlash("removed alpha from config")
	if cmd == nil {
		t.Fatal("setFlash must arm an expiry timer")
	}
	if sm.flash != "removed alpha from config" {
		t.Fatalf("flash = %q", sm.flash)
	}
	sm.Update(stgFlashExpiredMsg{})
	if sm.flash != "" {
		t.Errorf("flash must clear on expiry, got %q", sm.flash)
	}

	// and navigation dismisses any flash
	sm.setFlash("a message")
	r, _ := openStorageRoot(t, &stubEngine{})
	_ = r
	driveStorage := func(k tea.KeyMsg) {
		sm.Update(k)
	}
	driveStorage(tea.KeyMsg{Type: tea.KeyDown})
	if sm.flash != "" {
		t.Errorf("flash must clear on navigation, got %q", sm.flash)
	}
}

// TestStorageSpeedWindowed: the speed readout is computed over a ~2s
// window (not per-tick deltas) and does not jitter without new data.
func TestStorageSpeedWindowed(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	sm.dl = &downloadState{repo: "org/r", quant: "Q4_K_M", status: dlRunning, prog: &progressSlot{}}
	sm.dl.speedWinAt = time.Now().Add(-2 * time.Second)
	sm.dl.speedWinDone = 0
	sm.dl.prog.set(2<<20, 1<<30) // 2 MiB fetched over the last 2s

	sm.Update(dlTickMsg{})
	if want := int64(1 << 20); sm.dl.speed < want-1024 || sm.dl.speed > want+1024 {
		t.Errorf("speed = %d, want ~%d (2 MiB / 2s)", sm.dl.speed, want)
	}

	// no new progress: the window does not advance, speed stays put
	sm.Update(dlTickMsg{})
	if want := int64(1 << 20); sm.dl.speed < want-1024 || sm.dl.speed > want+1024 {
		t.Errorf("speed changed without new data: %d, want ~%d", sm.dl.speed, want)
	}
}

// TestProgressSlotKeepsLatest: the latest progress snapshot wins — no
// drops, unlike a bounded channel.
func TestProgressSlotKeepsLatest(t *testing.T) {
	slot := &progressSlot{}
	for i := int64(0); i < 1000; i++ {
		slot.set(i, 1000)
	}
	done, total := slot.get()
	if done != 999 || total != 1000 {
		t.Errorf("slot = %d/%d, want 999/1000 (latest wins)", done, total)
	}
}
