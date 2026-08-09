package tui

// Model-card markdown rendering (DESIGN §16.7): the card panel shows
// the README as styled text instead of raw markdown. goldmark parses
// (GFM extension on); this custom renderer emits lipgloss-styled lines
// rather than HTML — headings accent-bold, strong bold, emphasis
// italic, code Muted, links accent-underline, list items bulleted,
// blockquotes quoted, code blocks Muted, thematic breaks as lines,
// tables cell-separated. Raw HTML (badge soup) is skipped.

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/util"
)

// cardRenderer is the goldmark NodeRenderer that writes styled text
// into its own builder (state lives on the instance, which is created
// fresh per render).
type cardRenderer struct {
	theme  Theme
	buf    strings.Builder
	styles []func(string) string
}

func (r *cardRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(ast.KindDocument, r.renderChildren)
	reg.Register(ast.KindParagraph, r.renderParagraph)
	reg.Register(ast.KindHeading, r.renderHeading)
	reg.Register(ast.KindText, r.renderText)
	reg.Register(ast.KindString, r.renderSkip) // inline raw HTML
	reg.Register(ast.KindCodeSpan, r.renderCodeSpan)
	reg.Register(ast.KindCodeBlock, r.renderCodeBlock)
	reg.Register(ast.KindFencedCodeBlock, r.renderCodeBlock)
	reg.Register(ast.KindEmphasis, r.renderEmphasis)
	reg.Register(ast.KindLink, r.renderLink)
	reg.Register(ast.KindImage, r.renderImage)
	reg.Register(ast.KindAutoLink, r.renderAutoLink)
	reg.Register(ast.KindList, r.renderChildren)
	reg.Register(ast.KindListItem, r.renderListItem)
	reg.Register(ast.KindBlockquote, r.renderBlockquote)
	reg.Register(ast.KindThematicBreak, r.renderThematicBreak)
	reg.Register(ast.KindHTMLBlock, r.renderSkip)
	reg.Register(ast.KindRawHTML, r.renderSkip)
	// GFM (table + task-list kinds live in extension/ast)
	reg.Register(extast.KindTable, r.renderChildren)
	reg.Register(extast.KindTableHeader, r.renderTableRow)
	reg.Register(extast.KindTableRow, r.renderTableRow)
	reg.Register(extast.KindTableCell, r.renderTableCell)
	reg.Register(extast.KindTaskCheckBox, r.renderTaskCheckBox)
}

// ---- node funcs ----

func (r *cardRenderer) renderChildren(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderParagraph(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		r.buf.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderHeading(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.push(func(s string) string { return lipgloss.NewStyle().Foreground(r.theme.Accent).Bold(true).Render(s) })
	} else {
		r.pop()
		r.buf.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderText(_ util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		return ast.WalkContinue, nil
	}
	text := string(n.(*ast.Text).Segment.Value(source))
	// Soft line breaks become spaces (goldmark's default behavior).
	r.write(strings.ReplaceAll(text, "\n", " "))
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderCodeSpan(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.push(func(s string) string { return lipgloss.NewStyle().Foreground(r.theme.Muted).Render(s) })
	} else {
		r.pop()
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderCodeBlock(_ util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		style := lipgloss.NewStyle().Foreground(r.theme.Muted)
		lines := n.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			r.buf.WriteString(style.Render(strings.TrimRight(string(seg.Value(source)), "\n")) + "\n")
		}
		r.buf.WriteString("\n")
	}
	return ast.WalkSkipChildren, nil
}

func (r *cardRenderer) renderEmphasis(_ util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		if n.(*ast.Emphasis).Level == 2 {
			r.push(func(s string) string { return lipgloss.NewStyle().Bold(true).Render(s) })
		} else {
			r.push(func(s string) string { return lipgloss.NewStyle().Italic(true).Render(s) })
		}
	} else {
		r.pop()
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderLink(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.push(func(s string) string { return lipgloss.NewStyle().Foreground(r.theme.Accent).Underline(true).Render(s) })
	} else {
		r.pop()
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderImage(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	// Alt text in Muted; the image itself cannot render in a TUI.
	if entering {
		r.push(func(s string) string { return lipgloss.NewStyle().Foreground(r.theme.Muted).Render(s) })
	} else {
		r.pop()
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderAutoLink(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.write(lipgloss.NewStyle().Foreground(r.theme.Accent).Underline(true).Render(string(n.(*ast.AutoLink).URL(source))))
	}
	return ast.WalkSkipChildren, nil
}

func (r *cardRenderer) renderListItem(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.write("• ")
	} else {
		r.buf.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderBlockquote(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.write("▍ ") // prefix once; the muted style covers the chunks
		r.push(func(s string) string { return lipgloss.NewStyle().Foreground(r.theme.Muted).Render(s) })
	} else {
		r.pop()
		r.buf.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderThematicBreak(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		r.buf.WriteString(lipgloss.NewStyle().Foreground(r.theme.Muted).Render("────") + "\n")
	}
	return ast.WalkSkipChildren, nil
}

func (r *cardRenderer) renderSkip(_ util.BufWriter, _ []byte, _ ast.Node, _ bool) (ast.WalkStatus, error) {
	return ast.WalkSkipChildren, nil
}

func (r *cardRenderer) renderTableRow(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		r.buf.WriteString("\n")
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderTableCell(_ util.BufWriter, _ []byte, _ ast.Node, entering bool) (ast.WalkStatus, error) {
	if !entering {
		r.buf.WriteString(" │ ")
	}
	return ast.WalkContinue, nil
}

func (r *cardRenderer) renderTaskCheckBox(_ util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	if entering {
		mark := "[ ]"
		if n.(*extast.TaskCheckBox).IsChecked {
			mark = "[x]"
		}
		r.write(mark + " ")
	}
	return ast.WalkContinue, nil
}

// ---- style stack + entry point ----

func (r *cardRenderer) push(f func(string) string) { r.styles = append(r.styles, f) }
func (r *cardRenderer) pop() {
	if len(r.styles) > 0 {
		r.styles = r.styles[:len(r.styles)-1]
	}
}

func (r *cardRenderer) write(s string) {
	for i := len(r.styles) - 1; i >= 0; i-- {
		s = r.styles[i](s)
	}
	r.buf.WriteString(s)
}

// renderCardMarkdown parses and renders markdown into styled card
// lines. Returns nil on parse/render failure (the caller shows its
// friendly absence note).
func renderCardMarkdown(theme Theme, src []byte) []string {
	cr := &cardRenderer{theme: theme}
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithRenderer(renderer.NewRenderer(
			renderer.WithNodeRenderers(util.Prioritized(cr, 1000)),
		)),
	)
	if err := md.Convert(src, &cr.buf); err != nil {
		return nil
	}
	text := strings.TrimRight(cr.buf.String(), "\n")
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		// Drop pure-whitespace lines (paragraph spacing) — the card
		// panel is dense; blank lines eat its scarce height.
		// lipgloss.Width is ANSI-aware, so styled-empty lines count 0.
		if lipgloss.Width(ln) == 0 {
			continue
		}
		out = append(out, ln)
	}
	return out
}
