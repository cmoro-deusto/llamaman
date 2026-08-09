package tui

// HF model browser (DESIGN §16.7 / ROADMAP §3.5): search/browse Hugging
// Face inside the TUI. A search box with server-side tag filters
// (language, license) over the search endpoint's fixed gguf library
// filter, a result list, a metadata + quant pane for the selected repo
// (real sizes from one tree/main round trip, (cached) markers), and a
// hand-off of the picked org/repo:QUANT into the config editor's
// new-model form or the Storage manager's download action. Requests
// happen only on explicit enter (P7); every async result carries a
// generation counter and stale messages are dropped.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/hf"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// browserZone is the focused layout zone (Tab cycles).
type browserZone int

const (
	zoneSearch browserZone = iota
	zoneResults
	zoneQuants
)

// browserShield marks an in-flight async request; while set, every key
// is swallowed except esc (cancel) and the request's own done msg.
type browserShield struct {
	kind   string // "search" | "quants"
	repo   string // set for "quants"
	cancel context.CancelFunc
}

// browserSearchDoneMsg carries an async search result. gen ties it to
// the request that produced it — a stale msg must never overwrite a
// newer query's results.
type browserSearchDoneMsg struct {
	gen     int
	results []hf.SearchResult
	err     error
}

// browserQuantsDoneMsg carries an async tree/main → quants result for
// the selected repo (the §16.6 check shape: quants + mmproj + error).
type browserQuantsDoneMsg struct {
	repo   string
	gen    int
	opts   []hf.QuantOption
	mmproj bool
	err    error
}

// browserConfigHandoffMsg asks Root to open the config editor's
// new-model form pre-filled with the picked HF id.
type browserConfigHandoffMsg struct {
	id string // org/repo[:quant]
}

// browserDownloadHandoffMsg asks Root to open the Storage manager and
// start the download for the picked id (downloads stay in the manager
// — the single place downloads are managed, §16.4).
type browserDownloadHandoffMsg struct {
	id string // org/repo[:quant]
}

// returnFromBrowserMsg backs out of the browser to Main.
type returnFromBrowserMsg struct{}

// browserRunner is the injectable network surface: the §16.2 client
// satisfies it; tests inject a stub (the stubSpawner pattern).
type browserRunner interface {
	Search(ctx context.Context, opts hf.SearchOpts) ([]hf.SearchResult, error)
	// CheckHF is the §16.6 tree/main check — one round trip yields the
	// quant list, sizes, and mmproj presence for the quant pane.
	CheckHF(ctx context.Context, repo string) ([]hf.QuantOption, bool, error)
}

// browserClient is the production runner; CheckHF reuses the §16.6
// adapter so both surfaces agree.
type browserClient struct{ c *hf.Client }

func (a browserClient) Search(ctx context.Context, opts hf.SearchOpts) ([]hf.SearchResult, error) {
	res, err := a.c.Search(ctx, opts)
	if err != nil && ctx.Err() != nil {
		// The §16.2 client wraps a canceled request into an hf.Error
		// with no Unwrap/Is — re-raise so esc-cancel is recognizable.
		return nil, ctx.Err()
	}
	return res, err
}

func (a browserClient) CheckHF(ctx context.Context, repo string) ([]hf.QuantOption, bool, error) {
	return hfCheckClient{a.c}.CheckHF(ctx, repo)
}

// BrowserMode is the search/browse screen. The model-info + quant
// pane follows the results cursor automatically (owner flow):
// navigating the results list updates the right pane, so enter on a
// result is not needed — tab moves into the quants pane where enter
// hands the picked quant off.
type BrowserMode struct {
	runner browserRunner
	root   string // cache root for (cached) markers; "" disables them
	theme  Theme

	width  int
	height int

	query string
	input textinput.Model
	sort  string // "" = downloads (the client's default)

	filterLang string // e.g. "ja"; "" = all
	filterLic  string // e.g. "apache-2.0"; "" = any

	results list.Model // zone results
	zone    browserZone

	selected      *hf.SearchResult // repo whose quants are shown
	quants        []hf.QuantOption
	cached        map[string]bool
	mmproj        bool
	quantsLoaded  bool
	quantsLoading bool // a fetch is in flight (inline "loading quants…")
	quantIdx      int

	searchGen   int
	quantGen    int
	quantCancel context.CancelFunc // superseded/cancelled on navigation
	shield      *browserShield     // search only — the full-screen popup

	handoff    *huh.Form
	handoffID  string
	handoffVal string

	tagFilter     *huh.Form
	tagFilterVal  string
	tagFilterKind string // "lang" | "lic"

	flash string
}

