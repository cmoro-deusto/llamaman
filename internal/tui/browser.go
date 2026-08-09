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
	"regexp"
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

// browserCardDoneMsg carries the async model-card (README) fetch.
// gen ties it to the selection that produced it.
type browserCardDoneMsg struct {
	repo string
	gen  int
	text string
	err  error
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
	// Card fetches the repo's model card (README.md) text.
	Card(ctx context.Context, repo string) (string, error)
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

func (a browserClient) Card(ctx context.Context, repo string) (string, error) {
	text, err := a.c.Card(ctx, repo)
	if err != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return text, err
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

	filterLang         string  // e.g. "ja"; "" = all
	filterLic          string  // e.g. "apache-2.0"; "" = any
	filterTask         string  // e.g. "text-generation"; "" = any
	paramMin, paramMax float64 // params filter (billions, name-derived); 0 = none

	allResults []hf.SearchResult // the full fetched page (client-side filters apply on top)
	results    list.Model        // the displayed (filtered) list
	zone       browserZone

	selected      *hf.SearchResult // repo whose quants are shown
	quants        []hf.QuantOption
	cached        map[string]bool
	mmproj        bool
	quantsLoaded  bool
	quantsLoading bool // a fetch is in flight (inline "loading quants…")
	quantIdx      int
	quantOffset   int // first visible quant (windowed list, scrolls with the cursor)

	cardLines   []string // model card rendered to styled lines (markdown)
	cardErr     string   // friendly non-blocking note when the card is unavailable
	cardLoading bool
	cardOffset  int // first visible card line (pgup/pgdown scroll)

	searchGen   int
	quantGen    int
	cardGen     int
	quantCancel context.CancelFunc // superseded/cancelled on navigation
	cardCancel  context.CancelFunc
	shield      *browserShield // search only — the full-screen popup

	handoff    *huh.Form
	handoffID  string
	handoffVal string

	tagFilter     *huh.Form
	tagFilterVal  string
	tagFilterKind string // "lang" | "lic" | "task"
	paramsForm    *huh.Form
	paramsMinVal  string // staging for the min input
	paramsMaxVal  string // staging for the max input

	flash string
}

// NewBrowserMode builds the browser (search input focused, empty
// results). root is the resolved cache root for (cached) markers.
func NewBrowserMode(theme Theme, root string) BrowserMode {
	in := textinput.New()
	in.Prompt = "" // the panel's "search" title makes a prompt redundant
	in.Placeholder = "search Hugging Face… (empty = browse)"
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
	case browserCardDoneMsg:
		return s.handleCardDone(msg)
	}
	if s.tagFilter != nil {
		return s.updateTagFilter(msg)
	}
	if s.paramsForm != nil {
		return s.updateParamsForm(msg)
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
	sort := s.sort
	if sort == "" {
		sort = browserSorts[0] // the trending default
	}
	opts := hf.SearchOpts{
		Query:       s.query,
		Sort:        sort,
		Filter:      s.activeFilters(),
		PipelineTag: s.filterTask,
	}
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
	s.allResults = msg.results
	s.selected = nil
	s.quants = nil
	s.cached = nil
	s.mmproj = false
	s.quantsLoaded = false
	s.quantsLoading = false
	s.quantIdx = 0
	s.zone = zoneResults
	// Client-side params filter applies over the fetched page (the
	// search API has no params field).
	if s.paramMin > 0 || s.paramMax > 0 {
		return s.applyParamFilter()
	}
	s.results = newResultList(resultItems(msg.results), s.theme)
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
		case "k":
			return s.openTagFilter("task")
		case "m":
			return s.openParamsForm()
		case "s":
			// The footer's "s sort" works from the results zone — the
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
	s.quantIdx = 0
	s.quantOffset = 0
	if s.quantCancel != nil {
		s.quantCancel() // superseded by the new selection
	}
	qctx, qcancel := context.WithCancel(context.Background())
	s.quantCancel = qcancel
	s.quantGen++
	qgen := s.quantGen
	// The model card fetches alongside the quants, same discipline.
	s.cardLines = nil
	s.cardErr = ""
	s.cardLoading = true
	s.cardOffset = 0
	if s.cardCancel != nil {
		s.cardCancel()
	}
	cctx, ccancel := context.WithCancel(context.Background())
	s.cardCancel = ccancel
	s.cardGen++
	cgen := s.cardGen
	return s, tea.Batch(
		func() tea.Msg {
			opts, mmproj, err := s.runner.CheckHF(qctx, res.ID)
			return browserQuantsDoneMsg{repo: res.ID, gen: qgen, opts: opts, mmproj: mmproj, err: err}
		},
		func() tea.Msg {
			text, err := s.runner.Card(cctx, res.ID)
			return browserCardDoneMsg{repo: res.ID, gen: cgen, text: text, err: err}
		},
	)
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

// handleCardDone lands the model card text (or its friendly absence
// note) on the pane; stale gens are dropped.
func (s *BrowserMode) handleCardDone(msg browserCardDoneMsg) (*BrowserMode, tea.Cmd) {
	if msg.gen != s.cardGen {
		return s, nil
	}
	s.cardCancel = nil
	s.cardLoading = false
	if msg.err != nil {
		if errors.Is(msg.err, context.Canceled) {
			return s, nil
		}
		if hf.IsNotFound(msg.err) {
			s.cardErr = "no model card"
		} else {
			s.cardErr = "could not load model card"
		}
		s.cardLines = nil
		return s, nil
	}
	s.cardLines = renderCardMarkdown(s.theme, []byte(trimCardFrontmatter(msg.text)))
	s.cardErr = ""
	s.cardOffset = 0
	return s, nil
}

// trimCardFrontmatter drops the README's leading YAML frontmatter
// (--- … ---), leaving the card text proper.
func trimCardFrontmatter(text string) string {
	if !strings.HasPrefix(text, "---") {
		return text
	}
	lines := strings.Split(text, "\n")
	for i := 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "---") {
			return strings.Join(lines[i+1:], "\n")
		}
	}
	return text
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
				// keep the cursor inside the window
				if s.quantIdx < s.quantOffset {
					s.quantOffset = s.quantIdx
				}
			}
			return s, nil
		case "down":
			if len(s.quants) > 0 && s.quantIdx < len(s.quants)-1 {
				s.quantIdx++
			}
			return s, nil
		case "pgup":
			s.cardOffset = max(0, s.cardOffset-5)
			return s, nil
		case "pgdown":
			s.cardOffset += 5
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

