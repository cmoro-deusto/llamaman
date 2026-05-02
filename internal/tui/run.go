package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
	"github.com/cmoro-deusto/llamaman/internal/hwinfo"
	"github.com/cmoro-deusto/llamaman/internal/llamaapi"
	"github.com/cmoro-deusto/llamaman/internal/paths"
	"github.com/cmoro-deusto/llamaman/internal/server"
)

// Status is the run-mode state machine from DESIGN.md §7.4.
type Status int

const (
	StatusStarting Status = iota
	StatusReady
	StatusExited
	StatusErrored
)

// readyMarker is the substring llama-server prints when its HTTP server is
// up. Detected by scanning chunks streamed from the log file.
const readyMarker = "server is listening"

type logChunkMsg string
type procDoneMsg struct{ err error }
type tailerClosedMsg struct{}
type uptimeTickMsg time.Time

// RunMode owns (or has adopted) a llama-server child plus the viewport
// tailing its log file. Quit prompt is per DESIGN.md §7.4: q/Ctrl+C opens
// (k)ill / (d)etach / (c)ancel.
type RunMode struct {
	cfg      *config.Config
	model    config.Model
	preset   config.Preset
	argv     []string
	warnings []string

	proc          *server.Process
	tail          *server.Tailer
	sessionMgr    *server.SessionManager
	registry      flags.Registry
	serverVersion string // parsed from `<bin> --version`; "" on failure

	viewport viewport.Model
	buf      strings.Builder

	status   Status
	startErr error

	width, height int
	keys          Keymap
	theme         Theme

	showQuit      bool // quit prompt overlay active
	showHelp      bool // help overlay active
	showInfo      bool // i-info overlay active (model + preset detail)
	restartPrompt bool // r-confirm overlay
	killPrompt    bool // k-confirm overlay
	flash         string

	searchInput   textinput.Model
	searchActive  bool
	searchQuery   string
	searchMatches []int
	searchIdx     int

	// Live /props integration. fetcher is nil when main.go couldn't
	// recover host:port from argv (an internal invariant violation —
	// the feature degrades silently to the preset-only display).
	// liveCtxSize == 0 means "not fetched / unavailable"; any
	// positive value is the n_ctx llama-server reported.
	fetcher     Fetcher
	liveCtxSize int
	fetchCtx    context.Context
	fetchCancel context.CancelFunc

	// Live /metrics + /slots integration drives the run-mode header's
	// llama-server panel. Counters are sampled across two ticks so we
	// can report instantaneous tokens/s; gauges are taken straight
	// from the server. metricsAvailable starts true and flips to
	// false on the first ErrMetricsNotEnabled response so subsequent
	// ticks stop polling /metrics. /slots is independent — it works
	// even when --metrics is off.
	livePollStarted     bool
	metricsAvailable    bool
	prevMetrics         *llamaapi.Metrics
	currentTokensPerSec float64
	currentPromptPerSec float64
	avgTokensPerSec     float64
	avgPromptPerSec     float64
	busyCount           int
	totalSlots          int
	queuedCount         int
	tokensIdle          bool // last tick saw zero predicted-tokens delta
	promptIdle          bool // last tick saw zero prompt-tokens delta

	// Hardware-panel state. Refreshed on every live-poll tick from
	// hwinfo.Snapshot (gopsutil + NVML). Empty until the first tick
	// lands so the panel renders n/a placeholders during the first
	// second after Ready.
	hardware []hwinfo.Device
}

// RunModeOpts bundles the inputs to NewRunMode. Spawn is the responsibility
// of the caller (main.go) so it can race for the session lock and choose
// owner vs adopted mode before the TUI starts.
type RunModeOpts struct {
	Cfg        *config.Config
	Model      config.Model
	Preset     config.Preset
	Argv       []string
	Warnings   []string
	Process    *server.Process
	SessionMgr *server.SessionManager // optional; needed to clear session.json on kill
	Registry   flags.Registry         // optional; used by the header to canonicalize param keys
	Fetcher    Fetcher                // optional; nil disables the live ctx-size /props fetch
}

// NewRunMode wires a RunMode around an already-spawned (or adopted)
// process. It opens the tailer on the existing log file and returns the
// initial Cmd batch (chunk reader, process exit watcher, uptime ticker).
func NewRunMode(opts RunModeOpts) (*RunMode, tea.Cmd, error) {
	if opts.Process == nil {
		return nil, nil, fmt.Errorf("RunMode: Process must be non-nil")
	}
	tail, err := server.NewTailer(opts.Process.LogPath)
	if err != nil {
		return nil, nil, fmt.Errorf("tail: %w", err)
	}
	ti := textinput.New()
	ti.Placeholder = "search…"
	ti.Prompt = "/"
	ti.CharLimit = 256

	// Strip the viewport's default vi-style and double-bound keys: the
	// user wants only arrow keys for line scrolling (no j/k), and we
	// want exclusive control over space/b/u/d so RunMode's own paging
	// handlers are the single source of truth.
	vp := viewport.New(0, 0)
	vp.KeyMap = viewport.KeyMap{
		Up:   key.NewBinding(key.WithKeys("up")),
		Down: key.NewBinding(key.WithKeys("down")),
	}

	r := &RunMode{
		cfg:           opts.Cfg,
		model:         opts.Model,
		preset:        opts.Preset,
		argv:          opts.Argv,
		warnings:      opts.Warnings,
		proc:          opts.Process,
		tail:          tail,
		sessionMgr:    opts.SessionMgr,
		registry:      opts.Registry,
		viewport:      vp,
		status:        StatusStarting,
		keys:          DefaultKeymap(),
		theme:         CurrentTheme(),
		searchInput:   ti,
		serverVersion:    loadServerVersion(opts.Cfg.Globals.Bin),
		fetcher:          opts.Fetcher,
		metricsAvailable: true,
	}
	r.fetchCtx, r.fetchCancel = context.WithCancel(context.Background())
	cmds := []tea.Cmd{
		waitForChunk(tail.Chunks()),
		waitForProc(opts.Process),
		tickUptime(),
	}
	// Reattach: the process is already running, so the readyMarker log
	// line happened before we started tailing and we won't see a
	// StatusStarting → StatusReady transition. Fire the fetch
	// immediately. The owner-mode path waits for the transition (handled
	// in the logChunkMsg case in Update) because at construction the
	// server is still booting and /props would dial a port nothing is
	// listening on yet.
	if r.fetcher != nil && opts.Process != nil && !opts.Process.IsOwner() {
		cmds = append(cmds, fetchPropsCmd(r.fetchCtx, r.fetcher))
		cmds = append(cmds, r.startLivePoll()...)
	}
	return r, tea.Batch(cmds...), nil
}

