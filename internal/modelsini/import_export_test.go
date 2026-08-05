package modelsini

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// testHelp is a synthetic llama-server --help slice covering the flag
// shapes the import typing logic cares about: bool, numeric (N/FLOAT),
// enum, and string flags.
const testHelp = `usage: llama-server [options]

options:
-h, --help              show this help message and exit
-m, --model FNAME       model path
-a, --alias STRING      model alias
--host STRING           listen host
--port N                listen port
--hf-repo STRING        hugging face repo
-ngl, --gpu-layers N    number of layers to offload
-c, --ctx-size N        context size
--temp FLOAT            temperature
--no-mmap               disable mmap
--flash-attn [on|off]   flash attention
--jinja                 enable jinja templates
-t, --threads N         number of threads
`

func testRegistry(t *testing.T) flags.Registry {
	t.Helper()
	reg := flags.ParseHelp(testHelp)
	if len(reg) == 0 {
		t.Fatal("test registry is empty")
	}
	return reg
}

func TestImportBasicMapping(t *testing.T) {
	in := "[my-model]\nmodel = /models/m.gguf\nngl = 99\nno-mmap = true\n"
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(models) != 1 {
		t.Fatalf("got %d models, want 1", len(models))
	}
	m := models[0]
	if m.Alias != "my-model" {
		t.Errorf("alias = %q, want my-model (section name)", m.Alias)
	}
	if m.Location != "/models/m.gguf" || m.HF != "" {
		t.Errorf("location/hf = %q/%q", m.Location, m.HF)
	}
	if len(m.Presets) != 1 {
		t.Fatalf("got %d presets, want 1", len(m.Presets))
	}
	p := m.Presets[0]
	if p.Name != "default" {
		t.Errorf("preset name = %q, want default", p.Name)
	}
	ngl, _ := p.Params.Get("ngl")
	if ngl != json.Number("99") {
		t.Errorf("ngl = %#v, want json.Number(99)", ngl)
	}
	mmap, _ := p.Params.Get("no-mmap")
	if mmap != true {
		t.Errorf("no-mmap = %#v, want true (bool from negated key)", mmap)
	}
}

func TestImportAliasKeyAndCommaList(t *testing.T) {
	in := "[anything]\nhf = org/repo:Q8_0\nalias = my-alias,second-alias\n"
	f := mustParse(t, in)
	models, _ := Import(f, nil, testRegistry(t))
	m := models[0]
	if m.Alias != "my-alias" {
		t.Errorf("alias = %q, want first comma part", m.Alias)
	}
	if !m.IsHF() || m.HF != "org/repo:Q8_0" {
		t.Errorf("hf = %q", m.HF)
	}
}

func TestImportGlobalMergeSectionWins(t *testing.T) {
	in := "[*]\nctx-size = 4096\ntemp = 0.5\n\n[m]\nmodel = m.gguf\nctx-size = 8192\n"
	f := mustParse(t, in)
	models, _ := Import(f, nil, testRegistry(t))
	p := models[0].Presets[0]
	if len(p.Params) != 2 {
		t.Fatalf("got %d params, want 2 ([*] merged, section override in place)", len(p.Params))
	}
	if p.Params[0].Key != "ctx-size" {
		t.Errorf("param order: first key = %q, want ctx-size (globals first)", p.Params[0].Key)
	}
	ctx, _ := p.Params.Get("ctx-size")
	if ctx != json.Number("8192") {
		t.Errorf("ctx-size = %#v, want 8192 (section wins over [*])", ctx)
	}
	temp, _ := p.Params.Get("temp")
	if temp != json.Number("0.5") {
		t.Errorf("temp = %#v, want 0.5 from [*]", temp)
	}
}

func TestImportSkipsNoSourceAndDefaultSections(t *testing.T) {
	in := "[*]\nctx-size = 1024\n\n[no-source]\nngl = 10\n\n[default]\ntemp = 0.3\n\n[m]\nmodel = m.gguf\n"
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	if len(models) != 1 || models[0].Alias != "m" {
		t.Fatalf("models = %+v, want only [m]", models)
	}
	var msgs []string
	for _, w := range warnings {
		msgs = append(msgs, w)
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "[no-source]") {
		t.Errorf("missing skip warning for [no-source]: %v", msgs)
	}
	if !strings.Contains(strings.Join(msgs, "\n"), "[default]") {
		t.Errorf("missing skip warning for [default]: %v", msgs)
	}
}

