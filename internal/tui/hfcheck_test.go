package tui

// Tests for the §16.6 typed-repo check + quant offer (DESIGN §16.6 /
// ROADMAP §3.8 step B): the pickerInput enter-intercept, the hfCheck
// adapter, the shared quant chooser shape, and the ConfigMode flow
// (checking overlay → chooser → save; distinct non-blocking failure
// flashes).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/hf"
)

// stubHFCheck is a deterministic, injectable hfCheckRunner. When
// cancel is set the stub blocks until the ctx is done and reports
// ctx.Err() — the esc-cancel path.
type stubHFCheck struct {
	opts   []hf.QuantOption
	mmproj bool
	err    error
	cancel bool
	repo   string // last repo checked
}

func (s *stubHFCheck) CheckHF(ctx context.Context, repo string) ([]hf.QuantOption, bool, error) {
	s.repo = repo
	if s.cancel {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	return s.opts, s.mmproj, s.err
}

// TestPickerInputHFCheckTrigger covers the §16.6 enter-intercept: a
// typed, valid, bare id on the HF field emits hfCheckRequestedMsg
// without advancing; everything else delegates to the embedded input.
func TestPickerInputHFCheckTrigger(t *testing.T) {
	type want struct {
		check   bool   // true: enter must emit hfCheckRequestedMsg
		checkID string // expected id when check
	}
	cases := []struct {
		name   string
		kind   string
		typed  string
		edited bool // pre-set the edited flag (e.g. from an earlier keystroke)
		want   want
	}{
		{
			name: "typed bare id", kind: sourceHF, typed: "Qwen/Qwen3-32B-GGUF",
			want: want{check: true, checkID: "Qwen/Qwen3-32B-GGUF"},
		},
		{
			name: "typed id with surrounding spaces", kind: sourceHF, typed: "  Qwen/Qwen3-32B-GGUF  ",
			want: want{check: true, checkID: "Qwen/Qwen3-32B-GGUF"},
		},
		{name: "not edited", kind: sourceHF, typed: "", want: want{}},
		{name: "edited but empty (deleted everything)", kind: sourceHF, typed: "", edited: true, want: want{}},
		{name: "quanted id is explicit", kind: sourceHF, typed: "Qwen/Qwen3-32B-GGUF:Q4_K_M", want: want{}},
		{name: "invalid id delegates", kind: sourceHF, typed: "not a repo", want: want{}},
		{name: "local field never checks", kind: sourceLocal, typed: "/m/model.gguf", want: want{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val := strPtr("")
			// The embedded input must be bound to the staging pointer
			// (as buildModelForm does via .Value) for the accessor to
			// sync typed text into it.
			pi := wrapPickerInput(huh.NewInput().Value(val), tc.kind, val)
			pi.edited = tc.edited
			pi.Focus()
			if tc.typed != "" {
				_, _ = pi.Update(keyRunes(tc.typed))
			}
			m, cmd := pi.Update(tea.KeyMsg{Type: tea.KeyEnter})
			pi, ok := m.(*pickerInput)
			if !ok {
				t.Fatalf("Update returned %T, want *pickerInput", m)
			}
			msg := safeCmd(cmd)
			req, isCheck := msg.(hfCheckRequestedMsg)
			if tc.want.check != isCheck {
				t.Fatalf("enter check = %v, want %v (msg %T)", isCheck, tc.want.check, msg)
			}
			if tc.want.check {
				if req.id != tc.want.checkID {
					t.Errorf("check id = %q, want %q", req.id, tc.want.checkID)
				}
				// The intercept must not mutate the value.
				if *val != tc.typed {
					t.Errorf("value mutated by the intercept: %q", *val)
				}
			}
		})
	}
}

// TestHFCheckClient checks the production adapter: one Tree round trip
// yields quants + mmproj, with directories skipped and lfs sizes used.
func TestHFCheckClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api/models/Qwen/Qwen3-32B-GGUF/tree/main" {
			t.Errorf("path = %q", got)
		}
		if got := r.URL.Query().Get("recursive"); got != "true" {
			t.Errorf("recursive = %q, want true", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{"type": "file", "path": "qwen3-32b-Q4_K_M.gguf", "lfs": map[string]any{"size": 100, "oid": "aabb"}},
			{"type": "file", "path": "qwen3-32b-Q8_0.gguf", "size": 200, "oid": "ccdd"},
			{"type": "file", "path": "mmproj-vision.mmproj", "size": 50, "oid": "eeff"},
			{"type": "directory", "path": "subdir"},
		})
	}))
	defer srv.Close()

	opts, mmproj, err := hfCheckClient{hf.NewWithEndpoint(srv.URL, "")}.
		CheckHF(context.Background(), "Qwen/Qwen3-32B-GGUF")
	if err != nil {
		t.Fatalf("CheckHF: %v", err)
	}
	if len(opts) != 2 {
		t.Fatalf("quants = %d, want 2", len(opts))
	}
	if opts[0].Tag != "Q4_K_M" || opts[0].Size != 100 || opts[0].Files[0].OID != "aabb" {
		t.Errorf("first quant = %+v (lfs size/oid not honored?)", opts[0])
	}
	if opts[1].Tag != "Q8_0" || opts[1].Size != 200 {
		t.Errorf("second quant = %+v", opts[1])
	}
	if !mmproj {
		t.Error("mmproj not detected")
	}
}