// startLivePoll fires the first /metrics + /slots fetch and schedules
// the recurring tick. Returns the Cmds the caller should batch.
// Idempotent: livePollStarted guards against double-arming when both
// the reattach and ready-marker paths reach this code.
func (r *RunMode) startLivePoll() []tea.Cmd {
	if r.fetcher == nil || r.livePollStarted {
		return nil
	}
	r.livePollStarted = true
	out := []tea.Cmd{
		fetchSlotsCmd(r.fetchCtx, r.fetcher),
		hwSnapshotCmd(),
		tickLivePoll(),
	}
	if r.metricsAvailable {
		out = append(out, fetchMetricsCmd(r.fetchCtx, r.fetcher))
	}
	return out
}

// wordmarkMinWidth is the minimum terminal width at which the
// run-mode header shows the llamaman wordmark on the left side. Below
// this threshold the wordmark would be truncated mid-letter, so we
// fall back to the compact info-only layout instead. The smblock
// wordmark is 31 cols wide so this threshold is a state-machine
// breakpoint (see DESIGN.md §7.4) — Phase 2 layers in the live-band
// breakpoint at 110 on top of this.
const wordmarkMinWidth = 90

// SetSize configures viewport dimensions. Chrome above the viewport
// is the bordered top box (8 or 6 rows depending on whether the
// wordmark is shown); chrome below is the bordered log frame (2
// rows: top + bottom border) plus the 1-row footer. The viewport
// itself fills the inner area of the log box, with horizontal
// padding mirrored from the top box so the two boxes align visually.
func (r *RunMode) SetSize(w, h int) {
	r.width, r.height = w, h
	chrome := r.chromeHeight()
	if h > chrome {
		r.viewport.Height = h - chrome
	} else {
		r.viewport.Height = 1
	}
	// Inner viewport width = box width (r.width - 2 for outer border)
	// minus 2 for left+right horizontal padding inside the border.
	innerWidth := r.width - 4
	if innerWidth < 1 {
		innerWidth = 1
	}
	r.viewport.Width = innerWidth
}

// chromeHeight returns the total rows reserved above and below the
// viewport content: top strip (8 with wordmark / 6 without) + live
// band (liveBandHeight rows when the wide-mode breakpoint is hit, 0
// otherwise) + log box's 2 borders + 1 footer.
func (r *RunMode) chromeHeight() int {
	const logBoxBorders = 2
	const footerHeight = 1
	top := headerHeight
	if r.width >= wordmarkMinWidth {
		top = headerHeightWithWordmark
	}
	band := 0
	if r.width >= liveBandMinWidth {
		band = liveBandHeight
	}
	return top + band + logBoxBorders + footerHeight
}

