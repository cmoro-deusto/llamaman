package tui

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
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

	proc       *server.Process
	tail       *server.Tailer
	sessionMgr *server.SessionManager

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
	flash         string

	searchInput   textinput.Model
	searchActive  bool
	searchQuery   string
	searchMatches []int
	searchIdx     int

	// totalLines counts newlines we've ever rendered so we can display
	// "↓ N new lines" while the user is scrolled away from the bottom.
	totalLines      int
	lastSeenLines   int
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
		cfg:         opts.Cfg,
		model:       opts.Model,
		preset:      opts.Preset,
		argv:        opts.Argv,
		warnings:    opts.Warnings,
		proc:        opts.Process,
		tail:        tail,
		sessionMgr:  opts.SessionMgr,
		viewport:    vp,
		status:      StatusStarting,
		keys:        DefaultKeymap(),
		theme:       CurrentTheme(),
		searchInput: ti,
	}
	cmd := tea.Batch(
		waitForChunk(tail.Chunks()),
		waitForProc(opts.Process),
		tickUptime(),
	)
	return r, cmd, nil
}

// SetSize configures viewport dimensions. The top pane is fixed at 3 lines
// and the footer is 1 line; the viewport gets everything in between.
func (r *RunMode) SetSize(w, h int) {
	r.width, r.height = w, h
	r.viewport.Width = w
	if h > 4 {
		r.viewport.Height = h - 4
	} else {
		r.viewport.Height = 1
	}
}

