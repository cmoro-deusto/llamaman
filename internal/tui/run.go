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
// up. Detected by scanning chunks streamed from the log file. Older builds
// emitted "server is listening on ..."; newer builds use
// "llama_server: listening on http://...". "listening on" covers both.
const readyMarker = "listening on"

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
	showHelp      bool   // help overlay active
	showInfo      bool   // i-info overlay active (model + preset detail)
	copyResult    string // when non-empty, a centered "command copied" modal is shown; any key dismisses
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
	// tokensSeen / promptSeen latch true once we've observed the first
	// non-zero rate. Bug 3: after the first real value, we persist the
	// last-known rate even on subsequent zero-delta ticks (no more "—"
	// majority of the time). Before the first non-zero, we keep showing
	// "—" so the user knows we just don't have data yet.
	tokensSeen bool
	promptSeen bool
	// Token-rate history for the server-panel sparklines. Each ring
	// holds the last 30 seconds of "tokens this tick" / "prompt eval
	// this tick" values. Auto-scaled by renderSparkline.
	tokensHistory *ringBuffer
	promptHistory *ringBuffer

	// Hardware-panel state. Refreshed on every live-poll tick from
	// hwinfo.Snapshot (gopsutil + NVML). Empty until the first tick
	// lands so the panel renders n/a placeholders during the first
	// second after Ready.
	hardware []hwinfo.Device
	// Per-device Util% history (key = "<class><index>" e.g. "cpu0",
	// "gpu0"). Each entry is a 30-second ring buffer fed each tick.
	utilHistory map[string]*ringBuffer
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
		tokensHistory:    newRingBuffer(sparkBufferSamples),
		promptHistory:    newRingBuffer(sparkBufferSamples),
		utilHistory:      map[string]*ringBuffer{},
	}
	r.fetchCtx, r.fetchCancel = context.WithCancel(context.Background())
	cmds := []tea.Cmd{
		waitForChunk(tail.Chunks()),
		waitForProc(opts.Process),
		tickUptime(),
		// Hardware polling kicks off immediately — no dependency on
		// the server being ready (gopsutil + NVML are local reads).
		hwSnapshotCmd(),
		tickHwPoll(),
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
// the reattach and ready-marker paths reach this code. Hardware
// polling runs on its own ticker (started in NewRunMode) and is not
// gated on server readiness.
func (r *RunMode) startLivePoll() []tea.Cmd {
	if r.fetcher == nil || r.livePollStarted {
		return nil
	}
	r.livePollStarted = true
	out := []tea.Cmd{
		fetchSlotsCmd(r.fetchCtx, r.fetcher),
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
// viewport content. The 2-state machine: at width ≥ wordmarkMinWidth
// (wide mode), top strip + live band are both visible; at narrower
// widths, only the compact identity strip.
func (r *RunMode) chromeHeight() int {
	const logBoxBorders = 2
	const footerHeight = 1
	if r.width >= wordmarkMinWidth {
		return headerHeightWithWordmark + liveBandHeight + logBoxBorders + footerHeight
	}
	return headerHeight + logBoxBorders + footerHeight
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
		// Drive the next round of server fetches. /metrics is
		// suppressed once we've learned the server was launched
		// without --metrics. Hardware polling has its own ticker
		// (hwTickMsg) — independent of server readiness.
		if r.fetcher == nil {
			return r, nil
		}
		cmds := []tea.Cmd{
			fetchSlotsCmd(r.fetchCtx, r.fetcher),
			tickLivePoll(),
		}
		if r.metricsAvailable {
			cmds = append(cmds, fetchMetricsCmd(r.fetchCtx, r.fetcher))
		}
		return r, tea.Batch(cmds...)

	case hwTickMsg:
		// Independent hardware-poll cadence: starts at RunMode
		// birth, keeps running until the fetch context is
		// cancelled (kill/detach). No dependency on server state.
		return r, tea.Batch(hwSnapshotCmd(), tickHwPoll())

	case hwSnapshotMsg:
		r.hardware = m.devices
		// Feed the per-device util history so each device's spark
		// row shows trend over the last 30s. Key by class+index so
		// CPU 0 and GPU 0 don't collide; missing devices keep their
		// existing buffer (rolls off naturally).
		for _, d := range m.devices {
			key := deviceKey(d)
			buf, ok := r.utilHistory[key]
			if !ok {
				buf = newRingBuffer(sparkBufferSamples)
				r.utilHistory[key] = buf
			}
			buf.Append(float64(d.UtilPct))
		}
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
		if r.copyResult != "" {
			r.copyResult = ""
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
			// Always confirm — restart kills the running child even
			// when startup is mid-progress or the server has already
			// died, and an accidental `r` shouldn't blow away whatever
			// state the user was inspecting.
			r.restartPrompt = true
			return r, nil
		}
	}
	var cmd tea.Cmd
	r.viewport, cmd = r.viewport.Update(msg)
	return r, cmd
}

// applyMetrics computes instantaneous tokens/s + prompt-eval rates
// by differencing the counter values against the previous tick.
// First-tick (no prev) publishes only the lifetime gauges. After a
// tick yields a non-zero delta, the *Seen flags latch true and the
// rate value persists across subsequent zero-delta ticks (Bug 3) —
// the user's "majority of the time it shows —" complaint stems
// from inference being bursty. We feed every tick (including zero)
// into the sparkline ring buffers so the spark visualizes idle
// periods (low / blue zone) honestly.
func (r *RunMode) applyMetrics(m *llamaapi.Metrics) {
	r.avgTokensPerSec = m.PredictedTokensSecondsAvg
	r.avgPromptPerSec = m.PromptTokensSecondsAvg
	r.queuedCount = int(m.RequestsDeferred)

	if r.prevMetrics == nil {
		r.prevMetrics = m
		// First tick: no delta available; sparklines stay empty
		// until the second tick when we have something to compute.
		return
	}
	prev := r.prevMetrics
	dTokens := m.TokensPredictedTotal - prev.TokensPredictedTotal
	dTokenSecs := m.TokensPredictedSecondsTotal - prev.TokensPredictedSecondsTotal
	tickTokensRate := 0.0
	if dTokens > 0 {
		// Prefer server-side time (tokens-per-predict-second) when
		// available — that's the true generation rate. Fall back to
		// wall-clock when the server hasn't ticked the seconds
		// counter yet (some llama-server builds only increment it
		// at slot completion, so a long single response would
		// otherwise show "—" for its whole duration).
		if dTokenSecs > 0 {
			tickTokensRate = dTokens / dTokenSecs
		} else {
			tickTokensRate = dTokens / livePollInterval.Seconds()
		}
		r.currentTokensPerSec = tickTokensRate
		r.tokensSeen = true
	}
	r.tokensHistory.Append(tickTokensRate)

	dPrompt := m.PromptTokensTotal - prev.PromptTokensTotal
	dPromptSecs := m.PromptSecondsTotal - prev.PromptSecondsTotal
	tickPromptRate := 0.0
	if dPrompt > 0 {
		if dPromptSecs > 0 {
			tickPromptRate = dPrompt / dPromptSecs
		} else {
			tickPromptRate = dPrompt / livePollInterval.Seconds()
		}
		r.currentPromptPerSec = tickPromptRate
		r.promptSeen = true
	}
	r.promptHistory.Append(tickPromptRate)
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
// xclip; surfaces the outcome in a centered modal (`r.copyResult`) so
// the success / failure is unmissable instead of buried in the footer.
func (r *RunMode) copyCommand() {
	if len(r.argv) == 0 {
		r.copyResult = "Nothing to copy — the launch command is empty."
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
			r.copyResult = fmt.Sprintf("Command copied to clipboard via %s.", candidate.name)
			return
		}
	}
	r.copyResult = "No clipboard tool found.\n\nInstall wl-copy (Wayland) or xclip (X11) and try again."
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
	// Pre-truncate viewport content per-line to viewport.Width so
	// lipgloss's bordered box (which auto-wraps lines wider than its
	// .Width()) can't add extra physical rows. Without this, a single
	// log line wider than the inner box wraps inside the box, the
	// rendered logFrame grows by k extra rows, and bubbletea's diff
	// renderer overwrites at the wrong row on the next frame —
	// manifests as the live band "duplicating, moving upward" with
	// every log line.
	logContent := truncateLines(r.viewport.View(), r.viewport.Width)
	logFrame := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Border).
		Padding(0, 1).
		Width(r.width - 2).
		Render(logContent)
	bg := lipgloss.JoinVertical(lipgloss.Left, header, logFrame, footer)
	var out string
	switch {
	case r.showQuit:
		out = overlayCenter(bg, r.renderQuitPrompt(), r.width, r.height)
	case r.restartPrompt:
		out = overlayCenter(bg, r.renderRestartPrompt(), r.width, r.height)
	case r.killPrompt:
		out = overlayCenter(bg, r.renderKillPrompt(), r.width, r.height)
	case r.showHelp:
		out = overlayCenter(bg, r.renderHelp(), r.width, r.height)
	case r.showInfo:
		out = overlayCenter(bg, r.renderInfoOverlay(), r.width, r.height)
	case r.copyResult != "":
		out = overlayCenter(bg, r.renderCopyResult(), r.width, r.height)
	default:
		out = bg
	}
	return clampViewLines(out, r.width)
}

// truncateLines hard-truncates every line of `s` to `width` visual
// columns. ANSI-aware via ansi.Truncate so SGR escape sequences are
// preserved while content is clipped. Used to prevent lipgloss's
// bordered styles from auto-wrapping content longer than their
// declared width — wrapping silently grows the rendered box height,
// which breaks bubbletea's frame diffing.
func truncateLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

// clampViewLines is a defensive truncation pass: every line of the
// rendered View is hard-truncated to r.width visual columns. Without
// this, a single line wider than the terminal triggers terminal-side
// wrap, which adds an extra physical row that bubbletea's diff
// renderer doesn't account for — the next frame then overwrites at
// the wrong row, leaving stale text and shifting the panel "up" with
// every redraw. Pin the line widths here and the framework's frame
// math always matches reality.
func clampViewLines(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = ansi.Truncate(line, width, "")
		}
	}
	return strings.Join(lines, "\n")
}