// Update routes messages: log chunks, process exit, uptime tick, and key
// presses including the quit-prompt state machine.
func (r *RunMode) Update(msg tea.Msg) (*RunMode, tea.Cmd) {
	switch m := msg.(type) {
	case logChunkMsg:
		r.buf.WriteString(string(m))
		wasStarting := r.status == StatusStarting
		if wasStarting && strings.Contains(r.buf.String(), readyMarker) {
			r.status = StatusReady
		}
		atBottom := r.viewport.AtBottom()
		r.viewport.SetContent(r.renderViewportContent())
		if atBottom {
			r.viewport.GotoBottom()
		}
		cmds := []tea.Cmd{waitForChunk(r.tail.Chunks())}
		// Owner-mode kickoff: the moment we see the ready marker, /props
		// + /metrics + /slots are reachable. Fire the one-shot props
		// fetch and arm the recurring live poll alongside the next chunk
		// wait. The reattach path already kicked off in NewRunMode.
		if wasStarting && r.status == StatusReady && r.fetcher != nil {
			if r.liveCtxSize == 0 {
				cmds = append(cmds, fetchPropsCmd(r.fetchCtx, r.fetcher))
			}
			cmds = append(cmds, r.startLivePoll()...)
		}
		return r, tea.Batch(cmds...)

	case tailerClosedMsg:
		return r, nil

	case procDoneMsg:
		if r.fetchCancel != nil {
			r.fetchCancel()
		}
		if m.err == nil {
			r.status = StatusExited
		} else {
			r.status = StatusErrored
			r.startErr = m.err
		}
		return r, nil

	case propsFetchedMsg:
		if m.err != nil {
			// fetchPropsCmd already retried once. Permanent failure: log
			// for the user's troubleshooting, no TUI flash. liveCtxSize
			// stays 0 so the header keeps showing the preset value (or
			// n/a). Suppress the warn for context cancellation — that's
			// a kill-during-fetch and not actionable.
			if !errors.Is(m.err, context.Canceled) {
				slog.Warn("/props fetch failed",
					"err", m.err,
					"alias", r.model.Alias,
					"preset", presetNameOrDash(r.preset),
				)
			}
			return r, nil
		}
		if m.nctx <= 0 {
			// Server returned a valid /props but n_ctx wasn't populated.
			// Treat as unavailable; do not log (no actionable signal).
			return r, nil
		}
		r.liveCtxSize = m.nctx
		// Disagreement diagnostic: the preset declared a value, the
		// live server reports a different one. Most common cause is
		// llama-server clamping a too-large request to the model's max.
		params := canonicalParams(r.preset, r.registry)
		if presetVal, ok := params["ctx-size"]; ok {
			if pv, err := strconv.Atoi(paramValueAsString(presetVal)); err == nil && pv != m.nctx {
				slog.Info("ctx-size mismatch",
					"alias", r.model.Alias,
					"preset", presetNameOrDash(r.preset),
					"preset_value", pv,
					"live_value", m.nctx,
				)
			}
		}
		return r, nil

	case uptimeTickMsg:
		return r, tickUptime()

	case livePollTickMsg:
		// Drive the next round of fetches. /metrics is suppressed once
		// we've learned the server was launched without --metrics.
		// hwinfo always runs — it has no concept of "disabled".
		if r.fetcher == nil {
			return r, nil
		}
		cmds := []tea.Cmd{
			fetchSlotsCmd(r.fetchCtx, r.fetcher),
			hwSnapshotCmd(),
			tickLivePoll(),
		}
		if r.metricsAvailable {
			cmds = append(cmds, fetchMetricsCmd(r.fetchCtx, r.fetcher))
		}
		return r, tea.Batch(cmds...)

	case hwSnapshotMsg:
		r.hardware = m.devices
		return r, nil

	case metricsFetchedMsg:
		if m.err != nil {
			if errors.Is(m.err, llamaapi.ErrMetricsNotEnabled) {
				// Server was launched without --metrics. Stop polling
				// /metrics; /slots still works and Busy will keep
				// updating. Logged once at INFO so the user knows why
				// tokens/s is n/a.
				if r.metricsAvailable {
					slog.Info("/metrics endpoint disabled — preset lacks metrics: true",
						"alias", r.model.Alias,
						"preset", presetNameOrDash(r.preset),
					)
				}
				r.metricsAvailable = false
				return r, nil
			}
			if !errors.Is(m.err, context.Canceled) {
				slog.Warn("/metrics fetch failed",
					"err", m.err,
					"alias", r.model.Alias,
					"preset", presetNameOrDash(r.preset),
				)
			}
			return r, nil
		}
		r.applyMetrics(m.m)
		return r, nil

	case slotsFetchedMsg:
		if m.err != nil {
			if !errors.Is(m.err, context.Canceled) {
				slog.Warn("/slots fetch failed",
					"err", m.err,
					"alias", r.model.Alias,
					"preset", presetNameOrDash(r.preset),
				)
			}
			return r, nil
		}
		r.busyCount = m.s.BusyCount
		r.totalSlots = m.s.Total
		return r, nil

	case tea.KeyMsg:
		if r.showQuit {
			return r.handleQuitPrompt(m)
		}
		if r.restartPrompt {
			return r.handleRestartPrompt(m)
		}
		if r.killPrompt {
			return r.handleKillPrompt(m)
		}
		if r.searchActive {
			return r.handleSearchInput(m)
		}
		if r.showHelp {
			r.showHelp = false
			return r, nil
		}
		if r.showInfo {
			r.showInfo = false
			return r, nil
		}
		switch m.String() {
		case "esc":
			// Layered Esc: on the main run screen, clear any applied
			// query so highlights and n/N navigation drop back to a
			// clean log. No-op when nothing is applied.
			if r.searchQuery != "" {
				r.searchQuery = ""
				r.searchMatches = nil
				r.searchIdx = 0
				r.refreshContent()
			}
			return r, nil
		case "q", "ctrl+c":
			r.showQuit = true
			return r, nil
		case "k":
			// Direct kill — gated on a confirm dialog so an accidental
			// keypress doesn't murder a running session.
			r.killPrompt = true
			return r, nil
		case "?":
			r.showHelp = true
			return r, nil
		case "i":
			r.showInfo = true
			return r, nil
		case "/":
			r.searchActive = true
			r.searchInput.SetValue("")
			r.refreshContent()
			return r, r.searchInput.Focus()
		case "n":
			r.jumpSearch(+1)
			return r, nil
		case "N":
			r.jumpSearch(-1)
			return r, nil
		case "g":
			r.viewport.GotoTop()
			return r, nil
		case "G":
			r.viewport.GotoBottom()
			return r, nil
		case " ", "space":
			r.viewport.HalfPageDown()
			return r, nil
		case "b":
			r.viewport.HalfPageUp()
			return r, nil
		case "c":
			r.copyCommand()
			return r, nil
		case "r":
			if r.status == StatusReady {
				r.restartPrompt = true
				return r, nil
			}
			// Not ready: skip the confirm and restart immediately.
			return r, r.requestRestart()
		}
	}
	var cmd tea.Cmd
	r.viewport, cmd = r.viewport.Update(msg)
	return r, cmd
}

// applyMetrics computes instantaneous tokens/s by differencing the
// counter values against the previous tick. The first tick after
// startup has no prev so we publish only the gauges (lifetime
// averages); subsequent ticks update both. tokensIdle/promptIdle
// flags let the renderer show "—" instead of "0.0" when nothing
// happened in the window — easier to read than rolling zeros.
func (r *RunMode) applyMetrics(m *llamaapi.Metrics) {
	r.avgTokensPerSec = m.PredictedTokensSecondsAvg
	r.avgPromptPerSec = m.PromptTokensSecondsAvg
	r.queuedCount = int(m.RequestsDeferred)

	if r.prevMetrics == nil {
		r.prevMetrics = m
		// First-tick: no delta to compute. Mark as idle so the
		// renderer shows "—" rather than a stale zero.
		r.tokensIdle = true
		r.promptIdle = true
		return
	}
	prev := r.prevMetrics
	dTokens := m.TokensPredictedTotal - prev.TokensPredictedTotal
	dTokenSecs := m.TokensPredictedSecondsTotal - prev.TokensPredictedSecondsTotal
	if dTokens > 0 && dTokenSecs > 0 {
		r.currentTokensPerSec = dTokens / dTokenSecs
		r.tokensIdle = false
	} else {
		r.tokensIdle = true
	}
	dPrompt := m.PromptTokensTotal - prev.PromptTokensTotal
	dPromptSecs := m.PromptSecondsTotal - prev.PromptSecondsTotal
	if dPrompt > 0 && dPromptSecs > 0 {
		r.currentPromptPerSec = dPrompt / dPromptSecs
		r.promptIdle = false
	} else {
		r.promptIdle = true
	}
	r.prevMetrics = m
}

// killServer performs the cleanup shared by every kill path: stop the
// child, close the tailer, remove its log, and clear the session
// record. Callers decide whether to return to main mode or quit
// llamaman by chaining the appropriate tea.Cmd.
func (r *RunMode) killServer() {
	if r.fetchCancel != nil {
		r.fetchCancel()
	}
	r.proc.Stop(5 * time.Second)
	r.tail.Close()
	_ = r.proc.RemoveLog()
	if r.sessionMgr != nil {
		_ = r.sessionMgr.Clear()
	}
}

// killAndReturn kills the server and goes back to the main screen
// without exiting llamaman. Used by the direct `k` shortcut.
func (r *RunMode) killAndReturn() tea.Cmd {
	r.killServer()
	return func() tea.Msg { return returnToMainMsg{} }
}

