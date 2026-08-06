package tui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestPaletteTableShape pins the curated table: 23 palettes + the auto
// value, all with stable IDs, unique display names, and valid hex
// colors on every field.
func TestPaletteTableShape(t *testing.T) {
	if len(palettes) != 23 {
		t.Fatalf("palette table has %d entries, want 23 (DESIGN §15.1)", len(palettes))
	}
	seen := map[string]bool{}
	ids := map[string]bool{}
	for _, p := range palettes {
		if p.ID == "" || p.Display == "" {
			t.Errorf("palette with empty ID/Display: %+v", p)
		}
		if ids[p.ID] {
			t.Errorf("duplicate palette ID %q", p.ID)
		}
		ids[p.ID] = true
		if seen[p.Display] {
			t.Errorf("duplicate display name %q", p.Display)
		}
		seen[p.Display] = true
	}
	// The original theme must be first (auto default) and adaptive.
	if palettes[0].ID != "llamaman" || palettes[0].Background != BackgroundAdaptive {
		t.Errorf("first palette must be the adaptive llamaman default, got %+v", palettes[0])
	}
}

// TestPaletteHexesAreValid ensures every color field is a parseable hex
// — the table is data-heavy and a typo would render as a no-op color.
func TestPaletteHexesAreValid(t *testing.T) {
	for _, p := range palettes {
		for _, c := range []lipgloss.Color{
			p.T.Accent, p.T.Subtle, p.T.Muted,
			p.T.StatusIdle, p.T.StatusReady, p.T.StatusStart, p.T.StatusErr, p.T.StatusGone,
			p.T.BorderFocus, p.T.Border,
		} {
			s := string(c)
			if len(s) != 7 || s[0] != '#' {
				t.Errorf("%s: invalid color %q", p.ID, s)
			}
		}
	}
}

// TestResolveThemeAutoAndLlamaman covers the default resolution: "",
// "auto", and "llamaman" all resolve to the adaptive pair (dark or
// light by background) and report the resolved ID "llamaman".
func TestResolveThemeAutoAndLlamaman(t *testing.T) {
	for _, name := range []string{"", "auto", "llamaman"} {
		for _, dark := range []bool{true, false} {
			got, id, ok := ResolveTheme(name, dark)
			if !ok {
				t.Errorf("ResolveTheme(%q, %v): ok=false, want true", name, dark)
			}
			if id != "llamaman" {
				t.Errorf("ResolveTheme(%q, %v): resolvedID=%q, want llamaman", name, dark, id)
			}
			// The dark and light variants must differ, and the adaptive
			// pair must be exactly the pre-release theme.
			if dark {
				if string(got.Accent) != "#E8A33D" {
					t.Errorf("dark variant accent = %s, want #E8A33D (unchanged)", got.Accent)
				}
			} else {
				if string(got.Accent) != "#C26B11" {
					t.Errorf("light variant accent = %s, want #C26B11 (unchanged)", got.Accent)
				}
			}
		}
	}
}

// TestResolveThemeNamedPalettes verifies every named palette resolves
// to its own colors (distinct from the default and from each other).
func TestResolveThemeNamedPalettes(t *testing.T) {
	baseline, _, _ := ResolveTheme("auto", true)
	for _, p := range palettes {
		if p.ID == "llamaman" {
			continue
		}
		got, id, ok := ResolveTheme(p.ID, p.Background == BackgroundLight)
		if !ok {
			t.Fatalf("ResolveTheme(%q): ok=false", p.ID)
		}
		if id != p.ID {
			t.Errorf("ResolveTheme(%q): resolvedID=%q", p.ID, id)
		}
		if got.Accent == baseline.Accent && got.StatusReady == baseline.StatusReady {
			t.Errorf("%s: palette does not differ from the default theme", p.ID)
		}
	}
}

// TestResolveThemeUnknownFallsBack covers P3: an unknown name degrades
// to the auto theme with ok=false (the caller warns).
func TestResolveThemeUnknownFallsBack(t *testing.T) {
	got, id, ok := ResolveTheme("definitely-not-real", true)
	if ok {
		t.Error("unknown name must report ok=false")
	}
	if id != "auto" {
		t.Errorf("resolvedID = %q, want auto", id)
	}
	auto, _, _ := ResolveTheme("auto", true)
	if got != auto {
		t.Error("unknown name must resolve to the auto theme")
	}
}

