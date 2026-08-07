// Package paths resolves XDG directories and expands shell-style paths.
package paths

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

const appName = "llamaman"

// ConfigPath returns the default config file location:
// ${XDG_CONFIG_HOME:-$HOME/.config}/llamaman/config.json.
func ConfigPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// ConfigDir returns the llamaman config directory.
func ConfigDir() (string, error) {
	return configDir()
}

func configDir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, appName), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", appName), nil
}

// RuntimeDir returns ${XDG_RUNTIME_DIR:-/tmp/llamaman-$UID}/llamaman.
// The leaf directory is always llamaman/ for symmetry with the other XDG dirs.
func RuntimeDir() (string, error) {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" {
		return filepath.Join(v, appName), nil
	}
	uid := os.Getuid()
	return filepath.Join("/tmp", fmt.Sprintf("%s-%s", appName, strconv.Itoa(uid)), appName), nil
}

// CacheDir returns ${XDG_CACHE_HOME:-$HOME/.cache}/llamaman.
func CacheDir() (string, error) {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, appName), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", appName), nil
}

// StateDir returns ${XDG_STATE_HOME:-$HOME/.local/state}/llamaman.
func StateDir() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, appName), nil
	}
	home, err := homeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", appName), nil
}

// ExpandPath expands a leading ~ to the user's home directory and resolves
// $VAR / ${VAR} environment references. Unset variables are left literal so
// users notice the typo rather than silently getting an empty path.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", nil
	}
	if strings.HasPrefix(p, "~") {
		home, err := homeDir()
		if err != nil {
			return "", err
		}
		switch {
		case p == "~":
			p = home
		case strings.HasPrefix(p, "~/"):
			p = filepath.Join(home, p[2:])
		}
	}
	return expandEnvLiteral(p), nil
}

// expandEnvLiteral mirrors os.ExpandEnv but leaves unset variables intact
// (e.g. "$NOPE/x" stays "$NOPE/x" rather than becoming "/x").
func expandEnvLiteral(s string) string {
	return os.Expand(s, func(name string) string {
		if v, ok := os.LookupEnv(name); ok {
			return v
		}
		return "${" + name + "}"
	})
}

func homeDir() (string, error) {
	if h := os.Getenv("HOME"); h != "" {
		return h, nil
	}
	u, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return u.HomeDir, nil
}

// HomeDir returns $HOME, falling back to the OS user's home directory
// (mirrors llama.cpp's HOME / getpwuid resolution). Exported for the
// storage package's llama.cpp cache-root chain.
func HomeDir() (string, error) { return homeDir() }
