package flags

import "strconv"

// ExtractAddr walks argv looking for the canonical --host and --port
// tokens (canonical form sourced from the parsed --help registry, with
// fallback to the hard-coded set when reg is nil). Returns the values
// passed to llama-server, regardless of whether they came from globals
// or a preset override. ok is true only when both are present.
//
// The function relies on translate.Build's invariant that --host and
// --port always end up in argv (auto-added or preset-rendered). It
// tolerates only the two-token form (`--host VALUE`); the `--host=VALUE`
// form is never emitted by translate.Build, so it is not parsed here.
func ExtractAddr(argv []string, reg Registry) (host string, port int, ok bool) {
	hostFlag := CanonicalForm("host", reg)
	portFlag := CanonicalForm("port", reg)
	gotHost, gotPort := false, false
	for i := 0; i < len(argv)-1; i++ {
		switch argv[i] {
		case hostFlag:
			host = argv[i+1]
			gotHost = true
		case portFlag:
			if p, err := strconv.Atoi(argv[i+1]); err == nil {
				port = p
				gotPort = true
			}
		}
	}
	return host, port, gotHost && gotPort
}
