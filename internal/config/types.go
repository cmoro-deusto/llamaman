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
type Globals struct {
	Bin  string `json:"llama-server-bin"`
	Host string `json:"ip_address"`
	Port int    `json:"port"`
}

// Model is a single named model with its launch presets.
type Model struct {
	Alias    string   `json:"alias"`
	Location string   `json:"location"`
	Presets  []Preset `json:"presets"`
}

// Preset is a named bundle of llama-server flags. The JSON key for the name
// is `preset` (not `name`) to match the user's spec.
type Preset struct {
	Name        string `json:"preset"`
	Description string `json:"description"`
	Params      Params `json:"params"`
}