// killAndQuit kills the server and quits llamaman. Used by the (k)ill
// option inside the quit prompt — that prompt was triggered by `q`
// (quit llamaman) so its kill option must actually exit, not bounce
// back to main mode the way the standalone `k` shortcut does.
func (r *RunMode) killAndQuit() tea.Cmd {
	r.killServer()
	return tea.Quit
}

// handleRestartPrompt reads a single confirmation key.
func (r *RunMode) handleRestartPrompt(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "y", "enter":
		r.restartPrompt = false
		return r, r.requestRestart()
	case "n", "esc", "c":
		r.restartPrompt = false
		return r, nil
	}
	return r, nil
}

// handleKillPrompt reads the confirm/cancel keys for the direct `k`
// shortcut. Symmetric with handleRestartPrompt.
func (r *RunMode) handleKillPrompt(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "y", "enter", "k":
		r.killPrompt = false
		return r, r.killAndReturn()
	case "n", "esc", "c":
		r.killPrompt = false
		return r, nil
	}
	return r, nil
}

// handleSearchInput routes keystrokes while the search prompt is open.
// Esc layers: with text in the input it just cancels typing; with empty
// input AND an applied query, it also clears the applied query so a
// single Esc from a freshly opened prompt returns the log to a clean
// state. Every text change refreshes the viewport so live highlights
// track typing.
func (r *RunMode) handleSearchInput(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "esc":
		r.searchActive = false
		r.searchInput.Blur()
		if strings.TrimSpace(r.searchInput.Value()) == "" && r.searchQuery != "" {
			r.searchQuery = ""
			r.searchMatches = nil
			r.searchIdx = 0
		}
		r.refreshContent()
		return r, nil
	case "enter":
		r.searchQuery = strings.TrimSpace(r.searchInput.Value())
		r.searchActive = false
		r.searchInput.Blur()
		r.recomputeMatches()
		r.jumpSearch(0)
		r.refreshContent()
		return r, nil
	}
	var cmd tea.Cmd
	r.searchInput, cmd = r.searchInput.Update(m)
	r.refreshContent()
	return r, cmd
}

// requestRestart kills the current process and emits a SpawnRequestMsg
// for the same (model, preset). The root will route it back through the
// spawner to start fresh.
func (r *RunMode) requestRestart() tea.Cmd {
	if r.fetchCancel != nil {
		r.fetchCancel()
	}
	r.proc.Stop(5 * time.Second)
	r.tail.Close()
	_ = r.proc.RemoveLog()
	if r.sessionMgr != nil {
		_ = r.sessionMgr.Clear()
	}
	model, preset := r.model, r.preset
	return func() tea.Msg { return SpawnRequestMsg{Model: model, Preset: preset} }
}

// copyCommand pushes the launch argv onto the clipboard via wl-copy then
// xclip; flashes a confirmation status either way.
func (r *RunMode) copyCommand() {
	if len(r.argv) == 0 {
		r.flash = "no command to copy"
		return
	}
	cmdLine := strings.Join(r.argv, " ")
	for _, candidate := range []struct {
		name string
		args []string
	}{
		{"wl-copy", nil},
		{"xclip", []string{"-selection", "clipboard"}},
	} {
		if _, err := exec.LookPath(candidate.name); err != nil {
			continue
		}
		c := exec.Command(candidate.name, candidate.args...)
		c.Stdin = bytes.NewReader([]byte(cmdLine))
		if err := c.Run(); err == nil {
			r.flash = fmt.Sprintf("command copied via %s", candidate.name)
			return
		}
	}
	r.flash = "no clipboard tool found (install wl-copy or xclip)"
}

// effectiveQuery returns the query whose matches should be highlighted
// in the log viewport. The live search-input value wins while the
// prompt is open so highlights track typing in real time; otherwise the
// last-applied query is used so they persist for n/N navigation.
func (r *RunMode) effectiveQuery() string {
	if r.searchActive {
		return strings.TrimSpace(r.searchInput.Value())
	}
	return r.searchQuery
}

// renderViewportContent returns the log buffer with reverse+bold ANSI
// wrapped around every case-insensitive occurrence of the effective
// query. With no query, the buffer is returned verbatim. This is the
// single source of truth for what the viewport displays — every state
// transition that could change either the buffer or the highlight
// effective query funnels through it.
//
// Known limitation: if llama-server's own ANSI styling spans a match,
// our reset at the end of the highlight prematurely terminates the
// outer span. llama-server stderr is overwhelmingly plain in practice
// so we accept this rather than tokenizing the buffer.
func (r *RunMode) renderViewportContent() string {
	return highlightOccurrences(r.buf.String(), r.effectiveQuery())
}

// refreshContent re-renders the viewport, preserving auto-follow when
// the user was already at the bottom.
func (r *RunMode) refreshContent() {
	atBottom := r.viewport.AtBottom()
	r.viewport.SetContent(r.renderViewportContent())
	if atBottom {
		r.viewport.GotoBottom()
	}
}

// highlightOpen / highlightClose are the SGR sequences used to wrap
// matches: bold + reverse video (1;7) opens, full reset (0) closes.
// Emitted as literals rather than via lipgloss so the highlight is
// terminal-independent and deterministic in tests, where lipgloss
// would otherwise suppress codes when no TTY is detected.
const (
	highlightOpen  = "\x1b[1;7m"
	highlightClose = "\x1b[0m"
)

// highlightOccurrences wraps every non-overlapping case-insensitive
// match of q in raw with bold+reverse ANSI. Position-stable: matched
// bytes are taken from raw so original case is preserved. Advances by
// len(q) after each match to avoid overlap on inputs like "aaa"/"aa".
func highlightOccurrences(raw, q string) string {
	if q == "" || raw == "" {
		return raw
	}
	lower := strings.ToLower(raw)
	qLower := strings.ToLower(q)
	if len(qLower) == 0 || len(qLower) > len(lower) {
		return raw
	}
	var b strings.Builder
	b.Grow(len(raw) + 16)
	i := 0
	for {
		rel := strings.Index(lower[i:], qLower)
		if rel < 0 {
			b.WriteString(raw[i:])
			return b.String()
		}
		start := i + rel
		end := start + len(qLower)
		b.WriteString(raw[i:start])
		b.WriteString(highlightOpen)
		b.WriteString(raw[start:end])
		b.WriteString(highlightClose)
		i = end
	}
}