// NewBrowserMode builds the browser (search input focused, empty
// results). root is the resolved cache root for (cached) markers.
func NewBrowserMode(theme Theme, root string) BrowserMode {
	in := textinput.New()
	in.Prompt = "search: "
	in.Placeholder = "llama 3 …"
	in.CharLimit = 256
	in.Focus()
	return BrowserMode{
		runner:  nil,
		root:    root,
		theme:   theme,
		input:   in,
		results: newResultList(nil, theme),
		zone:    zoneSearch,
	}
}

// SetBrowserRunner injects the network runner (nil disables search,
// P3 — the mode still renders and esc works).
func (s *BrowserMode) SetBrowserRunner(r browserRunner) { s.runner = r }

// SetSize propagates the window size into the search input; the list
// and panes are sized at render time from s.width/s.height.
func (s *BrowserMode) SetSize(w, h int) {
	s.width, s.height = w, h
	s.input.Width = max(20, w-32)
}

// Update routes messages. The overlay-done messages and the shield run
// before anything else (the §16.4 discipline); the zone keys after.
func (s *BrowserMode) Update(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.input.Width = max(20, msg.Width-32)
		return s, nil
	case browserSearchDoneMsg:
		return s.handleSearchDone(msg)
	case browserQuantsDoneMsg:
		return s.handleQuantsDone(msg)
	}
	if s.tagFilter != nil {
		return s.updateTagFilter(msg)
	}
	if s.handoff != nil {
		return s.updateHandoff(msg)
	}
	if s.shield != nil {
		if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
			s.shield.cancel()
			// Invalidate the in-flight request: a done msg that lands
			// just before cancel() took effect must not apply results
			// after the user cancelled (a re-request bumps the gen
			// again, so nothing is lost).
			if s.shield.kind == "search" {
				s.searchGen++
			} else {
				s.quantGen++
			}
			s.shield = nil
			return s, nil
		}
		return s, nil
	}
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "tab":
			s.cycleZone(false)
			return s, nil
		case "shift+tab":
			s.cycleZone(true)
			return s, nil
		case "esc":
			switch s.zone {
			case zoneSearch:
				return s, func() tea.Msg { return returnFromBrowserMsg{} }
			case zoneResults:
				s.zone = zoneSearch
				return s, nil
			case zoneQuants:
				s.zone = zoneResults
				return s, nil
			}
		}
	}
	switch s.zone {
	case zoneSearch:
		return s.updateSearch(msg)
	case zoneResults:
		return s.updateResults(msg)
	case zoneQuants:
		return s.updateQuants(msg)
	}
	return s, nil
}

// ---- search zone ----

func (s *BrowserMode) updateSearch(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "enter" {
		return s.runSearch()
	}
	// Every printable key types into the input — the l/L tag filters
	// and the t sort cycle live in the results zone (where no text
	// entry happens), the same reason §16.5's picker hotkey is a
	// modifier (ctrl+o). A single-rune keystroke arrives as its own
	// KeyMsg, so intercepting "t"/"l"/"L" here would make queries
	// containing those letters untypeable.
	var cmd tea.Cmd
	s.input, cmd = s.input.Update(msg)
	return s, cmd
}

