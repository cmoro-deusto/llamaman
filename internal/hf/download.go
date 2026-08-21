package hf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cmoro-deusto/llamaman/internal/storage"
)

// Download fetches the files of repo:quant into the cache root,
// writing llama.cpp's exact HF hub layout (DESIGN §16.4, verified):
//
//	<root>/models--org--model/
//	  blobs/<oid>            (partial download: <oid>.incomplete)
//	  refs/main              (the commit)
//	  snapshots/<commit>/<file>  (symlink to ../../blobs/<oid>)
//
// Already-cached files are skipped (cache-first). The chosen quant is
// matched the way llama.cpp's find_best_model does: regex
// `quant + "[.-]"` over the file path (case-insensitive); split models
// download all their parts. progress reports aggregate done/total bytes
// across the files fetched this run (total == 0 means everything was
// already cached). Cancelling ctx aborts and leaves partial blobs on
// disk — a later call resumes them via Range.
func (c *Client) Download(ctx context.Context, root, repo, quant string, progress func(done, total int64)) error {
	if strings.TrimSpace(quant) == "" {
		return fmt.Errorf("hf: a quant is required")
	}
	commit, err := c.Refs(ctx, repo)
	if err != nil {
		return err
	}
	all, err := c.Tree(ctx, repo, commit)
	if err != nil {
		return err
	}
	files := selectModelFiles(all, quant)
	if len(files) == 0 {
		return &Error{Kind: ErrNotFound, Message: fmt.Sprintf("no %q model files in %s", quant, repo)}
	}

	repoDir := filepath.Join(root, storage.RepoFolderNames(repo)[0])
	blobsDir := filepath.Join(repoDir, "blobs")
	snapDir := filepath.Join(repoDir, "snapshots", commit)
	refsFile := filepath.Join(repoDir, "refs", "main")

	// Cache-first: skip files already present; compute the remaining
	// bytes per file so the progress bar reflects the actual work
	// (a resumed partial counts only its missing tail).
	type job struct {
		file   RepoFile
		offset int64
	}
	var jobs []job
	for _, f := range files {
		snapPath := filepath.Join(snapDir, f.Path)
		if _, err := os.Stat(snapPath); err == nil {
			continue // already cached
		}
		off := resumeDone(filepath.Join(blobsDir, f.OID), f)
		jobs = append(jobs, job{file: f, offset: off})
	}
	if len(jobs) == 0 {
		// everything cached: (0,0) is the manager's "already cached"
		// signal (its total==0 branch).
		if progress != nil {
			progress(0, 0)
		}
		return nil
	}
	// Absolute progress: total is the full size and done starts at the
	// bytes already on disk (partials), so a resume continues the bar
	// instead of resetting it to zero (owner report).
	total := int64(0)
	for _, f := range files {
		total += f.Size
	}
	inJobs := make(map[string]bool, len(jobs))
	var done atomic.Int64
	for _, j := range jobs {
		inJobs[j.file.Path] = true
		done.Add(j.offset)
	}
	for _, f := range files {
		if !inJobs[f.Path] {
			done.Add(f.Size) // already cached
		}
	}
	if progress != nil {
		progress(done.Load(), total)
	}
	// report accumulates deltas from possibly-concurrent chunk workers
	// (§16.4); slight reordering of two near-simultaneous snapshots is
	// harmless — the sum itself is exact.
	report := func(d int64) {
		v := done.Add(d)
		if progress != nil {
			progress(v, total)
		}
	}
	refsWritten := false
	for _, j := range jobs {
		blobPath := filepath.Join(blobsDir, j.file.OID)
		if err := c.downloadOne(ctx, j.file, blobPath, commit, repo, report); err != nil {
			return err
		}
		if !refsWritten {
			if err := writeRef(refsFile, commit); err != nil {
				return err
			}
			refsWritten = true
		}
		snapPath := filepath.Join(snapDir, j.file.Path)
		if err := finalizeBlob(blobPath, snapPath, filepath.Base(blobPath)); err != nil {
			return err
		}
	}
	return nil
}

// Tunables of the chunked downloader (§16.4). chunkSize, stallTimeout,
// and retryDelay live on the Client so tests can shrink them; these are
// the production values.
const (
	defaultChunkSize    = 32 << 20         // per-chunk range size
	defaultStallTimeout = 30 * time.Second // no-progress window before reconnecting
	defaultRetryDelay   = 500 * time.Millisecond
	maxChunkAttempts    = 5 // consecutive zero-progress attempts before giving up
	maxRestarts         = 3 // sequential from-zero restarts (server ignoring Range)
)

