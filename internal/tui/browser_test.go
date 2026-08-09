package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cmoro-deusto/llamaman/internal/hf"
)

// stubBrowserRunner implements browserRunner (DESIGN §16.7 P9): it
// records calls and lets tests control Search / CheckHF results.
type stubBrowserRunner struct {
	results   []hf.SearchResult
	searchErr error
	opts      []hf.QuantOption
	mmproj    bool
	checkErr  error

	searchCalls []hf.SearchOpts
	checkCalls  []string
}

func (s *stubBrowserRunner) Search(_ context.Context, opts hf.SearchOpts) ([]hf.SearchResult, error) {
	s.searchCalls = append(s.searchCalls, opts)
	return s.results, s.searchErr
}

func (s *stubBrowserRunner) CheckHF(_ context.Context, repo string) ([]hf.QuantOption, bool, error) {
	s.checkCalls = append(s.checkCalls, repo)
	return s.opts, s.mmproj, s.checkErr
}

// browserTestResults is a canned search response exercising the tag
// extraction paths: license, languages, base_model:quantized, and a
// non-commercial license repo.
func browserTestResults() []hf.SearchResult {
	return []hf.SearchResult{
		{
			ID: "org/one", Downloads: 743450, Likes: 17,
			Tags:        []string{"gguf", "en", "ja", "license:llama3.1", "base_model:quantized:meta-llama/Llama-3.1-8B-Instruct"},
			PipelineTag: "text-generation",
		},
		{
			ID: "org/nc", Downloads: 1200, Likes: 3,
			Tags:        []string{"gguf", "de", "license:cc-by-nc-4.0"},
			PipelineTag: "text-generation",
		},
	}
}

// openBrowserRoot drives the root into ViewBrowser and injects the
// stub runner (the real one is replaced after open).
func openBrowserRoot(t *testing.T, runner browserRunner) (*Root, *BrowserMode) {
	t.Helper()
	cfg, _ := storageTestConfig(t)
	r := NewRoot(cfg, "/dev/null", stubSpawner{}, nil, "v0.0.0-test", nil)
	driveRoot(t, r, tea.WindowSizeMsg{Width: 160, Height: 40}, keyMsg("b"))
	if r.view != ViewBrowser || r.browser == nil {
		t.Fatalf("view = %d, browser nil? %v", r.view, r.browser == nil)
	}
	r.browser.SetBrowserRunner(runner)
	return r, r.browser
}

// searchDrive drives the browser through a search with the given query.
func searchDrive(t *testing.T, r *Root, query string) {
	t.Helper()
	driveRoot(t, r, keyRunes(query), tea.KeyMsg{Type: tea.KeyEnter})
}

// TestBrowserOpensFromMain: `b` switches to the browser (search box
// focused, empty results); esc returns to Main.
func TestBrowserOpensFromMain(t *testing.T) {
	r, b := openBrowserRoot(t, &stubBrowserRunner{})
	out := stripANSI(b.View())
	for _, want := range []string{
		"browse — Hugging Face",
		"search:",
		"sort:",
		"search, or press enter to browse",
		"enter search", // the search-zone footer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("browser view missing %q\nout:\n%s", want, out)
		}
	}
	driveRoot(t, r, keyMsg("esc"))
	if r.view != ViewMain {
		t.Fatalf("view = %d, want ViewMain", r.view)
	}
}

