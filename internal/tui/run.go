package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"regexp"
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

type logChunkMsg string
type procDoneMsg struct{ err error }
type tailerClosedMsg struct{}
type uptimeTickMsg time.Time

// readyMarker is the substring llama-server prints when its HTTP server is
// up. Detected by scanning chunks streamed from the log file. Older builds
// emitted "server is listening on ..."; newer builds use
// "llama_server: listening on http://...". "listening on" covers both.
// Used as the primary readiness signal so /props is only fetched once the
// HTTP listener is actually accepting connections — avoids hammering a port
// during long model loads (GGUF into memory, HF download).
const readyMarker = "listening on"

// loadPhaseMinVisible is the minimum time the load-progress block stays
// visible once a phase line is seen (§15.4): the block never flashes by
// even when the server reaches READY sooner.
const loadPhaseMinVisible = 2 * time.Second

// RunMode owns (or has adopted) a llama-server child plus the viewport
// tailing its log file. Quit prompt is per DESIGN.md §7.4: q/Ctrl+C opens
// (k)ill / (d)etach / (c)ancel.
type RunMode struct {
	cfg     *config.Config
	cfgPath string // on-disk config path (from opts) — enables preference quick keys (§15.3)
	model   config.Model
	preset  config.Preset

	// Load-progress state (§15.4): newest parsed phase/progress while
	// starting, plus the minimum-visible deadline (2s from the last
	// phase line) and the partial-line accumulator for raw tailer
	// chunks.
	loadPhase      string
	loadProgress   *float64
	loadPhaseUntil time.Time
	loadPartial    string

	// Animation event timestamps (§15.5): one-shot effects fade from
	// these; smooth bars ease between polls.
	readyAt     time.Time
	errAt       time.Time
	lastJumpAt  time.Time
	ttftAt      time.Time
	genFrac     smoothVal
	procFrac    smoothVal
	routerFlash map[string]time.Time
	argv        []string
	routerFile  string // my-models.ini path for router-mode runs; "" otherwise
	warnings    []string

	// Router-mode live state (only populated when routerFile != ""):
	// the model list from GET /models and the loaded-model ids from
	// GET /health.
	routerModels []llamaapi.ModelInfo
	routerLoaded []string
	// routerStats holds each loaded model's live statistics (slots +
	// metrics deltas, sparkline history, TTFT), keyed by model id.
	routerStats map[string]*modelStats
	// routerMetricsAvailable goes false when the router reports the
	// metrics endpoint disabled (spawned without --metrics), which stops
	// the per-model metrics polling.
	routerMetricsAvailable bool
	// routerFocus is the selected model id — highlighted in the model
	// list and targeted by l/u/s/Enter. showRouterStats toggles between
	// the list and the selected model's full stats panel.
	// routerPanelActive makes ↑/↓ move the selection instead of
	// scrolling the log; routerListStart is the list's scroll window.
	// routerMenu is the Enter action menu (idx = highlighted item).
	routerFocus       string
	showRouterStats   bool
	routerPanelActive bool
	routerListStart   int
	routerMenu        bool
	routerMenuIdx     int
	// denoise hides llama.cpp's per-request router chatter
	// ("proxying request to model ...") from the log view. Default on;
	// toggled with `d` in the log panel (router mode for now; the flag
	// is generic so future log filters can apply in single-model mode).
	denoise bool
	// routerIniOnly hides cache-only models (llama.cpp HF download
	// leftovers, InCache) from the models panel. Toggled with `p`.
	routerIniOnly bool

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

	showQuit      bool   // quit prompt overlay active
	showHelp      bool   // help overlay active
	showInfo      bool   // i-info overlay active (model + preset detail)
	copyResult    string // when non-empty, a centered "command copied" modal is shown; any key dismisses
	restartPrompt bool   // r-confirm overlay
	killPrompt    bool   // k-confirm overlay
	unloadPrompt  bool   // router u-confirm overlay
	flash         string
	flashGen      int // guards flash auto-dismissal (older ticks can't clear newer flashes)

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
	// Context usage from /slots.
	contextUsed       int // tokens currently in context (prompt + generated)
	contextMax        int // total context window size
	contextCacheHit   int // prompt tokens served from cache
	contextPromptToks int // prompt tokens in context (for breakdown bar)
	contextGenToks    int // generated tokens in context (for breakdown bar)
	// Generation progress from /slots next_token.
	genDecoded int // tokens generated so far in current response
	genRemain  int // tokens remaining before generation limit
	// Prompt processing progress from /slots.
	promptToksTotal     int // total prompt tokens for current request
	promptToksProcessed int // prompt tokens processed so far
	// Lifetime request count from /metrics.
	decodeTotal float64
	// TTFT (time to first token) tracking.
	ttftStart          time.Time     // when new request started (zero = not tracking)
	ttft               time.Duration // measured TTFT
	ttftPrevPromptToks int           // previous prompt total to detect new request
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
	Cfg     *config.Config
	CfgPath string // on-disk config path; set by Root so run-mode preference quick keys can persist (§15.3)
	Model   config.Model
	Preset  config.Preset
	// Reattach marks an adopted, already-running server: the run view
	// starts at READY and skips the load-progress window entirely
	// (owner feedback — reattach replayed the loading animation).
	Reattach   bool
	RouterFile string // non-empty for router-mode runs (my-models.ini path)
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
// theme is the resolved palette (DESIGN §15.1); it is fixed for the
// lifetime of the run view — a theme change while a session runs shows
// on the next attach/launch.
func NewRunMode(opts RunModeOpts, theme Theme) (*RunMode, tea.Cmd, error) {
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
		cfg:        opts.Cfg,
		cfgPath:    opts.CfgPath,
		model:      opts.Model,
		preset:     opts.Preset,
		routerFile: opts.RouterFile,
		argv:       opts.Argv, warnings: opts.Warnings,
		proc:                   opts.Process,
		tail:                   tail,
		sessionMgr:             opts.SessionMgr,
		registry:               opts.Registry,
		viewport:               vp,
		status:                 StatusStarting,
		keys:                   DefaultKeymap(),
		theme:                  theme,
		searchInput:            ti,
		serverVersion:          loadServerVersion(opts.Cfg.Globals.Bin),
		fetcher:                opts.Fetcher,
		metricsAvailable:       true,
		routerMetricsAvailable: true,
		denoise:                true,
		tokensHistory:          newRingBuffer(sparkBufferSamples),
		promptHistory:          newRingBuffer(sparkBufferSamples),
		utilHistory:            map[string]*ringBuffer{},
	}
	r.fetchCtx, r.fetchCancel = context.WithCancel(context.Background())
	titleState := "STARTING"
	if opts.Reattach {
		// Adopted an already-running server: start at READY and skip the
		// load-progress window (owner feedback — reattach replayed the
		// loading animation). The /props poll still arms the live stats.
		r.status = StatusReady
		r.readyAt = clock()
		titleState = "READY"
	}
	cmds := []tea.Cmd{
		waitForChunk(tail.Chunks()),
		waitForProc(opts.Process),
		tickUptime(),
		// Hardware polling kicks off immediately — no dependency on
		// the server being ready (gopsutil + NVML are local reads).
		hwSnapshotCmd(),
		tickHwPoll(),
		// Terminal title (DESIGN §15.3): set at entry, updated on
		// ready/error/exited transitions.
		tea.SetWindowTitle(r.runTitle(titleState)),
		// Animation frame while an animated element is visible (§15.5).
		r.animCmd(),
	}
	// Readiness is detected by two signals, whichever arrives first:
	//
	//  1. Log marker (primary): "listening on" in the log means the
	//     HTTP server is up. The logChunkMsg handler fires fetchPropsCmd
	//     at that point — no wasted connection attempts during model load.
	//
	//  2. Polling fallback: fetchPropsCmd fires here immediately and
	//     retries every 5s. Catches cases where the log marker is
	//     missed (log truncation, unusual server build). On reattach
	//     the server is already running so this succeeds at once.
	//
	// StatusStarting → StatusReady transitions on first successful
	// /props (handled in propsFetchedMsg), which arms the recurring
	// live poll.
	if r.fetcher != nil {
		cmds = append(cmds, fetchPropsCmd(r.fetchCtx, r.fetcher))
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
	out := []tea.Cmd{tickLivePoll()}
	if r.routerFile != "" {
		// Router mode: /slots and /metrics are not served without a
		// model name; per-model slots feed the router models panel.
		out = append(out, r.routerPollCmds()...)
		return out
	}
	out = append(out, fetchSlotsCmd(r.fetchCtx, r.fetcher))
	if r.metricsAvailable {
		out = append(out, fetchMetricsCmd(r.fetchCtx, r.fetcher))
	}
	return out
}

// routerPollCmds returns the router-mode fetches for one poll round:
// the model list, the loaded ids, and per-model /slots for every model
// actually in memory (the router serves /slots only with a model name,
// and unloaded models have no slots to report).
func (r *RunMode) routerPollCmds() []tea.Cmd {
	cmds := []tea.Cmd{
		fetchModelsCmd(r.fetchCtx, r.fetcher),
		fetchHealthCmd(r.fetchCtx, r.fetcher),
	}
	for _, m := range r.visibleRouterModels() {
		// Only models confirmed loaded get stats polling — unloaded and
		// loading models are skipped (and the endpoints carry
		// autoload=false, so a stray request can never reload a model).
		// Hidden cache-only models are not polled either.
		if m.Status.Value == "loaded" {
			cmds = append(cmds, fetchRouterSlotsCmd(r.fetchCtx, r.fetcher, m.ID))
			if r.routerMetricsAvailable {
				cmds = append(cmds, fetchRouterMetricsCmd(r.fetchCtx, r.fetcher, m.ID))
			}
		}
	}
	return cmds
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
		// Everything lands in the buffer; the denoise filter is applied
		// at render time (visibleLogLines), so toggling it off shows
		// previously hidden lines at their exact positions.
		r.buf.WriteString(string(m))
		r.ingestLoadChunk(string(m))
		atBottom := r.viewport.AtBottom()
		r.viewport.SetContent(r.renderViewportContent())
		if atBottom {
			r.viewport.GotoBottom()
		}
		cmds := []tea.Cmd{waitForChunk(r.tail.Chunks())}
		// Primary readiness signal: log marker "listening on" appears.
		// Fire the /props fetch only after the HTTP server is confirmed
		// up — avoids hammering a port during long model loads.
		// The polling loop in fetchPropsCmd (fired from NewRunMode) is
		// the fallback for cases where the marker is missed.
		wasStarting := r.status == StatusStarting
		if wasStarting && strings.Contains(r.buf.String(), readyMarker) {
			if r.fetcher != nil && r.liveCtxSize == 0 {
				cmds = append(cmds, fetchPropsCmd(r.fetchCtx, r.fetcher))
			}
		}
		return r, tea.Batch(cmds...)

	case tailerClosedMsg:
		return r, nil

	case procDoneMsg:
		if r.fetchCancel != nil {
			r.fetchCancel()
		}
		// Load-progress block is load-window only: clear the minimum-
		// visible deadline so a dead server never shows "loading…".
		r.loadPhaseUntil = time.Time{}
		state := "EXITED"
		if m.err != nil {
			r.status = StatusErrored
			r.startErr = m.err
			r.errAt = clock()
			state = "ERROR"
		} else {
			r.status = StatusExited
		}
		return r, tea.Batch(tea.SetWindowTitle(r.runTitle(state)), r.animCmd())

	case propsFetchedMsg:
		if m.err != nil {
			// fetchPropsCmd polls in a loop until success or ctx cancel.
			// Reaching here means the context was cancelled (kill/detach)
			// or a non-recoverable transport error occurred. Suppress the
			// warn for context cancellation — that's a kill-during-fetch
			// and not actionable. liveCtxSize stays 0 so the header keeps
			// showing the preset value (or n/a).
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
			// Router mode: /props reports n_ctx = 0 by design (the
			// router has no single context — "model_path":"none"). A
			// successful /props round-trip is still the readiness
			// signal: the recurring live poll that feeds the router
			// models panel must start. Single-model runs keep treating
			// an unpopulated n_ctx as unavailable.
			if r.routerFile == "" {
				return r, nil
			}
		} else {
			r.liveCtxSize = m.nctx
		}
		// First successful /props means the server is ready. Transition
		// status and arm the recurring live poll. This replaces the old
		// log-marker approach ("listening on") which was fragile across
		// llama-server versions.
		if r.status == StatusStarting {
			r.status = StatusReady
			r.readyAt = clock()
		}
		cmds := r.startLivePoll()
		if r.status == StatusReady {
			// Terminal title + animation tick for the ready glow (§15.3/§15.5).
			cmds = append(cmds, tea.SetWindowTitle(r.runTitle("READY")))
		}
		cmds = append(cmds, r.animCmd())
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
		if len(cmds) > 0 {
			return r, tea.Batch(cmds...)
		}
		return r, r.animCmd()

	case animTickMsg:
		// Re-render for the animation frame; keep ticking while an
		// animated element is visible, stop in steady state (§15.5).
		return r, r.animCmd()

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
		cmds := []tea.Cmd{tickLivePoll()}
		if r.routerFile != "" {
			// Router mode: /slots and /metrics are not served without a
			// model name; per-model slots keep the panel fresh instead.
			cmds = append(cmds, r.routerPollCmds()...)
		} else {
			cmds = append(cmds, fetchSlotsCmd(r.fetchCtx, r.fetcher))
			if r.metricsAvailable {
				cmds = append(cmds, fetchMetricsCmd(r.fetchCtx, r.fetcher))
			}
		}
		return r, tea.Batch(cmds...)

	case modelsFetchedMsg:
		if m.err == nil && m.m != nil {
			// Record one-shot flash timestamps for models whose status
			// changed (loaded/unloaded, §15.5).
			if r.routerFlash == nil {
				r.routerFlash = map[string]time.Time{}
			}
			prev := map[string]string{}
			for _, old := range r.routerModels {
				prev[old.ID] = old.Status.Value
			}
			for _, mm := range m.m.Data {
				if prev[mm.ID] != "" && prev[mm.ID] != mm.Status.Value {
					r.routerFlash[mm.ID] = clock()
				}
			}
			r.routerModels = m.m.Data
			// Keep the selection and scroll window valid as the list
			// changes (a selected model may disappear).
			r.clampRouterList(-1)
			if r.selectedIdx() < 0 {
				r.routerFocus = ""
			}
		}
		return r, r.animCmd()

	case healthFetchedMsg:
		if m.err == nil && m.h != nil {
			r.routerLoaded = m.h.Models
		}
		return r, nil

	case routerSlotsMsg:
		if m.err == nil && m.s != nil {
			r.applyRouterSlots(m.model, m.s)
		}
		return r, nil

	case routerMetricsMsg:
		if errors.Is(m.err, llamaapi.ErrMetricsNotEnabled) {
			// Router spawned without --metrics (e.g. reattached to a
			// process started by an older llamaman): stop the metrics
			// polling, keep the slots-based stats.
			r.routerMetricsAvailable = false
			return r, nil
		}
		if m.err == nil && m.m != nil {
			r.applyRouterMetrics(m.model, m.m)
		}
		return r, nil

	case modelActionMsg:
		if m.err != nil {
			return r, r.setFlash(fmt.Sprintf("%s failed: %v", m.action, m.err))
		}
		if m.action == "unload" {
			// The selected model is now unloaded — drop it from the stats
			// poll immediately (marking it unloaded here stops the next
			// poll round; autoload=false on the endpoints stops any
			// in-flight one from reloading it), then back to the list.
			for i := range r.routerModels {
				if r.routerModels[i].ID == m.model {
					r.routerModels[i].Status.Value = "unloaded"
				}
			}
			r.showRouterStats = false
			return r, r.setFlash(fmt.Sprintf("unloaded %s", truncateRune(m.model, routerPanelIDMax)))
		}
		return r, r.setFlash(fmt.Sprintf("loading %s", truncateRune(m.model, routerPanelIDMax)))

	case flashExpiredMsg:
		if m.gen == r.flashGen {
			r.flash = ""
		}
		return r, nil

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
		r.contextUsed = m.s.ContextUsed
		r.contextMax = m.s.ContextMax
		r.contextCacheHit = m.s.ContextCacheHits
		r.contextPromptToks = m.s.ContextPromptTokens
		r.contextGenToks = m.s.ContextGenTokens
		r.genDecoded = m.s.GenDecoded
		r.genRemain = m.s.GenRemain
		r.promptToksTotal = m.s.PromptTokensTotal
		r.promptToksProcessed = m.s.PromptTokensProcessed
		// TTFT tracking: detect new request when n_prompt_tokens goes from
		// 0 → N (or changes to a new value). Measure until first token appears.
		newRequest := m.s.PromptTokensTotal > 0 && m.s.PromptTokensTotal != r.ttftPrevPromptToks
		if newRequest && r.ttftStart.IsZero() {
			// New request detected. If generation already started, we
			// missed the prompt phase — mark TTFT as <1s.
			if m.s.GenDecoded > 0 {
				r.ttft = -1 // sentinel for <1s
			} else {
				r.ttftStart = time.Now()
			}
		}
		if m.s.GenDecoded > 0 && !r.ttftStart.IsZero() && r.ttft == 0 {
			// First token appeared.
			r.ttft = time.Since(r.ttftStart)
			r.ttftAt = clock() // TTFT arrival glow (§15.5)
		}
		// Reset when request completes (back to idle).
		if m.s.PromptTokensTotal == 0 && m.s.GenDecoded == 0 {
			r.ttftStart = time.Time{}
			r.ttft = 0
			r.ttftPrevPromptToks = 0
		} else {
			r.ttftPrevPromptToks = m.s.PromptTokensTotal
		}
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
		if r.unloadPrompt {
			return r.handleUnloadPrompt(m)
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
		if r.routerMenu {
			return r.handleRouterMenuKey(m)
		}
		switch m.String() {
		case "up", "down":
			// Models panel focused: move the selection. Otherwise fall
			// through to the viewport (log scrolling).
			if r.routerFile != "" && r.routerPanelActive {
				delta := -1
				if m.String() == "down" {
					delta = 1
				}
				r.moveRouterSelection(delta)
				return r, nil
			}
		case "esc":
			// Layered Esc: close the action menu, then the router stats
			// panel (back to the model list), then clear any applied
			// search query, then detach to Main — the server keeps
			// running and llamaman stays open (DESIGN §15.2). The final
			// detach layer only fires while the session is live; a dead
			// server keeps the crash view in control.
			if r.routerFile != "" && r.routerMenu {
				r.routerMenu = false
				return r, nil
			}
			if r.routerFile != "" && r.showRouterStats {
				r.showRouterStats = false
				return r, nil
			}
			if r.searchQuery != "" {
				r.searchQuery = ""
				r.searchMatches = nil
				r.searchIdx = 0
				r.refreshContent()
				return r, nil
			}
			if r.proc != nil && server.IsLive(r.proc.Pid) {
				return r, func() tea.Msg { return returnToMainMsg{} }
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
		case "tab":
			// Router mode: toggle which panel the ↑/↓ arrows control
			// (log vs models). Never changes what the left panel shows
			// — stats stay up when focus returns to the log.
			if r.routerFile != "" {
				r.routerPanelActive = !r.routerPanelActive
			}
			return r, nil
		case "enter":
			// Models panel: open the action menu for the selection.
			// Silent no-op in the log panel.
			if r.routerFile != "" && r.routerPanelActive && r.routerFocus != "" {
				r.routerMenu = true
				r.routerMenuIdx = 0
				for i, a := range r.menuActions() {
					if r.menuItemEnabled(a) {
						r.routerMenuIdx = i
						break
					}
				}
			}
			return r, nil
		case "s":
			// Models panel: show the selected model's stats.
			if r.routerFile != "" && r.routerPanelActive && r.routerFocus != "" {
				r.showRouterStats = true
			}
			return r, nil
		case "p":
			// Models panel: toggle the ini-only filter (hide cache
			// leftovers). Silent no-op in the log panel.
			if r.routerFile != "" && r.routerPanelActive {
				r.routerIniOnly = !r.routerIniOnly
				if r.selectedIdx() < 0 {
					r.routerFocus = ""
				}
				if r.routerIniOnly {
					return r, r.setFlash("presets only — cache models hidden")
				}
				return r, r.setFlash("all models shown")
			}
			return r, nil
		case "d":
			// Log panel (router mode): toggle the proxy-log denoise
			// filter. The flag is generic so future log filters can
			// apply in single-model mode too.
			if r.routerFile != "" && !r.routerPanelActive {
				r.denoise = !r.denoise
				if r.denoise {
					return r, r.setFlash("denoise on — proxy chatter hidden from log")
				}
				return r, r.setFlash("denoise off — proxy chatter shown")
			}
			return r, nil
		case "l", "u":
			// Models panel: load / unload the selected model.
			if r.routerFile == "" || !r.routerPanelActive {
				return r, nil
			}
			if r.routerFocus == "" {
				return r, r.setFlash("select a model first (tab, ↑/↓)")
			}
			if m.String() == "u" {
				if r.selectedState() != "loaded" {
					return r, r.setFlash("model is not loaded")
				}
				r.unloadPrompt = true
				return r, nil
			}
			if r.selectedState() == "loaded" {
				return r, r.setFlash("model already loaded")
			}
			return r, loadModelCmd(r.fetchCtx, r.fetcher, r.routerFocus)
		case "/":
			// Log panel: search. No-op in the models panel.
			if r.routerPanelActive {
				return r, nil
			}
			r.searchActive = true
			r.searchInput.SetValue("")
			r.refreshContent()
			return r, r.searchInput.Focus()
		case "n":
			if r.routerPanelActive {
				return r, nil
			}
			return r, r.jumpSearch(+1)
		case "N":
			if r.routerPanelActive {
				return r, nil
			}
			return r, r.jumpSearch(-1)
		case "g":
			if r.routerPanelActive {
				return r, nil
			}
			r.viewport.GotoTop()
			return r, nil
		case "G":
			if r.routerPanelActive {
				return r, nil
			}
			r.viewport.GotoBottom()
			return r, nil
		case " ", "space":
			if r.routerPanelActive {
				return r, nil
			}
			r.viewport.HalfPageDown()
			return r, nil
		case "b":
			if r.routerPanelActive {
				return r, nil
			}
			r.viewport.HalfPageUp()
			return r, nil
		case "c":
			r.copyCommand()
			return r, nil
		case "o":
			return r.toggleLogColors()
		case "a":
			return r.toggleAnimations()
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
	r.decodeTotal = m.NDecodeTotal

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

// IsRouter reports whether this run mode serves a router session
// (my-models.ini file) rather than a single model. Used by Root when a
// session ends to return to the matching main-menu mode.
func (r *RunMode) IsRouter() bool { return r.routerFile != "" }

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

// handleUnloadPrompt reads the confirm/cancel keys for the router
// `u` (unload focused model) action.
func (r *RunMode) handleUnloadPrompt(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "y", "enter", "u":
		r.unloadPrompt = false
		if r.routerFocus == "" {
			return r, nil
		}
		return r, unloadModelCmd(r.fetchCtx, r.fetcher, r.routerFocus)
	case "n", "esc", "c":
		r.unloadPrompt = false
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
		r.refreshContent()
		return r, r.jumpSearch(0)
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
// renderViewportContent returns the log buffer with per-line kind
// coloring (DESIGN §15.3, disabled by the log-colors preference) and
// reverse+bold ANSI for search matches — the current occurrence (the
// line at searchIdx, §15.3) uses bold+reverse tinted with the theme's
// StatusStart color so n/N navigation stands out. Lines stay
// byte-identical on disk — coloring is render-time only — and
// search/jump/scrollback operate on the plain lines.
func (r *RunMode) renderViewportContent() string {
	lines := r.visibleLogLines()
	current := -1
	if r.searchQuery != "" && len(r.searchMatches) > 0 {
		current = r.searchMatches[r.searchIdx]
	}
	curOpen := r.currentOccurrenceOpen()
	colors := true
	if r.cfg != nil {
		colors = r.cfg.Prefs().LogColorsEnabled()
	}
	out := make([]string, len(lines))
	for i, ln := range lines {
		open := highlightOpen
		if i == current {
			open = curOpen
		}
		hl := highlightOccurrencesOpen(ln, r.effectiveQuery(), open)
		if colors {
			out[i] = r.colorizeLine(hl)
		} else {
			out[i] = hl
		}
	}
	return strings.Join(out, "\n")
}

// LineKind classifies a llama-server log line for render-time coloring.
type LineKind int

const (
	LineInfo LineKind = iota
	LineError
	LineWarn
	LineTiming
	LineReady
)

// lineKindRules is the ordered keyword classifier table (DESIGN
// §15.3). First match wins; READY is checked first so "listening on"
// lines always get the ready highlight regardless of other content.
// Patterns are conservative: the worst case is an uncolored line, and
// a real critical line is never rendered as plain INFO.
var lineKindRules = []struct {
	kind LineKind
	re   *regexp.Regexp
}{
	{LineReady, regexp.MustCompile(`listening on`)},
	{LineError, regexp.MustCompile(`(?i)\berror\b|\bfailed\b|\bfatal\b|\baborted\b`)},
	{LineWarn, regexp.MustCompile(`(?i)\bwarn(ing)?\b|\bdeprecated\b`)},
	{LineTiming, regexp.MustCompile(`(?i)tokens? per second|ms per token|eval time|prompt eval time|total time|load time`)},
}

// severityPrefixRE matches llama.cpp's default logger line shape:
// "<sec>.<ms>... <LETTER> <message>" (e.g. "0.00.177.074 W DEPRECATED:
// ..."). The letter is the authoritative severity — checked before the
// keyword table (owner feedback: keyword matching alone missed these).
var severityPrefixRE = regexp.MustCompile(`^[0-9.]+ ([IWEDiwed]) `)

func classifyLine(line string) LineKind {
	if m := severityPrefixRE.FindStringSubmatch(line); m != nil {
		switch strings.ToUpper(m[1]) {
		case "W":
			return LineWarn
		case "E":
			return LineError
		}
		return LineInfo // "I" and "D" are informational by default
	}
	for _, r := range lineKindRules {
		if r.re.MatchString(line) {
			return r.kind
		}
	}
	return LineInfo
}

// colorizeLine wraps line in its kind's SGR (DESIGN §15.3). INFO lines
// stay plain; the search highlight applied by the caller is wrapped
// inside the kind color.
func (r *RunMode) colorizeLine(line string) string {
	var color lipgloss.Color
	bold := false
	switch classifyLine(line) {
	case LineError:
		color = r.theme.StatusErr
	case LineWarn:
		color = r.theme.StatusStart
	case LineTiming:
		color = r.theme.Muted
	case LineReady:
		color = r.theme.StatusReady
		bold = true
	default:
		return line
	}
	st := lipgloss.NewStyle().Foreground(color)
	if bold {
		st = st.Bold(true)
	}
	return st.Render(line)
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

// pulseTheme lightens the sparkline colors slightly by phase — the
// live-edge glow (§15.5).
func pulseTheme(t Theme, phase float64) Theme {
	f := 0.12 * phase
	t.StatusIdle = lighten(t.StatusIdle, f)
	t.Accent = lighten(t.Accent, f)
	t.StatusReady = lighten(t.StatusReady, f)
	return t
}

// currentOccurrenceOpen returns the SGR opening for the current search
// occurrence: bold + reverse tinted with the theme's StatusIdle color
// (a colored background). StatusIdle is deliberately NOT one of the
// line-kind colors (Err/Start/Ready/Muted), so the highlight stays
// visible even on a WARN (StatusStart) line (owner feedback). With
// animations on, a one-shot pulse brightens it right after n/N
// (search-jump pulse, §15.5).
func (r *RunMode) currentOccurrenceOpen() string {
	if r.theme.StatusIdle == "" {
		return highlightOpen // no theme (tests/edge) → plain bold+reverse
	}
	color := r.theme.StatusIdle
	if animationsEnabled(r.cfg) {
		if s := oneShotStrength(r.lastJumpAt, jumpPulseDur); s > 0 {
			color = oneShotColor(color, s)
		}
	}
	s := lipgloss.NewStyle().
		Foreground(color).
		Reverse(true).
		Bold(true).
		Render("")
	return strings.TrimSuffix(s, "\x1b[0m")
}

// highlightOccurrences wraps every non-overlapping case-insensitive
// match of q in raw with bold+reverse ANSI. Position-stable: matched
// bytes are taken from raw so original case is preserved. Advances by
// len(q) after each match to avoid overlap on inputs like "aaa"/"aa".
func highlightOccurrences(raw, q string) string {
	return highlightOccurrencesOpen(raw, q, highlightOpen)
}

// highlightOccurrencesOpen is highlightOccurrences with an explicit
// opening SGR, so the current occurrence can use a distinct style.
func highlightOccurrencesOpen(raw, q, open string) string {
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
		b.WriteString(open)
		b.WriteString(raw[start:end])
		b.WriteString(highlightClose)
		i = end
	}
}

// recomputeMatches finds line indices in the VISIBLE log (denoised
// lines excluded) containing the search query (case-insensitive). Used
// by jumpSearch to navigate; the indices are positions in the rendered
// viewport content, so search and view stay in agreement.
func (r *RunMode) recomputeMatches() {
	r.searchMatches = nil
	r.searchIdx = 0
	if r.searchQuery == "" {
		return
	}
	q := strings.ToLower(r.searchQuery)
	for i, ln := range r.visibleLogLines() {
		if strings.Contains(strings.ToLower(ln), q) {
			r.searchMatches = append(r.searchMatches, i)
		}
	}
}

// jumpSearch advances the cursor in searchMatches by delta and scrolls
// the viewport to the resulting line. delta=0 means "jump to current
// match" (used right after recomputeMatches). Returns a cmd to
// auto-dismiss the position flash.
func (r *RunMode) jumpSearch(delta int) tea.Cmd {
	if len(r.searchMatches) == 0 {
		return r.setFlash(fmt.Sprintf("no matches for %q", r.searchQuery))
	}
	r.searchIdx = (r.searchIdx + delta) % len(r.searchMatches)
	if r.searchIdx < 0 {
		r.searchIdx += len(r.searchMatches)
	}
	target := r.searchMatches[r.searchIdx]
	r.lastJumpAt = clock()
	// Re-render first so the current-occurrence highlight moves with the
	// selection, then scroll to the match (owner feedback: the underline
	// never moved because the viewport content was stale).
	r.refreshContent()
	r.viewport.SetYOffset(target)
	return tea.Batch(r.setFlash(fmt.Sprintf("match %d/%d", r.searchIdx+1, len(r.searchMatches))), r.animCmd())
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
	bg := lipgloss.JoinVertical(lipgloss.Left, header, r.renderLogFrame(logContent), footer)
	var out string
	switch {
	case r.showQuit:
		out = overlayCenter(bg, r.renderQuitPrompt(), r.width, r.height)
	case r.restartPrompt:
		out = overlayCenter(bg, r.renderRestartPrompt(), r.width, r.height)
	case r.killPrompt:
		out = overlayCenter(bg, r.renderKillPrompt(), r.width, r.height)
	case r.unloadPrompt:
		out = overlayCenter(bg, r.renderUnloadPrompt(), r.width, r.height)
	case r.routerMenu:
		out = overlayCenter(bg, r.renderRouterMenu(), r.width, r.height)
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

// renderUnloadPrompt asks to unload the focused router model (frees
// its VRAM; autoload will reload it on the next request).
func (r *RunMode) renderUnloadPrompt() string {
	id := r.routerFocus
	if id == "" {
		id = "?"
	}
	title := fmt.Sprintf("Unload %s?", truncateRune(id, routerPanelIDMax))
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

	var lines []string
	if r.routerFile != "" {
		// Router mode: the selection is the model in question.
		id := r.routerFocus
		if id == "" {
			id = "(no selection)"
		}
		state := r.selectedState()
		if state == "" {
			state = "?"
		}
		lines = []string{accent.Render("Router model")}
		lines = append(lines, "")
		lines = append(lines, subtle.Render("Model  : ")+id)
		lines = append(lines, subtle.Render("State  : ")+state)
		src := "ini / models-dir"
		if r.selectedIsCache() {
			src = "download cache"
		}
		lines = append(lines, subtle.Render("Source : ")+src)
		if st, ok := r.routerStats[id]; ok && st != nil {
			lines = append(lines, subtle.Render("Context: ")+humanTokens(st.contextUsed)+"/"+humanTokens(st.contextMax))
		}
		if args := r.selectedArgs(); len(args) > 1 {
			lines = append(lines, "")
			lines = append(lines, accent.Render("Launch params"))
			// Fill the viewport: cap the params only when the popup
			// would grow taller than the screen.
			budget := r.height - 8 // box chrome + margin
			budget -= len(lines)   // fixed rows above (incl. the header)
			budget -= 2            // trailing blank + "(any key to close)"
			if budget < 1 {
				budget = 1
			}
			maxLine := r.width - 12
			if maxLine < 40 {
				maxLine = 40
			}
			for _, l := range formatArgvLines(args, budget, maxLine) {
				lines = append(lines, "  "+l)
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

	lines = []string{accent.Render("Model & preset")}
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
		{"esc", "back to main — detach (server keeps running)"},
		{"o", "toggle log line-kind colors (persists to preferences)"},
		{"a", "toggle animations (persists to preferences)"},
		{"k", "kill server and return to main"},
		{"r", "restart server (confirms)"},
		{"c", "copy launch command to clipboard"},
		{"i", "show model & preset details"},
		{"tab", "router mode: switch panel ↑/↓ target (log ↔ models)"},
		{"", ""},
		{"— log panel —", ""},
		{"↑ / ↓", "scroll"},
		{"/", "search (live highlights; Enter applies, Esc cancels)"},
		{"n / N", "next / previous match"},
		{"g / G", "jump to top / bottom"},
		{"space / b", "page down / up"},
		{"d", "router mode: toggle proxy-log denoise (default on)"},
		{"", ""},
		{"— models panel —", "router mode only"},
		{"↑ / ↓", "move selection"},
		{"Enter", "action menu (Load/Unload/Stats/Info)"},
		{"s", "show selected model's stats (Esc back)"},
		{"l / u", "load / unload the selected model (u confirms)"},
		{"p", "toggle ini-only filter (hide cache leftovers)"},
		{"", ""},
		{"Esc", "layered: close menu → close stats → clear search"},
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
		"q quit", "k kill", "r restart", "c copy", "i info", "o colors", "a anim", "esc back",
	}
	if r.routerFile != "" {
		// The tab hint names the panel you'd switch TO.
		if r.routerPanelActive {
			tokens = append(tokens, "tab log")
		} else {
			tokens = append(tokens, "tab models")
		}
	}
	if r.routerPanelActive {
		// Models panel focused: models-panel keys.
		tokens = append(tokens, "↑/↓ select", "⏎ menu", "s stats", "l/u load/unload")
		if r.routerFile != "" {
			tokens = append(tokens, "p ini-only")
		}
	} else {
		// Log panel focused: log keys.
		tokens = append(tokens, "/ search", "g/G top/bottom", "space/b page", "↑/↓ scroll")
		if r.routerFile != "" {
			tokens = append(tokens, "d denoise")
		}
	}
	tokens = append(tokens, "? help")
	parts := make([]string, len(tokens))
	for i, t := range tokens {
		parts[i] = paneShortcut(t, r.theme, true)
	}
	hint := strings.Join(parts, sep)
	if !r.proc.IsOwner() {
		hint = subtle.Render("[adopted] ") + hint
	}
	stack := []string{}
	// Transient flashes moved into the header's top-right blank space
	// (§15.5 owner feedback) — the footer stays a single static line.
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

	var cells []string
	if r.routerFile != "" {
		cells = []string{
			subtle.Render("File:") + " " + accent.Render(filepath.Base(r.routerFile)),
			subtle.Render("Server:") + " " + serverVersionOrNA(r.serverVersion),
			subtle.Render("Models:") + " " + r.renderRouterModelCount(),
			subtle.Render("Mode:") + " " + accent.Render("router"),
			subtle.Render("Uptime:") + " " + formatUptime(time.Since(r.proc.Started)),
			statusBadge(r.statusLabel(), r.badgeColor()),
		}
	} else {
		cells = []string{
			subtle.Render("Alias:") + " " + accent.Render(r.model.Alias),
			subtle.Render("Server:") + " " + serverVersionOrNA(r.serverVersion),
			subtle.Render("Context Size:") + " " + ctxSizeDisplay(r.liveCtxSize, params) + "  " + subtle.Render(fmt.Sprintf("(%d reqs)", int(r.decodeTotal))),
			subtle.Render("Preset:") + " " + accent.Render(presetNameOrDash(r.preset)),
			subtle.Render("Uptime:") + " " + formatUptime(time.Since(r.proc.Started)),
			statusBadge(r.statusLabel(), r.badgeColor()),
		}
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
		// Flashes live top-right, inside the fixed header box.
		if r.flash != "" {
			flash := lipgloss.NewStyle().Foreground(r.theme.StatusStart).Render(r.flash)
			row1 = rightFlash(row1, flash, innerWidth)
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
	// Transient flashes (match x/y, colors off, …) live top-right in
	// the header's blank space — never shifting the layout when they
	// show or hide (owner feedback).
	if r.flash != "" {
		flash := lipgloss.NewStyle().Foreground(r.theme.StatusStart).Render(r.flash)
		row1 = rightFlash(row1, flash, rightWidth)
	}
	// 4 rows total: 1 blank top + row1 + row2 + 1 blank bottom.
	rightCol := strings.Join([]string{"", row1, row2, ""}, "\n")

	twoColumn := lipgloss.JoinHorizontal(lipgloss.Top, wordmark, "  ", rightCol)
	body := strings.Join([]string{"", twoColumn, ""}, "\n")
	return box.Render(body)
}

// rightFlash aligns a transient flash to the right of an identity row
// within width, using the header's blank space — the header box keeps
// its fixed height, so nothing on screen moves when the flash shows or
// hides (owner feedback). Oversized flashes truncate with an ellipsis.
func rightFlash(row, flash string, width int) string {
	if flash == "" || width <= 0 {
		return row
	}
	rowW := lipgloss.Width(row)
	avail := width - rowW - 1 // reserve at least one gap space
	if avail < 1 {
		return row // no room at all — drop the flash
	}
	fw := lipgloss.Width(flash)
	if fw > avail {
		flash = ansi.Truncate(flash, avail, "…")
		fw = lipgloss.Width(flash)
	}
	gap := width - rowW - fw
	if gap < 1 {
		gap = 1
	}
	return row + strings.Repeat(" ", gap) + flash
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
// ("Util", "RAM", "VRAM", "Power", "Temp", "Tokens", "Prompt", "Process", "Context", "Breakdown", "Gen")
// to a uniform column so the bar/spark viz starts at the same column on
// every row. 9 fits the longest ("Breakdown").
const metricLabelWidth = 9

// statsView is the read-only projection of live llama-server statistics
// the server panel renders. Single-model mode fills it from RunMode's
// scalar fields; router mode fills it from a model's modelStats. The
// renderers only read through this struct so both modes share one panel
// implementation.
type statsView struct {
	metricsAvailable    bool
	tokensHistory       *ringBuffer
	currentTokensPerSec float64
	avgTokensPerSec     float64
	tokensSeen          bool
	promptHistory       *ringBuffer
	currentPromptPerSec float64
	avgPromptPerSec     float64
	promptSeen          bool
	busyCount           int
	totalSlots          int
	queuedCount         int
	decodeTotal         float64
	contextUsed         int
	contextMax          int
	contextCacheHit     int
	contextPromptToks   int
	contextGenToks      int
	genDecoded          int
	genRemain           int
	promptToksTotal     int
	promptToksProcessed int
	ttft                time.Duration
}

// modelStats is one router model's accumulated live statistics: the
// renderable statsView plus the deltas/TTFT bookkeeping that update it.
type modelStats struct {
	statsView
	prevMetrics        *llamaapi.Metrics
	ttftStart          time.Time
	ttftPrevPromptToks int
}

// statsFor returns (creating if needed) the modelStats for a router
// model id.
func (r *RunMode) statsFor(model string) *modelStats {
	if r.routerStats == nil {
		r.routerStats = make(map[string]*modelStats)
	}
	st, ok := r.routerStats[model]
	if !ok {
		st = &modelStats{statsView: statsView{
			tokensHistory: newRingBuffer(sparkBufferSamples),
			promptHistory: newRingBuffer(sparkBufferSamples),
		}}
		r.routerStats[model] = st
	}
	return st
}

// applyRouterSlots folds one /slots?model=<id> snapshot into the
// model's stats — bars, busy/queued counts, and TTFT tracking. Mirrors
// the single-model slotsFetchedMsg handler.
func (r *RunMode) applyRouterSlots(model string, s *llamaapi.Slots) {
	st := r.statsFor(model)
	st.busyCount = s.BusyCount
	st.totalSlots = s.Total
	st.contextUsed = s.ContextUsed
	st.contextMax = s.ContextMax
	st.contextCacheHit = s.ContextCacheHits
	st.contextPromptToks = s.ContextPromptTokens
	st.contextGenToks = s.ContextGenTokens
	st.genDecoded = s.GenDecoded
	st.genRemain = s.GenRemain
	st.promptToksTotal = s.PromptTokensTotal
	st.promptToksProcessed = s.PromptTokensProcessed
	// TTFT tracking: detect new request when n_prompt_tokens goes from
	// 0 → N (or changes). Measure until the first token appears.
	newRequest := s.PromptTokensTotal > 0 && s.PromptTokensTotal != st.ttftPrevPromptToks
	if newRequest && st.ttftStart.IsZero() {
		if s.GenDecoded > 0 {
			st.ttft = -1 // sentinel for <1s (missed the prompt phase)
		} else {
			st.ttftStart = time.Now()
		}
	}
	if s.GenDecoded > 0 && !st.ttftStart.IsZero() && st.ttft == 0 {
		st.ttft = time.Since(st.ttftStart)
	}
	if s.PromptTokensTotal == 0 && s.GenDecoded == 0 {
		st.ttftStart = time.Time{}
		st.ttft = 0
		st.ttftPrevPromptToks = 0
	} else {
		st.ttftPrevPromptToks = s.PromptTokensTotal
	}
}

// applyRouterMetrics folds one /metrics?model=<id> snapshot into the
// model's stats — lifetime averages plus current-rate deltas and
// sparkline history. Mirrors the single-model applyMetrics.
func (r *RunMode) applyRouterMetrics(model string, m *llamaapi.Metrics) {
	st := r.statsFor(model)
	st.avgTokensPerSec = m.PredictedTokensSecondsAvg
	st.avgPromptPerSec = m.PromptTokensSecondsAvg
	st.queuedCount = int(m.RequestsDeferred)
	st.decodeTotal = m.NDecodeTotal

	if st.prevMetrics == nil {
		st.prevMetrics = m
		return
	}
	prev := st.prevMetrics
	dTokens := m.TokensPredictedTotal - prev.TokensPredictedTotal
	dTokenSecs := m.TokensPredictedSecondsTotal - prev.TokensPredictedSecondsTotal
	tickTokensRate := 0.0
	if dTokens > 0 {
		if dTokenSecs > 0 {
			tickTokensRate = dTokens / dTokenSecs
		} else {
			tickTokensRate = dTokens / livePollInterval.Seconds()
		}
		st.currentTokensPerSec = tickTokensRate
		st.tokensSeen = true
	}
	st.tokensHistory.Append(tickTokensRate)

	dPrompt := m.PromptTokensTotal - prev.PromptTokensTotal
	dPromptSecs := m.PromptSecondsTotal - prev.PromptSecondsTotal
	tickPromptRate := 0.0
	if dPrompt > 0 {
		if dPromptSecs > 0 {
			tickPromptRate = dPrompt / dPromptSecs
		} else {
			tickPromptRate = dPrompt / livePollInterval.Seconds()
		}
		st.currentPromptPerSec = tickPromptRate
		st.promptSeen = true
	}
	st.promptHistory.Append(tickPromptRate)
	st.prevMetrics = m
}

// selectedIdx returns the index of the selected model in the VISIBLE
// list (ini-only filter applied), or -1 when it is not present.
func (r *RunMode) selectedIdx() int {
	for i, m := range r.visibleRouterModels() {
		if m.ID == r.routerFocus {
			return i
		}
	}
	return -1
}

// selectedState returns the selected model's load state ("" when no
// valid selection).
func (r *RunMode) selectedState() string {
	for _, m := range r.visibleRouterModels() {
		if m.ID == r.routerFocus {
			return r.routerModelState(m.ID, m.Status.Value)
		}
	}
	return ""
}

// selectedArgs returns the selected model's launch argv (the child
// llama-server command reported by the router), or nil.
func (r *RunMode) selectedArgs() []string {
	for _, m := range r.visibleRouterModels() {
		if m.ID == r.routerFocus {
			return m.Status.Args
		}
	}
	return nil
}

// selectedIsCache reports whether the selected model is a cache-only
// leftover (InCache) rather than an ini/models-dir entry.
func (r *RunMode) selectedIsCache() bool {
	for _, m := range r.visibleRouterModels() {
		if m.ID == r.routerFocus {
			return m.IsCache()
		}
	}
	return false
}

// visibleRouterModels returns the models panel's list: all router
// models, minus cache-only leftovers when the ini-only filter is on.
func (r *RunMode) visibleRouterModels() []llamaapi.ModelInfo {
	if !r.routerIniOnly {
		return r.routerModels
	}
	kept := make([]llamaapi.ModelInfo, 0, len(r.routerModels))
	for _, m := range r.routerModels {
		if !m.IsCache() {
			kept = append(kept, m)
		}
	}
	return kept
}

// flashTTL is how long an informational flash stays in the footer
// before it auto-dismisses.
const flashTTL = 3 * time.Second

// flashExpiredMsg clears the footer flash when its TTL elapses. gen
// guards against an older tick clearing a newer flash.
type flashExpiredMsg struct{ gen int }

// setFlash shows msg in the footer and schedules its auto-dismissal
// (3s). A newer flash supersedes an older one.
func (r *RunMode) setFlash(msg string) tea.Cmd {
	r.flash = msg
	gen := r.flashGen + 1
	r.flashGen = gen
	return tea.Tick(flashTTL, func(time.Time) tea.Msg {
		return flashExpiredMsg{gen: gen}
	})
}

// isProxyNoise reports whether a log line is llama.cpp router chatter
// of the form "proxying request to model <id> on port <n>" — emitted
// once per proxied request, including llamaman's own stats polls.
func isProxyNoise(line string) bool {
	return strings.Contains(line, "proxying request to model")
}

// filterProxyLines removes router proxy-chatter lines from a log chunk.
// Kept for ingest-side use; the run-mode view filters via
// visibleLogLines instead so toggling denoise can restore lines.
func filterProxyLines(chunk string) string {
	lines := strings.Split(chunk, "\n")
	kept := lines[:0]
	for _, l := range lines {
		if isProxyNoise(l) {
			continue
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

// visibleLogLines returns the log buffer's lines as the view should
// show them: with denoise on in router mode, proxy-chatter lines are
// hidden (but stay in the buffer — toggling denoise off restores them
// at their exact positions).
func (r *RunMode) visibleLogLines() []string {
	lines := strings.Split(r.buf.String(), "\n")
	if !(r.routerFile != "" && r.denoise) {
		return lines
	}
	kept := lines[:0]
	for _, l := range lines {
		if isProxyNoise(l) {
			continue
		}
		kept = append(kept, l)
	}
	return kept
}

// formatArgvLines renders a launch argv (binary dropped) as aligned
// "--flag  value" pairs, capped at maxLines (an ellipsis line notes any
// remainder). Each line is truncated to maxLineWidth so the info box
// stays within the viewport.
func formatArgvLines(args []string, maxLines, maxLineWidth int) []string {
	if len(args) <= 1 {
		return nil
	}
	type kv struct{ flag, val string }
	var pairs []kv
	for i := 1; i < len(args); i++ { // args[0] is the binary
		flag := args[i]
		val := ""
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
			val = args[i+1]
			i++
		}
		pairs = append(pairs, kv{flag: flag, val: val})
	}
	keyWidth := 0
	for _, p := range pairs {
		if w := lipgloss.Width(p.flag); w > keyWidth {
			keyWidth = w
		}
	}
	lines := make([]string, 0, min(maxLines, len(pairs)))
	for i, p := range pairs {
		if i >= maxLines {
			lines = append(lines, fmt.Sprintf("… and %d more", len(pairs)-maxLines))
			break
		}
		line := padRight(p.flag, keyWidth)
		if p.val != "" {
			line += "  " + p.val
		}
		lines = append(lines, ansi.Truncate(line, maxLineWidth, "…"))
	}
	return lines
}

// moveRouterSelection moves the selection by delta (wrapping) and
// keeps it inside the list's visible window. Stale selections (model
// gone) restart at the first row.
func (r *RunMode) moveRouterSelection(delta int) {
	models := r.visibleRouterModels()
	if len(models) == 0 {
		return
	}
	idx := r.selectedIdx()
	if idx < 0 {
		idx = 0
	} else {
		idx = (idx + delta + len(models)) % len(models)
	}
	r.routerFocus = models[idx].ID
	r.clampRouterList(idx)
}

// clampRouterList keeps the scroll window valid: the selection stays
// visible, and the window stays inside the list. selIdx < 0 re-derives
// it from the current selection.
func (r *RunMode) clampRouterList(selIdx int) {
	if selIdx < 0 {
		selIdx = r.selectedIdx()
	}
	if selIdx < 0 {
		r.routerListStart = 0
		return
	}
	visible := liveBandContentRows
	if selIdx < r.routerListStart {
		r.routerListStart = selIdx
	}
	if selIdx >= r.routerListStart+visible {
		r.routerListStart = selIdx - visible + 1
	}
	if max := len(r.visibleRouterModels()) - visible; r.routerListStart > max {
		if max < 0 {
			max = 0
		}
		r.routerListStart = max
	}
	if r.routerListStart < 0 {
		r.routerListStart = 0
	}
}

// routerMenuAction is one entry of the Enter action menu.
type routerMenuAction int

const (
	menuLoad routerMenuAction = iota
	menuUnload
	menuStats
	menuInfo
)

// menuActions lists the four menu entries in display order.
func (r *RunMode) menuActions() []routerMenuAction {
	return []routerMenuAction{menuLoad, menuUnload, menuStats, menuInfo}
}

// menuItemEnabled reports whether an action applies to the current
// selection (Load needs unloaded, Unload needs loaded).
func (r *RunMode) menuItemEnabled(a routerMenuAction) bool {
	switch a {
	case menuLoad:
		return r.selectedState() != "loaded"
	case menuUnload:
		return r.selectedState() == "loaded"
	default:
		return true
	}
}

// handleRouterMenuKey navigates the Enter action menu (↑/↓ skip
// inapplicable entries, Enter runs, Esc dismisses).
func (r *RunMode) handleRouterMenuKey(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "up", "down":
		delta := -1
		if m.String() == "down" {
			delta = 1
		}
		actions := r.menuActions()
		for i := 0; i < len(actions); i++ {
			r.routerMenuIdx = (r.routerMenuIdx + delta + len(actions)) % len(actions)
			if r.menuItemEnabled(actions[r.routerMenuIdx]) {
				break
			}
		}
	case "enter":
		return r.applyRouterMenu()
	case "esc", "c", "q":
		r.routerMenu = false
	}
	return r, nil
}

// applyRouterMenu runs the highlighted menu action.
func (r *RunMode) applyRouterMenu() (*RunMode, tea.Cmd) {
	r.routerMenu = false
	if r.routerFocus == "" {
		return r, nil
	}
	switch r.menuActions()[r.routerMenuIdx] {
	case menuLoad:
		if r.selectedState() == "loaded" {
			return r, r.setFlash("model already loaded")
		}
		return r, loadModelCmd(r.fetchCtx, r.fetcher, r.routerFocus)
	case menuUnload:
		if r.selectedState() != "loaded" {
			return r, r.setFlash("model is not loaded")
		}
		r.unloadPrompt = true
		return r, nil
	case menuStats:
		r.showRouterStats = true
	case menuInfo:
		r.showInfo = true
	}
	return r, nil
}

// renderServerRows builds the seven server-panel content rows
// (Tokens / Prompt / Process / Context / Breakdown / Cache / Gen) from
// a statsView. Shared by single-model and router focus views.
func (r *RunMode) renderServerRows(sv *statsView) []string {
	return []string{
		r.renderServerRow(sv, "Tokens", sv.tokensHistory, sv.currentTokensPerSec, sv.avgTokensPerSec, sv.tokensSeen, sv.busyCount, sv.totalSlots, true),
		r.renderServerRow(sv, "Prompt", sv.promptHistory, sv.currentPromptPerSec, sv.avgPromptPerSec, sv.promptSeen, sv.queuedCount, 0, false),
		r.renderPromptProgressRow(sv),
		r.renderContextRow(sv),
		r.renderContextBreakdownRow(sv),
		r.renderCacheRow(sv),
		r.renderGenProgressRow(sv),
	}
}

// panelBorderColor picks the titled-panel border: BorderFocus when the
// panel is the active ↑/↓ target, Border otherwise.
func (r *RunMode) panelBorderColor(focused bool) lipgloss.Color {
	if focused {
		return r.theme.BorderFocus
	}
	return r.theme.Border
}

// logBorderColor returns the log frame border: BorderFocus while the
// log is the active ↑/↓ target in router mode (models panel inactive),
// Border otherwise.
func (r *RunMode) logBorderColor() lipgloss.Color {
	if r.routerFile != "" && !r.routerPanelActive {
		return r.theme.BorderFocus
	}
	return r.theme.Border
}

// renderLogFrame wraps the pre-truncated log content in the bordered
// log box. The border lights up (BorderFocus) in router mode while the
// models panel is inactive — the log is the focused panel then, and
// ↑/↓ scroll it.
func (r *RunMode) renderLogFrame(logContent string) string {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.logBorderColor()).
		Padding(0, 1).
		Width(r.width - 2).
		Render(logContent)
}

// renderServerPanel renders the llama-server live data box (SP3
// shape, DESIGN.md §7.4): seven content rows.
//
//	Tokens <spark>   80.0 /  60.0 /s avg   Busy   2/4 slots
//	Prompt <spark> 2331.0 / 2300.0 /s avg  Queued 1
//	Process  <bar>    45% (2.8K/6.2K)
//	Context  <bar>    15K/80K (19%)
//	Breakdown  <bar>    prompt(gen)empty
//	Cache  <bar>    80%
//	Gen    <bar>     279/16K (2%)   TTFT 1.2s
//
// Tokens/Prompt sparklines roll over 40s. Trailing rate cell shows
// "current / avg /s avg" — current is the per-tick instantaneous,
// avg is the lifetime gauge llama-server maintains. Both persist
// last-known after the first non-zero (Bug 3); before any inference,
// the cell reads "—". When --metrics is off, "n/a".
// Process row shows prompt processing progress.
// Context row shows tokens-in-context vs max context window.
// Breakdown row shows context breakdown: prompt (purple) + gen (orange) + empty.
// Cache row shows prompt cache hit ratio from /slots.
// Gen row shows response progress with TTFT (time to first token).
//
// In router mode this panel becomes the model list, or — when the
// selection's stats are open (Enter) — that model's full seven-row
// stats panel.
func (r *RunMode) renderServerPanel(width int) string {
	if r.routerFile != "" {
		if r.showRouterStats && r.routerFocus != "" {
			return r.renderRouterStatsPanel(width)
		}
		return r.renderRouterPanel(width)
	}
	if r.showingLoadBlock() {
		// §15.4: live stats can't render before READY — the panel shows
		// the load-progress block during the starting window.
		return r.renderTitledPanel("llama-server", width, padRows(r.loadRows(), liveBandContentRows), false)
	}
	return r.renderTitledPanel("llama-server", width, padRows(r.renderServerRows(r.singleModelStatsView()), liveBandContentRows), false)
}

// singleModelStatsView projects RunMode's single-model scalar fields
// into a statsView for the shared panel renderers.
func (r *RunMode) singleModelStatsView() *statsView {
	return &statsView{
		metricsAvailable:    r.metricsAvailable,
		tokensHistory:       r.tokensHistory,
		currentTokensPerSec: r.currentTokensPerSec,
		avgTokensPerSec:     r.avgTokensPerSec,
		tokensSeen:          r.tokensSeen,
		promptHistory:       r.promptHistory,
		currentPromptPerSec: r.currentPromptPerSec,
		avgPromptPerSec:     r.avgPromptPerSec,
		promptSeen:          r.promptSeen,
		busyCount:           r.busyCount,
		totalSlots:          r.totalSlots,
		queuedCount:         r.queuedCount,
		decodeTotal:         r.decodeTotal,
		contextUsed:         r.contextUsed,
		contextMax:          r.contextMax,
		contextCacheHit:     r.contextCacheHit,
		contextPromptToks:   r.contextPromptToks,
		contextGenToks:      r.contextGenToks,
		genDecoded:          r.genDecoded,
		genRemain:           r.genRemain,
		promptToksTotal:     r.promptToksTotal,
		promptToksProcessed: r.promptToksProcessed,
		ttft:                r.ttft,
	}
}

// renderRouterStatsPanel shows the focused router model's full
// seven-row stats panel — full parity with the single-model view.
func (r *RunMode) renderRouterStatsPanel(width int) string {
	sv := &statsView{
		metricsAvailable: r.routerMetricsAvailable,
		tokensHistory:    newRingBuffer(sparkBufferSamples),
		promptHistory:    newRingBuffer(sparkBufferSamples),
	}
	if st, ok := r.routerStats[r.routerFocus]; ok && st != nil {
		sv = &st.statsView
		sv.metricsAvailable = r.routerMetricsAvailable
	}
	title := "router stats"
	if r.routerFocus != "" {
		// renderTitledPanel truncates the title to the panel border,
		// so pass the full id — no premature trimming.
		title = "router · " + r.routerFocus
	}
	return r.renderTitledPanel(title, width, padRows(r.renderServerRows(sv), liveBandContentRows), r.routerPanelActive)
}

// renderRouterModelCount renders the "N total (M loaded)" header cell
// for router runs: model count from GET /models, loaded count from
// GET /health. "—" until the first successful fetch.
func (r *RunMode) renderRouterModelCount() string {
	if r.routerModels == nil && r.routerLoaded == nil {
		return "—"
	}
	loaded := 0
	for _, m := range r.routerModels {
		if r.routerModelState(m.ID, m.Status.Value) == "loaded" {
			loaded++
		}
	}
	return fmt.Sprintf("%d total (%d loaded)", len(r.routerModels), loaded)
}

// routerModelState resolves one model's load state. Current llama.cpp
// routers report it per model in GET /models ("status.value": loaded /
// loading / unloaded); earlier builds listed loaded ids in GET /health
// instead. Prefer the per-model field, fall back to /health.
func (r *RunMode) routerModelState(id, status string) string {
	if status != "" {
		return status
	}
	for _, loadedID := range r.routerLoaded {
		if loadedID == id {
			return "loaded"
		}
	}
	return "unloaded"
}

// renderRouterPanel replaces the llama-server live-data panel in router
// runs: one row per model from GET /models, tagged loaded/loading/
// unloaded. During the starting window it shows the load-progress block
// instead (§15.4 — the model list can't render before READY).
func (r *RunMode) renderRouterPanel(width int) string {
	if r.showingLoadBlock() {
		return r.renderTitledPanel("models", width, padRows(r.loadRows(), liveBandContentRows), r.routerPanelActive)
	}
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	loadedStyle := lipgloss.NewStyle().Foreground(r.theme.StatusReady)
	unloadedStyle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	loadingStyle := lipgloss.NewStyle().Foreground(r.theme.Accent)

	selStyle := lipgloss.NewStyle().Foreground(r.theme.Accent)
	models := r.visibleRouterModels()
	var rows []string
	if len(models) == 0 {
		if len(r.routerModels) == 0 {
			rows = append(rows, subtle.Render("(no models reported)"))
		} else {
			rows = append(rows, subtle.Render("(all models are cache-only — p to show)"))
		}
	} else {
		// Scroll window: render only the rows around the selection so a
		// long list stays navigable and the selection is always visible.
		r.clampRouterList(-1)
		visible := liveBandContentRows
		start := r.routerListStart
		end := start + visible
		if end > len(models) {
			end = len(models)
		}
		for _, m := range models[start:end] {
			state := r.routerModelState(m.ID, m.Status.Value)
			mark, style := "○", unloadedStyle
			var baseColor lipgloss.Color = r.theme.Subtle
			switch state {
			case "loaded":
				mark, style, baseColor = "●", loadedStyle, r.theme.StatusReady
			case "loading":
				mark, style, baseColor = "◐", loadingStyle, r.theme.Accent
			}
			// One-shot flash on a just-loaded/unloaded model (§15.5).
			if animationsEnabled(r.cfg) {
				if base, ok := r.routerFlash[m.ID]; ok {
					if s := oneShotStrength(base, routerFlashDur); s > 0 {
						style = lipgloss.NewStyle().Foreground(oneShotColor(baseColor, s))
					} else {
						delete(r.routerFlash, m.ID) // expired
					}
				}
			}
			// Selection indicator: a leading ▸ on the selected row.
			sel := "  "
			if m.ID == r.routerFocus {
				sel = selStyle.Render("▸ ") + ""
			}
			id := truncateRune(m.ID, routerPanelIDMax)
			if id == "" {
				id = "?"
			}
			stats := ""
			if st, ok := r.routerStats[m.ID]; ok && st != nil {
				activity := "idle"
				if st.busyCount > 0 {
					activity = "processing"
				}
				parts := []string{humanTokens(st.contextUsed) + "/" + humanTokens(st.contextMax)}
				if st.avgTokensPerSec > 0 {
					parts = append(parts, fmt.Sprintf("%.1f tok/s", st.avgTokensPerSec))
				}
				parts = append(parts, activity)
				stats = " · " + strings.Join(parts, " · ")
			}
			rows = append(rows, sel+style.Render(mark+" "+id)+subtle.Render(stats)+"  "+subtle.Render(state))
			if m.IsCache() {
				rows[len(rows)-1] += subtle.Render(" (cache)")
			}
		}
	}
	return r.renderTitledPanel("router models", width, padRows(rows, liveBandContentRows), r.routerPanelActive)
}

// renderRouterMenu renders the Enter action menu for the selection:
// Load / Unload / Statistics / Info, inapplicable entries grayed.
func (r *RunMode) renderRouterMenu() string {
	title := truncateRune(r.routerFocus, routerPanelIDMax)
	if title == "" {
		title = "?"
	}
	accent := lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true)
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	muted := lipgloss.NewStyle().Foreground(r.theme.Muted)

	items := []struct {
		name    string
		enabled bool
	}{
		{"Load", r.menuItemEnabled(menuLoad)},
		{"Unload", r.menuItemEnabled(menuUnload)},
		{"Statistics", true},
		{"Info", true},
	}
	lines := make([]string, 0, len(items))
	for i, it := range items {
		mark := "  "
		if i == r.routerMenuIdx {
			mark = accent.Render("▸ ")
		}
		style := subtle
		if !it.enabled {
			style = muted
		}
		lines = append(lines, mark+style.Render(it.name))
	}
	body := title + "\n\n" + strings.Join(lines, "\n") + "\n\n  " +
		r.promptShortcuts("↑/↓ move", "⏎ select", "esc cancel")
	return r.promptBox().Render(body)
}

// routerPanelIDMax truncates long model ids in the router panel so the
// per-model stats suffix (ctx, tok/s, activity) stays visible on a
// half-width panel.
const routerPanelIDMax = 34

// truncateRune shortens s to at most max runes, appending "…".
func truncateRune(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}

// humanTokens renders a token count compactly: 950 → "950", 4222 →
// "4.2K", 65536 → "65.5K", 150000 → "150K".
func humanTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	s := strings.TrimSuffix(fmt.Sprintf("%.1f", float64(n)/1000), ".0")
	return s + "K"
}

// renderServerRow builds one row of the server panel: label + spark
// + rate cell ("current / avg /s") + secondary scalar (Busy or
// Queued). When the rate has never been seen, the cell renders as
// "—" (Bug 3 persistence). When metrics are disabled, "n/a".
func (r *RunMode) renderServerRow(sv *statsView, label string, hist *ringBuffer, currentRate, avgRate float64, seen bool, sec1, sec2 int, isBusyRow bool) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight(label, metricLabelWidth))

	// Sparkline live-edge glow (§15.5): while busy, the whole
	// sparkline breathes softly (the newest cell is where the motion
	// reads).
	sparkTheme := r.theme
	if animationsEnabled(r.cfg) && r.busyCount > 0 {
		sparkTheme = pulseTheme(sparkTheme, animPhase(1000*time.Millisecond))
	}
	spark := renderSparkline(sparkTheme, hist.Snapshot(), MetricUtil)

	// Rate cell: "<current> tps / <avg> avg". Fixed-width slots so
	// the column stays put as values transition (e.g. 99.9 → 100.0
	// or 999 → 1000). %6.1f covers up to "9999.9" which is more
	// than llama-server ever produces. Current shows "—" until the
	// first non-zero delta; avg (from the server gauge) is available
	// from tick 1.
	const rateSlot = "%6.1f"
	var rateCell string
	switch {
	case !sv.metricsAvailable:
		rateCell = "             n/a"
	case !seen:
		rateCell = fmt.Sprintf("     — tps / "+rateSlot+" avg", avgRate)
	default:
		rateCell = fmt.Sprintf(rateSlot+" tps / "+rateSlot+" avg", currentRate, avgRate)
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
		if sv.metricsAvailable {
			secondary = subtle.Render("Queued ") + strconv.Itoa(sec1)
		} else {
			secondary = subtle.Render("Queued ") + "n/a"
		}
	}
	return labelCell + " " + spark + " " + rateCell + "   " + secondary
}

// renderContextRow builds the third server-panel row showing context
// window usage.
//
//	Context  <bar>    15K/80K (19%)
//
// When no data is available (slots not fetched), shows "n/a".
func (r *RunMode) renderContextRow(sv *statsView) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight("Context", metricLabelWidth))

	if sv.contextMax == 0 {
		return labelCell + " " + renderSparkline(r.theme, []float64{-1}, MetricUtil) + "             n/a"
	}

	ctxPct := float64(sv.contextUsed) / float64(sv.contextMax) * 100
	if ctxPct > 100 {
		ctxPct = 100
	}
	ctxBar := renderBar(r.theme, ctxPct, zoneFor(MetricUtil, ctxPct))

	ctxUsedStr := formatTokenCount(sv.contextUsed)
	ctxMaxStr := formatTokenCount(sv.contextMax)
	ctxRateCell := fmt.Sprintf("%s/%s (%d%%)", ctxUsedStr, ctxMaxStr, int(ctxPct))

	return labelCell + " " + ctxBar + " " + ctxRateCell
}

// renderCacheRow builds the fourth server-panel row showing prompt
// cache hit ratio as a bar.
//
//	Cache  <bar>    80%
//
// When no data is available, shows "n/a".
func (r *RunMode) renderCacheRow(sv *statsView) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight("Cache", metricLabelWidth))

	if sv.contextUsed == 0 {
		return labelCell + " " + renderSparkline(r.theme, []float64{-1}, MetricUtil) + "             n/a"
	}

	cachePct := float64(sv.contextCacheHit) / float64(sv.contextUsed) * 100
	if cachePct > 100 {
		cachePct = 100
	}
	// Invert: high cache hit = good (green), low = bad (red).
	// Feed (100 - pct) to zoneFor so the color tiers flip.
	cacheBar := renderBar(r.theme, cachePct, zoneFor(MetricMem, 100-cachePct))

	cacheCell := fmt.Sprintf("%d%%", int(cachePct))
	return labelCell + " " + cacheBar + " " + cacheCell
}

// formatTokenCount formats a token count with K/M suffix for display.
// e.g. 15234 → "15K", 1048576 → "1M", 8192 → "8192".
func formatTokenCount(n int) string {
	if n >= 1000000 {
		return fmt.Sprintf("%dM", n/1000000)
	}
	if n >= 10000 {
		return fmt.Sprintf("%dK", n/1000)
	}
	return strconv.Itoa(n)
}

// renderGenProgressRow builds the fourth server-panel row showing
// generation progress: tokens generated so far vs the generation
// limit (n_decoded / (n_decoded + n_remain)).
//
//	Gen    <bar>    279/16K (2%)           —
//
// When no generation is in progress, shows the last known values
// or "n/a" if no data.
func (r *RunMode) renderGenProgressRow(sv *statsView) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight("Gen", metricLabelWidth))

	totalAllocated := sv.genDecoded + sv.genRemain
	if totalAllocated == 0 {
		return labelCell + " " + renderSparkline(r.theme, []float64{-1}, MetricUtil) + "             n/a"
	}

	genPct := float64(sv.genDecoded) / float64(totalAllocated) * 100
	if genPct > 100 {
		genPct = 100
	}
	if animationsEnabled(r.cfg) {
		r.genFrac.set(genPct)
		genPct = r.genFrac.display() // smooth fill between polls (§15.5)
	}
	genBar := renderBar(r.theme, genPct, zoneFor(MetricUtil, genPct))

	genDecodedStr := formatTokenCount(sv.genDecoded)
	genTotalStr := formatTokenCount(totalAllocated)
	genRateCell := fmt.Sprintf("%s/%s (%d%%)", genDecodedStr, genTotalStr, int(genPct))

	// TTFT display
	var ttftCell string
	ttftColor := r.theme.Subtle
	if animationsEnabled(r.cfg) {
		if s := oneShotStrength(r.ttftAt, ttftGlowDur); s > 0 {
			ttftColor = oneShotColor(r.theme.StatusReady, s) // TTFT arrival glow (§15.5)
		}
	}
	ttftStyle := lipgloss.NewStyle().Foreground(ttftColor)
	if sv.ttft < 0 {
		// Missed the prompt phase — generation already started.
		ttftCell = subtle.Render("TTFT ") + "<1s"
	} else if sv.ttft > 0 {
		ttftCell = subtle.Render("TTFT ") + ttftStyle.Render(formatDuration(sv.ttft))
	} else {
		ttftCell = subtle.Render("TTFT ") + "—"
	}

	return labelCell + " " + genBar + " " + genRateCell + "   " + ttftCell
}

// formatDuration formats a duration for display: "1.2s" or "250ms".
func formatDuration(d time.Duration) string {
	secs := float64(d) / 1000000000
	if secs >= 1 {
		return fmt.Sprintf("%.1fs", secs)
	}
	return fmt.Sprintf("%dms", d.Milliseconds())
}

// renderPromptProgressRow shows prompt processing progress.
//
//	Process  <bar>    45% (2.8K/6.2K)
//
// When no prompt is being processed, shows "n/a".
func (r *RunMode) renderPromptProgressRow(sv *statsView) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight("Process", metricLabelWidth))

	if sv.promptToksTotal == 0 {
		return labelCell + " " + renderSparkline(r.theme, []float64{-1}, MetricUtil) + "             n/a"
	}

	pct := float64(sv.promptToksProcessed) / float64(sv.promptToksTotal) * 100
	if pct > 100 {
		pct = 100
	}
	if animationsEnabled(r.cfg) {
		r.procFrac.set(pct)
		pct = r.procFrac.display() // smooth fill between polls (§15.5)
	}
	bar := renderBar(r.theme, pct, zoneFor(MetricUtil, pct))

	processedStr := formatTokenCount(sv.promptToksProcessed)
	totalStr := formatTokenCount(sv.promptToksTotal)
	rateCell := fmt.Sprintf("%s/%s (%d%%)", processedStr, totalStr, int(pct))

	return labelCell + " " + bar + " " + rateCell
}

// renderContextBreakdownRow shows context usage breakdown:
// prompt tokens (purple), generated tokens (orange), empty (dim).
//
//	Breakdown  <bar>  4.2K prompt 279 gen 77K free
//
// When no data is available, shows "n/a".
func (r *RunMode) renderContextBreakdownRow(sv *statsView) string {
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	labelCell := subtle.Render(padRight("Breakdown", metricLabelWidth))

	if sv.contextMax == 0 {
		return labelCell + " " + renderSparkline(r.theme, []float64{-1}, MetricUtil) + "             n/a"
	}

	// Build a segmented bar: prompt (purple), gen (orange), empty (dim).
	bar := r.renderSegmentedBar(sv.contextPromptToks, sv.contextGenToks, sv.contextMax)

	purpleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9B59B6"))
	orangeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8C00"))
	promptStr := purpleStyle.Render(formatTokenCount(sv.contextPromptToks))
	genStr := orangeStyle.Render(formatTokenCount(sv.contextGenToks))
	freeToks := sv.contextMax - sv.contextPromptToks - sv.contextGenToks
	if freeToks < 0 {
		freeToks = 0
	}
	freeStr := subtle.Render(formatTokenCount(freeToks))
	rateCell := fmt.Sprintf("%s prompt %s gen %s free", promptStr, genStr, freeStr)

	return labelCell + " " + bar + " " + rateCell
}

// renderSegmentedBar renders a bar with two colored segments:
// segment1 (purple/prompt), segment2 (orange/generated), remainder (dim/empty).
func (r *RunMode) renderSegmentedBar(seg1, seg2, total int) string {
	if total == 0 {
		return renderSparkline(r.theme, []float64{-1}, MetricUtil)
	}
	seg1Pct := float64(seg1) / float64(total) * 100
	seg2Pct := float64(seg2) / float64(total) * 100

	seg1Cells := int((seg1Pct/100)*liveBarWidth + 0.5)
	seg2Cells := int((seg2Pct/100)*liveBarWidth + 0.5)

	purpleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#9B59B6")) // purple — prompt
	orangeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#FF8C00")) // dark orange — generated
	dimStyle := lipgloss.NewStyle().Foreground(r.theme.Muted)

	var b strings.Builder
	b.Grow(liveBarWidth * 4)
	for i := 0; i < liveBarWidth; i++ {
		if i < seg1Cells {
			b.WriteString(purpleStyle.Render(barFillChar))
		} else if i < seg1Cells+seg2Cells {
			b.WriteString(orangeStyle.Render(barFillChar))
		} else {
			b.WriteString(dimStyle.Render(barEmptyChar))
		}
	}
	return b.String()
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
	return r.renderTitledPanel("Hardware", width, padRows(rows, liveBandContentRows), false)
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

	rows := []string{header, utilRow, memRow}
	if powerRow != "" {
		rows = append(rows, powerRow)
	}
	if tempRow != "" {
		rows = append(rows, tempRow)
	}
	return rows
}

// joinMetricCells builds a metric row from its four cell parts:
// label + viz + value + trailing-%. Cells are joined by fixed
// separators so the column positions are deterministic across rows.
//
//	"    LABEL VIZ    VALUE  PCT"
//
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
func (r *RunMode) renderTitledPanel(title string, width int, contentRows []string, focused bool) string {
	border := lipgloss.NewStyle().Foreground(r.panelBorderColor(focused))
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
// end of row 1. A per-state unicode glyph prefix (DESIGN §15.3) plus
// the bracketed uppercase label, bold, in the state's themed
// foreground color. The [STARTING] badge text/format stays (only the
// hollow-dot prefix is additive — §2.3).
func statusBadge(label string, color lipgloss.Color) string {
	glyph := ""
	switch strings.ToUpper(label) {
	case "READY":
		glyph = "● "
	case "STARTING":
		glyph = "◌ "
	case "ERROR":
		glyph = "✕ "
	case "EXITED":
		glyph = "◌ "
	}
	return lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render(glyph + "[" + strings.ToUpper(label) + "]")
}

// toggleLogColors flips the log-colors preference live (DESIGN §15.3):
// the run-mode `o` quick key writes the same object the Settings form
// edits (P8) and persists it when a config path is available; without
// one it flips in-memory only.
func (r *RunMode) toggleLogColors() (*RunMode, tea.Cmd) {
	if r.cfg == nil {
		return r, r.setFlash("no config to toggle log colors in")
	}
	prefs := r.cfg.Prefs()
	enabled := !prefs.LogColorsEnabled()
	prefs.LogColors = &enabled
	r.cfg.Preferences = &prefs
	if r.cfgPath != "" {
		if err := config.Save(r.cfgPath, r.cfg); err != nil {
			return r, r.setFlash("could not save log-colors: " + err.Error())
		}
	}
	state := "off"
	if enabled {
		state = "on"
	}
	r.refreshContent()
	return r, r.setFlash("log colors " + state)
}

// parseLoadPhase classifies a llama-server stderr line into a
// load-progress phase (DESIGN §15.4). Tolerant by design (§2.3/P6):
// patterns that stop matching simply yield no phase and the indicator
// degrades to the static "loading…" block.
func parseLoadPhase(line string) (phase string, progress *float64) {
	if m := loadOffloadedRE.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[1])
		d, _ := strconv.Atoi(m[2])
		if d > 0 {
			p := float64(n) / float64(d)
			if p > 1 {
				p = 1
			}
			return "offloading layers to GPU", &p
		}
		return "offloading layers to GPU", nil
	}
	if loadOffloadingRE.MatchString(line) {
		return "offloading layers to GPU", nil
	}
	if loadOffloadOutputRE.MatchString(line) {
		return "offloading layers to GPU", nil
	}
	if m := loadDownloadRE.FindStringSubmatch(line); m != nil {
		if pct, err := strconv.ParseFloat(m[1], 64); err == nil && pct >= 0 && pct <= 100 {
			p := pct / 100
			return "downloading model", &p
		}
		return "downloading model", nil
	}
	if loadLoadingRE.MatchString(line) {
		return "loading model", nil
	}
	if loadInitRE.MatchString(line) {
		return "initializing", nil
	}
	if loadLoadedRE.MatchString(line) {
		return "model loaded", nil
	}
	return "", nil
}

