package flags

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadFallbackWhenBinMissing(t *testing.T) {
	l := &Loader{BinPath: "/nonexistent/llama-server", CacheDir: t.TempDir()}
	reg, real := l.Load()
	if real {
		t.Fatal("expected fallback (real=false) when binary is missing")
	}
	if _, ok := reg["m"]; !ok {
		t.Fatalf("fallback registry missing -m entry: %v", reg)
	}
	if reg["m"].Form != "-m" {
		t.Errorf("Form = %q", reg["m"].Form)
	}
}

func TestLoadFallbackWhenBinPathEmpty(t *testing.T) {
	l := &Loader{BinPath: "", CacheDir: t.TempDir()}
	if _, real := l.Load(); real {
		t.Fatal("empty BinPath should yield fallback")
	}
}

// Use the fakeserver as a stand-in for a real llama-server binary. It
// doesn't recognize --help and runs forever, so the help invocation must
// timeout — we expect a graceful fallback to the hard-coded set.
func TestLoadHandlesNonResponsiveBinary(t *testing.T) {
	bin := filepath.Join(repoRoot(t), "bin", "llamaman-fakeserver")
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("fakeserver not built: %v", err)
	}
	l := &Loader{BinPath: bin, CacheDir: t.TempDir(), HelpTimeout: 100 * time.Millisecond}
	reg, _ := l.Load()
	// Always return a usable registry.
	if len(reg) == 0 {
		t.Fatal("Load returned empty registry")
	}
}

func TestCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := Registry{
		"threads": {Name: "threads", Form: "--threads", IsBool: false, Placeholder: "N", Kind: KindNumeric},
		"jinja":   {Name: "jinja", Form: "--jinja", IsBool: true, Kind: KindBool},
	}
	path := filepath.Join(dir, "flags-1.json")
	if err := writeCache(path, in); err != nil {
		t.Fatal(err)
	}
	out, ok := readCache(path)
	if !ok {
		t.Fatal("readCache failed")
	}
	for _, k := range []string{"threads", "jinja"} {
		if out[k].Name != in[k].Name || out[k].Form != in[k].Form ||
			out[k].IsBool != in[k].IsBool || out[k].Kind != in[k].Kind {
			t.Errorf("round-trip mismatch for %q: got %+v, want %+v", k, out[k], in[k])
		}
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod found above %s", dir)
		}
		dir = parent
	}
}
