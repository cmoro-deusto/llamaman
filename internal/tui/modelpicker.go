package tui

// Picker-assisted model-form inputs (DESIGN §16.5): the location and
// HF identifier fields of the model form keep working as free-type
// inputs and gain a ctrl+o hotkey that opens a picker overlay — a
// .gguf filepicker for the local branch, a cached-repo list for the
// HF branch. The overlays live outside huh (the paramPicker pattern),
// are driven by a done message, and only pre-fill the form's staging
// pointers: nothing is written, no config changes (P8).

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// openModelPickerMsg asks ConfigMode to open the picker overlay for the
// model-form input that emitted it. It originates inside the huh form
// (the pickerInput field) and is caught by ConfigMode.Update before the
// form sees it again.
type openModelPickerMsg struct {
	kind string // sourceLocal | sourceHF
}

// modelPickerDoneMsg carries the overlay result back to ConfigMode.
// cancelled leaves the field untouched; otherwise value pre-fills the
// staging pointer of the matching kind. An empty value with
// !cancelled means "no change" (the "type a new repo…" row).
type modelPickerDoneMsg struct {
	kind      string // sourceLocal | sourceHF
	value     string
	cancelled bool
}

// pickerFormRefreshMsg is a no-op message fed through the model form
// after a picker pre-fill. huh's group caches its built view in a
// viewport, so without a forced rebuild the pre-filled value would not
// render until the user's next keypress.
type pickerFormRefreshMsg struct{}

// repoTypeNew is the always-present trailing row of the HF picker.
// Selecting it closes the picker and lets the user type an id in the
// field (owner-chosen label).
const repoTypeNew = "select a repo…"

// fileBrowser is the local branch's filterable directory browser
// (DESIGN §16.5). Dirs-first listing of the current directory; .gguf
// files are selectable, everything else is shown disabled; hidden
// entries are listed by default and tab toggles them; typing filters
// live (case-insensitive substring on the name).
type fileBrowser struct {
	dir        string
	startDir   string // the opening directory — the esc-cancel boundary
	all        []browserEntry
	entries    []browserEntry // filtered view of all
	cursor     int
	filter     string
	showHidden bool
	errLine    string
	width      int
	height     int

	keys browserKeys
}

// browserKeys are the browser's keybindings (arrows-only, config-mode
// convention). "right" also opens, matching the filepicker it replaced.
type browserKeys struct {
	up, down, pageUp, pageDown, back, open, toggleHidden key.Binding
}

// browserEntry is one directory entry.
type browserEntry struct {
	name  string
	isDir bool
	size  int64
}

// SetSize stores the browser dimensions (paging + row truncation).
func (b *fileBrowser) SetSize(w, h int) {
	b.width, b.height = w, h
	if h < 4 {
		b.height = 4
	}
}

// readDir (re)reads the current directory into all (dirs first, then
// files, both alphabetical) and re-applies the filter.
func (b *fileBrowser) readDir() {
	b.all = b.all[:0]
	entries, err := os.ReadDir(b.dir) // already name-sorted
	if err != nil {
		b.errLine = "cannot read " + b.dir
		b.applyFilter()
		return
	}
	for _, e := range entries {
		if !b.showHidden && strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := os.Stat(filepath.Join(b.dir, e.Name())) // follows symlinks
		isDir := err == nil && info.IsDir()
		var size int64
		if !isDir && err == nil {
			size = info.Size()
		}
		b.all = append(b.all, browserEntry{name: e.Name(), isDir: isDir, size: size})
	}
	// dirs first, then files; stable keeps the name order within groups.
	sort.SliceStable(b.all, func(i, j int) bool {
		if b.all[i].isDir != b.all[j].isDir {
			return b.all[i].isDir
		}
		return b.all[i].name < b.all[j].name
	})
	b.applyFilter()
}

