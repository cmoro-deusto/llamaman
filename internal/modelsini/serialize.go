package modelsini

import "strings"

// String renders the file in llama.cpp's my-models.ini format: one
// `[section]` header, `key = value` lines in order, and a blank line after
// each section (mirroring common_preset::to_ini). Comment lines captured
// by the parser are re-emitted above their section header.
func (f *File) String() string {
	var b strings.Builder
	for i := range f.Sections {
		s := &f.Sections[i]
		for _, c := range s.Comment {
			b.WriteString(c)
			b.WriteByte('\n')
		}
		b.WriteByte('[')
		b.WriteString(s.Name)
		b.WriteString("]\n")
		for _, k := range s.Keys {
			b.WriteString(k.Name)
			b.WriteString(" = ")
			v, _ := EscapeValue(k.Value)
			b.WriteString(v)
			b.WriteByte('\n')
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// EscapeValue renders v for the INI format, escaping newlines the way
// llama.cpp does (backslash + newline). The bool result reports whether the
// value is lossy in this format: leading/trailing whitespace is trimmed by
// llama.cpp's parser, any ';' or '#' truncates the value, and newlines
// break the single-line value rule entirely.
func EscapeValue(v string) (escaped string, lossy bool) {
	if v == "" {
		return "", false
	}
	if strings.TrimSpace(v) != v {
		lossy = true
	}
	if strings.ContainsAny(v, ";#") {
		lossy = true
	}
	if strings.ContainsAny(v, "\n\r") {
		lossy = true
	}
	// Only newlines are escaped; llama.cpp performs no other escaping.
	return strings.ReplaceAll(v, "\n", "\\\n"), lossy
}
