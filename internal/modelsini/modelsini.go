// Package modelsini parses and serializes llama.cpp model-presets files
// (the "my-models.ini" format consumed by llama-server --models-preset).
//
// The grammar mirrors llama.cpp's own PEG parser (common/preset.cpp):
//
//	section ::= "[" <name> "]" eol
//	kv      ::= ident ws "=" ws <value> eol
//	comment ::= [;#] ... eol
//	blank   ::= ws eol
//
// Keys before any section header belong to the implicit "[default]" section,
// matching llama.cpp (COMMON_PRESET_DEFAULT_NAME). "[*]" is the global
// section applied to every model; callers merge it themselves.
//
// Structural parse errors are hard errors (llama.cpp aborts on malformed
// lines). Unknown *keys* are NOT an error here — that is the job of the
// import layer, which has the flag registry.
package modelsini

// DefaultName is the fallback section (COMMON_PRESET_DEFAULT_NAME in
// llama.cpp): keys before any header and unmatched model ids resolve here.
const DefaultName = "default"

// GlobalName is the global section applied to every model ("[*]").
const GlobalName = "*"

// File is a parsed INI file, preserving section and key order.
type File struct {
	Sections []Section
}

// Section is one [name] block with its keys in file order.
type Section struct {
	// Name is the raw section name between brackets, e.g. "my-model",
	// DefaultName, or GlobalName. Whitespace is significant (llama.cpp
	// does not trim it).
	Name string

	// Comment holds comment lines that appeared directly above this
	// section header (empty for sections at the top of the file). The
	// export layer uses "description:" comments for round-tripping.
	Comment []string

	Keys []Key
}

// Key is a single `name = value` pair.
type Key struct {
	Name  string
	Value string
}

// Get returns the value of the first key with the given name.
func (s *Section) Get(name string) (string, bool) {
	for _, k := range s.Keys {
		if k.Name == name {
			return k.Value, true
		}
	}
	return "", false
}

// Set inserts or replaces a key, preserving the position of the first
// occurrence (later values win, matching llama.cpp's map semantics).
func (s *Section) Set(name, value string) {
	for i := range s.Keys {
		if s.Keys[i].Name == name {
			s.Keys[i].Value = value
			return
		}
	}
	s.Keys = append(s.Keys, Key{Name: name, Value: value})
}

// Section returns the section with the given name, or nil.
func (f *File) Section(name string) *Section {
	for i := range f.Sections {
		if f.Sections[i].Name == name {
			return &f.Sections[i]
		}
	}
	return nil
}

// Global returns the "[*]" section, or nil when absent.
func (f *File) Global() *Section { return f.Section(GlobalName) }
