package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// testCommit is a valid 40-hex refs/main value (same fixture shape as
// internal/storage/scan_test.go).
const testCommit = "68c3ea2061e8c7688455fab07597dde0f4d7f0db"

// mkHubRepo builds a hub-layout cache repo under root (refs/main + one
// snapshot dir holding the given files as plain files — enough for
// storage.Scan to classify and list them).
func mkHubRepo(t *testing.T, root, repoID string, files ...string) {
	t.Helper()
	repoDir := filepath.Join(root, storage.RepoFolderNames(repoID)[0])
	if err := os.MkdirAll(filepath.Join(repoDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "refs", "main"), []byte(testCommit), 0o644); err != nil {
		t.Fatal(err)
	}
	snapDir := filepath.Join(repoDir, "snapshots", testCommit)
	for _, f := range files {
		if err := os.MkdirAll(snapDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(snapDir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// cfgModel adapts *ConfigMode to tea.Model for drainCmds (ConfigMode
// itself has no Init — it is a sub-mode, not a root tea.Model).
type cfgModel struct{ *ConfigMode }

func (cfgModel) Init() tea.Cmd { return nil }
func (m cfgModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.ConfigMode.Update(msg)
	return cfgModel{next}, cmd
}

// driveInit drains a cmd (e.g. installForm's Init batch) through the
// model — forms need their Init sequenceMsg drained or they stay
// unfocused (§16.4 gotcha).
func driveInit(t *testing.T, c *ConfigMode, cmd tea.Cmd) *ConfigMode {
	t.Helper()
	m := drainCmds(cfgModel{c}, cmd, 0)
	return m.(cfgModel).ConfigMode
}

// drive sends one message through the model and drains every resulting
// command (feeding each produced message back), mirroring the tea loop.
func drive(t *testing.T, c *ConfigMode, msg tea.Msg) *ConfigMode {
	t.Helper()
	next, cmd := c.Update(msg)
	c = next
	return driveInit(t, c, cmd)
}

// newModelFormConfig builds a ConfigMode over an empty config with a
// known theme and size.
func newModelFormConfig(t *testing.T, cfg *config.Config) *ConfigMode {
	t.Helper()
	cm := NewConfigMode(filepath.Join(t.TempDir(), "config.json"), cfg, DefaultTheme())
	cm.SetSize(120, 40)
	return &cm
}

// openModelFormToValue drives the model form from scratch to the value
// input for the given source: type an alias, advance, select the
// source, advance again.
func openModelFormToValue(t *testing.T, c *ConfigMode, source string) *ConfigMode {
	t.Helper()
	c = driveInit(t, c, c.openNewModelForm())
	c = drive(t, c, keyRunes("mymodel"))
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // alias → source
	if source == sourceHF {
		c = drive(t, c, tea.KeyMsg{Type: tea.KeyDown}) // local → hf
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // → value input
	return c
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// TestPickerInputHotkey checks the ctrl+o trigger: it emits
// openModelPickerMsg without touching the input value, other keys
// delegate to the embedded input, and the wrapper survives Update (the
// group stores whatever Update returns).
func TestPickerInputHotkey(t *testing.T) {
	val := strPtr("")
	pi := wrapPickerInput(huh.NewInput(), sourceLocal, val)

	m, cmd := pi.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	pi, ok := m.(*pickerInput)
	if !ok {
		t.Fatalf("Update returned %T, want *pickerInput (wrapper must survive)", m)
	}
	msg := safeCmd(cmd)
	om, ok := msg.(openModelPickerMsg)
	if !ok {
		t.Fatalf("ctrl+o emitted %T, want openModelPickerMsg", msg)
	}
	if om.kind != sourceLocal {
		t.Errorf("kind = %q, want %q", om.kind, sourceLocal)
	}
	if *val != "" {
		t.Errorf("hotkey mutated the value to %q", *val)
	}

	// Non-hotkey keys delegate to the embedded input: typing updates
	// the input's value (checked via Blur, which syncs accessor ←
	// textinput) and keeps the wrapper. The input must be focused for
	// the textinput to accept keys.
	pi2 := wrapPickerInput(huh.NewInput(), sourceLocal, strPtr(""))
	pi2.Focus()
	m2, _ := pi2.Update(keyRunes("m"))
	if _, ok := m2.(*pickerInput); !ok {
		t.Fatalf("typing returned %T, want *pickerInput", m2)
	}
	pi2.Blur()
	if got, _ := pi2.GetValue().(string); got != "m" {
		t.Errorf("typed value = %q, want %q", got, "m")
	}

	// KeyBinds advertise the hotkey.
	found := false
	for _, b := range pi.KeyBinds() {
		if b.Help().Key == "ctrl+o" {
			found = true
		}
	}
	if !found {
		t.Error("KeyBinds does not include ctrl+o")
	}
}

// TestPickerStartDir covers the §16.5 start-directory resolution:
// models-dir wins when it exists, then the current value's dir, then
// the first local model's dir, then HOME; nonexistent candidates fall
// through.
func TestPickerStartDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	modelsDir := t.TempDir()
	curDir := t.TempDir()
	modelDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nope")

	cases := []struct {
		name      string
		modelsDir string
		current   string
		models    []config.Model
		want      string
	}{
		{"models-dir wins", modelsDir, filepath.Join(curDir, "x.gguf"),
			[]config.Model{{Location: filepath.Join(modelDir, "m.gguf")}}, modelsDir},
		{"current value dir when models-dir missing", missing,
			filepath.Join(curDir, "x.gguf"), nil, curDir},
		{"first local model dir", "", "", []config.Model{{Location: filepath.Join(modelDir, "m.gguf")}}, modelDir},
		{"home fallback", "", "", nil, home},
		{"missing current dir falls back to home", "", filepath.Join(t.TempDir(), "nope", "x.gguf"), nil, home},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pickerStartDir(tc.modelsDir, tc.current, tc.models); got != tc.want {
				t.Errorf("pickerStartDir = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPrefillRepo covers the §3.8 pre-fill rule: one cached quant →
// org/repo:QUANT, several (or none) → bare org/repo.
func TestPrefillRepo(t *testing.T) {
	if got := prefillRepo("a/b", []string{"Q4_K_M"}); got != "a/b:Q4_K_M" {
		t.Errorf("single quant = %q", got)
	}
	if got := prefillRepo("a/b", []string{"Q4_K_M", "Q8_0"}); got != "a/b" {
		t.Errorf("multi quant = %q", got)
	}
	if got := prefillRepo("a/b", nil); got != "a/b" {
		t.Errorf("no quant = %q", got)
	}
}

// TestModelFormLocalPickerFlow is the end-to-end local branch: open the
// model form, drive to the location field, ctrl+o opens the .gguf
// filepicker at the resolved start dir, picking a file pre-fills the
// staging pointer and the rendered input; the form stays open on the
// same field.
func TestModelFormLocalPickerFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "model.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceLocal)

	// ctrl+o opens the filepicker; start dir falls back to HOME.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("ctrl+o did not open the local picker")
	}
	if c.modelPicker.kind != sourceLocal {
		t.Fatalf("picker kind = %q, want %q", c.modelPicker.kind, sourceLocal)
	}
	if got := c.modelPicker.fp.CurrentDirectory; got != home {
		t.Errorf("start dir = %q, want %q", got, home)
	}
	if !strings.Contains(c.View(), "model.gguf") {
		t.Error("filepicker view does not list model.gguf")
	}

	// Enter selects the first file (alphabetical: model.gguf < notes.txt).
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.modelPicker != nil {
		t.Fatal("overlay did not close after picking")
	}
	want := filepath.Join(home, "model.gguf")
	if got := deref(c.formStaging.location); got != want {
		t.Errorf("staged location = %q, want %q", got, want)
	}
	// The input shows the picked path (RefreshValue worked). Long temp
	// paths clip in the narrow input, so assert on the visible tail.
	if !strings.Contains(c.View(), "model.gguf") {
		t.Error("location input does not show the picked path (RefreshValue missing?)")
	}
	if c.form == nil {
		t.Fatal("model form was dismissed by the picker")
	}
}

// TestModelFormPickerEscCancels checks that Esc at the picker root
// cancels without touching the staged value.
func TestModelFormPickerEscCancels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceLocal)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("local picker did not open")
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEsc})
	if c.modelPicker != nil {
		t.Fatal("overlay did not close on esc")
	}
	if got := deref(c.formStaging.location); got != "" {
		t.Errorf("staged location = %q after cancel, want empty", got)
	}
	if c.form == nil {
		t.Fatal("model form was dismissed by the cancel")
	}
}

// TestModelFormPickerDisabledFile checks that selecting a non-.gguf
// file is a no-op with a brief error line, not a selection.
func TestModelFormPickerDisabledFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "notes.txt"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceLocal)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.modelPicker == nil {
		t.Fatal("overlay closed on a disabled file")
	}
	if c.modelPicker.errLine == "" {
		t.Error("no error line after selecting a non-.gguf file")
	}
	if !strings.Contains(c.View(), ".gguf files only") {
		t.Error("view does not surface the .gguf-only error")
	}
}

// TestModelFormHFRepoPickerFlow covers the HF branch: a single cached
// quant pre-fills org/repo:QUANT, the repo list renders quants+sizes,
// and picking returns to the form on the same field.
func TestModelFormHFRepoPickerFlow(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "Qwen/Qwen3-32B-GGUF", "model-Q4_K_M.gguf")

	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})

	if c.modelPicker == nil {
		t.Fatal("ctrl+o did not open the repo picker")
	}
	if c.modelPicker.kind != sourceHF {
		t.Fatalf("picker kind = %q, want %q", c.modelPicker.kind, sourceHF)
	}
	items := c.modelPicker.repos.list.VisibleItems()
	if len(items) != 1 {
		t.Fatalf("repo list has %d rows, want 1", len(items))
	}
	it, ok := items[0].(repoItem)
	if !ok {
		t.Fatalf("row is %T, want repoItem", items[0])
	}
	if it.repo != "Qwen/Qwen3-32B-GGUF" || it.detail == "" || !strings.Contains(it.detail, "Q4_K_M") {
		t.Errorf("repo row = %q / %q", it.repo, it.detail)
	}
	if !strings.Contains(c.View(), "Qwen/Qwen3-32B-GGUF") {
		t.Error("repo picker view does not render the repo row")
	}

	// Pick the single-quant repo → org/repo:QUANT.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.modelPicker != nil {
		t.Fatal("overlay did not close after picking")
	}
	if got := deref(c.formStaging.hf); got != "Qwen/Qwen3-32B-GGUF:Q4_K_M" {
		t.Errorf("staged hf = %q, want org/repo:QUANT", got)
	}
	if !strings.Contains(c.View(), "Qwen/Qwen3-32B-GGUF:Q4_K_M") {
		t.Error("HF input does not show the picked id (RefreshValue missing?)")
	}
}

