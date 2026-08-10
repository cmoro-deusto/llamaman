package storage

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// commit is a valid 40-hex refs/main value.
const commit = "68c3ea2061e8c7688455fab07597dde0f4d7f0db"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hubRepo builds a hub-layout repo folder with refs/main, one snapshot
// file, and a blob; the snapshot entry links to the blob (llama.cpp
// finalize_file behavior).
func hubRepo(t *testing.T, root, repoID, fileName string) (repoDir, snapPath, blobPath string) {
	t.Helper()
	repoDir = filepath.Join(root, RepoFolderNames(repoID)[0])
	writeFile(t, filepath.Join(repoDir, "refs", "main"), commit)
	blobPath = filepath.Join(repoDir, "blobs", "50d019817c2626eb9e8a41f361ff5bfa538757e6f708a3076cd3356354a75694")
	writeFile(t, blobPath, "blob-content")
	snapDir := filepath.Join(repoDir, "snapshots", commit)
	if err := os.MkdirAll(snapDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snapPath = filepath.Join(snapDir, fileName)
	if err := os.Symlink(filepath.Join("..", "..", "blobs", filepath.Base(blobPath)), snapPath); err != nil {
		t.Fatal(err)
	}
	return repoDir, snapPath, blobPath
}

func TestScanHubLayout(t *testing.T) {
	root := t.TempDir()
	_, snapPath, blobPath := hubRepo(t, root, "Qwen/Qwen3-32B-GGUF", "qwen3-Q4_K_M.gguf")

	var warned []string
	files, err := Scan(root, func(name string) { warned = append(warned, name) })
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("Scan() = %d files, want 1: %+v", len(files), files)
	}
	f := files[0]
	if f.RepoID != "Qwen/Qwen3-32B-GGUF" {
		t.Errorf("RepoID = %q", f.RepoID)
	}
	if f.Path != snapPath {
		t.Errorf("Path = %q, want snapshots path %q", f.Path, snapPath)
	}
	if f.Layout != LayoutHFHub {
		t.Errorf("Layout = %v, want HFHub", f.Layout)
	}
	if want := int64(len("blob-content")); f.Size != want {
		t.Errorf("Size = %d, want %d (symlink resolved)", f.Size, want)
	}
	if f.Path == blobPath {
		t.Error("Path must be the snapshots entry, not the blob")
	}
	if len(warned) != 0 {
		t.Errorf("no warnings expected, got %v", warned)
	}
}

func TestScanHubEmptyRepoNoWarning(t *testing.T) {
	root := t.TempDir()
	// models-- repo dir without snapshots == empty cache state
	repoDir := filepath.Join(root, RepoFolderNames("org/repo")[0])
	writeFile(t, filepath.Join(repoDir, "refs", "main"), commit)

	var warned []string
	files, err := Scan(root, func(name string) { warned = append(warned, name) })
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("expected no files, got %+v", files)
	}
	if len(warned) != 0 {
		t.Errorf("empty hub repo must not warn, got %v", warned)
	}
}

func TestScanHubRefFallback(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, RepoFolderNames("org/repo")[0])
	// no refs/main — fall back to any snapshots/* dir
	writeFile(t, filepath.Join(repoDir, "snapshots", commit, "model.gguf"), "x")

	files, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0].Path, "model.gguf") {
		t.Fatalf("ref fallback failed: %+v", files)
	}
}

