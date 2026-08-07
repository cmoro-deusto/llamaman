package storage

import (
	"regexp"
	"strings"
)

// Layout identifies the on-disk form of a cache entry (DESIGN §16.1).
type Layout int

const (
	// LayoutUnknown matches no known form; Scan reports it as a warning.
	LayoutUnknown Layout = iota
	// LayoutHFHub is the current llama.cpp layout (since PR #20775):
	// <root>/models--<org>--<model>/{refs,blobs,snapshots}.
	LayoutHFHub
	// LayoutLegacyFolder is the older <org>__<model>/<file> form.
	LayoutLegacyFolder
	// LayoutLegacyFlat is the older flat <org>__<repo>__<file>.gguf
	// form with .etag sidecars and manifest=… metadata (pre-#20775).
	LayoutLegacyFlat
	// LayoutMeta is recognized legacy metadata (.etag, manifest=…):
	// skipped silently, never warned about.
	LayoutMeta
)

const (
	hubPrefix = "models--" // llama.cpp repo_to_folder_name prefix
	hubSep    = "--"       // llama.cpp repo_to_folder_name separator
	legacySep = "__"       // legacy llama.cpp separator
)

var (
	etagRE = regexp.MustCompile(`\.etag$`)
	segRE  = regexp.MustCompile(`^[\w.-]+$`)
)

// RepoFolderNames returns the candidate repo folder names for a repo id
// (org/repo, no quant) in the current (HF hub) and legacy layouts.
func RepoFolderNames(repoID string) []string {
	return []string{
		hubPrefix + strings.ReplaceAll(repoID, "/", hubSep),
		strings.ReplaceAll(repoID, "/", legacySep),
	}
}

// DetectLayout classifies one cache-root child by name and type
// (DESIGN §16.1 detection rules 1–5).
func DetectLayout(name string, isDir bool) Layout {
	switch {
	case isDir && strings.HasPrefix(name, hubPrefix) && validHubRepo(name):
		return LayoutHFHub
	case isDir:
		if _, _, ok := splitLegacyFolder(name); ok {
			return LayoutLegacyFolder
		}
	case !isDir:
		if _, ok := parseLegacyFlat(name); ok {
			return LayoutLegacyFlat
		}
		if etagRE.MatchString(name) || strings.HasPrefix(name, "manifest=") {
			return LayoutMeta
		}
	}
	return LayoutUnknown
}

// validHubRepo reports whether a models--… name converts to a valid repo
// id: exactly one '/' after the -- → / conversion (llama.cpp requires
// exactly one slash; DESIGN §16.1 detection rule 1).
func validHubRepo(name string) bool {
	repo := strings.ReplaceAll(strings.TrimPrefix(name, hubPrefix), hubSep, "/")
	return repo != "" && strings.Count(repo, "/") == 1 && validSegments(repo, "/")
}

// splitLegacyFolder parses an <org>__<model> directory name (exactly
// two non-empty segments; the legacy folder form).
func splitLegacyFolder(name string) (org, model string, ok bool) {
	parts := strings.Split(name, legacySep)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	if !segRE.MatchString(parts[0]) || !segRE.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// parseLegacyFlat parses a flat <org>__<repo>__<file>.gguf/.mmproj name.
// The file part is split at the first two separators, so a file name
// that itself contains "__" still parses.
func parseLegacyFlat(name string) (repoID string, ok bool) {
	if !strings.HasSuffix(strings.ToLower(name), ".gguf") &&
		!strings.HasSuffix(strings.ToLower(name), ".mmproj") {
		return "", false
	}
	rest := strings.TrimSuffix(name, fileExt(name))
	parts := strings.SplitN(rest, legacySep, 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", false
	}
	if !segRE.MatchString(parts[0]) || !segRE.MatchString(parts[1]) {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// validSegments reports whether every '/' -split segment is a valid
// repo segment ([A-Za-z0-9_.-], matching llama.cpp's is_valid_repo_id).
func validSegments(s, sep string) bool {
	for _, seg := range strings.Split(s, sep) {
		if !segRE.MatchString(seg) {
			return false
		}
	}
	return true
}

// fileExt returns the model-file extension (.gguf or .mmproj) of name,
// or "" when name is not a model file.
func fileExt(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".gguf"):
		return name[len(name)-5:]
	case strings.HasSuffix(lower, ".mmproj"):
		return name[len(name)-7:]
	}
	return ""
}
