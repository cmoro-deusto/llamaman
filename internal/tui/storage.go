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
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
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
	kind   entryKind
	title  string // repo id / alias / "downloading …"
	detail string
	size   int64 // -1 = unknown/missing
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
	err         error
	cancel      context.CancelFunc
	paused      bool // user pressed pause
	discard     bool // user pressed cancel (remove partials)
}

type dlTickMsg struct{}
type dlProgressMsg struct{ done, total int64 }
type dlFinishedMsg struct{ err error }

// returnFromStorageMsg pops back to Main.
type returnFromStorageMsg struct{}

// StorageMode implements the manager view.
type StorageMode struct {
	cfgPath string
	cfg     *config.Config
	theme   Theme
	engine  downloadEngine
	root    string

	entries []storageEntry
	cursor  int

	form      *huh.Form // download-now repo input
	repoVal   string
	quantForm *huh.Form // quant picker
	quantVal  string
	pendingRepo string
	menu      *huh.Form // per-row action menu
	menuAction string
	confirm   *huh.Form // delete confirmation
	confirmYes bool
	confirmEntry int

	dl      *downloadState
	dlProg  chan dlProgressMsg
	dlDone  chan error

	flash string
	width, height int
}

// NewStorageMode builds the manager over the live config. engine may be
// nil — the caller replaces it via SetEngine before use (tests inject a
// stub; the root wires a real *hf.Client).
func NewStorageMode(cfg *config.Config, theme Theme, root string) *StorageMode {
	return &StorageMode{cfg: cfg, theme: theme, root: root}
}

// SetEngine attaches the download engine.
func (s *StorageMode) SetEngine(e downloadEngine) { s.engine = e }

// SetSize tracks terminal dimensions.
func (s *StorageMode) SetSize(w, h int) { s.width, s.height = w, h }

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
			entry.detail = fmt.Sprintf("%s · %s", strings.Join(quants, ", "), hf.HumanSize(total))
		} else {
			entry.detail = "empty cache repo"
		}
		s.entries = append(s.entries, entry)
	}
	for _, m := range s.cfg.Models {
		if m.Location == "" {
			continue
		}
		entry := storageEntry{kind: entryLocalModel, title: m.Alias, detail: "local model"}
		if info, err := os.Stat(m.Location); err == nil {
			entry.size = info.Size()
			entry.detail = m.Location + " · " + hf.HumanSize(info.Size())
		} else {
			entry.size = -1
			entry.detail = m.Location + " · missing"
		}
		s.entries = append(s.entries, entry)
	}
	if s.dl != nil {
		s.entries = append(s.entries, storageEntry{kind: entryDownload, title: s.dl.repo + ":" + s.dl.quant, size: s.dl.total})
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
	s.flash = ""
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
	if s.form != nil {
		return s.updateRepoForm(msg)
	}
	return s, nil
}

func (s *StorageMode) handleDlTick() (*StorageMode, tea.Cmd) {
	if s.dl == nil {
		return s, nil
	}
	for {
		select {
		case p := <-s.dlProg:
			s.dl.done, s.dl.total = p.done, p.total
		default:
			goto drained
		}
	}
drained:
	select {
	case err := <-s.dlDone:
		s.handleDlFinished(err)
		return s, nil
	default:
	}
	return s, func() tea.Msg { return dlTickMsg{} }
}

func (s *StorageMode) handleDlFinished(err error) {
	if s.dl == nil {
		return
	}
	switch {
	case err == nil:
		s.dl.status = dlDone
		s.flash = "downloaded " + s.dl.repo + ":" + s.dl.quant
	case s.dl.discard:
		s.removePartials()
		s.flash = "download cancelled"
		s.dl = nil
		s.rebuild()
		return
	case s.dl.paused:
		s.dl.status = dlPaused
	default:
		s.dl.status = dlFailed
		s.dl.err = err
	}
}

