// Package-level theme system: the curated palette table (DESIGN §15.1),
// the resolver, and the compatibility/cycle helpers. The Theme struct
// itself lives in common.go (zones.go and the live bars consume it).
package tui

import "github.com/charmbracelet/lipgloss"

// Background classifies where a palette is meant to be used. The
// compatibility filter (P1) offers a palette only when it is adaptive
// or matches the detected terminal background.
type Background int

const (
	// BackgroundAdaptive palettes (only llamaman) carry both a dark and
	// a light variant and are compatible with any terminal.
	BackgroundAdaptive Background = iota
	BackgroundDark
	BackgroundLight
)

// Palette is one entry in the palette table: a stable ID (the value
// stored in preferences.theme), a display name, a background mode, and
// the 10 color fields the TUI renders with.
type Palette struct {
	ID         string
	Display    string
	Background Background
	T          Theme
}

// hex is a shorthand for a lipgloss color literal.
func hex(s string) lipgloss.Color { return lipgloss.Color(s) }

// palettes is the curated table. Colors are each theme's canonical
// values (owner-approved set, DESIGN §15.1); every field carries its
// nearest xterm-256 index (P1 discipline, §10.4) computed with the
// standard 6x6x6-cube + grayscale approximation — ties resolve to the
// lower index. Actual rendering conversion is lipgloss/termenv's.
var palettes = []Palette{
	{"llamaman", "llamaman (default)", BackgroundAdaptive, Theme{
		Accent:        hex("#E8A33D"), // 256 ≈ 179
		SegmentPrompt: hex("#9B59B6"), // 256 ≈ 97
		SegmentGen:    hex("#FF8C00"), // 256 ≈ 208
		Subtle:        hex("#9A9A9A"), // 256 ≈ 247
		Muted:         hex("#5C5C5C"), // 256 ≈ 59
		StatusIdle:    hex("#87CEEB"), // 256 ≈ 116
		StatusReady:   hex("#73D216"), // 256 ≈ 76
		StatusStart:   hex("#FFD700"), // 256 ≈ 220
		StatusErr:     hex("#FF6B6B"), // 256 ≈ 203
		StatusGone:    hex("#7C7C7C"), // 256 ≈ 244
		BorderFocus:   hex("#E8A33D"), // 256 ≈ 179
		Border:        hex("#444444"), // 256 ≈ 238
	}},
	{"catppuccin-mocha", "Catppuccin Mocha", BackgroundDark, Theme{
		Accent:        hex("#FAB387"), // 256 ≈ 216
		SegmentPrompt: hex("#CBA6F7"), // 256 ≈ 183
		SegmentGen:    hex("#FAB387"), // 256 ≈ 216
		Subtle:        hex("#A6ADC8"), // 256 ≈ 146
		Muted:         hex("#6C7086"), // 256 ≈ 243
		StatusIdle:    hex("#89B4FA"), // 256 ≈ 111
		StatusReady:   hex("#A6E3A1"), // 256 ≈ 151
		StatusStart:   hex("#F9E2AF"), // 256 ≈ 223
		StatusErr:     hex("#F38BA8"), // 256 ≈ 211
		StatusGone:    hex("#6C7086"), // 256 ≈ 243
		BorderFocus:   hex("#FAB387"), // 256 ≈ 216
		Border:        hex("#45475A"), // 256 ≈ 239
	}},
	{"catppuccin-latte", "Catppuccin Latte", BackgroundLight, Theme{
		Accent:        hex("#FE640B"), // 256 ≈ 202
		SegmentPrompt: hex("#8839EF"), // 256 ≈ 99
		SegmentGen:    hex("#FE640B"), // 256 ≈ 202
		Subtle:        hex("#6C6F85"), // 256 ≈ 243
		Muted:         hex("#9CA0B0"), // 256 ≈ 248
		StatusIdle:    hex("#1E66F5"), // 256 ≈ 27
		StatusReady:   hex("#40A02B"), // 256 ≈ 70
		StatusStart:   hex("#DF8E1D"), // 256 ≈ 172
		StatusErr:     hex("#D20F39"), // 256 ≈ 161
		StatusGone:    hex("#9CA0B0"), // 256 ≈ 248
		BorderFocus:   hex("#FE640B"), // 256 ≈ 202
		Border:        hex("#BCC0CC"), // 256 ≈ 251
	}},
	{"tokyo-night", "Tokyo Night", BackgroundDark, Theme{
		Accent:        hex("#7AA2F7"), // 256 ≈ 111
		SegmentPrompt: hex("#BB9AF7"), // 256 ≈ 141
		SegmentGen:    hex("#FF9E64"), // 256 ≈ 215
		Subtle:        hex("#A9B1D6"), // 256 ≈ 146
		Muted:         hex("#565F89"), // 256 ≈ 60
		StatusIdle:    hex("#7DCFFF"), // 256 ≈ 117
		StatusReady:   hex("#9ECE6A"), // 256 ≈ 149
		StatusStart:   hex("#E0AF68"), // 256 ≈ 179
		StatusErr:     hex("#F7768E"), // 256 ≈ 210
		StatusGone:    hex("#565F89"), // 256 ≈ 60
		BorderFocus:   hex("#7AA2F7"), // 256 ≈ 111
		Border:        hex("#292E42"), // 256 ≈ 236
	}},
	{"tokyo-night-day", "Tokyo Night Day", BackgroundLight, Theme{
		Accent:        hex("#2E7DE9"), // 256 ≈ 32
		SegmentPrompt: hex("#9854F1"), // 256 ≈ 99
		SegmentGen:    hex("#B15C00"), // 256 ≈ 130
		Subtle:        hex("#848CB5"), // 256 ≈ 103
		Muted:         hex("#C4C8DA"), // 256 ≈ 252
		StatusIdle:    hex("#007197"), // 256 ≈ 24
		StatusReady:   hex("#587539"), // 256 ≈ 240
		StatusStart:   hex("#8C6C3E"), // 256 ≈ 95
		StatusErr:     hex("#F52A65"), // 256 ≈ 197
		StatusGone:    hex("#848CB5"), // 256 ≈ 103
		BorderFocus:   hex("#2E7DE9"), // 256 ≈ 32
		Border:        hex("#C4C8DA"), // 256 ≈ 252
	}},
	{"dracula", "Dracula", BackgroundDark, Theme{
		Accent:        hex("#BD93F9"), // 256 ≈ 141
		SegmentPrompt: hex("#BD93F9"), // 256 ≈ 141
		SegmentGen:    hex("#FFB86C"), // 256 ≈ 215
		Subtle:        hex("#6272A4"), // 256 ≈ 61
		Muted:         hex("#44475A"), // 256 ≈ 239
		StatusIdle:    hex("#8BE9FD"), // 256 ≈ 117
		StatusReady:   hex("#50FA7B"), // 256 ≈ 84
		StatusStart:   hex("#F1FA8C"), // 256 ≈ 228
		StatusErr:     hex("#FF5555"), // 256 ≈ 203
		StatusGone:    hex("#44475A"), // 256 ≈ 239
		BorderFocus:   hex("#BD93F9"), // 256 ≈ 141
		Border:        hex("#44475A"), // 256 ≈ 239
	}},
	{"dracula-light", "Dracula (light)", BackgroundLight, Theme{
		Accent:        hex("#644AC9"), // 256 ≈ 62
		SegmentPrompt: hex("#6C4FBF"), // 256 ≈ 61
		SegmentGen:    hex("#C07A1E"), // 256 ≈ 136
		Subtle:        hex("#6C664B"), // 256 ≈ 59
		Muted:         hex("#CFCFDE"), // 256 ≈ 188
		StatusIdle:    hex("#036A96"), // 256 ≈ 24
		StatusReady:   hex("#14710A"), // 256 ≈ 22
		StatusStart:   hex("#846E15"), // 256 ≈ 94
		StatusErr:     hex("#CB3A2A"), // 256 ≈ 166
		StatusGone:    hex("#CFCFDE"), // 256 ≈ 188
		BorderFocus:   hex("#644AC9"), // 256 ≈ 62
		Border:        hex("#DEDCCF"), // 256 ≈ 188
	}},
	{"gruvbox-dark", "Gruvbox (dark)", BackgroundDark, Theme{
		Accent:        hex("#FE8019"), // 256 ≈ 208
		SegmentPrompt: hex("#D3869B"), // 256 ≈ 174
		SegmentGen:    hex("#FE8019"), // 256 ≈ 208
		Subtle:        hex("#D5C4A1"), // 256 ≈ 187
		Muted:         hex("#928374"), // 256 ≈ 244
		StatusIdle:    hex("#83A598"), // 256 ≈ 108
		StatusReady:   hex("#B8BB26"), // 256 ≈ 142
		StatusStart:   hex("#FABD2F"), // 256 ≈ 214
		StatusErr:     hex("#FB4934"), // 256 ≈ 203
		StatusGone:    hex("#928374"), // 256 ≈ 244
		BorderFocus:   hex("#FE8019"), // 256 ≈ 208
		Border:        hex("#504945"), // 256 ≈ 239
	}},
	{"gruvbox-light", "Gruvbox (light)", BackgroundLight, Theme{
		Accent:        hex("#AF3A03"), // 256 ≈ 130
		SegmentPrompt: hex("#8F3F71"), // 256 ≈ 95
		SegmentGen:    hex("#AF3A03"), // 256 ≈ 130
		Subtle:        hex("#928374"), // 256 ≈ 244
		Muted:         hex("#A89984"), // 256 ≈ 138
		StatusIdle:    hex("#076678"), // 256 ≈ 24
		StatusReady:   hex("#79740E"), // 256 ≈ 100
		StatusStart:   hex("#B57614"), // 256 ≈ 136
		StatusErr:     hex("#9D0006"), // 256 ≈ 124
		StatusGone:    hex("#A89984"), // 256 ≈ 138
		BorderFocus:   hex("#AF3A03"), // 256 ≈ 130
		Border:        hex("#D5C4A1"), // 256 ≈ 187
	}},
	{"solarized-dark", "Solarized Dark", BackgroundDark, Theme{
		Accent:        hex("#CB4B16"), // 256 ≈ 166
		SegmentPrompt: hex("#6C71C4"), // 256 ≈ 62
		SegmentGen:    hex("#CB4B16"), // 256 ≈ 166
		Subtle:        hex("#839496"), // 256 ≈ 245
		Muted:         hex("#586E75"), // 256 ≈ 242
		StatusIdle:    hex("#268BD2"), // 256 ≈ 32
		StatusReady:   hex("#859900"), // 256 ≈ 100
		StatusStart:   hex("#B58900"), // 256 ≈ 136
		StatusErr:     hex("#DC322F"), // 256 ≈ 166
		StatusGone:    hex("#586E75"), // 256 ≈ 242
		BorderFocus:   hex("#CB4B16"), // 256 ≈ 166
		Border:        hex("#073642"), // 256 ≈ 235
	}},
	{"solarized-light", "Solarized Light", BackgroundLight, Theme{
		Accent:        hex("#CB4B16"), // 256 ≈ 166
		SegmentPrompt: hex("#6C71C4"), // 256 ≈ 62
		SegmentGen:    hex("#CB4B16"), // 256 ≈ 166
		Subtle:        hex("#657B83"), // 256 ≈ 66
		Muted:         hex("#93A1A1"), // 256 ≈ 247
		StatusIdle:    hex("#268BD2"), // 256 ≈ 32
		StatusReady:   hex("#859900"), // 256 ≈ 100
		StatusStart:   hex("#B58900"), // 256 ≈ 136
		StatusErr:     hex("#DC322F"), // 256 ≈ 166
		StatusGone:    hex("#93A1A1"), // 256 ≈ 247
		BorderFocus:   hex("#CB4B16"), // 256 ≈ 166
		Border:        hex("#EEE8D5"), // 256 ≈ 254
	}},
	{"nord", "Nord", BackgroundDark, Theme{
		Accent:        hex("#88C0D0"), // 256 ≈ 110
		SegmentPrompt: hex("#B48EAD"), // 256 ≈ 139
		SegmentGen:    hex("#D08770"), // 256 ≈ 173
		Subtle:        hex("#D8DEE9"), // 256 ≈ 254
		Muted:         hex("#4C566A"), // 256 ≈ 240
		StatusIdle:    hex("#81A1C1"), // 256 ≈ 109
		StatusReady:   hex("#A3BE8C"), // 256 ≈ 144
		StatusStart:   hex("#EBCB8B"), // 256 ≈ 186
		StatusErr:     hex("#BF616A"), // 256 ≈ 131
		StatusGone:    hex("#4C566A"), // 256 ≈ 240
		BorderFocus:   hex("#88C0D0"), // 256 ≈ 110
		Border:        hex("#3B4252"), // 256 ≈ 238
	}},
	{"nord-light", "Nord Light", BackgroundLight, Theme{
		Accent:        hex("#5E81AC"), // 256 ≈ 67
		SegmentPrompt: hex("#8D6687"), // 256 ≈ 96
		SegmentGen:    hex("#B26A50"), // 256 ≈ 131
		Subtle:        hex("#4C566A"), // 256 ≈ 240
		Muted:         hex("#D8DEE9"), // 256 ≈ 254
		StatusIdle:    hex("#81A1C1"), // 256 ≈ 109
		StatusReady:   hex("#A3BE8C"), // 256 ≈ 144
		StatusStart:   hex("#EBCB8B"), // 256 ≈ 186
		StatusErr:     hex("#BF616A"), // 256 ≈ 131
		StatusGone:    hex("#D8DEE9"), // 256 ≈ 254
		BorderFocus:   hex("#5E81AC"), // 256 ≈ 67
		Border:        hex("#D8DEE9"), // 256 ≈ 254
	}},
	{"one-dark", "One Dark", BackgroundDark, Theme{
		Accent:        hex("#61AFEF"), // 256 ≈ 75
		SegmentPrompt: hex("#C678DD"), // 256 ≈ 176
		SegmentGen:    hex("#D19A66"), // 256 ≈ 173
		Subtle:        hex("#ABB2BF"), // 256 ≈ 249
		Muted:         hex("#5C6370"), // 256 ≈ 241
		StatusIdle:    hex("#56B6C2"), // 256 ≈ 73
		StatusReady:   hex("#98C379"), // 256 ≈ 108
		StatusStart:   hex("#E5C07B"), // 256 ≈ 180
		StatusErr:     hex("#E06C75"), // 256 ≈ 168
		StatusGone:    hex("#5C6370"), // 256 ≈ 241
		BorderFocus:   hex("#61AFEF"), // 256 ≈ 75
		Border:        hex("#3E4451"), // 256 ≈ 238
	}},
	{"one-dark-light", "One Dark Light", BackgroundLight, Theme{
		Accent:        hex("#4078F2"), // 256 ≈ 69
		SegmentPrompt: hex("#A626A4"), // 256 ≈ 127
		SegmentGen:    hex("#C18401"), // 256 ≈ 136
		Subtle:        hex("#696C77"), // 256 ≈ 242
		Muted:         hex("#A0A1A7"), // 256 ≈ 247
		StatusIdle:    hex("#0184BC"), // 256 ≈ 31
		StatusReady:   hex("#50A14F"), // 256 ≈ 71
		StatusStart:   hex("#986801"), // 256 ≈ 94
		StatusErr:     hex("#E45649"), // 256 ≈ 167
		StatusGone:    hex("#A0A1A7"), // 256 ≈ 247
		BorderFocus:   hex("#4078F2"), // 256 ≈ 69
		Border:        hex("#E5E5E6"), // 256 ≈ 254
	}},
	{"kanagawa", "Kanagawa", BackgroundDark, Theme{
		Accent:        hex("#7E9CD8"), // 256 ≈ 110
		SegmentPrompt: hex("#957FB8"), // 256 ≈ 103
		SegmentGen:    hex("#FF9E3B"), // 256 ≈ 215
		Subtle:        hex("#C8C093"), // 256 ≈ 180
		Muted:         hex("#727169"), // 256 ≈ 242
		StatusIdle:    hex("#9CABCA"), // 256 ≈ 146
		StatusReady:   hex("#98BB6C"), // 256 ≈ 107
		StatusStart:   hex("#DCA561"), // 256 ≈ 179
		StatusErr:     hex("#E46876"), // 256 ≈ 168
		StatusGone:    hex("#727169"), // 256 ≈ 242
		BorderFocus:   hex("#7E9CD8"), // 256 ≈ 110
		Border:        hex("#363646"), // 256 ≈ 237
	}},
	{"kanagawa-lotus", "Kanagawa Lotus", BackgroundLight, Theme{
		Accent:        hex("#4D699B"), // 256 ≈ 60
		SegmentPrompt: hex("#624C83"), // 256 ≈ 60
		SegmentGen:    hex("#CC6D00"), // 256 ≈ 166
		Subtle:        hex("#545464"), // 256 ≈ 240
		Muted:         hex("#8A8980"), // 256 ≈ 102
		StatusIdle:    hex("#597B75"), // 256 ≈ 66
		StatusReady:   hex("#6F894E"), // 256 ≈ 65
		StatusStart:   hex("#77713F"), // 256 ≈ 95
		StatusErr:     hex("#C84053"), // 256 ≈ 167
		StatusGone:    hex("#8A8980"), // 256 ≈ 102
		BorderFocus:   hex("#4D699B"), // 256 ≈ 60
		Border:        hex("#E7DBA0"), // 256 ≈ 187
	}},
	{"monokai", "Monokai", BackgroundDark, Theme{
		Accent:        hex("#FD971F"), // 256 ≈ 208
		SegmentPrompt: hex("#AE81FF"), // 256 ≈ 141
		SegmentGen:    hex("#FD971F"), // 256 ≈ 208
		Subtle:        hex("#75715E"), // 256 ≈ 242
		Muted:         hex("#49483E"), // 256 ≈ 238
		StatusIdle:    hex("#66D9EF"), // 256 ≈ 81
		StatusReady:   hex("#A6E22E"), // 256 ≈ 148
		StatusStart:   hex("#E6DB74"), // 256 ≈ 186
		StatusErr:     hex("#F92672"), // 256 ≈ 197
		StatusGone:    hex("#49483E"), // 256 ≈ 238
		BorderFocus:   hex("#FD971F"), // 256 ≈ 208
		Border:        hex("#3E3D32"), // 256 ≈ 237
	}},
	{"monokai-light", "Monokai Light", BackgroundLight, Theme{
		Accent:        hex("#FD971F"), // 256 ≈ 208
		SegmentPrompt: hex("#7A57C4"), // 256 ≈ 98
		SegmentGen:    hex("#C2701A"), // 256 ≈ 130
		Subtle:        hex("#75715E"), // 256 ≈ 242
		Muted:         hex("#CFCBC0"), // 256 ≈ 251
		StatusIdle:    hex("#66D9EF"), // 256 ≈ 81
		StatusReady:   hex("#A6E22E"), // 256 ≈ 148
		StatusStart:   hex("#E6DB74"), // 256 ≈ 186
		StatusErr:     hex("#F92672"), // 256 ≈ 197
		StatusGone:    hex("#CFCBC0"), // 256 ≈ 251
		BorderFocus:   hex("#FD971F"), // 256 ≈ 208
		Border:        hex("#E5E1D5"), // 256 ≈ 253
	}},
	{"rose-pine", "Rosé Pine", BackgroundDark, Theme{
		Accent:        hex("#EBBCBA"), // 256 ≈ 181
		SegmentPrompt: hex("#C4A7E7"), // 256 ≈ 182
		SegmentGen:    hex("#F6C177"), // 256 ≈ 216
		Subtle:        hex("#908CAA"), // 256 ≈ 103
		Muted:         hex("#6E6A86"), // 256 ≈ 60
		StatusIdle:    hex("#9CCFD8"), // 256 ≈ 152
		StatusReady:   hex("#31748F"), // 256 ≈ 66
		StatusStart:   hex("#F6C177"), // 256 ≈ 216
		StatusErr:     hex("#EB6F92"), // 256 ≈ 168
		StatusGone:    hex("#6E6A86"), // 256 ≈ 60
		BorderFocus:   hex("#EBBCBA"), // 256 ≈ 181
		Border:        hex("#1F1D2E"), // 256 ≈ 235
	}},
	{"rose-pine-dawn", "Rosé Pine Dawn", BackgroundLight, Theme{
		Accent:        hex("#D7827E"), // 256 ≈ 174
		SegmentPrompt: hex("#907AA9"), // 256 ≈ 103
		SegmentGen:    hex("#EA9D34"), // 256 ≈ 179
		Subtle:        hex("#797593"), // 256 ≈ 244
		Muted:         hex("#9893A5"), // 256 ≈ 247
		StatusIdle:    hex("#56949F"), // 256 ≈ 67
		StatusReady:   hex("#286983"), // 256 ≈ 24
		StatusStart:   hex("#EA9D34"), // 256 ≈ 179
		StatusErr:     hex("#B4637A"), // 256 ≈ 132
		StatusGone:    hex("#9893A5"), // 256 ≈ 247
		BorderFocus:   hex("#D7827E"), // 256 ≈ 174
		Border:        hex("#F2E9E1"), // 256 ≈ 255
	}},
	{"night-owl", "Night Owl", BackgroundDark, Theme{
		Accent:        hex("#82AAFF"), // 256 ≈ 111
		SegmentPrompt: hex("#C792EA"), // 256 ≈ 176
		SegmentGen:    hex("#FFCB8B"), // 256 ≈ 222
		Subtle:        hex("#89A4BB"), // 256 ≈ 109
		Muted:         hex("#637777"), // 256 ≈ 242
		StatusIdle:    hex("#7FDBCA"), // 256 ≈ 116
		StatusReady:   hex("#22DA6E"), // 256 ≈ 41
		StatusStart:   hex("#FFEB95"), // 256 ≈ 222
		StatusErr:     hex("#EF5350"), // 256 ≈ 203
		StatusGone:    hex("#637777"), // 256 ≈ 242
		BorderFocus:   hex("#82AAFF"), // 256 ≈ 111
		Border:        hex("#122D42"), // 256 ≈ 235
	}},
	{"light-owl", "Light Owl", BackgroundLight, Theme{
		Accent:        hex("#2AA298"), // 256 ≈ 36
		SegmentPrompt: hex("#A093E8"), // 256 ≈ 140
		SegmentGen:    hex("#D28E00"), // 256 ≈ 172
		Subtle:        hex("#989FB1"), // 256 ≈ 247
		Muted:         hex("#C3C8D6"), // 256 ≈ 251
		StatusIdle:    hex("#4876D6"), // 256 ≈ 68
		StatusReady:   hex("#08916A"), // 256 ≈ 29
		StatusStart:   hex("#E0AF02"), // 256 ≈ 178
		StatusErr:     hex("#DE3D3B"), // 256 ≈ 167
		StatusGone:    hex("#C3C8D6"), // 256 ≈ 251
		BorderFocus:   hex("#2AA298"), // 256 ≈ 36
		Border:        hex("#D9D9D9"), // 256 ≈ 253
	}},
}