// recomputeMatches finds line indices in buf containing the search query
// (case-insensitive). Used by jumpSearch to navigate.
func (r *RunMode) recomputeMatches() {
	r.searchMatches = nil
	r.searchIdx = 0
	if r.searchQuery == "" {
		return
	}
	q := strings.ToLower(r.searchQuery)
	lines := strings.Split(r.buf.String(), "\n")
	for i, ln := range lines {
		if strings.Contains(strings.ToLower(ln), q) {
			r.searchMatches = append(r.searchMatches, i)
		}
	}
}

// jumpSearch advances the cursor in searchMatches by delta and scrolls
// the viewport to the resulting line. delta=0 means "jump to current
// match" (used right after recomputeMatches).
func (r *RunMode) jumpSearch(delta int) {
	if len(r.searchMatches) == 0 {
		r.flash = fmt.Sprintf("no matches for %q", r.searchQuery)
		return
	}
	r.searchIdx = (r.searchIdx + delta) % len(r.searchMatches)
	if r.searchIdx < 0 {
		r.searchIdx += len(r.searchMatches)
	}
	target := r.searchMatches[r.searchIdx]
	r.viewport.SetYOffset(target)
	r.flash = fmt.Sprintf("match %d/%d", r.searchIdx+1, len(r.searchMatches))
}

// handleQuitPrompt runs the (k)ill / (d)etach / (c)ancel decision tree
// for the q-triggered quit prompt. Both (k)ill and (d)etach exit
// llamaman — they differ on whether llama-server lives on. The direct
// `k` shortcut takes a separate path (back to main, llamaman stays).
func (r *RunMode) handleQuitPrompt(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "k":
		r.showQuit = false
		return r, r.killAndQuit()
	case "d":
		// Leave the process and session.json intact; just unwind the TUI.
		if r.fetchCancel != nil {
			r.fetchCancel()
		}
		r.tail.Close()
		return r, tea.Quit
	case "c", "esc":
		r.showQuit = false
		return r, nil
	}
	return r, nil
}

// View renders the 3-line header, the viewport, a 1-line footer, and any
// active overlay (quit / restart / help / search). Overlays float over
// the background instead of replacing it so the user keeps context
// (header, log) while interacting with a modal.
func (r *RunMode) View() string {
	if r.width == 0 {
		return ""
	}
	header := r.renderHeader()
	footer := r.renderFooter()
	logFrame := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Border).
		Padding(0, 1).
		Width(r.width - 2).
		Render(r.viewport.View())
	bg := lipgloss.JoinVertical(lipgloss.Left, header, logFrame, footer)
	switch {
	case r.showQuit:
		return overlayCenter(bg, r.renderQuitPrompt(), r.width, r.height)
	case r.restartPrompt:
		return overlayCenter(bg, r.renderRestartPrompt(), r.width, r.height)
	case r.killPrompt:
		return overlayCenter(bg, r.renderKillPrompt(), r.width, r.height)
	case r.showHelp:
		return overlayCenter(bg, r.renderHelp(), r.width, r.height)
	case r.showInfo:
		return overlayCenter(bg, r.renderInfoOverlay(), r.width, r.height)
	}
	return bg
}

func (r *RunMode) renderQuitPrompt() string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Accent).
		Padding(1, 3)
	return box.Render("Quit llamaman?\n\n  (k) kill server   (d) detach (leaves it running)   (c) cancel")
}

func (r *RunMode) renderRestartPrompt() string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Accent).
		Padding(1, 3)
	return box.Render("Server is ready. Restart anyway?\n\n  (y) yes   (n) no")
}

func (r *RunMode) renderKillPrompt() string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Accent).
		Padding(1, 3)
	return box.Render(fmt.Sprintf(
		"Kill llama-server (%s/%s)?\n\n  (y) yes   (n) no",
		r.model.Alias, presetNameOrDash(r.preset),
	))
}

// renderInfoOverlay renders the `i`-toggled overlay: model identity
// (alias + Source/HF) followed by the preset name + every preset
// param in source order. The full param list — which the header
// dropped in Phase 0 — is the point of this overlay; iterating
// r.preset.Params directly preserves the user's JSON key order
// (CLAUDE.md: "Param order matters end-to-end").
func (r *RunMode) renderInfoOverlay() string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	accent := lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true)

	lines := []string{accent.Render("Model & preset")}
	lines = append(lines, "")
	lines = append(lines, subtle.Render("Alias  : ")+r.model.Alias)
	switch {
	case r.model.HF != "":
		lines = append(lines, subtle.Render("HF     : ")+r.model.HF)
	case r.model.Location != "":
		lines = append(lines, subtle.Render("Source : ")+r.model.Location)
	}
	lines = append(lines, "")
	lines = append(lines, subtle.Render("Preset : ")+presetNameOrDash(r.preset))
	if len(r.preset.Params) == 0 {
		lines = append(lines, "  "+subtle.Render("(no params)"))
	} else {
		// Right-pad keys to the longest key width so values line up.
		keyWidth := 0
		for _, p := range r.preset.Params {
			if len(p.Key) > keyWidth {
				keyWidth = len(p.Key)
			}
		}
		for _, p := range r.preset.Params {
			pad := strings.Repeat(" ", keyWidth-len(p.Key))
			lines = append(lines, "  "+p.Key+pad+"  "+paramValueAsString(p.Value))
		}
	}
	lines = append(lines, "")
	lines = append(lines, subtle.Render("(any key to close)"))

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Accent).
		Padding(1, 3)
	return box.Render(strings.Join(lines, "\n"))
}

func (r *RunMode) renderHelp() string {
	keys := []string{
		"q / Ctrl+C  open quit prompt (k)ill / (d)etach / (c)ancel",
		"k           kill server and return to main",
		"r           restart server (confirm if ready)",
		"c           copy launch command to clipboard",
		"i           show model & preset details",
		"/           search (live highlights; Enter applies, Esc cancels)",
		"n / N       next / previous match",
		"Esc         clear active search and highlights",
		"g / G       jump to top / bottom",
		"space / b   page down / up",
		"↑ / ↓       scroll one line",
		"?           toggle this help",
	}
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Accent).
		Padding(1, 3)
	return box.Render("Run mode keys\n\n" + strings.Join(keys, "\n"))
}