// applyFilter recomputes the visible entries from the current query.
func (b *fileBrowser) applyFilter() {
	if b.filter == "" {
		b.entries = b.all
	} else {
		q := strings.ToLower(b.filter)
		b.entries = b.entries[:0]
		for _, e := range b.all {
			if strings.Contains(strings.ToLower(e.name), q) {
				b.entries = append(b.entries, e)
			}
		}
	}
	if b.cursor >= len(b.entries) {
		b.cursor = max(0, len(b.entries)-1)
	}
	if b.cursor < 0 {
		b.cursor = 0
	}
}

// isSelectableGGUF reports whether a file entry may be picked.
func isSelectableGGUF(name string) bool {
	return strings.HasSuffix(strings.ToLower(name), ".gguf")
}

// Update routes keys for the browser. Live filtering: printable runes
// narrow the listing, enter acts on the selection, backspace erases a
// filter character (or goes up with an empty filter), esc clears the
// filter first (then goes up / cancels at the start dir), tab toggles
// hidden.
func (b *fileBrowser) Update(msg tea.Msg) (*fileBrowser, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return b, nil
	}
	b.errLine = ""
	switch {
	case key.Matches(k, b.keys.toggleHidden):
		b.showHidden = !b.showHidden
		b.readDir()
	case key.Matches(k, b.keys.up):
		if b.cursor > 0 {
			b.cursor--
		}
	case key.Matches(k, b.keys.down):
		if b.cursor < len(b.entries)-1 {
			b.cursor++
		}
	case key.Matches(k, b.keys.pageUp):
		b.cursor = max(0, b.cursor-max(1, b.height-2))
	case key.Matches(k, b.keys.pageDown):
		if n := len(b.entries); n > 0 {
			b.cursor = min(n-1, b.cursor+max(1, b.height-2))
		}
	case key.Matches(k, b.keys.open):
		e, ok := b.selected()
		if !ok {
			return b, nil
		}
		if e.isDir {
			b.dir = filepath.Join(b.dir, e.name)
			b.filter, b.cursor = "", 0
			b.readDir()
			return b, nil
		}
		if !isSelectableGGUF(e.name) {
			b.errLine = ".gguf files only"
			return b, nil
		}
		return b, func() tea.Msg {
			return modelPickerDoneMsg{kind: sourceLocal, value: filepath.Join(b.dir, e.name)}
		}
	case k.Type == tea.KeyBackspace:
		if b.filter != "" {
			b.filter = b.filter[:len(b.filter)-1]
			b.applyFilter()
		} else if b.dir == b.startDir {
			return b, func() tea.Msg { return modelPickerDoneMsg{kind: sourceLocal, cancelled: true} }
		} else {
			b.ascend()
		}
	case key.Matches(k, b.keys.back): // esc / left
		if b.filter != "" {
			b.filter = ""
			b.applyFilter()
			return b, nil
		}
		if b.dir == b.startDir {
			return b, func() tea.Msg { return modelPickerDoneMsg{kind: sourceLocal, cancelled: true} }
		}
		b.ascend()
	case isPrintableRune(k):
		b.filter += string(k.Runes)
		b.applyFilter()
	}
	return b, nil
}

// ascend moves one level up and re-reads.
func (b *fileBrowser) ascend() {
	b.dir = filepath.Dir(b.dir)
	b.filter, b.cursor = "", 0
	b.readDir()
}

// selected returns the entry under the cursor.
func (b *fileBrowser) selected() (browserEntry, bool) {
	if len(b.entries) == 0 {
		return browserEntry{}, false
	}
	return b.entries[b.cursor], true
}

// View renders the filter line, the listing, and a hint line inside
// the popup box (the box itself is drawn by modelPicker.View).
func (b *fileBrowser) View(theme Theme) string {
	parts := []string{}
	if b.filter != "" {
		label := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render("filter: ")
		cursor := lipgloss.NewStyle().Foreground(theme.Accent).Render("▎")
		parts = append(parts, label+b.filter+cursor)
	}
	if len(b.entries) == 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Subtle).Render("no matching files"))
	} else {
		inner := max(20, b.width-6)
		for i, e := range b.entries {
			parts = append(parts, b.renderRow(theme, e, i == b.cursor, inner))
		}
	}
	if b.errLine != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Subtle).Render(b.errLine))
	}
	hidden := "off"
	if b.showHidden {
		hidden = "on"
	}
	parts = append(parts, lipgloss.NewStyle().Foreground(theme.Subtle).Render(
		"type to filter · ↑↓: move · enter: open/select · esc: back, cancel at start · tab: hidden "+hidden))
	return strings.Join(parts, "\n")
}

