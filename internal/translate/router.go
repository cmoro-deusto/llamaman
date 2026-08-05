package translate

import (
	"strconv"

	"github.com/cmoro-deusto/llamaman/internal/config"
	"github.com/cmoro-deusto/llamaman/internal/flags"
)

// RouterBuild produces the argv vector for a router-mode launch:
//
//	<bin> --models-preset <file> --host <host> --port <port> --metrics
//
// Router mode (llama.cpp model presets, added Dec 2025) serves every
// model in the my-models.ini file from a single llama-server process;
// llamaman treats the file — not an individual section — as the run
// target. The listen address comes from globals like any other launch.
//
// --metrics is always passed: the router only serves per-model
// statistics (GET /metrics?model=..., GET /slots?model=...) when it is
// enabled, and llamaman owns the spawn so there is no reason to leave
// it off. The registry is consulted for canonical flag forms; if the
// running llama-server lacks --models-preset entirely, callers should
// surface a version-gate error before reaching Spawn (see main.go).
func RouterBuild(globals config.Globals, file string, reg flags.Registry) Result {
	return Result{Argv: []string{
		globals.Bin,
		flags.CanonicalForm("models-preset", reg), file,
		flags.CanonicalForm("host", reg), globals.Host,
		flags.CanonicalForm("port", reg), strconv.Itoa(globals.Port),
		flags.CanonicalForm("metrics", reg),
	}}
}