func (r *RunMode) renderFooter() string {
	if r.searchActive {
		return r.searchInput.View()
	}
	parts := []string{"q: quit  k: kill  r: restart  c: copy  i: info  /: search  ?: help  g/G: top/bottom  space/b: page  ↑/↓: scroll"}
	if !r.proc.IsOwner() {
		parts = append([]string{"[adopted]"}, parts...)
	}
	hint := lipgloss.NewStyle().Foreground(r.theme.Subtle).Render(strings.Join(parts, " "))
	stack := []string{}
	if r.flash != "" {
		stack = append(stack, lipgloss.NewStyle().Foreground(r.theme.StatusStart).Render(r.flash))
	}
	stack = append(stack, hint)
	return lipgloss.JoinVertical(lipgloss.Left, stack...)
}

// shortToLong maps the short-form llama-server flags whose canonical
// long forms appear in the run-mode header. The parsed Registry stores
// one entry per alias and does not link aliases together, so this small
// curated map is the source of truth for short→long translation. Add
// here when the header surfaces a new key that has a common short form.
var shortToLong = map[string]string{
	"c": "ctx-size",
}

// canonicalParams indexes a preset's params by canonical long-form
// flag name. A user who wrote `-c 8192` (the short form) gets the same
// lookup as one who wrote `ctx-size: 8192`. Unknown keys (those the
// Registry has never seen and that aren't in shortToLong) are stored
// under their literal key — the renderer falls back to the verbatim
// key for display, but the value is still surfaced.
func canonicalParams(preset config.Preset, reg flags.Registry) map[string]any {
	out := make(map[string]any, len(preset.Params))
	for _, p := range preset.Params {
		key := p.Key
		if long, ok := shortToLong[key]; ok {
			key = long
		} else if reg != nil {
			if _, ok := reg.Lookup(key); !ok {
				// Unknown to registry; keep the literal key.
			}
		}
		out[key] = p.Value
	}
	return out
}

// headerHeight is the row count of the compact (no-wordmark) run-mode
// top box: 1 top border + 1 empty padding + 2 content rows + 1 empty
// padding + 1 bottom border = 6 rows. Used on terminals narrower than
// wordmarkMinWidth.
const headerHeight = 6

// headerHeightWithWordmark is the row count when the llamaman ASCII
// wordmark is shown on the left side: 1 top border + 1 empty padding
// + 4 wordmark rows + 1 empty padding + 1 bottom border = 8 rows.
const headerHeightWithWordmark = 8

// wordmarkLines is the number of lines in the embedded Wordmark asset
// (31×4 — smblock with letter spacing). Exposed as a constant so
// layout math is explicit; the renderer also vertically centers the 2
// info content rows inside this many rows on the right side of the box.
const wordmarkLines = 4

// liveBandMinWidth is the breakpoint at which the run-mode header
// gains a second row of side-by-side panels (llama-server live data
// + Hardware). Below this threshold the band is hidden so the
// identity strip can keep its full width without truncation noise
// from the live cells.
const liveBandMinWidth = 110

// liveBandHeight is the row count consumed by the live-data band: 1
// top border + liveBandContentRows content rows + 1 bottom border.
// The server panel always uses 2 content rows; the Hardware panel
// uses 2 rows per device. We size the band to fit the typical 1 CPU
// + 1 GPU = 4 content rows, padding the server panel with blank
// trailing rows so both columns align flush-bottom.
const (
	liveBandContentRows = 4
	liveBandHeight      = liveBandContentRows + 2
)

// renderHeader composes the top strip and (when wide enough) the live
// band into a single header block. The state machine is:
//
//	Width        Top strip                  Live band
//	≥110 (1)     wordmark + 3×2 identity    visible
//	90–110 (2)   wordmark + 2×3 identity    hidden
//	<90 (3)      no wordmark, 3×2 identity  hidden
//
// (See DESIGN.md §7.4 for the rationale.) Identity cells are kept in
// the same source order across states so the user's eye doesn't have
// to relearn the layout when resizing.
func (r *RunMode) renderHeader() string {
	top := r.renderTopStrip()
	if r.width < liveBandMinWidth {
		return top
	}
	band := r.renderLiveBand()
	return lipgloss.JoinVertical(lipgloss.Left, top, band)
}

// renderTopStrip renders the bordered top box (identity cells, plus
// wordmark when the terminal is wide enough). One of three layouts is
// produced based on r.width — see the table on renderHeader.
func (r *RunMode) renderTopStrip() string {
	params := canonicalParams(r.preset, r.registry)

	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	accent := lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true)

	cells := []string{
		subtle.Render("Alias:") + " " + accent.Render(r.model.Alias),
		subtle.Render("Server:") + " " + serverVersionOrNA(r.serverVersion),
		subtle.Render("Context Size:") + " " + ctxSizeDisplay(r.liveCtxSize, params),
		subtle.Render("Preset:") + " " + accent.Render(presetNameOrDash(r.preset)),
		subtle.Render("Uptime:") + " " + formatUptime(time.Since(r.proc.Started)),
		statusBadge(r.statusLabel(), r.statusColor()),
	}

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Border).
		Padding(0, 1).
		Width(r.width - 2)

	if r.width < wordmarkMinWidth {
		// State 3: 3 cells × 2 rows, no wordmark.
		row1 := strings.Join(cells[:3], "   ")
		row2 := strings.Join(cells[3:], "   ")
		innerWidth := r.width - 4
		if innerWidth < 1 {
			innerWidth = 1
		}
		body := strings.Join([]string{
			"",
			ansi.Truncate(row1, innerWidth, ""),
			ansi.Truncate(row2, innerWidth, ""),
			"",
		}, "\n")
		return box.Render(body)
	}

	wordmark := lipgloss.NewStyle().
		Foreground(r.theme.Subtle).
		Render(strings.TrimRight(Wordmark, "\n"))
	wordmarkWidth := lipgloss.Width(wordmark)

	// Reserve: borders (2) + padding (2) + 2-col gap between columns
	// + wordmark width = chrome around the right column.
	rightWidth := r.width - 2 - 2 - 2 - wordmarkWidth
	if rightWidth < 10 {
		rightWidth = 10
	}

	var rightCol string
	if r.width < liveBandMinWidth {
		// State 2: 2 cells × 3 rows.
		row1 := ansi.Truncate(strings.Join(cells[0:2], "   "), rightWidth, "")
		row2 := ansi.Truncate(strings.Join(cells[2:4], "   "), rightWidth, "")
		row3 := ansi.Truncate(strings.Join(cells[4:6], "   "), rightWidth, "")
		// 4 rows total to match wordmarkLines: 3 content + 1 bottom blank.
		rightCol = strings.Join([]string{row1, row2, row3, ""}, "\n")
	} else {
		// State 1: 3 cells × 2 rows.
		row1 := ansi.Truncate(strings.Join(cells[:3], "   "), rightWidth, "")
		row2 := ansi.Truncate(strings.Join(cells[3:], "   "), rightWidth, "")
		// 4 rows total: 1 blank top + row1 + row2 + 1 blank bottom.
		rightCol = strings.Join([]string{"", row1, row2, ""}, "\n")
	}

	twoColumn := lipgloss.JoinHorizontal(lipgloss.Top, wordmark, "  ", rightCol)
	body := strings.Join([]string{"", twoColumn, ""}, "\n")
	return box.Render(body)
}