// formAdapter adapts a *huh.Form to tea.Model so drainCmds can pump its
// Init focus msgs (a select renders empty until focused).
type formAdapter struct{ *huh.Form }

func (formAdapter) Init() tea.Cmd { return nil }
func (f formAdapter) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := f.Form.Update(msg)
	if form, ok := next.(*huh.Form); ok {
		return formAdapter{form}, cmd
	}
	return f, cmd
}

// TestHFCheckClientCancel verifies the esc-cancel path surfaces as
// context.Canceled even though the §16.2 client wraps the transport
// error into an hf.Error without Unwrap/Is (hfcheck.CheckHF re-raises
// ctx.Err()). Without this, esc on the checking overlay would flash
// "could not reach Hugging Face" and still save the model.
func TestHFCheckClientCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the request open until the test cancels
	}))
	defer srv.Close()

	done := make(chan error, 1)
	go func() {
		_, _, err := hfCheckClient{hf.NewWithEndpoint(srv.URL, "")}.CheckHF(ctx, "Qwen/Qwen3-32B-GGUF")
		done <- err
	}()
	cancel()
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("CheckHF after cancel = %v, want context.Canceled", err)
	}
}

// TestQuantChooserFormShape checks the shared chooser's options: sizes,
// (cached) markers, the informational note, and the keep-bare row only
// when requested.
func TestQuantChooserFormShape(t *testing.T) {
	opts := []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}, {Tag: "Q8_0", Size: 200}}
	val := ""
	form := quantChooserForm("org/repo", opts, map[string]bool{"Q4_K_M": true},
		"mmproj present — llama.cpp auto-downloads it", &val, true).
		WithTheme(configHuhTheme(DefaultTheme())).
		WithWidth(60)
	m := drainCmds(formAdapter{form}, form.Init(), 0)
	view := m.(formAdapter).View()
	for _, want := range []string{
		"org/repo",
		"Q4_K_M — " + hf.HumanSize(100),
		"Q8_0 — " + hf.HumanSize(200),
		"(cached)",
		"mmproj present",
		"keep org/repo (no quant)",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("chooser view missing %q:\n%s", want, view)
		}
	}

	val2 := ""
	storageForm := quantChooserForm("org/repo", opts, nil, "", &val2, false).
		WithTheme(configHuhTheme(DefaultTheme())).
		WithWidth(60)
	m2 := drainCmds(formAdapter{storageForm}, storageForm.Init(), 0)
	if strings.Contains(m2.(formAdapter).View(), "keep org/repo") {
		t.Error("keep-bare row present when keepBare=false (storage variant)")
	}
}

// hfFlowConfig builds a ConfigMode with a stub check runner over an
// empty config.
func hfFlowConfig(t *testing.T, stub *stubHFCheck) *ConfigMode {
	t.Helper()
	c := newModelFormConfig(t, &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: t.TempDir()},
	})
	if stub != nil {
		c.SetHFCheckRunner(stub)
	}
	return c
}