// runSearch launches the gen-guarded search with the current query,
// sort, and active tag filters, and shields the screen.
func (s *BrowserMode) runSearch() (*BrowserMode, tea.Cmd) {
	if s.runner == nil {
		s.flash = "search unavailable"
		return s, nil
	}
	s.query = s.input.Value()
	opts := hf.SearchOpts{Query: s.query, Sort: s.sort, Filter: s.activeFilters()}
	ctx, cancel := context.WithCancel(context.Background())
	s.searchGen++
	gen := s.searchGen
	s.shield = &browserShield{kind: "search", cancel: cancel}
	return s, func() tea.Msg {
		res, err := s.runner.Search(ctx, opts)
		return browserSearchDoneMsg{gen: gen, results: res, err: err}
	}
}

func (s *BrowserMode) activeFilters() []string {
	var f []string
	if s.filterLang != "" {
		f = append(f, s.filterLang)
	}
	if s.filterLic != "" {
		f = append(f, "license:"+s.filterLic)
	}
	return f
}

func (s *BrowserMode) handleSearchDone(msg browserSearchDoneMsg) (*BrowserMode, tea.Cmd) {
	if msg.gen != s.searchGen {
		return s, nil // stale — a newer search owns the screen
	}
	s.shield = nil
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return s, nil // esc — no flash, no results
		}
		s.flash = browserFlash(msg.err)
		return s, nil
	}
	s.results = newResultList(resultItems(msg.results), s.theme)
	s.selected = nil
	s.quants = nil
	s.cached = nil
	s.mmproj = false
	s.quantsLoaded = false
	s.quantsLoading = false
	s.quantIdx = 0
	s.zone = zoneResults
	if len(msg.results) > 0 {
		// The pane follows the first hit immediately — no enter needed.
		return s.runQuantFetch(msg.results[0])
	}
	return s, nil
}

// ---- results zone ----

func (s *BrowserMode) updateResults(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "l":
			return s.openTagFilter("lang")
		case "L":
			return s.openTagFilter("lic")
		case "t":
			// The footer's "t sort" works from the results zone — the
			// search box owns typing only while it is focused.
			s.sort = nextSort(s.sort)
			return s.runSearch()
		}
	}
	before := s.results.Index()
	var cmd tea.Cmd
	s.results, cmd = s.results.Update(msg)
	if s.results.Index() != before {
		// Seamless: navigating updates the right pane (metadata from
		// the search response, quants async). Stale fetches are
		// cancelled and gen-dropped, so fast navigation is safe.
		if item, ok := s.results.SelectedItem().(resultItem); ok {
			return s.runQuantFetch(item.res)
		}
	}
	return s, cmd
}

// runQuantFetch loads the quants for the selected repo: one tree/main
// round trip, gen-guarded and cancellable. Background by design — the
// pane follows the results cursor, so there is no full-screen shield;
// a superseded fetch is cancelled and its done msg gen-dropped.
func (s *BrowserMode) runQuantFetch(res hf.SearchResult) (*BrowserMode, tea.Cmd) {
	if s.runner == nil {
		s.flash = "search unavailable"
		return s, nil
	}
	s.selected = &res
	s.quants = nil
	s.cached = nil
	s.mmproj = false
	s.quantsLoaded = false
	s.quantsLoading = true
	if s.quantCancel != nil {
		s.quantCancel() // superseded by the new selection
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.quantCancel = cancel
	s.quantGen++
	gen := s.quantGen
	return s, func() tea.Msg {
		opts, mmproj, err := s.runner.CheckHF(ctx, res.ID)
		return browserQuantsDoneMsg{repo: res.ID, gen: gen, opts: opts, mmproj: mmproj, err: err}
	}
}

func (s *BrowserMode) handleQuantsDone(msg browserQuantsDoneMsg) (*BrowserMode, tea.Cmd) {
	if msg.gen != s.quantGen {
		return s, nil // stale — a newer selection owns the pane
	}
	s.quantCancel = nil
	s.quantsLoading = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return s, nil // superseded/cancelled — pane keeps prior state
		}
		s.flash = hfCheckFlash(msg.repo, msg.err)
		// Metadata-only state: the pane still offers the bare-id
		// hand-off (mirrors §16.6's Save path).
		s.quantsLoaded = true
		s.quants = nil
		s.mmproj = false
		return s, nil
	}
	s.quants = msg.opts
	s.mmproj = msg.mmproj
	s.quantsLoaded = true
	s.quantIdx = 0
	// (cached) markers from the cache reader, same rule as the Storage
	// manager's quant picker (P3: lookup failure → empty marker set).
	s.cached = map[string]bool{}
	if files, err := storage.Lookup(s.root, msg.repo); err == nil {
		for _, q := range hf.Quants(repoFiles(files)) {
			s.cached[q.Tag] = true
		}
	}
	return s, nil
}