// TestModelFormHFMultiQuantPrefill checks the several-quants rule:
// picking a repo with two cached quants pre-fills bare org/repo.
func TestModelFormHFMultiQuantPrefill(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "Qwen/Qwen3-32B-GGUF", "model-Q4_K_M.gguf", "model-Q8_0.gguf")
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("repo picker did not open")
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if got := deref(c.formStaging.hf); got != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("staged hf = %q, want bare org/repo", got)
	}
}

// TestModelFormHFNewRepoRow checks the pinned "type a new repo…" row:
// stepping past the last repo selects it, and Enter there keeps the
// field as typed (no change).
func TestModelFormHFNewRepoRow(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "Qwen/Qwen3-32B-GGUF", "model-Q4_K_M.gguf")
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("repo picker did not open")
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyDown})
	if !c.modelPicker.repos.newRepo {
		t.Fatal("down past the last repo did not select the new-repo row")
	}
	if !strings.Contains(c.View(), repoTypeNew) {
		t.Error("view does not render the new-repo row")
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.modelPicker != nil {
		t.Fatal("overlay did not close on new-repo pick")
	}
	if got := deref(c.formStaging.hf); got != "" {
		t.Errorf("staged hf = %q after new-repo row, want untouched", got)
	}
}

// TestModelFormPickerDirNavigation checks that entering a directory
// navigates into it instead of pre-filling it as the location
// (DirAllowed=false: dirs are navigable but never selectable).
func TestModelFormPickerDirNavigation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "sub", "inner.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceLocal)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("local picker did not open")
	}

	// Enter on the directory navigates; the overlay stays open and no
	// value is staged.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.modelPicker == nil {
		t.Fatal("overlay closed when entering a directory")
	}
	if got := deref(c.formStaging.location); got != "" {
		t.Errorf("staged location = %q after dir enter, want empty", got)
	}
	if c.modelPicker.fp.CurrentDirectory != filepath.Join(home, "sub") {
		t.Errorf("CurrentDirectory = %q, want %q", c.modelPicker.fp.CurrentDirectory, filepath.Join(home, "sub"))
	}
	if !strings.Contains(c.View(), "inner.gguf") {
		t.Error("picker did not list the file inside the directory")
	}

	// Enter again picks the file inside it.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.modelPicker != nil {
		t.Fatal("overlay did not close after picking inside the directory")
	}
	if got := deref(c.formStaging.location); got != filepath.Join(home, "sub", "inner.gguf") {
		t.Errorf("staged location = %q", got)
	}
}

