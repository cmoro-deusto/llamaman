package tui

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestPalette256AnnotationsAccurate pins the P1 discipline (§10.4):
// every `// 256 ≈ N` annotation in theme.go — on the palette table and
// on the llamamanDark/llamamanLight "maps to 256-color" comments — must
// equal the true nearest xterm-256 index (6x6x6 cube + 16-step gray
// ramp, ties resolve to the lower index, matching the file comment).
// Regression: the llamaman vars used to carry stale indices (e.g.
// "118" for #73D216; the true nearest is 76, a tie with 118 resolved
// downward) that contradicted the table.
func TestPalette256AnnotationsAccurate(t *testing.T) {
	src, err := os.ReadFile("theme.go")
	if err != nil {
		t.Fatalf("read theme.go: %v", err)
	}
	// hex("#RRGGBB"), // 256 ≈ N   (palette table)
	tableRE := regexp.MustCompile(`hex\("#([0-9A-Fa-f]{6})"\)\s*,?\s*//\s*256\s*≈\s*(\d+)`)
	// lipgloss.Color("#RRGGBB"), // ... maps to 256-color N  (llamaman vars)
	varRE := regexp.MustCompile(`#([0-9A-Fa-f]{6})"\)\s*,?\s*//.*maps to 256-color (\d+)`)

	check := func(matches [][]string, where string) {
		for _, m := range matches {
			rgb := m[1]
			want := nearestXterm256(rgb)
			claimed, _ := strconv.Atoi(m[2])
			if claimed != want {
				t.Errorf("theme.go (%s): #%s claimed 256 ≈ %d, true nearest is %d", where, rgb, claimed, want)
			}
		}
	}
	check(tableRE.FindAllStringSubmatch(string(src), -1), "palette table")
	check(varRE.FindAllStringSubmatch(string(src), -1), "llamaman vars")
}

// nearestXterm256 returns the xterm-256 index closest to the given hex
// color over the 6x6x6 color cube (16..231) and the 16-step gray ramp
// (232..255), with ties resolving to the lower index. The 16 system
// colors are excluded (they are terminal-dependent).
func nearestXterm256(hex string) int {
	r, g, b := hexChan(hex, 0), hexChan(hex, 2), hexChan(hex, 4)
	levels := [6]int{0, 95, 135, 175, 215, 255}
	best, bestD := 0, math.MaxInt64
	for i := 16; i < 232; i++ {
		n := i - 16
		cr, cg, cb := levels[n/36], levels[(n/6)%6], levels[n%6]
		if d := sq(r-cr) + sq(g-cg) + sq(b-cb); d < bestD {
			best, bestD = i, d
		}
	}
	for i := 232; i < 256; i++ {
		v := 8 + 10*(i-232)
		if d := sq(r-v) + sq(g-v) + sq(b-v); d < bestD {
			best, bestD = i, d
		}
	}
	return best
}

func hexChan(s string, off int) int {
	v, _ := strconv.ParseUint(s[off:off+2], 16, 8)
	return int(v)
}

func sq(v int) int { return v * v }

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
			p.T.Accent, p.T.SegmentPrompt, p.T.SegmentGen, p.T.Subtle, p.T.Muted,
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

