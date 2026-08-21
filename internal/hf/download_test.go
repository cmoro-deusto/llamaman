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
	"sync"
	"testing"
	"time"

	"github.com/cmoro-deusto/llamaman/internal/storage"
)

const testCommit = "68c3ea2061e8c7688455fab07597dde0f4d7f0db"

// fakeHFS is a minimal Hugging Face file server: refs, tree, and
// Range-capable resolve. File content doubles as the oid source.
type fakeHFS struct {
	t            *testing.T
	files        map[string][]byte
	mu           sync.Mutex // parallel chunk requests hit concurrently
	rangeHeaders []string
	authHeaders  []string // Authorization header of each resolve request
	resolveCalls int
	servedBytes  int64 // total body bytes written across resolve responses
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
			f.mu.Lock()
			f.resolveCalls++
			f.authHeaders = append(f.authHeaders, r.Header.Get("Authorization"))
			f.mu.Unlock()
			file := p[strings.Index(p, "/resolve/")+len("/resolve/"):]
			file = file[strings.Index(file, "/")+1:] // strip the commit
			data, ok := f.files[file]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			if rng := r.Header.Get("Range"); rng != "" {
				f.mu.Lock()
				f.rangeHeaders = append(f.rangeHeaders, rng)
				f.mu.Unlock()
				from, to, err := parseRange(rng, int64(len(data)))
				if err != nil {
					http.Error(w, "bad range", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to-1, len(data)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(data[from:to])
				f.mu.Lock()
				f.servedBytes += to - from
				f.mu.Unlock()
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write(data)
			f.mu.Lock()
			f.servedBytes += int64(len(data))
			f.mu.Unlock()
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// parseRange parses "bytes=a-" and "bytes=a-b" (inclusive b) into the
// half-open [from, to) against a body of size bytes.
func parseRange(rng string, size int64) (from, to int64, err error) {
	spec := strings.TrimPrefix(rng, "bytes=")
	first, last, found := strings.Cut(spec, "-")
	if !found {
		return 0, 0, fmt.Errorf("bad range %q", rng)
	}
	if from, err = strconv.ParseInt(first, 10, 64); err != nil {
		return 0, 0, err
	}
	to = size
	if last != "" {
		end, err := strconv.ParseInt(last, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		to = min(end+1, size)
	}
	if from < 0 || from > to {
		return 0, 0, fmt.Errorf("bad range %q", rng)
	}
	return from, to, nil
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

// TestSelectModelFilesSkipsSidecars guards the primary pick: an
// mmproj-* or dflash-* .gguf (listed first, alphabetically) must never
// become the downloaded model — llama.cpp's is_model_file excludes the
// same names (common/download.cpp).
func TestSelectModelFilesSkipsSidecars(t *testing.T) {
	files := []RepoFile{
		{Path: "mmproj-Muse-Glimmer-30B-Q8_0.gguf", Size: 2 << 30},
		{Path: "Muse-Glimmer-30B-Q8_0.gguf", Size: 5 << 30},
		{Path: "dflash-kquant.gguf", Size: 1 << 30},
	}
	got := selectModelFiles(files, "Q8_0")
	if len(got) != 1 || got[0].Path != "Muse-Glimmer-30B-Q8_0.gguf" {
		t.Fatalf("Q8_0 selected %+v, want the model file only", got)
	}
	if got := selectModelFiles(files, "KQUANT"); got != nil {
		t.Fatalf("KQUANT selected %+v, want none (dflash is a sidecar)", got)
	}
}

// testBlob builds n deterministic non-repeating bytes so a misplaced
// chunk write cannot produce a passing sha256.
func testBlob(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i*31>>3 ^ i)
	}
	return b
}

// parallelClient tunes a client for chunked-download tests: small
// chunks, several connections, fast retries.
func parallelClient(t *testing.T, endpoint string, conns int) *Client {
	t.Helper()
	c := NewWithEndpoint(endpoint, "")
	c.SetConnections(conns)
	c.chunkSize = 128 << 10
	c.stallTimeout = 2 * time.Second
	c.retryDelay = 10 * time.Millisecond
	return c
}

// TestDownloadParallelChunks verifies a large blob fetches as bounded
// ranged chunks, reassembles byte-exact, and leaves no resume sidecar.
func TestDownloadParallelChunks(t *testing.T) {
	content := testBlob(1 << 20) // 1 MiB = 8 chunks of 128 KiB
	f, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": content})
	c := parallelClient(t, srv.URL, 4)
	root := t.TempDir()

	var lastDone, lastTotal int64
	var mu sync.Mutex
	err := c.Download(context.Background(), root, "org/repo", "Q4_K_M",
		func(done, total int64) {
			mu.Lock()
			lastDone, lastTotal = done, total
			mu.Unlock()
		})
	if err != nil {
		t.Fatal(err)
	}
	blobPath := filepath.Join(root, "models--org--repo", "blobs", sha256Hex(content))
	got, err := os.ReadFile(blobPath)
	if err != nil || string(got) != string(content) {
		t.Fatalf("blob mismatch (len %d, err %v)", len(got), err)
	}
	if len(f.rangeHeaders) != 8 {
		t.Errorf("range requests = %d (%v), want 8 bounded chunks", len(f.rangeHeaders), f.rangeHeaders)
	}
	for _, rng := range f.rangeHeaders {
		if !strings.Contains(strings.TrimPrefix(rng, "bytes="), "-") || strings.HasSuffix(rng, "-") {
			t.Errorf("chunk range %q is not bounded", rng)
		}
	}
	if lastDone != int64(len(content)) || lastTotal != int64(len(content)) {
		t.Errorf("final progress = %d/%d, want %d/%d", lastDone, lastTotal, len(content), len(content))
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "models--org--repo", "blobs", "*.incomplete*")); len(leftovers) != 0 {
		t.Errorf("sidecar/partial left behind: %v", leftovers)
	}
}

// TestDownloadParallelPauseResume verifies a cancelled parallel
// download keeps its partial + sidecar and a later run fetches only
// the missing bytes, continuing the absolute progress bar.
func TestDownloadParallelPauseResume(t *testing.T) {
	content := testBlob(1 << 20)
	f, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": content})
	root := t.TempDir()
	const threshold = 256 << 10

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var once sync.Once
	err := parallelClient(t, srv.URL, 3).Download(ctx, root, "org/repo", "Q4_K_M",
		func(done, total int64) {
			if done >= threshold {
				once.Do(cancel)
			}
		})
	if !IsCanceled(err) {
		t.Fatalf("err = %v, want cancellation", err)
	}
	blobs := filepath.Join(root, "models--org--repo", "blobs")
	oid := sha256Hex(content)
	if _, err := os.Stat(filepath.Join(blobs, oid+".incomplete")); err != nil {
		t.Fatalf("partial must survive the cancel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(blobs, oid+".incomplete.state")); err != nil {
		t.Fatalf("sidecar must survive the cancel: %v", err)
	}

	f.mu.Lock()
	servedBefore := f.servedBytes
	f.mu.Unlock()
	var lastDone, lastTotal int64
	var mu sync.Mutex
	err = parallelClient(t, srv.URL, 3).Download(context.Background(), root, "org/repo", "Q4_K_M",
		func(done, total int64) {
			mu.Lock()
			lastDone, lastTotal = done, total
			mu.Unlock()
		})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(blobs, oid))
	if err != nil || string(got) != string(content) {
		t.Fatalf("resumed blob mismatch (len %d, err %v)", len(got), err)
	}
	f.mu.Lock()
	served2 := f.servedBytes - servedBefore
	f.mu.Unlock()
	if served2 > int64(len(content))-threshold {
		t.Errorf("resume served %d bytes, want at most %d (only the missing tail)",
			served2, int64(len(content))-threshold)
	}
	if lastDone != int64(len(content)) || lastTotal != int64(len(content)) {
		t.Errorf("final progress = %d/%d, want full size", lastDone, lastTotal)
	}
}

// TestDownloadLegacyPartialParallelResume verifies a pre-sidecar
// contiguous partial is credited into the chunk grid: only the bytes
// past the prefix are fetched.
func TestDownloadLegacyPartialParallelResume(t *testing.T) {
	content := testBlob(1 << 20)
	const prefix = 300 << 10
	f, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": content})
	root := t.TempDir()
	blobs := filepath.Join(root, "models--org--repo", "blobs")
	oid := sha256Hex(content)
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, oid+".incomplete"), content[:prefix], 0o644); err != nil {
		t.Fatal(err)
	}

	if err := parallelClient(t, srv.URL, 4).Download(context.Background(), root, "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(blobs, oid))
	if err != nil || string(got) != string(content) {
		t.Fatalf("blob mismatch (len %d, err %v)", len(got), err)
	}
	f.mu.Lock()
	served := f.servedBytes
	f.mu.Unlock()
	if want := int64(len(content) - prefix); served != want {
		t.Errorf("served %d bytes, want exactly the %d past the legacy prefix", served, want)
	}
}

// TestDownloadCompletePartialFinalizes verifies a fully-fetched but
// never-verified partial (crash between EOF and rename) finalizes
// without refetching a byte.
func TestDownloadCompletePartialFinalizes(t *testing.T) {
	content := testBlob(64 << 10)
	f, srv := newFakeHFS(t, map[string][]byte{"model-Q4_K_M.gguf": content})
	root := t.TempDir()
	blobs := filepath.Join(root, "models--org--repo", "blobs")
	oid := sha256Hex(content)
	if err := os.MkdirAll(blobs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blobs, oid+".incomplete"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewWithEndpoint(srv.URL, "").Download(context.Background(), root, "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	if f.resolveCalls != 0 {
		t.Errorf("resolve calls = %d, want 0 (nothing to fetch)", f.resolveCalls)
	}
	if _, err := os.Stat(filepath.Join(blobs, oid)); err != nil {
		t.Errorf("blob not finalized: %v", err)
	}
}

// TestDownloadStallReconnects pins the crawl fix: a connection that
// goes silent is killed after stallTimeout and the download reconnects
// from the current offset instead of hanging forever.
func TestDownloadStallReconnects(t *testing.T) {
	content := testBlob(32 << 10)
	var mu sync.Mutex
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/refs"):
			json.NewEncoder(w).Encode(map[string]any{
				"branches": []map[string]string{{"name": "main", "targetCommit": testCommit}},
			})
		case strings.Contains(p, "/tree/"):
			json.NewEncoder(w).Encode([]map[string]any{{
				"type": "file", "path": "model-Q4_K_M.gguf", "size": len(content),
				"lfs": map[string]any{"oid": sha256Hex(content), "size": len(content)},
			}})
		case strings.Contains(p, "/resolve/"):
			mu.Lock()
			calls++
			n := calls
			mu.Unlock()
			if n == 1 {
				// serve a taste, then go silent until the client bails
				w.WriteHeader(http.StatusOK)
				w.Write(content[:5])
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				return
			}
			from, to, err := parseRange(r.Header.Get("Range"), int64(len(content)))
			if err != nil {
				t.Errorf("reconnect without a valid Range: %v", err)
				http.Error(w, "bad range", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, to-1, len(content)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(content[from:to])
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := NewWithEndpoint(srv.URL, "")
	c.stallTimeout = 100 * time.Millisecond
	c.retryDelay = 10 * time.Millisecond
	root := t.TempDir()
	if err := c.Download(context.Background(), root, "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Errorf("resolve calls = %d, want a reconnect after the stall", calls)
	}
	blob := filepath.Join(root, "models--org--repo", "blobs", sha256Hex(content))
	if got, err := os.ReadFile(blob); err != nil || string(got) != string(content) {
		t.Fatalf("blob mismatch after reconnect (len %d, err %v)", len(got), err)
	}
}

// TestDownloadParallelNoRangeFallback verifies a server that ignores
// Range requests degrades to one sequential stream and still completes.
func TestDownloadParallelNoRangeFallback(t *testing.T) {
	content := testBlob(1 << 20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/refs"):
			json.NewEncoder(w).Encode(map[string]any{
				"branches": []map[string]string{{"name": "main", "targetCommit": testCommit}},
			})
		case strings.Contains(p, "/tree/"):
			json.NewEncoder(w).Encode([]map[string]any{{
				"type": "file", "path": "model-Q4_K_M.gguf", "size": len(content),
				"lfs": map[string]any{"oid": sha256Hex(content), "size": len(content)},
			}})
		case strings.Contains(p, "/resolve/"):
			w.WriteHeader(http.StatusOK) // Range deliberately ignored
			w.Write(content)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	c := parallelClient(t, srv.URL, 4)
	root := t.TempDir()
	var lastDone, lastTotal int64
	var mu sync.Mutex
	err := c.Download(context.Background(), root, "org/repo", "Q4_K_M",
		func(done, total int64) {
			mu.Lock()
			lastDone, lastTotal = done, total
			mu.Unlock()
		})
	if err != nil {
		t.Fatal(err)
	}
	blob := filepath.Join(root, "models--org--repo", "blobs", sha256Hex(content))
	if got, err := os.ReadFile(blob); err != nil || string(got) != string(content) {
		t.Fatalf("blob mismatch (len %d, err %v)", len(got), err)
	}
	if lastDone != int64(len(content)) || lastTotal != int64(len(content)) {
		t.Errorf("final progress = %d/%d, want full size (rewound cleanly)", lastDone, lastTotal)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(root, "models--org--repo", "blobs", "*.state*")); len(leftovers) != 0 {
		t.Errorf("sidecar left behind after fallback: %v", leftovers)
	}
}

// TestSetConnectionsClamps pins the clamp contract: <= 0 restores the
// default, above MaxConnections caps.
func TestSetConnectionsClamps(t *testing.T) {
	c := NewWithEndpoint("https://example.test", "")
	if got := c.connections(); got != DefaultConnections {
		t.Errorf("unset connections = %d, want %d", got, DefaultConnections)
	}
	for _, tc := range []struct{ set, want int }{
		{1, 1}, {8, 8}, {MaxConnections, MaxConnections},
		{MaxConnections + 50, MaxConnections}, {0, DefaultConnections}, {-3, DefaultConnections},
	} {
		c.SetConnections(tc.set)
		if got := c.connections(); got != tc.want {
			t.Errorf("SetConnections(%d) → %d, want %d", tc.set, got, tc.want)
		}
	}
}

// TestChunkStatePrefixCredit pins the legacy-prefix conversion math
// and sidecar validation.
func TestChunkStatePrefixCredit(t *testing.T) {
	st := newChunkState(1000, 300, 650)
	want := []int64{300, 300, 50, 0}
	if len(st.Done) != len(want) {
		t.Fatalf("chunks = %d, want %d", len(st.Done), len(want))
	}
	for i, w := range want {
		if st.Done[i] != w {
			t.Errorf("Done[%d] = %d, want %d", i, st.Done[i], w)
		}
	}
	if st.doneBytes() != 650 {
		t.Errorf("doneBytes = %d, want 650", st.doneBytes())
	}
	if !st.valid(1000) {
		t.Error("fresh state must validate")
	}
	if st.valid(999) {
		t.Error("size mismatch must invalidate")
	}
	st.Done[3] = 400 // beyond the 100-byte tail chunk
	if st.valid(1000) {
		t.Error("overflowing chunk must invalidate")
	}
}

// TestDownloadSendsBearerToken pins the gated-repo fix: the blob
// download itself must carry the Bearer token (§16.2: every request),
// not only the API calls — a gated repo's resolve 401s without it.
// Without a token the header must stay absent.
func TestDownloadSendsBearerToken(t *testing.T) {
	content := "0123456789"
	files := map[string][]byte{"model-Q4_K_M.gguf": []byte(content)}

	f, srv := newFakeHFS(t, files)
	c := NewWithEndpoint(srv.URL, "hf_secret12345678901234567890")
	if err := c.Download(context.Background(), t.TempDir(), "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	if len(f.authHeaders) == 0 {
		t.Fatal("no resolve requests recorded")
	}
	for _, h := range f.authHeaders {
		if h != "Bearer hf_secret12345678901234567890" {
			t.Errorf("resolve Authorization = %q, want Bearer token", h)
		}
	}

	f2, srv2 := newFakeHFS(t, files)
	c2 := NewWithEndpoint(srv2.URL, "")
	if err := c2.Download(context.Background(), t.TempDir(), "org/repo", "Q4_K_M", nil); err != nil {
		t.Fatal(err)
	}
	for _, h := range f2.authHeaders {
		if h != "" {
			t.Errorf("no token set, resolve Authorization = %q, want absent", h)
		}
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