// Update routes messages: log chunks, process exit, uptime tick, and key
// presses including the quit-prompt state machine.
func (r *RunMode) Update(msg tea.Msg) (*RunMode, tea.Cmd) {
	switch m := msg.(type) {
	case logChunkMsg:
		r.buf.WriteString(string(m))
		r.totalLines = strings.Count(r.buf.String(), "\n")
		if r.status == StatusStarting && strings.Contains(r.buf.String(), readyMarker) {
			r.status = StatusReady
		}
		atBottom := r.viewport.AtBottom()
		r.viewport.SetContent(r.buf.String())
		if atBottom {
			r.viewport.GotoBottom()
			r.lastSeenLines = r.totalLines
		}
		return r, waitForChunk(r.tail.Chunks())

	case tailerClosedMsg:
		return r, nil

	case procDoneMsg:
		if m.err == nil {
			r.status = StatusExited
		} else {
			r.status = StatusErrored
			r.startErr = m.err
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
		if r.searchActive {
			return r.handleSearchInput(m)
		}
		if r.showHelp {
			r.showHelp = false
			return r, nil
		}
		switch m.String() {
		case "q", "ctrl+c":
			r.showQuit = true
			return r, nil
		case "k":
			// Direct kill: stop llama-server, clean up, and return to
			// the main screen — llamaman itself stays open.
			return r, r.killAndReturn()
		case "?":
			r.showHelp = true
			return r, nil
		case "/":
			r.searchActive = true
			r.searchInput.SetValue("")
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
			r.lastSeenLines = r.totalLines
			return r, nil
		case " ", "space":
			r.viewport.HalfPageDown()
			if r.viewport.AtBottom() {
				r.lastSeenLines = r.totalLines
			}
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

// killAndReturn stops llama-server, cleans up the log + session, and
// returns to the main screen (without exiting llamaman). Used by both
// the direct `k` shortcut and the (k)ill option in the quit prompt so
// kill is consistently a "back to main" action.
func (r *RunMode) killAndReturn() tea.Cmd {
	r.proc.Stop(5 * time.Second)
	r.tail.Close()
	_ = r.proc.RemoveLog()
	if r.sessionMgr != nil {
		_ = r.sessionMgr.Clear()
	}
	return func() tea.Msg { return returnToMainMsg{} }
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

// handleSearchInput routes keystrokes while the search prompt is open.
func (r *RunMode) handleSearchInput(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "esc":
		r.searchActive = false
		r.searchInput.Blur()
		return r, nil
	case "enter":
		r.searchQuery = strings.TrimSpace(r.searchInput.Value())
		r.searchActive = false
		r.searchInput.Blur()
		r.recomputeMatches()
		r.jumpSearch(0)
		return r, nil
	}
	var cmd tea.Cmd
	r.searchInput, cmd = r.searchInput.Update(m)
	return r, cmd
}

// requestRestart kills the current process and emits a SpawnRequestMsg
// for the same (model, preset). The root will route it back through the
// spawner to start fresh.
func (r *RunMode) requestRestart() tea.Cmd {
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

// handleQuitPrompt runs the (k)ill / (d)etach / (c)ancel decision tree.
// (k)ill returns to the main screen — symmetric with the direct `k`
// shortcut. (d)etach exits llamaman and leaves llama-server running.
func (r *RunMode) handleQuitPrompt(m tea.KeyMsg) (*RunMode, tea.Cmd) {
	switch m.String() {
	case "k":
		r.showQuit = false
		return r, r.killAndReturn()
	case "d":
		// Leave the process and session.json intact; just unwind the TUI.
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
	bg := lipgloss.JoinVertical(lipgloss.Left, header, r.viewport.View(), footer)
	switch {
	case r.showQuit:
		return overlayCenter(bg, r.renderQuitPrompt(), r.width, r.height)
	case r.restartPrompt:
		return overlayCenter(bg, r.renderRestartPrompt(), r.width, r.height)
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

func (r *RunMode) renderHelp() string {
	keys := []string{
		"q / Ctrl+C  open quit prompt (k)ill / (d)etach / (c)ancel",
		"k           kill server and return to main",
		"r           restart server (confirm if ready)",
		"c           copy launch command to clipboard",
		"/           search forward",
		"n / N       next / previous match",
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
	indicator := ""
	if !r.viewport.AtBottom() && r.totalLines > r.lastSeenLines {
		n := r.totalLines - r.lastSeenLines
		indicator = lipgloss.NewStyle().Foreground(r.theme.Accent).
			Render(fmt.Sprintf("↓ %d new line%s — G to follow", n, pluralN(n)))
	}
	stack := []string{}
	if indicator != "" {
		stack = append(stack, indicator)
	}
	if r.flash != "" {
		stack = append(stack, lipgloss.NewStyle().Foreground(r.theme.StatusStart).Render(r.flash))
	}
	stack = append(stack, hint)
	return lipgloss.JoinVertical(lipgloss.Left, stack...)
}

func pluralN(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (r *RunMode) renderHeader() string {
	statusDot := lipgloss.NewStyle().Foreground(r.statusColor()).Render("●")
	statusText := lipgloss.NewStyle().Foreground(r.theme.Subtle).Render(r.statusLabel())
	uptime := lipgloss.NewStyle().Foreground(r.theme.Subtle).
		Render(formatUptime(time.Since(r.proc.Started)))

	identity := lipgloss.NewStyle().Bold(true).Foreground(r.theme.Accent).
		Render(fmt.Sprintf("%s / %s", r.model.Alias, presetNameOrDash(r.preset)))
	hostport := fmt.Sprintf("%s:%d", r.cfg.Globals.Host, r.cfg.Globals.Port)

	line1 := fmt.Sprintf("%s  %s  %s %s  uptime %s",
		identity, lipgloss.NewStyle().Foreground(r.theme.Subtle).Render(hostport),
		statusDot, statusText, uptime)

	line2 := lipgloss.NewStyle().Foreground(r.theme.Subtle).
		Render(condensedSummary(r.model.Location, r.preset))

	line3 := lipgloss.NewStyle().Foreground(r.theme.Muted).
		Render(booleanSummary(r.preset))
	if len(r.warnings) > 0 {
		warn := lipgloss.NewStyle().Foreground(r.theme.StatusStart).
			Render("warn: " + strings.Join(r.warnings, "; "))
		if line3 == "" {
			line3 = warn
		} else {
			line3 = line3 + "  " + warn
		}
	}
	if r.startErr != nil {
		line3 = lipgloss.NewStyle().Foreground(r.theme.StatusErr).
			Render(fmt.Sprintf("error: %v", r.startErr))
	}

	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(r.theme.Border).
		Width(r.width - 2)
	return box.Render(strings.Join([]string{line1, line2, line3}, "\n"))
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

// condensedSummary picks a few highlights for line 2 of the header.
func condensedSummary(location string, preset config.Preset) string {
	parts := []string{filepath.Base(location)}
	for _, key := range []string{"ngl", "ctx-size", "fa", "ctk", "ctv"} {
		if v, ok := preset.Params.Get(key); ok {
			parts = append(parts, fmt.Sprintf("%s=%v", key, v))
		}
	}
	return strings.Join(parts, "  ")
}

// booleanSummary lists the param keys whose value is `true`.
func booleanSummary(preset config.Preset) string {
	var bools []string
	for _, p := range preset.Params {
		if v, ok := p.Value.(bool); ok && v {
			bools = append(bools, p.Key)
		}
	}
	if len(bools) == 0 {
		return ""
	}
	return strings.Join(bools, " ")
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
