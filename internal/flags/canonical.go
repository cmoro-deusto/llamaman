package flags

// CanonicalForm returns the canonical CLI form (e.g. "--ctx-size", "-m")
// for a parameter key. It first consults the parsed --help registry; if
// reg is nil or doesn't know the key, it falls back to the hard-coded
// short-form set via Canonical.
func CanonicalForm(name string, reg Registry) string {
	if reg != nil {
		if fi, ok := reg.Lookup(name); ok {
			return fi.Form
		}
	}
	return Canonical(name)
}