// renderLiveBand renders the side-by-side llama-server + Hardware
// panels that sit below the top strip in State 1. Both panels are
// placeholders in Phase 2 — Phase 3 wires real /metrics + /slots
// data into the server panel and Phase 4 wires NVML/gopsutil into the
// Hardware panel.
func (r *RunMode) renderLiveBand() string {
	leftWidth := r.width / 2
	rightWidth := r.width - leftWidth

	left := r.renderServerPanel(leftWidth)
	right := r.renderHardwarePanel(rightWidth)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, right)
}

// renderServerPanel renders the llama-server live data box. Tokens/s
// and Prompt eval are formatted into fixed-width slots so column
// alignment stays stable as values transition (e.g. 99.9 → 100.0).
// `now` shows "—" while idle (no token delta last tick); avg is the
// lifetime gauge llama-server already maintains.
func (r *RunMode) renderServerPanel(width int) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)

	tokensCell := fmtRatePair(r.currentTokensPerSec, r.avgTokensPerSec, r.tokensIdle, r.metricsAvailable)
	promptCell := fmtRatePair(r.currentPromptPerSec, r.avgPromptPerSec, r.promptIdle, r.metricsAvailable)
	busyCell := fmtBusy(r.busyCount, r.totalSlots)
	queuedCell := fmtQueued(r.queuedCount, r.metricsAvailable)

	row1 := subtle.Render("Tokens/s:") + " " + tokensCell + "     " +
		subtle.Render("Prompt eval:") + " " + promptCell
	row2 := subtle.Render("Busy:") + " " + busyCell + "                 " +
		subtle.Render("Queued:") + " " + queuedCell
	rows := padRows([]string{row1, row2}, liveBandContentRows)
	return r.renderTitledPanel("llama-server", width, rows)
}

// padRows trims or pads `rows` to exactly n entries. Used to keep
// the server panel and Hardware panel at the same vertical size so
// JoinHorizontal aligns their bottom borders.
func padRows(rows []string, n int) []string {
	if len(rows) >= n {
		return rows[:n]
	}
	for len(rows) < n {
		rows = append(rows, "")
	}
	return rows
}

// fmtRatePair renders the "now / avg avg" cell. Slot widths are fixed
// (5.1 → 7 chars) so transitions don't shift columns. When metrics
// are unavailable, both halves show fixed-width "  n/a"; when idle,
// `now` shows "  —" while avg keeps its lifetime value.
func fmtRatePair(now, avg float64, idle, available bool) string {
	const slot = "%7.1f"
	if !available {
		return "    n/a /     n/a avg"
	}
	nowStr := fmt.Sprintf(slot, now)
	if idle {
		nowStr = "      —"
	}
	return nowStr + " / " + fmt.Sprintf(slot, avg) + " avg"
}

// fmtBusy renders "<busy>/<total> slots". Total is small (typically
// 1–8); no padding needed beyond the natural width.
func fmtBusy(busy, total int) string {
	if total == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d slots", busy, total)
}

// fmtQueued renders the queued-requests cell. Falls back to n/a when
// /metrics is disabled (queued counter only ships via /metrics).
func fmtQueued(queued int, available bool) string {
	if !available {
		return "n/a"
	}
	return strconv.Itoa(queued)
}

// renderHardwarePanel renders the Hardware live data box. Two rows
// per device: name + values. Always fills exactly liveBandContentRows
// rows so the panel stays aligned with the server panel — extra
// devices truncate, fewer pad with blanks. Empty hardware (no CPU
// info, no NVIDIA GPU) renders an n/a placeholder block.
func (r *RunMode) renderHardwarePanel(width int) string {
	rows := []string{}
	if len(r.hardware) == 0 {
		subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
		rows = []string{
			"[0] " + subtle.Render("(no devices reported)"),
			"    " + subtle.Render("Util  n/a   RAM  n/a    n/a   n/a   n/a"),
		}
	} else {
		// Stop once we'd exceed the panel's row budget. liveBandContentRows
		// rows per panel; each device emits 2 rows.
		maxDevices := liveBandContentRows / 2
		for i, d := range r.hardware {
			if i >= maxDevices {
				break
			}
			rows = append(rows, hardwareDeviceRows(d)...)
		}
	}
	rows = padRows(rows, liveBandContentRows)
	return r.renderTitledPanel("Hardware", width, rows)
}

// hardwareDeviceRows formats one device into its two-row block. All
// numeric slots are fixed-width so column positions stay stable as
// values transition.
func hardwareDeviceRows(d hwinfo.Device) []string {
	header := fmt.Sprintf("[%d] %s", d.Index, d.Name)
	memLabel := "RAM"
	if d.Class == ClassGPU {
		memLabel = "VRAM"
	}
	utilCell := fmt.Sprintf("Util %3d%%", d.UtilPct)
	memCell := fmt.Sprintf("%-4s %3d%%", memLabel, d.MemPct)
	powerCell := naSlot(5)
	if d.HasPower {
		powerCell = fmt.Sprintf("%4dW", d.PowerW)
	}
	tempCell := naSlot(5)
	if d.HasTemp {
		tempCell = fmt.Sprintf("%3d°C", d.TempC)
	}
	fanCell := naSlot(7)
	if d.HasFan {
		switch d.Class {
		case ClassGPU:
			fanCell = fmt.Sprintf("%6d%%", d.FanPct)
		default:
			fanCell = fmt.Sprintf("%4drpm", d.FanRPM)
		}
	}
	values := fmt.Sprintf("    %s  %s  %s  %s  %s",
		utilCell, memCell, powerCell, tempCell, fanCell)
	return []string{header, values}
}

