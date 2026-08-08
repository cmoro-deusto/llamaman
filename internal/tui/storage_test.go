package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"

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

// firstDL returns the first download entry (tests drive single
// downloads; several run concurrently in production).
func firstDL(sm *StorageMode) *downloadState {
	if len(sm.downloads) == 0 {
		return nil
	}
	return sm.downloads[0]
}

// mkDL builds a download entry with a usable spinner (renderDownloads
// renders d.spinner even when running).
func mkDL(repo, quant string, status dlStatus) *downloadState {
	return &downloadState{
		repo: repo, quant: quant, status: status,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
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
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // pick Q4_K_M (picker completes, tick drains)

	want := "Download org/newrepo Q4_K_M"
	if len(eng.calls) == 0 || eng.calls[len(eng.calls)-1] != want {
		t.Fatalf("engine calls = %v, want last %q", eng.calls, want)
	}
	d := firstDL(sm)
	if d == nil || d.repo != "org/newrepo" || d.quant != "Q4_K_M" {
		t.Fatalf("download state = %+v", sm.downloads)
	}
	// the stub engine completes instantly: the download is done, the
	// row auto-dismisses, and the flash announces it.
	if d.status != dlDone {
		t.Fatalf("download status = %v, want done", d.status)
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
	if d := firstDL(sm); d == nil || d.status != dlRunning {
		t.Fatalf("download not running: %+v", sm.downloads)
	}

	// pause via the download row's action menu (select: choose + submit)
	sm.cursor = len(sm.entries) - 1 // the download row (sorted last)
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // open menu
		tea.KeyMsg{Type: tea.KeyEnter}, // choose pause
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu
	)
	// the blocking Download sees the cancel and finishes as paused
	sm.handleDlFinished(firstDL(sm), context.Canceled)
	if d := firstDL(sm); d == nil || d.status != dlPaused {
		t.Fatalf("after pause: %+v", sm.downloads)
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
	d := firstDL(sm)

	// cancel: seed a partial first, then cancel
	partial := filepath.Join(sm.root, storage.RepoFolderNames("org/newrepo")[0], "blobs", "x.incomplete")
	if err := os.MkdirAll(filepath.Dir(partial), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partial, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	sm.cancelDownload(d)
	sm.handleDlFinished(d, context.Canceled)
	if d.status != dlDone {
		t.Fatalf("cancelled entry must be marked done, got %+v", d)
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
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu (scope menu opens)
		tea.KeyMsg{Type: tea.KeyEnter}, // scope: delete all files (confirm opens)
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
		tea.KeyMsg{Type: tea.KeyEnter}, // submit menu (scope menu opens)
		tea.KeyMsg{Type: tea.KeyEnter}, // scope: delete all files (confirm opens)
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
	sm.downloads = []*downloadState{mkDL("org/repo", "Q4_K_M", dlRunning)}
	sm.downloads[0].total = 15 << 30
	widths := map[int64]int{}
	for _, done := range []int64{0, 1 << 10, 512 << 20, 999 << 20, 1 << 30, 15 << 30} {
		sm.downloads[0].done = done
		widths[done] = len(stripANSI(sm.renderDownloads()[0]))
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

	sm.downloads = []*downloadState{mkDL("org/finished", "Q8_0", dlDone)}
	sm.downloads[0].total = 1
	sm.downloads[0].doneAt = time.Now()
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
	sm.downloads = []*downloadState{mkDL("org/finished", "Q8_0", dlDone)}
	sm.downloads[0].doneAt = time.Now()
	sm.flash = "downloaded org/finished:Q8_0"
	sm.flashAt = time.Now()
	sm.rebuild()
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyDown})
	if len(sm.downloads) != 0 {
		t.Errorf("done download must be dismissed on navigation, got %+v", sm.downloads)
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
	d := mkDL("org/r", "Q4_K_M", dlRunning)
	d.prog = &progressSlot{}
	sm.downloads = []*downloadState{d}
	d.speedWinAt = time.Now().Add(-2 * time.Second)
	d.speedWinDone = 0
	d.prog.set(2<<20, 1<<30) // 2 MiB fetched over the last 2s

	sm.Update(dlTickMsg{})
	if want := int64(1 << 20); d.speed < want-1024 || d.speed > want+1024 {
		t.Errorf("speed = %d, want ~%d (2 MiB / 2s)", d.speed, want)
	}

	// no new progress: the window does not advance, speed stays put
	sm.Update(dlTickMsg{})
	if want := int64(1 << 20); d.speed < want-1024 || d.speed > want+1024 {
		t.Errorf("speed changed without new data: %d, want ~%d", d.speed, want)
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

// TestStorageSpinnerWhileDownloading: a dot spinner animates left of the
// download line while the download runs.
func TestStorageSpinnerWhileDownloading(t *testing.T) {
	_, sm := openStorageRoot(t, &stubEngine{})
	d := mkDL("org/r", "Q4_K_M", dlRunning)
	d.total = 1 << 30
	d.prog = &progressSlot{}
	sm.downloads = []*downloadState{d}
	d.prog.set(1<<29, 1<<30)
	// two ticks advance the frame
	sm.Update(dlTickMsg{})
	sm.Update(dlTickMsg{})
	line := stripANSI(sm.renderDownloads()[0])
	if !strings.ContainsAny(line, "⣾⣽⣻⢿⡿⣟⣯⣷") {
		t.Errorf("dot spinner missing from download line: %q", line)
	}
}

// TestStorageEscKeepsDownload: leaving the manager with Esc keeps the
// download running, Main shows its status, and re-entering shows it.
func TestStorageEscKeepsDownload(t *testing.T) {
	eng := &stubEngine{blockCh: make(chan struct{})}
	r, sm := openStorageRoot(t, eng)
	// start a blocking download directly
	cmd := sm.startDownload("org/big", "Q4_K_M")
	_ = cmd
	if d := firstDL(sm); d == nil || d.status != dlRunning {
		t.Fatalf("download not running: %+v", sm.downloads)
	}

	driveRoot(t, r, keyMsg("esc"))
	if r.view != ViewMain {
		t.Fatalf("view = %d, want ViewMain", r.view)
	}
	if r.storage == nil {
		t.Fatal("storage manager must survive esc (download keeps running)")
	}
	if d := firstDL(r.storage); d == nil || d.status != dlRunning {
		t.Fatal("download must keep running after esc")
	}
	out := stripANSI(r.mainMode.View())
	if !strings.Contains(out, "downloading org/big") {
		t.Errorf("Main must surface the running download:\n%s", out)
	}
	if !strings.ContainsAny(out, "⣾⣽⣻⢿⡿⣟⣯⣷") {
		t.Errorf("Main status line must show the spinner, not a static arrow:\n%s", out)
	}

	// re-enter: same manager, same download
	driveRoot(t, r, keyMsg("s"))
	if r.view != ViewStorage || r.storage != sm {
		t.Fatalf("re-entry must reuse the live manager")
	}
	if d := firstDL(r.storage); d == nil || d.status != dlRunning {
		t.Fatal("download must still be visible after re-entry")
	}
}

// TestStorageReentryResumesDownload: re-entering the manager mid-
// download resumes progress (the tick is re-armed), lands the cursor
// on the download row, and the action menu offers cancel.
func TestStorageReentryResumesDownload(t *testing.T) {
	eng := &stubEngine{blockCh: make(chan struct{})}
	r, sm := openStorageRoot(t, eng)
	_ = sm.startDownload("org/big", "Q4_K_M")
	d := firstDL(sm)
	d.prog.set(5<<20, 1<<30)
	sm.Update(dlTickMsg{}) // drain the slot

	driveRoot(t, r, keyMsg("esc")) // leave mid-download
	driveRoot(t, r, keyMsg("s"))   // re-enter

	// progress must resume: a fresh tick drains the live slot
	d.prog.set(7<<20, 1<<30)
	sm.Update(dlTickMsg{})
	if d.done != 7<<20 {
		t.Errorf("done = %d, want 7 MiB (tick resumed)", d.done)
	}

	// cursor must be on the download row; Enter offers cancel
	if sm.cursor < 0 || sm.cursor >= len(sm.entries) || sm.entries[sm.cursor].kind != entryDownload {
		t.Fatalf("cursor must land on the download row, at [%d]: %+v", sm.cursor, sm.entries)
	}
	cmd := sm.openMenu()
	if cmd == nil {
		t.Fatal("download row must open an action menu")
	}
	out := stripANSI(sm.View())
	if !strings.Contains(out, "cancel") {
		t.Errorf("download menu must offer cancel:\n%s", out)
	}
}

// twoQuantRepo builds a hub repo with two quants and returns the repo
// dir and both snapshot paths.
func twoQuantRepo(t *testing.T, root, repoID string) (repoDir, snapA, snapB string) {
	t.Helper()
	repoDir = filepath.Join(root, storage.RepoFolderNames(repoID)[0])
	blobsDir := filepath.Join(repoDir, "blobs")
	for _, f := range []struct{ name, oid, content string }{
		{"model-Q4_K_M.gguf", "1111111111111111111111111111111111111111111111111111111111111111", "aaaa"},
		{"model-Q8_0.gguf", "2222222222222222222222222222222222222222222222222222222222222222", "bbbb"},
	} {
		if err := os.MkdirAll(blobsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(blobsDir, f.oid), []byte(f.content), 0o644); err != nil {
			t.Fatal(err)
		}
		snap := filepath.Join(repoDir, "snapshots", "aa", f.name)
		if err := os.MkdirAll(filepath.Dir(snap), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "blobs", f.oid), snap); err != nil {
			t.Fatal(err)
		}
	}
	return repoDir, filepath.Join(repoDir, "snapshots", "aa", "model-Q4_K_M.gguf"),
		filepath.Join(repoDir, "snapshots", "aa", "model-Q8_0.gguf")
}

// TestStorageDeleteSingleQuant: deleting one quant removes only its
// files and its orphaned blob; the other quant stays.
func TestStorageDeleteSingleQuant(t *testing.T) {
	eng := &stubEngine{}
	cfg := sampleSnapshotConfig()
	root := t.TempDir()
	repoDir, snapA, snapB := twoQuantRepo(t, root, "org/two")
	cfg.Preferences = &config.Preferences{ModelsDir: root}
	r := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	r.SetDownloadEngine(eng)
	driveRoot(t, r, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("s"))
	sm := r.storage

	// select the org/two row
	for i, e := range sm.entries {
		if e.title == "org/two" {
			sm.cursor = i
		}
	}
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // action menu
		tea.KeyMsg{Type: tea.KeyDown},  // delete
		tea.KeyMsg{Type: tea.KeyEnter}, // submit → scope menu
		tea.KeyMsg{Type: tea.KeyDown},  // scope: Q4_K_M
		tea.KeyMsg{Type: tea.KeyDown},  // scope: Q8_0 quant
		tea.KeyMsg{Type: tea.KeyEnter}, // choose Q8_0 (confirm opens)
		tea.KeyMsg{Type: tea.KeyRight}, // confirm: Yes
		tea.KeyMsg{Type: tea.KeyEnter}, // choose Yes
		tea.KeyMsg{Type: tea.KeyEnter}, // submit
	)
	if _, err := os.Stat(snapB); !os.IsNotExist(err) {
		t.Errorf("Q8_0 snapshot must be removed, stat err = %v", err)
	}
	if _, err := os.Stat(snapA); err != nil {
		t.Errorf("Q4_K_M snapshot must stay, stat err = %v", err)
	}
	blobB := filepath.Join(repoDir, "blobs", "2222222222222222222222222222222222222222222222222222222222222222")
	if _, err := os.Stat(blobB); !os.IsNotExist(err) {
		t.Errorf("orphaned Q8_0 blob must be removed, stat err = %v", err)
	}
}

// TestStorageDeleteQuantMultiselect: "select quants…" lets the user
// pick several quants, confirmed once.
func TestStorageDeleteQuantMultiselect(t *testing.T) {
	eng := &stubEngine{}
	cfg := sampleSnapshotConfig()
	root := t.TempDir()
	repoDir, snapA, snapB := twoQuantRepo(t, root, "org/two")
	cfg.Preferences = &config.Preferences{ModelsDir: root}
	r := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	r.SetDownloadEngine(eng)
	driveRoot(t, r, tea.WindowSizeMsg{Width: 120, Height: 40}, keyMsg("s"))
	sm := r.storage
	for i, e := range sm.entries {
		if e.title == "org/two" {
			sm.cursor = i
		}
	}
	// scope menu has [all, Q4_K_M, Q8_0, select quants…] — pick "select"
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyEnter}, // action menu
		tea.KeyMsg{Type: tea.KeyDown},  // delete
		tea.KeyMsg{Type: tea.KeyEnter}, // submit → scope menu
		tea.KeyMsg{Type: tea.KeyDown},  // Q4_K_M
		tea.KeyMsg{Type: tea.KeyDown},  // Q8_0
		tea.KeyMsg{Type: tea.KeyDown},  // select quants…
		tea.KeyMsg{Type: tea.KeyEnter}, // choose "select quants…"
	)
	if sm.quantDel == nil {
		t.Fatalf("multi-select did not open:\n%s", stripANSI(sm.View()))
	}
	// multiselect: space on first two options, then submit
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeySpace}, // select Q4_K_M
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeySpace}, // select Q8_0
		tea.KeyMsg{Type: tea.KeyEnter}, // submit (confirm opens)
		tea.KeyMsg{Type: tea.KeyRight}, // confirm: Yes
		tea.KeyMsg{Type: tea.KeyEnter}, // choose Yes
		tea.KeyMsg{Type: tea.KeyEnter}, // submit
	)
	if _, err := os.Stat(snapA); !os.IsNotExist(err) {
		t.Errorf("Q4_K_M must be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(snapB); !os.IsNotExist(err) {
		t.Errorf("Q8_0 must be deleted, stat err = %v", err)
	}
	if _, err := os.Stat(repoDir); !os.IsNotExist(err) {
		t.Errorf("empty repo dir must be removed, stat err = %v", err)
	}
}

