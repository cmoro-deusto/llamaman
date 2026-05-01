package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// paramPicker is the custom new-param key chooser used by configuration
// mode. It renders the registry as a bubbles/list with name + parsed
// help description so the user can see what each flag does before
// picking it. Filtering is enabled (`/` to start typing).
//
// huh.Select doesn't support per-option descriptions; rather than work
// around it with truncated multi-line keys, we use bubbles/list directly
// and feed the chosen key back to ConfigMode via paramPickerDoneMsg.
type paramPicker struct {
	list   list.Model
	width  int
	height int
}

// paramPickerItem is a list.Item carrying a flag's display fields.
type paramPickerItem struct {
	name        string
	kind        string
	description string
}

func (p paramPickerItem) Title() string       { return p.name }
func (p paramPickerItem) Description() string { return p.summary() }
func (p paramPickerItem) FilterValue() string { return p.name }

// summary combines the value-kind hint with the parsed help description.
// Format: "(kind) description". Kind is parenthesized so users can
// scan-for-bool / scan-for-numeric.
func (p paramPickerItem) summary() string {
	if p.kind != "" && p.description != "" {
		return p.kind + " " + p.description
	}
	if p.kind != "" {
		return p.kind
	}
	return p.description
}

// paramPickerDoneMsg fires when the user picks a key (or an empty key
// for the free-text fallback). Cancellation goes through Esc → no msg.
type paramPickerDoneMsg struct {
	key       string
	cancelled bool
}

// newParamPicker constructs the picker. registry may be nil to fall back
// to a single free-text input wrapped in the same picker shape; in that
// case the picker is still navigable but contains only an "(other)" row.
func newParamPicker(reg flags.Registry) *paramPicker {
	items := make([]list.Item, 0, len(reg)+1)
	for _, name := range reg.Names() {
		fi := reg[name]
		items = append(items, paramPickerItem{
			name:        name,
			kind:        kindLabel(fi),
			description: fi.Description,
		})
	}
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	l := list.New(items, delegate, 0, 0)
	// Hide the title bar entirely — the bordered box + footer hint
	// provide context. The filter input renders inline when active so
	// the picker stays a single, unified surface (no separate "filter
	// window" feel).
	l.SetShowTitle(false)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.SetShowPagination(true)
	l.SetFilteringEnabled(true)
	return &paramPicker{list: l}
}

// SetSize configures the picker dimensions.
func (p *paramPicker) SetSize(w, h int) {
	p.width, p.height = w, h
	p.list.SetSize(w, h)
}

// Update routes keys for the picker.
//
// When the list is in Filtering mode, every key (including Enter, which
// confirms the filter) goes to the list. In Unfiltered mode, pressing a
// printable rune transparently switches the list into Filtering mode
// and forwards the rune — so the user can just start typing instead of
// having to press `/` first. Enter without filter activity selects the
// current row; Esc cancels the picker.
func (p *paramPicker) Update(msg tea.Msg) (*paramPicker, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		if p.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			p.list, cmd = p.list.Update(msg)
			return p, cmd
		}
		switch k.String() {
		case "esc":
			return p, func() tea.Msg { return paramPickerDoneMsg{cancelled: true} }
		case "enter":
			if it, ok := p.list.SelectedItem().(paramPickerItem); ok {
				key := it.name
				return p, func() tea.Msg { return paramPickerDoneMsg{key: key} }
			}
			return p, nil
		}
		if isPrintableRune(k) {
			p.list.SetFilterState(list.Filtering)
			var cmd tea.Cmd
			p.list, cmd = p.list.Update(msg)
			return p, cmd
		}
	}
	var cmd tea.Cmd
	p.list, cmd = p.list.Update(msg)
	return p, cmd
}

// isPrintableRune reports whether a key event represents a single
// printable character — i.e., the kind of key that should kick off
// filter mode in the picker. Excludes navigation keys (arrows, tab,
// enter, esc, etc.) which are routed by the switch above.
func isPrintableRune(k tea.KeyMsg) bool {
	if k.Type != tea.KeyRunes {
		return false
	}
	if len(k.Runes) != 1 {
		return false
	}
	r := k.Runes[0]
	if r < 0x20 || r == 0x7f {
		return false
	}
	return true
}

// View renders the picker as a bordered box. It's overlaid by ConfigMode
// over the three-pane background.
func (p *paramPicker) View(theme Theme) string {
	body := p.list.View()
	hint := lipgloss.NewStyle().Foreground(theme.Subtle).
		Render("type to filter · ↑↓: navigate · enter: pick · esc: cancel")
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2)
	return box.Render(fmt.Sprintf("%s\n%s", body, hint))
}

// renderEmpty shows a placeholder when the registry is empty (e.g.,
// llama-server isn't installed yet) so the user knows what's happening.
//
//nolint:unused
func (p *paramPicker) renderEmpty(theme Theme) string {
	box := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(theme.Accent).
		Padding(1, 2)
	return box.Render(strings.Join([]string{
		"No flag registry available.",
		"Type a flag name in the next form (free-text accepted).",
	}, "\n"))
}