// promptBox is the shared wrapper for run-mode confirmation modals —
// rounded accent border, comfortable padding. Centralized so quit /
// restart / kill prompts stay visually identical.
func (r *RunMode) promptBox() lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Accent).
		Padding(1, 3)
}

// promptShortcuts joins shortcut tokens (`k kill server`, `y yes`, …)
// with three spaces between them, each rendered via paneShortcut so
// the key gets accent-bold and the label gets subtle — same two-tone
// styling as main mode's `shortcut()` helper and the config footer.
func (r *RunMode) promptShortcuts(tokens ...string) string {
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = paneShortcut(t, r.theme, true)
	}
	return strings.Join(parts, "   ")
}

func (r *RunMode) renderQuitPrompt() string {
	body := "Quit llamaman?\n\n  " + r.promptShortcuts(
		"k kill server",
		"d detach (leaves it running)",
		"c cancel",
	)
	return r.promptBox().Render(body)
}

func (r *RunMode) renderRestartPrompt() string {
	body := "Restart llama-server?\n\n  " + r.promptShortcuts(
		"y yes",
		"n no",
	)
	return r.promptBox().Render(body)
}

// renderCopyResult is the centered modal that surfaces the outcome of
// `c` (copy launch command). Same shape as the error modal in config
// mode: focus-styled [Dismiss] button, "(any key)" hint, accent border
// — except the border colour reflects success vs failure so a glance
// at the modal tells you whether the clipboard write actually worked.
func (r *RunMode) renderCopyResult() string {
	success := strings.HasPrefix(r.copyResult, "Command copied")
	border := r.theme.StatusErr
	titleText := "⚠  Copy failed"
	if success {
		border = r.theme.StatusReady
		titleText = "✓  Copied"
	}
	title := lipgloss.NewStyle().Foreground(border).Bold(true).Render(titleText)
	msg := lipgloss.NewStyle().Foreground(r.theme.Subtle).Render(r.copyResult)
	button := lipgloss.NewStyle().Reverse(true).Padding(0, 2).Render(" Dismiss ")
	hint := lipgloss.NewStyle().Foreground(r.theme.Muted).Render("(any key)")
	body := strings.Join([]string{
		title,
		"",
		msg,
		"",
		button + "  " + hint,
	}, "\n")
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(border).
		Padding(1, 2).
		Render(body)
}