// TestStorageConcurrentDownloads: two downloads run at once — both
// rows render with their own progress, both are selectable, and Main
// aggregates them.
func TestStorageConcurrentDownloads(t *testing.T) {
	eng := &stubEngine{blockCh: make(chan struct{})}
	r, sm := openStorageRoot(t, eng)
	_ = sm.startDownload("org/one", "Q4_K_M")
	_ = sm.startDownload("org/two", "Q8_0")
	sm.downloads[0].prog.set(1<<20, 1<<30)
	sm.downloads[1].prog.set(2<<20, 1<<30)
	sm.Update(dlTickMsg{})

	out := stripANSI(sm.View())
	if !strings.Contains(out, "org/one:Q4_K_M") || !strings.Contains(out, "org/two:Q8_0") {
		t.Errorf("both downloads must render:\n%s", out)
	}
	if sm.downloads[0].done != 1<<20 || sm.downloads[1].done != 2<<20 {
		t.Errorf("each download keeps its own progress: %d, %d", sm.downloads[0].done, sm.downloads[1].done)
	}
	rows := 0
	for _, e := range sm.entries {
		if e.kind == entryDownload {
			rows++
		}
	}
	if rows != 2 {
		t.Errorf("download rows = %d, want 2", rows)
	}
	// each row is individually selectable → its own menu
	sm.cursor = len(sm.entries) - 1
	if cmd := sm.openMenu(); cmd == nil {
		t.Fatal("a download row must open its action menu")
	}
	driveRoot(t, r, keyMsg("esc")) // close the menu

	// Main aggregates both
	driveRoot(t, r, keyMsg("esc")) // leave the manager
	if !strings.Contains(r.mainMode.statusLine, "2 downloads") {
		t.Fatalf("Main status line must aggregate both downloads, got %q", r.mainMode.statusLine)
	}
	out = stripANSI(r.mainMode.View())
	if !strings.Contains(out, "org/one") || !strings.Contains(out, "org/two") {
		t.Errorf("Main must list both downloads:\n%s", out)
	}
}

