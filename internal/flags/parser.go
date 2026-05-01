package flags

import (
	"bufio"
	"regexp"
	"sort"
	"strings"
)

// ValueKind describes how the editor should prompt for a flag's value.
type ValueKind int

const (
	KindUnknown ValueKind = iota
	KindBool              // no placeholder → toggle yes/no
	KindNumeric           // placeholder is N, FLOAT, etc.
	KindEnum              // placeholder is a bracketed/braced enum, or hard-coded
	KindString            // generic text
)

// FlagInfo describes one parameter discovered from `llama-server --help`.
// One FlagInfo per alias: a flag with multiple aliases (e.g.
// `-fa, --flash-attn`) produces two entries that share metadata but
// differ in Form.
type FlagInfo struct {
	Name        string    // bare key, no leading dashes ("flash-attn")
	Form        string    // canonical CLI form with dashes ("--flash-attn")
	IsBool      bool      // true when the flag has no value placeholder
	Placeholder string    // raw placeholder text after the alias, if any
	Enum        []string  // parsed enum values, when Placeholder is bracketed/braced
	Kind        ValueKind // editor hint: bool / numeric / enum / string
}

// Registry maps the bare param key to its FlagInfo.
type Registry map[string]FlagInfo

// Lookup returns the registry entry for a key, or (FlagInfo{}, false).
func (r Registry) Lookup(name string) (FlagInfo, bool) {
	fi, ok := r[name]
	return fi, ok
}

// Names returns the registry keys sorted alphabetically. Used by the
// configuration mode's fuzzy picker.
func (r Registry) Names() []string {
	names := make([]string, 0, len(r))
	for k := range r {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// flagLineRE matches the start of a flag entry line.
var flagLineRE = regexp.MustCompile(`^-{1,2}[A-Za-z]`)

// numericPlaceholderRE matches placeholders that look numeric (N, INT,
// FLOAT, lo-hi, 1..100).
var numericPlaceholderRE = regexp.MustCompile(`^([NMK]|N>?[0-9]+|INT|FLOAT|NUM|FLOAT[A-Z_]*|N\.\.N|[0-9]+\.\.[0-9]+|[0-9]+\.\.\.[0-9]+|lo-hi)$`)

// hardcodedEnums supplements the parsed-enum table with known values from
// the help-text descriptions (DESIGN.md §7.5). Currently only ctk/ctv,
// which share their enum with --cache-type-k / --cache-type-v.
var hardcodedEnums = map[string][]string{
	"ctk":          {"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"},
	"ctv":          {"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"},
	"cache-type-k": {"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"},
	"cache-type-v": {"f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1"},
}

// ParseHelp consumes the output of `llama-server --help` and returns the
// per-alias registry. Lines that don't look like flag entries are ignored.
func ParseHelp(out string) Registry {
	reg := make(Registry)
	scanner := bufio.NewScanner(strings.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if !flagLineRE.MatchString(line) {
			continue
		}
		aliasPart, _ := splitAliasFromDesc(line)
		ingestEntry(reg, aliasPart)
	}
	// Apply hard-coded enums after parsing so they win over any
	// generic-string classification.
	for name, vals := range hardcodedEnums {
		if fi, ok := reg[name]; ok {
			fi.Enum = vals
			fi.Kind = KindEnum
			reg[name] = fi
		}
	}
	return reg
}

// ingestEntry parses one alias section like
// "-fa,   --flash-attn [on|off|auto]" and adds each alias to the registry.
func ingestEntry(reg Registry, aliasPart string) {
	tokens := splitTopLevelCommas(aliasPart)
	if len(tokens) == 0 {
		return
	}

	type pendingAlias struct{ name, form string }
	var pending []pendingAlias

	hasValue := false
	var placeholder string
	var enum []string

	for _, tok := range tokens {
		tok = strings.TrimSpace(tok)
		if tok == "" || tok[0] != '-' {
			continue
		}
		head, ph := splitFirstWhitespace(tok)
		if ph != "" {
			hasValue = true
			if placeholder == "" {
				placeholder = ph
			}
		}
		bare := strings.TrimLeft(head, "-")
		if bare == "" {
			continue
		}
		pending = append(pending, pendingAlias{name: bare, form: head})
	}

	if hasValue {
		enum = parseEnumPlaceholder(placeholder)
	}

	kind := KindBool
	if hasValue {
		switch {
		case len(enum) > 0:
			kind = KindEnum
		case isNumericPlaceholder(placeholder):
			kind = KindNumeric
		default:
			kind = KindString
		}
	}

	for _, a := range pending {
		// First-write wins so earlier sections (closer to the user-facing
		// surface) keep their canonical form.
		if _, exists := reg[a.name]; exists {
			continue
		}
		reg[a.name] = FlagInfo{
			Name:        a.name,
			Form:        a.form,
			IsBool:      !hasValue,
			Placeholder: placeholder,
			Enum:        enum,
			Kind:        kind,
		}
	}
}

// parseEnumPlaceholder extracts enum values from `[a|b|c]`, `{a,b,c}`,
// or `<a|b>` placeholders. Returns nil for any other shape.
func parseEnumPlaceholder(ph string) []string {
	ph = strings.TrimSpace(ph)
	if len(ph) < 3 {
		return nil
	}
	open, close := ph[0], ph[len(ph)-1]
	switch {
	case open == '[' && close == ']':
		return splitEnum(ph[1:len(ph)-1], "|")
	case open == '<' && close == '>':
		return splitEnum(ph[1:len(ph)-1], "|")
	case open == '{' && close == '}':
		return splitEnum(ph[1:len(ph)-1], ",")
	}
	return nil
}

func splitEnum(body, sep string) []string {
	parts := strings.Split(body, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		v := strings.TrimSpace(p)
		if v == "" {
			continue
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// isNumericPlaceholder is a lenient classifier: anything that's a single
// uppercase letter or matches the regex above is treated as numeric.
// Heuristic only — false negatives fall through to KindString, which
// still works in the editor (just no special UX).
func isNumericPlaceholder(ph string) bool {
	ph = strings.TrimSpace(ph)
	if ph == "" {
		return false
	}
	if numericPlaceholderRE.MatchString(ph) {
		return true
	}
	// Single ALL-CAPS placeholder consisting of letters only with the
	// 'N' prefix is also treated as numeric (covers N, NS, etc.).
	if len(ph) <= 4 && allUpperLetters(ph) && strings.HasPrefix(ph, "N") {
		return true
	}
	return false
}

func allUpperLetters(s string) bool {
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

// splitAliasFromDesc separates the alias section from the inline
// description. See ingestEntry for usage.
func splitAliasFromDesc(line string) (alias, desc string) {
	const minGap = 2
	runStart := -1
	runLen := 0
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			if runStart < 0 {
				runStart = i
			}
			runLen++
			continue
		}
		if runLen >= minGap && line[i] != '-' {
			return strings.TrimRight(line[:runStart], " "), strings.TrimSpace(line[i:])
		}
		runStart = -1
		runLen = 0
	}
	return strings.TrimRight(line, " "), ""
}

// splitTopLevelCommas splits on commas, ignoring those inside brackets
// or braces.
func splitTopLevelCommas(s string) []string {
	var out []string
	var cur strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '{', '[', '<', '(':
			depth++
		case '}', ']', '>', ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, cur.String())
				cur.Reset()
				continue
			}
		}
		cur.WriteRune(r)
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// splitFirstWhitespace returns the prefix before the first whitespace and
// everything after it (already trimmed).
func splitFirstWhitespace(s string) (string, string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			return s[:i], strings.TrimSpace(s[i:])
		}
	}
	return s, ""
}
