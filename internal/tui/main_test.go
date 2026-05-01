package tui

import (
	"strings"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// TestMainViewRendersWordmarkAndShortcuts is a lightweight render check
// that doesn't require a PTY — full teatest snapshots come in Phase 11.
func TestMainViewRendersWordmarkAndShortcuts(t *testing.T) {
	cfg := &config.Config{Version: 1}
	m := NewMainMode(cfg, "v0.0.1-test")
	m.SetSize(120, 40)

	out := m.View()
	for _, want := range []string{
		"llamaman v0.0.1-test", // tagline
		"select model",        // shortcut label
		"configure",           // shortcut label
		"quit",                // shortcut label
	} {
		if !strings.Contains(stripANSI(out), want) {
			t.Errorf("View() missing %q in:\n%s", want, out)
		}
	}
}

// stripANSI is a minimal escape-stripper for assertions in tests. It
// drops CSI sequences like \x1b[...m so we can match against literal
// substrings.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j - 1
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