// ---- quants zone ----

func (s *BrowserMode) updateQuants(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "enter":
			if s.selected == nil || !s.quantsLoaded {
				return s, nil // still loading — nothing to hand off yet
			}
			if len(s.quants) > 0 {
				q := s.quants[s.quantIdx]
				return s.openHandoff(s.selected.ID + ":" + q.Tag)
			}
			return s.openHandoff(s.selected.ID) // the bare-id row
		case "up":
			if len(s.quants) > 0 && s.quantIdx > 0 {
				s.quantIdx--
			}
			return s, nil
		case "down":
			if len(s.quants) > 0 && s.quantIdx < len(s.quants)-1 {
				s.quantIdx++
			}
			return s, nil
		}
	}
	return s, nil
}

// ---- hand-off dialog ----

// openHandoff opens the boxed add-to-config / download-now / cancel
// select for the picked id (bare ids — no GGUF quants — skip the
// download option; hf.Download requires a quant).
func (s *BrowserMode) openHandoff(id string) (*BrowserMode, tea.Cmd) {
	options := []huh.Option[string]{huh.NewOption("add to config", "config")}
	if strings.Contains(id, ":") {
		options = append(options, huh.NewOption("download now", "download"))
	}
	options = append(options, huh.NewOption("cancel", "cancel"))
	s.handoffID = id
	s.handoffVal = ""
	s.handoff = huh.NewForm(huh.NewGroup(
		huh.NewSelect[string]().
			Title("hand off " + id).
			Options(options...).
			Value(&s.handoffVal),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(formWidthFor(s.width))
	return s, s.handoff.Init()
}

func (s *BrowserMode) updateHandoff(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.handoff = nil
		return s, nil
	}
	next, cmd := s.handoff.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.handoff = f
	}
	if s.handoff != nil && s.handoff.State == huh.StateCompleted {
		choice, id := s.handoffVal, s.handoffID
		s.handoff = nil
		switch choice {
		case "config":
			return s, func() tea.Msg { return browserConfigHandoffMsg{id: id} }
		case "download":
			return s, func() tea.Msg { return browserDownloadHandoffMsg{id: id} }
		}
	}
	return s, cmd
}

// ---- tag filters ----

// browserLangs and browserLicenses are the curated filter options — the
// picker options and the request assembly both read from them (the
// "same helpers" rule). Values are HF tag values: bare language codes
// and license:<id> suffixes.
var browserLangs = []string{"en", "es", "de", "fr", "it", "pt", "ja", "zh", "ko", "ru", "ar", "hi", "th", "multilingual"}

var browserLicenses = []string{"apache-2.0", "mit", "llama3.1", "llama3.2", "llama3.3", "gemma", "openrail", "cc-by-nc-4.0", "other"}

func (s *BrowserMode) openTagFilter(kind string) (*BrowserMode, tea.Cmd) {
	var options []huh.Option[string]
	title := "filter by language"
	if kind == "lang" {
		options = append(options, huh.NewOption("all languages", ""))
		for _, l := range browserLangs {
			options = append(options, huh.NewOption(l, l))
		}
	} else {
		title = "filter by license"
		options = append(options, huh.NewOption("any license", ""))
		for _, l := range browserLicenses {
			options = append(options, huh.NewOption(l, l))
		}
	}
	sel := huh.NewSelect[string]().
		Title(title).
		Options(options...).
		Value(&s.tagFilterVal)
	// Height-capped so the box fits small terminals (item-6 discipline:
	// offset 1 = the title row the Select subtracts from its viewport).
	if maxRows := max(4, s.height-12); len(options) > maxRows {
		sel.Height(maxRows + 1)
	}
	s.tagFilterKind = kind
	s.tagFilterVal = ""
	s.tagFilter = huh.NewForm(huh.NewGroup(sel)).
		WithTheme(configHuhTheme(s.theme)).
		WithWidth(formWidthFor(s.width))
	return s, s.tagFilter.Init()
}

