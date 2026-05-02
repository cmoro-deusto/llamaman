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
		serverVersion: loadServerVersion(opts.Cfg.Globals.Bin),
		fetcher:       opts.Fetcher,
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
	}
	return r, tea.Batch(cmds...), nil
}

// wordmarkMinWidth is the minimum terminal width at which the
// run-mode header shows the llamaman wordmark on the left side. Below
// this threshold the wordmark would be truncated mid-letter, so we
// fall back to the compact info-only layout instead.
const wordmarkMinWidth = 110

// SetSize configures viewport dimensions. Chrome above the viewport
// is the bordered top box (10 or 6 rows depending on whether the
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
// viewport content: top box (10 with wordmark / 6 without) + log
// box's 2 borders + 1 footer.
func (r *RunMode) chromeHeight() int {
	const logBoxBorders = 2
	const footerHeight = 1
	top := headerHeight
	if r.width >= wordmarkMinWidth {
		top = headerHeightWithWordmark
	}
	return top + logBoxBorders + footerHeight
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
		// is reachable. Fire the fetch alongside the next chunk wait. The
		// reattach path already kicked off in NewRunMode.
		if wasStarting && r.status == StatusReady && r.fetcher != nil && r.liveCtxSize == 0 {
			cmds = append(cmds, fetchPropsCmd(r.fetchCtx, r.fetcher))
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

func (r *RunMode) renderHelp() string {
	keys := []string{
		"q / Ctrl+C  open quit prompt (k)ill / (d)etach / (c)ancel",
		"k           kill server and return to main",
		"r           restart server (confirm if ready)",
		"c           copy launch command to clipboard",
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
	parts := []string{"q: quit  k: kill  r: restart  c: copy  /: search  ?: help  g/G: top/bottom  space/b: page  ↑/↓: scroll"}
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
// + 6 wordmark rows + 1 empty padding + 1 bottom border = 10 rows.
const headerHeightWithWordmark = 10

// wordmarkLines is the number of lines in the embedded Wordmark asset
// (72×6). Exposed as a constant so layout math is explicit; the
// renderer also vertically centers the 2 info content rows inside
// this many rows on the right side of the box.
const wordmarkLines = 6

func (r *RunMode) renderHeader() string {
	params := canonicalParams(r.preset, r.registry)

	subtle := lipgloss.NewStyle().Foreground(r.theme.Subtle)
	accent := lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true)

	row1 := strings.Join([]string{
		subtle.Render("Alias:") + " " + accent.Render(r.model.Alias),
		subtle.Render("Server:") + " " + serverVersionOrNA(r.serverVersion),
		subtle.Render("Context Size:") + " " + ctxSizeDisplay(r.liveCtxSize, params),
		subtle.Render("Uptime:") + " " + formatUptime(time.Since(r.proc.Started)),
		statusBadge(r.statusLabel(), r.statusColor()),
		metricsIndicator(params, r.theme),
	}, "   ")

	row2 := strings.Join([]string{
		subtle.Render("Preset:") + " " + accent.Render(presetNameOrDash(r.preset)),
		subtle.Render("Temp:") + " " + paramOrNA(params, "temp"),
		subtle.Render("Top_P:") + " " + paramOrNA(params, "top-p"),
		subtle.Render("Top_K:") + " " + paramOrNA(params, "top-k"),
		subtle.Render("Min_P:") + " " + paramOrNA(params, "min-p"),
	}, "   ")

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Border).
		Padding(0, 1).
		Width(r.width - 2)

	if r.width < wordmarkMinWidth {
		// Compact 6-row layout: rows truncated to inner width, sandwiched
		// by empty padding rows. Same as v0.3.0.
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

	// Wide layout: wordmark column on the left (subtle) + vertically
	// centered info rows on the right. The right column matches the
	// wordmark's height so JoinHorizontal lines them up cleanly.
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
	row1 = ansi.Truncate(row1, rightWidth, "")
	row2 = ansi.Truncate(row2, rightWidth, "")

	// Vertically center the 2 content rows inside `wordmarkLines`
	// rows: 2 blank top + row1 + row2 + 2 blank bottom = 6 rows.
	rightCol := strings.Join([]string{"", "", row1, row2, "", ""}, "\n")

	twoColumn := lipgloss.JoinHorizontal(lipgloss.Top, wordmark, "  ", rightCol)
	body := strings.Join([]string{"", twoColumn, ""}, "\n")
	return box.Render(body)
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

// metricsOn / metricsOff are the SGR sequences used by the
// [Metrics] indicator: black foreground on green background when the
// preset has `metrics: true` (the "lit-button" look the user asked
// for); a full reset closes the run. The bold modifier was dropped
// because terminals tend to render bold-black as gray, washing out
// the text against the green background — pure-black sticks. Literal
// SGR rather than lipgloss so the styling is deterministic in tests
// where lipgloss suppresses styling without a TTY.
const (
	metricsOnOpen  = "\x1b[30;42m"
	metricsOnClose = "\x1b[0m"
)

// statusBadge renders the run-mode status indicator that sits between
// Uptime and [Metrics] on row 1. Bracketed, uppercase, bold, with the
// state's themed foreground color and no background fill — chosen
// over the [Metrics]-style black-on-color treatment because the
// status palette spans 4 themed colors that double as foregrounds in
// light mode (where black-on-dark-red would be unreadable).
func statusBadge(label string, color lipgloss.Color) string {
	return lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render("[" + strings.ToUpper(label) + "]")
}

// metricsIndicator renders the [Metrics] tag at the end of row 1.
// Bold black-on-green when the preset has `metrics: true`; subtle/dim
// otherwise. Always shown so the indicator's spot is stable across
// state changes.
func metricsIndicator(params map[string]any, theme Theme) string {
	on := false
	if v, ok := params["metrics"]; ok {
		if b, isBool := v.(bool); isBool && b {
			on = true
		}
	}
	if on {
		return metricsOnOpen + "[Metrics]" + metricsOnClose
	}
	return lipgloss.NewStyle().Foreground(theme.Muted).Render("[Metrics]")
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
