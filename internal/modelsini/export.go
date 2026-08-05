package modelsini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

// Export converts a llamaman config into a my-models.ini File.
//
// Mapping rules (locked in design):
//
//   - One section per (model, preset): "[alias]" for single-preset
//     models, "[alias:preset]" when a model has several.
//   - Every section carries explicit model/hf and alias keys so import is
//     unambiguous regardless of the section name (which is decorative for
//     llamaman round-trips and a model id for llama.cpp's router).
//   - Preset descriptions become "; description: <text>" comments, the
//     only lossless channel llama.cpp's strict key table allows.
//   - Bools are emitted explicitly ("true"/"false"), numbers as their
//     literal, strings raw. Lossy string values (whitespace, ';'/'#',
//     newlines — all destructive under llama.cpp's parser) produce
//     warnings but are still emitted.
//
// The result is valid input for llama-server --models-preset as long as
// every parameter key is known to the running llama-server.
func Export(cfg *config.Config) (*File, []string) {
	f := &File{}
	var warnings []string

	for _, model := range cfg.Models {
		for _, preset := range model.Presets {
			s := Section{Name: sectionName(model, preset, &warnings)}
			if preset.Description != "" {
				// One comment line per physical line.
				for _, line := range strings.Split(preset.Description, "\n") {
					s.Comment = append(s.Comment, "; description: "+line)
				}
			}
			if model.IsHF() {
				s.Set("hf", model.HF)
			} else {
				s.Set("model", model.Location)
			}
			if len(model.Presets) > 1 {
				// Each preset is its own model section for llama.cpp's
				// router, which requires unique aliases across sections —
				// a repeated model alias aborts router startup. The alias
				// therefore carries the preset suffix (matching the
				// section name), and the original model alias is recorded
				// in a "; llamaman-model:" comment so import can still
				// merge the presets back onto one model. llama.cpp treats
				// the comment as noise, so the file stays router-valid.
				s.Comment = append(s.Comment, "; llamaman-model: "+model.Alias)
				s.Set("alias", model.Alias+":"+preset.Name)
			} else {
				s.Set("alias", model.Alias)
			}
			if strings.Contains(model.Alias, ",") {
				warnings = append(warnings, fmt.Sprintf("model %q: alias contains a comma — llama.cpp will read it as a list of routing aliases", model.Alias))
			}
			for _, p := range preset.Params {
				v, ok, warn := exportValue(p.Value)
				if warn != "" {
					warnings = append(warnings, fmt.Sprintf("model %q preset %q: param %q: %s", model.Alias, preset.Name, p.Key, warn))
				}
				if !ok {
					continue
				}
				s.Set(p.Key, v)
			}
			f.Sections = append(f.Sections, s)
		}
	}
	return f, warnings
}

// sectionName picks "[alias]" or "[alias:preset]". ']' in a name would
// break the INI grammar, so it is replaced (with a warning) — the
// explicit alias key still carries the true identity.
func sectionName(model config.Model, preset config.Preset, warnings *[]string) string {
	name := model.Alias
	if len(model.Presets) > 1 {
		name = model.Alias + ":" + preset.Name
	}
	if strings.ContainsAny(name, "]") {
		*warnings = append(*warnings, fmt.Sprintf("model %q: section name contains ']' — replaced for the INI header", model.Alias))
		name = strings.ReplaceAll(name, "]", "-")
	}
	return name
}

// exportValue renders one typed param value to its INI string form.
// ok=false means the value was dropped (unsupported type).
func exportValue(v any) (s string, ok bool, warning string) {
	switch t := v.(type) {
	case bool:
		if t {
			return "true", true, ""
		}
		return "false", true, ""
	case json.Number:
		return t.String(), true, ""
	case string:
		escaped, lossy := EscapeValue(t)
		if lossy {
			warning = "value is lossy in the INI format (whitespace/';'/'#'/newlines) — edit the .ini carefully or re-import"
		}
		return escaped, true, warning
	default:
		return "", false, fmt.Sprintf("unsupported value type %T", v)
	}
}
