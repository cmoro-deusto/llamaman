package hf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/storage"
)

const testCommit = "68c3ea2061e8c7688455fab07597dde0f4d7f0db"

// fakeHFS is a minimal Hugging Face file server: refs, tree, and
// Range-capable resolve. File content doubles as the oid source.
type fakeHFS struct {
	t            *testing.T
	files        map[string][]byte
	rangeHeaders []string
	resolveCalls int
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func newFakeHFS(t *testing.T, files map[string][]byte) (*fakeHFS, *httptest.Server) {
	t.Helper()
	f := &fakeHFS{t: t, files: files}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/refs"):
			json.NewEncoder(w).Encode(map[string]any{
				"branches": []map[string]string{{"name": "main", "targetCommit": testCommit}},
			})
		case strings.Contains(p, "/tree/"):
			entries := []map[string]any{}
			for path, data := range f.files {
				entries = append(entries, map[string]any{
					"type": "file", "path": path, "size": len(data),
					"lfs": map[string]any{"oid": sha256Hex(data), "size": len(data)},
				})
			}
			json.NewEncoder(w).Encode(entries)
		case strings.Contains(p, "/resolve/"):
			f.resolveCalls++
			file := p[strings.Index(p, "/resolve/")+len("/resolve/"):]
			file = file[strings.Index(file, "/")+1:] // strip the commit
			data, ok := f.files[file]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			if rng := r.Header.Get("Range"); rng != "" {
				f.rangeHeaders = append(f.rangeHeaders, rng)
				from, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), 10, 64)
				if err != nil {
					http.Error(w, "bad range", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(data)-1, len(data)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(data[from:])
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write(data)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func mustOID(t *testing.T, content string) string {
	t.Helper()
	return sha256Hex([]byte(content))
}

// TestDownloadFullLayout verifies a full download writes llama.cpp's
// exact hub layout and that storage.Scan reads it back (integration of
// §16.1 + §16.4).
func TestDownloadFullLayout(t *testing.T) {
	content := "0123456789"
	_, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": []byte(content)})
	c := NewWithEndpoint(srv.URL, "")
	root := t.TempDir()

	var progress []int64
	err := c.Download(context.Background(), root, "org/repo", "Q4_K_M",
		func(done, total int64) { progress = append(progress, done) })
	if err != nil {
		t.Fatal(err)
	}

	repoDir := filepath.Join(root, "models--org--repo")
	ref, err := os.ReadFile(filepath.Join(repoDir, "refs", "main"))
	if err != nil || strings.TrimSpace(string(ref)) != testCommit {
		t.Fatalf("refs/main = %q, err %v", ref, err)
	}
	blobPath := filepath.Join(repoDir, "blobs", mustOID(t, content))
	if b, err := os.ReadFile(blobPath); err != nil || string(b) != content {
		t.Fatalf("blob = %q, err %v", b, err)
	}
	snapPath := filepath.Join(repoDir, "snapshots", testCommit, "model-Q4_K_M.gguf")
	info, err := os.Lstat(snapPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("snapshot entry must be a symlink, got %v", info.Mode())
	}
	if got, err := os.ReadFile(snapPath); err != nil || string(got) != content {
		t.Fatalf("snapshot read = %q, err %v", got, err)
	}

	// the storage reader sees the downloaded model (layout integration)
	files, err := storage.Scan(root, nil)
	if err != nil || len(files) != 1 {
		t.Fatalf("storage.Scan after download = %+v, err %v", files, err)
	}
	if files[0].RepoID != "org/repo" || files[0].Path != snapPath || files[0].Size != int64(len(content)) {
		t.Errorf("scan entry = %+v", files[0])
	}

	if len(progress) == 0 || progress[len(progress)-1] != int64(len(content)) {
		t.Errorf("progress = %v, want final == %d", progress, len(content))
	}
}

// TestDownloadResume verifies a partial <oid>.incomplete resumes via
// Range and the bar reflects only the remaining bytes.
func TestDownloadResume(t *testing.T) {
	content := "0123456789"
	f, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": []byte(content)})
	c := NewWithEndpoint(srv.URL, "")
	root := t.TempDir()

	// pre-seed a partial with the first 5 bytes
	repoDir := filepath.Join(root, "models--org--repo")
	oid := mustOID(t, content)
	if err := os.MkdirAll(filepath.Join(repoDir, "blobs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "blobs", oid+".incomplete"), []byte(content[:5]), 0o644); err != nil {
		t.Fatal(err)
	}

	var lastDone, lastTotal int64
	err := c.Download(context.Background(), root, "org/repo", "Q4_K_M",
		func(done, total int64) { lastDone, lastTotal = done, total })
	if err != nil {
		t.Fatal(err)
	}
	// absolute progress: the pre-seeded 5 bytes count as done and the
	// total is the full size — the bar continues, not resets
	if lastTotal != 10 || lastDone != 10 {
		t.Errorf("progress = %d/%d, want 10/10 (absolute, resumed)", lastDone, lastTotal)
	}
	if len(f.rangeHeaders) != 1 || f.rangeHeaders[0] != "bytes=5-" {
		t.Errorf("Range headers = %v, want [bytes=5-]", f.rangeHeaders)
	}
	if b, err := os.ReadFile(filepath.Join(repoDir, "blobs", oid)); err != nil || string(b) != content {
		t.Fatalf("resumed blob = %q, err %v", b, err)
	}
}

// TestDownloadAlreadyCached verifies cache-first: a fully present model
// is skipped and nothing is fetched.
func TestDownloadAlreadyCached(t *testing.T) {
	content := "0123456789"
	f, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": []byte(content)})
	c := NewWithEndpoint(srv.URL, "")
	root := t.TempDir()

	// full download first
	if err := c.Download(context.Background(), root, "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	callsAfterFirst := f.resolveCalls

	var progressCalls int
	err := c.Download(context.Background(), root, "org/repo", "Q4_K_M",
		func(done, total int64) {
			progressCalls++
			if done != 0 || total != 0 {
				t.Errorf("cached run progress = %d/%d, want 0/0", done, total)
			}
		})
	if err != nil {
		t.Fatal(err)
	}
	if progressCalls != 1 {
		t.Errorf("progress calls = %d, want 1 (0/0)", progressCalls)
	}
	if f.resolveCalls != callsAfterFirst {
		t.Errorf("resolve calls grew: %d → %d", callsAfterFirst, f.resolveCalls)
	}
}

// TestDownloadSplitParts verifies split models download every part.
func TestDownloadSplitParts(t *testing.T) {
	files := map[string][]byte{
		"big-Q4_K_M-00001-of-00002.gguf": []byte("part-one"),
		"big-Q4_K_M-00002-of-00002.gguf": []byte("part-two"),
		"model-Q8_0.gguf":                []byte("other-quant"),
	}
	_, srv := newFakeHFS(t, files)
	c := NewWithEndpoint(srv.URL, "")
	root := t.TempDir()

	if err := c.Download(context.Background(), root, "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	found, err := storage.Scan(root, nil)
	if err != nil || len(found) != 2 {
		t.Fatalf("Scan = %+v, err %v; want the two Q4_K_M parts only", found, err)
	}
	for _, f := range found {
		if !strings.Contains(f.Path, "00001-of-00002") && !strings.Contains(f.Path, "00002-of-00002") {
			t.Errorf("unexpected cached file: %s", f.Path)
		}
	}
}

// TestDownloadShaMismatch verifies verification failure removes the
// partial and errors.
func TestDownloadShaMismatch(t *testing.T) {
	content := "0123456789"
	f := &fakeHFS{t: t, files: map[string][]byte{"model-Q4_K_M.gguf": []byte(content)}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/refs"):
			json.NewEncoder(w).Encode(map[string]any{
				"branches": []map[string]string{{"name": "main", "targetCommit": testCommit}},
			})
		case strings.Contains(p, "/tree/"):
			// advertise a WRONG oid
			json.NewEncoder(w).Encode([]map[string]any{{
				"type": "file", "path": "model-Q4_K_M.gguf", "size": len(content),
				"lfs": map[string]any{"oid": strings.Repeat("0", 64), "size": len(content)},
			}})
		case strings.Contains(p, "/resolve/"):
			w.Write([]byte(content))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	_ = f

	c := NewWithEndpoint(srv.URL, "")
	root := t.TempDir()
	err := c.Download(context.Background(), root, "org/repo", "Q4_K_M", nil)
	if err == nil || !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Fatalf("err = %v, want sha256 mismatch", err)
	}
	partials, _ := filepath.Glob(filepath.Join(root, "models--org--repo", "blobs", "*.incomplete"))
	if len(partials) != 0 {
		t.Errorf("mismatched partial must be removed, found %v", partials)
	}
}

// TestDownloadCancel verifies ctx cancellation aborts mid-file and
// keeps the partial for a later resume.
func TestDownloadCancel(t *testing.T) {
	content := strings.Repeat("x", 1024*1024) // 1 MiB
	_, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": []byte(content)})
	c := NewWithEndpoint(srv.URL, "")
	root := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	var cancelled bool
	err := c.Download(ctx, root, "org/repo", "Q4_K_M",
		func(done, total int64) {
			if done > 0 && !cancelled {
				cancelled = true
				cancel()
			}
		})
	if !IsCanceled(err) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	partials, _ := filepath.Glob(filepath.Join(root, "models--org--repo", "blobs", "*.incomplete"))
	if len(partials) != 1 {
		t.Errorf("cancelled download must keep its partial, found %v", partials)
	}
}

// TestDownloadNotFound verifies a missing file maps to ErrNotFound.
func TestDownloadNotFound(t *testing.T) {
	_, srv := newFakeHFS(t, map[string][]byte{})
	c := NewWithEndpoint(srv.URL, "")
	err := c.Download(context.Background(), t.TempDir(), "org/repo", "Q4_K_M", nil)
	if err == nil || !IsNotFound(err) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestDownloadRequiresQuant verifies an empty quant is rejected.
func TestDownloadRequiresQuant(t *testing.T) {
	_, srv := newFakeHFS(t, map[string][]byte{"model.gguf": []byte("x")})
	c := NewWithEndpoint(srv.URL, "")
	if err := c.Download(context.Background(), t.TempDir(), "org/repo", "", nil); err == nil {
		t.Error("empty quant must error")
	}
}

// TestDownloadClientHasNoTimeout pins the owner-reported regression:
// the 30s API timeout must never apply to downloads — a 16 GiB body
// read would die mid-stream with "context deadline exceeded". Only ctx
// (user cancel / pause) may end a download.
func TestDownloadClientHasNoTimeout(t *testing.T) {
	c := NewWithEndpoint("https://example.test", "")
	if c.dlHTTP == nil || c.dlHTTP.Timeout != 0 {
		t.Errorf("download client timeout = %v, want 0 (no timeout)", c.dlHTTP.Timeout)
	}
	if c.http.Timeout != requestTimeout {
		t.Errorf("API client timeout = %v, want %v", c.http.Timeout, requestTimeout)
	}
}
