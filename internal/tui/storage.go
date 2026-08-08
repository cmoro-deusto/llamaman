// StorageMode is the Storage & Downloads manager (DESIGN §16.4) — the
// single place downloads are managed. It lists cache repos + local
// config models + in-flight downloads with free disk, deletes cache
// entries with confirmation, and pre-fetches repos ("download now")
// with pause/resume/cancel, sha256 verification, and clear failures.
// Launch stays delegated (owner decision C); nothing here touches
// config entries (P8).
package tui

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// downloadEngine is the injectable downloader. *hf.Client satisfies it;
// tests use a stub (DESIGN §16.4 P9).
type downloadEngine interface {
	Download(ctx context.Context, root, repo, quant string, progress func(done, total int64)) error
	Choose(ctx context.Context, repo string) ([]hf.QuantOption, error)
}

type entryKind int

const (
	entryCacheRepo entryKind = iota
	entryLocalModel
	entryDownload
)

// storageEntry is one selectable row of the manager list.
type storageEntry struct {
	kind     entryKind
	title    string // repo id / alias / download row
	detail   string // quants list / local model path
	size     int64  // -1 = unknown/missing
	path     string // local model path (the reveal target)
	missing  bool   // local model file absent on disk
	modelIdx int    // index into cfg.Models for local models
}

type dlStatus int

const (
	dlRunning dlStatus = iota
	dlPaused
	dlDone
	dlFailed
)

// downloadState is the single active download (DESIGN §16.4). Pause
// cancels the context and keeps the partial blob; resume starts a new
// Download that Range-resumes it; cancel also removes the partials.
type downloadState struct {
	repo, quant string
	status      dlStatus
	done, total int64
	// speed is a bytes/s estimate over a ~2s window (instantaneous
	// per-tick deltas are too noisy to read).
	speed        int64
	speedWinAt   time.Time
	speedWinDone int64
	prog         *progressSlot
	err       error
	cancel    context.CancelFunc
	paused    bool // user pressed pause
	discard   bool // user pressed cancel (remove partials)
	doneAt    time.Time // when the download finished (auto-dismiss clock)
}

type dlTickMsg struct{}
type dlFinishedMsg struct{ err error }

// progressSlot holds the latest progress snapshot: the download
// goroutine overwrites it, the render loop reads it. No drops (a
// full channel would lose updates and make the bar/speed jump) and
// no shared-state race.
type progressSlot struct {
	mu    sync.Mutex
	done  int64
	total int64
}

func (p *progressSlot) set(done, total int64) {
	p.mu.Lock()
	p.done, p.total = done, total
	p.mu.Unlock()
}

func (p *progressSlot) get() (done, total int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.done, p.total
}

// stgFlashExpiredMsg fires from the flash timer (stgFlashTTL) — flashes
// expire even when no download is running.
type stgFlashExpiredMsg struct{}

// stgFlashTTL is how long a status flash stays on screen.
const stgFlashTTL = 4 * time.Second

// returnFromStorageMsg pops back to Main.
type returnFromStorageMsg struct{}

// setFlash shows a transient status line and arms its expiry timer.
func (s *StorageMode) setFlash(msg string) tea.Cmd {
	s.flash = msg
	s.flashAt = time.Now()
	return tea.Tick(stgFlashTTL, func(t time.Time) tea.Msg { return stgFlashExpiredMsg{} })
}

// StorageMode implements the manager view.
type StorageMode struct {
	cfgPath string
	cfg     *config.Config
	theme   Theme
	engine  downloadEngine
	root    string

	entries []storageEntry
	cursor  int
	spinner spinner.Model

	form      *huh.Form // download-now repo input
	repoVal   string
	quantForm *huh.Form // quant picker
	quantVal  string
	pendingRepo string
	menu      *huh.Form // per-row action menu
	menuAction string
	menuStage  int // 0 = row action menu, 1 = delete-scope menu
	quantDel  *huh.Form // per-quant delete multi-select
	quantDelVal []string
	confirm      *huh.Form // delete confirmation
	confirmYes   bool
	confirmEntry int
	confirmAction string // "deletecache" | "deleteconfig" | "deletequants"
	confirmQuants []string

	dl     *downloadState
	dlDone chan error

	flash   string
	flashAt time.Time // flash auto-expiry clock
	width, height int
}

// NewStorageMode builds the manager over the live config. engine may be
// nil — the caller replaces it via SetEngine before use (tests inject a
// stub; the root wires a real *hf.Client).
func NewStorageMode(cfg *config.Config, theme Theme, root string) *StorageMode {
	return &StorageMode{
		cfg:     cfg,
		theme:   theme,
		root:    root,
		spinner: spinner.New(spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(lipgloss.NewStyle().Foreground(theme.Accent))),
	}
}

