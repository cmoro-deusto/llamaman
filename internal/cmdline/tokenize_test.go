package cmdline

import (
	"reflect"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"empty", "", nil, false},
		{"whitespace only", "   \t\n  ", nil, false},
		{"simple", "llama-server -m /x.gguf --ctx-size 4096",
			[]string{"llama-server", "-m", "/x.gguf", "--ctx-size", "4096"}, false},
		{"single quotes literal", `-m '/my dir/model.gguf'`,
			[]string{"-m", "/my dir/model.gguf"}, false},
		{"double quotes literal", `--alias "my alias"`,
			[]string{"--alias", "my alias"}, false},
		{"quotes concatenate", `'ab'"cd"`,
			[]string{"abcd"}, false},
		{"escaped space", `-m /my\ dir/m.gguf`,
			[]string{"-m", "/my dir/m.gguf"}, false},
		{"escaped quote", `--alias \"x\"`,
			[]string{"--alias", `"x"`}, false},
		{"double quote escaped quote", `--alias "say \"hi\""`,
			[]string{"--alias", `say "hi"`}, false},
		{"equals form", "--ctx-size=4096 -m=/x.gguf",
			[]string{"--ctx-size=4096", "-m=/x.gguf"}, false},
		{"empty quotes dropped", `-m ""`,
			[]string{"-m"}, false},
		{"dollar and tilde literal", "-m $HOME/x.gguf ~/y.gguf",
			[]string{"-m", "$HOME/x.gguf", "~/y.gguf"}, false},
		{"line continuation dropped", "-hf org/repo \\\n    -ngl 99",
			[]string{"-hf", "org/repo", "-ngl", "99"}, false},
		{"crlf line continuation dropped", "-m /x \\\r\n    -ngl 1",
			[]string{"-m", "/x", "-ngl", "1"}, false},
		{"escaped newline in quotes kept", `--alias "a\
b"`,
			[]string{"--alias", "ab"}, false},
		{"unterminated single quote", "-m 'x", nil, true},
		{"unterminated double quote", `-m "x`, nil, true},
		{"dangling backslash", `-m x\`, nil, true},
		{"dangling backslash in double quotes", `-m "x\`, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Tokenize(tt.in)
			if tt.err {
				if err == nil {
					t.Fatalf("Tokenize(%q) = %v, want error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Tokenize(%q): %v", tt.in, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("Tokenize(%q) = %#v, want %#v", tt.in, got, tt.want)
			}
		})
	}
}
