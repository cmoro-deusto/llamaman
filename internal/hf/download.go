package hf

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
		off := int64(0)
		if info, err := os.Stat(filepath.Join(blobsDir, f.OID) + ".incomplete"); err == nil {
			off = info.Size()
		}
		if off > f.Size {
			off = 0 // corrupt partial: restart clean
		}
		jobs = append(jobs, job{file: f, offset: off})
	}
	var total int64
	for _, j := range jobs {
		total += j.file.Size - j.offset
	}
	var done int64
	if progress != nil {
		progress(0, total)
	}
	if len(jobs) == 0 {
		return nil // everything cached
	}
	refsWritten := false
	for _, j := range jobs {
		blobPath := filepath.Join(blobsDir, j.file.OID)
		n, err := c.downloadOne(ctx, j.file, blobPath, commit, repo, j.offset, func(d int64) {
			done += d
			if progress != nil {
				progress(done, total)
			}
		})
		if err != nil {
			return err
		}
		_ = n
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

// downloadOne fetches one blob into blobsDir/<oid> starting at offset
// (the resume position from a partial <oid>.incomplete). The blob is
// verified (full sha256 == oid) before the caller renames it into
// place. progress reports bytes fetched this run.
func (c *Client) downloadOne(ctx context.Context, f RepoFile, blobPath, commit, repo string, offset int64, progress func(done int64)) (int64, error) {
	if f.OID == "" {
		return 0, fmt.Errorf("hf: %s has no oid to verify against", f.Path)
	}
	partial := blobPath + ".incomplete"
	if offset > 0 {
		if info, err := os.Stat(partial); err == nil && info.Size() != offset {
			offset = 0 // partial changed under us; restart clean
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.resolveURL(repo, commit, f.Path), nil)
	if err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		offset = 0 // server ignored the Range; start clean
	case http.StatusPartialContent:
	default:
		return 0, downloadStatusError(resp.StatusCode)
	}

	if err := os.MkdirAll(filepath.Dir(partial), 0o755); err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	fh, err := os.OpenFile(partial, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	defer fh.Close()
	if offset > 0 {
		if _, err := fh.Seek(offset, io.SeekStart); err != nil {
			return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
		}
	}

	written := int64(0)
	buf := make([]byte, 256<<10)
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := fh.Write(buf[:n]); werr != nil {
				return 0, &Error{Kind: ErrNetwork, Message: werr.Error()}
			}
			written += int64(n)
			progress(int64(n)) // incremental — the caller accumulates
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, &Error{Kind: ErrNetwork, Message: rerr.Error()}
		}
	}

	// verify the full file against the LFS sha256
	got, err := sha256File(partial)
	if err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	if got != strings.ToLower(f.OID) {
		_ = os.Remove(partial)
		return 0, fmt.Errorf("hf: sha256 mismatch for %s (got %s, want %s)", f.Path, got, f.OID)
	}
	if err := os.Rename(partial, blobPath); err != nil {
		return 0, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	return written, nil
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
// `quant[.-]` (case-insensitive), skipping only non-first split parts,
// then every file sharing its split prefix.
func selectModelFiles(files []RepoFile, quant string) []RepoFile {
	pattern := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(quant) + `[.-]`)
	var primary *RepoFile
	for i := range files {
		f := &files[i]
		if !isGGUF(f.Path) || !pattern.MatchString(f.Path) {
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