// TestBrowserSearchFlow: typing reaches the input (every printable
// key), enter runs the search, results render with metadata, and the
// pane auto-follows the first hit (no enter needed).
func TestBrowserSearchFlow(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "llama 3")

	if b.input.Value() != "llama 3" {
		t.Errorf("input = %q, want %q", b.input.Value(), "llama 3")
	}
	if b.zone != zoneResults {
		t.Errorf("zone = %v, want zoneResults", b.zone)
	}
	if len(stub.searchCalls) != 1 || stub.searchCalls[0].Query != "llama 3" {
		t.Fatalf("search calls = %+v", stub.searchCalls)
	}
	if stub.searchCalls[0].Filter != nil {
		t.Errorf("filter = %v, want none", stub.searchCalls[0].Filter)
	}
	// The pane follows the first hit automatically.
	if len(stub.checkCalls) != 1 || stub.checkCalls[0] != "org/one" {
		t.Errorf("auto-fetch calls = %v, want [org/one]", stub.checkCalls)
	}
	out := stripANSI(b.View())
	for _, want := range []string{
		"org/one",
		"org/nc",
		"downloads",
		"17 likes",
		"license: llama3.1",
		"en ja",
		"results (2)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("results view missing %q\nout:\n%s", want, out)
		}
	}
}

// TestBrowserMetadataPane: navigating the results auto-updates the
// info pane — repo name, from <base_model>, downloads, likes, license,
// task, and the non-commercial warning for cc-by-nc repos.
func TestBrowserMetadataPane(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q") // auto-selects org/one

	out := stripANSI(b.View())
	for _, want := range []string{
		"org/one",
		"8B params · from meta-llama/Llama-3.1-8B-Instruct",
		"⬇ 743.5k downloads",
		"♥ 17 likes",
		"⚖ license: llama3.1",
		"▷ task: text-generation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info pane missing %q\nout:\n%s", want, out)
		}
	}
	if strings.Contains(out, "non-commercial license") {
		t.Error("org/one is llama3.1-licensed; no non-commercial warning expected")
	}

	// ↓ → org/nc: the pane follows; cc-by-nc-4.0 → the warning, and no
	// base_model line.
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyDown})
	if b.selected == nil || b.selected.ID != "org/nc" {
		t.Fatalf("selected = %v, want org/nc", b.selected)
	}
	out2 := stripANSI(b.View())
	for _, want := range []string{"org/nc", "⚠ non-commercial license — check terms"} {
		if !strings.Contains(out2, want) {
			t.Errorf("info pane missing %q\nout:\n%s", want, out2)
		}
	}
	if strings.Contains(out2, "from ") {
		t.Error("org/nc has no base_model tag; no 'from' line expected")
	}
}

// TestBrowserQuantPane: selecting a repo (auto-fetch) shows quants
// with real sizes, the ● cached badge from the cache reader, and the
// mmproj note.
func TestBrowserQuantPane(t *testing.T) {
	stub := &stubBrowserRunner{
		results: []hf.SearchResult{{ID: "org/cachedrepo", Tags: []string{"gguf", "en"}}},
		opts:    []hf.QuantOption{{Tag: "Q4_K_M", Size: 5 << 30}, {Tag: "Q8_0", Size: 10 << 30}},
		mmproj:  true,
	}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q") // auto-selects org/cachedrepo

	if !b.quantsLoaded || len(b.quants) != 2 {
		t.Fatalf("quantsLoaded = %v quants = %v", b.quantsLoaded, b.quants)
	}
	if len(stub.checkCalls) != 1 || stub.checkCalls[0] != "org/cachedrepo" {
		t.Fatalf("check calls = %v", stub.checkCalls)
	}
	out := stripANSI(b.View())
	for _, want := range []string{
		"org/cachedrepo",
		"Q4_K_M — 5 GiB",
		"● cached", // the fancy badge (storageTestConfig fixture)
		"Q8_0 — 10 GiB",
		"mmproj present — llama.cpp auto-downloads it",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("quant pane missing %q\nout:\n%s", want, out)
		}
	}
}