// typeBareRepo drives the model form to the HF field and types a bare
// repo id there.
func typeBareRepo(t *testing.T, c *ConfigMode) *ConfigMode {
	t.Helper()
	c = openModelFormToValue(t, c, sourceHF)
	return drive(t, c, keyRunes("Qwen/Qwen3-32B-GGUF"))
}

// startCheck sends the confirming enter and returns the state with the
// check in flight (hfCheck set, checking overlay active).
func startCheck(t *testing.T, c *ConfigMode) (*ConfigMode, tea.Cmd) {
	t.Helper()
	next, cmd := c.Update(tea.KeyMsg{Type: tea.KeyEnter})
	c = next
	req, ok := safeCmd(cmd).(hfCheckRequestedMsg)
	if !ok {
		t.Fatalf("enter did not emit hfCheckRequestedMsg (got %T)", safeCmd(cmd))
	}
	if req.id != "Qwen/Qwen3-32B-GGUF" {
		t.Fatalf("check id = %q", req.id)
	}
	next, cmd = c.Update(req)
	c = next
	if c.hfCheck == nil {
		t.Fatal("check did not start (runner missing or id rejected)")
	}
	if c.hfCheck.repo != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("check repo = %q", c.hfCheck.repo)
	}
	if !strings.Contains(c.View(), "checking Qwen/Qwen3-32B-GGUF…") {
		t.Error("checking overlay not rendered")
	}
	return c, cmd
}

// TestConfigHFCheckHappyPath: typed bare id → enter → checking overlay
// → chooser with real sizes → pick the first quant → model saved with
// the :quant suffix.
func TestConfigHFCheckHappyPath(t *testing.T) {
	stub := &stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}, {Tag: "Q8_0", Size: 200}}}
	c := typeBareRepo(t, hfFlowConfig(t, stub))

	c, checkCmd := startCheck(t, c)
	next, cmd := c.Update(safeCmd(checkCmd)) // hfCheckDoneMsg → chooser
	c = next
	if c.hfQuant == nil {
		t.Fatal("chooser did not open after a successful check")
	}
	c = driveInit(t, c, cmd) // chooser Init (focus msgs)
	view := c.View()
	for _, want := range []string{"Q4_K_M — " + hf.HumanSize(100), "Q8_0 — " + hf.HumanSize(200), "keep Qwen/Qwen3-32B-GGUF (no quant)"} {
		if !strings.Contains(view, want) {
			t.Errorf("chooser view missing %q", want)
		}
	}
	if stub.repo != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("runner saw repo %q", stub.repo)
	}

	// Pick the first quant (Q4_K_M, sorted first).
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.hfQuant != nil {
		t.Fatal("chooser did not close on pick")
	}
	if c.form != nil {
		t.Fatal("model form did not complete after the pick")
	}
	if len(c.work.Models) != 1 || c.work.Models[0].HF != "Qwen/Qwen3-32B-GGUF:Q4_K_M" {
		t.Errorf("saved model = %+v, want HF org/repo:Q4_K_M", c.work.Models)
	}
}

// TestConfigHFCheckKeepBare: the trailing keep-bare row saves the id
// without a quant suffix.
func TestConfigHFCheckKeepBare(t *testing.T) {
	stub := &stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}}}
	c := typeBareRepo(t, hfFlowConfig(t, stub))
	c, checkCmd := startCheck(t, c)
	next, cmd := c.Update(safeCmd(checkCmd))
	c = next
	c = driveInit(t, c, cmd)
	// Down onto the keep-bare row, enter.
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyDown})
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if len(c.work.Models) != 1 || c.work.Models[0].HF != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("saved model = %+v, want bare org/repo", c.work.Models)
	}
}

