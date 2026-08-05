package modelsini

import (
	"fmt"
	"strings"
)

// ValidateRouterAliases checks the alias uniqueness llama.cpp's router
// enforces at startup: every section's routing name — the comma-split
// `alias` key values, else the section name — must be unique across the
// file. Duplicate aliases abort llama-server with a cryptic error, so
// llamaman surfaces them with the offending sections before spawning.
//
// "[*]" and "[default]" are excluded: they carry shared params, they are
// not models.
func (f *File) ValidateRouterAliases() error {
	// first[s] = the section that claimed alias s.
	first := make(map[string]string)
	conflicts := make(map[string][]string) // alias -> sections using it
	for i := range f.Sections {
		s := &f.Sections[i]
		switch s.Name {
		case GlobalName, DefaultName:
			continue
		}
		names := []string{s.Name}
		if v, ok := s.Get("alias"); ok && strings.TrimSpace(v) != "" {
			names = nil
			for _, part := range strings.Split(v, ",") {
				if p := strings.TrimSpace(part); p != "" {
					names = append(names, p)
				}
			}
		}
		for _, n := range names {
			if owner, seen := first[n]; seen {
				conflicts[n] = append(conflicts[n], owner, s.Name)
			} else {
				first[n] = s.Name
			}
		}
	}
	if len(conflicts) == 0 {
		return nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "router aliases conflict — llama.cpp requires one unique alias per model section: ")
	firstConflict := true
	for alias, sections := range conflicts {
		if !firstConflict {
			b.WriteString("; ")
		}
		firstConflict = false
		fmt.Fprintf(&b, "%q used by sections %s", alias, quotedList(dedupe(sections)))
	}
	return fmt.Errorf("%s", b.String())
}

func quotedList(items []string) string {
	seen := make(map[string]bool, len(items))
	parts := make([]string, 0, len(items))
	for _, it := range items {
		if !seen[it] {
			seen[it] = true
			parts = append(parts, fmt.Sprintf("[%s]", it))
		}
	}
	return strings.Join(parts, ", ")
}

func dedupe(items []string) []string {
	seen := make(map[string]bool, len(items))
	out := make([]string, 0, len(items))
	for _, it := range items {
		if !seen[it] {
			seen[it] = true
			out = append(out, it)
		}
	}
	return out
}
