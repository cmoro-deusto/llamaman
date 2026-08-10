package hf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestQuants covers the §16.3 parsing contract: real quant shapes,
// split-file summing, case-insensitive tags, no-tag fallback, mmproj
// exclusion, deterministic ordering (P9). Note the strict tag rule:
// "qwen3-UD-Q4_K_XL.gguf" extracts to Q4_K_XL (llama.cpp's
// get_gguf_split_info), which still selects the UD variant because
// find_best_model matches tag + "[.-]" as a path substring.
func TestQuants(t *testing.T) {
	files := []RepoFile{
		{Path: "qwen3-UD-Q4_K_XL.gguf", Size: 2 << 30},
		{Path: "model-Q8_0.gguf", Size: 5 << 30},
		{Path: "model-q4_k_m.gguf", Size: 1 << 30}, // lowercase tag → uppercased
		{Path: "model-F16.gguf", Size: 3 << 30},
		{Path: "big-Q4_K_M-00001-of-00002.gguf", Size: 4 << 30},
		{Path: "big-Q4_K_M-00002-of-00002.gguf", Size: 5 << 30},
		{Path: "stories260K.gguf", Size: 10 << 20}, // no tag → basename fallback
		{Path: "vision.mmproj", Size: 1 << 20},     // excluded from Quants
		{Path: "config.json", Size: 100},           // not a model file
	}
	quants := Quants(files)

	byTag := map[string]QuantOption{}
	for _, q := range quants {
		byTag[q.Tag] = q
	}

	if q := byTag["Q4_K_M"]; q.Size != 10<<30 || len(q.Files) != 3 {
		t.Errorf("Q4_K_M = %+v, want summed 10 GiB across 3 files (split parts + lowercase variant)", q)
	}
	if q := byTag["Q8_0"]; q.Size != 5<<30 {
		t.Errorf("Q8_0 size = %d", q.Size)
	}
	if q := byTag["Q4_K_XL"]; q.Size != 2<<30 {
		t.Errorf("UD file strict tag Q4_K_XL size = %d", q.Size)
	}
	if q := byTag["F16"]; q.Size != 3<<30 {
		t.Errorf("F16 size = %d", q.Size)
	}
	if q := byTag["stories260K"]; q.Size != 10<<20 {
		t.Errorf("no-tag fallback missing: %+v", q)
	}

	if len(quants) != 5 { // Q4_K_M, Q8_0, Q4_K_XL, F16, stories260K
		t.Errorf("Quants() = %d options, want 5: %v", len(quants), quantTags(quants))
	}
	// deterministic ordering: size ascending
	for i := 1; i < len(quants); i++ {
		if quants[i].Size < quants[i-1].Size {
			t.Errorf("not sorted by size: %+v", quantTags(quants))
		}
	}
}

func TestQuantsCaseInsensitiveGGUF(t *testing.T) {
	quants := Quants([]RepoFile{{Path: "model-Q8_0.GGUF", Size: 7}})
	if len(quants) != 1 || quants[0].Tag != "Q8_0" {
		t.Errorf("case-insensitive .gguf failed: %+v", quants)
	}
}

func TestHasMMProj(t *testing.T) {
	if HasMMProj([]RepoFile{{Path: "model.gguf"}}) {
		t.Error("no mmproj present")
	}
	if !HasMMProj([]RepoFile{{Path: "model.gguf"}, {Path: "vision.mmproj"}}) {
		t.Error("mmproj not detected")
	}
	if !HasMMProj([]RepoFile{{Path: "vision.MMPROJ"}}) {
		t.Error("case-insensitive mmproj not detected")
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{42, "42 B"},
		{1024, "1 KiB"},
		{1536, "1.5 KiB"},
		{5 << 20, "5 MiB"},
		{2 << 30, "2 GiB"},
		{1258291200, "1.2 GiB"},
	}
	for _, tt := range tests {
		if got := HumanSize(tt.n); got != tt.want {
			t.Errorf("HumanSize(%d) = %q, want %q", tt.n, got, tt.want)
		}
	}
}

func TestChoose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"type": "file", "path": "model-Q4_K_M.gguf", "size": 100},
			{"type": "file", "path": "model-Q8_0.gguf", "size": 200}
		]`))
	}))
	t.Cleanup(srv.Close)
	c := NewWithEndpoint(srv.URL, "")

	quants, err := Choose(context.Background(), c, "org/repo")
	if err != nil {
		t.Fatal(err)
	}
	if len(quants) != 2 || quants[0].Tag != "Q4_K_M" || quants[1].Tag != "Q8_0" {
		t.Fatalf("Choose() = %+v", quantTags(quants))
	}
	if quants[0].Size != 100 || quants[1].Size != 200 {
		t.Errorf("sizes = %d, %d", quants[0].Size, quants[1].Size)
	}
}

func quantTags(qs []QuantOption) []string {
	var out []string
	for _, q := range qs {
		out = append(out, q.Tag)
	}
	return out
}