func TestImportCollisionRename(t *testing.T) {
	existing := []config.Model{{Alias: "m"}}
	in := "[m]\nmodel = m.gguf\n"
	f := mustParse(t, in)
	models, warnings := Import(f, existing, testRegistry(t))
	if len(models) != 1 || models[0].Alias != "m-ini" {
		t.Fatalf("alias = %+v, want m-ini", models)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "m-ini") {
		t.Errorf("warnings missing rename note: %v", warnings)
	}

	// Second collision on the same alias must go to m-ini-2.
	existing2 := append(existing, config.Model{Alias: "m-ini"})
	models2, _ := Import(f, existing2, testRegistry(t))
	if models2[0].Alias != "m-ini-2" {
		t.Errorf("alias = %q, want m-ini-2", models2[0].Alias)
	}
}

func TestImportMergesSameAliasPresets(t *testing.T) {
	// The exporter's multi-preset shape: [alias:preset] sections with an
	// explicit alias key must merge back into one model.
	in := "[m:balanced]\nmodel = m.gguf\nalias = m\nngl = 99\n\n[m:fast]\nmodel = m.gguf\nalias = m\nngl = 32\n"
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(models) != 1 || models[0].Alias != "m" {
		t.Fatalf("models = %+v, want one model 'm'", models)
	}
	if len(models[0].Presets) != 2 {
		t.Fatalf("presets = %+v, want 2 merged presets", models[0].Presets)
	}
	if models[0].Presets[0].Name != "balanced" || models[0].Presets[1].Name != "fast" {
		t.Errorf("preset names = %q, %q", models[0].Presets[0].Name, models[0].Presets[1].Name)
	}
}

func TestImportDescriptionFromComment(t *testing.T) {
	in := "; description: balanced preset\n[m]\nmodel = m.gguf\n"
	f := mustParse(t, in)
	models, _ := Import(f, nil, testRegistry(t))
	if got := models[0].Presets[0].Description; got != "balanced preset" {
		t.Errorf("description = %q, want balanced preset", got)
	}
}

func TestImportDropsReservedAndPresetOnlyKeys(t *testing.T) {
	in := "[m]\nmodel = m.gguf\nversion = 1\nload-on-startup = m\nstop-timeout = 5\nctx-size = 2048\n"
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	p := models[0].Presets[0]
	for _, key := range []string{"version", "load-on-startup", "stop-timeout"} {
		if _, ok := p.Params.Get(key); ok {
			t.Errorf("key %q was imported", key)
		}
	}
	if _, ok := p.Params.Get("ctx-size"); !ok {
		t.Error("ctx-size missing")
	}
	if len(warnings) != 2 {
		t.Errorf("got %d warnings, want 2 (both preset-only keys)", len(warnings))
	}
}

func TestImportValueTyping(t *testing.T) {
	in := "[m]\nmodel = m.gguf\n" +
		"no-mmap = true\n" + // bool via negated key
		"jinja = on\n" + // bool via plain flag + truthy value
		"flash-attn = on\n" + // enum key with truthy-looking value → string
		"ngl = 32\n" + // numeric
		"temp = 0.7\n" + // float numeric
		"prompt = hello world\n" + // unknown key → string
		"threads = 1e2\n" // numeric-looking → json.Number (raw preserved)
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	p := models[0].Presets[0]
	cases := map[string]any{
		"no-mmap":    true,
		"jinja":      true,
		"flash-attn": "on",
		"ngl":        json.Number("32"),
		"temp":       json.Number("0.7"),
		"prompt":     "hello world",
		"threads":    json.Number("1e2"),
	}
	for key, want := range cases {
		got, ok := p.Params.Get(key)
		if !ok {
			t.Errorf("param %q missing", key)
			continue
		}
		if got != want {
			t.Errorf("param %q = %#v (%T), want %#v", key, got, got, want)
		}
	}
	if _, ok := p.Params.Get("hf-repo"); ok {
		t.Error("hf-repo identity key leaked into params")
	}
}

func TestImportInvalidBoolDroppedWithWarning(t *testing.T) {
	in := "[m]\nmodel = m.gguf\nno-mmap = maybe\n"
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	if _, ok := models[0].Presets[0].Params.Get("no-mmap"); ok {
		t.Error("invalid bool value was stored")
	}
	if len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1", len(warnings))
	}
}

func TestImportModelAndHFConflict(t *testing.T) {
	in := "[m]\nmodel = a.gguf\nhf = org/repo:Q4_0\n"
	f := mustParse(t, in)
	models, warnings := Import(f, nil, testRegistry(t))
	m := models[0]
	if !m.IsHF() || m.HF != "org/repo:Q4_0" {
		t.Errorf("hf = %q, want org/repo:Q4_0 (hf wins)", m.HF)
	}
	if len(warnings) != 1 {
		t.Errorf("got %d warnings, want 1 (both keys set)", len(warnings))
	}
}

