// Package config loads, validates, and writes llamaman config files.
package config

// SchemaVersion is the only `version` value llamaman v1 accepts.
// Unknown versions are a hard error (no migrations in v1, per DESIGN.md §3.2).
const SchemaVersion = 1

// Config mirrors the on-disk JSON schema documented in DESIGN.md §3.2.
type Config struct {
	Version     int          `json:"version"`
	Globals     Globals      `json:"globals"`
	Preferences *Preferences `json:"preferences,omitempty"`
	Models      []Model      `json:"models"`
}

// Preferences holds user preferences, separate from Globals per DESIGN
// §15.1: globals are launch-time parameters every preset needs (host,
// port, binary, models-files); preferences are user preferences of a
// different nature (theme, animations). Additive v1 — older binaries
// reject the object with `json: unknown field "preferences"` (accepted
// P2 contract).
//
// The zero value equals the defaults (theme "auto", animations true),
// so Config.Preferences is a pointer: the object stays absent from the
// file until the user actually changes a preference, and untouched
// configs remain byte-identical on save.
type Preferences struct {
	// Theme is a palette ID from the TUI palette table ("auto", the
	// default, resolves to the llamaman palette). Unknown values are a
	// Warning resolved to "auto" by the TUI resolver — config.Validate
	// only checks shape (non-empty string), keeping the palette-name
	// list single-sourced in internal/tui (DESIGN §15.1).
	Theme string `json:"theme,omitempty"`
	// Animations defaults to true. A pointer so an explicit `false` is
	// distinct from absent and survives a save round-trip (a plain bool
	// with omitempty would silently drop an explicit false).
	Animations *bool `json:"animations,omitempty"`
}

// Prefs returns the effective preferences, or the zero value (==
// defaults) when the object is absent. Callers must use this instead of
// dereferencing Preferences directly.
func (c *Config) Prefs() Preferences {
	if c.Preferences == nil {
		return Preferences{}
	}
	return *c.Preferences
}

// AnimationsEnabled reports the effective animations setting: absent
// (nil) means the default, true.
func (p Preferences) AnimationsEnabled() bool {
	return p.Animations == nil || *p.Animations
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