// errNoRanges signals the server answered a ranged request with a
// plain 200 — it does not support Range, so the parallel path falls
// back to a single sequential stream.
var errNoRanges = errors.New("hf: server ignored Range request")

// chunkState is the resume sidecar of a parallel download
// (`<oid>.incomplete.state`): the blob is cut into a fixed chunk grid
// and Done[i] holds the completed bytes at the start of chunk i. The
// partial is preallocated to full size, so — unlike the sequential
// path — its length says nothing about progress; the sidecar is the
// source of truth. It is persisted when a chunk completes and when the
// download stops (pause/cancel/error), and removed once the blob
// verifies.
type chunkState struct {
	Version int     `json:"version"`
	Size    int64   `json:"size"`
	Chunk   int64   `json:"chunk"`
	Done    []int64 `json:"done"`
}

// bounds returns chunk i's byte range [from, to).
func (st *chunkState) bounds(i int) (from, to int64) {
	from = int64(i) * st.Chunk
	to = min(from+st.Chunk, st.Size)
	return from, to
}

// doneBytes sums the completed bytes across all chunks.
func (st *chunkState) doneBytes() int64 {
	var sum int64
	for _, d := range st.Done {
		sum += d
	}
	return sum
}

// valid reports whether the sidecar matches a blob of the given size.
// An invalid sidecar is discarded (the partial cannot be trusted — its
// preallocated length lies about progress — so the download restarts).
func (st *chunkState) valid(size int64) bool {
	if st.Version != 1 || st.Size != size || st.Chunk <= 0 || size <= 0 {
		return false
	}
	if int64(len(st.Done)) != (size+st.Chunk-1)/st.Chunk {
		return false
	}
	for i, d := range st.Done {
		from, to := st.bounds(i)
		if d < 0 || d > to-from {
			return false
		}
	}
	return true
}

// newChunkState builds a fresh grid over size bytes, crediting a
// legacy contiguous prefix (a pre-sidecar `.incomplete`) so an old
// partial resumes instead of restarting.
func newChunkState(size, chunk, prefix int64) *chunkState {
	st := &chunkState{Version: 1, Size: size, Chunk: chunk,
		Done: make([]int64, (size+chunk-1)/chunk)}
	for i := range st.Done {
		from, to := st.bounds(i)
		if prefix <= from {
			break
		}
		st.Done[i] = min(prefix, to) - from
	}
	return st
}

// loadChunkState reads and validates a sidecar; nil when absent or
// unusable.
func loadChunkState(path string, size int64) *chunkState {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var st chunkState
	if json.Unmarshal(data, &st) != nil || !st.valid(size) {
		return nil
	}
	return &st
}

// persist writes the sidecar atomically (tmp + rename), so a crash
// never leaves a torn state file.
func (st *chunkState) persist(path string) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// resumeDone reports the bytes of f already present in a partial — the
// seed of the absolute progress bar. Parallel partials answer from
// their sidecar; legacy partials from their contiguous length.
func resumeDone(blobPath string, f RepoFile) int64 {
	partial := blobPath + ".incomplete"
	if st := loadChunkState(partial+".state", f.Size); st != nil {
		return st.doneBytes()
	}
	if info, err := os.Stat(partial); err == nil && info.Size() <= f.Size {
		return info.Size()
	}
	return 0
}