func (s *BrowserMode) updateTagFilter(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.tagFilter = nil
		return s, nil
	}
	next, cmd := s.tagFilter.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.tagFilter = f
	}
	if s.tagFilter != nil && s.tagFilter.State == huh.StateCompleted {
		v, kind := s.tagFilterVal, s.tagFilterKind
		s.tagFilter = nil
		if kind == "lang" {
			s.filterLang = v
		} else {
			s.filterLic = v
		}
		return s.runSearch() // re-run with the new filter, same query
	}
	return s, cmd
}

// ---- misc helpers ----

// cycleZone moves the focus. Tab toggles the results/quants pair (the
// pane follows the cursor, so tab just picks which side you act on);
// from the search box either direction lands on the results.
func (s *BrowserMode) cycleZone(back bool) {
	switch {
	case !back && s.zone == zoneSearch:
		s.zone = zoneResults
	case !back && s.zone == zoneResults:
		s.zone = zoneQuants
	case !back: // zoneQuants
		s.zone = zoneResults
	case back && s.zone == zoneQuants:
		s.zone = zoneResults
	case back && s.zone == zoneResults:
		s.zone = zoneSearch
	default: // back && zoneSearch
		s.zone = zoneResults
	}
}

// browserSorts is the t-key cycle (the search endpoint's sort fields).
var browserSorts = []string{"downloads", "likes", "lastModified"}

func nextSort(cur string) string {
	if cur == "" {
		return "likes"
	}
	for i, s := range browserSorts {
		if s == cur {
			return browserSorts[(i+1)%len(browserSorts)]
		}
	}
	return browserSorts[0]
}

func effSort(cur string) string {
	if cur == "" {
		return browserSorts[0]
	}
	return cur
}

// browserFlash maps a search failure to its distinct message (the §16.2
// kinds; hfCheckFlash covers the per-repo quant-fetch failures).
func browserFlash(err error) string {
	switch {
	case hf.IsNotFound(err):
		return "search failed: not found"
	case hf.IsGated(err):
		return "search failed: gated — requires HF_TOKEN"
	default:
		var he *hf.Error
		if errors.As(err, &he) && he.Kind == hf.ErrHTTP {
			return fmt.Sprintf("search failed: HTTP %d", he.Status)
		}
		return "search failed: could not reach Hugging Face"
	}
}

// ---- result items ----

// resultItem is one search hit in the results list.
type resultItem struct {
	res hf.SearchResult
}

func (i resultItem) Title() string       { return i.res.ID }
func (i resultItem) Description() string { return describeResult(i.res) }
func (i resultItem) FilterValue() string { return i.res.ID }

func resultItems(results []hf.SearchResult) []list.Item {
	out := make([]list.Item, 0, len(results))
	for _, r := range results {
		out = append(out, resultItem{res: r})
	}
	return out
}