func TestScanLegacyLayouts(t *testing.T) {
	root := t.TempDir()
	// legacy folder <org>__<model>/<file>
	writeFile(t, filepath.Join(root, "Qwen__Qwen3-32B-GGUF", "qwen3-Q4_K_M.gguf"), "a")
	// legacy flat + metadata
	writeFile(t, filepath.Join(root, "org__repo__model-Q4_K_M.gguf"), "b")
	writeFile(t, filepath.Join(root, "org__repo__model-Q4_K_M.gguf.etag"), "etag")
	writeFile(t, filepath.Join(root, "manifest=org=repo=latest.json"), "{}")
	// junk that must warn
	writeFile(t, filepath.Join(root, "notes.txt"), "n")
	if err := os.Mkdir(filepath.Join(root, "junkdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	var warned []string
	files, err := Scan(root, func(name string) { warned = append(warned, name) })
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("Scan() = %d files, want 2: %+v", len(files), files)
	}
	// sorted by Path
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	got := []string{filepath.Base(files[0].Path), filepath.Base(files[1].Path)}
	// "Qwen__…" (uppercase) sorts before "org__…" (lowercase)
	want := []string{"qwen3-Q4_K_M.gguf", "org__repo__model-Q4_K_M.gguf"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Errorf("files = %v, want %v", got, want)
	}
	if files[0].Layout != LayoutLegacyFolder || files[1].Layout != LayoutLegacyFlat {
		t.Errorf("layouts = %v, %v", files[0].Layout, files[1].Layout)
	}
	wantedWarns := []string{"junkdir", "notes.txt"}
	sort.Strings(warned)
	if len(warned) != 2 || warned[0] != wantedWarns[0] || warned[1] != wantedWarns[1] {
		t.Errorf("warnings = %v, want %v", warned, wantedWarns)
	}
}

func TestScanMissingRoot(t *testing.T) {
	files, err := Scan(filepath.Join(t.TempDir(), "nope"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 0 {
		t.Errorf("missing root must yield no files, got %+v", files)
	}
}

func TestScanNestedSnapshotFiles(t *testing.T) {
	root := t.TempDir()
	repoDir := filepath.Join(root, RepoFolderNames("org/repo")[0])
	writeFile(t, filepath.Join(repoDir, "refs", "main"), commit)
	// nested file under snapshots/<commit>/sub/
	writeFile(t, filepath.Join(repoDir, "snapshots", commit, "sub", "model.gguf"), "x")
	// non-model file ignored
	writeFile(t, filepath.Join(repoDir, "snapshots", commit, "tokenizer.json"), "{}")

	files, err := Scan(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || !strings.HasSuffix(files[0].Path, "sub"+string(filepath.Separator)+"model.gguf") {
		t.Fatalf("nested snapshot scan failed: %+v", files)
	}
}

func TestLookup(t *testing.T) {
	root := t.TempDir()
	// hub repo
	hubRepo(t, root, "Qwen/Qwen3-32B-GGUF", "qwen3-Q4_K_M.gguf")
	// legacy folder for a second repo
	writeFile(t, filepath.Join(root, "org__legacymodel", "legacy-Q8_0.gguf"), "c")
	// legacy flat for the same repo as hub
	writeFile(t, filepath.Join(root, "Qwen__Qwen3-32B-GGUF__old-Q4_K_M.gguf"), "d")

	// quant suffix is stripped
	files, err := Lookup(root, "Qwen/Qwen3-32B-GGUF:Q4_K_M")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("Lookup() = %d files, want 2 (hub + legacy flat): %+v", len(files), files)
	}
	for _, f := range files {
		if f.RepoID != "Qwen/Qwen3-32B-GGUF" {
			t.Errorf("RepoID = %q", f.RepoID)
		}
	}

	// legacy folder repo
	files, err = Lookup(root, "org/legacymodel")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Layout != LayoutLegacyFolder {
		t.Fatalf("legacy folder lookup failed: %+v", files)
	}

	// unknown repo → empty, no error
	files, err = Lookup(root, "nope/nothing")
	if err != nil || len(files) != 0 {
		t.Errorf("unknown repo: files=%+v err=%v, want empty+nil", files, err)
	}
}

func TestLookupMissingRoot(t *testing.T) {
	files, err := Lookup(filepath.Join(t.TempDir(), "nope"), "org/repo")
	if err != nil || len(files) != 0 {
		t.Errorf("missing root: files=%+v err=%v, want empty+nil", files, err)
	}
}

// TestScanHubRootMetadataNoWarnings pins that the standard HF hub root
// metadata files (.locks/, CACHEDIR.TAG, version.txt) are recognized
// and skipped silently — they are part of the known hub layout, not
// unrecognized entries (owner report).
func TestScanHubRootMetadataNoWarnings(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "CACHEDIR.TAG"), "Signature: 8a477f597d28d172789f06886806bc55")
	writeFile(t, filepath.Join(root, "version.txt"), "3.0.0")
	if err := os.Mkdir(filepath.Join(root, ".locks"), 0o755); err != nil {
		t.Fatal(err)
	}
	// plus a real cached repo so Scan has something to list
	hubRepo(t, root, "org/repo", "model-Q4_K_M.gguf")

	var warned []string
	files, err := Scan(root, func(name string) { warned = append(warned, name) })
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("Scan() = %d files, want the repo's model only: %+v", len(files), files)
	}
	if len(warned) != 0 {
		t.Errorf("hub root metadata must not warn, got %v", warned)
	}
}
