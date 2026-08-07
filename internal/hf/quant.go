package hf

import (
	"context"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// QuantOption is one pickable quantization of a repo (DESIGN §16.3).
// Tag is the quant tag extracted from the file names (uppercased), or
// the file base name when no tag parses — the value that becomes the
// `:quant` suffix of a config `hf` entry. Split models group into one
// option whose Size is the sum of their parts.
type QuantOption struct {
	Tag   string
	Files []RepoFile
	Size  int64
}

// quantTagRE matches llama.cpp's get_gguf_split_info tag rule
// (verified): a trailing [-.]<tag> where the tag is alnum/underscore,
// case-insensitive, uppercased on extraction.
var quantTagRE = regexp.MustCompile(`(?i)[-.]([a-z0-9_]+)$`)

// splitRE matches llama.cpp's split-model suffix -NNNNN-of-NNNNN.
var splitRE = regexp.MustCompile(`(?i)^(.+)-([0-9]{5})-of-([0-9]{5})$`)

// Quants turns a repo file listing into a sized quant list (DESIGN
// §16.3): .gguf files only, split parts grouped and summed, sorted by
// size ascending with ties by tag. A pure function — no network.
func Quants(files []RepoFile) []QuantOption {
	groups := make(map[string][]RepoFile)
	for _, f := range files {
		if !isGGUF(f.Path) {
			continue
		}
		key := quantTag(f.Path)
		groups[key] = append(groups[key], f)
	}
	out := make([]QuantOption, 0, len(groups))
	for tag, fs := range groups {
		var size int64
		for _, f := range fs {
			size += f.Size
		}
		out = append(out, QuantOption{Tag: tag, Files: fs, Size: size})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Size != out[j].Size {
			return out[i].Size < out[j].Size
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

// HasMMProj reports whether the repo provides a multimodal projector
// (informational only — llama.cpp downloads it alongside the model;
// DESIGN §16.3, §3.8b).
func HasMMProj(files []RepoFile) bool {
	for _, f := range files {
		if strings.HasSuffix(strings.ToLower(f.Path), ".mmproj") {
			return true
		}
	}
	return false
}

// Choose fetches a repo's file list and returns its quants — the
// single network wrapper behind every quant picker.
func Choose(ctx context.Context, c *Client, repo string) ([]QuantOption, error) {
	files, err := c.Tree(ctx, repo, "main")
	if err != nil {
		return nil, err
	}
	return Quants(files), nil
}

// HumanSize formats bytes for pickers: 1.2 GiB / 512.0 MiB / 9.0 KiB /
// 42 B (binary units).
func HumanSize(n int64) string {
	const unit = 1024
	switch {
	case n >= unit*unit*unit:
		return trimOne(n, unit*unit*unit) + " GiB"
	case n >= unit*unit:
		return trimOne(n, unit*unit) + " MiB"
	case n >= unit:
		return trimOne(n, unit) + " KiB"
	default:
		return strconv.FormatInt(n, 10) + " B"
	}
}

// trimOne renders n/unit with one decimal, trailing ".0" trimmed
// (1.0 → 1, 1.25 → 1.2).
func trimOne(n, unit int64) string {
	return strings.TrimSuffix(strconv.FormatFloat(float64(n)/float64(unit), 'f', 1, 64), ".0")
}

// isGGUF reports whether a repo path names a .gguf file.
func isGGUF(p string) bool {
	base := filepath.Base(p)
	return len(base) >= 5 && strings.EqualFold(base[len(base)-5:], ".gguf")
}

// quantTag extracts the quant tag from a repo path per llama.cpp's
// rule, or falls back to the file base name (without .gguf) when no
// tag parses.
func quantTag(p string) string {
	name := filepath.Base(p)
	if isGGUF(name) {
		name = name[:len(name)-5]
	}
	if m := splitRE.FindStringSubmatch(name); m != nil {
		name = m[1]
	}
	if m := quantTagRE.FindStringSubmatch(name); m != nil {
		return strings.ToUpper(m[1])
	}
	return name
}