// browserTasks is the curated server-side pipeline_tag filter — the
// GGUF-relevant tasks (verified live: the search endpoint accepts the
// pipeline_tag query param).
var browserTasks = []string{"text-generation", "translation", "text2text-generation", "feature-extraction", "sentence-similarity", "image-text-to-text"}

func (s *BrowserMode) openTagFilter(kind string) (*BrowserMode, tea.Cmd) {
	var options []huh.Option[string]
	title := "filter by language"
	switch kind {
	case "lang":
		options = append(options, huh.NewOption("all languages", ""))
		for _, l := range browserLangs {
			options = append(options, huh.NewOption(l, l))
		}
	case "lic":
		title = "filter by license"
		options = append(options, huh.NewOption("any license", ""))
		for _, l := range browserLicenses {
			options = append(options, huh.NewOption(l, l))
		}
	case "task":
		title = "filter by task"
		options = append(options, huh.NewOption("any task", ""))
		for _, t := range browserTasks {
			options = append(options, huh.NewOption(t, t))
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
		switch kind {
		case "lang":
			s.filterLang = v
		case "lic":
			s.filterLic = v
		case "task":
			s.filterTask = v
		}
		return s.runSearch() // re-run with the new filter, same query
	}
	return s, cmd
}

// openParamsForm opens the client-side params (size) filter: min and
// max billions, applied over the current result page (the search API
// has no params field, so the filter is name-derived and page-scoped —
// flagged in the design note).
func (s *BrowserMode) openParamsForm() (*BrowserMode, tea.Cmd) {
	s.paramsMinVal = ""
	s.paramsMaxVal = ""
	s.paramsForm = huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("min params (B)").Placeholder("e.g. 7").Value(&s.paramsMinVal).Validate(optionalFloat),
		huh.NewInput().Title("max params (B)").Placeholder("e.g. 70").Value(&s.paramsMaxVal).Validate(optionalFloat),
	)).WithTheme(configHuhTheme(s.theme)).WithWidth(formWidthFor(s.width))
	return s, s.paramsForm.Init()
}

