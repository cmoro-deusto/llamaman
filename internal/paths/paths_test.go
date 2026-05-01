package paths

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/h/alice")
	t.Setenv("FOO", "bar")

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "/etc/hosts", "/etc/hosts"},
		{"tilde alone", "~", "/h/alice"},
		{"tilde slash", "~/models", "/h/alice/models"},
		{"tilde no slash stays literal", "~alice", "~alice"},
		{"dollar var", "$FOO/x", "bar/x"},
		{"braced var", "${FOO}/x", "bar/x"},
		{"unset var stays literal", "${NOPE_LLAMAMAN}/x", "${NOPE_LLAMAMAN}/x"},
		{"tilde plus var", "~/$FOO", "/h/alice/bar"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandPath(tc.in)
			if err != nil {
				t.Fatalf("ExpandPath(%q) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestConfigDirRespectsXDG(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(tmp, "llamaman")
	if got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/h/alice")
	got, err := ConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want := "/h/alice/.config/llamaman"
	if got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
}

func TestConfigPath(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	got, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	want := "/cfg/llamaman/config.json"
	if got != want {
		t.Fatalf("ConfigPath() = %q, want %q", got, want)
	}
}

func TestRuntimeDirXDG(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	got, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	want := "/run/user/1000/llamaman"
	if got != want {
		t.Fatalf("RuntimeDir() = %q, want %q", got, want)
	}
}

func TestRuntimeDirFallback(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	got, err := RuntimeDir()
	if err != nil {
		t.Fatal(err)
	}
	// /tmp/llamaman-<UID>/llamaman — UID varies by environment, so just
	// check the structure rather than the literal value.
	if !strings.HasPrefix(got, "/tmp/llamaman-") || !strings.HasSuffix(got, "/llamaman") {
		t.Fatalf("RuntimeDir() = %q, want /tmp/llamaman-<uid>/llamaman", got)
	}
	// Sanity: the middle segment should be a numeric UID.
	mid := strings.TrimSuffix(strings.TrimPrefix(got, "/tmp/llamaman-"), "/llamaman")
	if _, err := strconv.Atoi(mid); err != nil {
		t.Fatalf("RuntimeDir() uid segment %q is not numeric", mid)
	}
}

func TestCacheDirXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/c")
	got, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/c/llamaman" {
		t.Fatalf("CacheDir() = %q", got)
	}
}

func TestCacheDirFallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("HOME", "/h/alice")
	got, err := CacheDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/h/alice/.cache/llamaman" {
		t.Fatalf("CacheDir() = %q", got)
	}
}

func TestStateDirXDG(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/s")
	got, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/s/llamaman" {
		t.Fatalf("StateDir() = %q", got)
	}
}

func TestStateDirFallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/h/alice")
	got, err := StateDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/h/alice/.local/state/llamaman" {
		t.Fatalf("StateDir() = %q", got)
	}
}