// renderRow draws one listing row: title = name (+ "/" for dirs),
// description = size or "directory". Selected rows reverse-video;
// non-.gguf files render dimmed.
func (b *fileBrowser) renderRow(theme Theme, e browserEntry, selected bool, inner int) string {
	name := truncateWidth(e.name, inner-1)
	title := name
	if e.isDir {
		title += "/"
	}
	desc := "directory"
	if !e.isDir {
		desc = hf.HumanSize(e.size)
	}
	dim := !e.isDir && !isSelectableGGUF(e.name)
	if selected {
		style := lipgloss.NewStyle().Reverse(true)
		return style.Render(truncateWidth(title, inner)) + "\n" + style.Render(truncateWidth(desc, inner))
	}
	titleStyle := lipgloss.NewStyle()
	descStyle := lipgloss.NewStyle()
	if dim {
		titleStyle = titleStyle.Foreground(theme.Subtle)
		descStyle = descStyle.Foreground(theme.Subtle)
	}
	return titleStyle.Render(truncateWidth(title, inner)) + "\n" +
		descStyle.Render(truncateWidth(desc, inner))
}

// truncateWidth shortens s to at most w cells, appending an ellipsis
// when it had to cut (rows must never wrap inside the popup).
func truncateWidth(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	var out []rune
	width := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if width+rw > w-1 {
			break
		}
		out = append(out, r)
		width += rw
	}
	return string(out) + "…"
}

// pickerInput is the model form's picker-assisted input (DESIGN §16.5).
// It embeds *huh.Input — the free-type fallback stays fully usable —
// and adds a ctrl+o hotkey. huh v1.0.0 has no custom-key escape hatch
// (an Input field consumes every key except prev/next/submit), so the
// hotkey is intercepted in this field's own Update before the embedded
// input sees it; the emitted openModelPickerMsg travels back through
// the tea loop to ConfigMode.
type pickerInput struct {
	*huh.Input
	kind    string  // sourceLocal | sourceHF
	value   *string // bound staging pointer, for RefreshValue
	openKey key.Binding
}

// wrapPickerInput wraps a fully-built huh input with the picker hotkey
// and the staging pointer. The caller chains the builder calls on the
// *huh.Input *before* wrapping (promoted builder methods return
// *huh.Input and would unwrap the field).
func wrapPickerInput(in *huh.Input, kind string, value *string) *pickerInput {
	return &pickerInput{Input: in, kind: kind, value: value, openKey: pickerOpenKey()}
}

// pickerOpenKey returns the shared ctrl+o picker hotkey binding.
func pickerOpenKey() key.Binding {
	return key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("ctrl+o", "open picker"))
}

// Update intercepts the picker hotkey before the embedded input. All
// other messages delegate unchanged; the returned model is always the
// wrapper, because the group replaces its stored field with whatever
// Update returns (losing the wrapper would also lose the hotkey).
func (p *pickerInput) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && key.Matches(k, p.openKey) {
		return p, func() tea.Msg { return openModelPickerMsg{kind: p.kind} }
	}
	m, cmd := p.Input.Update(msg)
	if in, ok := m.(*huh.Input); ok {
		p.Input = in
	}
	return p, cmd
}

// View delegates to the embedded input — the form renders byte-for-byte
// like a plain input, so existing config-mode snapshots are unaffected.
func (p *pickerInput) View() string { return p.Input.View() }

