package flags

import "testing"

func TestExtractAddrHappyPath(t *testing.T) {
	argv := []string{
		"/opt/llama.cpp/bin/llama-server",
		"-m", "/m.gguf",
		"--alias", "qwen",
		"--host", "127.0.0.1",
		"--ctx-size", "4096",
		"--port", "9080",
	}
	host, port, ok := ExtractAddr(argv, nil)
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if host != "127.0.0.1" {
		t.Errorf("host = %q, want 127.0.0.1", host)
	}
	if port != 9080 {
		t.Errorf("port = %d, want 9080", port)
	}
}

func TestExtractAddrPresetOverrideWins(t *testing.T) {
	// Translate.Build emits the preset-rendered --host/--port at the
	// position the preset sits, not the auto-added position. Either way
	// the LAST occurrence in argv is what llama-server actually binds —
	// but Build never emits both, so a single occurrence is the realistic
	// case. This test confirms ExtractAddr finds it.
	argv := []string{
		"/x", "-m", "/m.gguf", "--alias", "a",
		"--host", "0.0.0.0",
		"--port", "9999",
	}
	host, port, ok := ExtractAddr(argv, nil)
	if !ok || host != "0.0.0.0" || port != 9999 {
		t.Errorf("got host=%q port=%d ok=%v; want 0.0.0.0/9999/true", host, port, ok)
	}
}

func TestExtractAddrMissingHost(t *testing.T) {
	argv := []string{"/x", "-m", "/m.gguf", "--port", "9080"}
	if _, _, ok := ExtractAddr(argv, nil); ok {
		t.Errorf("ok = true, want false (missing --host)")
	}
}

func TestExtractAddrMissingPort(t *testing.T) {
	argv := []string{"/x", "-m", "/m.gguf", "--host", "127.0.0.1"}
	if _, _, ok := ExtractAddr(argv, nil); ok {
		t.Errorf("ok = true, want false (missing --port)")
	}
}

func TestExtractAddrPortNotInteger(t *testing.T) {
	argv := []string{"/x", "--host", "127.0.0.1", "--port", "not-a-number"}
	if _, _, ok := ExtractAddr(argv, nil); ok {
		t.Errorf("ok = true, want false (--port value not an int)")
	}
}

func TestExtractAddrUsesRegistryForm(t *testing.T) {
	// Contrived: pretend --help renamed --host to --bind. ExtractAddr
	// must follow the registry-derived canonical form.
	reg := Registry{
		"host": {Name: "host", Form: "--bind"},
		"port": {Name: "port", Form: "--port"},
	}
	argv := []string{"/x", "--bind", "10.0.0.5", "--port", "8080"}
	host, port, ok := ExtractAddr(argv, reg)
	if !ok || host != "10.0.0.5" || port != 8080 {
		t.Errorf("got host=%q port=%d ok=%v; want 10.0.0.5/8080/true", host, port, ok)
	}
}

func TestExtractAddrEmptyArgv(t *testing.T) {
	if _, _, ok := ExtractAddr(nil, nil); ok {
		t.Errorf("ok = true, want false on nil argv")
	}
	if _, _, ok := ExtractAddr([]string{}, nil); ok {
		t.Errorf("ok = true, want false on empty argv")
	}
}

func TestExtractAddrTrailingFlagWithoutValue(t *testing.T) {
	// `--port` as last token means there's no value to read; loop bound
	// `i < len(argv)-1` guards this. Result: not ok.
	argv := []string{"/x", "--host", "127.0.0.1", "--port"}
	if _, _, ok := ExtractAddr(argv, nil); ok {
		t.Errorf("ok = true, want false on dangling --port")
	}
}
