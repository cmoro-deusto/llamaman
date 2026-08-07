package storage

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CachedFile is one model file found in the cache (DESIGN §16.1).
// RepoID is the org/repo form (no quant — the quant lives in the file
// name). Path is the absolute file path; for the hub layout it is the
// snapshots/<commit>/<file> path (llama.cpp's final_path), which may be
// a symlink to a blob — Size resolves through the link.
type CachedFile struct {
	RepoID string
	Path   string
	Size   int64
	Layout Layout
}

// Scan lists every recognized model file under root (DESIGN §16.1).
// A missing or empty root yields an empty result and no error: "not
// cached" is a normal answer. Unrecognized entries are reported through
// warn (once per entry); recognized legacy metadata is skipped silently.
// Results are sorted by Path for determinism (P9). Scan never mutates
// anything (P8).
func Scan(root string, warn func(string)) ([]CachedFile, error) {
	if root == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan cache %s: %w", root, err)
	}
	var out []CachedFile
	for _, e := range entries {
		switch DetectLayout(e.Name(), e.IsDir()) {
		case LayoutHFHub:
			repoID := hubRepoID(e.Name())
			out = append(out, listHubRepo(filepath.Join(root, e.Name()), repoID)...)
		case LayoutLegacyFolder:
			org, model, _ := splitLegacyFolder(e.Name())
			out = append(out, listLegacyFolder(filepath.Join(root, e.Name()), org+"/"+model)...)
		case LayoutLegacyFlat:
			repoID, _ := parseLegacyFlat(e.Name())
			p := filepath.Join(root, e.Name())
			out = append(out, CachedFile{RepoID: repoID, Path: p, Size: sizeOf(p), Layout: LayoutLegacyFlat})
		case LayoutMeta:
			// recognized legacy metadata; skip silently
		default:
			if warn != nil {
				warn(e.Name())
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Lookup finds cached files for hfID (org/repo[:quant]). The quant
// suffix is stripped — it lives in the file name, not the repo folder
// (DESIGN §16.1). A miss or a missing root returns an empty slice and
// no error. Results are sorted by Path.
func Lookup(root, hfID string) ([]CachedFile, error) {
	if root == "" {
		return nil, nil
	}
	repoID := strings.SplitN(hfID, ":", 2)[0]
	if _, err := os.Stat(root); err != nil {
		return nil, nil // missing root == "not cached"
	}
	var out []CachedFile

	hubDir := filepath.Join(root, RepoFolderNames(repoID)[0])
	if isDir(hubDir) {
		out = append(out, listHubRepo(hubDir, repoID)...)
	}
	legDir := filepath.Join(root, RepoFolderNames(repoID)[1])
	if isDir(legDir) {
		out = append(out, listLegacyFolder(legDir, repoID)...)
	}
	// legacy flat files org__repo__<file>
	prefix := strings.ReplaceAll(repoID, "/", legacySep) + legacySep
	if entries, err := os.ReadDir(root); err == nil {
		for _, e := range entries {
			if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) {
				continue
			}
			if fileExt(e.Name()) == "" {
				continue
			}
			p := filepath.Join(root, e.Name())
			out = append(out, CachedFile{RepoID: repoID, Path: p, Size: sizeOf(p), Layout: LayoutLegacyFlat})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// hubRepoID converts a models--… folder name back to org/repo.
func hubRepoID(name string) string {
	return strings.ReplaceAll(strings.TrimPrefix(name, hubPrefix), hubSep, "/")
}

// listHubRepo enumerates the model files of one HF hub repo folder:
// commit from refs/main (llama.cpp prefers main, then any ref); if no
// ref resolves, the first snapshots/* directory that has model files.
// A repo without a snapshots/ directory is an empty cache state — zero
// files, no warning (DESIGN §16.1 detection rule 1).
func listHubRepo(repoDir, repoID string) []CachedFile {
	snapshots := filepath.Join(repoDir, "snapshots")
	dirs, err := os.ReadDir(snapshots)
	if err != nil {
		return nil // missing or unreadable snapshots == nothing cached
	}
	if commit, ok := readRef(repoDir); ok {
		if d := filepath.Join(snapshots, commit); isDir(d) {
			return listSnapshotDir(d, repoID)
		}
	}
	var names []string
	for _, e := range dirs {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		if files := listSnapshotDir(filepath.Join(snapshots, n), repoID); len(files) > 0 {
			return files
		}
	}
	return nil
}

// readRef returns the commit from <repoDir>/refs/main (a 40-hex hash),
// or false when absent or malformed.
func readRef(repoDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(repoDir, "refs", "main"))
	if err != nil {
		return "", false
	}
	c := strings.TrimSpace(string(data))
	if len(c) != 40 {
		return "", false
	}
	for _, r := range c {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return "", false
		}
	}
	return c, true
}

// listSnapshotDir collects .gguf/.mmproj files under a snapshot
// directory (recursively — repos may nest files in subfolders).
func listSnapshotDir(dir, repoID string) []CachedFile {
	var out []CachedFile
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fileExt(d.Name()) == "" {
			return nil
		}
		out = append(out, CachedFile{RepoID: repoID, Path: p, Size: sizeOf(p), Layout: LayoutHFHub})
		return nil
	})
	return out
}

// listLegacyFolder collects .gguf/.mmproj files directly inside a
// <org>__<model>/ folder.
func listLegacyFolder(dir, repoID string) []CachedFile {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []CachedFile
	for _, e := range entries {
		if e.IsDir() || fileExt(e.Name()) == "" {
			continue
		}
		p := filepath.Join(dir, e.Name())
		out = append(out, CachedFile{RepoID: repoID, Path: p, Size: sizeOf(p), Layout: LayoutLegacyFolder})
	}
	return out
}

// sizeOf returns the on-disk size, resolving symlinks (hub snapshot
// entries link to blobs). Zero on any error.
func sizeOf(p string) int64 {
	if info, err := os.Stat(p); err == nil {
		return info.Size()
	}
	return 0
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