func optionalFloat(s string) error {
	if s == "" {
		return nil
	}
	if _, err := strconv.ParseFloat(s, 64); err != nil {
		return errors.New("a number, or empty")
	}
	return nil
}

func (s *BrowserMode) updateParamsForm(msg tea.Msg) (*BrowserMode, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
		s.paramsForm = nil
		return s, nil
	}
	next, cmd := s.paramsForm.Update(msg)
	if f, ok := next.(*huh.Form); ok {
		s.paramsForm = f
	}
	if s.paramsForm != nil && s.paramsForm.State == huh.StateCompleted {
		minV, maxV := s.paramsMinVal, s.paramsMaxVal
		s.paramsForm = nil
		s.paramMin, _ = strconv.ParseFloat(minV, 64)
		s.paramMax, _ = strconv.ParseFloat(maxV, 64)
		if s.paramMin == 0 && s.paramMax == 0 {
			return s, nil // cleared
		}
		return s.applyParamFilter()
	}
	return s, cmd
}

// applyParamFilter re-renders the displayed list through the params
// filter (name-derived) and re-follows the first hit.
func (s *BrowserMode) applyParamFilter() (*BrowserMode, tea.Cmd) {
	filtered := make([]hf.SearchResult, 0, len(s.allResults))
	for _, r := range s.allResults {
		if p, ok := paramCountOf(r); ok {
			if s.paramMin > 0 && p < s.paramMin {
				continue
			}
			if s.paramMax > 0 && p > s.paramMax {
				continue
			}
		}
		filtered = append(filtered, r)
	}
	s.results = newResultList(resultItems(filtered), s.theme)
	s.quantIdx = 0
	if len(filtered) > 0 {
		return s.runQuantFetch(filtered[0])
	}
	s.selected = nil
	s.quants = nil
	s.quantsLoaded = false
	return s, nil
}

// paramCountOf extracts a parameter count (in billions) from the
// repo's name — base_model first, then the repo id — matching the
// "<N>B"-style suffix most model names carry (8B, 32B, 2.6B …). A
// display heuristic, not an API field: the search endpoint exposes no
// params metadata (flagged in the design note).
func paramCountOf(r hf.SearchResult) (float64, bool) {
	id := r.ID
	if bm := baseModelOf(r); bm != "" {
		id = bm
	}
	m := paramRe.FindStringSubmatch(id)
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	switch strings.ToLower(m[2]) {
	case "k":
		v /= 1000
	case "m":
		v /= 1000
	}
	return v, true
}

var paramRe = regexp.MustCompile(`(?i)\b(\d+(?:\.\d+)?)([km]?b)\b`)

// ---- misc helpers ----

// cycleZone moves the focus (owner round): the search bar is part of
// the tab cycle — tab: search → results → quants → search; shift+tab
// reverses. esc still backs out one step (quants → results → search →
// exit).
func (s *BrowserMode) cycleZone(back bool) {
	n := 3
	if back {
		s.zone = browserZone((int(s.zone) + n - 1) % n)
	} else {
		s.zone = browserZone((int(s.zone) + 1) % n)
	}
}

// browserSorts is the s-key cycle — the search endpoint's sort fields,
// mirroring the HF site's Models ranking (verified live). The DEFAULT
// sort is trendingScore (owner round): both browse and search start at
// "trending", matching huggingface.co/models.
var browserSorts = []string{"trendingScore", "downloads", "likes", "createdAt", "lastModified"}

