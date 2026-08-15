package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runSH runs a script under /bin/sh and returns its stdout minus the
// trailing newline. Used to prove shellArg round-trips through a real
// shell.
func runSH(t *testing.T, script string) string {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", script, err)
	}
	return strings.TrimSuffix(string(out), "\n")
}

// shellArg quoting is the one place where a wrong byte would hand the user
// a broken command, so it gets a small table of safe / unsafe inputs.
func TestShellArg(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"4096", "4096"},
		{"/usr/bin/llama-server", "/usr/bin/llama-server"},
		{"-selection", "-selection"},
		{"a,b:c@d%e+f=g.h/i", "a,b:c@d%e+f=g.h/i"},
		{"", "''"},
		{"my model.gguf", "'my model.gguf'"},
		{"a b;c", "'a b;c'"},
		{`don't`, `'don'\''t'`},
		{"a*b", "'a*b'"},
		{"$(rm -rf)", "'$(rm -rf)'"},
	}
	for _, tt := range tests {
		if got := shellArg(tt.in); got != tt.want {
			t.Errorf("shellArg(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// A quoted word must round-trip to the original under a real shell. This is
// the true acceptance test for the quoting; it shells out to /bin/sh.
func TestShellArgRoundTrips(t *testing.T) {
	inputs := []string{"4096", "/usr/bin/llama-server", "my model.gguf", "a b;c", "don't", "a*b", "$(rm -rf)"}
	for _, in := range inputs {
		q := shellArg(in)
		// `printf '%s\n' <quoted>` should print exactly the original arg.
		script := "printf '%s\\n' " + q + "\n"
		out := runSH(t, script)
		if out != in {
			t.Errorf("shellArg(%q)=%q round-trips to %q, want %q", in, q, out, in)
		}
	}
}

func TestJoinShellArgs(t *testing.T) {
	got := joinShellArgs([]string{"llama-server", "--model", "my model.gguf", "--ctx-size", "4096"})
	want := "llama-server --model 'my model.gguf' --ctx-size 4096"
	if got != want {
		t.Errorf("joinShellArgs = %q, want %q", got, want)
	}
}

func TestFormatShellArgvLines(t *testing.T) {
	got := formatShellArgvLines([]string{"llama-server", "--model", "a b", "--ctx-size", "4096", "--metrics"})
	if len(got) != 4 {
		t.Fatalf("got %d lines, want 4: %q", len(got), got)
	}
	// Flag+value grouped, boolean flag alone, last line has no continuation.
	// The binary (line 0) is unindented; every param line is indented by
	// paramIndent spaces.
	ind := strings.Repeat(" ", paramIndent)
	wantContent := []string{"llama-server", ind + "--model 'a b'", ind + "--ctx-size 4096", ind + "--metrics"}
	for i, want := range wantContent {
		line := got[i]
		if i < len(got)-1 {
			if !strings.HasSuffix(line, " \\") {
				t.Errorf("line %d should end in a continuation backslash: %q", i, line)
			}
			// Strip the continuation and any right-alignment padding.
			line = strings.TrimRight(strings.TrimSuffix(line, " \\"), " ")
		} else if strings.HasSuffix(line, "\\") {
			t.Errorf("last line must not end in a backslash: %q", line)
		}
		if line != want {
			t.Errorf("line %d content = %q, want %q", i, line, want)
		}
	}
	// All continuation backslashes must be right-aligned to one column so a
	// rectangular selection to the command's right edge stays clean.
	w := 0
	for i := 0; i < len(got)-1; i++ {
		if i == 0 {
			w = len(got[i])
		} else if len(got[i]) != w {
			t.Errorf("continuation line %d is %d wide, want %d (\\ not aligned): %q", i, len(got[i]), w, got[i])
		}
	}
}

func TestFormatShellArgvLinesEmpty(t *testing.T) {
	if got := formatShellArgvLines(nil); got != nil {
		t.Errorf("empty argv should yield nil, got %q", got)
	}
}

func TestLaunchArgvReplacesBinary(t *testing.T) {
	r := &RunMode{argv: []string{"/usr/local/bin/llama-server", "--port", "8080"}}
	got := r.launchArgv()
	want := []string{"llama-server", "--port", "8080"}
	if len(got) != len(want) {
		t.Fatalf("got %d args, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q", i, got[i], want[i])
		}
	}
	// The original argv must be untouched.
	if r.argv[0] != "/usr/local/bin/llama-server" {
		t.Errorf("launchArgv mutated original argv[0]: %q", r.argv[0])
	}
}

// The no-clipboard dialog must show the advisory text and the one-param-per
// line command, in a terminal that is comfortably tall.
func TestRenderCopyNoClipboardShort(t *testing.T) {
	r := &RunMode{theme: DefaultTheme(), width: 100, height: 40}
	r.argv = []string{"/usr/bin/llama-server", "--model", "/home/me/my model.gguf", "--ctx-size", "4096", "--port", "8080"}
	r.copyCmdLines = formatShellArgvLines(r.launchArgv())
	r.copyResult = "No clipboard tool found"
	out := r.renderCopyNoClipboard()

	for _, want := range []string{
		"No clipboard tool found",
		"connected over SSH",
		"Copy the launch command below manually",
		"llama-server",
		"--model '/home/me/my model.gguf'",
		"--ctx-size 4096",
		"--port 8080",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q\n---\n%s", want, out)
		}
	}
	// A short command must NOT advertise scrolling.
	if strings.Contains(out, "scroll") {
		t.Errorf("short view should not show a scroll hint:\n%s", out)
	}
	// Borderless: the command sits at column 0 (no box border / indent) so
	// a native selection captures only the command. The first command line
	// must start exactly at column 0.
	lines := strings.Split(out, "\n")
	for _, l := range lines {
		if strings.Contains(l, "llama-server") {
			if !strings.HasPrefix(l, "llama-server") {
				t.Errorf("command should start at column 0, got %q", l)
			}
			break
		}
	}
	// The view fills exactly the terminal height (hint pinned to the bottom).
	if n := len(lines); n != r.height {
		t.Errorf("view is %d lines, want exactly the terminal height %d", n, r.height)
	}
}

// The header has a fixed shape: blank, error title, blank, advisory,
// blank, the "copy manually:" line, then two blank rows before the
// command starts on row 8.
func TestCopyNoClipboardHeaderLayout(t *testing.T) {
	r := &RunMode{theme: DefaultTheme(), width: 80, height: 30}
	r.argv = []string{"/usr/bin/llama-server", "--port", "8080"}
	r.copyCmdLines = formatShellArgvLines(r.launchArgv())
	r.copyResult = "No clipboard tool found"
	lines := strings.Split(r.renderCopyNoClipboard(), "\n")

	if lines[0] != "" {
		t.Errorf("row 0 should be blank, got %q", lines[0])
	}
	if !strings.Contains(lines[1], "No clipboard tool found") {
		t.Errorf("row 1 should be the error title, got %q", lines[1])
	}
	if lines[2] != "" {
		t.Errorf("row 2 should be blank, got %q", lines[2])
	}
	if !strings.Contains(lines[3], "connected over SSH") {
		t.Errorf("row 3 should be the advisory, got %q", lines[3])
	}
	if lines[4] != "" {
		t.Errorf("row 4 should be blank, got %q", lines[4])
	}
	if !strings.Contains(lines[5], "Copy the launch command below manually:") {
		t.Errorf("row 5 should read 'copy manually:', got %q", lines[5])
	}
	if lines[6] != "" || lines[7] != "" {
		t.Errorf("rows 6-7 should be blank, got %q / %q", lines[6], lines[7])
	}
	if !strings.HasPrefix(lines[8], "llama-server") {
		t.Errorf("command should start at row 8, got %q", lines[8])
	}
}

// A command taller than the terminal must be clipped to fit and become
// scrollable; scrolling must actually change what is shown.
func TestRenderCopyNoClipboardOverflowScrolls(t *testing.T) {
	argv := []string{"/bin/llama-server"}
	for i := 0; i < 60; i++ {
		argv = append(argv, fmt.Sprintf("--flag%d", i), "value")
	}
	r := &RunMode{theme: DefaultTheme(), width: 100, height: 20}
	r.copyCmdLines = formatShellArgvLines(argv)
	r.copyResult = "No clipboard tool found"

	out := r.renderCopyNoClipboard()
	lines := strings.Split(out, "\n")
	if len(lines) > r.height {
		t.Fatalf("overflow dialog is %d lines, taller than height %d", len(lines), r.height)
	}
	if !strings.Contains(out, "scroll") {
		t.Errorf("overflow dialog should show a scroll hint:\n%s", out)
	}
	if !strings.Contains(out, "--flag0") {
		t.Errorf("should show the first line at scroll 0:\n%s", out)
	}

	// Scroll down by 30 lines: the window now starts at line index 30
	// (binary is index 0, so --flag29 is index 30) — --flag29 should be
	// visible and --flag0 should have scrolled out of view.
	r.copyScroll = 30
	out2 := r.renderCopyNoClipboard()
	if out2 == out {
		t.Fatalf("scrolling changed nothing")
	}
	if !strings.Contains(out2, "--flag29") {
		t.Errorf("after scrolling, expected --flag29:\n%s", out2)
	}
	if strings.Contains(out2, "--flag0") {
		t.Errorf("--flag0 should have scrolled out of view:\n%s", out2)
	}
	// Still fits after scrolling.
	if n := len(strings.Split(out2, "\n")); n > r.height {
		t.Errorf("scrolled dialog is %d lines, taller than height %d", n, r.height)
	}
}

// Scroll clamping keeps the offset within the valid range.
func TestClampCopyScroll(t *testing.T) {
	argv := []string{"/bin/llama-server"}
	for i := 0; i < 60; i++ {
		argv = append(argv, fmt.Sprintf("--flag%d", i), "value")
	}
	r := &RunMode{theme: DefaultTheme(), width: 100, height: 20}
	r.copyCmdLines = formatShellArgvLines(argv)

	r.copyScroll = -5
	r.clampCopyScroll()
	if r.copyScroll != 0 {
		t.Errorf("negative scroll should clamp to 0, got %d", r.copyScroll)
	}

	r.copyScroll = 9999
	r.clampCopyScroll()
	v, _ := r.copyBlockMetrics()
	max := len(r.copyCmdLines) - v
	if r.copyScroll != max {
		t.Errorf("overflow scroll should clamp to %d, got %d", max, r.copyScroll)
	}
}

// Dismissal clears all transient copy state.
func TestDismissCopyResult(t *testing.T) {
	r := &RunMode{
		theme:        DefaultTheme(),
		width:        100,
		height:       40,
		copyResult:   "No clipboard tool found",
		copyCmdLines: []string{"a", "b"},
		copyScroll:   3,
		copyDragHeld: true,
	}
	r.dismissCopyResult()
	if r.copyResult != "" || r.copyCmdLines != nil || r.copyScroll != 0 || r.copyDragHeld {
		t.Errorf("dismissCopyResult left state: %+v", r)
	}
}

// overflowCopyRunMode builds a run mode whose command block overflows a
// height-20 terminal, for the scroll-behaviour tests.
func overflowCopyRunMode() *RunMode {
	argv := []string{"/bin/llama-server"}
	for i := 0; i < 60; i++ {
		argv = append(argv, fmt.Sprintf("--flag%d", i), "value")
	}
	r := &RunMode{theme: DefaultTheme(), width: 100, height: 20, copyResult: "No clipboard tool found"}
	r.copyCmdLines = formatShellArgvLines(argv)
	return r
}

func TestHandleCopyResultKeyDismiss(t *testing.T) {
	for _, k := range []tea.KeyMsg{{Type: tea.KeyEnter}, {Type: tea.KeyEsc}} {
		r := &RunMode{copyResult: "x"}
		r.handleCopyResultKey(k)
		if r.copyResult != "" {
			t.Errorf("key %q should dismiss the modal", k.String())
		}
	}
}

// The modal no longer closes on an arbitrary key, so j/k/↑/↓ stay free for
// scrolling and a stray letter must not dismiss.
func TestHandleCopyResultKeyIgnoresOtherKeys(t *testing.T) {
	r := &RunMode{copyResult: "x"}
	r.handleCopyResultKey(keyRunes("a"))
	if r.copyResult == "" {
		t.Error("an arbitrary key must not dismiss the copy modal")
	}
}

func TestHandleCopyResultKeyScrollsOnlyWhenOverflow(t *testing.T) {
	r := overflowCopyRunMode()
	r.handleCopyResultKey(keyRunes("j"))
	if r.copyScroll != 1 {
		t.Errorf("j should scroll to 1, got %d", r.copyScroll)
	}
	r.handleCopyResultKey(keyRunes("k"))
	if r.copyScroll != 0 {
		t.Errorf("k should scroll back to 0, got %d", r.copyScroll)
	}
	r.handleCopyResultKey(tea.KeyMsg{Type: tea.KeyDown})
	if r.copyScroll != 1 {
		t.Errorf("down should scroll to 1, got %d", r.copyScroll)
	}
	// Page down from 1 jumps a full visible window; page up returns.
	v, _ := r.copyBlockMetrics()
	r.handleCopyResultKey(tea.KeyMsg{Type: tea.KeyPgDown})
	if r.copyScroll != 1+v {
		t.Errorf("pgdown from 1 should be %d, got %d", 1+v, r.copyScroll)
	}
	r.handleCopyResultKey(tea.KeyMsg{Type: tea.KeyPgUp})
	if r.copyScroll != 1 {
		t.Errorf("pgup should return to 1, got %d", r.copyScroll)
	}

	// Non-overflow: movement keys are no-ops.
	r2 := &RunMode{theme: DefaultTheme(), width: 100, height: 40, copyResult: "x"}
	r2.copyCmdLines = []string{"a \\", "b"}
	r2.handleCopyResultKey(keyRunes("j"))
	if r2.copyScroll != 0 {
		t.Errorf("scroll should be a no-op without overflow, got %d", r2.copyScroll)
	}
}

func TestHandleCopyResultMouseWheel(t *testing.T) {
	r := overflowCopyRunMode()
	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown})
	if r.copyScroll != 1 {
		t.Errorf("wheel down should scroll to 1, got %d", r.copyScroll)
	}
	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if r.copyScroll != 0 {
		t.Errorf("wheel up should scroll to 0, got %d", r.copyScroll)
	}
}

// A left-button drag that moves past the visible edge of the block
// auto-scrolls it (rubber-band select of a tall command).
func TestHandleCopyResultMouseDragAutoScroll(t *testing.T) {
	r := overflowCopyRunMode()
	top, bottom := r.copyBlockScreenYRange()

	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, Y: 5})
	if !r.copyDragHeld {
		t.Fatal("press should arm the drag")
	}
	// Move above the visible top edge: scroll up (clamps to 0).
	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionMotion, Y: top - 1})
	// Move below the visible bottom edge: scroll down.
	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionMotion, Y: bottom + 1})
	if r.copyScroll != 1 {
		t.Errorf("drag past bottom edge should scroll to 1, got %d", r.copyScroll)
	}
	// Releasing stops auto-scroll.
	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionRelease, Button: tea.MouseButtonLeft, Y: bottom + 1})
	before := r.copyScroll
	r.handleCopyResultMouse(tea.MouseMsg{Action: tea.MouseActionMotion, Y: bottom + 1})
	if r.copyScroll != before {
		t.Errorf("released drag should not auto-scroll, got %d", r.copyScroll)
	}
}