// DefaultTheme resolves the "auto" theme against the real terminal
// background. Used by callers that construct modes before a config
// exists (first-run) and by tests.
func DefaultTheme() Theme {
	t, _, _ := ResolveTheme("", lipgloss.HasDarkBackground())
	return t
}

// ResolveTheme maps a preferences.theme value to the Theme to render
// with, for the given terminal background. It is a pure function (P9):
//
//   - "" and "auto" → the llamaman palette (adaptive; the default).
//   - "llamaman"    → the same adaptive palette, pinned by name.
//   - a named palette → that palette.
//   - unknown value → ok=false and the auto theme; the caller must turn
//     that into a Warning (P3: degrade to a sensible default, never
//     block) and log it.
func ResolveTheme(name string, darkBg bool) (t Theme, resolvedID string, ok bool) {
	if name == "" || name == "auto" || name == "llamaman" {
		if darkBg {
			return llamamanDark, "llamaman", true
		}
		return llamamanLight, "llamaman", true
	}
	for _, p := range palettes {
		if p.ID == name {
			return p.T, p.ID, true
		}
	}
	if darkBg {
		return llamamanDark, "auto", false
	}
	return llamamanLight, "auto", false
}

// lookupPalette returns the palette with the given ID, if any.
func lookupPalette(id string) (Palette, bool) {
	for _, p := range palettes {
		if p.ID == id {
			return p, true
		}
	}
	return Palette{}, false
}