// startDownload launches the engine download in a goroutine; progress
// and completion arrive as messages drained by a tick (no shared state
// between the goroutine and the render loop — P9-safe).
func (s *StorageMode) startDownload(repo, quant string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	s.dl = &downloadState{repo: repo, quant: quant, status: dlRunning, cancel: cancel}
	s.dlProg = make(chan dlProgressMsg, 128)
	s.dlDone = make(chan error, 1)
	go func() {
		err := s.engine.Download(ctx, s.root, repo, quant, func(done, total int64) {
			select {
			case s.dlProg <- dlProgressMsg{done: done, total: total}:
			default:
			}
		})
		s.dlDone <- err
	}()
	s.rebuild()
	return func() tea.Msg { return dlTickMsg{} }
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

// reveal opens the parent folder of a local model (best effort — a
// missing xdg-open is a warning line, never a crash).
func (s *StorageMode) reveal(path string) tea.Cmd {
	dir := filepath.Dir(path)
	if _, err := exec.LookPath("xdg-open"); err != nil {
		s.flash = "xdg-open not found"
		return nil
	}
	cmd := exec.Command("xdg-open", dir)
	if err := cmd.Start(); err != nil {
		s.flash = "could not open folder: " + err.Error()
	}
	return nil
}

// handleKey implements the manager's keys: list navigation, action
// menu, download-now, overlays.
func (s *StorageMode) handleKey(k tea.KeyMsg) (*StorageMode, tea.Cmd) {
	// overlays take priority (they also process the completing message,
	// which is often a follow-up, not the key itself)
	if s.confirm != nil || s.menu != nil || s.quantForm != nil || s.form != nil {
		if s.confirm != nil {
			return s.updateConfirm(k)
		}
		if s.menu != nil {
			return s.updateMenu(k)
		}
		if s.quantForm != nil {
			return s.updateQuantForm(k)
		}
		return s.updateRepoForm(k)
	}
	if k.String() == "esc" {
		return s, func() tea.Msg { return returnFromStorageMsg{} }
	}
	switch k.String() {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.entries)-1 {
			s.cursor++
		}
	case "enter":
		return s, s.openMenu()
	case "d":
		return s, s.openRepoForm()
	case "?":
		s.flash = "↑/↓ select · enter actions · d download · esc back"
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
			Value(&s.repoVal),
	)).WithTheme(configHuhTheme(s.theme))
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
		s.flash = "could not list " + repo + ": " + err.Error()
		return nil
	}
	if len(opts) == 0 {
		s.flash = "no GGUF files in " + repo
		return nil
	}
	s.quantVal = ""
	choices := make([]huh.Option[string], 0, len(opts))
	for _, q := range opts {
		choices = append(choices, huh.NewOption(fmt.Sprintf("%s — %s", q.Tag, hf.HumanSize(q.Size)), q.Tag))
	}
	s.quantForm = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("quantization").
			Description(repo).
			Options(choices...).
			Value(&s.quantVal),
	)).WithTheme(configHuhTheme(s.theme))
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
		opts = []huh.Option[string]{huh.NewOption("open folder", "reveal")}
	}
	if len(opts) == 0 {
		return nil
	}
	s.menuAction = ""
	s.menu = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().Title(e.title).Options(opts...).Value(&s.menuAction),
	)).WithTheme(configHuhTheme(s.theme))
	return s.menu.Init()
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
		s.menu = nil
		e := s.entries[s.cursor]
		switch e.kind {
		case entryCacheRepo:
			switch action {
			case "download":
				repo, _ := splitRepoQuant(e.title)
				s.pendingRepo = repo
				return s, s.openQuantPicker(repo)
			case "delete":
				s.confirmEntry = s.cursor
				s.confirmYes = false
				s.confirm = huh.NewForm(huh.NewGroup(
					huh.NewConfirm().Title("delete " + e.title + "?").
						Description("removes the cached files; config entries are never touched").
						Value(&s.confirmYes),
				)).WithTheme(configHuhTheme(s.theme))
				return s, s.confirm.Init()
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
			if action == "reveal" {
				return s, s.reveal(e.detail)
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
		s.confirm = nil
		if yes {
			repo := s.entries[s.confirmEntry].title
			if err := s.deleteCacheEntry(repo); err != nil {
				s.flash = "delete failed: " + err.Error()
			} else {
				s.flash = "deleted " + repo
			}
			s.rebuild()
		}
	}
	return s, cmd
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
	if s.form != nil {
		content += "\n\n" + s.form.View()
	}
	if s.quantForm != nil {
		content += "\n\n" + s.quantForm.View()
	}
	if s.menu != nil {
		content += "\n\n" + s.menu.View()
	}
	if s.confirm != nil {
		content += "\n\n" + s.confirm.View()
	}
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(s.theme.Border).
		Padding(1, 2)
	return lipgloss.Place(max(s.width, 1), max(s.height, 1), lipgloss.Center, lipgloss.Center, box.Render(content))
}

func (s StorageMode) renderList() []string {
	var rows []string
	if len(s.entries) == 0 {
		rows = append(rows, lipgloss.NewStyle().Foreground(s.theme.Muted).Render("nothing on disk yet — press d to download a model"))
		return rows
	}
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
			label = lipgloss.NewStyle().Foreground(s.theme.StatusStart).Render(e.title)
		}
		rows = append(rows, fmt.Sprintf("%s %s  %s", cursor, label, lipgloss.NewStyle().Foreground(s.theme.Muted).Render(e.detail)))
	}
	return rows
}

func (s StorageMode) renderDownload() string {
	if s.dl == nil {
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
		// %3d and %9s keep the line width constant across ticks so the
		// centered view does not re-flow (and flicker) as done grows.
		bar = fmt.Sprintf(" %s%s %3d%%", fill, rest, pct)
	}
	status := "downloading"
	switch s.dl.status {
	case dlPaused:
		status = "paused"
	case dlDone:
		status = "done"
	case dlFailed:
		status = "failed: " + s.dl.err.Error()
	}
	line := fmt.Sprintf("%s:%s — %s%s", s.dl.repo, s.dl.quant, status, bar)
	if s.dl.status == dlRunning {
		line += fmt.Sprintf("  (%9s / %9s)", hf.HumanSize(s.dl.done), hf.HumanSize(s.dl.total))
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
		"↑/↓ select",
		"enter actions",
		"d download",
		"esc back",
	}
	return strings.Join([]string{
		lipgloss.NewStyle().Foreground(s.theme.Muted).Render(strings.Join(keys, "  ·  ")),
		lipgloss.NewStyle().Foreground(s.theme.Muted).Render(freeText + " · cache root: " + s.root),
	}, "\n")
}