// newResultList builds the results list with the repoPicker chrome
// (reverse-video selection, no title/filter/help chrome, arrows-only
// keymap) — filtering stays in the search box, so list filtering is
// disabled. Colors (owner): titles in Subtle, descriptions in Muted,
// the selected row accent-bold on the reversed background.
func newResultList(items []list.Item, theme Theme) list.Model {
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	pad := lipgloss.NewStyle().Padding(0, 0, 0, 2)
	delegate.Styles.NormalTitle = pad.Foreground(theme.Subtle)
	delegate.Styles.NormalDesc = pad.Foreground(theme.Muted)
	delegate.Styles.SelectedTitle = pad.Reverse(true).Foreground(theme.Accent).Bold(true)
	delegate.Styles.SelectedDesc = pad.Reverse(true).Foreground(theme.Subtle)
	l := list.New(items, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowFilter(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(true)
	l.SetFilteringEnabled(false)
	l.KeyMap.CursorUp = key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "up"))
	l.KeyMap.CursorDown = key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "down"))
	// Truly arrows-only: the default bubbles keymap still binds page
	// jumps (h/j/k/l/b/f/g/G), help, and quit — unbind them so the
	// footer's ↑/↓ · enter is the whole surface.
	l.KeyMap.PrevPage = key.NewBinding()
	l.KeyMap.NextPage = key.NewBinding()
	l.KeyMap.GoToStart = key.NewBinding()
	l.KeyMap.GoToEnd = key.NewBinding()
	l.KeyMap.ShowFullHelp = key.NewBinding()
	l.KeyMap.CloseFullHelp = key.NewBinding()
	l.KeyMap.Quit = key.NewBinding()
	l.KeyMap.ForceQuit = key.NewBinding()
	l.KeyMap.Filter = key.NewBinding()
	l.KeyMap.ClearFilter = key.NewBinding()
	return l
}

// ---- tag extraction (all from the search response's raw tags) ----

// licenseOf returns the repo's license id (the "license:<id>" tag).
func licenseOf(r hf.SearchResult) string {
	for _, t := range r.Tags {
		if strings.HasPrefix(t, "license:") {
			return strings.TrimPrefix(t, "license:")
		}
	}
	return ""
}

// languagesOf returns the bare language-code tags. HF tags carry
// ISO-ish short codes ("en", "ja") plus "multilingual"; the heuristic
// accepts 2-3 letter lowercase tags (a display hint, not a contract).
func languagesOf(r hf.SearchResult) []string {
	var out []string
	for _, t := range r.Tags {
		if t == "multilingual" || (len(t) >= 2 && len(t) <= 3 && isLowerAlpha(t)) {
			out = append(out, t)
		}
	}
	return out
}

// baseModelOf returns the repo the GGUF was quantized from
// ("base_model:quantized:<id>", falling back to "base_model:<id>").
func baseModelOf(r hf.SearchResult) string {
	for _, t := range r.Tags {
		if strings.HasPrefix(t, "base_model:quantized:") {
			return strings.TrimPrefix(t, "base_model:quantized:")
		}
	}
	for _, t := range r.Tags {
		if strings.HasPrefix(t, "base_model:") {
			return strings.TrimPrefix(t, "base_model:")
		}
	}
	return ""
}

// nonCommercialLicense returns a warning line when the repo's license
// is in the cc-by-nc family (display only, P3 — never blocks).
func nonCommercialLicense(r hf.SearchResult) string {
	if strings.HasPrefix(licenseOf(r), "cc-by-nc") {
		return "non-commercial license — check terms"
	}
	return ""
}