// downloadOne fetches one blob into blobs/<oid>, resuming any partial,
// and verifies it (full sha256 == oid) before the caller links it into
// the snapshot. Blobs of at least two chunks fetch as parallel ranged
// chunks when more than one connection is configured; small blobs (and
// connections == 1) stream sequentially with the legacy
// contiguous-partial resume. progress reports bytes fetched this run,
// incrementally, possibly from several goroutines — and may go
// negative once (rewind) when a parallel attempt falls back to
// sequential-from-zero.
func (c *Client) downloadOne(ctx context.Context, f RepoFile, blobPath, commit, repo string, progress func(done int64)) error {
	if f.OID == "" {
		return fmt.Errorf("hf: %s has no oid to verify against", f.Path)
	}
	partial := blobPath + ".incomplete"
	statePath := partial + ".state"
	url := c.resolveURL(repo, commit, f.Path)
	if err := os.MkdirAll(filepath.Dir(partial), 0o755); err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}

	// A parallel partial resumes from its sidecar grid regardless of
	// the currently configured connection count.
	if st := loadChunkState(statePath, f.Size); st != nil {
		err := c.downloadChunks(ctx, url, partial, statePath, st, progress)
		if errors.Is(err, errNoRanges) {
			return c.sequentialFallback(ctx, url, blobPath, f, progress)
		}
		if err != nil {
			return err
		}
		return c.verifyBlob(f, blobPath)
	}

	// No sidecar: any partial is a legacy contiguous prefix.
	prefix := int64(0)
	if info, err := os.Stat(partial); err == nil {
		prefix = info.Size()
		if prefix > f.Size {
			prefix = 0 // corrupt partial: restart clean
		}
	}
	if prefix == f.Size && f.Size > 0 {
		// fully fetched but never verified/renamed: finish the job
		return c.verifyBlob(f, blobPath)
	}
	if c.connections() > 1 && f.Size >= 2*c.chunkSize {
		st := newChunkState(f.Size, c.chunkSize, prefix)
		if err := st.persist(statePath); err != nil {
			return &Error{Kind: ErrNetwork, Message: err.Error()}
		}
		err := c.downloadChunks(ctx, url, partial, statePath, st, progress)
		if errors.Is(err, errNoRanges) {
			return c.sequentialFallback(ctx, url, blobPath, f, progress)
		}
		if err != nil {
			return err
		}
		return c.verifyBlob(f, blobPath)
	}
	if err := c.downloadSequential(ctx, url, partial, f.Size, prefix, progress); err != nil {
		return err
	}
	return c.verifyBlob(f, blobPath)
}

// sequentialFallback restarts a blob from zero over one stream after
// the server ignored ranged requests: rewind the progress already
// credited (sidecar bytes), drop the sidecar, truncate the partial —
// its preallocated content cannot be trusted — and stream.
func (c *Client) sequentialFallback(ctx context.Context, url, blobPath string, f RepoFile, progress func(int64)) error {
	partial := blobPath + ".incomplete"
	statePath := partial + ".state"
	if st := loadChunkState(statePath, f.Size); st != nil && progress != nil && st.doneBytes() > 0 {
		progress(-st.doneBytes())
	}
	_ = os.Remove(statePath)
	if err := os.Truncate(partial, 0); err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	if err := c.downloadSequential(ctx, url, partial, f.Size, 0, progress); err != nil {
		return err
	}
	return c.verifyBlob(f, blobPath)
}

