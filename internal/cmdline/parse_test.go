package cmdline

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// testRegistry is a synthetic flag registry covering the shapes the
// parser must handle: model sources, alias, numeric, enum, string, and
// bool flags — plus per-alias keys (-m / --model).
func testRegistry() flags.Registry {
	return flags.Registry{
		"m":        {Name: "m", Form: "-m", Kind: flags.KindString, Placeholder: "FILE"},
		"model":    {Name: "model", Form: "--model", Kind: flags.KindString, Placeholder: "FILE"},
		"hf":       {Name: "hf", Form: "-hf", Kind: flags.KindString, Placeholder: "REPO"},
		"hf-repo":  {Name: "hf-repo", Form: "--hf-repo", Kind: flags.KindString, Placeholder: "REPO"},
		"alias":    {Name: "alias", Form: "--alias", Kind: flags.KindString, Placeholder: "ALIAS"},
		"ctx-size": {Name: "ctx-size", Form: "--ctx-size", Kind: flags.KindNumeric, Placeholder: "N"},
		"ngl":      {Name: "ngl", Form: "-ngl", Kind: flags.KindNumeric, Placeholder: "N"},
		"fa":       {Name: "fa", Form: "-fa", Kind: flags.KindEnum, Placeholder: "[on|off|auto]", Enum: []string{"on", "off", "auto"}},
		"temp":     {Name: "temp", Form: "--temp", Kind: flags.KindString, Placeholder: "FLOAT"},
		"host":     {Name: "host", Form: "--host", Kind: flags.KindString, Placeholder: "HOST"},
		"port":     {Name: "port", Form: "--port", Kind: flags.KindNumeric, Placeholder: "PORT"},
		"lora":     {Name: "lora", Form: "--lora", Kind: flags.KindString, Placeholder: "LORA"},
		"metrics":  {Name: "metrics", Form: "--metrics", IsBool: true},
		"no-mmap":  {Name: "no-mmap", Form: "--no-mmap", IsBool: true},
	}
}

func param(key string, value any) config.Param { return config.Param{Key: key, Value: value} }