// SetEngine attaches the download engine.
func (s *StorageMode) SetEngine(e downloadEngine) { s.engine = e }

// SetSize tracks terminal dimensions.
func (s *StorageMode) SetSize(w, h int) { s.width, s.height = w, h }

// formWidth sizes overlay forms to the window so long repo ids stay
// visible while typing (owner report: ~90-char ids did not fit).
func (s *StorageMode) formWidth() int {
	return max(60, min(s.width-12, 160))
}

// Init starts nothing — the list renders immediately.
func (s *StorageMode) Init() tea.Cmd { return nil }

// Engine exposes the active engine (tests inspect it).
func (s *StorageMode) Engine() downloadEngine { return s.engine }

// rebuild re-scans the cache and re-stats local models. Deterministic:
// entries sorted by kind then title.
func (s *StorageMode) rebuild() {
	s.entries = s.entries[:0]
	var warns []string
	cacheFiles, _ := storage.Scan(s.root, func(name string) {
		warns = append(warns, fmt.Sprintf("unrecognized cache entry: %s", name))
	})
	// group cache files by repo
	byRepo := map[string][]storage.CachedFile{}
	var repos []string
	for _, f := range cacheFiles {
		if _, ok := byRepo[f.RepoID]; !ok {
			repos = append(repos, f.RepoID)
		}
		byRepo[f.RepoID] = append(byRepo[f.RepoID], f)
	}
	sort.Strings(repos)
	for _, repo := range repos {
		fs := byRepo[repo]
		var total int64
		var quants []string
		for _, q := range hf.Quants(repoFiles(fs)) {
			total += q.Size
			quants = append(quants, q.Tag)
		}
		entry := storageEntry{kind: entryCacheRepo, title: repo, size: total}
		if len(quants) > 0 {
			entry.detail = strings.Join(quants, ", ")
		} else {
			entry.detail = "empty cache repo"
			entry.size = -1
		}
		s.entries = append(s.entries, entry)
	}
	for i, m := range s.cfg.Models {
		if m.Location == "" {
			continue
		}
		entry := storageEntry{kind: entryLocalModel, title: m.Alias, detail: m.Location, path: m.Location, modelIdx: i}
		if info, err := os.Stat(m.Location); err == nil {
			entry.size = info.Size()
		} else {
			entry.size = -1
			entry.missing = true
		}
		s.entries = append(s.entries, entry)
	}
	if s.dl != nil && s.dl.status != dlDone {
		// a finished download leaves no row: the repo now appears as a
		// normal cache entry (with quants, size, and actions).
		status := "downloading"
		if s.dl.status == dlPaused {
			status = "paused"
		} else if s.dl.status == dlFailed {
			status = "failed"
		}
		s.entries = append(s.entries, storageEntry{
			kind: entryDownload, title: s.dl.repo + ":" + s.dl.quant, detail: status, size: s.dl.total,
		})
	}
	sort.SliceStable(s.entries, func(i, j int) bool {
		if s.entries[i].kind != s.entries[j].kind {
			return s.entries[i].kind < s.entries[j].kind
		}
		return s.entries[i].title < s.entries[j].title
	})
	if s.cursor >= len(s.entries) {
		s.cursor = max(0, len(s.entries)-1)
	}
	// Note: rebuild deliberately does NOT clear s.flash — the flash has
	// its own auto-expiry (flashAt) and is dismissed on navigation, so
	// a rebuild (e.g. right after a download finishes) must not wipe a
	// fresh announcement. Unrecognized-cache warnings, when any, are
	// surfaced through the flash.
	if len(warns) > 0 {
		s.flash = strings.Join(warns, "; ")
	}
}

// repoFiles converts storage cache files to hf.RepoFile (OID unknown
// from the reader — sizes/paths suffice for the quant grouping).
func repoFiles(fs []storage.CachedFile) []hf.RepoFile {
	out := make([]hf.RepoFile, 0, len(fs))
	for _, f := range fs {
		out = append(out, hf.RepoFile{Path: f.Path, Size: f.Size})
	}
	return out
}