// TestSegmentColorsPinned pins the theme-driven prompt/gen segment
// colors: the default theme must resolve non-empty segment colors, the
// two llamaman variants must keep the historical hard-coded values
// (the pre-theme look), and the palette table entries must carry
// distinct prompt/gen colors (purple vs orange).
func TestSegmentColorsPinned(t *testing.T) {
	dark, _, ok := ResolveTheme("auto", true)
	if !ok || dark.SegmentPrompt == "" || dark.SegmentGen == "" {
		t.Fatalf("dark default must resolve segment colors, got prompt=%q gen=%q", dark.SegmentPrompt, dark.SegmentGen)
	}
	light, _, _ := ResolveTheme("auto", false)
	if string(dark.SegmentPrompt) != "#9B59B6" || string(dark.SegmentGen) != "#FF8C00" {
		t.Errorf("dark default segments = %q/%q, want the historical #9B59B6/#FF8C00", dark.SegmentPrompt, dark.SegmentGen)
	}
	if string(light.SegmentPrompt) != "#8E44AD" || string(light.SegmentGen) != "#D35400" {
		t.Errorf("light default segments = %q/%q, want #8E44AD/#D35400", light.SegmentPrompt, light.SegmentGen)
	}
	for _, p := range palettes {
		if p.ID == "llamaman" {
			continue
		}
		if p.T.SegmentPrompt == "" || p.T.SegmentGen == "" {
			t.Errorf("%s: missing segment colors", p.ID)
			continue
		}
		if p.T.SegmentPrompt == p.T.SegmentGen {
			t.Errorf("%s: prompt and gen segment colors are identical", p.ID)
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

// TestCyclePalettesGroupsByBackground pins the picker/cycle ordering:
// llamaman (adaptive) first, then dark palettes, then light ones — both
// variants of every family are offered (owner decision: background is a
// hint, not a filter).
func TestCyclePalettesGroupsByBackground(t *testing.T) {
	all := cyclePalettes()
	if len(all) != 23 {
		t.Fatalf("cycle palettes = %d, want 23", len(all))
	}
	if all[0].ID != "llamaman" || all[0].Background != BackgroundAdaptive {
		t.Errorf("first palette must be the adaptive llamaman, got %+v", all[0])
	}
	darks, lights := 0, 0
	for i, p := range all {
		switch p.Background {
		case BackgroundDark:
			darks++
			if i < 1 || i > 11 {
				t.Errorf("dark palette %s out of the dark block", p.ID)
			}
		case BackgroundLight:
			lights++
			if i < 12 || i > 22 {
				t.Errorf("light palette %s outside the light block", p.ID)
			}
		}
	}
	if darks != 11 || lights != 11 {
		t.Errorf("dark/light counts = %d/%d, want 11/11", darks, lights)
	}
}

// TestMismatchWarning covers the explicit-override behavior: a known
// palette whose background mismatches the terminal yields a warning;
// adaptive, matching, and unknown values warn nothing.
func TestMismatchWarning(t *testing.T) {
	if w := mismatchWarning("solarized-light", true); w == "" || !containsSequence(w, "hard to read") {
		t.Errorf("light palette on dark terminal must warn, got %q", w)
	}
	if w := mismatchWarning("solarized-light", false); w != "" {
		t.Errorf("matching light palette on light terminal must not warn, got %q", w)
	}
	if w := mismatchWarning("solarized-dark", true); w != "" {
		t.Errorf("matching dark palette on dark terminal must not warn, got %q", w)
	}
	if w := mismatchWarning("llamaman", true); w != "" {
		t.Errorf("adaptive palette must not warn, got %q", w)
	}
	if w := mismatchWarning("auto", true); w != "" {
		t.Errorf("auto must not warn, got %q", w)
	}
	if w := mismatchWarning("garbage", true); w != "" {
		t.Errorf("unknown value must not warn (resolver handles it), got %q", w)
	}
}

// TestThemeCycleCoversAllValuesAndWraps verifies the quick-key cycle:
// it starts at "auto", steps through all 23 palettes, and wraps in both
// directions.
func TestThemeCycleCoversAllValuesAndWraps(t *testing.T) {
	seq := themeCycle()
	if seq[0] != "auto" {
		t.Fatalf("cycle must start at auto, got %q", seq[0])
	}
	if len(seq) != 24 { // auto + 23 palettes
		t.Fatalf("cycle length = %d, want 24", len(seq))
	}

	// Forward from auto walks the whole cycle and wraps.
	cur := "auto"
	for i := 0; i < len(seq); i++ {
		cur = nextTheme(cur, +1)
		if cur != seq[(i+1)%len(seq)] {
			t.Fatalf("forward step %d = %q, want %q", i, cur, seq[(i+1)%len(seq)])
		}
	}
	if cur != "auto" {
		t.Errorf("after a full forward loop, cur = %q, want auto", cur)
	}

	// Backward wraps too.
	cur = "auto"
	cur = nextTheme(cur, -1)
	if cur != seq[len(seq)-1] {
		t.Errorf("backward from auto = %q, want %q", cur, seq[len(seq)-1])
	}
}

// TestThemeCycleAnchorsUnknownValues: an unknown stored value behaves
// as if sitting just before "auto", so the first press lands on auto
// and re-anchors.
func TestThemeCycleAnchorsUnknownValues(t *testing.T) {
	seq := themeCycle()
	got := nextTheme("garbage", +1)
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

// mustPalette returns the palette Theme for a known ID (test helper).
func mustPalette(id string) Theme {
	p, ok := lookupPalette(id)
	if !ok {
		panic("unknown palette in test: " + id)
	}
	return p.T
}
