package modelsini

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, in string) *File {
	t.Helper()
	f, err := Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%q): %v", in, err)
	}
	return f
}

func TestParseBasicSectionsAndOrder(t *testing.T) {
	in := "[alpha]\nngl = 99\nctx-size = 8192\n\n[beta]\nmodel = ~/models/b.gguf\n"
	f := mustParse(t, in)
	if len(f.Sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(f.Sections))
	}
	a := f.Sections[0]
	if a.Name != "alpha" {
		t.Errorf("section 0 name = %q, want alpha", a.Name)
	}
	if len(a.Keys) != 2 {
		t.Fatalf("alpha has %d keys, want 2", len(a.Keys))
	}
	if a.Keys[0].Name != "ngl" || a.Keys[0].Value != "99" {
		t.Errorf("alpha.Keys[0] = %+v, want ngl=99", a.Keys[0])
	}
	if a.Keys[1].Name != "ctx-size" || a.Keys[1].Value != "8192" {
		t.Errorf("alpha.Keys[1] = %+v, want ctx-size=8192", a.Keys[1])
	}
	if b := f.Sections[1]; b.Name != "beta" {
		t.Errorf("section 1 name = %q, want beta", b.Name)
	}
}

func TestParseImplicitDefault(t *testing.T) {
	// Keys before any header belong to the implicit [default] section,
	// exactly like llama.cpp's COMMON_PRESET_DEFAULT_NAME.
	in := "ngl = 12\n[my-model]\nmodel = m.gguf\n"
	f := mustParse(t, in)
	if len(f.Sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(f.Sections))
	}
	if f.Sections[0].Name != DefaultName {
		t.Errorf("first section name = %q, want %q", f.Sections[0].Name, DefaultName)
	}
	if v, _ := f.Sections[0].Get("ngl"); v != "12" {
		t.Errorf("implicit default ngl = %q, want 12", v)
	}
}

func TestParseGlobalSection(t *testing.T) {
	in := "[*]\nctx-size = 4096\n\n[model-a]\nmodel = a.gguf\n"
	f := mustParse(t, in)
	g := f.Global()
	if g == nil {
		t.Fatal("no [*] section")
	}
	if v, _ := g.Get("ctx-size"); v != "4096" {
		t.Errorf("[*] ctx-size = %q, want 4096", v)
	}
	if len(f.Sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(f.Sections))
	}
}

func TestParseComments(t *testing.T) {
	in := strings.Join([]string{
		"; leading comment",
		"# another",
		"[a] ; trailing header comment",
		"model = a.gguf ; inline value comment",
		"ngl = 99#glued hash terminates too",
		"",
		"[b]",
		"  ; indented comment",
		"temp = 0.7",
	}, "\n") + "\n"
	f := mustParse(t, in)
	if len(f.Sections) != 2 {
		t.Fatalf("got %d sections, want 2", len(f.Sections))
	}
	a := f.Sections[0]
	if v, _ := a.Get("model"); v != "a.gguf" {
		t.Errorf("model = %q, want a.gguf (inline comment stripped)", v)
	}
	if v, _ := a.Get("ngl"); v != "99" {
		t.Errorf("ngl = %q, want 99 (# glued to value terminates it)", v)
	}
	if len(a.Comment) != 2 {
		t.Errorf("section a captured %d comments, want 2 (the two leading lines)", len(a.Comment))
	}
	if a.Comment[0] != "; leading comment" || a.Comment[1] != "# another" {
		t.Errorf("section a comments = %v", a.Comment)
	}
	if b := f.Sections[1]; len(b.Comment) != 0 {
		t.Errorf("section b comments = %v, want none (indented comment is inline, not above header)", b.Comment)
	}
}

func TestParseDescriptionCommentCapture(t *testing.T) {
	// Our own export convention: "; description: <text>" above a section.
	in := "; description: balanced preset\n[m]\nmodel = m.gguf\n"
	f := mustParse(t, in)
	s := f.Sections[0]
	if len(s.Comment) != 1 || !strings.HasPrefix(s.Comment[0], "; description:") {
		t.Fatalf("description comment not captured: %v", s.Comment)
	}
	if got := strings.TrimPrefix(s.Comment[0], "; description:"); strings.TrimSpace(got) != "balanced preset" {
		t.Errorf("description = %q, want balanced preset", got)
	}
}