func TestParseHappyPaths(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want Result
	}{
		{
			name: "binary name dropped",
			argv: []string{"llama-server", "-m", "/x.gguf"},
			want: Result{Source: Source{Location: "/x.gguf"}},
		},
		{
			name: "binary with path dropped",
			argv: []string{"/usr/bin/llama-server", "--ctx-size", "4096", "-ngl", "99"},
			want: Result{Params: config.Params{
				param("ctx-size", json.Number("4096")),
				param("ngl", json.Number("99")),
			}},
		},
		{
			name: "bare flags only",
			argv: []string{"-m", "/x.gguf", "--ctx-size", "4096"},
			want: Result{
				Source: Source{Location: "/x.gguf"},
				Params: config.Params{param("ctx-size", json.Number("4096"))},
			},
		},
		{
			name: "hf with quant",
			argv: []string{"-hf", "org/repo:Q4_K_M"},
			want: Result{Source: Source{HF: "org/repo:Q4_K_M"}},
		},
		{
			name: "hf bare",
			argv: []string{"--hf-repo", "org/repo"},
			want: Result{Source: Source{HF: "org/repo"}},
		},
		{
			name: "equals forms",
			argv: []string{"-m=/x.gguf", "--ctx-size=4096"},
			want: Result{
				Source: Source{Location: "/x.gguf"},
				Params: config.Params{param("ctx-size", json.Number("4096"))},
			},
		},
		{
			name: "alias extracted and kept in params",
			argv: []string{"--alias", "my-model", "-m", "/x.gguf"},
			want: Result{
				Source: Source{Location: "/x.gguf", Alias: "my-model"},
				Params: config.Params{param("alias", "my-model")},
			},
		},
		{
			name: "alias comma list first part",
			argv: []string{"--alias", "a,b", "-m", "/x.gguf"},
			want: Result{
				Source: Source{Location: "/x.gguf", Alias: "a"},
				Params: config.Params{param("alias", "a,b")},
			},
		},
		{
			name: "bool flag",
			argv: []string{"--metrics", "--no-mmap"},
			want: Result{Params: config.Params{
				param("metrics", true),
				param("no-mmap", true),
			}},
		},
		{
			name: "enum and string values",
			argv: []string{"-fa", "on", "--temp", "0.5"},
			want: Result{Params: config.Params{
				param("fa", "on"),
				param("temp", "0.5"),
			}},
		},
		{
			name: "host and port kept in preset",
			argv: []string{"--host", "0.0.0.0", "--port", "9080", "-m", "/x.gguf"},
			want: Result{
				Source: Source{Location: "/x.gguf"},
				Params: config.Params{
					param("host", "0.0.0.0"),
					param("port", json.Number("9080")),
				},
			},
		},
		{
			name: "unknown flag with value",
			argv: []string{"--future-flag", "xyz"},
			want: Result{
				Params:   config.Params{param("future-flag", "xyz")},
				Warnings: []string{"unknown flag --future-flag (imported best-effort)"},
			},
		},
		{
			name: "unknown bare flag is bool",
			argv: []string{"--future-bool"},
			want: Result{
				Params:   config.Params{param("future-bool", true)},
				Warnings: []string{"unknown flag --future-bool (imported best-effort)"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.argv, testRegistry())
			if err != nil {
				t.Fatalf("Parse(%v): %v", tt.argv, err)
			}
			if !reflect.DeepEqual(got.Source, tt.want.Source) {
				t.Errorf("Source = %+v, want %+v", got.Source, tt.want.Source)
			}
			if !reflect.DeepEqual(got.Params, tt.want.Params) {
				t.Errorf("Params = %v, want %v", got.Params, tt.want.Params)
			}
			if !reflect.DeepEqual(got.Warnings, tt.want.Warnings) {
				t.Errorf("Warnings = %v, want %v", got.Warnings, tt.want.Warnings)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want string // substring of the joined error
	}{
		{"missing value at end", []string{"-m"}, "missing value"},
		{"missing value before flag", []string{"-m", "-fa"}, "missing value"},
		{"empty equals value", []string{"--ctx-size="}, "empty value"},
		{"empty short equals value", []string{"-m="}, "empty value"},
		{"non-numeric for numeric", []string{"-ngl", "abc"}, "not a valid number"},
		{"non-numeric equals", []string{"--ctx-size=abc"}, "not a valid number"},
		{"both m and hf", []string{"-m", "/x.gguf", "-hf", "org/repo"}, "both -m and -hf"},
		{"repeated m", []string{"-m", "/a.gguf", "--model", "/b.gguf"}, "model source repeated"},
		{"repeated hf", []string{"-hf", "a/r", "--hf-repo", "b/r"}, "model source repeated"},
		{"invalid hf id", []string{"-hf", "foo"}, "invalid HF identifier"},
		{"missing alias value", []string{"--alias"}, "missing value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.argv, testRegistry())
			if err == nil {
				t.Fatalf("Parse(%v): want error containing %q, got nil", tt.argv, tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse(%v) error = %q, want substring %q", tt.argv, err, tt.want)
			}
		})
	}
}

func TestParseWarnings(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want Result
	}{
		{
			name: "repeated flag last wins",
			argv: []string{"-ngl", "10", "-ngl", "20"},
			want: Result{
				Params:   config.Params{param("ngl", json.Number("20"))},
				Warnings: []string{"-ngl repeated — last value wins"},
			},
		},
		{
			name: "repeated bool",
			argv: []string{"--metrics", "--metrics"},
			want: Result{
				Params:   config.Params{param("metrics", true)},
				Warnings: []string{"--metrics repeated — last value wins"},
			},
		},
		{
			name: "bool flag value ignored",
			argv: []string{"--metrics=1"},
			want: Result{
				Params:   config.Params{param("metrics", true)},
				Warnings: []string{`--metrics=1 is boolean — value "1" ignored`},
			},
		},
		{
			name: "repeated alias first kept",
			argv: []string{"--alias", "a", "--alias", "b", "-m", "/x.gguf"},
			want: Result{
				Source: Source{Location: "/x.gguf", Alias: "a"},
				Params: config.Params{param("alias", "a")},
				Warnings: []string{
					`alias repeated — first value kept ("a")`,
				},
			},
		},
		{
			name: "positional argument ignored",
			argv: []string{"-m", "/x.gguf", "stray"},
			want: Result{
				Source:   Source{Location: "/x.gguf"},
				Warnings: []string{`unexpected argument "stray" ignored`},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse(tt.argv, testRegistry())
			if err != nil {
				t.Fatalf("Parse(%v): %v", tt.argv, err)
			}
			if !reflect.DeepEqual(got.Params, tt.want.Params) {
				t.Errorf("Params = %v, want %v", got.Params, tt.want.Params)
			}
			if !reflect.DeepEqual(got.Warnings, tt.want.Warnings) {
				t.Errorf("Warnings = %v, want %v", got.Warnings, tt.want.Warnings)
			}
		})
	}
}

func TestParseNilRegistry(t *testing.T) {
	// No registry: model-source keys still resolve; every other flag is
	// unknown (best-effort strings/bools) — never a crash.
	got, err := Parse([]string{"-m", "/x.gguf", "--whatever", "v"}, nil)
	if err != nil {
		t.Fatalf("Parse with nil registry: %v", err)
	}
	if got.Source.Location != "/x.gguf" {
		t.Errorf("Location = %q", got.Source.Location)
	}
	if _, ok := got.Params.Get("whatever"); !ok {
		t.Error("unknown flag not imported best-effort")
	}
}
