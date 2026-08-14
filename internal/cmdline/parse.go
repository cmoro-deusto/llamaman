package cmdline

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// Source is the model identity parsed out of the command line. At most
// one of Location/HF is set (both is an error); Alias is the first
// comma-part of the --alias value, when present.
type Source struct {
	Location string
	HF       string
	Alias    string
}

// Result is the parsed command line: the model source (if any), the
// ordered preset params (every non-model flag, including --alias), and
// non-blocking warnings. Parse errors are returned separately and block
// the import.
type Result struct {
	Source   Source
	Params   config.Params
	Warnings []string
}

// modelSourceKeys are the registry keys that name the model source —
// the same set modelsini.sourceOf accepts, plus --model-file.
var modelSourceKeys = map[string]bool{
	"m": true, "model": true, "model-file": true,
	"hf": true, "hf-repo": true,
}

// jsonNumberRE is the JSON number grammar (same as modelsini's) —
// stricter than strconv.ParseFloat, which also accepts hex floats,
// "Inf", and "NaN".
var jsonNumberRE = regexp.MustCompile(`^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$`)

// Parse converts argv tokens into a Result, validating every flag
// against the registry (per-alias keys: "-m", "--model" resolve
// directly). A leading "llama-server" binary token (any path) is
// dropped; a bare flag list works unchanged.
//
// Errors block the import: a value-flag with a missing value, an empty
// `--flag=`, a non-numeric value for a known numeric flag, -m and -hf
// together, a repeated model source, an invalid HF id, a bool flag
// given a value is a warning. The Result is still populated best-effort
// so the caller can show what parsed.
//
// Warnings never block: unknown flags (imported best-effort), a
// repeated flag overwritten (last wins — matches llama.cpp and
// config.Params.Set), a repeated --alias (first kept), a bool flag
// given a value (value ignored), and stray positional arguments.
func Parse(argv []string, reg flags.Registry) (Result, error) {
	if len(argv) > 0 && filepath.Base(argv[0]) == "llama-server" {
		argv = argv[1:]
	}
	var (
		res   Result
		errs  []error
		seen  = map[string]bool{} // param keys already stored (duplicate warning)
		sawM  bool
		sawHF bool
	)
	for i := 0; i < len(argv); i++ {
		tok := argv[i]
		if !strings.HasPrefix(tok, "-") || tok == "-" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("unexpected argument %q ignored", tok))
			continue
		}
		namePart, valPart, hasEq := strings.Cut(tok, "=")
		key := strings.TrimLeft(namePart, "-")
		if key == "" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("ignored token %q", tok))
			continue
		}
		fi, known := reg.Lookup(key)

		// Value acquisition: known bool flags take none; everything
		// else consumes the =value or the next token (unless that next
		// token is itself a known flag — a missing value is an error,
		// not a swallowed flag).
		needValue := !known || !fi.IsBool
		value, hasValue := valPart, hasEq
		if !hasEq && needValue {
			if i+1 < len(argv) && !isFlagToken(argv[i+1], reg) {
				i++
				value = argv[i]
				hasValue = true
			}
		}

		switch {
		case modelSourceKeys[key]:
			if !hasValue {
				errs = append(errs, fmt.Errorf("%s: missing value", tok))
				continue
			}
			if hasEq && value == "" {
				errs = append(errs, fmt.Errorf("%s: empty value", tok))
				continue
			}
			if key == "m" || key == "model" || key == "model-file" {
				if sawM {
					errs = append(errs, fmt.Errorf("model source repeated: %s", tok))
					continue
				}
				sawM = true
				res.Source.Location = strings.TrimSpace(value)
			} else {
				if sawHF {
					errs = append(errs, fmt.Errorf("model source repeated: %s", tok))
					continue
				}
				sawHF = true
				res.Source.HF = strings.TrimSpace(value)
			}
		case key == "alias":
			if !hasValue {
				errs = append(errs, fmt.Errorf("%s: missing value", tok))
				continue
			}
			if hasEq && value == "" {
				errs = append(errs, fmt.Errorf("%s: empty value", tok))
				continue
			}
			if res.Source.Alias != "" {
				res.Warnings = append(res.Warnings, fmt.Sprintf("alias repeated — first value kept (%q)", res.Source.Alias))
				continue
			}
			res.Source.Alias = firstComma(value)
			res.Params.Set("alias", value)
			seen["alias"] = true
		case known && fi.IsBool:
			if hasValue {
				res.Warnings = append(res.Warnings, fmt.Sprintf("%s is boolean — value %q ignored", tok, value))
			}
			addParam(&res, seen, tok, true)
		case known && !fi.IsBool:
			if !hasValue {
				errs = append(errs, fmt.Errorf("%s: missing value", tok))
				continue
			}
			if hasEq && value == "" {
				errs = append(errs, fmt.Errorf("%s: empty value", tok))
				continue
			}
			if fi.Kind == flags.KindNumeric && !jsonNumberRE.MatchString(value) {
				errs = append(errs, fmt.Errorf("%s: %q is not a valid number", tok, value))
				continue
			}
			if fi.Kind == flags.KindNumeric {
				addParam(&res, seen, tok, json.Number(value))
			} else {
				addParam(&res, seen, tok, value)
			}
		default: // unknown flag: best-effort import (warning only)
			res.Warnings = append(res.Warnings, fmt.Sprintf("unknown flag %s (imported best-effort)", tok))
			if hasValue {
				addParam(&res, seen, tok, value)
			} else {
				addParam(&res, seen, tok, true)
			}
		}
	}
	if res.Source.Location != "" && res.Source.HF != "" {
		errs = append(errs, fmt.Errorf("both -m and -hf given — pick one model source"))
	}
	if res.Source.HF != "" && !config.ValidHFIdentifier(res.Source.HF) {
		errs = append(errs, fmt.Errorf("invalid HF identifier %q (expected org/repo[:quant])", res.Source.HF))
	}
	return res, errors.Join(errs...)
}

// addParam stores a param, warning when an earlier occurrence is
// overwritten (last wins, matching llama.cpp and config.Params.Set).
func addParam(res *Result, seen map[string]bool, tok string, value any) {
	before, _, _ := strings.Cut(tok, "=")
	key := strings.TrimLeft(before, "-")
	if seen[key] {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%s repeated — last value wins", tok))
	}
	seen[key] = true
	res.Params.Set(key, value)
}

// isFlagToken reports whether tok looks like a flag that should not be
// consumed as a value: a known registry key, a model-source key, or the
// alias key. Unknown flags ARE consumed as values (llama.cpp consumes
// the next argument unconditionally).
func isFlagToken(tok string, reg flags.Registry) bool {
	if !strings.HasPrefix(tok, "-") || tok == "-" {
		return false
	}
	name, _, _ := strings.Cut(tok, "=")
	key := strings.TrimLeft(name, "-")
	if key == "" {
		return false
	}
	if modelSourceKeys[key] || key == "alias" {
		return true
	}
	_, ok := reg.Lookup(key)
	return ok
}

// firstComma returns the first comma-part of an --alias list (llama.cpp
// allows a comma-separated list), trimmed.
func firstComma(s string) string {
	if first, _, _ := strings.Cut(s, ","); strings.TrimSpace(first) != "" {
		return strings.TrimSpace(first)
	}
	return strings.TrimSpace(s)
}
