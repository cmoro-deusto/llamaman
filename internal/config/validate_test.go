package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateModelsFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "models.ini")
	if err := os.WriteFile(existing, []byte("[m]\nmodel = m.gguf\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &Config{
		Version: SchemaVersion,
		Globals: Globals{Bin: "/usr/local/bin/llama-server", Host: "127.0.0.1", Port: 9080,
			ModelsFiles: []string{existing, filepath.Join(dir, "missing.ini"), ""}},
	}
	issues := Validate(cfg)
	var msgs []string
	for _, it := range issues {
		msgs = append(msgs, it.Path+":"+it.Message)
	}
	joined := strings.Join(msgs, "\n")
	if !strings.Contains(joined, "globals.models-files[1]") || !strings.Contains(joined, "does not exist") {
		t.Errorf("missing-file warning not emitted: %v", msgs)
	}
	if !strings.Contains(joined, "globals.models-files[2]") || !strings.Contains(joined, "path is required") {
		t.Errorf("empty-path error not emitted: %v", msgs)
	}
	// Existing file must not produce an issue.
	for _, m := range msgs {
		if strings.Contains(m, "models-files[0]") {
			t.Errorf("unexpected issue for existing file: %v", msgs)
		}
	}
}

func TestConfigRoundTripWithModelsFiles(t *testing.T) {
	cfg := &Config{
		Version: SchemaVersion,
		Globals: Globals{Bin: "/usr/local/bin/llama-server", Host: "127.0.0.1", Port: 9080,
			ModelsFiles: []string{"/etc/llamaman/models.ini"}},
	}
	data, err := MarshalForDiff(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := writeFile(t, "rt.json", string(data))
	round, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(round.Globals.ModelsFiles) != 1 || round.Globals.ModelsFiles[0] != "/etc/llamaman/models.ini" {
		t.Errorf("ModelsFiles round-trip = %v", round.Globals.ModelsFiles)
	}
}

// TestValidateModelsDirNotADirectory verifies the single models-dir
// rule (DESIGN §16.1): when set and the path exists but is not a
// directory → Warning, never a Block.
func TestValidateModelsDirNotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o755); err != nil {
		t.Fatal(err)
	}

	base := func() *Config {
		return &Config{
			Version: SchemaVersion,
			Globals: Globals{Bin: "/usr/local/bin/llama-server", Host: "127.0.0.1", Port: 9080},
		}
	}

	// file as models-dir → warning
	cfg := base()
	cfg.Preferences = &Preferences{ModelsDir: file}
	issues := Validate(cfg)
	if issues.HasErrors() {
		t.Errorf("a file as models-dir must warn, not block: %+v", issues)
	}
	if !hasIssue(issues, "preferences.models-dir", "not a directory") {
		t.Errorf("expected models-dir warning, got %+v", issues)
	}

	// real dir → no models-dir issue
	cfg = base()
	cfg.Preferences = &Preferences{ModelsDir: realDir}
	if issues := Validate(cfg); hasIssue(issues, "preferences.models-dir", "") {
		t.Errorf("a real dir must be clean, got %+v", issues)
	}

	// missing dir → no models-dir issue (created on first download)
	cfg = base()
	cfg.Preferences = &Preferences{ModelsDir: filepath.Join(dir, "missing")}
	if issues := Validate(cfg); hasIssue(issues, "preferences.models-dir", "") {
		t.Errorf("a missing dir must be clean, got %+v", issues)
	}

	// absent → no models-dir issue
	if issues := Validate(base()); hasIssue(issues, "preferences.models-dir", "") {
		t.Errorf("absent models-dir must be clean, got %+v", issues)
	}
}

func hasIssue(issues Issues, path, fragment string) bool {
	for _, it := range issues {
		if it.Path == path && strings.Contains(it.Message, fragment) {
			return true
		}
	}
	return false
}