// freeDisk returns the free bytes on root's filesystem, or -1.
func freeDisk(root string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(root, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

// Update routes keys and overlay messages. Non-key messages (huh
// focus/blur, suggestions, …) are forwarded to the active overlay —
// dropping them would leave a form unfocusable.
func (s *StorageMode) Update(msg tea.Msg) (*StorageMode, tea.Cmd) {
	switch m := msg.(type) {
	case dlTickMsg:
		return s.handleDlTick()
	case dlFinishedMsg:
		s.handleDlFinished(m.err)
		return s, nil
	case returnFromStorageMsg:
		// handled by Root; here just a safety no-op
		return s, nil
	case stgFlashExpiredMsg:
		s.flash = ""
		return s, nil
	case tea.KeyMsg:
		return s.handleKey(m)
	}
	if s.confirm != nil {
		return s.updateConfirm(msg)
	}
	if s.menu != nil {
		return s.updateMenu(msg)
	}
	if s.quantForm != nil {
		return s.updateQuantForm(msg)
	}
	if s.quantDel != nil {
		return s.updateQuantDel(msg)
	}
	if s.form != nil {
		return s.updateRepoForm(msg)
	}
	return s, nil
}

func (s *StorageMode) handleDlTick() (*StorageMode, tea.Cmd) {
	if s.dl == nil {
		return s, nil
	}
	if p := s.dl.prog; p != nil {
		s.dl.done, s.dl.total = p.get()
	}
	select {
	case err := <-s.dlDone:
		return s, s.handleDlFinished(err)
	default:
	}
	// advance the spinner frame while downloading (dot style)
	if s.dl.status == dlRunning {
		s.spinner, _ = s.spinner.Update(s.spinner.Tick())
	}
	// speed over a ~2s window: instant per-tick deltas are too noisy
	// to read (owner report).
	if s.dl.status == dlRunning {
		now := time.Now()
		if s.dl.speedWinAt.IsZero() {
			s.dl.speedWinAt, s.dl.speedWinDone = now, s.dl.done
		} else if now.Sub(s.dl.speedWinAt) >= 2*time.Second {
			if dt := now.Sub(s.dl.speedWinAt).Seconds(); dt > 0 {
				s.dl.speed = int64(float64(s.dl.done-s.dl.speedWinDone) / dt)
			}
			s.dl.speedWinAt, s.dl.speedWinDone = now, s.dl.done
		}
	}
	// auto-dismiss: finished downloads leave the row after 3s; flashes
	// expire after 4s — the list (with the repo now cached) is the
	// obvious next step.
	now := time.Now()
	if s.dl.status == dlDone && !s.dl.doneAt.IsZero() && now.Sub(s.dl.doneAt) > 3*time.Second {
		s.dl = nil
		s.rebuild()
	}
	if s.flash != "" && !s.flashAt.IsZero() && now.Sub(s.flashAt) > 4*time.Second {
		s.flash = ""
	}
	if s.dl == nil {
		return s, nil
	}
	interval := s.spinner.Spinner.FPS
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	return s, tea.Tick(interval, func(time.Time) tea.Msg { return dlTickMsg{} })
}

func (s *StorageMode) handleDlFinished(err error) tea.Cmd {
	if s.dl == nil {
		return nil
	}
	switch {
	case err == nil:
		s.dl.status = dlDone
		s.dl.doneAt = time.Now()
		msg := "downloaded " + s.dl.repo + ":" + s.dl.quant
		if s.dl.total == 0 {
			msg = "already cached: " + s.dl.repo + ":" + s.dl.quant
		}
		s.rebuild() // the repo row (quants, size, actions) appears now
		return s.setFlash(msg)
	case s.dl.discard:
		s.removePartials()
		s.dl = nil
		s.rebuild()
		return s.setFlash("download cancelled")
	case s.dl.paused:
		s.dl.status = dlPaused
	default:
		s.dl.status = dlFailed
		s.dl.err = err
	}
	return nil
}

// startDownload launches the engine download in a goroutine; progress
// and completion arrive as messages drained by a tick (no shared state
// between the goroutine and the render loop — P9-safe).
func (s *StorageMode) startDownload(repo, quant string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	s.dl = &downloadState{repo: repo, quant: quant, status: dlRunning, cancel: cancel}
	s.dl.prog = &progressSlot{}
	s.dlDone = make(chan error, 1)
	prog := s.dl.prog // captured: a resumed download writes its own slot
	go func() {
		err := s.engine.Download(ctx, s.root, repo, quant, func(done, total int64) {
			prog.set(done, total)
		})
		s.dlDone <- err
	}()
	s.rebuild()
	return func() tea.Msg { return dlTickMsg{} }
}

// tickCmd returns a cmd that kicks the manager tick — used on re-entry
// after Esc, when the tick chain stopped but a download is still
// running (its progress and spinner would otherwise stay frozen).
func (s *StorageMode) tickCmd() tea.Cmd {
	if s.dl != nil || s.flash != "" {
		return func() tea.Msg { return dlTickMsg{} }
	}
	return nil
}

// focusDownloadRow moves the cursor to the active download row so the
// action menu (pause/cancel) is one Enter away.
func (s *StorageMode) focusDownloadRow() {
	if s.dl == nil {
		return
	}
	for i, e := range s.entries {
		if e.kind == entryDownload {
			s.cursor = i
			return
		}
	}
}

// pauseDownload keeps the partial and stops the fetch (resume reuses
// Range).
func (s *StorageMode) pauseDownload() {
	if s.dl == nil || s.dl.status != dlRunning {
		return
	}
	s.dl.paused = true
	s.dl.cancel()
}

// cancelDownload stops and removes the partials.
func (s *StorageMode) cancelDownload() {
	if s.dl == nil || s.dl.status != dlRunning {
		return
	}
	s.dl.discard = true
	s.dl.cancel()
}

func (s *StorageMode) removePartials() {
	if s.dl == nil {
		return
	}
	repoDir := filepath.Join(s.root, storage.RepoFolderNames(s.dl.repo)[0])
	partials, _ := filepath.Glob(filepath.Join(repoDir, "blobs", "*.incomplete"))
	for _, p := range partials {
		_ = os.Remove(p)
	}
}

// deleteCacheEntry removes a cache repo (hub dir, or legacy flat files
// + metadata) after confirmation. Config entries are never touched (P8).
func (s *StorageMode) deleteCacheEntry(repo string) error {
	hubDir := filepath.Join(s.root, storage.RepoFolderNames(repo)[0])
	if _, err := os.Stat(hubDir); err == nil {
		return os.RemoveAll(hubDir)
	}
	// legacy forms
	prefix := strings.ReplaceAll(repo, "/", "__")
	for _, pattern := range []string{
		prefix + "__*.gguf", prefix + "__*.mmproj", prefix + "__*.gguf.etag",
		"manifest=*",
	} {
		matches, _ := filepath.Glob(filepath.Join(s.root, pattern))
		for _, m := range matches {
			if err := os.Remove(m); err != nil {
				return err
			}
		}
	}
	_ = os.RemoveAll(filepath.Join(s.root, prefix))
	return nil
}

// deleteCacheQuants removes the given quant files of a cache repo.
// Blobs no longer referenced by any remaining snapshot entry are
// removed too (refcounted, like llama.cpp); an empty repo folder is
// cleaned up entirely. Config entries are never touched (P8).
func (s *StorageMode) deleteCacheQuants(repo string, quants []string) error {
	files, _ := storage.Lookup(s.root, repo)
	want := map[string]bool{}
	for _, q := range quants {
		want[q] = true
	}
	removed := 0
	for _, f := range files {
		for _, q := range hf.Quants([]hf.RepoFile{{Path: f.Path, Size: f.Size}}) {
			if want[q.Tag] {
				if err := os.Remove(f.Path); err == nil {
					removed++
				}
			}
		}
	}
	if removed == 0 {
		return fmt.Errorf("no files matched the selected quants")
	}
	// refcount blobs: drop blobs no snapshot entry references anymore
	repoDir := filepath.Join(s.root, storage.RepoFolderNames(repo)[0])
	snapDir := filepath.Join(repoDir, "snapshots")
	if info, err := os.Stat(snapDir); err == nil && info.IsDir() {
		referenced := map[string]bool{}
		_ = filepath.WalkDir(snapDir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if target, rerr := os.Readlink(p); rerr == nil {
				referenced[filepath.Base(target)] = true
			} else {
				referenced[filepath.Base(p)] = true
			}
			return nil
		})
		blobsDir := filepath.Join(repoDir, "blobs")
		if entries, err := os.ReadDir(blobsDir); err == nil {
			for _, b := range entries {
				if !referenced[b.Name()] {
					_ = os.Remove(filepath.Join(blobsDir, b.Name()))
				}
			}
		}
	}
	// nothing left → drop the whole repo dir
	if remaining, err := storage.Lookup(s.root, repo); err == nil && len(remaining) == 0 {
		_ = os.RemoveAll(repoDir)
	}
	return nil
}

// reveal opens the parent folder of a local model (best effort — a
// missing xdg-open is a warning line, never a crash).
func (s *StorageMode) reveal(path string) tea.Cmd {
	dir := filepath.Dir(path)
	if _, err := exec.LookPath("xdg-open"); err != nil {
		return s.setFlash("xdg-open not found")
	}
	cmd := exec.Command("xdg-open", dir)
	if err := cmd.Start(); err != nil {
		return s.setFlash("could not open folder: " + err.Error())
	}
	return nil
}

// handleKey implements the manager's keys: list navigation, action
// menu, download-now, overlays.
func (s *StorageMode) handleKey(k tea.KeyMsg) (*StorageMode, tea.Cmd) {
	// overlays take priority (they also process the completing message,
	// which is often a follow-up, not the key itself)
	if s.confirm != nil || s.menu != nil || s.quantForm != nil || s.quantDel != nil || s.form != nil {
		if s.confirm != nil {
			return s.updateConfirm(k)
		}
		if s.menu != nil {
			return s.updateMenu(k)
		}
		if s.quantForm != nil {
			return s.updateQuantForm(k)
		}
		if s.quantDel != nil {
			return s.updateQuantDel(k)
		}
		return s.updateRepoForm(k)
	}
	if k.String() == "esc" {
		return s, func() tea.Msg { return returnFromStorageMsg{} }
	}
	switch k.String() {
	case "up", "k":
		// navigating dismisses a finished/failed download line and any
		// status flash — the list is the next step.
		s.dismissFlash()
		if s.dl != nil && (s.dl.status == dlDone || s.dl.status == dlFailed) {
			s.dl = nil
			s.rebuild()
		}
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		s.dismissFlash()
		if s.dl != nil && (s.dl.status == dlDone || s.dl.status == dlFailed) {
			s.dl = nil
			s.rebuild()
		}
		if s.cursor < len(s.entries)-1 {
			s.cursor++
		}
	case "enter":
		return s, s.openMenu()
	case "d":
		return s, s.openRepoForm()
	case "x":
		// quick-cancel the active download (same as the menu action)
		if s.dl != nil && (s.dl.status == dlRunning || s.dl.status == dlPaused) {
			s.cancelDownload()
		}
	case "?":
		return s, s.setFlash("↑/↓ select · enter actions · d download · x cancel · esc back")
	}
	return s, nil
}

func (s *StorageMode) openRepoForm() tea.Cmd {
	s.repoVal = ""
	s.form = huh.NewForm(huh.NewGroup(
		huh.NewInput().
			Title("download model").
			Description("org/repo[:quant] — fetched into the cache; launch stays delegated (llama.cpp)").
			Placeholder("e.g. unsloth/Qwen3.6-27B-GGUF:UD-Q4_K_XL").
			CharLimit(2048).
			Value(&s.repoVal),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
	s.pendingRepo = ""
	return s.form.Init()
}

func (s *StorageMode) updateRepoForm(msg tea.Msg) (*StorageMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.form = nil
		return s, nil
	}
	next, cmd := s.form.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.form = f
	}
	if s.form != nil && s.form.State == huh.StateCompleted {
		repo := s.repoVal
		s.form = nil
		repo, quant := splitRepoQuant(repo)
		if quant != "" {
			return s, s.startDownload(repo, quant)
		}
		s.pendingRepo = repo
		return s, s.openQuantPicker(repo)
	}
	return s, cmd
}

// splitRepoQuant splits org/repo[:quant].
func splitRepoQuant(s string) (repo, quant string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i > 0 && !strings.Contains(s[i+1:], "/") {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func (s *StorageMode) openQuantPicker(repo string) tea.Cmd {
	opts, err := s.engine.Choose(context.Background(), repo)
	if err != nil {
		return s.setFlash("could not list " + repo + ": " + err.Error())
	}
	if len(opts) == 0 {
		return s.setFlash("no GGUF files in " + repo)
	}
	// mark quants already on disk so the picker shows what a download
	// would actually fetch (cache-first: cached quants are a no-op).
	cached := map[string]bool{}
	if files, err := storage.Lookup(s.root, repo); err == nil {
		for _, q := range hf.Quants(repoFiles(files)) {
			cached[q.Tag] = true
		}
	}
	s.quantVal = ""
	choices := make([]huh.Option[string], 0, len(opts))
	for _, q := range opts {
		label := fmt.Sprintf("%s — %s", q.Tag, hf.HumanSize(q.Size))
		if cached[q.Tag] {
			label += " (cached)"
		}
		choices = append(choices, huh.NewOption(label, q.Tag))
	}
	s.quantForm = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("quantization").
			Description(repo).
			Options(choices...).
			Value(&s.quantVal),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
	s.pendingRepo = repo
	return s.quantForm.Init()
}

func (s *StorageMode) updateQuantForm(msg tea.Msg) (*StorageMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.quantForm = nil
		s.pendingRepo = ""
		return s, nil
	}
	next, cmd := s.quantForm.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.quantForm = f
	}
	if s.quantForm != nil && s.quantForm.State == huh.StateCompleted {
		quant := s.quantVal
		s.quantForm = nil
		repo := s.pendingRepo
		s.pendingRepo = ""
		return s, s.startDownload(repo, quant)
	}
	return s, cmd
}

func (s *StorageMode) openMenu() tea.Cmd {
	if s.cursor < 0 || s.cursor >= len(s.entries) {
		return nil
	}
	e := s.entries[s.cursor]
	var opts []huh.Option[string]
	switch e.kind {
	case entryCacheRepo:
		opts = []huh.Option[string]{
			huh.NewOption("download now", "download"),
			huh.NewOption("delete", "delete"),
		}
	case entryDownload:
		if s.dl != nil && s.dl.status == dlRunning {
			opts = []huh.Option[string]{
				huh.NewOption("pause", "pause"),
				huh.NewOption("cancel", "cancel"),
			}
		} else if s.dl != nil && s.dl.status == dlPaused {
			opts = []huh.Option[string]{
				huh.NewOption("resume", "resume"),
				huh.NewOption("cancel", "cancel"),
			}
		}
	case entryLocalModel:
		if e.missing {
			// a missing model has nothing on disk to remove — deleting
			// the config entry (with confirmation) is the cleanup.
			opts = append(opts, huh.NewOption("delete from config", "deleteconfig"))
		}
		opts = append(opts, huh.NewOption("open folder", "reveal"))
	}
	if len(opts) == 0 {
		return nil
	}
	s.menuAction = ""
	s.menuStage = 0
	s.menu = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(e.title).Options(opts...).Value(&s.menuAction),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
	return s.menu.Init()
}

// openDeleteScopeMenu offers "delete all files" (as before) or picking
// the specific quants to remove.
func (s *StorageMode) openDeleteScopeMenu(repo string) tea.Cmd {
	s.menuAction = ""
	s.menuStage = 1
	opts := []huh.Option[string]{
		huh.NewOption("delete all files", "all"),
	}
	files, _ := storage.Lookup(s.root, repo)
	quants := hf.Quants(repoFiles(files))
	for _, q := range quants {
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%s — %s", q.Tag, hf.HumanSize(q.Size)), q.Tag))
	}
	if len(quants) > 1 {
		opts = append(opts, huh.NewOption("select quants…", "select"))
	}
	s.menu = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title("delete " + repo).Options(opts...).Value(&s.menuAction),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
	return s.menu.Init()
}

// openQuantDeletePicker multi-selects the cached quants of a repo.
func (s *StorageMode) openQuantDeletePicker(repo string) tea.Cmd {
	files, _ := storage.Lookup(s.root, repo)
	quants := hf.Quants(repoFiles(files))
	s.quantDelVal = nil
	opts := make([]huh.Option[string], 0, len(quants))
	for _, q := range quants {
		opts = append(opts, huh.NewOption(
			fmt.Sprintf("%s — %s", q.Tag, hf.HumanSize(q.Size)), q.Tag))
	}
	s.quantDel = huh.NewForm(huh.NewGroup(
		huh.NewMultiSelect[string]().
			Title("quants to delete").
			Description(repo + " — space to select").
			Options(opts...).
			Value(&s.quantDelVal),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
	return s.quantDel.Init()
}

func (s *StorageMode) updateQuantDel(msg tea.Msg) (*StorageMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.quantDel = nil
		return s, nil
	}
	next, cmd := s.quantDel.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.quantDel = f
	}
	if s.quantDel != nil && s.quantDel.State == huh.StateCompleted {
		sel := s.quantDelVal
		s.quantDel = nil
		repo := s.entries[s.confirmEntry].title
		if len(sel) == 0 {
			return s, s.setFlash("no quants selected — nothing deleted")
		}
		s.confirmYes = false
		s.confirmAction = "deletequants"
		s.confirmQuants = sel
		s.confirm = huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title(fmt.Sprintf("delete %d quant(s) from %s?", len(sel), repo)).
				Description("removes those quant files; other quants stay").
				Value(&s.confirmYes),
		)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
		return s, s.confirm.Init()
	}
	return s, cmd
}