// KeyBinds reports the embedded input's binds plus the hotkey, so
// huh's help row advertises ctrl+o.
func (p *pickerInput) KeyBinds() []key.Binding {
	return append(p.Input.KeyBinds(), p.openKey)
}

// RefreshValue re-syncs the rendered input from the bound staging
// pointer after the picker pre-fills it. huh has no exported value
// setter; re-binding the accessor re-reads the pointer into the
// textinput (Accessor → SetValue). Without this the input would still
// show the old text and Blur on the next submit would overwrite the
// staged value with it.
func (p *pickerInput) RefreshValue() {
	if p.value == nil {
		return
	}
	p.Input.Accessor(huh.NewPointerAccessor(p.value))
}

// modelPicker is the picker overlay opened from the model form: a
// .gguf file browser for the local branch, a cached-repo list for the
// HF branch. It is rendered by ConfigMode over the three-pane
// background (overlayCenter) and reports back via modelPickerDoneMsg.
type modelPicker struct {
	kind    string // sourceLocal | sourceHF
	browser *fileBrowser
	repos   *repoPicker
}

// newFileBrowser builds the local branch's filterable directory
// browser starting at dir.
//
// bubbles/filepicker has no filtering hook, so the local branch is a
// thin custom browser (DESIGN §16.5 amendment) with the same behaviors
// — dirs-first listing, .gguf-only selection, hidden toggle, arrows-only
// keys — plus type-to-filter. Dirs are navigable but never selectable;
// a shown hidden .gguf is selectable like any other file.
func newFileBrowser(dir string) *fileBrowser {
	b := &fileBrowser{
		dir:        dir,
		startDir:   dir,
		showHidden: true,
	}
	b.keys.up = key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up"))
	b.keys.down = key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down"))
	b.keys.pageUp = key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up"))
	b.keys.pageDown = key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "page down"))
	b.keys.back = key.NewBinding(key.WithKeys("backspace", "esc", "left"), key.WithHelp("esc", "back"))
	b.keys.open = key.NewBinding(key.WithKeys("right", "enter"), key.WithHelp("enter", "open/select"))
	// tab is the hidden toggle: typing filters, so a printable toggle
	// key is impossible (round-1 "." moved to the filter).
	b.keys.toggleHidden = key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "toggle hidden"))
	b.readDir()
	return b
}

// newRepoPicker scans root and builds the cached-repo list overlay.
// An empty cache (or an unresolvable/scan-error root) returns nil —
// §3.8: "an empty cache skips the list" — and the field stays a plain
// free-type input (P3: never a blocking error inside a form).
func newRepoPicker(root string, warn func(string)) (*modelPicker, error) {
	files, err := storage.Scan(root, warn)
	if err != nil {
		return nil, err
	}
	// Group by repo, sorted keys — the same grouping the Storage
	// manager uses (§16.4), so both surfaces always agree.
	byRepo := map[string][]storage.CachedFile{}
	var repos []string
	for _, f := range files {
		if _, ok := byRepo[f.RepoID]; !ok {
			repos = append(repos, f.RepoID)
		}
		byRepo[f.RepoID] = append(byRepo[f.RepoID], f)
	}
	sort.Strings(repos)
	if len(repos) == 0 {
		return nil, nil
	}
	items := make([]list.Item, 0, len(repos))
	for _, repo := range repos {
		var quants []string
		var parts []string
		for _, q := range hf.Quants(repoFiles(byRepo[repo])) {
			quants = append(quants, q.Tag)
			parts = append(parts, q.Tag+" — "+hf.HumanSize(q.Size))
		}
		detail := strings.Join(parts, ", ")
		if detail == "" {
			detail = "no model quants cached"
		}
		items = append(items, repoItem{repo: repo, detail: detail, quants: quants})
	}
	return &modelPicker{kind: sourceHF, repos: &repoPicker{list: newRepoList(items)}}, nil
}

// Init starts the overlay's async work. Both branches read their
// listings synchronously at construction, so there is none.
func (p *modelPicker) Init() tea.Cmd {
	return nil
}