// paletteCompatible reports whether a stored theme value matches the
// terminal background: "auto"/"" always, named palettes when adaptive
// or background-matching. Compatibility is now a warning-level hint,
// not a filter (owner decision, DESIGN §15.1): the user may pick any
// variant explicitly.
func paletteCompatible(id string, darkBg bool) bool {
	if id == "" || id == "auto" {
		return true
	}
	p, ok := lookupPalette(id)
	if !ok {
		return false
	}
	return p.Background == BackgroundAdaptive ||
		(p.Background == BackgroundDark) == darkBg
}

// lookupKnown reports whether the value is a usable theme reference
// ("", "auto", "llamaman", or a palette-table ID).
func lookupKnown(id string) bool {
	if id == "" || id == "auto" || id == "llamaman" {
		return true
	}
	_, ok := lookupPalette(id)
	return ok
}

// mismatchWarning returns a user-facing warning when the stored theme
// is a known palette that does not match the terminal background
// (empty string when it does, or when the value is unknown/auto — the
// unknown case is handled by the resolver's fallback instead).
func mismatchWarning(id string, darkBg bool) string {
	if id == "" || id == "auto" {
		return ""
	}
	p, ok := lookupPalette(id)
	if !ok {
		return ""
	}
	if paletteCompatible(id, darkBg) {
		return ""
	}
	term := "light"
	if darkBg {
		term = "dark"
	}
	return p.Display + " may be hard to read on a " + term + " terminal"
}