// TestBrowserHandoffToConfig: tab into the quants pane, enter on a
// quant → dialog → "add to config" opens the new-model form
// pre-filled source=hf with org/repo:QUANT (staging, so the §16.6
// check must not fire).
func TestBrowserHandoffToConfig(t *testing.T) {
	stub := &stubBrowserRunner{
		results: []hf.SearchResult{{ID: "org/cachedrepo", Tags: []string{"gguf"}}},
		opts:    []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}},
	}
	r, _ := openBrowserRoot(t, stub)
	searchDrive(t, r, "q")                          // auto-selects org/cachedrepo
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyTab})   // → quants pane
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // Q4_K_M → hand-off dialog
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // "add to config" (first option)

	if r.view != ViewConfig || r.configMod == nil {
		t.Fatalf("view = %d, configMod nil? %v", r.view, r.configMod == nil)
	}
	cm := r.configMod
	if cm.formStaging.hf == nil || *cm.formStaging.hf != "org/cachedrepo:Q4_K_M" {
		t.Errorf("staging hf = %v, want org/cachedrepo:Q4_K_M", *cm.formStaging.hf)
	}
	if cm.formStaging.source == nil || *cm.formStaging.source != sourceHF {
		t.Errorf("staging source = %v, want %q", *cm.formStaging.source, sourceHF)
	}
}

// TestBrowserHandoffToDownload: "download now" lands the download in
// the Storage manager (split repo/quant), not in the browser.
func TestBrowserHandoffToDownload(t *testing.T) {
	eng := &stubEngine{}
	stub := &stubBrowserRunner{
		results: []hf.SearchResult{{ID: "org/cachedrepo", Tags: []string{"gguf"}}},
		opts:    []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}},
	}
	r, _ := openBrowserRoot(t, stub)
	r.SetDownloadEngine(eng)
	searchDrive(t, r, "q")
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyTab})   // → quants pane
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // quant → dialog
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyDown}, // "download now"
		tea.KeyMsg{Type: tea.KeyEnter},
	)

	if r.view != ViewStorage || r.storage == nil {
		t.Fatalf("view = %d, storage nil? %v", r.view, r.storage == nil)
	}
	sm := r.storage
	if len(sm.downloads) != 1 {
		t.Fatalf("downloads = %d, want 1", len(sm.downloads))
	}
	d := sm.downloads[0]
	if d.repo != "org/cachedrepo" || d.quant != "Q4_K_M" {
		t.Errorf("download = %s:%s, want org/cachedrepo:Q4_K_M", d.repo, d.quant)
	}
	if len(eng.calls) != 1 || eng.calls[0] != "Download org/cachedrepo Q4_K_M" {
		t.Errorf("engine calls = %v", eng.calls)
	}
}

// TestBrowserNoQuantRow: a repo with no GGUF quants offers a bare-id
// hand-off row, and the dialog skips the download option (hf.Download
// requires a quant).
func TestBrowserNoQuantRow(t *testing.T) {
	stub := &stubBrowserRunner{results: []hf.SearchResult{{ID: "org/empty", Tags: []string{"gguf"}}}}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q") // auto-selects org/empty → empty quants

	if !b.quantsLoaded || len(b.quants) != 0 {
		t.Fatalf("quantsLoaded = %v quants = %v, want loaded with none", b.quantsLoaded, b.quants)
	}
	if out := stripANSI(b.View()); !strings.Contains(out, "use org/empty without a quant") {
		t.Errorf("bare row missing\nout:\n%s", out)
	}
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyTab})   // → quants pane
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // bare row → dialog
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // "add to config"

	if r.view != ViewConfig || r.configMod == nil || r.configMod.formStaging.hf == nil {
		t.Fatalf("view = %d, staging hf = %v", r.view, r.configMod.formStaging.hf)
	}
	if got := *r.configMod.formStaging.hf; got != "org/empty" {
		t.Errorf("staging hf = %q, want bare org/empty", got)
	}
}