// downloadChunks fetches the pending chunks of st over up to
// connections() parallel ranged streams, each with its own
// stall-reconnect loop. The first error cancels the rest; the sidecar
// is persisted once more on the way out, so a pause mid-chunk resumes
// exactly. errNoRanges aborts the whole attempt (the caller falls back
// to sequential).
func (c *Client) downloadChunks(ctx context.Context, url, partial, statePath string, st *chunkState, progress func(int64)) error {
	fh, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	defer fh.Close()
	if err := fh.Truncate(st.Size); err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}

	var mu sync.Mutex // guards st.Done and sidecar writes
	defer func() {
		mu.Lock()
		_ = st.persist(statePath)
		mu.Unlock()
	}()

	var pending []int
	for i := range st.Done {
		from, to := st.bounds(i)
		if st.Done[i] < to-from {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	workers := min(c.connections(), len(pending))

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	idxCh := make(chan int)
	go func() {
		defer close(idxCh)
		for _, i := range pending {
			select {
			case idxCh <- i:
			case <-cctx.Done():
				return
			}
		}
	}()

	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idxCh {
				if err := c.fetchChunk(cctx, url, fh, st, i, statePath, &mu, progress); err != nil {
					errCh <- err
					cancel() // first failure stops the fleet
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)

	if err := ctx.Err(); err != nil {
		return err // user pause/cancel wins over induced worker aborts
	}
	var firstErr error
	for err := range errCh {
		if errors.Is(err, errNoRanges) {
			return err
		}
		if firstErr == nil && !errors.Is(err, context.Canceled) {
			firstErr = err
		}
	}
	return firstErr
}

// fetchChunk brings chunk i to completion: ranged requests from the
// chunk's current offset, reconnecting on stalls and transport errors,
// giving up after maxChunkAttempts consecutive attempts without a
// byte of progress. Done[i] advances as bytes land, so a pause
// mid-chunk resumes exactly where it stopped.
func (c *Client) fetchChunk(ctx context.Context, url string, fh *os.File, st *chunkState, i int, statePath string, mu *sync.Mutex, progress func(int64)) error {
	from0, to := st.bounds(i)
	attempts := 0
	for {
		mu.Lock()
		from := from0 + st.Done[i]
		mu.Unlock()
		if from >= to {
			mu.Lock()
			err := st.persist(statePath)
			mu.Unlock()
			if err != nil {
				return &Error{Kind: ErrNetwork, Message: err.Error()}
			}
			return nil
		}
		n, err := c.fetchRange(ctx, url, fh, from, to, func(w int64) {
			mu.Lock()
			st.Done[i] += w
			mu.Unlock()
			if progress != nil {
				progress(w)
			}
		})
		if err == nil {
			continue // loop persists and returns once the chunk is full
		}
		if ctx.Err() != nil {
			return ctx.Err() // pause/cancel: the deferred persist keeps progress
		}
		if errors.Is(err, errNoRanges) {
			return err
		}
		var he *Error
		if errors.As(err, &he) && he.Kind != ErrNetwork {
			return err // HTTP-status failures (404/gated/…) don't retry
		}
		if n == 0 {
			attempts++
		} else {
			attempts = 0
		}
		if attempts >= maxChunkAttempts {
			return err
		}
		if serr := c.sleepBackoff(ctx, attempts); serr != nil {
			return serr
		}
	}
}

// downloadSequential streams the blob over one connection — the legacy
// path for small blobs and connections == 1. The partial is a
// contiguous prefix whose length is the resume offset. Stalled or
// dropped connections reconnect from the current offset; a 200 to a
// ranged request restarts from zero (server without Range support).
func (c *Client) downloadSequential(ctx context.Context, url, partial string, size, offset int64, progress func(int64)) error {
	fh, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	defer fh.Close()
	attempts, restarts := 0, 0
	for offset < size {
		n, err := c.fetchRange(ctx, url, fh, offset, -1, progress)
		offset += n
		if err == nil {
			break // clean EOF — the sha verify judges completeness
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if errors.Is(err, errNoRanges) {
			restarts++
			if restarts > maxRestarts {
				return &Error{Kind: ErrNetwork, Message: "server ignored Range resume repeatedly"}
			}
			if progress != nil && offset > 0 {
				progress(-offset) // rewind the bar with the restart
			}
			offset = 0
			if terr := fh.Truncate(0); terr != nil {
				return &Error{Kind: ErrNetwork, Message: terr.Error()}
			}
			continue
		}
		var he *Error
		if errors.As(err, &he) && he.Kind != ErrNetwork {
			return err
		}
		if n == 0 {
			attempts++
		} else {
			attempts = 0
		}
		if attempts >= maxChunkAttempts {
			return err
		}
		if serr := c.sleepBackoff(ctx, attempts); serr != nil {
			return serr
		}
	}
	return nil
}

// fetchRange GETs url bytes [from, to) — to < 0 means to EOF — writing
// them into fh at their absolute offsets and reporting each landed
// slice via report. A watchdog kills the request when no byte arrives
// for stallTimeout (a single throttled-to-nothing TCP stream otherwise
// hangs forever); the caller reconnects from the current offset, which
// a fresh connection typically restores to full speed.
func (c *Client) fetchRange(ctx context.Context, url string, fh *os.File, from, to int64, report func(int64)) (int64, error) {
	rctx, cancel := context.WithCancel(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	c.authorize(req)
	ranged := false
	switch {
	case to >= 0:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to-1))
		ranged = true
	case from > 0:
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", from))
		ranged = true
	}
	resp, err := c.dlHTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusPartialContent:
	case http.StatusOK:
		if ranged {
			return 0, errNoRanges
		}
	default:
		return 0, downloadStatusError(resp.StatusCode)
	}

	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var stalled atomic.Bool
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		tick := max(c.stallTimeout/4, time.Millisecond)
		tick = min(tick, time.Second)
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-watchDone:
				return
			case <-rctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > c.stallTimeout {
					stalled.Store(true)
					cancel()
					return
				}
			}
		}
	}()

	var body io.Reader = resp.Body
	if to >= 0 {
		body = io.LimitReader(resp.Body, to-from)
	}
	written := int64(0)
	buf := make([]byte, 256<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := fh.WriteAt(buf[:n], from+written); werr != nil {
				return written, &Error{Kind: ErrNetwork, Message: werr.Error()}
			}
			written += int64(n)
			last.Store(time.Now().UnixNano())
			if report != nil {
				report(int64(n))
			}
		}
		if rerr == io.EOF {
			return written, nil
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return written, ctx.Err()
			}
			if stalled.Load() {
				return written, &Error{Kind: ErrNetwork,
					Message: fmt.Sprintf("stalled: no data for %s — reconnecting", c.stallTimeout)}
			}
			return written, &Error{Kind: ErrNetwork, Message: rerr.Error()}
		}
	}
}