func (r *RunMode) renderKillPrompt() string {
	title := fmt.Sprintf("Kill llama-server (%s/%s)?", r.model.Alias, presetNameOrDash(r.preset))
	body := title + "\n\n  " + r.promptShortcuts(
		"y yes",
		"n no",
	)
	return r.promptBox().Render(body)
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
	type entry struct{ key, desc string }
	rows := []entry{
		{"q / Ctrl+C", "open quit prompt — kill / detach / cancel"},
		{"k", "kill server and return to main"},
		{"r", "restart server (confirms)"},
		{"c", "copy launch command to clipboard"},
		{"i", "show model & preset details"},
		{"/", "search (live highlights; Enter applies, Esc cancels)"},
		{"n / N", "next / previous match"},
		{"Esc", "clear active search and highlights"},
		{"g / G", "jump to top / bottom"},
		{"space / b", "page down / up"},
		{"↑ / ↓", "scroll one line"},
		{"?", "toggle this help"},
	}
	keyW := 0
	for _, e := range rows {
		if w := lipgloss.Width(e.key); w > keyW {
			keyW = w
		}
	}
	keyStyle := lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	lines := make([]string, 0, len(rows))
	for _, e := range rows {
		pad := strings.Repeat(" ", keyW-lipgloss.Width(e.key)+2)
		lines = append(lines, keyStyle.Render(e.key)+pad+descStyle.Render(e.desc))
	}
	return r.promptBox().Render(
		lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true).Render("Run mode keys") +
			"\n\n" + strings.Join(lines, "\n"))
}

