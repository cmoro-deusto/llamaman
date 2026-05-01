package tui

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// firstRunStage tracks which step of the first-run flow we're in.
type firstRunStage int

const (
	frWelcome firstRunStage = iota
	frGlobals
	frDone
)

// FirstRunCompletedMsg is sent when the first-run flow has written the
// initial config.json and the user is ready to proceed into config mode.
type FirstRunCompletedMsg struct {
	Cfg     *config.Config
	CfgPath string
}

// FirstRunQuitMsg is sent when the user quits the first-run flow before
// writing config.
type FirstRunQuitMsg struct{}

// FirstRunMode is the welcome → globals → save sequence from DESIGN.md §8.
type FirstRunMode struct {
	cfgPath string

	stage firstRunStage
	form  *huh.Form

	bin, host, port string

	saveErr error
	flash   string

	width, height int
	theme         Theme
}

// NewFirstRunMode constructs the flow rooted at the given config path.
// The path is resolved by main.go (XDG default) before this is reached.
func NewFirstRunMode(cfgPath string) *FirstRunMode {
	return &FirstRunMode{
		cfgPath: cfgPath,
		stage:   frWelcome,
		bin:     autodetectBinary(),
		host:    "127.0.0.1",
		port:    "9080",
		theme:   CurrentTheme(),
	}
}

// SetSize tracks terminal dimensions for layout.
func (f *FirstRunMode) SetSize(w, h int) { f.width, f.height = w, h }

func (f *FirstRunMode) Init() tea.Cmd { return nil }

func (f *FirstRunMode) Update(msg tea.Msg) (*FirstRunMode, tea.Cmd) {
	switch f.stage {
	case frWelcome:
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "enter":
				f.stage = frGlobals
				f.form = f.buildGlobalsForm()
				return f, f.form.Init()
			case "q", "esc", "ctrl+c":
				return f, func() tea.Msg { return FirstRunQuitMsg{} }
			}
		}
		return f, nil
	case frGlobals:
		if f.form == nil {
			return f, nil
		}
		if k, ok := msg.(tea.KeyMsg); ok && k.String() == "esc" {
			// Treat Esc as "Quit setup". Tighter prompt (y/N) lands in
			// Phase 10 polish; for now we just bail.
			return f, func() tea.Msg { return FirstRunQuitMsg{} }
		}
		model, cmd := f.form.Update(msg)
		if ff, ok := model.(*huh.Form); ok {
			f.form = ff
		}
		if f.form != nil && f.form.State == huh.StateCompleted {
			return f.finalizeGlobals()
		}
		return f, cmd
	}
	return f, nil
}

func (f *FirstRunMode) buildGlobalsForm() *huh.Form {
	return huh.NewForm(huh.NewGroup(
		huh.NewInput().Title("llama-server binary").Value(&f.bin),
		huh.NewInput().Title("host (IPv4 / [::IPv6] / hostname)").Value(&f.host),
		huh.NewInput().Title("port").Value(&f.port).Validate(numericRange(1, 65535)),
	)).WithTheme(huh.ThemeBase())
}

func (f *FirstRunMode) finalizeGlobals() (*FirstRunMode, tea.Cmd) {
	port, err := strconv.Atoi(strings.TrimSpace(f.port))
	if err != nil {
		f.flash = "invalid port"
		f.form = f.buildGlobalsForm()
		return f, f.form.Init()
	}
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Globals: config.Globals{
			Bin:  strings.TrimSpace(f.bin),
			Host: strings.TrimSpace(f.host),
			Port: port,
		},
		Models: []config.Model{},
	}
	if err := config.Save(f.cfgPath, cfg); err != nil {
		f.saveErr = err
		f.flash = fmt.Sprintf("save failed: %v", err)
		// Re-open the form so the user can retry.
		f.form = f.buildGlobalsForm()
		return f, f.form.Init()
	}
	f.stage = frDone
	return f, func() tea.Msg { return FirstRunCompletedMsg{Cfg: cfg, CfgPath: f.cfgPath} }
}

func (f *FirstRunMode) View() string {
	if f.width == 0 || f.height == 0 {
		return ""
	}
	switch f.stage {
	case frWelcome:
		return f.renderWelcome()
	case frGlobals:
		title := lipgloss.NewStyle().Foreground(f.theme.Accent).Bold(true).Render("First-time setup")
		desc := lipgloss.NewStyle().Foreground(f.theme.Subtle).
			Render("Configure llama-server defaults. You can change these later.")
		flash := ""
		if f.flash != "" {
			flash = lipgloss.NewStyle().Foreground(f.theme.StatusErr).Render(f.flash)
		}
		body := lipgloss.JoinVertical(lipgloss.Left, title, desc, "", f.form.View(), flash)
		box := lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(f.theme.Accent).
			Padding(1, 3).
			Render(body)
		return lipgloss.Place(f.width, f.height, lipgloss.Center, lipgloss.Center, box)
	case frDone:
		return ""
	}
	return ""
}

func (f *FirstRunMode) renderWelcome() string {
	wordmark := lipgloss.NewStyle().Foreground(f.theme.Accent).
		Render(strings.TrimRight(Wordmark, "\n"))
	body := lipgloss.JoinVertical(lipgloss.Center,
		wordmark,
		"",
		lipgloss.NewStyle().Foreground(f.theme.Subtle).Render("No configuration found."),
		"llamaman will guide you through setup.",
		"",
		shortcut("Enter", "begin", f.theme)+"   "+shortcut("q", "quit", f.theme),
	)
	return lipgloss.Place(f.width, f.height, lipgloss.Center, lipgloss.Center, body)
}

// autodetectBinary applies the search list from DESIGN.md §8: which
// llama-server first, then a couple of common install paths. Returns ""
// if nothing matches; the user can fill it in manually.
func autodetectBinary() string {
	if p, err := exec.LookPath("llama-server"); err == nil {
		return p
	}
	candidates := []string{
		"/usr/local/bin/llama-server",
		"/usr/local/llama.cpp/bin/llama-server",
		"/opt/llama.cpp/bin/llama-server",
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && info.Mode()&0o111 != 0 {
			return c
		}
	}
	return ""
}
