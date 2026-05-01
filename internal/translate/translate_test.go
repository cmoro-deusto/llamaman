package translate

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

func num(s string) json.Number { return json.Number(s) }

// build is a shorthand that uses the fallback registry.
func build(t *testing.T, g config.Globals, m config.Model, p config.Preset) Result {
	t.Helper()
	r, err := Build(g, m, p, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return r
}

func TestBuildSpecExample(t *testing.T) {
	// Mirrors the larger example in llamaman_specs.md (lines ~178/188) but
	// applies DESIGN.md §6.2 fallback canonicalization (so ctk/ctv/fa are
	// short-form, not long).
	globals := config.Globals{
		Bin:  "/opt/llama.cpp/bin/llama-server",
		Host: "127.0.0.1",
		Port: 9080,
	}
	model := config.Model{
		Alias:    "qwen3.6-27B",
		Location: "/h/alice/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf",
	}
	preset := config.Preset{
		Name: "default",
		Params: config.Params{
			{Key: "ngl", Value: num("99")},
			{Key: "ctx-size", Value: num("262144")},
			{Key: "parallel", Value: num("1")},
			{Key: "batch-size", Value: num("2048")},
			{Key: "ubatch-size", Value: num("256")},
			{Key: "ctk", Value: "q4_0"},
			{Key: "ctv", Value: "q4_0"},
			{Key: "fa", Value: "on"},
			{Key: "presence-penalty", Value: num("0.0")},
			{Key: "temp", Value: num("0.6")},
			{Key: "top-p", Value: num("0.95")},
			{Key: "top-k", Value: num("20")},
			{Key: "min-p", Value: num("0.00")},
			{Key: "chat-template-kwargs", Value: `{"preserve_thinking": true}`},
			{Key: "jinja", Value: true},
			{Key: "no-mmproj", Value: true},
			{Key: "metrics", Value: true},
		},
	}

	want := []string{
		"/opt/llama.cpp/bin/llama-server",
		"-m", "/h/alice/Code/ai/models/Qwen3.6-27B-Q4_K_XL.gguf",
		"--alias", "qwen3.6-27B",
		"--host", "127.0.0.1",
		"-ngl", "99",
		"--ctx-size", "262144",
		"--parallel", "1",
		"--batch-size", "2048",
		"--ubatch-size", "256",
		"-ctk", "q4_0",
		"-ctv", "q4_0",
		"-fa", "on",
		"--presence-penalty", "0.0",
		"--temp", "0.6",
		"--top-p", "0.95",
		"--top-k", "20",
		"--min-p", "0.00",
		"--chat-template-kwargs", `{"preserve_thinking": true}`,
		"--jinja",
		"--no-mmproj",
		"--metrics",
		"--port", "9080",
	}

	got := build(t, globals, model, preset)
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("argv mismatch:\n got: %v\nwant: %v", got.Argv, want)
	}
}

func TestBuildBooleanFalseOmitted(t *testing.T) {
	preset := config.Preset{
		Params: config.Params{
			{Key: "jinja", Value: true},
			{Key: "metrics", Value: false}, // omitted
		},
	}
	got := build(t, config.Globals{Bin: "x", Host: "h", Port: 1}, config.Model{Alias: "a", Location: "l"}, preset)
	for _, a := range got.Argv {
		if a == "--metrics" {
			t.Fatalf("--metrics should be omitted when value=false; argv=%v", got.Argv)
		}
	}
	if !contains(got.Argv, "--jinja") {
		t.Fatalf("--jinja should be present; argv=%v", got.Argv)
	}
}

func TestBuildPresetOverridesAutoAdded(t *testing.T) {
	preset := config.Preset{
		Params: config.Params{
			{Key: "host", Value: "0.0.0.0"},
			{Key: "port", Value: num("9999")},
			{Key: "temp", Value: num("0.6")},
		},
	}
	got := build(t,
		config.Globals{Bin: "/x", Host: "127.0.0.1", Port: 9080},
		config.Model{Alias: "qwen", Location: "/m.gguf"},
		preset,
	)
	want := []string{
		"/x",
		"-m", "/m.gguf",
		"--alias", "qwen",
		"--host", "0.0.0.0",
		"--port", "9999",
		"--temp", "0.6",
	}
	if !reflect.DeepEqual(got.Argv, want) {
		t.Fatalf("argv mismatch:\n got: %v\nwant: %v", got.Argv, want)
	}
}