// TestModelFormHFRepoFilterResetsNewRepoRow checks that filtering after
// stepping onto the synthetic "type a new repo…" row deselects it: the
// cursor is back on the real rows, so enter picks the filtered repo.
func TestModelFormHFRepoFilterResetsNewRepoRow(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "Alpha/First", "model-Q4_K_M.gguf")
	mkHubRepo(t, root, "Beta/Second", "model-Q4_K_M.gguf")
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("repo picker did not open")
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyDown}) // cursor → last repo
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyDown}) // step onto the new-repo row
	if !c.modelPicker.repos.newRepo {
		t.Fatal("down past the last repo did not select the new-repo row")
	}
	// Typing a filter moves the cursor: the synthetic selection drops.
	// Each rune is its own KeyMsg, as a real terminal delivers them.
	for _, r := range "beta" {
		c = drive(t, c, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if c.modelPicker.repos.newRepo {
		t.Fatal("new-repo row still selected after filtering")
	}
	// Live filter: the single enter picks the filtered repo.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if got := deref(c.formStaging.hf); got != "Beta/Second:Q4_K_M" {
		t.Errorf("staged hf = %q, want the filtered repo", got)
	}
	if c.modelPicker != nil {
		t.Fatal("overlay did not close after picking")
	}
}

// TestModelFormPickerHiddenFiles checks the owner feedback: hidden
// files/dirs are shown by default, "." toggles them off and on
// (re-reading the current directory each time), and a shown hidden
// .gguf is selectable.
func TestModelFormPickerHiddenFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "model.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".hidden.gguf"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Version: 1, Globals: config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080}}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceLocal)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("local picker did not open")
	}
	if !c.modelPicker.fp.ShowHidden {
		t.Error("hidden files should be shown by default")
	}
	if !strings.Contains(c.View(), ".hidden.gguf") {
		t.Error("hidden file not listed while hidden shown")
	}

	// "." toggles hidden off.
	c = drive(t, c, keyRunes("."))
	if c.modelPicker == nil {
		t.Fatal("overlay closed on the toggle key")
	}
	if c.modelPicker.fp.ShowHidden {
		t.Error("toggle did not switch hidden off")
	}
	if strings.Contains(c.View(), ".hidden.gguf") {
		t.Error("hidden file still listed after toggle off")
	}
	if !strings.Contains(c.View(), "hidden off") {
		t.Error("hint does not show hidden off state")
	}

	// "." again restores them.
	c = drive(t, c, keyRunes("."))
	if !c.modelPicker.fp.ShowHidden {
		t.Error("toggle did not switch hidden back on")
	}
	if !strings.Contains(c.View(), ".hidden.gguf") {
		t.Error("hidden file not restored after toggle on")
	}
}