// cyclePalettes returns every selectable palette grouped for browsing:
// the adaptive llamaman default first, then dark palettes, then light
// ones. Both variants of every family are offered — the background is
// a hint, not a filter (owner decision).
func cyclePalettes() []Palette {
	out := make([]Palette, 0, len(palettes))
	for _, p := range palettes {
		if p.Background == BackgroundAdaptive {
			out = append(out, p)
		}
	}
	for _, p := range palettes {
		if p.Background == BackgroundDark {
			out = append(out, p)
		}
	}
	for _, p := range palettes {
		if p.Background == BackgroundLight {
			out = append(out, p)
		}
	}
	return out
}

// themeCycle returns the ordered sequence of preferences.theme values
// the quick keys step through: "auto" first (the default), then all 23
// palettes. An unknown stored value is treated as sitting just before
// "auto", so the first keypress lands on "auto" and re-anchors.
func themeCycle() []string {
	all := cyclePalettes()
	seq := make([]string, 0, len(all)+1)
	seq = append(seq, "auto")
	for _, p := range all {
		seq = append(seq, p.ID)
	}
	return seq
}

// nextTheme returns the theme value after cycling `dir` steps (+1
// forward, -1 backward) from the current value, wrapping around the
// cycle. `t` / `shift+t` call this with ±1. An unknown current value
// sits just before "auto", so the first press lands on "auto" and
// re-anchors the cycle.
func nextTheme(current string, dir int) string {
	seq := themeCycle()
	idx := -1 // unknown values sit before "auto"
	if current == "" || current == "auto" {
		idx = 0 // "" (absent) means auto: first press steps to llamaman
	} else {
		for i, v := range seq {
			if v == current {
				idx = i
				break
			}
		}
	}
	return seq[(idx+dir+len(seq))%len(seq)]
}

