package tui

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// termSafe makes externally sourced text (Hugging Face model cards,
// tags, repo ids, task names) safe to lay out in a bordered panel
// (DESIGN §16.7 "terminal-safe text"). Layout only holds when lipgloss
// and the terminal agree on every rune's width; they agree on plain
// letters, digits, punctuation and narrow symbols, and disagree — by
// terminal, font, Unicode version, or emoji support — on almost
// everything else. So this is an ALLOWLIST by Unicode category, not a
// blocklist of ranges that grows one owner report at a time:
//
//   - C0/C1 controls, DEL, and every space separator become one ASCII
//     space (the terminal EXECUTES controls: a VT moves the cursor, a
//     tab jumps to a tab stop). '\n' survives only when keepNewlines.
//   - Combining and invisible runes are dropped: all marks (M — Mn/Me
//     are zero width; spacing marks Mc are 1 column under wcwidth but
//     merge into the previous cluster under grapheme-aware terminals,
//     so Indic/Myanmar text drifts), format characters (Cf — ZWJ, bidi marks,
//     variation selectors' VS15/VS16 are Mn), private use (Co — Nerd
//     Font glyphs), surrogates, line/paragraph separators, unassigned.
//   - Wide runes are kept only when they are letters, digits or
//     punctuation (CJK, fullwidth forms — every terminal agrees on 2);
//     wide symbols are emoji and are dropped.
//   - Hangul conjoining jamo are dropped (they compose into one
//     cluster); precomposed syllables are kept.
//   - Narrow emoji-capable symbol blocks (Misc Symbols, Dingbats, Misc
//     Symbols and Arrows: ♥ ⚠ ✔ ⬇ …) are dropped: emoji-aware terminals
//     draw several of them at width 2.
//   - Runes outside the BMP are kept only as wide letters (CJK Ext B+);
//     math alphanumerics (𝛕) and other astral narrow runes are dropped.
//
//
// Category rules cannot see context, and Unicode scatters runes that
// merge into the previous character across categories (halfwidth kana
// sound marks are letters, Thai SARA AM is a letter). So a final pass
// splits the result into grapheme clusters and keeps only the first
// rune of any cluster whose width grapheme-aware and wcwidth terminals
// would disagree on.
//
// It never emits escape sequences; callers style the result afterwards.
func termSafe(s string, keepNewlines bool) string {
	return consistentClusters(filterRunes(s, keepNewlines))
}

// filterRunes applies the per-rune category allowlist (see termSafe).
func filterRunes(s string, keepNewlines bool) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' && keepNewlines:
			b.WriteRune(r)
		case r < 0x20 || (r >= 0x7F && r <= 0x9F):
			b.WriteByte(' ')
		case r < 0x7F:
			b.WriteRune(r) // printable ASCII
		case unicode.Is(unicode.Zs, r):
			b.WriteByte(' ')
		case termSafeRune(r):
			b.WriteRune(r)
		}
	}
	return b.String()
}

// consistentClusters truncates every grapheme cluster whose grapheme
// width differs from its wcwidth sum to its first rune, repeating until
// stable (dropping runes can move cluster boundaries).
func consistentClusters(s string) string {
	for range 4 {
		var b strings.Builder
		b.Grow(len(s))
		changed := false
		rest, state := s, -1
		for len(rest) > 0 {
			var cluster string
			cluster, rest, _, state = uniseg.FirstGraphemeClusterInString(rest, state)
			if ansi.StringWidth(cluster) == ansi.StringWidthWc(cluster) {
				b.WriteString(cluster)
				continue
			}
			r, _ := utf8.DecodeRuneInString(cluster)
			b.WriteRune(r)
			changed = true
		}
		s = b.String()
		if !changed {
			break
		}
	}
	return s
}

// termSafeLine is termSafe for single-line fields (newlines → space).
func termSafeLine(s string) string { return termSafe(s, false) }

// termSafeRune reports whether a non-ASCII, non-space, non-control rune
// has a width every terminal agrees on (see termSafe).
func termSafeRune(r rune) bool {
	if !unicode.IsGraphic(r) || unicode.In(r, unicode.M, unicode.Cf, unicode.Co) {
		return false
	}
	if emojiCapableBlock(r) || conjoiningJamo(r) {
		return false
	}
	wide := ansi.StringWidth(string(r)) == 2
	if r > 0xFFFF {
		return wide && unicode.IsLetter(r)
	}
	if wide {
		return unicode.In(r, unicode.L, unicode.N, unicode.P)
	}
	return true
}

// conjoiningJamo reports whether r is a Hangul conjoining jamo: runs of
// them compose into one syllable cluster (grapheme width 2) that
// wcwidth counts per jamo. Precomposed syllables (U+AC00…) are kept.
func conjoiningJamo(r rune) bool {
	return (r >= 0x1100 && r <= 0x11FF) || (r >= 0xA960 && r <= 0xA97F) || (r >= 0xD7B0 && r <= 0xD7FF)
}

// emojiCapableBlock reports whether r sits in a block whose narrow
// symbols emoji-aware terminals may draw at width 2.
func emojiCapableBlock(r rune) bool {
	return (r >= 0x2600 && r <= 0x27BF) || // Misc Symbols, Dingbats (⚡♥⚖⚠✨…)
		(r >= 0x2B00 && r <= 0x2BFF) // Misc Symbols and Arrows (⬇…)
}