func TestParseValueEdgeCases(t *testing.T) {
	cases := []struct{ line, want string }{
		{"key = a  b", "a  b"}, // internal whitespace preserved
		{"key = a b  ", "a b"}, // trailing whitespace trimmed
		{"key =", ""},          // empty value
		{"key = #nope", ""},    // value starts at a comment
		{"key =   x  ; c", "x"},
	}
	for _, c := range cases {
		f := mustParse(t, c.line+"\n")
		v, _ := f.Sections[0].Get("key")
		if v != c.want {
			t.Errorf("%q → value %q, want %q", c.line, v, c.want)
		}
	}
}

func TestParseDuplicateKeyLastWins(t *testing.T) {
	in := "[m]\nngl = 10\nngl = 99\n"
	f := mustParse(t, in)
	s := f.Sections[0]
	if len(s.Keys) != 1 {
		t.Fatalf("got %d keys, want 1 (duplicate collapsed)", len(s.Keys))
	}
	if v, _ := s.Get("ngl"); v != "99" {
		t.Errorf("ngl = %q, want 99 (last value wins)", v)
	}
}

func TestParseRepeatedHeaderResetsSection(t *testing.T) {
	// llama.cpp: parsed[current_section] = {} on every header.
	in := "[m]\na = 1\n[m]\nb = 2\n"
	f := mustParse(t, in)
	if len(f.Sections) != 1 {
		t.Fatalf("got %d sections, want 1", len(f.Sections))
	}
	s := f.Sections[0]
	if _, ok := s.Get("a"); ok {
		t.Error("key a survived a repeated header; llama.cpp resets the section")
	}
	if v, _ := s.Get("b"); v != "2" {
		t.Errorf("b = %q, want 2", v)
	}
}

func TestParseCRLF(t *testing.T) {
	in := "[m]\r\nmodel = m.gguf\r\n"
	f := mustParse(t, in)
	if v, _ := f.Sections[0].Get("model"); v != "m.gguf" {
		t.Errorf("model = %q with CRLF input", v)
	}
}

func TestParseSectionNameWhitespaceSignificant(t *testing.T) {
	// llama.cpp does not trim section names: "[ foo ]" has name " foo ".
	f := mustParse(t, "[ foo ]\nmodel = m.gguf\n")
	if got := f.Sections[0].Name; got != " foo " {
		t.Errorf("section name = %q, want %q (raw, untrimmed)", got, " foo ")
	}
}

func TestParseEmptySectionName(t *testing.T) {
	f := mustParse(t, "[]\nmodel = m.gguf\n")
	if got := f.Sections[0].Name; got != "" {
		t.Errorf("section name = %q, want empty", got)
	}
}

func TestParseErrors(t *testing.T) {
	bad := []string{
		"no equals sign\n",
		"= missing key\n",
		"[unterminated\n",
		"[ok] trailing garbage\n",
		"bad key! = x\n", // '!' not valid in ident
	}
	for _, in := range bad {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("Parse(%q) succeeded, want structural error", in)
		}
	}
}

func TestParseBoolValues(t *testing.T) {
	truthy := []string{"on", "enabled", "true", "1"}
	falsey := []string{"off", "disabled", "false", "0"}
	for _, v := range truthy {
		if got, ok := ParseBoolValue(v); !ok || !got {
			t.Errorf("ParseBoolValue(%q) = %v,%v want true,true", v, got, ok)
		}
	}
	for _, v := range falsey {
		if got, ok := ParseBoolValue(v); !ok || got {
			t.Errorf("ParseBoolValue(%q) = %v,%v want false,true", v, got, ok)
		}
	}
	for _, v := range []string{"yes", "no", "TRUE", "On", "2", "auto"} {
		if _, ok := ParseBoolValue(v); ok {
			t.Errorf("ParseBoolValue(%q) accepted, want not-a-bool (llama.cpp table is exact)", v)
		}
	}
}

