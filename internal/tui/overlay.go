package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// overlayCenter pastes popup over background, centered in (totalW x totalH).
// Both inputs may contain ANSI escapes; ansi.Truncate / ansi.TruncateLeft
// keep the SGR state intact when slicing background lines around the popup.
//
// Used by run-mode and config-mode to render dialogs without nuking the
// underlying screen content (DESIGN.md doesn't prescribe but our user
// asked for it explicitly).
func overlayCenter(background, popup string, totalW, totalH int) string {
	if popup == "" {
		return background
	}
	bgLines := strings.Split(background, "\n")
	popLines := strings.Split(popup, "\n")
	popH := len(popLines)
	popW := 0
	for _, l := range popLines {
		if w := ansi.StringWidth(l); w > popW {
			popW = w
		}
	}
	rowOff := (totalH - popH) / 2
	if rowOff < 0 {
		rowOff = 0
	}
	colOff := (totalW - popW) / 2
	if colOff < 0 {
		colOff = 0
	}

	for i, popLine := range popLines {
		row := rowOff + i
		// If background is shorter than the target row, extend it with
		// blank lines so the overlay still lands at the right place.
		for row >= len(bgLines) {
			bgLines = append(bgLines, "")
		}
		bgLine := bgLines[row]
		left := ansi.Truncate(bgLine, colOff, "")
		leftWidth := ansi.StringWidth(left)
		if leftWidth < colOff {
			left += strings.Repeat(" ", colOff-leftWidth)
		}
		right := ansi.TruncateLeft(bgLine, colOff+popW, "")
		// Pad popup line to popW so any background to the right doesn't
		// bleed into the box.
		popPadded := popLine
		if w := ansi.StringWidth(popLine); w < popW {
			popPadded += strings.Repeat(" ", popW-w)
		}
		bgLines[row] = left + popPadded + right
	}
	return strings.Join(bgLines, "\n")
}