func (r *RunMode) renderFooter() string {
	if r.searchActive {
		return r.searchInput.View()
	}
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	sep := subtle.Render(" · ")
	tokens := []string{
		"q quit", "k kill", "r restart", "c copy", "i info",
		"/ search", "? help", "g/G top/bottom", "space/b page", "↑/↓ scroll",
	}
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = paneShortcut(t, r.theme, true)
	}
	hint := strings.Join(parts, sep)
	if !r.proc.IsOwner() {
		hint = subtle.Render("[adopted] ") + hint
	}
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

// liveBandHeight is the row count consumed by the live-data band: 1
// top border + liveBandContentRows content rows + 1 bottom border.
// The Hardware panel uses 5 rows per device (name + util spark + RAM
// bar + Power bar + Temp bar). We size the band to fit the typical
// 1 CPU + 1 GPU = 10 content rows. The server panel pads with blank
// trailing rows so both columns align flush-bottom.
const (
	liveBandContentRows = 10
	liveBandHeight      = liveBandContentRows + 2
)

// Run-mode header layout state machine (DESIGN.md §7.4):
//
//	Width        Top strip                     Live band
//	≥90 (wide)   wordmark + 3×2 identity       visible
//	<90 (compact) identity only (3×2)          hidden
//
// Two states. Below 90 cols, the wordmark is dropped to free
// horizontal room and the live band hides entirely (its content
// would truncate uselessly at half-half-narrow widths). At 90+ the
// band renders at full width — content may visually truncate at the
// right edge between 90 and ~120 cols (band content needs ~110 to
// fit cleanly), and that truncation is honest signal to the user
// that they should widen the terminal. No word wrap; no graceful
// per-column degradation.

// renderHeader composes the top strip and (in wide mode) the live
// band into a single header block. Two states (DESIGN.md §7.4):
// width ≥ wordmarkMinWidth → wordmark + 3×2 identity + live band;
// width < wordmarkMinWidth → identity only, no wordmark, no band.
func (r *RunMode) renderHeader() string {
	top := r.renderTopStrip()
	if r.width < wordmarkMinWidth {
		return top
	}
	band := r.renderLiveBand()
	return lipgloss.JoinVertical(lipgloss.Left, top, band)
}

