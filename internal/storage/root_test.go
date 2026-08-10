package storage

import (
	"path/filepath"
	"testing"
)

// TestCacheRootModelsDirWins verifies preferences.models-dir outranks
// every environment variable (DESIGN §16.1 priority 1).
func TestCacheRootModelsDirWins(t *testing.T) {
	t.Setenv("LLAMA_CACHE", "/env/llama-cache")
	t.Setenv("HF_HOME", "/env/hf-home")
	got, err := CacheRoot("/opt/my-models")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/opt/my-models" {
		t.Errorf("models-dir should win, got %q", got)
	}
}

// TestCacheRootEnvChain verifies the llama.cpp chain, first non-empty
// wins (DESIGN §16.1 priorities 2–7).
func TestCacheRootEnvChain(t *testing.T) {
	tests := []struct {
		name  string
		env   map[string]string
		want  string
	}{
		{name: "LLAMA_CACHE alone", env: map[string]string{"LLAMA_CACHE": "/c"}, want: "/c"},
		{name: "HF_HUB_CACHE alone", env: map[string]string{"HF_HUB_CACHE": "/c"}, want: "/c"},
		{name: "HUGGINGFACE_HUB_CACHE alone", env: map[string]string{"HUGGINGFACE_HUB_CACHE": "/c"}, want: "/c"},
		{name: "HF_HOME appends hub", env: map[string]string{"HF_HOME": "/h"}, want: filepath.Join("/h", "hub")},
		{name: "XDG_CACHE_HOME appends huggingface/hub", env: map[string]string{"XDG_CACHE_HOME": "/x"}, want: filepath.Join("/x", "huggingface", "hub")},
		{name: "HOME appends .cache/huggingface/hub", env: map[string]string{"HOME": "/u"}, want: filepath.Join("/u", ".cache", "huggingface", "hub")},
		{name: "LLAMA_CACHE beats HF_HOME", env: map[string]string{"LLAMA_CACHE": "/a", "HF_HOME": "/b"}, want: "/a"},
		{name: "HF_HOME beats XDG_CACHE_HOME", env: map[string]string{"HF_HOME": "/a", "XDG_CACHE_HOME": "/b"}, want: filepath.Join("/a", "hub")},
		{name: "XDG_CACHE_HOME beats HOME", env: map[string]string{"XDG_CACHE_HOME": "/a", "HOME": "/b"}, want: filepath.Join("/a", "huggingface", "hub")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k := range map[string]string{
				"LLAMA_CACHE": "", "HF_HUB_CACHE": "", "HUGGINGFACE_HUB_CACHE": "",
				"HF_HOME": "", "XDG_CACHE_HOME": "", "HOME": "",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			got, err := CacheRoot("")
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("CacheRoot() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestCacheRootNoEnvFallsBackToHome verifies the final fallback is
// $HOME/.cache/huggingface/hub when no env var is set.
func TestCacheRootNoEnvFallsBackToHome(t *testing.T) {
	for k := range map[string]string{
		"LLAMA_CACHE": "", "HF_HUB_CACHE": "", "HUGGINGFACE_HUB_CACHE": "",
		"HF_HOME": "", "XDG_CACHE_HOME": "",
	} {
		t.Setenv(k, "")
	}
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("HOME", home)
	got, err := CacheRoot("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".cache", "huggingface", "hub")
	if got != want {
		t.Errorf("CacheRoot() = %q, want %q", got, want)
	}
}

// TestCacheRootRelativePassthrough verifies a relative models-dir is
// passed through verbatim (expansion is a load-time job, §16.1).
func TestCacheRootRelativePassthrough(t *testing.T) {
	got, err := CacheRoot("models")
	if err != nil {
		t.Fatal(err)
	}
	if got != "models" {
		t.Errorf("CacheRoot(relative) = %q, want literal passthrough", got)
	}
}