func (s *StorageMode) updateMenu(msg tea.Msg) (*StorageMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.menu = nil
		return s, nil
	}
	next, cmd := s.menu.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.menu = f
	}
	if s.menu != nil && s.menu.State == huh.StateCompleted {
		action := s.menuAction
		stage := s.menuStage
		s.menu = nil
		s.menuStage = 0
		e := s.entries[s.cursor]
		if stage == 1 {
			// delete-scope menu: all files vs specific quants
			switch action {
			case "all":
				s.confirmEntry = s.cursor
				s.confirmYes = false
				s.confirmAction = "deletecache"
				s.confirm = huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title("delete " + e.title + "?").
						Description("removes the cached files; config entries are never touched").
						Value(&s.confirmYes),
				)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
				return s, s.confirm.Init()
			case "select":
				s.confirmEntry = s.cursor
				return s, s.openQuantDeletePicker(e.title)
			default: // a single quant chosen directly
				s.confirmEntry = s.cursor
				s.confirmYes = false
				s.confirmAction = "deletequants"
				s.confirmQuants = []string{action}
				s.confirm = huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title("delete " + action + " from " + e.title + "?").
						Description("removes that quant's files; other quants stay").
						Value(&s.confirmYes),
				)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
				return s, s.confirm.Init()
			}
		}
		switch e.kind {
		case entryCacheRepo:
			switch action {
			case "download":
				repo, _ := splitRepoQuant(e.title)
				s.pendingRepo = repo
				return s, s.openQuantPicker(repo)
			case "delete":
				s.confirmEntry = s.cursor
				return s, s.openDeleteScopeMenu(e.title)
			}
		case entryDownload:
			if s.dl == nil {
				return s, nil
			}
			switch action {
			case "pause":
				s.pauseDownload()
			case "resume":
				return s, s.startDownload(s.dl.repo, s.dl.quant)
			case "cancel":
				s.cancelDownload()
			}
		case entryLocalModel:
			switch action {
			case "deleteconfig":
				s.confirmEntry = s.cursor
				s.confirmYes = false
				s.confirmAction = "deleteconfig"
				s.confirm = huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title("remove " + e.title + " from config?").
						Description("deletes the config entry; cached files are untouched").
						Value(&s.confirmYes),
				)).WithTheme(configHuhTheme(s.theme)).WithWidth(s.formWidth())
				return s, s.confirm.Init()
			case "reveal":
				return s, s.reveal(e.path)
			}
		}
		s.rebuild()
		return s, nil
	}
	return s, cmd
}