// TestConfigHFCheckChooserEsc: esc on the chooser returns to the HF
// field with nothing committed.
func TestConfigHFCheckChooserEsc(t *testing.T) {
	stub := &stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}}}
	c := typeBareRepo(t, hfFlowConfig(t, stub))
	c, checkCmd := startCheck(t, c)
	next, cmd := c.Update(safeCmd(checkCmd))
	c = next
	c = driveInit(t, c, cmd)
	if c.hfQuant == nil {
		t.Fatal("chooser did not open")
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEsc})
	if c.hfQuant != nil {
		t.Fatal("esc did not close the chooser")
	}
	if c.form == nil {
		t.Fatal("esc on the chooser dismissed the model form — must stay on the HF field")
	}
	if len(c.work.Models) != 0 {
		t.Fatalf("model added while chooser was open: %+v", c.work.Models)
	}
	if got := deref(c.formStaging.hf); got != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("staged hf = %q after chooser esc, want untouched bare id", got)
	}
}

// TestConfigHFCheckCancel: esc during the in-flight check cancels it —
// no flash, no chooser, the form stays on the field.
func TestConfigHFCheckCancel(t *testing.T) {
	stub := &stubHFCheck{cancel: true}
	c := typeBareRepo(t, hfFlowConfig(t, stub))
	c, checkCmd := startCheck(t, c)

	// User hits esc while the check cmd is still running.
	next, _ := c.Update(tea.KeyMsg{Type: tea.KeyEsc})
	c = next
	if c.hfCheck != nil {
		t.Fatal("esc did not cancel the check")
	}

	// The cmd returns ctx.Canceled (the stub blocks on ctx.Done).
	next, _ = c.Update(safeCmd(checkCmd))
	c = next
	if c.flash != "" {
		t.Errorf("cancel flashed %q, want no flash", c.flash)
	}
	if c.hfQuant != nil {
		t.Fatal("cancel opened the chooser")
	}
	if c.form == nil {
		t.Fatal("cancel dismissed the model form — must stay on the HF field")
	}
	if len(c.work.Models) != 0 {
		t.Fatalf("model added after cancel: %+v", c.work.Models)
	}
}

// TestConfigHFCheckFailures: every failure path flashes a distinct,
// non-blocking message and still completes the form, saving the bare
// id as-is (ROADMAP §3.8: "the id can still be saved").
func TestConfigHFCheckFailures(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		opts   []hf.QuantOption
		mmproj bool
		flash  string
	}{
		{name: "not found", err: &hf.Error{Kind: hf.ErrNotFound, Status: 404},
			flash: "Qwen/Qwen3-32B-GGUF: not found on Hugging Face"},
		{name: "gated", err: &hf.Error{Kind: hf.ErrGated, Status: 401},
			flash: "Qwen/Qwen3-32B-GGUF: gated — requires HF_TOKEN"},
		{name: "network", err: errors.New("dial tcp: connection refused"),
			flash: "Qwen/Qwen3-32B-GGUF: could not reach Hugging Face"},
		{name: "http other", err: &hf.Error{Kind: hf.ErrHTTP, Status: 502, Message: "bad gateway"},
			flash: "Qwen/Qwen3-32B-GGUF: HTTP 502"},
		{name: "no quants", flash: "Qwen/Qwen3-32B-GGUF: no GGUF files found"},
		{name: "mmproj only", opts: nil, mmproj: true,
			flash: "Qwen/Qwen3-32B-GGUF: no GGUF files found (mmproj only)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubHFCheck{err: tc.err, opts: tc.opts, mmproj: tc.mmproj}
			c := typeBareRepo(t, hfFlowConfig(t, stub))
			// One drive runs the whole chain: enter → check → flash →
			// form completion → applyForm.
			c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
			if c.flash != tc.flash {
				t.Errorf("flash = %q, want %q", c.flash, tc.flash)
			}
			if c.form != nil {
				t.Fatal("form did not complete after the failure (must be non-blocking)")
			}
			if len(c.work.Models) != 1 || c.work.Models[0].HF != "Qwen/Qwen3-32B-GGUF" {
				t.Errorf("saved model = %+v, want bare org/repo (id still saved)", c.work.Models)
			}
		})
	}
}

