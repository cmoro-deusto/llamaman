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

	"github.com/charmbracelet/bubbles/filepicker"
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
const repoTypeNew = "type a new repo…"

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
// .gguf filepicker for the local branch, a cached-repo list for the
// HF branch. It is rendered by ConfigMode over the three-pane
// background (overlayCenter) and reports back via modelPickerDoneMsg.
type modelPicker struct {
	kind         string // sourceLocal | sourceHF
	fp           filepicker.Model
	repos        *repoPicker
	startDir     string // the filepicker's opening directory (the esc-cancel boundary)
	toggleHidden key.Binding // "." — show/hide hidden files in the local picker
	errLine      string // transient overlay error (e.g. selecting a disabled file)
}

// newLocalPicker builds the .gguf filepicker starting at dir.
func newLocalPicker(dir string) *modelPicker {
	fp := filepicker.New()
	fp.CurrentDirectory = dir
	fp.AllowedTypes = []string{".gguf"}
	fp.ShowSize = true
	fp.ShowPermissions = false
	// Hidden files/dirs are listed by default and "." toggles them
	// (owner feedback; Init re-reads the current dir with the new
	// visibility).
	fp.ShowHidden = true
	// DirAllowed stays false (the default): dirs are still navigable
	// via enter, but must never read as a file selection — with it
	// true, entering a directory sets fp.Path and DidSelectFile
	// reports the dir as picked (huh keeps it false for the same
	// reason).
	fp.DirAllowed = false
	fp.FileAllowed = true
	// Arrows-only keymap, matching the config-mode convention (the
	// same rationale as paramPicker dropping j/k). esc doubles as
	// "up one level" and, at the root, "cancel the overlay".
	fp.KeyMap = filepicker.KeyMap{
		GoToTop:  key.NewBinding(),
		GoToLast: key.NewBinding(),
		Up:       key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up")),
		Down:     key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down")),
		PageUp:   key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "page up")),
		PageDown: key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdown", "page down")),
		Back:     key.NewBinding(key.WithKeys("backspace", "esc", "left"), key.WithHelp("esc", "back")),
		Open:     key.NewBinding(key.WithKeys("right", "enter"), key.WithHelp("enter", "open")),
		Select:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	}
	return &modelPicker{
		kind:         sourceLocal,
		fp:           fp,
		startDir:     dir,
		toggleHidden: key.NewBinding(key.WithKeys("."), key.WithHelp(".", "toggle hidden")),
	}
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

// Init starts the overlay's async work (the filepicker's readDir).
// The repo list is fully populated at construction, so it has none.
func (p *modelPicker) Init() tea.Cmd {
	if p.kind == sourceLocal {
		return p.fp.Init()
	}
	return nil
}

// SetSize propagates the overlay dimensions.
func (p *modelPicker) SetSize(w, h int) {
	if p.kind == sourceLocal {
		if h := h - 3; h > 3 {
			p.fp.SetHeight(h)
		}
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
	// Local filepicker.
	if k, ok := msg.(tea.KeyMsg); ok {
		p.errLine = ""
		if key.Matches(k, p.toggleHidden) {
			p.fp.ShowHidden = !p.fp.ShowHidden
			// Init re-reads the current directory with the new
			// visibility (readDirMsg flows back through the loop).
			return p, p.fp.Init()
		}
		if key.Matches(k, p.fp.KeyMap.Back) && p.fp.CurrentDirectory == p.startDir {
			// esc back at the opening directory cancels instead of
			// leaving it: the picker is closed, nothing changes.
			return p, func() tea.Msg { return modelPickerDoneMsg{kind: sourceLocal, cancelled: true} }
		}
	}
	var cmd tea.Cmd
	p.fp, cmd = p.fp.Update(msg)
	if did, path := p.fp.DidSelectFile(msg); did {
		return p, func() tea.Msg { return modelPickerDoneMsg{kind: sourceLocal, value: path} }
	}
	if did, _ := p.fp.DidSelectDisabledFile(msg); did {
		p.errLine = ".gguf files only"
		return p, nil
	}
	return p, cmd
}

// View renders the overlay as a bordered popup (paramPicker shape).
func (p *modelPicker) View(theme Theme) string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2)
	if p.kind == sourceLocal {
		parts := []string{p.fp.View()}
		if p.errLine != "" {
			parts = append(parts, lipgloss.NewStyle().Foreground(theme.Subtle).Render(p.errLine))
		}
		hidden := "off"
		if p.fp.ShowHidden {
			hidden = "on"
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(theme.Subtle).Render(
			"↑↓: move · enter: open/select · esc: back, cancel at start · .: hidden "+hidden))
		return box.Render(strings.Join(parts, "\n"))
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
			if p.newRepo {
				// "type a new repo…": keep whatever the field holds.
				return p, func() tea.Msg { return modelPickerDoneMsg{kind: sourceHF} }
			}
			if it, ok := p.list.SelectedItem().(repoItem); ok {
				return p, func() tea.Msg {
					return modelPickerDoneMsg{kind: sourceHF, value: prefillRepo(it.repo, it.quants)}
				}
			}
			return p, nil
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