// TestCompatiblePalettesFiltersByBackground pins the P1 compatibility
// rule: a dark terminal sees llamaman + all dark palettes; a light
// terminal sees llamaman + all light palettes. 12 each, no overlap.
func TestCompatiblePalettesFiltersByBackground(t *testing.T) {
	dark := CompatiblePalettes(true)
	light := CompatiblePalettes(false)
	if len(dark) != 12 {
		t.Errorf("dark-terminal palette count = %d, want 12 (llamaman + 11 dark)", len(dark))
	}
	if len(light) != 12 {
		t.Errorf("light-terminal palette count = %d, want 12 (llamaman + 11 light)", len(light))
	}
	if dark[0].ID != "llamaman" || light[0].ID != "llamaman" {
		t.Error("llamaman (adaptive) must be offered first on both backgrounds")
	}
	for _, p := range dark {
		if p.Background == BackgroundLight {
			t.Errorf("dark terminal offered light palette %s", p.ID)
		}
	}
	for _, p := range light {
		if p.Background == BackgroundDark {
			t.Errorf("light terminal offered dark palette %s", p.ID)
		}
	}
}

// TestThemeCycleCoversAllValuesAndWraps verifies the quick-key cycle:
// it starts at "auto", steps through every compatible palette, and
// wraps in both directions.
func TestThemeCycleCoversAllValuesAndWraps(t *testing.T) {
	seq := themeCycle(true)
	if seq[0] != "auto" {
		t.Fatalf("cycle must start at auto, got %q", seq[0])
	}
	if len(seq) != 13 { // auto + 12 compatible
		t.Fatalf("cycle length = %d, want 13", len(seq))
	}

	// Forward from auto walks the whole cycle and wraps.
	cur := "auto"
	for i := 0; i < len(seq); i++ {
		cur = nextTheme(cur, +1, true)
		if cur != seq[(i+1)%len(seq)] {
			t.Fatalf("forward step %d = %q, want %q", i, cur, seq[(i+1)%len(seq)])
		}
	}
	if cur != "auto" {
		t.Errorf("after a full forward loop, cur = %q, want auto", cur)
	}

	// Backward wraps too.
	cur = "auto"
	cur = nextTheme(cur, -1, true)
	if cur != seq[len(seq)-1] {
		t.Errorf("backward from auto = %q, want %q", cur, seq[len(seq)-1])
	}
}

// TestThemeCycleAnchorsUnknownValues: an unknown/incompatible stored
// value behaves as if sitting just before "auto", so the first press
// lands on auto and re-anchors.
func TestThemeCycleAnchorsUnknownValues(t *testing.T) {
	seq := themeCycle(true)
	got := nextTheme("garbage", +1, true)
	if got != seq[0] {
		t.Errorf("first press from an unknown value = %q, want %q", got, seq[0])
	}
}

// TestRender256ColorDiscipline forces an ANSI-256 profile and asserts
// the rendered SGR uses 256-color codes that differ per palette — the
// P1 "snapshot tests assert specific colors" contract, kept
// deterministic via lipgloss.SetColorProfile / SetHasDarkBackground.
func TestRender256ColorDiscipline(t *testing.T) {
	lipgloss.SetColorProfile(termenv.ANSI256)
	defer lipgloss.SetColorProfile(termenv.ColorProfile())
	lipgloss.SetHasDarkBackground(true)
	defer lipgloss.SetHasDarkBackground(termenv.HasDarkBackground())

	renderAccent := func(name string) string {
		th, _, _ := ResolveTheme(name, true)
		return lipgloss.NewStyle().Foreground(th.Accent).Render("x")
	}
	dracula := renderAccent("dracula")
	gruvbox := renderAccent("gruvbox-dark")
	if dracula == gruvbox {
		t.Fatal("different palettes must emit different SGR on a forced 256 profile")
	}
	if !contains256SGR(dracula) {
		t.Errorf("rendered accent lacks a 38;5;… 256-color code: %q", dracula)
	}
	// Dracula's accent purple #BD93F9 is closest to xterm 141.
	want := "\x1b[38;5;141m"
	if !containsSequence(dracula, want) {
		t.Errorf("dracula accent SGR = %q, want to contain %q", dracula, want)
	}
}

func contains256SGR(s string) bool { return containsSequence(s, "\x1b[38;5;") }

func containsSequence(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