// renderTopStrip renders the bordered top box. Wide mode shows
// wordmark + 3-cells-per-2-rows identity; compact mode shows just
// the 3×2 identity, no wordmark.
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

	// Right-pad each cell to its column's max width so the second
	// row's cells line up directly under the first row's. Without
	// this, "Preset: default" (col 0) is shorter than "Alias:
	// alpha", which pushes Uptime out from under Server and the
	// status badge out from under Context Size.
	col0w := maxWidth(cells[0], cells[3])
	col1w := maxWidth(cells[1], cells[4])
	cells[0] = padRight(cells[0], col0w)
	cells[3] = padRight(cells[3], col0w)
	cells[1] = padRight(cells[1], col1w)
	cells[4] = padRight(cells[4], col1w)
	// Col 2 doesn't need padding — it's the rightmost cell, nothing
	// downstream needs to align under it.

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Border).
		Padding(0, 1).
		Width(r.width - 2)

	if r.width < wordmarkMinWidth {
		// Compact mode: 3 cells × 2 rows, no wordmark.
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

	// Wide mode: wordmark + 3-cells-per-2-rows identity.
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
	row1 := ansi.Truncate(strings.Join(cells[:3], "   "), rightWidth, "")
	row2 := ansi.Truncate(strings.Join(cells[3:], "   "), rightWidth, "")
	// 4 rows total: 1 blank top + row1 + row2 + 1 blank bottom.
	rightCol := strings.Join([]string{"", row1, row2, ""}, "\n")

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

// metricLabelWidth right-pads every live-band metric label
// ("Util", "RAM", "VRAM", "Power", "Temp", "Tokens", "Prompt") to a
// uniform column so the bar/spark viz starts at the same column on
// every row. 5 fits the longest ("Power", "Tokens", "Prompt").
const metricLabelWidth = 5

// renderServerPanel renders the llama-server live data box (SP3
// shape, DESIGN.md §7.4): two content rows.
//
//	Tokens <spark>   80.0 /  60.0 /s avg   Busy   2/4 slots
//	Prompt <spark> 2331.0 / 2300.0 /s avg  Queued 1
//
// Tokens/Prompt sparklines roll over 40s. Trailing rate cell shows
// "current / avg /s avg" — current is the per-tick instantaneous,
// avg is the lifetime gauge llama-server maintains. Both persist
// last-known after the first non-zero (Bug 3); before any inference,
// the cell reads "—". When --metrics is off, "n/a".
func (r *RunMode) renderServerPanel(width int) string {
	rows := []string{
		r.renderServerRow("Tokens", r.tokensHistory, r.currentTokensPerSec, r.avgTokensPerSec, r.tokensSeen, r.busyCount, r.totalSlots, true),
		r.renderServerRow("Prompt", r.promptHistory, r.currentPromptPerSec, r.avgPromptPerSec, r.promptSeen, r.queuedCount, 0, false),
	}
	return r.renderTitledPanel("llama-server", width, padRows(rows, liveBandContentRows))
}

// renderServerRow builds one row of the server panel: label + spark
// + rate cell ("current / avg /s") + secondary scalar (Busy or
// Queued). When the rate has never been seen, the cell renders as
// "—" (Bug 3 persistence). When metrics are disabled, "n/a".
func (r *RunMode) renderServerRow(label string, hist *ringBuffer, currentRate, avgRate float64, seen bool, sec1, sec2 int, isBusyRow bool) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight(label, metricLabelWidth))

	spark := renderSparkline(r.theme, hist.Snapshot(), MetricUtil)

	// Rate cell: "<current> / <avg> tps". Fixed-width slots so
	// the column stays put as values transition (e.g. 99.9 → 100.0
	// or 999 → 1000). %6.1f covers up to "9999.9" which is more
	// than llama-server ever produces.
	const rateSlot = "%6.1f"
	var rateCell string
	switch {
	case !r.metricsAvailable:
		rateCell = "          n/a"
	case !seen:
		rateCell = "            —"
	default:
		rateCell = fmt.Sprintf(rateSlot+" / "+rateSlot+" tps", currentRate, avgRate)
	}

	var secondary string
	if isBusyRow {
		// Busy: "Busy <n>/<total> slots", or n/a when /slots unread.
		if sec2 > 0 {
			secondary = subtle.Render("Busy ") + fmt.Sprintf("%d/%d slots", sec1, sec2)
		} else {
			secondary = subtle.Render("Busy ") + "n/a"
		}
	} else {
		// Queued: "Queued <n>", or n/a when /metrics disabled.
		if r.metricsAvailable {
			secondary = subtle.Render("Queued ") + strconv.Itoa(sec1)
		} else {
			secondary = subtle.Render("Queued ") + "n/a"
		}
	}
	return labelCell + " " + spark + " " + rateCell + "   " + secondary
}

// padRows trims or pads `rows` to exactly n entries. Keeps the
// server panel and Hardware panel at the same vertical size so
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

// padRight right-pads `s` with spaces to at least `width` visual
// cols. Used for fixed-width label columns. ANSI-aware via
// lipgloss.Width — caller's pre-styled label stays correctly sized.
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