// SetSize propagates the overlay dimensions.
func (p *modelPicker) SetSize(w, h int) {
	if p.kind == sourceLocal {
		p.browser.SetSize(w, h-3)
		return
	}
	p.repos.list.SetSize(w, h)
}

// Update routes messages for the active overlay. The done message is
// emitted here and consumed by ConfigMode on its next turn — the form
// underneath never sees it (the "overlay handlers run on EVERY
// message" discipline from §16.4).
func (p *modelPicker) Update(msg tea.Msg) (*modelPicker, tea.Cmd) {
	if p.kind == sourceHF {
		next, cmd := p.repos.Update(msg)
		p.repos = next
		return p, cmd
	}
	next, cmd := p.browser.Update(msg)
	p.browser = next
	return p, cmd
}

// View renders the overlay as a bordered popup (paramPicker shape).
func (p *modelPicker) View(theme Theme) string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2)
	if p.kind == sourceLocal {
		return box.Render(p.browser.View(theme))
	}
	return box.Render(p.repos.View(theme))
}

// repoPicker is the HF branch overlay: a bubbles/list of cached repos
// plus an always-present synthetic "type a new repo…" row pinned below
// whatever the type-to-filter shows.
type repoPicker struct {
	list    list.Model
	newRepo bool // the synthetic "type a new repo…" row is selected
}

// repoItem is one cached-repo row: org/repo with its cached quants +
// sizes as the description.
type repoItem struct {
	repo   string
	detail string
	quants []string
}

func (r repoItem) Title() string       { return r.repo }
func (r repoItem) Description() string { return r.detail }
func (r repoItem) FilterValue() string { return r.repo }