var (
	loadOffloadedRE     = regexp.MustCompile(`(?i)offloaded\s+(\d+)\s*/\s*(\d+)\s+layers`)
	loadOffloadingRE    = regexp.MustCompile(`(?i)offloading\s+\d+\s+repeating\s+layers`)
	loadOffloadOutputRE = regexp.MustCompile(`(?i)offloading output layer`)
	loadDownloadRE      = regexp.MustCompile(`(?i)(?:downloading|download).*?(\d+(?:\.\d+)?)\s*%`)
	loadLoadingRE       = regexp.MustCompile(`(?i)loading\s+model\b`)
	loadInitRE          = regexp.MustCompile(`(?i)\binitializing\b`)
	loadLoadedRE        = regexp.MustCompile(`(?i)\bmodel loaded\b`)
)

// showingLoadBlock reports whether the left panel should show the
// load-progress block: while starting, or for the 2s minimum-visible
// window after the last phase line even once READY (§15.4).
func (r *RunMode) showingLoadBlock() bool {
	return r.status == StatusStarting || time.Now().Before(r.loadPhaseUntil)
}

// loadRows builds the panel content for the starting window (§15.4):
// the parsed phase line(s) with the progress bar, or the static
// "loading…" fallback when nothing has been parsed yet. Owner polish:
// a blank row above the block, a " > " prefix + one trailing space per
// row, and the phase text in the brighter Accent color. With
// animations on (§15.5) the color breathes and, when no numeric
// progress is known, an indeterminate moving fill replaces the static
// bar.
func (r *RunMode) loadRows() []string {
	accentColor := r.theme.Accent
	if animationsEnabled(r.cfg) {
		accentColor = animColor(r.theme.Accent, lighten(r.theme.Accent, 0.3), 1600*time.Millisecond)
	}
	accent := lipgloss.NewStyle().Foreground(accentColor)
	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	rows := []string{""}
	if r.loadPhase != "" {
		rows = append(rows, accent.Render(" > "+r.loadPhase)+" ")
		if r.loadProgress != nil {
			pct := int(*r.loadProgress * 100)
			if pct > 100 {
				pct = 100
			}
			if pct < 0 {
				pct = 0
			}
			bar := progressBar(*r.loadProgress, 12)
			rows = append(rows, accent.Render(" > "+bar)+subtle.Render(fmt.Sprintf(" %d%%", pct))+" ")
		} else if animationsEnabled(r.cfg) {
			// No numeric progress: the indeterminate comet (§15.5).
			p, forward := cometPhase(1600 * time.Millisecond)
			rows = append(rows, accent.Render(" > "+indeterminateBar(12, p, forward))+" ")
		}
	} else {
		rows = append(rows, accent.Render(" > loading…")+" ")
	}
	return rows
}