// TestBrowserTagFilters: l/L open the curated overlays; picking a
// value re-runs the search with the combined filter tags and the
// header shows the active filters.
func TestBrowserTagFilters(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "llama") // searchCalls[0]

	// l → language filter → ja (index 7: "", en, es, de, fr, it, pt, ja)
	driveRoot(t, r, keyMsg("l"))
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if b.filterLang != "ja" {
		t.Fatalf("filterLang = %q, want ja", b.filterLang)
	}
	if len(stub.searchCalls) != 2 || strings.Join(stub.searchCalls[1].Filter, ",") != "ja" {
		t.Fatalf("search calls = %+v", stub.searchCalls)
	}

	// L → license filter → llama3.1 (index 3: "", apache-2.0, mit, llama3.1)
	driveRoot(t, r, keyMsg("L"))
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyDown},
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if b.filterLic != "llama3.1" {
		t.Fatalf("filterLic = %q, want llama3.1", b.filterLic)
	}
	got := stub.searchCalls[2].Filter
	if strings.Join(got, ",") != "ja,license:llama3.1" {
		t.Fatalf("combined filter = %v, want [ja license:llama3.1]", got)
	}
	out := stripANSI(b.View())
	if !strings.Contains(out, "filter: ja · llama3.1") {
		t.Errorf("header missing active filters\nout:\n%s", out)
	}

	// Re-open and clear with "all languages" (the pre-selected row).
	driveRoot(t, r, keyMsg("l"))
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter})
	if b.filterLang != "" {
		t.Errorf("filterLang = %q, want cleared", b.filterLang)
	}
	if got := stub.searchCalls[3].Filter; strings.Join(got, ",") != "license:llama3.1" {
		t.Errorf("after clear filter = %v, want [license:llama3.1]", got)
	}
}

// TestBrowserSortCycle: t re-runs the search with the next sort field.
func TestBrowserSortCycle(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q")
	if stub.searchCalls[0].Sort != "" {
		t.Errorf("initial sort = %q, want default", stub.searchCalls[0].Sort)
	}
	driveRoot(t, r, keyMsg("t")) // in zoneResults, t also re-sorts
	if b.sort != "likes" || stub.searchCalls[1].Sort != "likes" {
		t.Errorf("sort = %q calls = %+v, want likes", b.sort, stub.searchCalls)
	}
	if out := stripANSI(b.View()); !strings.Contains(out, "sort: likes") {
		t.Errorf("header missing sort\nout:\n%s", out)
	}
	// The cycle mirrors the HF site's Models ranking: downloads →
	// likes → trending → newest → updated.
	driveRoot(t, r, keyMsg("t"))
	if b.sort != "trendingScore" || stub.searchCalls[2].Sort != "trendingScore" {
		t.Errorf("sort = %q, want trendingScore", b.sort)
	}
	if out := stripANSI(b.View()); !strings.Contains(out, "sort: trending") {
		t.Errorf("header missing friendly sort label\nout:\n%s", out)
	}
	driveRoot(t, r, keyMsg("t"))
	if b.sort != "createdAt" {
		t.Errorf("sort = %q, want createdAt", b.sort)
	}
	driveRoot(t, r, keyMsg("t"))
	if b.sort != "lastModified" {
		t.Errorf("sort = %q, want lastModified", b.sort)
	}
}

// TestBrowserBrowseNoQuery: an empty query browses the top repos by
// the current sort (the HF Models ranking, no search term needed).
func TestBrowserBrowseNoQuery(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	driveRoot(t, r, tea.KeyMsg{Type: tea.KeyEnter}) // empty query = browse
	if len(stub.searchCalls) != 1 {
		t.Fatalf("search calls = %d, want 1", len(stub.searchCalls))
	}
	if stub.searchCalls[0].Query != "" {
		t.Errorf("query = %q, want empty (browse)", stub.searchCalls[0].Query)
	}
	if b.zone != zoneResults || len(b.results.Items()) != 2 {
		t.Errorf("zone = %v items = %d, want results with the page", b.zone, len(b.results.Items()))
	}
}

