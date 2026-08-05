package modelsini

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// presetOnlyKeys are llama.cpp preset options that are not CLI arguments
// (see common_params_add_preset_options in common/arg.cpp). They make no
// sense as llamaman params, so import drops them with a warning.
var presetOnlyKeys = map[string]struct{}{
	"load-on-startup": {},
	"stop-timeout":    {},
}

// Import converts a parsed my-models.ini into llamaman models.
//
// Mapping rules (locked in design):
//
//   - Every section except "[*]" and "[default]" becomes one Model whose
//     alias is the first comma-part of its `alias` key, or the section
//     name when the key is absent.
//   - Sections without a `model`/`hf` (or `m`/`hf-repo`) key are skipped
//     with a warning — they cannot name a model.
//   - "[*]" params are merged into every imported preset, section params
//     winning on key collision (llama.cpp's cascade order).
//   - A section named "<alias>:<preset>" with an explicit alias key maps
//     to preset "<preset>"; otherwise the preset is named "default".
//   - Sections that collide with an *existing* model alias are renamed
//     with an "-ini" provenance suffix (foo → foo-ini → foo-ini-2).
//   - Sections within the same import that share an alias merge as
//     additional presets on one model — this is what makes export of
//     multi-preset models round-trip.
//
// Values are typed with the flag registry: bool flags take llama.cpp's
// truthiness table, numeric flags become json.Number, everything else is a
// string. Unknown keys produce warnings, not errors (llamaman is
// forward-compatible with newer llama-server flags).
func Import(f *File, existing []config.Model, reg flags.Registry) ([]config.Model, []string) {
	var (
		out      []config.Model
		warnings []string
	)
	global := f.Global()

	for i := range f.Sections {
		s := &f.Sections[i]
		switch s.Name {
		case GlobalName:
			continue
		case DefaultName:
			if len(s.Keys) > 0 {
				warnings = append(warnings, fmt.Sprintf("section [default] is the fallback preset, not a model — its params are not imported"))
			}
			continue
		}

		location, hf := sourceOf(s)
		switch {
		case location == "" && hf == "":
			warnings = append(warnings, fmt.Sprintf("section [%s] has no model or hf key — skipped", s.Name))
			continue
		case location != "" && hf != "":
			warnings = append(warnings, fmt.Sprintf("section [%s] sets both model and hf — using hf", s.Name))
		}

		alias, fromKey := aliasOf(s)
		presetName := "default"
		if fromKey {
			// "[alias:preset]" convention used by our exporter.
			if rest, ok := strings.CutPrefix(s.Name, alias+":"); ok && rest != "" {
				presetName = rest
			}
		}

		params, warns := importParams(global, s, reg)
		warnings = append(warnings, warns...)

		model := config.Model{
			Alias:    alias,
			Location: location,
			HF:       hf,
			Presets: []config.Preset{{
				Name:        presetName,
				Description: descriptionOf(s),
				Params:      params,
			}},
		}
		out = mergeOrAppend(out, model)
	}

	// Resolve collisions against the existing config (never mutate it).
	taken := make(map[string]bool, len(existing)+len(out))
	for _, m := range existing {
		taken[m.Alias] = true
	}
	for i := range out {
		if !taken[out[i].Alias] {
			taken[out[i].Alias] = true
			continue
		}
		old := out[i].Alias
		out[i].Alias = uniquify(old, taken)
		taken[out[i].Alias] = true
		warnings = append(warnings, fmt.Sprintf("alias %q already exists in config — imported as %q", old, out[i].Alias))
	}
	return out, warnings
}

// sourceOf resolves the model/hf keys of a section, accepting the
// aliases llama.cpp's key table maps to the same arguments: m/model and
// hf/hf-repo, with optional leading dashes.
func sourceOf(s *Section) (location, hf string) {
	for _, k := range s.Keys {
		switch k.Name {
		case "m", "model":
			if location == "" {
				location = k.Value
			}
		case "hf", "hf-repo":
			if hf == "" {
				hf = k.Value
			}
		}
	}
	return location, hf
}

// aliasOf returns the model alias: the first comma-part of the `alias`
// key (llama.cpp allows a comma-separated list for routing), or the
// section name. fromKey reports whether the alias key was present.
func aliasOf(s *Section) (alias string, fromKey bool) {
	if v, ok := s.Get("alias"); ok {
		if first, _, _ := strings.Cut(v, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first), true
		}
	}
	return s.Name, false
}