func TestSerializeShape(t *testing.T) {
	f := &File{Sections: []Section{
		{Name: "m", Keys: []Key{{Name: "model", Value: "m.gguf"}, {Name: "ngl", Value: "99"}}},
		{Name: "m:fast", Keys: []Key{{Name: "hf", Value: "org/repo:Q4_K_M"}}},
	}}
	want := "[m]\nmodel = m.gguf\nngl = 99\n\n[m:fast]\nhf = org/repo:Q4_K_M\n\n"
	if got := f.String(); got != want {
		t.Errorf("String() =\n%q\nwant\n%q", got, want)
	}
}

func TestSerializeRoundTripStable(t *testing.T) {
	in := strings.Join([]string{
		"; description: balanced",
		"[my-model]",
		"model = ~/models/model.gguf",
		"ngl = 99",
		"ctx-size = 8192",
		"",
		"[*]",
		"temp = 0.7",
		"",
		"[ggml-org/gemma-3-27b-it-GGUF:Q6_K]",
		"hf = ggml-org/gemma-3-27b-it-GGUF:Q6_K",
		"alias = gemma,gemma-27b",
		"load-on-startup = gemma",
	}, "\n") + "\n"
	f1 := mustParse(t, in)
	out := f1.String()
	f2 := mustParse(t, out)
	if len(f1.Sections) != len(f2.Sections) {
		t.Fatalf("round-trip changed section count: %d → %d\n%s", len(f1.Sections), len(f2.Sections), out)
	}
	for i := range f1.Sections {
		a, b := &f1.Sections[i], &f2.Sections[i]
		if a.Name != b.Name {
			t.Errorf("section %d name %q → %q", i, a.Name, b.Name)
		}
		if len(a.Keys) != len(b.Keys) {
			t.Errorf("section %d keys %d → %d", i, len(a.Keys), len(b.Keys))
			continue
		}
		for j := range a.Keys {
			if a.Keys[j] != b.Keys[j] {
				t.Errorf("section %d key %d: %+v → %+v", i, j, a.Keys[j], b.Keys[j])
			}
		}
	}
}

func TestEscapeValue(t *testing.T) {
	if v, lossy := EscapeValue("plain"); v != "plain" || lossy {
		t.Errorf("EscapeValue(plain) = %q,%v want plain,false", v, lossy)
	}
	if _, lossy := EscapeValue(" leading"); !lossy {
		t.Error("leading space not flagged lossy")
	}
	if _, lossy := EscapeValue("a;b"); !lossy {
		t.Error("';' not flagged lossy")
	}
	if _, lossy := EscapeValue("a#b"); !lossy {
		t.Error("'#' not flagged lossy")
	}
	if v, lossy := EscapeValue("a\nb"); v != "a\\\nb" || !lossy {
		t.Errorf("EscapeValue(newline) = %q,%v want llama.cpp escape + lossy", v, lossy)
	}
}

// TestLlamaCppSampleDocumentedFile exercises a file shaped like the
// llama.cpp docs/preset.md examples: an HF model section with quant tag,
// a local model, the [*] global section, and a [default] fallback.
func TestLlamaCppSampleDocumentedFile(t *testing.T) {
	in := strings.Join([]string{
		"[*]",
		"ctx-size = 8192",
		"",
		"[ggml-org/gemma-3-27b-it-GGUF:Q6_K]",
		"hf = ggml-org/gemma-3-27b-it-GGUF:Q6_K",
		"alias = gemma",
		"ngl = 99",
		"",
		"[my-local-model]",
		"model = ./models/my-local-model.gguf",
		"no-mmap = true",
		"",
		"[default]",
		"temp = 0.7",
	}, "\n") + "\n"
	f := mustParse(t, in)
	if g := f.Global(); g == nil {
		t.Fatal("missing [*] section")
	}
	gemma := f.Section("ggml-org/gemma-3-27b-it-GGUF:Q6_K")
	if gemma == nil {
		t.Fatal("missing HF section")
	}
	if v, _ := gemma.Get("hf"); v != "ggml-org/gemma-3-27b-it-GGUF:Q6_K" {
		t.Errorf("hf = %q", v)
	}
	local := f.Section("my-local-model")
	if local == nil {
		t.Fatal("missing local section")
	}
	if v, _ := local.Get("no-mmap"); v != "true" {
		t.Errorf("no-mmap = %q, want true (raw; bool semantics applied by import)", v)
	}
	if d := f.Section(DefaultName); d == nil {
		t.Fatal("missing [default] section")
	}
}