// sortLabels are the friendly names shown in the sort indicator.
var sortLabels = map[string]string{
	"trendingScore": "trending",
	"downloads":     "downloads",
	"likes":         "likes",
	"createdAt":     "newest",
	"lastModified":  "updated",
}

func nextSort(cur string) string {
	if cur == "" {
		return browserSorts[1] // first press leaves the trending default
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
		cur = browserSorts[0] // the trending default
	}
	if l, ok := sortLabels[cur]; ok {
		return l
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
		s.renderSearchBox(cw, s.zone == zoneSearch),
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

// renderSearchBox draws the search panel: the accent-bold `search: `
// prompt + input on the content line, the sort indicator right-aligned
// on the same line, the panel title ("search") embedded in the top
// border. The input width is reserved so the indicator never overflows
// the box's right border; the value is accent-bold with a friendly
// label. The border lights up (BorderFocus) when the search zone is
// focused (owner round, router-mode pattern).
func (s *BrowserMode) renderSearchBox(cw int, focused bool) string {
	inner := cw - 2
	label := lipgloss.NewStyle().Foreground(s.theme.Subtle).Render("sort: ")
	value := lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render(effSort(s.sort))
	sortLen := len("sort: ") + len(effSort(s.sort))
	// 3 = cursor + the "  " gap + margin. The input pads to Width+1
	// (cursor) with no prompt, so the line must fit inner exactly or
	// the sort value wraps onto its own line.
	s.input.Width = max(10, inner-3-sortLen)
	line := s.input.View() + "  " + label + value
	return strings.Join(titledBoxLines([]string{line}, "search", cw, 3, s.theme, focused), "\n")
}

func (s *BrowserMode) renderFilterLine() string {
	var active []string
	if s.filterLang != "" {
		active = append(active, s.filterLang)
	}
	if s.filterLic != "" {
		active = append(active, s.filterLic)
	}
	if s.filterTask != "" {
		active = append(active, s.filterTask)
	}
	if s.paramMin > 0 || s.paramMax > 0 {
		active = append(active, fmt.Sprintf("%.0fB-%.0fB", s.paramMin, s.paramMax))
	}
	if len(active) == 0 {
		return ""
	}
	return lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(
		"filter: " + strings.Join(active, " · ") + "  (l/L/k/m)")
}

// renderPanes lays out the results panel (left, full height) and the
// right column of three titled panels — model info (content-sized:
// name, params·from, downloads, likes, license, task), the quants
// panel (scrollable list, in the tab focus flow), and the model card
// (scrollable) — stacking to the results height so the column
// bottom-aligns (owner round). The focused panel's border lights up
// (BorderFocus).
func (s *BrowserMode) renderPanes(cw int) []string {
	lw := cw * 2 / 5
	rw := cw - lw - 1 // " " separator
	ph := max(6, s.height-15)

	var left []string
	if len(s.results.Items()) == 0 {
		hint := "search, or press enter to browse"
		if s.query != "" {
			hint = "no results for " + s.query
		}
		left = []string{lipgloss.NewStyle().Foreground(s.theme.Muted).Render(hint)}
	} else {
		s.results.SetSize(lw-2, ph-2)
		left = strings.Split(s.results.View(), "\n")
	}
	lb := titledBoxLines(left, fmt.Sprintf("results (%d)", len(s.results.Items())), lw, ph, s.theme, s.zone == zoneResults)

	info := s.infoLines(rw - 2)
	infoH := len(info) + 2
	rem := max(0, ph-infoH)
	// The quants panel shows a fixed 5-quant window (title border + 5
	// rows + bottom border = 7 lines, owner round); the model card
	// panel takes every freed line.
	quantsH := min(7, rem)
	cardH := max(0, rem-quantsH)
	ib := titledBoxLines(info, "model info", rw, infoH, s.theme, false)
	qb := titledBoxLines(s.quantsLines(rw-2, max(0, quantsH-2)), fmt.Sprintf("quants (%d)", len(s.quants)), rw, quantsH, s.theme, s.zone == zoneQuants)
	cb := titledBoxLines(s.cardPanelLines(rw-2, max(0, cardH-2)), "model card", rw, cardH, s.theme, false)

	return joinPanes(lb, joinLines(ib, qb, cb))
}

// titledBoxLines renders content inside a fixed-width rounded box of
// exactly h lines with the panel title embedded in the top border line
// (owner round: "all panels should have their title enclosed in the
// top line"). Drawn manually — lipgloss Width() wraps long lines
// instead of truncating, which previously pushed boxes past their
// allocation (owner report: the search bar rendered one character
// wider than the model info and long lines wrapped onto new rows).
// focused lights the border up (BorderFocus, the router-mode pattern).
func titledBoxLines(content []string, title string, w, h int, theme Theme, focused bool) []string {
	if h < 2 {
		return nil // no room for a box (tiny terminals): the caller skips it
	}
	inner := w - 2
	border := theme.Border
	if focused {
		border = theme.BorderFocus
	}
	bs := lipgloss.NewStyle().Foreground(border)
	// ╭─ title ──────────╮  (width w exactly)
	dashes := max(0, inner-len(title)-3)
	top := bs.Render("╭─ ") +
		lipgloss.NewStyle().Foreground(theme.Accent).Bold(true).Render(title) +
		bs.Render(" "+strings.Repeat("─", dashes)+"╮")
	side := bs.Render("│")
	out := make([]string, 0, h)
	out = append(out, padLinesTo(top, w))
	maxLines := max(0, h-2)
	if len(content) > maxLines {
		content = content[:maxLines]
	}
	for _, ln := range content {
		out = append(out, side+truncatePad(ln, inner)+side)
	}
	for len(out) < h-1 {
		out = append(out, side+strings.Repeat(" ", inner)+side)
	}
	out = append(out, bs.Render("╰"+strings.Repeat("─", inner)+"╯"))
	return out
}

// truncatePad truncates and pads a line to exactly width — box content
// must fit or the box grows (lipgloss Width wraps; MaxWidth truncates
// explicitly).
func truncatePad(s string, width int) string {
	s = lipgloss.NewStyle().MaxWidth(width).Render(s)
	return padLinesTo(s, width)
}

// joinPanes lays two same-height panel line-sets side by side with a
// one-space gutter (the results panel and the right column).
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

// joinLines stacks panel line-sets vertically (the right column).
func joinLines(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// infoLines renders the model-info panel content (the panel is
// content-sized; its title lives in the border): repo name (accent
// bold), params·from line, separator, downloads and likes, license and
// task, and the non-commercial warning.
func (s *BrowserMode) infoLines(inner int) []string {
	var lines []string
	muted := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Muted).Render(t) }
	subtle := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(t) }
	accent := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Accent).Bold(true).Render(t) }
	good := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.StatusReady).Render(t) }
	warn := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.StatusStart).Render(t) }
	if s.selected == nil {
		lines = append(lines, muted("select a repo…"))
		return lines
	}
	m := s.selected
	lines = append(lines, accent(m.ID))
	if p, ok := paramCountOf(*m); ok {
		line := good(humanB(p) + " params")
		if bm := baseModelOf(*m); bm != "" {
			line += " · " + muted("from ") + subtle(bm)
		}
		lines = append(lines, line)
	} else if bm := baseModelOf(*m); bm != "" {
		lines = append(lines, muted("from ")+subtle(bm))
	}
	lines = append(lines, "")
	lines = append(lines, good("⬇ "+humanCount(m.Downloads))+" "+muted("downloads"))
	lines = append(lines, accent("♥ "+strconv.FormatInt(m.Likes, 10))+" "+muted("likes"))
	lines = append(lines, muted("⚖ license: ")+subtle(licenseOf(*m)))
	if m.PipelineTag != "" {
		lines = append(lines, muted("▷ task: ")+subtle(m.PipelineTag))
	}
	if w := nonCommercialLicense(*m); w != "" {
		lines = append(lines, warn("⚠ "+w))
	}
	if s.mmproj {
		lines = append(lines, subtle("mmproj present — llama.cpp auto-downloads it"))
	}
	return lines
}