// TestBrowserTaskFilter: k opens the task picker; picking one re-runs
// the search with the server-side pipeline_tag param.
func TestBrowserTaskFilter(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q")
	driveRoot(t, r, keyMsg("k")) // task filter
	// options: any task(""), text-generation, translation, …
	driveRoot(t, r,
		tea.KeyMsg{Type: tea.KeyDown}, // text-generation
		tea.KeyMsg{Type: tea.KeyEnter},
	)
	if b.filterTask != "text-generation" {
		t.Fatalf("filterTask = %q, want text-generation", b.filterTask)
	}
	if len(stub.searchCalls) != 2 || stub.searchCalls[1].PipelineTag != "text-generation" {
		t.Fatalf("search calls = %+v", stub.searchCalls)
	}
	if out := stripANSI(b.View()); !strings.Contains(out, "filter: text-generation") {
		t.Errorf("filter line missing task\nout:\n%s", out)
	}
}

// TestBrowserParamsFilter: m opens the min/max params form; the
// client-side filter (name-derived) prunes the current page.
func TestBrowserParamsFilter(t *testing.T) {
	stub := &stubBrowserRunner{results: []hf.SearchResult{
		{ID: "org/big", Tags: []string{"gguf", "base_model:org/Llama-70B"}},
		{ID: "org/small", Tags: []string{"gguf", "base_model:org/Mistral-7B"}},
	}}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q")
	if len(b.results.Items()) != 2 {
		t.Fatalf("items = %d, want 2 before filtering", len(b.results.Items()))
	}
	driveRoot(t, r, keyMsg("m")) // params form
	driveRoot(t, r,
		keyRunes("7"), tea.KeyMsg{Type: tea.KeyEnter}, // min
		keyRunes("10"), tea.KeyMsg{Type: tea.KeyEnter}, // max
	)
	if b.paramMin != 7 || b.paramMax != 10 {
		t.Fatalf("paramMin/Max = %v/%v, want 7/10", b.paramMin, b.paramMax)
	}
	if len(b.results.Items()) != 1 {
		t.Fatalf("items = %d, want 1 after 7B-10B filter", len(b.results.Items()))
	}
	item, ok := b.results.SelectedItem().(resultItem)
	if !ok || item.res.ID != "org/small" {
		t.Errorf("first hit = %v, want org/small (70B excluded)", item)
	}
	if out := stripANSI(b.View()); !strings.Contains(out, "filter: 7B-10B") {
		t.Errorf("filter line missing params\nout:\n%s", out)
	}
}

// TestBrowserTabCyclesZones: tab cycles search → results → quants →
// search (the search bar is part of the cycle); shift+tab reverses.

// TestBrowserSearchError: a failed search flashes its distinct message
// and stays in the search zone.
func TestBrowserSearchError(t *testing.T) {
	stub := &stubBrowserRunner{searchErr: &hf.Error{Kind: hf.ErrGated}}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q")
	out := stripANSI(b.View())
	if !strings.Contains(out, "search failed: gated — requires HF_TOKEN") {
		t.Errorf("missing failure message\nout:\n%s", out)
	}
	if b.zone != zoneSearch {
		t.Errorf("zone = %v, want zoneSearch (stay on the search box)", b.zone)
	}
}

// TestBrowserQuantError: a failed quant fetch flashes the distinct
// message and still offers the bare-id hand-off; the zone stays on the
// results (the pane follows the cursor in the background).
func TestBrowserQuantError(t *testing.T) {
	stub := &stubBrowserRunner{
		results:  []hf.SearchResult{{ID: "org/one", Tags: []string{"gguf"}}},
		checkErr: &hf.Error{Kind: hf.ErrNotFound},
	}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q") // auto-selects org/one → fetch fails

	if b.zone != zoneResults {
		t.Errorf("zone = %v, want zoneResults (auto-fetch is background)", b.zone)
	}
	out := stripANSI(b.View())
	if !strings.Contains(out, "org/one: not found on Hugging Face") {
		t.Errorf("missing failure message\nout:\n%s", out)
	}
	if !b.quantsLoaded || len(b.quants) != 0 {
		t.Errorf("quantsLoaded = %v, want loaded-with-none (bare row)", b.quantsLoaded)
	}
	if !strings.Contains(out, "use org/one without a quant") {
		t.Errorf("bare row must survive a fetch failure\nout:\n%s", out)
	}
}

