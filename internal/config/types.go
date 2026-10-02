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
	// LogColors defaults to true: render-time line-kind coloring of the
	// run-mode log (§15.3). Same pointer semantics as Animations.
	LogColors *bool `json:"log-colors,omitempty"`
	// LogoEffect is the wordmark highlight sweep mode on the main screen
	// (§15.5a): "once" (the default) sweeps each time the main screen
	// opens, "loop" keeps sweeping while it stays open. The Animations
	// toggle is the master switch — off disables the sweep entirely, so
	// this only picks between one-shot and looping. Empty string means
	// the default, "once" (omitempty keeps untouched configs minimal).
	LogoEffect string `json:"logo-effect,omitempty"`
	// ModelsDir, when set, is the single llama.cpp HF cache root
	// (DESIGN §16.1): it wins over every environment variable
	// (LLAMA_CACHE, HF_HOME, …). Empty (the default) means follow
	// llama.cpp's chain. A plain string, not a pointer: the default is
	// the empty string, and there is no meaningful explicit-empty
	// distinct from absent (unlike Animations). Expanded ~/$VAR at load
	// time (§3.3).
	ModelsDir string `json:"models-dir,omitempty"`
	// DownloadConnections is the parallel-connection count for model
	// downloads in the Storage manager (DESIGN §16.4). 0 (the default,
	// kept absent via omitempty) means the downloader's default;
	// out-of-range values warn (P3) and are clamped at use time
	// (hf.SetConnections owns the default and the cap, mirroring the
	// theme pattern of resolving semantics outside config).
	DownloadConnections int `json:"download-connections,omitempty"`
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

// LogColorsEnabled reports the effective log-coloring setting: absent
// (nil) means the default, true.
func (p Preferences) LogColorsEnabled() bool {
	return p.LogColors == nil || *p.LogColors
}

// Logo-effect modes for the main-screen wordmark highlight sweep
// (§15.5a). Empty LogoEffect means LogoEffectOnce (the default).
const (
	LogoEffectOnce = "once"
	LogoEffectLoop = "loop"
)

// LogoEffectMode reports the effective wordmark-highlight mode: absent
// means "once".
func (p Preferences) LogoEffectMode() string {
	if p.LogoEffect == "" {
		return LogoEffectOnce
	}
	return p.LogoEffect
}

// Binary is one named llama-server build (DESIGN §3.2a): mainline plus
// any forks/branches. Name is the user-facing identifier referenced by
// Preset.Binary and Globals.DefaultBinary; Path is expanded ~/$VAR at
// load time.
type Binary struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Note string `json:"note,omitempty"`
}

// DefaultBinaryName is the name assigned by load-time migration when a
// legacy config has only globals.llama-server-bin (DESIGN §3.2a).
const DefaultBinaryName = "default"

// Globals holds the listen host/port, the named llama-server binaries,
// and the my-models.ini router sources. The JSON tag for the host is
// `ip_address` for compatibility with the user's draft schema, but the
// Go field is named Host because the value can be a hostname or an IPv6
// literal too.
//
// ModelsFiles lists my-models.ini files (llama.cpp model-presets) that
// appear as router-mode run entries in the TUI. Each is a llama-server
// `--models-preset` source, one process hosting every model in the file.
type Globals struct {
	// Bin is the legacy single-binary key: read-compat only. On Load,
	// when Binaries is absent, Bin is migrated into a Binaries entry
	// named DefaultBinaryName and Bin is cleared in memory — so the next
	// Save writes only the new form (DESIGN §3.2a). Callers must use
	// ResolveBinary instead of reading Bin directly.
	Bin string `json:"llama-server-bin,omitempty"`
	// Binaries lists every named llama-server build. Empty (absent) with
	// a legacy Bin set is migrated at load; both absent is a validation
	// error.
	Binaries []Binary `json:"binaries,omitempty"`
	// DefaultBinary names the Binaries entry used when nothing else
	// pins one. Empty means the first entry (or the migrated legacy
	// entry, DefaultBinaryName).
	DefaultBinary string   `json:"default-binary,omitempty"`
	Host          string   `json:"ip_address"`
	Port          int      `json:"port"`
	ModelsFiles   []string `json:"models-files,omitempty"`
}

// FindBinary returns the binaries[] entry with the given name — the one
// lookup every caller uses (resolution, CLI --binary validation, preset
// pin validation, reattach, the TUI pickers).
func (g Globals) FindBinary(name string) (Binary, bool) {
	for _, b := range g.Binaries {
		if b.Name == name {
			return b, true
		}
	}
	return Binary{}, false
}

// ResolveBinary returns the binary to launch with, per the DESIGN §3.2a
// chain: override (CLI --binary or the TUI one-shot quick-switch) →
// preset pin → default. Source reports why: "override", "preset", or
// "default". A preset pin naming an unknown entry warns (caller's job)
// and falls through to the default; an unknown/empty default resolves
// to the first entry. With no binaries at all, a placeholder built from
// the legacy Bin is returned so the launch attempt fails with a path
// named in the error.
func (g Globals) ResolveBinary(override, presetPin string) (Binary, string) {
	find := g.FindBinary
	if override != "" {
		if b, ok := find(override); ok {
			return b, "override"
		}
	}
	if presetPin != "" {
		if b, ok := find(presetPin); ok {
			return b, "preset"
		}
	}
	if g.DefaultBinary != "" {
		if b, ok := find(g.DefaultBinary); ok {
			return b, "default"
		}
	}
	if len(g.Binaries) > 0 {
		return g.Binaries[0], "default"
	}
	return Binary{Name: DefaultBinaryName, Path: g.Bin}, "default"
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
	// Binary, when set, pins the preset to a named Globals.Binaries
	// entry (DESIGN §3.2a). Absent = default binary. An unknown name is
	// a validation warning that falls back to the default at launch.
	Binary string `json:"binary,omitempty"`

	Params Params `json:"params"`
}
