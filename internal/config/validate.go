package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// hfIdentifierRE matches `org/repo` and `org/repo:quant` shapes. We
// permit `[\w.-]+` on each segment so `Qwen/Qwen3-32B-GGUF:Q4_K_M`
// passes. No network reachability check — that's llama-server's job at
// launch time.
var hfIdentifierRE = regexp.MustCompile(`^[\w.-]+/[\w.-]+(?::[\w.-]+)?$`)

// ValidHFIdentifier reports whether s parses as a Hugging Face
// identifier (`<user>/<model>` with an optional `:quant` suffix).
// Exposed so the TUI's huh.Validate can reuse the same check.
func ValidHFIdentifier(s string) bool {
	return hfIdentifierRE.MatchString(strings.TrimSpace(s))
}

// Severity classifies a validation finding. Errors block save; warnings
// surface in the TUI but don't prevent persisting.
type Severity int

const (
	Warning Severity = iota
	Error
)

// Issue is a single validation finding. Path is a dotted location like
// "models[0].presets[1].params" so the editor can highlight the row.
type Issue struct {
	Severity Severity
	Path     string
	Message  string
}

// Issues is a slice of Issue with a couple of convenience methods.
type Issues []Issue

// HasErrors reports whether any issue has Severity == Error.
func (i Issues) HasErrors() bool {
	for _, it := range i {
		if it.Severity == Error {
			return true
		}
	}
	return false
}

// FilterBySeverity returns only issues at or above the given severity.
func (i Issues) FilterBySeverity(min Severity) Issues {
	out := make(Issues, 0, len(i))
	for _, it := range i {
		if it.Severity >= min {
			out = append(out, it)
		}
	}
	return out
}

// Validate runs cross-field validation per DESIGN.md §3.5. Errors block
// save; warnings (missing files, unknown flags) don't.
func Validate(cfg *Config) Issues {
	var out Issues

	if cfg.Version != SchemaVersion {
		out = append(out, Issue{Severity: Error, Path: "version",
			Message: fmt.Sprintf("schema version %d not supported (expected %d)", cfg.Version, SchemaVersion)})
	}

	knownBinaries := make(map[string]bool, len(cfg.Globals.Binaries))
	for _, b := range cfg.Globals.Binaries {
		knownBinaries[b.Name] = true
	}

	out = append(out, validateGlobals(cfg.Globals)...)
	out = append(out, validatePreferences(cfg.Prefs())...)

	aliasSeen := make(map[string]int)
	for i, m := range cfg.Models {
		if m.Alias == "" {
			out = append(out, Issue{Severity: Error,
				Path:    fmt.Sprintf("models[%d].alias", i),
				Message: "alias is required",
			})
		} else if prev, dup := aliasSeen[m.Alias]; dup {
			out = append(out, Issue{Severity: Error,
				Path:    fmt.Sprintf("models[%d].alias", i),
				Message: fmt.Sprintf("alias %q duplicates models[%d]", m.Alias, prev),
			})
		} else {
			aliasSeen[m.Alias] = i
		}
		switch {
		case m.Location == "" && m.HF == "":
			out = append(out, Issue{Severity: Error,
				Path:    fmt.Sprintf("models[%d]", i),
				Message: "either `location` (local file) or `hf` (Hugging Face identifier) is required",
			})
		case m.Location != "" && m.HF != "":
			out = append(out, Issue{Severity: Error,
				Path:    fmt.Sprintf("models[%d]", i),
				Message: "`location` and `hf` are mutually exclusive — set exactly one",
			})
		case m.Location != "":
			if _, err := os.Stat(m.Location); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					out = append(out, Issue{Severity: Warning,
						Path:    fmt.Sprintf("models[%d].location", i),
						Message: fmt.Sprintf("file does not exist: %s", m.Location),
					})
				}
			}
		case m.HF != "":
			if !ValidHFIdentifier(m.HF) {
				out = append(out, Issue{Severity: Error,
					Path:    fmt.Sprintf("models[%d].hf", i),
					Message: fmt.Sprintf("not a valid HF identifier (expected `org/repo[:quant]`): %s", m.HF),
				})
			}
		}

		out = append(out, validatePresets(i, m.Presets, knownBinaries)...)
	}
	return out
}