// ClassGPU is re-exported here so renderHardwarePanel doesn't have
// to qualify it as hwinfo.ClassGPU at every reference. The single
// alias keeps the panel-formatting code skim-friendly.
const ClassGPU = hwinfo.ClassGPU

// naSlot returns "n/a" right-padded to width — used so missing
// fields don't shift columns relative to populated ones.
func naSlot(width int) string {
	const v = "n/a"
	if len(v) >= width {
		return v
	}
	return strings.Repeat(" ", width-len(v)) + v
}

// renderTitledPanel draws a hand-rolled rounded box with the title
// label embedded in the top border (the design summary's
// "╭── llama-server ───╮" shape). Lipgloss's bordered styles render a
// plain border so we hand-build the four sides to keep total panel
// height at exactly 1 (top) + len(rows) + 1 (bottom) — important for
// liveBandHeight to stay tight against its declared value.
func (r *RunMode) renderTitledPanel(title string, width int, contentRows []string) string {
	border := lipgloss.NewStyle().Foreground(r.theme.Border)
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)

	if width < 8 {
		width = 8
	}
	// Top border: ╭── <title> ───╮
	prefix := "── "
	suffix := " "
	titleVisible := title
	maxTitleLen := width - 1 - len(prefix) - len(suffix) - 1
	if maxTitleLen < 1 {
		maxTitleLen = 1
	}
	if len(titleVisible) > maxTitleLen {
		titleVisible = titleVisible[:maxTitleLen]
	}
	usedCols := 1 + len(prefix) + len(titleVisible) + len(suffix) + 1 // ╭ + "── " + title + " " + ╮
	fillerCount := width - usedCols
	if fillerCount < 0 {
		fillerCount = 0
	}
	top := border.Render("╭"+prefix) + subtle.Render(titleVisible) + border.Render(suffix+strings.Repeat("─", fillerCount)+"╮")

	innerWidth := width - 4 // "│ " + content + " │"
	if innerWidth < 1 {
		innerWidth = 1
	}
	rows := make([]string, len(contentRows))
	for i, line := range contentRows {
		truncated := ansi.Truncate(line, innerWidth, "")
		w := lipgloss.Width(truncated)
		if w < innerWidth {
			truncated += strings.Repeat(" ", innerWidth-w)
		}
		rows[i] = border.Render("│ ") + truncated + border.Render(" │")
	}

	bottom := border.Render("╰" + strings.Repeat("─", width-2) + "╯")

	return strings.Join(append(append([]string{top}, rows...), bottom), "\n")
}

// serverVersionOrNA renders the parsed llama-server version, falling
// back to "n/a" when --version produced nothing usable. Matches the
// param-row convention so missing-value cells read consistently.
func serverVersionOrNA(v string) string {
	if strings.TrimSpace(v) == "" {
		return "n/a"
	}
	return v
}

// paramOrNA returns the rendered value of a canonical-keyed param, or
// "n/a" when the key isn't present in the active preset.
func paramOrNA(params map[string]any, key string) string {
	v, ok := params[key]
	if !ok {
		return "n/a"
	}
	return paramValueAsString(v)
}

// ctxSizeDisplay renders the Context Size cell. The live value (from
// /props) wins when present; otherwise we fall back to the preset's
// declared value or "n/a". Live value is rendered as a plain integer
// without a thousands separator so it matches the rest of the
// param-row formatting.
func ctxSizeDisplay(live int, params map[string]any) string {
	if live > 0 {
		return strconv.Itoa(live)
	}
	return paramOrNA(params, "ctx-size")
}

// statusBadge renders the run-mode status indicator that sits at the
// end of row 1. Bracketed, uppercase, bold, with the state's themed
// foreground color and no background fill.
func statusBadge(label string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render("[" + strings.ToUpper(label) + "]")
}

func (r *RunMode) statusColor() lipgloss.Color {
	switch r.status {
	case StatusReady:
		return r.theme.StatusReady
	case StatusErrored:
		return r.theme.StatusErr
	case StatusExited:
		return r.theme.StatusGone
	default:
		return r.theme.StatusStart
	}
}

func (r *RunMode) statusLabel() string {
	switch r.status {
	case StatusReady:
		return "ready"
	case StatusErrored:
		return "error"
	case StatusExited:
		return "exited"
	default:
		return "starting"
	}
}

func presetNameOrDash(p config.Preset) string {
	if p.Name == "" {
		return "—"
	}
	return p.Name
}

// loadServerVersion runs `<bin> --version` and parses its output for
// the line beginning with "version:" (llama.cpp's --version writes a
// few setup lines plus a single `version: <build> (<hash>)` line).
// Combined output is captured because some llama.cpp builds emit the
// version line on stderr while CUDA init noise lands on stdout (or
// vice versa). Returns "" when the binary doesn't exist, exits
// non-zero, or doesn't include the expected line — the renderer shows
// "n/a" in that case rather than blocking startup.
//
// Called once per RunMode lifecycle (not at llamaman startup) so a
// llama-server upgrade between spawns is reflected on the next run.
func loadServerVersion(bin string) string {
	if strings.TrimSpace(bin) == "" {
		return ""
	}
	out, err := exec.Command(bin, "--version").CombinedOutput()
	if err != nil && len(out) == 0 {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "version:") {
			return strings.TrimSpace(strings.TrimPrefix(trimmed, "version:"))
		}
	}
	return ""
}

func formatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func waitForChunk(ch <-chan string) tea.Cmd {
	return func() tea.Msg {
		s, ok := <-ch
		if !ok {
			return tailerClosedMsg{}
		}
		return logChunkMsg(s)
	}
}

func waitForProc(p *server.Process) tea.Cmd {
	return func() tea.Msg {
		<-p.Done()
		return procDoneMsg{err: p.WaitErr()}
	}
}

func tickUptime() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return uptimeTickMsg(t)
	})
}

// LogFilePath is the runtime location for llama-server's combined output
// (DESIGN.md §5.3). Exported so main.go can reuse it when spawning.
func LogFilePath() (string, error) {
	dir, err := paths.RuntimeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "llama-server.log"), nil
}