// TestBrowserStaleResults: done msgs whose gen does not match the
// current request are dropped (search and quants).
func TestBrowserStaleResults(t *testing.T) {
	_, b := openBrowserRoot(t, &stubBrowserRunner{})
	b.searchGen = 6 // a newer search owns the screen
	next, _ := b.Update(browserSearchDoneMsg{gen: 5, results: browserTestResults()})
	b = next
	if n := len(b.results.Items()); n != 0 {
		t.Errorf("stale search results applied: %d items", n)
	}

	b.selected = &hf.SearchResult{ID: "org/one"}
	b.quantGen = 6
	next, _ = b.Update(browserQuantsDoneMsg{repo: "org/one", gen: 5, opts: []hf.QuantOption{{Tag: "Q4_K_M"}}})
	b = next
	if len(b.quants) != 0 {
		t.Error("stale quant results applied")
	}
	if b.shield != nil {
		t.Error("a stale done msg must not clear a newer shield")
	}
}

// TestBrowserRunnerNil: a nil runner disables search with a flash
// (P3 — the mode still renders and esc works).
func TestBrowserRunnerNil(t *testing.T) {
	r, _ := openBrowserRoot(t, nil)
	searchDrive(t, r, "q")
	out := stripANSI(r.browser.View())
	if !strings.Contains(out, "search unavailable") {
		t.Errorf("missing search-unavailable flash\nout:\n%s", out)
	}
	driveRoot(t, r, keyMsg("esc"))
	if r.view != ViewMain {
		t.Errorf("esc must still work with a nil runner: view = %d", r.view)
	}
}

// TestBrowserTypingSingleRunes: keystrokes arrive one rune at a time in
// production, so a query containing t/l/L must type, not trigger the
// sort cycle or a tag filter (regression: the sort hotkey used to
// hijack the search box).
func TestBrowserTypingSingleRunes(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	msgs := make([]tea.Msg, 0, len("mistral"))
	for _, ch := range "mistral" {
		msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
	}
	msgs = append(msgs, tea.KeyMsg{Type: tea.KeyEnter})
	driveRoot(t, r, msgs...)

	if b.input.Value() != "mistral" {
		t.Errorf("input = %q, want %q (t/l/L must type)", b.input.Value(), "mistral")
	}
	if b.sort != "" {
		t.Errorf("sort = %q, want unchanged", b.sort)
	}
	if len(stub.searchCalls) != 1 {
		t.Errorf("search calls = %d, want 1 (only the enter)", len(stub.searchCalls))
	}
	if stub.searchCalls[0].Query != "mistral" {
		t.Errorf("query = %q, want mistral", stub.searchCalls[0].Query)
	}
}

// TestBrowserTagFilterEscDismisses: esc closes the filter overlay
// without changing the filter (the "all languages"/"any license" rows
// are the explicit clear).
func TestBrowserTagFilterEscDismisses(t *testing.T) {
	stub := &stubBrowserRunner{results: browserTestResults()}
	r, b := openBrowserRoot(t, stub)
	searchDrive(t, r, "q")
	driveRoot(t, r, keyMsg("l")) // open the language filter
	driveRoot(t, r, keyMsg("esc"))
	if b.filterLang != "" {
		t.Errorf("filterLang = %q, want unchanged", b.filterLang)
	}
	if b.tagFilter != nil {
		t.Error("tag filter overlay must be closed")
	}
	if len(stub.searchCalls) != 1 {
		t.Errorf("search calls = %d, want 1 (esc must not re-search)", len(stub.searchCalls))
	}
}

// TestBrowserTabCyclesZones: tab toggles the results/quants pair;
// from the search box either direction lands on the results.