// validatePreferences holds the config-level rules for the preferences
// object (DESIGN §15.1, §16.1). Theme/animations/log-colors are
// type-checked by JSON decode, and the zero value equals the defaults,
// so there is nothing to flag at config level for them — the *semantic*
// theme check deliberately lives in the TUI resolver (config cannot
// import tui, and duplicating the palette-name list would break P8).
// models-dir has one rule: when set and the path exists but is not a
// directory, warn (P3) — the directory itself may not exist yet (the
// Storage Manager's download action creates it). logo-effect is
// enumerated ("once"/"loop", §15.5a): an unknown value warns (P3) and
// the TUI falls back to the default.
func validatePreferences(p Preferences) Issues {
	var out Issues
	switch p.LogoEffect {
	case "", LogoEffectOnce, LogoEffectLoop:
	default:
		out = append(out, Issue{Severity: Warning, Path: "preferences.logo-effect",
			Message: fmt.Sprintf("unknown logo-effect %q (want %q or %q), using %q",
				p.LogoEffect, LogoEffectOnce, LogoEffectLoop, LogoEffectOnce)})
	}
	// keep the bounds in sync with hf.MaxConnections (config cannot
	// import the network client; the runtime clamp lives there)
	if p.DownloadConnections < 0 || p.DownloadConnections > 16 {
		out = append(out, Issue{Severity: Warning, Path: "preferences.download-connections",
			Message: fmt.Sprintf("download-connections %d out of range (1–16; 0/absent = default), clamped at runtime",
				p.DownloadConnections)})
	}
	if p.ModelsDir == "" {
		return out
	}
	if info, err := os.Stat(p.ModelsDir); err == nil && !info.IsDir() {
		out = append(out, Issue{Severity: Warning, Path: "preferences.models-dir",
			Message: fmt.Sprintf("models-dir is not a directory: %s", p.ModelsDir)})
	}
	return out
}

func validateGlobals(g Globals) Issues {
	var out Issues
	if len(g.Binaries) == 0 {
		// Load migrates a legacy Bin into Binaries, so reaching here with
		// both empty means there is no binary to run anything (§3.5).
		out = append(out, Issue{Severity: Error, Path: "globals.binaries",
			Message: "at least one llama-server binary is required"})
	}
	if strings.TrimSpace(g.Bin) != "" && len(g.Binaries) > 0 {
		// Migration only rewrites a legacy-only config, so the key can
		// legitimately coexist with binaries[] — but nothing reads it
		// then (DESIGN §3.2a). Say so instead of ignoring the edit.
		out = append(out, Issue{Severity: Warning, Path: "globals.llama-server-bin",
			Message: "ignored: globals.binaries is set, so this legacy key has no effect"})
	}
	seen := make(map[string]bool, len(g.Binaries))
	for i, b := range g.Binaries {
		path := fmt.Sprintf("globals.binaries[%d]", i)
		if strings.TrimSpace(b.Name) == "" {
			out = append(out, Issue{Severity: Error, Path: path + ".name",
				Message: "binary name is required"})
		} else if seen[b.Name] {
			out = append(out, Issue{Severity: Error, Path: path + ".name",
				Message: fmt.Sprintf("duplicate binary name %q", b.Name)})
		}
		seen[b.Name] = true
		if strings.TrimSpace(b.Path) == "" {
			out = append(out, Issue{Severity: Error, Path: path + ".path",
				Message: "binary path is required"})
		} else if info, err := os.Stat(b.Path); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				out = append(out, Issue{Severity: Warning, Path: path + ".path",
					Message: fmt.Sprintf("binary %q does not exist: %s", b.Name, b.Path)})
			}
		} else if info.Mode()&0o111 == 0 {
			out = append(out, Issue{Severity: Warning, Path: path + ".path",
				Message: fmt.Sprintf("binary %q is not executable", b.Name)})
		}
	}
	if g.DefaultBinary != "" && !seen[g.DefaultBinary] {
		out = append(out, Issue{Severity: Warning, Path: "globals.default-binary",
			Message: fmt.Sprintf("default binary %q names no entry; falling back to the first entry", g.DefaultBinary)})
	}
	if !ValidHost(g.Host) {
		out = append(out, Issue{Severity: Error, Path: "globals.ip_address",
			Message: fmt.Sprintf("invalid host %q (expected IPv4, [::IPv6], or hostname)", g.Host)})
	}
	if g.Port < 1 || g.Port > 65535 {
		out = append(out, Issue{Severity: Error, Path: "globals.port",
			Message: fmt.Sprintf("port %d out of range 1..65535", g.Port)})
	}
	for i, mf := range g.ModelsFiles {
		if strings.TrimSpace(mf) == "" {
			out = append(out, Issue{Severity: Error, Path: fmt.Sprintf("globals.models-files[%d]", i),
				Message: "models file path is required"})
			continue
		}
		if _, err := os.Stat(mf); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				out = append(out, Issue{Severity: Warning, Path: fmt.Sprintf("globals.models-files[%d]", i),
					Message: fmt.Sprintf("models file does not exist: %s", mf)})
			}
		}
	}
	return out
}