func TestBuildHostAlwaysExplicit(t *testing.T) {
	got := build(t,
		config.Globals{Bin: "/x", Host: "127.0.0.1", Port: 8080},
		config.Model{Alias: "a", Location: "/l"},
		config.Preset{},
	)
	if !contains(got.Argv, "--host") {
		t.Fatalf("--host should always be explicit; got %v", got.Argv)
	}
}

func TestBuildOrderMatchesParamsSlice(t *testing.T) {
	preset := config.Preset{
		Params: config.Params{
			{Key: "z-last", Value: num("1")},
			{Key: "a-first", Value: num("2")},
		},
	}
	got := build(t, config.Globals{Bin: "x", Host: "h", Port: 1}, config.Model{Alias: "a", Location: "l"}, preset)
	zIdx, aIdx := indexOf(got.Argv, "--z-last"), indexOf(got.Argv, "--a-first")
	if zIdx < 0 || aIdx < 0 || zIdx >= aIdx {
		t.Fatalf("expected z-last before a-first; got %v", got.Argv)
	}
}

func TestBuildRejectsUnsupportedValue(t *testing.T) {
	preset := config.Preset{
		Params: config.Params{
			{Key: "broken", Value: 42},
		},
	}
	_, err := Build(config.Globals{Bin: "x", Host: "h", Port: 1}, config.Model{Alias: "a", Location: "l"}, preset, nil)
	if err == nil {
		t.Fatal("expected error for unsupported value type")
	}
}

// With a real registry, ctk/ctv resolve to short form (matches fallback in
// this case) and unknown keys produce a warning instead of a hard error.
func TestBuildWarnsOnUnknownKey(t *testing.T) {
	reg := flags.Registry{
		"temp": {Name: "temp", Form: "--temp", IsBool: false},
	}
	preset := config.Preset{
		Params: config.Params{
			{Key: "temp", Value: num("0.6")},
			{Key: "made-up-flag", Value: num("1")},
		},
	}
	got, err := Build(
		config.Globals{Bin: "x", Host: "h", Port: 1},
		config.Model{Alias: "a", Location: "l"},
		preset,
		reg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 {
		t.Fatalf("expected 1 warning, got %d: %v", len(got.Warnings), got.Warnings)
	}
	if !contains(got.Argv, "--made-up-flag") {
		t.Errorf("unknown flag should pass through; argv=%v", got.Argv)
	}
}

// When a registry is provided and the auto-added flags resolve through it,
// the canonical form should win (parsed --help wins over fallback). This
// guards against the case where llama-server adds new short forms.
func TestBuildUsesRegistryFormForAutoAddedFlags(t *testing.T) {
	reg := flags.Registry{
		"m":     {Name: "m", Form: "-m", IsBool: false},
		"alias": {Name: "alias", Form: "--alias", IsBool: false},
		"host":  {Name: "host", Form: "--host", IsBool: false},
		"port":  {Name: "port", Form: "--port", IsBool: false},
	}
	got, err := Build(
		config.Globals{Bin: "x", Host: "h", Port: 1},
		config.Model{Alias: "a", Location: "/l"},
		config.Preset{},
		reg,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Argv, []string{"x", "-m", "/l", "--alias", "a", "--host", "h", "--port", "1"}) {
		t.Errorf("argv=%v", got.Argv)
	}
}

func contains(slice []string, s string) bool {
	for _, x := range slice {
		if x == s {
			return true
		}
	}
	return false
}

func indexOf(slice []string, s string) int {
	for i, x := range slice {
		if x == s {
			return i
		}
	}
	return -1
}
