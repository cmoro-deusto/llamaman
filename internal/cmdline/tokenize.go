// Package cmdline turns a pasted llama-server command line into config
// data (DESIGN §16.8, ROADMAP §3.9): Tokenize splits the text into argv
// tokens, Parse validates them against the flag registry and produces a
// model source plus preset params. Pure functions — no network, no
// filesystem, no execution (the pasted text is never spawned).
package cmdline

import (
	"fmt"
	"strings"
)

// Tokenize splits a command-line string into argv tokens using
// POSIX-ish rules: unquoted whitespace separates tokens; single quotes
// group literally; double quotes group (with \" and \\ escapes); a
// backslash escapes the next character outside quotes. No expansion —
// $VAR, ~, and globs pass through literally: the config loader expands
// the model Location later (internal/config/load.go) and every other
// value goes to llama-server as-is (exec argv semantics).
//
// The empty string yields no tokens; an unterminated quote or a
// dangling backslash is an error.
func Tokenize(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inSingle, inDouble := false, false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inSingle:
			if ch == '\'' {
				inSingle = false
			} else {
				cur.WriteByte(ch)
			}
		case inDouble:
			if ch == '"' {
				inDouble = false
			} else if ch == '\\' {
				if i+1 >= len(s) {
					return nil, fmt.Errorf("dangling backslash inside double quotes")
				}
				i++
				cur.WriteByte(s[i])
			} else {
				cur.WriteByte(ch)
			}
		case ch == '\\':
			if i+1 >= len(s) {
				return nil, fmt.Errorf("dangling backslash")
			}
			i++
			cur.WriteByte(s[i])
		case ch == '\'':
			inSingle = true
		case ch == '"':
			inDouble = true
		case ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r':
			flush()
		default:
			cur.WriteByte(ch)
		}
	}
	if inSingle {
		return nil, fmt.Errorf("unterminated single quote")
	}
	if inDouble {
		return nil, fmt.Errorf("unterminated double quote")
	}
	flush()
	return tokens, nil
}
