package storage

import "testing"

// TestRepoFolderNames verifies the hub and legacy folder-name forms
// (llama.cpp repo_to_folder_name: "models--" + / → --; legacy: / → __).
func TestRepoFolderNames(t *testing.T) {
	got := RepoFolderNames("Qwen/Qwen3-32B-GGUF")
	want := []string{"models--Qwen--Qwen3-32B-GGUF", "Qwen__Qwen3-32B-GGUF"}
	if len(got) != len(want) {
		t.Fatalf("RepoFolderNames() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("RepoFolderNames()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDetectLayout covers DESIGN §16.1 detection rules 1–5 with
// table-driven cases (P9).
func TestDetectLayout(t *testing.T) {
	tests := []struct {
		name  string
		isDir bool
		want  Layout
	}{
		// rule 1: hub repo folders
		{"models--Qwen--Qwen3-32B-GGUF", true, LayoutHFHub},
		// invalid converted repo id (no slash / two slashes) → unknown
		{"models--norepo", true, LayoutUnknown},
		{"models--a--b--c", true, LayoutUnknown},
		// a models-- name that is a file → unknown
		{"models--Qwen--Qwen3-32B-GGUF", false, LayoutUnknown},
		// rule 2: legacy folder <org>__<model>
		{"Qwen__Qwen3-32B-GGUF", true, LayoutLegacyFolder},
		{"org__model", true, LayoutLegacyFolder},
		// three segments → not a legacy folder (strict split)
		{"a__b__c", true, LayoutUnknown},
		// rule 3: legacy flat model files
		{"org__repo__file-Q4_K_M.gguf", false, LayoutLegacyFlat},
		{"org__repo__file.mmproj", false, LayoutLegacyFlat},
		{"org__repo__x__y-Q8_0.gguf", false, LayoutLegacyFlat}, // file part with __
		{"org__repo.gguf", false, LayoutUnknown},               // missing file segment
		// rule 4: legacy metadata, skipped silently
		{"org__repo__file-Q4_K_M.gguf.etag", false, LayoutMeta},
		{"manifest=org=repo=latest.json", false, LayoutMeta},
		// rule 5: everything else → unknown
		{"notes.txt", false, LayoutUnknown},
		{".DS_Store", false, LayoutUnknown},
		{"some-dir", true, LayoutUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectLayout(tt.name, tt.isDir); got != tt.want {
				t.Errorf("DetectLayout(%q, dir=%v) = %v, want %v", tt.name, tt.isDir, got, tt.want)
			}
		})
	}
}