// llamamanDark and llamamanLight are the two variants of the original
// hard-coded theme, kept exactly as-is (DESIGN §15.1). They back both
// the "auto" default and the pinned "llamaman" palette.
var (
	llamamanDark = Theme{
		Accent:        lipgloss.Color("#E8A33D"), // soft orange (DESIGN §10.4)
		SegmentPrompt: lipgloss.Color("#9B59B6"),
		SegmentGen:    lipgloss.Color("#FF8C00"),
		Subtle:        lipgloss.Color("#9A9A9A"),
		Muted:         lipgloss.Color("#5C5C5C"),
		StatusIdle:    lipgloss.Color("#87CEEB"), // sky blue — maps to 256-color 116
		StatusReady:   lipgloss.Color("#73D216"), // green — maps to 256-color 76
		StatusStart:   lipgloss.Color("#FFD700"), // gold — maps to 256-color 220
		StatusErr:     lipgloss.Color("#FF6B6B"), // soft red — maps to 256-color 203
		StatusGone:    lipgloss.Color("#7C7C7C"),
		BorderFocus:   lipgloss.Color("#E8A33D"),
		Border:        lipgloss.Color("#444444"),
	}
	llamamanLight = Theme{
		Accent:        lipgloss.Color("#C26B11"),
		SegmentPrompt: lipgloss.Color("#8E44AD"),
		SegmentGen:    lipgloss.Color("#D35400"),
		Subtle:        lipgloss.Color("#5A5A5A"),
		Muted:         lipgloss.Color("#9A9A9A"),
		StatusIdle:    lipgloss.Color("#3A7AAB"), // medium blue — readable on light bg
		StatusReady:   lipgloss.Color("#1F7A28"),
		StatusStart:   lipgloss.Color("#A06B00"),
		StatusErr:     lipgloss.Color("#CC0000"), // strong red — maps to 256-color 160
		StatusGone:    lipgloss.Color("#7C7C7C"),
		BorderFocus:   lipgloss.Color("#C26B11"),
		Border:        lipgloss.Color("#BBBBBB"),
	}
)
