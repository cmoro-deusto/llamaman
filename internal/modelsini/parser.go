package modelsini

import (
	"fmt"
	"os"
	"strings"
)

// identStart and identRest follow the llama.cpp grammar:
// ident ::= [a-zA-Z_] [a-zA-Z0-9_.-]*
func isIdentStart(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
}

func isIdentRest(c byte) bool {
	return isIdentStart(c) || c >= '0' && c <= '9' || c == '.' || c == '-'
}

func isWS(c byte) bool { return c == ' ' || c == '\t' }

// Parse parses INI content according to the llama.cpp preset grammar.
func Parse(data []byte) (*File, error) {
	f := &File{}
	current := Section{Name: DefaultName} // implicit fallback section

	// pendingComments accumulate comment lines until the next header.
	var pendingComments []string

	lineNo := 0
	for len(data) > 0 {
		lineNo++
		var line string
		if i := strings.IndexByte(string(data), '\n'); i >= 0 {
			line = string(data[:i])
			data = data[i+1:]
		} else {
			line = string(data)
			data = nil
		}
		line = strings.TrimSuffix(line, "\r")

		rest := line
		for len(rest) > 0 && isWS(rest[0]) {
			rest = rest[1:]
		}
		if rest == "" {
			continue // blank line
		}

		switch {
		case rest[0] == ';' || rest[0] == '#':
			// comment-line ::= ws comment
			pendingComments = append(pendingComments, rest)
		case rest[0] == '[':
			// header-line ::= "[" ws <name> ws "]" eol
			close := strings.IndexByte(rest, ']')
			if close < 0 {
				return nil, fmt.Errorf("line %d: unterminated section header %q", lineNo, line)
			}
			name := rest[1:close]
			if !eolOK(rest[close+1:]) {
				return nil, fmt.Errorf("line %d: unexpected text after section header %q", lineNo, line)
			}
			flushSection(f, &current)
			current = Section{Name: name, Comment: pendingComments}
			pendingComments = nil
		default:
			// kv-line ::= ident ws "=" ws value eol
			name, value, ok := parseKV(rest)
			if !ok {
				return nil, fmt.Errorf("line %d: malformed line %q (expected `key = value`, section header, or comment)", lineNo, line)
			}
			current.Set(name, value)
		}
	}
	flushSection(f, &current)
	return f, nil
}

// ParseFile reads and parses an INI file from disk.
func ParseFile(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models file %s: %w", path, err)
	}
	f, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("parse models file %s: %w", path, err)
	}
	return f, nil
}

// flushSection appends current to f unless it is the empty implicit
// [default] section (nothing before the first header). If a section with
// the same name already exists it is replaced, matching llama.cpp's map
// semantics (a repeated header resets the section).
func flushSection(f *File, current *Section) {
	if current.Name == DefaultName && len(current.Keys) == 0 && len(current.Comment) == 0 {
		return
	}
	for i := range f.Sections {
		if f.Sections[i].Name == current.Name {
			f.Sections = append(f.Sections[:i], f.Sections[i+1:]...)
			break
		}
	}
	f.Sections = append(f.Sections, *current)
}

// eolOK reports whether the text after a header/value is a valid end of
// line: optional whitespace, then an optional comment.
func eolOK(after string) bool {
	rest := after
	for len(rest) > 0 && isWS(rest[0]) {
		rest = rest[1:]
	}
	if rest == "" {
		return true
	}
	return rest[0] == ';' || rest[0] == '#'
}

// parseKV splits `ident ws "=" ws value`. The value runs until the first
// ';' or '#' (llama.cpp's eol-start rule: an optional run of whitespace
// followed by [;#], newline, or EOF — so any ';'/'#' terminates it).
// Surrounding whitespace is trimmed, matching llama.cpp's intent.
func parseKV(rest string) (name, value string, ok bool) {
	i := 0
	if i >= len(rest) || !isIdentStart(rest[i]) {
		return "", "", false
	}
	for i < len(rest) && isIdentRest(rest[i]) {
		i++
	}
	name = rest[:i]
	for i < len(rest) && isWS(rest[i]) {
		i++
	}
	if i >= len(rest) || rest[i] != '=' {
		return "", "", false
	}
	i++ // consume '='
	for i < len(rest) && isWS(rest[i]) {
		i++
	}
	val := rest[i:]
	if j := strings.IndexAny(val, ";#"); j >= 0 {
		val = val[:j]
	}
	return name, strings.TrimRight(val, " \t"), true
}