func (s *StorageMode) updateConfirm(msg tea.Msg) (*StorageMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.confirm = nil
		return s, nil
	}
	next, cmd := s.confirm.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.confirm = f
	}
	if s.confirm != nil && s.confirm.State == huh.StateCompleted {
		yes := s.confirmYes
		action := s.confirmAction
		s.confirm = nil
		if yes {
			e := s.entries[s.confirmEntry]
			var flashMsg string
			switch action {
			case "deleteconfig":
				if err := s.deleteModelFromConfig(e.modelIdx); err != nil {
					flashMsg = "could not remove from config: " + err.Error()
				} else {
					flashMsg = "removed " + e.title + " from config"
				}
			case "deletequants":
				if err := s.deleteCacheQuants(e.title, s.confirmQuants); err != nil {
					flashMsg = "delete failed: " + err.Error()
				} else {
					flashMsg = fmt.Sprintf("deleted %d quant(s) from %s", len(s.confirmQuants), e.title)
				}
			default:
				if err := s.deleteCacheEntry(e.title); err != nil {
					flashMsg = "delete failed: " + err.Error()
				} else {
					flashMsg = "deleted " + e.title
				}
			}
			s.rebuild()
			if flashMsg != "" {
				return s, s.setFlash(flashMsg)
			}
		}
	}
	return s, cmd
}

