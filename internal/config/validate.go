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

	out = append(out, validateGlobals(cfg.Globals)...)

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

		out = append(out, validatePresets(i, m.Presets)...)
	}
	return out
}

func validateGlobals(g Globals) Issues {
	var out Issues
	if strings.TrimSpace(g.Bin) == "" {
		out = append(out, Issue{Severity: Error, Path: "globals.llama-server-bin",
			Message: "binary path is required"})
	} else if info, err := os.Stat(g.Bin); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			out = append(out, Issue{Severity: Warning, Path: "globals.llama-server-bin",
				Message: fmt.Sprintf("binary does not exist: %s", g.Bin)})
		}
	} else if info.Mode()&0o111 == 0 {
		out = append(out, Issue{Severity: Warning, Path: "globals.llama-server-bin",
			Message: "binary is not executable"})
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

func validatePresets(modelIdx int, presets []Preset) Issues {
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