// renderHardwarePanel renders the Hardware live data box (T4 shape,
// DESIGN.md §7.4): 5 rows per device — name row + Util spark + RAM
// bar + Power bar + Temp bar (with optional Fan trailing). Missing
// hardware renders an n/a placeholder block in the same shape.
func (r *RunMode) renderHardwarePanel(width int) string {
	rows := []string{}
	if len(r.hardware) == 0 {
		subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
		rows = []string{"[0] " + subtle.Render("(no devices reported)")}
	} else {
		// 5 rows per device → max devices = liveBandContentRows / 5.
		maxDevices := liveBandContentRows / hardwareRowsPerDevice
		for i, d := range r.hardware {
			if i >= maxDevices {
				break
			}
			rows = append(rows, r.hardwareDeviceRows(d)...)
		}
	}
	return r.renderTitledPanel("Hardware", width, padRows(rows, liveBandContentRows))
}

// hardwareRowsPerDevice is the row budget each Hardware device
// claims: name + util-spark + memory-bar + power-bar + temp-bar.
const hardwareRowsPerDevice = 5

// metricValueWidth pads the current-of-max column (between bar and
// %) to a fixed width. 13 fits the longest natural value:
// `41.0G / 64.0G`. Right-aligned so units + slash position stay
// consistent across rows.
const metricValueWidth = 13

// hardwareDeviceRows formats one device into its 5-row block (T4):
//
//	[N] <name>
//	    Util  <spark>                      XX.X%
//	    RAM   <bar>      41.0G / 64.0G     XX.X%
//	    Power <bar>      32W / 125W        XX.X%
//	    Temp  <bar>      68°C / 100°C      XX.X%   Fan XX%/XXrpm
//
// The values column is fixed-width and right-aligned so the slash
// and unit positions don't drift between rows. Util has no
// current/max value — its slot renders as blank to keep the % column
// aligned with the bar rows below it.
func (r *RunMode) hardwareDeviceRows(d hwinfo.Device) []string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelStyle := func(label string) string {
		return subtle.Render(padRight(label, metricLabelWidth))
	}
	blankValue := strings.Repeat(" ", metricValueWidth)

	// Name row — no leading indent so the [N] marker aligns to the
	// device-name column at the panel's left edge.
	header := fmt.Sprintf("[%d] %s", d.Index, d.Name)

	// Util spark.
	utilSamples := []float64{}
	if buf, ok := r.utilHistory[deviceKey(d)]; ok {
		utilSamples = buf.Snapshot()
	}
	utilSpark := renderSparkline(r.theme, utilSamples, MetricUtil)
	utilRow := joinMetricCells(labelStyle("Util"), utilSpark, blankValue, fmtTrailingPct(float64(d.UtilPct)))

	// Memory bar + bytes value.
	memLabel := "RAM"
	if d.Class == hwinfo.ClassGPU {
		memLabel = "VRAM"
	}
	memBar := renderBar(r.theme, float64(d.MemPct), zoneFor(MetricMem, float64(d.MemPct)))
	memValue := blankValue
	if d.MemTotalBytes > 0 {
		memValue = padLeft(
			fmt.Sprintf("%s / %s", formatBytes(d.MemUsedBytes), formatBytes(d.MemTotalBytes)),
			metricValueWidth)
	}
	memRow := joinMetricCells(labelStyle(memLabel), memBar, memValue, fmtTrailingPct(float64(d.MemPct)))

	// Power bar + W value.
	var powerRow string
	switch {
	case d.HasPower && d.PowerMaxW > 0:
		powerPct := float64(d.PowerW) * 100.0 / float64(d.PowerMaxW)
		powerBar := renderBar(r.theme, powerPct, zoneFor(MetricPower, powerPct))
		powerValue := padLeft(fmt.Sprintf("%dW / %dW", d.PowerW, d.PowerMaxW), metricValueWidth)
		powerRow = joinMetricCells(labelStyle("Power"), powerBar, powerValue, fmtTrailingPct(powerPct))
	case d.HasPower:
		// Current draw known but no max → omit bar; show just the
		// scalar in the value column for column-alignment.
		powerValue := padLeft(fmt.Sprintf("%dW", d.PowerW), metricValueWidth)
		powerRow = joinMetricCells(labelStyle("Power"), strings.Repeat(" ", liveBarWidth), powerValue, "")
	default:
		// No power reading at all (Bug 6: omit, don't render n/a).
		powerRow = ""
	}

	// Temp bar + °C value.
	var tempRow string
	switch {
	case d.HasTemp && d.TempMaxC > 0:
		tempPct := float64(d.TempC) * 100.0 / float64(d.TempMaxC)
		tempBar := renderBar(r.theme, tempPct, zoneFor(MetricTemp, tempPct))
		tempValue := padLeft(fmt.Sprintf("%d°C / %d°C", d.TempC, d.TempMaxC), metricValueWidth)
		tempRow = joinMetricCells(labelStyle("Temp"), tempBar, tempValue, fmtTrailingPct(tempPct))
	case d.HasTemp:
		tempValue := padLeft(fmt.Sprintf("%d°C", d.TempC), metricValueWidth)
		tempRow = joinMetricCells(labelStyle("Temp"), strings.Repeat(" ", liveBarWidth), tempValue, "")
	default:
		tempRow = ""
	}
	// Fan trails the temp row only when present (Bug 6).
	if d.HasFan && tempRow != "" {
		fanText := ""
		switch d.Class {
		case hwinfo.ClassGPU:
			fanText = fmt.Sprintf("%d%%", d.FanPct)
		default:
			fanText = fmt.Sprintf("%drpm", d.FanRPM)
		}
		tempRow += "   " + subtle.Render("Fan ") + fanText
	}

	return []string{header, utilRow, memRow, powerRow, tempRow}
}