// deleteModelFromConfig removes a model entry from the live config and
// persists via the standard atomic save (P8: config is only mutated by
// explicit user actions — this one is confirmed). Cached files are
// never touched here.
func (s *StorageMode) deleteModelFromConfig(idx int) error {
	if idx < 0 || idx >= len(s.cfg.Models) {
		return nil
	}
	s.cfg.Models = append(s.cfg.Models[:idx], s.cfg.Models[idx+1:]...)
	if err := config.Save(s.cfgPath, s.cfg); err != nil {
		return err
	}
	return nil
}

// View renders the manager.
func (s StorageMode) View() string {
	if s.width == 0 {
		s.width = 80
	}
	if s.height == 0 {
		s.height = 24
	}
	body := []string{
		lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render("Storage & Downloads"),
		"",
	}
	body = append(body, s.renderList()...)
	if s.dl != nil {
		body = append(body, "", s.renderDownload())
	}
	if s.flash != "" {
		body = append(body, "", lipgloss.NewStyle().Foreground(s.theme.StatusStart).Render("⚠ "+s.flash))
	}
	body = append(body, "", s.renderFooter())
	content := strings.Join(body, "\n")
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(s.theme.Border).
		Padding(1, 2)
	bg := lipgloss.Place(max(s.width, 1), max(s.height, 1), lipgloss.Center, lipgloss.Center, box.Render(content))
	// Overlays (repo input, quant picker, action menu, confirm) float
	// as a centered popup over the fixed-size box — appending them to
	// the box made it grow and visually "move" (owner report).
	if ov := s.overlayView(); ov != "" {
		return overlayCenter(bg, ov, s.width, s.height)
	}
	return bg
}