func TestExportSinglePresetSection(t *testing.T) {
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Models: []config.Model{{
			Alias: "gemma", Location: "/models/gemma.gguf",
			Presets: []config.Preset{{
				Name: "default", Description: "balanced",
				Params: config.Params{
					{Key: "ngl", Value: json.Number("99")},
					{Key: "no-mmap", Value: true},
					{Key: "temp", Value: false}, // explicit false must be emitted
				},
			}},
		}},
	}
	f, warnings := Export(cfg)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(f.Sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(f.Sections))
	}
	s := f.Sections[0]
	if s.Name != "gemma" {
		t.Errorf("section name = %q, want gemma (single preset)", s.Name)
	}
	if v, _ := s.Get("model"); v != "/models/gemma.gguf" {
		t.Errorf("model key = %q", v)
	}
	if v, _ := s.Get("alias"); v != "gemma" {
		t.Errorf("alias key = %q", v)
	}
	if v, _ := s.Get("no-mmap"); v != "true" {
		t.Errorf("no-mmap = %q", v)
	}
	if v, _ := s.Get("temp"); v != "false" {
		t.Errorf("temp = %q, want explicit false", v)
	}
	if len(s.Comment) != 1 || s.Comment[0] != "; description: balanced" {
		t.Errorf("comments = %v", s.Comment)
	}
}

func TestExportMultiPresetSections(t *testing.T) {
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Models: []config.Model{{
			Alias: "m", HF: "org/repo:Q8_0",
			Presets: []config.Preset{
				{Name: "balanced", Params: config.Params{{Key: "ngl", Value: json.Number("99")}}},
				{Name: "fast", Params: config.Params{{Key: "ngl", Value: json.Number("32")}}},
			},
		}},
	}
	f, _ := Export(cfg)
	if len(f.Sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(f.Sections))
	}
	if f.Sections[0].Name != "m:balanced" || f.Sections[1].Name != "m:fast" {
		t.Errorf("section names = %q, %q", f.Sections[0].Name, f.Sections[1].Name)
	}
	for _, s := range f.Sections {
		if v, _ := s.Get("hf"); v != "org/repo:Q8_0" {
			t.Errorf("section %q hf = %q", s.Name, v)
		}
	}
}

func TestExportLossyValueWarning(t *testing.T) {
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Models: []config.Model{{
			Alias: "m", Location: "m.gguf",
			Presets: []config.Preset{{
				Name: "default",
				Params: config.Params{
					{Key: "prompt", Value: "a b ; c"},
				},
			}},
		}},
	}
	f, warnings := Export(cfg)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1 (lossy value)", len(warnings))
	}
	s := f.Sections[0]
	if v, _ := s.Get("prompt"); v != "a b ; c" {
		t.Errorf("prompt = %q (still emitted, llama.cpp will truncate it)", v)
	}
}

// TestExportImportRoundTrip is the end-to-end stability guarantee: a
// config with multi-preset models and descriptions must survive
// export → import → export byte-identically.
func TestExportImportRoundTrip(t *testing.T) {
	cfg := &config.Config{
		Version: config.SchemaVersion,
		Models: []config.Model{
			{
				Alias: "m", Location: "/models/m.gguf",
				Presets: []config.Preset{
					{Name: "balanced", Description: "balanced preset", Params: config.Params{
						{Key: "ngl", Value: json.Number("99")},
						{Key: "temp", Value: json.Number("0.7")},
						{Key: "no-mmap", Value: true},
					}},
					{Name: "fast", Description: "fast preset", Params: config.Params{
						{Key: "ngl", Value: json.Number("32")},
						{Key: "ctx-size", Value: json.Number("4096")},
						{Key: "flash-attn", Value: false},
					}},
				},
			},
			{
				Alias: "gemma", HF: "org/repo:Q4_K_M",
				Presets: []config.Preset{{
					Name: "default", Description: "single preset",
					Params: config.Params{{Key: "temp", Value: json.Number("0.3")}},
				}},
			},
		},
	}

	f1, w1 := Export(cfg)
	if len(w1) != 0 {
		t.Fatalf("export warnings: %v", w1)
	}
	ini1 := f1.String()

	models, w2 := Import(f1, nil, testRegistry(t))
	if len(w2) != 0 {
		t.Fatalf("import warnings: %v", w2)
	}
	if len(models) != 2 {
		t.Fatalf("got %d models, want 2", len(models))
	}
	if len(models[0].Presets) != 2 {
		t.Fatalf("model m presets = %d, want 2 (merged)", len(models[0].Presets))
	}
	if models[0].Presets[0].Description != "balanced preset" {
		t.Errorf("description lost: %q", models[0].Presets[0].Description)
	}

	round := &config.Config{Version: config.SchemaVersion, Models: models}
	f2, w3 := Export(round)
	if len(w3) != 0 {
		t.Fatalf("re-export warnings: %v", w3)
	}
	if ini2 := f2.String(); ini2 != ini1 {
		t.Errorf("round-trip not stable:\n--- first export ---\n%s\n--- re-export ---\n%s", ini1, ini2)
	}
}