// TestConfigHFCheckRunnerNil: without a runner the check is disabled
// (P3) — enter completes the form directly, no overlay, no flash.
func TestConfigHFCheckRunnerNil(t *testing.T) {
	c := typeBareRepo(t, hfFlowConfig(t, nil))
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter})
	if c.hfCheck != nil || c.hfQuant != nil {
		t.Fatal("overlays appeared without a runner")
	}
	// Behave exactly like today: no check flash, plain confirmation.
	if c.flash != "model added" {
		t.Errorf("flash = %q, want %q", c.flash, "model added")
	}
	if len(c.work.Models) != 1 || c.work.Models[0].HF != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("saved model = %+v, want bare org/repo", c.work.Models)
	}
}

// TestConfigHFCheckEditMode: editing an existing HF model with a bare
// id and saving unchanged skips the check (the id was never "typed" in
// this session); editing the id to a new bare repo runs it.
func TestConfigHFCheckEditMode(t *testing.T) {
	cfg := &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Models:      []config.Model{{Alias: "beta", HF: "Qwen/Qwen3-32B-GGUF"}},
		Preferences: &config.Preferences{ModelsDir: t.TempDir()},
	}
	stub := &stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}}}
	c := newModelFormConfig(t, cfg)
	c.SetHFCheckRunner(stub)
	c.modelIdx = 0

	// Open edit, drive to the HF field, save unchanged → no check.
	c = driveInit(t, c, c.openEditModelForm())
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // alias → source (unchanged)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // source → HF field (unchanged)
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // submit the untouched id
	if c.form != nil {
		t.Fatal("form did not complete on the unchanged edit")
	}
	if stub.repo != "" {
		t.Errorf("check ran for an unchanged id (repo %q)", stub.repo)
	}
	if c.work.Models[0].HF != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("HF = %q after unchanged edit", c.work.Models[0].HF)
	}

	// Re-open edit, change the id to a new bare repo, save → check runs.
	c = driveInit(t, c, c.openEditModelForm())
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // alias → source
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // source → HF field
	// Clear the pre-filled id, type a new repo.
	for i := 0; i < len("Qwen/Qwen3-32B-GGUF"); i++ {
		c = drive(t, c, tea.KeyMsg{Type: tea.KeyBackspace})
	}
	c = drive(t, c, keyRunes("meta-llama/Llama-3.1-8B-Instruct-GGUF"))
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // check → chooser
	if c.hfQuant == nil {
		t.Fatal("changed id did not open the quant chooser")
	}
	if stub.repo != "meta-llama/Llama-3.1-8B-Instruct-GGUF" {
		t.Errorf("runner saw repo %q", stub.repo)
	}
	c = drive(t, c, tea.KeyMsg{Type: tea.KeyEnter}) // pick Q4_K_M
	if len(c.work.Models) != 1 || c.work.Models[0].HF != "meta-llama/Llama-3.1-8B-Instruct-GGUF:Q4_K_M" {
		t.Errorf("saved model = %+v, want edited id with quant", c.work.Models)
	}
}

// TestConfigHFCheckCachedMarker: quants already on disk render with a
// (cached) marker in the chooser.
func TestConfigHFCheckCachedMarker(t *testing.T) {
	root := t.TempDir()
	mkHubRepo(t, root, "Qwen/Qwen3-32B-GGUF", "model-Q4_K_M.gguf")
	stub := &stubHFCheck{opts: []hf.QuantOption{{Tag: "Q4_K_M", Size: 100}, {Tag: "Q8_0", Size: 200}}}
	c := newModelFormConfig(t, &config.Config{
		Version:     1,
		Globals:     config.Globals{Bin: "/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		Preferences: &config.Preferences{ModelsDir: root},
	})
	c.SetHFCheckRunner(stub)
	c = typeBareRepo(t, c)
	c, checkCmd := startCheck(t, c)
	next, cmd := c.Update(safeCmd(checkCmd))
	c = next
	c = driveInit(t, c, cmd)
	view := c.View()
	if !strings.Contains(view, "Q4_K_M — "+hf.HumanSize(100)+" (cached)") {
		t.Errorf("cached marker missing:\n%s", view)
	}
	if strings.Contains(view, "Q8_0 — "+hf.HumanSize(200)+" (cached)") {
		t.Error("Q8_0 wrongly marked cached")
	}
}