// TestStorageCancelKeepsSiblingPartial: cancelling one download must
// not delete another download's partial for the same repo (owner
// report: the survivor's sha256 verify failed with ENOENT).
func TestStorageCancelKeepsSiblingPartial(t *testing.T) {
	eng := &stubEngine{blockCh: make(chan struct{})}
	_, sm := openStorageRoot(t, eng)
	_ = sm.startDownload("org/shared", "Q4_K_M")
	_ = sm.startDownload("org/shared", "Q8_0")
	a, b := sm.downloads[0], sm.downloads[1]

	// a partial for B's quant exists on disk
	repoDir := filepath.Join(sm.root, storage.RepoFolderNames("org/shared")[0])
	partialB := filepath.Join(repoDir, "blobs", "b.incomplete")
	if err := os.MkdirAll(filepath.Dir(partialB), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(partialB, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	// cancel A (discard): B is still active for the repo → its partial
	// must survive
	sm.cancelDownload(a)
	sm.handleDlFinished(a, context.Canceled)
	if _, err := os.Stat(partialB); err != nil {
		t.Fatalf("sibling partial must survive a cancel, stat err = %v", err)
	}

	// now cancel B (last one for the repo) → its own partial goes
	sm.cancelDownload(b)
	sm.handleDlFinished(b, context.Canceled)
	if _, err := os.Stat(partialB); !os.IsNotExist(err) {
		t.Errorf("last download's partial should be removed, stat err = %v", err)
	}
}

// TestStorageDeleteBlockedWhileDownloading: deleting a repo with an
// active download refuses (its partials must not be removed mid-flight).
func TestStorageDeleteBlockedWhileDownloading(t *testing.T) {
	eng := &stubEngine{blockCh: make(chan struct{})}
	_, sm := openStorageRoot(t, eng)
	_ = sm.startDownload("org/shared", "Q4_K_M")
	if err := sm.deleteCacheEntry("org/shared"); err == nil {
		t.Fatal("delete must refuse while a download for the repo is active")
	}
	if err := sm.deleteCacheQuants("org/shared", []string{"Q4_K_M"}); err == nil {
		t.Fatal("quant delete must refuse while a download for the repo is active")
	}
}