func validatePresets(modelIdx int, presets []Preset, knownBinaries map[string]bool) Issues {
	var out Issues
	nameSeen := make(map[string]int)
	for i, p := range presets {
		if p.Name == "" {
			out = append(out, Issue{Severity: Error,
				Path:    fmt.Sprintf("models[%d].presets[%d].preset", modelIdx, i),
				Message: "preset name is required"})
		} else if prev, dup := nameSeen[p.Name]; dup {
			out = append(out, Issue{Severity: Error,
				Path:    fmt.Sprintf("models[%d].presets[%d].preset", modelIdx, i),
				Message: fmt.Sprintf("preset name %q duplicates [%d]", p.Name, prev)})
		} else {
			nameSeen[p.Name] = i
		}

		paramSeen := make(map[string]int)
		for j, pr := range p.Params {
			if pr.Key == "" {
				out = append(out, Issue{Severity: Error,
					Path:    fmt.Sprintf("models[%d].presets[%d].params[%d]", modelIdx, i, j),
					Message: "param key is required"})
			} else if prev, dup := paramSeen[pr.Key]; dup {
				out = append(out, Issue{Severity: Warning,
					Path:    fmt.Sprintf("models[%d].presets[%d].params[%d]", modelIdx, i, j),
					Message: fmt.Sprintf("param %q already defined at [%d]", pr.Key, prev)})
			} else {
				paramSeen[pr.Key] = j
			}
		}
		if p.Binary != "" && !knownBinaries[p.Binary] {
			out = append(out, Issue{Severity: Warning,
				Path:    fmt.Sprintf("models[%d].presets[%d].binary", modelIdx, i),
				Message: fmt.Sprintf("binary %q names no globals.binaries entry; falling back to the default binary", p.Binary)})
		}
	}
	return out
}

// ValidHost accepts an IPv4 literal, an IPv6 literal in brackets ([::1]),
// or a non-empty DNS-ish hostname. We don't try to be RFC-strict — if it
// parses or matches a permissive name pattern, we accept it.
func ValidHost(host string) bool {
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	// IPv6 literal in brackets.
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		return net.ParseIP(host[1:len(host)-1]) != nil
	}
	// IPv4.
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	// Hostname: must contain only ASCII alnum, dots, and hyphens, and at
	// least one alpha character somewhere.
	for _, r := range host {
		if !(r == '-' || r == '.' ||
			(r >= '0' && r <= '9') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= 'a' && r <= 'z')) {
			return false
		}
	}
	return true
}