// newRepoList builds the list with the same chrome/selection styling
// as paramPicker (reverse video, no chrome, type-to-filter, arrows-only).
func newRepoList(items []list.Item) list.Model {
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	reverse := lipgloss.NewStyle().Reverse(true)
	delegate.Styles.SelectedTitle = reverse
	delegate.Styles.SelectedDesc = reverse
	l := list.New(items, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowFilter(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(true)
	l.SetFilteringEnabled(true)
	l.KeyMap.CursorUp = key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up"))
	l.KeyMap.CursorDown = key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down"))
	return l
}

// Update routes keys. While the list is filtering, every key (including
// enter/esc) goes to the list. Otherwise: ↓ past the last visible item
// steps onto the synthetic new-repo row (and clamps there), ↑ steps
// back; enter picks the row (the new-repo row reports "no change"),
// esc clears an applied filter or cancels the picker.
func (p *repoPicker) Update(msg tea.Msg) (*repoPicker, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if p.list.FilterState() == list.Filtering {
			if k.String() == "enter" {
				// Live filter (consistent with the file browser):
				// enter picks the selected repo instead of confirming
				// the filter; esc (handled by the list) clears it.
				return p, p.pick()
			}
			var cmd tea.Cmd
			p.list, cmd = p.list.Update(msg)
			// Filtering moves the cursor: the synthetic row selection
			// no longer matches what the cursor points at.
			p.newRepo = false
			return p, cmd
		}
		switch k.String() {
		case "esc":
			if p.list.FilterState() == list.FilterApplied {
				p.list.ResetFilter()
				p.newRepo = false
				return p, nil
			}
			return p, func() tea.Msg { return modelPickerDoneMsg{kind: sourceHF, cancelled: true} }
		case "enter":
			return p, p.pick()
		case "down":
			if p.newRepo {
				return p, nil // clamped on the synthetic row
			}
			vis := p.list.VisibleItems()
			if len(vis) == 0 || p.list.Index() == len(vis)-1 {
				p.newRepo = true
				return p, nil
			}
		case "up":
			if p.newRepo {
				p.newRepo = false
				return p, nil
			}
		}
		if isPrintableRune(k) {
			// Type-to-filter, same as paramPicker: the list must be
			// told to enter Filtering explicitly, then the rune is
			// forwarded into the filter input.
			p.list.SetFilterState(list.Filtering)
			var cmd tea.Cmd
			p.list, cmd = p.list.Update(msg)
			p.newRepo = false
			return p, cmd
		}
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	// Any other key the list handled may have moved the cursor (e.g.
	// pgup/pgdn or the first filter keystroke) — the synthetic row
	// selection only stays valid while we clamped on it.
	p.newRepo = false
	return p, cmd
}

// pick emits the done message for the current selection: the synthetic
// "select a repo…" row reports no change (the field keeps what was
// typed), a repo row pre-fills per the §3.8 rule.
func (p *repoPicker) pick() tea.Cmd {
	if p.newRepo {
		return func() tea.Msg { return modelPickerDoneMsg{kind: sourceHF} }
	}
	if it, ok := p.list.SelectedItem().(repoItem); ok {
		return func() tea.Msg {
			return modelPickerDoneMsg{kind: sourceHF, value: prefillRepo(it.repo, it.quants)}
		}
	}
	return nil
}

// View renders the list, the pinned new-repo row, and a hint line.
func (p *repoPicker) View(theme Theme) string {
	parts := []string{}
	if topLine := p.renderFilterLine(theme); topLine != "" {
		parts = append(parts, topLine)
	}
	parts = append(parts, p.list.View())
	parts = append(parts, p.renderNewRepoRow(theme))
	hint := "type to filter · ↑↓: navigate · enter: pick · esc: cancel"
	if p.list.FilterState() == list.FilterApplied {
		hint = "↑↓: navigate · enter: pick · esc: clear filter"
	}
	parts = append(parts, lipgloss.NewStyle().Foreground(theme.Subtle).Render(hint))
	return strings.Join(parts, "\n")
}

// renderNewRepoRow draws the pinned trailing row with the same
// reverse-video selection signal as the list rows.
func (p *repoPicker) renderNewRepoRow(theme Theme) string {
	label := repoTypeNew
	if p.newRepo {
		return lipgloss.NewStyle().Reverse(true).Render(label)
	}
	return lipgloss.NewStyle().Foreground(theme.Subtle).Render(label)
}

// renderFilterLine produces the compact filter indicator (same shape
// as paramPicker's). Empty when not filtering.
func (p *repoPicker) renderFilterLine(theme Theme) string {
	switch p.list.FilterState() {
	case list.Filtering:
		val := p.list.FilterInput.Value()
		label := lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render("filter: ")
		cursor := lipgloss.NewStyle().Foreground(theme.Accent).Render("▎")
		return label + val + cursor
	case list.FilterApplied:
		val := p.list.FilterInput.Value()
		label := lipgloss.NewStyle().Foreground(theme.Subtle).Render("filter: ")
		return label + lipgloss.NewStyle().Foreground(theme.Accent).Render(val)
	}
	return ""
}

// prefillRepo applies the §3.8 pre-fill rule: a single cached quant
// pre-fills org/repo:QUANT; several (or none) pre-fill bare org/repo.
func prefillRepo(repo string, quants []string) string {
	if len(quants) == 1 {
		return repo + ":" + quants[0]
	}
	return repo
}

// pickerStartDir resolves where the local filepicker opens (DESIGN
// §16.5): preferences.models-dir when set, else the current value's
// directory (edit case), else the first local model's directory (the
// "last-used model directory" proxy — llamaman keeps no usage history
// and P8 forbids a field to record one), else home. Candidates must
// exist; the first existing one wins, with home as the final fallback.
func pickerStartDir(modelsDir, current string, models []config.Model) string {
	var cands []string
	if modelsDir != "" {
		cands = append(cands, modelsDir)
	}
	if current != "" {
		cands = append(cands, filepath.Dir(current))
	}
	for _, m := range models {
		if m.Location != "" {
			cands = append(cands, filepath.Dir(m.Location))
			break
		}
	}
	for _, c := range cands {
		if info, err := os.Stat(c); err == nil && info.IsDir() {
			return c
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "."
	}
	return home
}