// overlayView renders the active overlay form as a popup, or "" when
// none is open.
func (s StorageMode) overlayView() string {
	var v string
	switch {
	case s.confirm != nil:
		v = s.confirm.View()
	case s.menu != nil:
		v = s.menu.View()
	case s.quantForm != nil:
		v = s.quantForm.View()
	case s.quantDel != nil:
		v = s.quantDel.View()
	case s.form != nil:
		v = s.form.View()
	default:
		return ""
	}
	frame := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(s.theme.Border).
		Padding(1, 2)
	return frame.Render(v)
}

func (s StorageMode) renderList() []string {
	var rows []string
	if len(s.entries) == 0 {
		rows = append(rows, lipgloss.NewStyle().Foreground(s.theme.Muted).Render("nothing on disk yet — press d to download a model"))
		return rows
	}
	muted := lipgloss.NewStyle().Foreground(s.theme.Muted)
	subtle := lipgloss.NewStyle().Foreground(s.theme.Subtle)
	warn := lipgloss.NewStyle().Foreground(s.theme.StatusStart)
	for i, e := range s.entries {
		cursor := " "
		if i == s.cursor {
			cursor = ">"
		}
		label := e.title
		switch e.kind {
		case entryCacheRepo:
			label = lipgloss.NewStyle().Foreground(s.theme.Accent).Render(e.title)
		case entryDownload:
			label = warn.Render(e.title)
		}
		parts := []string{cursor, label}
		if e.detail != "" {
			parts = append(parts, muted.Render(e.detail))
		}
		// size in its own color (name / quants / size: three colors)
		if e.missing {
			parts = append(parts, warn.Render("missing"))
		} else if e.size >= 0 {
			parts = append(parts, subtle.Render(hf.HumanSize(e.size)))
		}
		rows = append(rows, strings.Join(parts, "  "))
	}
	return rows
}