// humanB renders a param count: 8 → "8B", 2.6 → "2.6B".
func humanB(n float64) string {
	return strconv.FormatFloat(n, 'f', -1, 64) + "B"
}

// quantsLines renders the quants panel content — a fixed 5-row window
// (standard list behavior: the cursor stays visible, the window
// follows it). The panel is shorter by design (owner round: 5 quants
// visible) so the model card panel gets the room; the "quants (N)"
// title carries the total count, and scrolling is visible as the
// window moves.
func (s *BrowserMode) quantsLines(inner, maxLines int) []string {
	var lines []string
	muted := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Muted).Render(t) }
	subtle := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(t) }

	if s.selected == nil {
		lines = append(lines, muted("select a repo…"))
		return lines
	}
	switch {
	case !s.quantsLoaded:
		lines = append(lines, subtle("loading quants…"))
	case len(s.quants) > 0:
		visible := min(5, max(1, maxLines))
		// Keep the cursor inside the window (standard list behavior).
		if s.quantIdx < s.quantOffset {
			s.quantOffset = s.quantIdx
		}
		if s.quantIdx >= s.quantOffset+visible {
			s.quantOffset = s.quantIdx - visible + 1
		}
		end := min(len(s.quants), s.quantOffset+visible)
		for i := s.quantOffset; i < end; i++ {
			q := s.quants[i]
			row := q.Tag + " — " + muted(hf.HumanSize(q.Size))
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
	default: // loaded, no GGUF quants (or fetch failed): the bare row
		row := "use " + s.selected.ID + " without a quant"
		if s.zone == zoneQuants && s.quantIdx == 0 {
			row = "▶ " + row
		} else {
			row = "  " + row
		}
		lines = append(lines, row)
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
// cardLines renders the model card panel content: the README rendered
// to styled markdown lines (renderCardMarkdown), windowed to the panel
// height, scrollable with pgup/pgdown in the quants zone (quants ↑/↓
// select; pgup/pgdown scroll the card). A scroll indicator — percent
// plus a 10-dot bar — replaces the old ▴/▾ text markers when the card
// overflows (owner round). Loading and absence states are friendly and
// non-blocking.
func (s *BrowserMode) cardPanelLines(inner, maxLines int) []string {
	var lines []string
	muted := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Muted).Render(t) }
	subtle := func(t string) string { return lipgloss.NewStyle().Foreground(s.theme.Subtle).Render(t) }

	switch {
	case s.cardLoading:
		lines = append(lines, subtle("loading card…"))
	case s.cardErr != "":
		lines = append(lines, muted(s.cardErr))
	case len(s.cardLines) == 0:
		lines = append(lines, muted("no model card"))
	default:
		total := len(s.cardLines)
		visible := maxLines - 1 // reserve the scroll-indicator slot
		if visible < 1 {
			visible = 1
		}
		maxOff := max(0, total-visible)
		if s.cardOffset > maxOff {
			s.cardOffset = maxOff
		}
		end := min(total, s.cardOffset+visible)
		for _, ln := range s.cardLines[s.cardOffset:end] {
			lines = append(lines, subtle(ln))
		}
		if total > visible {
			pct := 0
			if maxOff > 0 {
				pct = s.cardOffset * 100 / maxOff
			}
			filled := (pct + 5) / 10
			dots := strings.Repeat("▰", filled) + strings.Repeat("▱", 10-filled)
			lines = append(lines, subtle(fmt.Sprintf("%3d%% %s", pct, dots)))
		}
	}
	return lines
}

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
			shortcut("l/L/k/m", "filter", s.theme),
			shortcut("s", "sort", s.theme),
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