func isLowerAlpha(s string) bool {
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// describeResult builds the result-row description:
// "743k downloads · 17 likes · en ja · license: llama3.1".
func describeResult(r hf.SearchResult) string {
	parts := []string{humanCount(r.Downloads) + " downloads", fmt.Sprintf("%d likes", r.Likes)}
	if langs := languagesOf(r); len(langs) > 0 {
		parts = append(parts, strings.Join(langs, " "))
	}
	if lic := licenseOf(r); lic != "" {
		parts = append(parts, "license: "+lic)
	}
	return strings.Join(parts, " · ")
}

// humanCount renders a compact count: 743450 → "743k", 1700 → "1.7k".
func humanCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ---- rendering ----

func (s *BrowserMode) View() string {
	if s.width == 0 {
		s.width = 80
	}
	if s.height == 0 {
		s.height = 24
	}
	cw := max(40, s.width-8)
	body := []string{
		lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render("browse — Hugging Face (gguf)"),
		"",
		s.renderSearchBox(cw),
	}
	if f := s.renderFilterLine(); f != "" {
		body = append(body, f)
	}
	body = append(body, "")
	body = append(body, s.renderPanes(cw)...)
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
	if s.shield != nil {
		return overlayCenter(bg, s.shieldView(), s.width, s.height)
	}
	if ov := s.overlayView(); ov != "" {
		return overlayCenter(bg, ov, s.width, s.height)
	}
	return bg
}

// renderSearchBox draws the search line + the sort indicator inside a
// thin rectangle. The input width is reserved so the indicator never
// overflows the box's right border (owner report: long sort values
// used to overlap the border). The sort value is accent-bold — the
// active sort is evident at a glance (owner).
func (s *BrowserMode) renderSearchBox(cw int) string {
	inner := cw - 2
	label := lipgloss.NewStyle().Foreground(s.theme.Subtle).Render("sort: ")
	value := lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render(effSort(s.sort))
	sortLen := len("sort: ") + len(effSort(s.sort))
	// 10 = prompt ("search: ") + the "  " gap + margin
	s.input.Width = max(10, inner-10-sortLen)
	line := s.input.View() + "  " + label + value
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(s.theme.Border).
		Width(cw).
		Render(line)
}

func (s *BrowserMode) renderFilterLine() string {
	var active []string
	if s.filterLang != "" {
		active = append(active, s.filterLang)
	}
	if s.filterLic != "" {
		active = append(active, s.filterLic)
	}
	if len(active) == 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(
		"filter: " + strings.Join(active, " · ") + "  (l/L)")
}

// renderPanes lays out the results box (left) and the model-info +
// quants box (right) side by side inside their own thin rectangles.
func (s *BrowserMode) renderPanes(cw int) []string {
	lw := cw * 2 / 5
	rw := cw - lw - 1 // " " separator
	ph := max(6, s.height-15)

	var left []string
	if len(s.results.Items()) == 0 {
		hint := "enter a query and press enter"
		if s.query != "" {
			hint = "no results for " + s.query
		}
		left = []string{lipgloss.NewStyle().Foreground(s.theme.Muted).Render(hint)}
	} else {
		left = append(left, lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render(
			fmt.Sprintf("results (%d)", len(s.results.Items()))))
		s.results.SetSize(lw-2, ph-1)
		left = append(left, strings.Split(s.results.View(), "\n")...)
	}
	right := s.infoLines(rw-2, ph)

	lb := boxLines(left, lw, ph+2, s.theme)
	rb := boxLines(right, rw, ph+2, s.theme)
	return joinPanes(lb, rb)
}

// boxLines renders content inside a fixed-width rounded box of exactly
// h lines (content padded/truncated to h-2 inner lines) — the results
// and model-info panes share the height so both sides align.
func boxLines(content []string, w, h int, theme Theme) []string {
	inner := w - 2
	maxLines := h - 2
	if len(content) > maxLines {
		content = content[:maxLines]
	}
	for i := range content {
		content[i] = padLinesTo(content[i], inner)
	}
	for len(content) < maxLines {
		content = append(content, strings.Repeat(" ", inner))
	}
	b := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(theme.Border).
		Width(w)
	return strings.Split(b.Render(strings.Join(content, "\n")), "\n")
}

func joinPanes(left, right []string) []string {
	h := max(len(left), len(right))
	out := make([]string, 0, h)
	for i := 0; i < h; i++ {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out = append(out, l+" "+r)
	}
	return out
}

// infoLines renders the model-info + quants pane for the selected repo
// (owner layout): repo name, "from <base_model>", separator, downloads
// and likes, license, task, the non-commercial warning, separator, and
// the quant rows (with the ● cached badge).
func (s *BrowserMode) infoLines(inner, ph int) []string {
	var lines []string
	muted := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Muted).Render(t) }
	subtle := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(t) }
	accent := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Accent).Render(t) }
	warn := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.StatusStart).Render(t) }
	sep := subtle(strings.Repeat("─", max(10, inner)))

	if s.selected == nil {
		lines = append(lines, muted("select a repo…"))
	} else {
		m := s.selected
		lines = append(lines, accent(m.ID))
		if bm := baseModelOf(*m); bm != "" {
			lines = append(lines, subtle("from "+bm))
		}
		lines = append(lines, sep)
		lines = append(lines, "⬇ "+humanCount(m.Downloads)+" downloads")
		lines = append(lines, "♥ "+strconv.FormatInt(m.Likes, 10)+" likes")
		lines = append(lines, subtle("license: "+licenseOf(*m)))
		if m.PipelineTag != "" {
			lines = append(lines, subtle("task: "+m.PipelineTag))
		}
		if w := nonCommercialLicense(*m); w != "" {
			lines = append(lines, warn("⚠ "+w))
		}
		lines = append(lines, sep)
		switch {
		case !s.quantsLoaded:
			lines = append(lines, subtle("loading quants…"))
		case len(s.quants) > 0:
			lines = append(lines, accent(fmt.Sprintf("quants (%d)", len(s.quants))))
			for i, q := range s.quants {
				row := quantRowLabel(q, false)
				if s.cached[q.Tag] {
					row += "  " + cachedBadge(s.theme)
				}
				if s.zone == zoneQuants && i == s.quantIdx {
					row = "▶ " + row
				} else {
					row = "  " + row
				}
				lines = append(lines, row)
			}
			if s.mmproj {
				lines = append(lines, subtle("mmproj present — llama.cpp auto-downloads it"))
			}
		default: // loaded, no GGUF quants (or fetch failed): the bare row
			row := "use " + m.ID + " without a quant"
			if s.zone == zoneQuants && s.quantIdx == 0 {
				row = "▶ " + row
			} else {
				row = "  " + row
			}
			lines = append(lines, row)
			if s.mmproj {
				lines = append(lines, subtle("mmproj present — llama.cpp auto-downloads it"))
			}
		}
	}
	return lines
}