// blockFrags are the 7 sub-cell block fragments (U+258F..U+2589, high
// to low) used by the comet's tail: ▉ (7/8) nearest the head down to
// ▏ (1/8) farthest away.
var blockFrags = []rune{'▏', '▎', '▍', '▌', '▋', '▊', '▉'}

func cometPhase(period time.Duration) (float64, bool) {
	f := float64(clock().UnixMilli()%period.Milliseconds()) / float64(period.Milliseconds())
	if f < 0.5 {
		return f * 2, true
	}
	return (1 - f) * 2, false
}

// indeterminateBar renders a comet: a solid █ head leading, with a
// 7-fragment tail behind it (▉ nearest the head … ▏ farthest — the
// "tail follows the head" look, owner's design). Cycle per direction:
// slide across the track, then at the far edge the head pins and the
// tail merges into a solid block, then the block dissolves back to the
// head, which is where the opposite pass takes over — no teleports, no
// static holds, no fabricated percentages (§15.5), and no fragment
// ever on the wrong side of the head.
func indeterminateBar(width, phase float64, forward bool) string {
	w := int(width)
	const tail = 7
	// Slide (w-1-tail) + drain (tail) = 11 units of work; travel = 20
	// leaves a ~360ms edge hold at 1600ms/cycle (owner: a bit less
	// waiting at the edges than the previous +15).
	travel := float64(w-1) + 2*float64(tail) - 5

	var head float64
	if forward {
		head = float64(tail) + phase*travel // p: 0→1
	} else {
		head = float64(w-1) - (1-phase)*travel // p: 1→0
	}
	head = float64(int(head)) // whole-cell motion, no gaps

	// Drain: the head pins at the far edge; the tail shrinks from its
	// far end (drained = how many far fragments have vanished).
	drained := 0
	headInt := int(head)
	if forward {
		if head > float64(w-1+tail) {
			head = float64(w - 1 + tail)
		}
		if headInt > w-1 {
			drained = int(head) - (w - 1)
			if drained > tail {
				drained = tail
			}
			headInt = w - 1
		}
	} else {
		if head < float64(-tail) {
			head = float64(-tail)
		}
		if headInt < 0 {
			drained = -int(head)
			if drained > tail {
				drained = tail
			}
			headInt = 0
		}
	}

	out := make([]rune, w)
	for i := 0; i < w; i++ {
		if i == headInt {
			out[i] = '█'
			continue
		}
		var pd int
		if forward {
			pd = headInt - i // tail behind the head: to the left
		} else {
			pd = i - headInt // tail to the right
		}
		switch {
		case pd <= 0 || pd > tail:
			out[i] = '░'
		case pd > tail-drained:
			out[i] = '░' // far end has drained
		default:
			out[i] = blockFrags[tail-pd] // ▉ nearest … ▏ farthest
		}
	}
	return string(out)
}

