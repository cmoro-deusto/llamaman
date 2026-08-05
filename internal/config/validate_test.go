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