// sleepBackoff waits before a reconnect attempt (retryDelay doubling
// per consecutive failure, capped at 5s), aborting early on cancel.
func (c *Client) sleepBackoff(ctx context.Context, attempt int) error {
	d := c.retryDelay
	for i := 1; i < attempt; i++ {
		d *= 2
	}
	d = min(d, 5*time.Second)
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// verifyBlob hashes the completed partial against the LFS oid, then
// renames it into place and drops the resume sidecar. A mismatch
// removes both so the next attempt starts clean.
func (c *Client) verifyBlob(f RepoFile, blobPath string) error {
	partial := blobPath + ".incomplete"
	got, err := sha256File(partial)
	if err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	if got != strings.ToLower(f.OID) {
		_ = os.Remove(partial)
		_ = os.Remove(partial + ".state")
		return fmt.Errorf("hf: sha256 mismatch for %s (got %s, want %s)", f.Path, got, f.OID)
	}
	_ = os.Remove(partial + ".state")
	if err := os.Rename(partial, blobPath); err != nil {
		return &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	return nil
}

// finalizeBlob links snapshots/<commit>/<file> → ../../blobs/<oid>,
// mirroring llama.cpp's finalize_file (symlink, degraded to copy).
func finalizeBlob(blobPath, snapPath, blobName string) error {
	if err := os.MkdirAll(filepath.Dir(snapPath), 0o755); err != nil {
		return err
	}
	rel := filepath.Join("..", "..", "blobs", blobName)
	if err := os.Symlink(rel, snapPath); err != nil {
		// degraded mode: copy (matches llama.cpp's fallback)
		data, err := os.ReadFile(blobPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(snapPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// writeRef writes refs/main = commit.
func writeRef(path, commit string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(commit+"\n"), 0o644)
}

// sha256File hashes a file from byte 0 (the partial may have been
// resumed mid-stream; the hash covers the whole blob).
func sha256File(path string) (string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer fh.Close()
	h := sha256.New()
	if _, err := io.Copy(h, fh); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// selectModelFiles picks the files of repo:quant per llama.cpp's
// find_best_model + get_split_files: first .gguf whose path matches
// `quant[.-]` (case-insensitive), skipping only non-first split parts
// and non-model sidecars (mmproj / imatrix / draft heads — llama.cpp's
// is_model_file), then every file sharing its split prefix.
func selectModelFiles(files []RepoFile, quant string) []RepoFile {
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(quant) + `[.-]`)
	var primary *RepoFile
	for i := range files {
		f := &files[i]
		if !isGGUF(f.Path) || isSidecar(f.Path) || !pattern.MatchString(f.Path) {
			continue
		}
		if splitCount(f.Path) > 1 && splitIndex(f.Path) != 1 {
			continue // non-first part of a split model
		}
		primary = f
		break
	}
	if primary == nil {
		return nil
	}
	prefix := splitPrefix(primary.Path)
	if prefix == "" {
		return []RepoFile{*primary}
	}
	var out []RepoFile
	for _, f := range files {
		if splitPrefix(f.Path) == prefix {
			out = append(out, f)
		}
	}
	return out
}

// splitIndex returns the part index (1-based) for a split file, or 0
// for a single-file model.
func splitIndex(p string) int {
	m := splitRE.FindStringSubmatch(filepath.Base(p))
	if m == nil {
		return 0
	}
	var idx int
	fmt.Sscanf(m[2], "%d", &idx)
	return idx
}

// splitCount returns the part count of a split file, or 1 for a
// single-file model.
func splitCount(p string) int {
	m := splitRE.FindStringSubmatch(filepath.Base(p))
	if m == nil {
		return 1
	}
	var n int
	fmt.Sscanf(m[3], "%d", &n)
	if n < 1 {
		return 1
	}
	return n
}

// splitPrefix returns the shared base of a split file (everything
// before -NNNNN-of-NNNNN), or "" for a single-file model.
func splitPrefix(p string) string {
	name := filepath.Base(p)
	if isGGUF(name) {
		name = name[:len(name)-5]
	}
	if m := splitRE.FindStringSubmatch(name); m != nil {
		return m[1]
	}
	return ""
}

func downloadStatusError(status int) error {
	switch status {
	case http.StatusNotFound:
		return &Error{Kind: ErrNotFound, Status: status, Message: "file not found"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &Error{Kind: ErrGated, Status: status, Message: "gated or requires a token"}
	default:
		return &Error{Kind: ErrHTTP, Status: status, Message: "download failed"}
	}
}

// IsCanceled reports whether err is a context cancellation from
// Download (pause/abort — the partial blob is kept for resume).
func IsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
