// Package translate turns a (globals, model, preset) tuple into the argv
// vector llamaman exec()s.
package translate

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// Result is what Build produces: the argv plus any warnings the user
// should see (typically: param keys not in the parsed --help registry).
type Result struct {
	Argv     []string
	Warnings []string
}

// Build produces the argv vector for a llama-server launch.
//
// Order per DESIGN.md §6.1:
//
//	<bin> {-m <location> | -hf <id>} --alias <alias> --host <host> <preset.params...> --port <port>
//
// The model source is either local (`-m <location>`, the default) or a
// Hugging Face identifier (`-hf <id>`) when model.HF is non-empty. The
// schema enforces exactly one (see config.Validate).
//
// If a preset param's key overlaps with an auto-added flag — currently
// {m, hf, alias, host, port} — the preset value wins and the auto-added
// entry is suppressed. This lets a preset redirect a model's source as
// the universal escape hatch, mirroring how host/port overrides work.
//
// reg is consulted first for canonical CLI form; unknown keys fall back
// to the hard-coded short-form set (DESIGN.md §6.2). Unknown keys also
// produce a warning in the returned Result.
func Build(globals config.Globals, model config.Model, preset config.Preset, reg flags.Registry) (Result, error) {
	overrides := overrideSet(preset.Params)

	argv := []string{globals.Bin}
	switch {
	case model.IsHF():
		if !overrides["hf"] && !overrides["m"] {
			argv = append(argv, flags.CanonicalForm("hf", reg), model.HF)
		}
	default:
		if !overrides["m"] && !overrides["hf"] {
			argv = append(argv, flags.CanonicalForm("m", reg), model.Location)
		}
	}
	if !overrides["alias"] {
		argv = append(argv, flags.CanonicalForm("alias", reg), model.Alias)
	}
	if !overrides["host"] {
		argv = append(argv, flags.CanonicalForm("host", reg), globals.Host)
	}

	var warnings []string
	for _, p := range preset.Params {
		entry, warn, err := renderParam(p, reg)
		if err != nil {
			return Result{}, err
		}
		argv = append(argv, entry...)
		if warn != "" {
			warnings = append(warnings, warn)
		}
	}

	if !overrides["port"] {
		argv = append(argv, flags.CanonicalForm("port", reg), strconv.Itoa(globals.Port))
	}
	return Result{Argv: argv, Warnings: warnings}, nil
}

func overrideSet(params config.Params) map[string]bool {
	o := make(map[string]bool, 5)
	for _, p := range params {
		switch p.Key {
		case "m", "hf", "alias", "host", "port":
			o[p.Key] = true
		}
	}
	return o
}

func renderParam(p config.Param, reg flags.Registry) (entry []string, warning string, err error) {
	flag := flags.CanonicalForm(p.Key, reg)
	if reg != nil {
		if _, ok := reg.Lookup(p.Key); !ok {
			warning = fmt.Sprintf("unknown flag %q (passed through as %s)", p.Key, flag)
		}
	}
	switch v := p.Value.(type) {
	case bool:
		if v {
			return []string{flag}, warning, nil
		}
		return nil, warning, nil
	case json.Number:
		return []string{flag, v.String()}, warning, nil
	case string:
		return []string{flag, v}, warning, nil
	default:
		return nil, "", fmt.Errorf("param %q: unsupported value type %T", p.Key, p.Value)
	}
}