// cachedBadge renders the fancy "already on disk" marker — a green
// dot + label instead of the plain "(cached)" suffix (owner).
func cachedBadge(theme Theme) string {
	return lipgloss.NewStyle().Foreground(theme.StatusReady).Bold(true).Render("● cached")
}

// renderFooter shows the shortcuts relevant to the focused zone — the
// quants zone advertises enter as "hand off" (owner).
func (s *BrowserMode) renderFooter() string {
	var keys []string
	switch s.zone {
	case zoneSearch:
		keys = []string{
			shortcut("enter", "search", s.theme),
			shortcut("tab", "results", s.theme),
			shortcut("esc", "back", s.theme),
		}
	case zoneResults:
		keys = []string{
			shortcut("↑/↓", "navigate", s.theme),
			shortcut("tab", "quants", s.theme),
			shortcut("l/L", "filter", s.theme),
			shortcut("t", "sort", s.theme),
			shortcut("esc", "search", s.theme),
		}
	case zoneQuants:
		keys = []string{
			shortcut("↑/↓", "quant", s.theme),
			shortcut("enter", "hand off", s.theme),
			shortcut("tab", "results", s.theme),
			shortcut("esc", "back", s.theme),
		}
	}
	return strings.Join(keys, "  ·  ")
}

// shieldView renders the static in-flight search popup (no spinner —
// one bounded call; static text keeps snapshot tests deterministic).
// The quants fetch is background by design (it follows the cursor), so
// only the search gets a shield.
func (s *BrowserMode) shieldView() string {
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render("searching Hugging Face…"),
		lipgloss.NewStyle().Foreground(s.theme.Subtle).Render("esc: cancel"),
	}
	return overlayBox(s.theme, strings.Join(lines, "\n"))
}

// overlayView renders the active huh overlay (hand-off dialog or tag
// filter) boxed like the §16.6 dialogs.
func (s *BrowserMode) overlayView() string {
	var v string
	switch {
	case s.handoff != nil:
		v = s.handoff.View()
	case s.tagFilter != nil:
		v = s.tagFilter.View()
	default:
		return ""
	}
	return overlayBox(s.theme, v)
}