// joinMetricCells builds a metric row from its four cell parts:
// label + viz + value + trailing-%. Cells are joined by fixed
// separators so the column positions are deterministic across rows.
//   "    LABEL VIZ    VALUE  PCT"
// Separator spacing matches the user's "value to the right of bar,
// to the left of %, all aligned" directive (last grilling round).
func joinMetricCells(label, viz, value, pct string) string {
	return "    " + label + " " + viz + "  " + value + "  " + pct
}

// padLeft right-aligns `s` to at least `width` visual cols by
// prepending spaces. ANSI-aware via lipgloss.Width.
func padLeft(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

// maxWidth returns the larger of the two strings' visual widths.
// Used by the top-strip column-alignment math.
func maxWidth(a, b string) int {
	wa := lipgloss.Width(a)
	wb := lipgloss.Width(b)
	if wa > wb {
		return wa
	}
	return wb
}

// fmtTrailingPct formats the right-edge percentage cell that sits
// after every bar/spark. 5 chars wide ("23.4%" / "100.0%" → 5–6).
func fmtTrailingPct(pct float64) string {
	return fmt.Sprintf("%5.1f%%", pct)
}

// formatBytes turns a byte count into a human-readable size with
// one decimal: bytes → KiB → MiB → GiB → TiB. Used for memory
// overlays inside the Hardware panel's RAM/VRAM bars.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	suffix := []string{"K", "M", "G", "T", "P"}[exp]
	return fmt.Sprintf("%.1f%s", float64(b)/float64(div), suffix)
}

// deviceKey returns a stable map key for per-device history (e.g.
// the Util sparkline ring buffer). Uses class + index so CPU 0 and
// GPU 0 don't collide.
func deviceKey(d hwinfo.Device) string {
	prefix := "cpu"
	if d.Class == hwinfo.ClassGPU {
		prefix = "gpu"
	}
	return fmt.Sprintf("%s%d", prefix, d.Index)
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
	// Use visual width (lipgloss.Width) — `len()` is byte length and
	// counts each `─` (3 bytes UTF-8) as 3 instead of 1, leaving the
	// top border a few visual cols short of `width`. JoinHorizontal
	// then pads the gap with spaces, which manifests as visible
	// trailing whitespace inside the panel — and at narrow widths can
	// push content past the terminal edge and trigger wrap.
	prefixVis := lipgloss.Width(prefix)
	suffixVis := lipgloss.Width(suffix)
	titleVisible := title
	maxTitleLen := width - 1 - prefixVis - suffixVis - 1
	if maxTitleLen < 1 {
		maxTitleLen = 1
	}
	titleRunes := []rune(titleVisible)
	if len(titleRunes) > maxTitleLen {
		titleVisible = string(titleRunes[:maxTitleLen])
	}
	titleVis := lipgloss.Width(titleVisible)
	usedCols := 1 + prefixVis + titleVis + suffixVis + 1 // ╭ + "── " + title + " " + ╮
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
