// Package flags maps llama-server param names to their canonical CLI form.
// In Phase 3 only a hard-coded fallback table from DESIGN.md §6.2 is used;
// Phase 5 adds parsed `llama-server --help` output.
package flags

// fallbackShort lists the keys that take a single-dash form when the
// `llama-server --help` output cannot be parsed (DESIGN.md §6.2). The
// `hf*` family was added when model-level Hugging Face support landed
// — `hf` in particular is auto-emitted for HF-sourced models, so its
// canonical form has to be correct even when llama-server is missing.
var fallbackShort = map[string]struct{}{
	"m":   {},
	"n":   {},
	"c":   {},
	"t":   {},
	"s":   {},
	"b":   {},
	"h":   {},
	"p":   {},
	"ngl": {},
	"ctk": {},
	"ctv": {},
	"fa":  {},
	"np":  {},
	"cb":  {},
	"hf":  {},
	"hff": {},
	"hft": {},
	"hfr": {},
	"hfd": {},
	"hfv": {},
}

// Canonical returns the CLI form (with dashes) for a parameter key.
// Single-dash for the keys in the fallback short set, double-dash otherwise.
func Canonical(name string) string {
	if _, ok := fallbackShort[name]; ok {
		return "-" + name
	}
	return "--" + name
}

// IsFallbackShort reports whether a key is in the hard-coded short-form set.
// Phase 5 will replace this with a lookup against the parsed --help map.
func IsFallbackShort(name string) bool {
	_, ok := fallbackShort[name]
	return ok
}