func (s StorageMode) renderDownload() string {
	if s.dl == nil {
		return ""
	}
	// a finished download has nothing to render — the repo now appears
	// as a normal cache row (auto-dismissed shortly after).
	if s.dl == nil || s.dl.status == dlDone {
		return ""
	}
	bar := ""
	if s.dl.total > 0 {
		pct := int(float64(s.dl.done) / float64(s.dl.total) * 100)
		if pct > 100 {
			pct = 100
		}
		if pct < 0 {
			pct = 0
		}
		fillCells := pct * 12 / 100
		fill := strings.Repeat("▓", fillCells)
		rest := strings.Repeat("░", 12-fillCells)
		// %3d / %9s / %11s keep the line width constant across ticks so
		// the centered view does not re-flow (and flicker).
		bar = fmt.Sprintf(" %s%s %3d%%", fill, rest, pct)
	}
	status := "downloading"
	switch s.dl.status {
	case dlPaused:
		status = "paused"
	case dlFailed:
		status = "failed: " + s.dl.err.Error()
	}
	line := fmt.Sprintf("%s:%s — %s%s", s.dl.repo, s.dl.quant, status, bar)
	if s.dl.status == dlRunning {
		line = s.spinner.View() + " " + line
		speed := hf.HumanSize(s.dl.speed) + "/s"
		line += fmt.Sprintf("  (%9s / %9s, %11s)", hf.HumanSize(s.dl.done), hf.HumanSize(s.dl.total), speed)
	}
	return lipgloss.NewStyle().Foreground(s.theme.Accent).Render(line)
}

func (s StorageMode) renderFooter() string {
	free := freeDisk(s.root)
	freeText := "free disk: —"
	if free >= 0 {
		freeText = "free disk: " + hf.HumanSize(free)
	}
	keys := []string{
		shortcut("↑/↓", "select", s.theme),
		shortcut("enter", "actions", s.theme),
		shortcut("d", "download", s.theme),
	}
	if s.dl != nil && (s.dl.status == dlRunning || s.dl.status == dlPaused) {
		keys = append(keys, shortcut("x", "cancel", s.theme))
	}
	keys = append(keys, shortcut("esc", "back", s.theme))
	return strings.Join([]string{
		strings.Join(keys, "  ·  "),
		lipgloss.NewStyle().Foreground(s.theme.Muted).Render("cache root: " + s.root + "  ·  " + freeText),
	}, "\n")
}

// dismissFlash clears the status flash (navigation affordance).
func (s *StorageMode) dismissFlash() { s.flash = "" }
