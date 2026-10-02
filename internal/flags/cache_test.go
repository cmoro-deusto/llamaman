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

func TestCachePathDistinguishesSameMtimeDifferentPaths(t *testing.T) {
	l1 := &Loader{BinPath: "/a/llama-server", CacheDir: t.TempDir()}
	l2 := &Loader{BinPath: "/b/llama-server", CacheDir: l1.CacheDir}
	mt := time.Unix(1700000000, 0)
	if l1.cachePath(mt) == l2.cachePath(mt) {
		t.Fatal("two different binaries with the same mtime must not share a cache file")
	}
	// Same path twice → same name (cache hit).
	l3 := &Loader{BinPath: "/a/llama-server", CacheDir: l1.CacheDir}
	if l1.cachePath(mt) != l3.cachePath(mt) {
		t.Fatal("same path + mtime must map to the same cache file")
	}
}

// The pre-§3.2a cache names carried only the mtime, so the current code can
// never read them again. Writing a new entry must sweep those leftovers
// while leaving other binaries' (hashed) entries and foreign files alone.
func TestWriteCachePrunesLegacyNames(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "flags-1700000000000000000.json")
	current := filepath.Join(dir, "flags-0123456789abcdef-1700000000000000000.json")
	hashedSibling := filepath.Join(dir, "flags-ffffffffffffffff-1700000000000000001.json")
	foreign := filepath.Join(dir, "keep-me.json")
	allDigitsHash := filepath.Join(dir, "flags-0123456789abcdef-1700000000000000002.json")
	for _, p := range []string{legacy, hashedSibling, foreign, allDigitsHash} {
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if err := writeCache(current, Registry{"threads": {Name: "threads", Form: "--threads"}}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Errorf("legacy (mtime-only) cache file survived: %v", err)
	}
	for _, p := range []string{current, hashedSibling, foreign, allDigitsHash} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("prune removed %s: %v", filepath.Base(p), err)
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