// TestModelFormHFWideList checks the owner feedback: the HF repo list
// is sized to nearly the full screen width so long org/repo ids and
// their quant lists stay on one line.
func TestModelFormHFWideList(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "DavidAU/Qwen3.6-27B-Fable-Fusion-711-Uncensored-Heretic-NM-DAU-NEO-MAX-MTP-GGUF",
		"model-Q4_K_S.gguf", "model-Q4_K_M.gguf")
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("repo picker did not open")
	}
	if got := c.modelPicker.repos.list.Width(); got != c.width-6 {
		t.Errorf("repo list width = %d, want %d (screen width - 6)", got, c.width-6)
	}
	// The long id and both quants render on a single line each.
	view := c.View()
	for _, want := range []string{
		"DavidAU/Qwen3.6-27B-Fable-Fusion-711-Uncensored-Heretic-NM-DAU-NEO-MAX-MTP-GGUF",
		"Q4_K_S", "Q4_K_M",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("repo picker view missing %q", want)
		}
	}
}

// TestModelFormHFFixedBoxWidth checks the owner round-3 feedback: the
// enclosing selector rectangle spans the full screen width and its
// size does not change when the selection moves (the selected row used
// to render 2 cells narrower, shifting the box).
func TestModelFormHFFixedBoxWidth(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "Alpha/First", "model-Q4_K_M.gguf")
	mkHubRepo(t, root, "Beta/Second", "model-Q4_K_M.gguf")
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker == nil {
		t.Fatal("repo picker did not open")
	}
	boxWidth := func() int {
		for _, l := range strings.Split(c.View(), "\n") {
			if strings.Contains(l, "╭") { // the box's top border spans its width
				return lipgloss.Width(l)
			}
		}
		t.Fatal("no box border found in view")
		return 0
	}
	w1 := boxWidth()
	if w1 != c.width {
		t.Errorf("box width = %d, want full screen %d", w1, c.width)
	}
	// Moving the selection must not change the box width.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyDown})
	if w2 := boxWidth(); w2 != w1 {
		t.Errorf("box width changed on selection move: %d → %d", w1, w2)
	}
}

// TestModelFormHFEmptyCacheSkipsPicker checks §3.8: an empty cache
// makes ctrl+o a no-op — the field stays a plain free-type input.
func TestModelFormHFEmptyCacheSkipsPicker(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	}
	c := openModelFormToValue(t, newModelFormConfig(t, cfg), sourceHF)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyCtrlO})
	if c.modelPicker != nil {
		t.Fatal("picker opened despite an empty cache")
	}
	if c.form == nil {
		t.Fatal("model form was dismissed")
	}
}