// progressBar renders a fixed-width filled/empty block bar (▓/░) for a
// progress fraction in [0,1], capped at width cells.
func progressBar(frac float64, width int) string {
	if width < 1 {
		width = 1
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	filled := int(frac*float64(width) + 0.5)
	return strings.Repeat("▓", filled) + strings.Repeat("░", width-filled)
}

// ingestLoadChunk feeds a log chunk to the load-progress classifier
// (§15.4): while starting, the newest phase/progress is kept and the
// 2s minimum-visible deadline extends on every update. The tailer
// delivers raw 4096-byte chunks that can split a line in two, so a
// partial-line accumulator reassembles them before parsing (owner
// feedback: only "loading…" ever showed).
func (r *RunMode) ingestLoadChunk(chunk string) {
	if r.status != StatusStarting {
		return
	}
	joined := r.loadPartial + chunk
	lines := strings.Split(joined, "\n")
	r.loadPartial = lines[len(lines)-1] // keep the unfinished tail
	for _, ln := range lines[:len(lines)-1] {
		if phase, prog := parseLoadPhase(ln); phase != "" {
			r.loadPhase = phase
			r.loadProgress = prog
			r.loadPhaseUntil = time.Now().Add(loadPhaseMinVisible)
		}
	}
}

// toggleAnimations flips the animations preference live (§15.5): the
// run-mode `a` quick key writes the same object the Settings form
// edits (P8) and persists it when a config path is available.
func (r *RunMode) toggleAnimations() (*RunMode, tea.Cmd) {
	if r.cfg == nil {
		return r, r.setFlash("no config to toggle animations in")
	}
	prefs := r.cfg.Prefs()
	enabled := !prefs.AnimationsEnabled()
	prefs.Animations = &enabled
	r.cfg.Preferences = &prefs
	if r.cfgPath != "" {
		if err := config.Save(r.cfgPath, r.cfg); err != nil {
			return r, r.setFlash("could not save animations: " + err.Error())
		}
	}
	state := "off"
	if enabled {
		state = "on"
	}
	return r, tea.Batch(r.setFlash("animations "+state), r.animCmd())
}

// runTitle is the terminal-title (OSC) content for the run view
// (DESIGN §15.3): `llamaman — <alias> [STATE]`. Router sessions use
// the models-file basename as the alias.
func (r *RunMode) runTitle(state string) string {
	alias := r.model.Alias
	if alias == "" && r.routerFile != "" {
		alias = filepath.Base(r.routerFile)
	}
	return fmt.Sprintf("llamaman — %s [%s]", alias, state)
}

// badgeColor is statusColor with the §15.5 animations applied: the
// STARTING badge breathes yellow↔gold, the READY dot pulses while
// generating, and READY/ERROR get a one-shot glow/flash on entry. All
// gated by preferences.animations.
func (r *RunMode) badgeColor() lipgloss.Color {
	base := r.statusColor()
	if !animationsEnabled(r.cfg) {
		return base
	}
	switch r.status {
	case StatusStarting:
		return animColor(base, lighten(base, 0.25), 2000*time.Millisecond)
	case StatusReady:
		if s := oneShotStrength(r.readyAt, readyGlowDur); s > 0 {
			return oneShotColor(base, s)
		}
		if r.busyCount > 0 {
			return animColor(base, lighten(base, 0.3), 800*time.Millisecond)
		}
	case StatusErrored:
		if s := oneShotStrength(r.errAt, errFlashDur); s > 0 {
			return oneShotColor(base, s)
		}
	}
	return base
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
