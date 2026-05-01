package flags

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseHelpFromFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "llama-server-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	reg := ParseHelp(string(data))

	cases := []struct {
		name      string
		wantForm  string
		wantBool  bool
		wantExist bool
	}{
		{"help", "--help", true, true},
		{"h", "-h", true, true},
		{"threads", "--threads", false, true},
		{"t", "-t", false, true},
		{"flash-attn", "--flash-attn", false, true},
		{"fa", "-fa", false, true},
		{"ctx-size", "--ctx-size", false, true},
		{"c", "-c", false, true},
		{"jinja", "--jinja", true, true}, // paired no-form, boolean
		{"no-jinja", "--no-jinja", true, true},
		{"mmap", "--mmap", true, true},
		{"no-mmap", "--no-mmap", true, true},
		{"cache-type-k", "--cache-type-k", false, true},
		{"ctk", "-ctk", false, true},
		{"rope-scaling", "--rope-scaling", false, true}, // {none,linear,yarn} — not split
		{"completion-bash", "--completion-bash", true, true},
		{"version", "--version", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fi, ok := reg.Lookup(tc.name)
			if ok != tc.wantExist {
				t.Fatalf("Lookup(%q) exist=%v, want %v", tc.name, ok, tc.wantExist)
			}
			if !tc.wantExist {
				return
			}
			if fi.Form != tc.wantForm {
				t.Errorf("Form = %q, want %q", fi.Form, tc.wantForm)
			}
			if fi.IsBool != tc.wantBool {
				t.Errorf("IsBool = %v, want %v", fi.IsBool, tc.wantBool)
			}
		})
	}
}

func TestSplitAliasFromDescNoDescription(t *testing.T) {
	// `-kvo, --kv-offload, -nkvo, --no-kv-offload` (description on next line).
	line := "-kvo,  --kv-offload, -nkvo, --no-kv-offload"
	alias, desc := splitAliasFromDesc(line)
	if alias != line {
		t.Errorf("alias = %q, want full line", alias)
	}
	if desc != "" {
		t.Errorf("desc = %q, want empty", desc)
	}
}

func TestSplitAliasFromDescWithDescription(t *testing.T) {
	line := "-h,    --help, --usage                  print usage and exit"
	alias, desc := splitAliasFromDesc(line)
	if alias != "-h,    --help, --usage" {
		t.Errorf("alias = %q", alias)
	}
	if desc != "print usage and exit" {
		t.Errorf("desc = %q", desc)
	}
}

func TestSplitTopLevelCommasRespectsBraces(t *testing.T) {
	tokens := splitTopLevelCommas("--rope-scaling {none,linear,yarn}")
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1: %v", len(tokens), tokens)
	}
	if tokens[0] != "--rope-scaling {none,linear,yarn}" {
		t.Errorf("token = %q", tokens[0])
	}
}

func TestParseHelpClassifiesValueKinds(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "llama-server-help.txt"))
	if err != nil {
		t.Fatal(err)
	}
	reg := ParseHelp(string(data))

	cases := []struct {
		name     string
		wantKind ValueKind
		wantEnum []string
	}{
		{"jinja", KindBool, nil},
		{"threads", KindNumeric, nil},
		{"ctx-size", KindNumeric, nil},
		{"flash-attn", KindEnum, []string{"on", "off", "auto"}},
		{"rope-scaling", KindEnum, []string{"none", "linear", "yarn"}},
		{"cpu-strict", KindEnum, []string{"0", "1"}},
		// Hard-coded enum override for ctk/ctv.
		{"ctk", KindEnum, []string{"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}},
		{"cache-type-k", KindEnum, []string{"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fi, ok := reg.Lookup(tc.name)
			if !ok {
				t.Fatalf("missing %q", tc.name)
			}
			if fi.Kind != tc.wantKind {
				t.Errorf("Kind = %d, want %d", fi.Kind, tc.wantKind)
			}
			if len(tc.wantEnum) > 0 {
				if len(fi.Enum) != len(tc.wantEnum) {
					t.Fatalf("Enum = %v, want %v", fi.Enum, tc.wantEnum)
				}
				for i, e := range tc.wantEnum {
					if fi.Enum[i] != e {
						t.Errorf("Enum[%d] = %q, want %q", i, fi.Enum[i], e)
					}
				}
			}
		})
	}
}

func TestRegistryNamesSorted(t *testing.T) {
	reg := Registry{
		"banana": {Name: "banana"},
		"apple":  {Name: "apple"},
		"cherry": {Name: "cherry"},
	}
	got := reg.Names()
	want := []string{"apple", "banana", "cherry"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}