// descriptionOf extracts the preset description from the "; description:"
// comment convention used by the exporter.
func descriptionOf(s *Section) string {
	for i := len(s.Comment) - 1; i >= 0; i-- {
		if rest, ok := strings.CutPrefix(s.Comment[i], "; description:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// importParams builds the ordered param list for one section: "[*]"
// params first (in file order), then the section's own params, section
// values replacing globals on key collision. Keys that name the model
// source, the alias, or llama.cpp's reserved/preset-only options are
// excluded.
func importParams(global *Section, s *Section, reg flags.Registry) (config.Params, []string) {
	var (
		params   config.Params
		warnings []string
	)
	if global != nil {
		for _, k := range global.Keys {
			if excludedKey(k.Name) {
				continue
			}
			if v, warn := typedValue(k.Name, k.Value, reg); warn != "" {
				warnings = append(warnings, fmt.Sprintf("[*] %s", warn))
			} else {
				params = append(params, v)
			}
		}
	}
	for _, k := range s.Keys {
		switch k.Name {
		case "m", "model", "hf", "hf-repo", "alias":
			continue // model identity, not a launch param
		case "version":
			continue // reserved by llama.cpp for future use
		}
		if _, isPresetOnly := presetOnlyKeys[k.Name]; isPresetOnly {
			warnings = append(warnings, fmt.Sprintf("section [%s]: preset-only key %q dropped (not a llama-server CLI flag)", s.Name, k.Name))
			continue
		}
		if v, warn := typedValue(k.Name, k.Value, reg); warn != "" {
			warnings = append(warnings, fmt.Sprintf("section [%s]: %s", s.Name, warn))
		} else {
			params.Set(v.Key, v.Value) // section wins over [*]
		}
	}
	return params, warnings
}

// excludedKey reports whether a [*] key must be dropped: model identity,
// the alias, the reserved version key, and preset-only options.
func excludedKey(name string) bool {
	switch name {
	case "m", "model", "hf", "hf-repo", "alias", "version":
		return true
	}
	_, isPresetOnly := presetOnlyKeys[name]
	return isPresetOnly
}

// typedValue converts an INI value to a config.Param using the flag
// registry. A non-empty warning means the value was dropped (not stored).
func typedValue(key, value string, reg flags.Registry) (config.Param, string) {
	if fi, ok := reg.Lookup(key); ok && fi.IsBool {
		b, ok := ParseBoolValue(value)
		if !ok {
			return config.Param{}, fmt.Sprintf("key %q: %q is not a valid bool (true/false/on/off/1/0/enabled/disabled)", key, value)
		}
		return config.Param{Key: key, Value: b}, ""
	}
	// Negated flags (no-mmap, no-webui, ...) are bools even when the
	// registry lacks them — llama.cpp only accepts truthy/falsey values.
	if strings.HasPrefix(key, "no-") {
		if b, ok := ParseBoolValue(value); ok {
			return config.Param{Key: key, Value: b}, ""
		}
	}

	if fi, ok := reg.Lookup(key); ok && fi.Kind == flags.KindString {
		return config.Param{Key: key, Value: value}, ""
	}

	if num, ok := numericValue(value); ok {
		return config.Param{Key: key, Value: num}, ""
	}

	// A known numeric flag with a non-numeric value is a real error.
	// Enum and string flags fall through to the plain-string storage.
	if fi, ok := reg.Lookup(key); ok && fi.Kind == flags.KindNumeric {
		return config.Param{}, fmt.Sprintf("key %q: %q is not a valid value", key, value)
	}
	return config.Param{Key: key, Value: value}, ""
}

// jsonNumberRE is the JSON number grammar — stricter than Go's
// ParseFloat, which also accepts hex floats, "Inf", and "NaN".
var jsonNumberRE = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// numericValue reports whether v is a valid JSON number, returning it as
// a json.Number (which preserves the raw literal, so "007" stays "007").
func numericValue(v string) (json.Number, bool) {
	if !jsonNumberRE.MatchString(v) {
		return "", false
	}
	if _, err := strconv.ParseFloat(v, 64); err != nil {
		return "", false
	}
	return json.Number(v), true
}

// mergeOrAppend merges m into the last model with the same alias (adding
// its preset), or appends m as a new model. Merging is what lets our
// "[alias:preset]" export round-trip back into one model.
func mergeOrAppend(out []config.Model, m config.Model) []config.Model {
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Alias == m.Alias {
			out[i].Presets = append(out[i].Presets, m.Presets[0])
			return out
		}
	}
	return append(out, m)
}

// uniquify finds an unused alias: alias, alias-ini, alias-ini-2, ...
func uniquify(alias string, taken map[string]bool) string {
	candidate := alias + "-ini"
	for n := 2; taken[candidate]; n++ {
		candidate = fmt.Sprintf("%s-ini-%d", alias, n)
	}
	return candidate
}
