// Package config loads, validates, and writes llamaman config files.
package config

// SchemaVersion is the only `version` value llamaman v1 accepts.
// Unknown versions are a hard error (no migrations in v1, per DESIGN.md §3.2).
const SchemaVersion = 1

// Config mirrors the on-disk JSON schema documented in DESIGN.md §3.2.
type Config struct {
	Version int     `json:"version"`
	Globals Globals `json:"globals"`
	Models  []Model `json:"models"`
}

// Globals holds the binary path and the listen host/port. The JSON tag for
// the host is `ip_address` for compatibility with the user's draft schema,
// but the Go field is named Host because the value can be a hostname or an
// IPv6 literal too.
//
// ModelsFiles lists my-models.ini files (llama.cpp model-presets) that
// appear as router-mode run entries in the TUI. Each is a llama-server
// `--models-preset` source, one process hosting every model in the file.
type Globals struct {
	Bin         string   `json:"llama-server-bin"`
	Host        string   `json:"ip_address"`
	Port        int      `json:"port"`
	ModelsFiles []string `json:"models-files,omitempty"`
}

// Model is a single named model with its launch presets. Exactly one
// of Location or HF is set:
//
//   - Location is a path to a local .gguf file (subject to ~/$VAR
//     expansion at load time). Maps to `-m <path>` at launch.
//   - HF is a Hugging Face identifier in `org/repo[:quant]` form. Maps
//     to `-hf <id>` at launch — llama-server downloads on demand.
//
// Setting both is a validation error; setting neither is also an error.
type Model struct {
	Alias    string   `json:"alias"`
	Location string   `json:"location,omitempty"`
	HF       string   `json:"hf,omitempty"`
	Presets  []Preset `json:"presets"`
}

// IsHF reports whether the model is sourced from a Hugging Face
// repository rather than a local file.
func (m Model) IsHF() bool { return m.HF != "" }

// SourceLabel returns "local" or "hf" — used by --list, picker tags, and
// debug logs.
func (m Model) SourceLabel() string {
	if m.IsHF() {
		return "hf"
	}
	return "local"
}

// Preset is a named bundle of llama-server flags. The JSON key for the name
// is `preset` (not `name`) to match the user's spec.
type Preset struct {
	Name        string `json:"preset"`
	Description string `json:"description"`
	Params      Params `json:"params"`
}
