package hf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Refs resolves the target commit of a repo's main branch — llama.cpp's
// get_repo_commit (GET /api/models/{repo}/refs). The downloader needs
// the commit to write refs/main and the snapshots/<commit>/ entries in
// the exact layout llama.cpp reads.
func (c *Client) Refs(ctx context.Context, repo string) (string, error) {
	u := c.endpoint + "/api/models/" + escapeRepo(repo) + "/refs"
	var resp struct {
		Branches []struct {
			Name          string `json:"name"`
			TargetCommit  string `json:"targetCommit"`
		} `json:"branches"`
	}
	body, err := c.get(ctx, u)
	if err != nil {
		return "", err
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", &Error{Kind: ErrNetwork, Message: "invalid refs JSON: " + err.Error()}
	}
	for _, b := range resp.Branches {
		if b.Name == "main" && isCommitHash(b.TargetCommit) {
			return b.TargetCommit, nil
		}
	}
	return "", &Error{Kind: ErrNetwork, Message: "repo has no main branch ref"}
}

// get performs the GET (shared by getJSON and Refs) and returns the raw
// body, mapping failures to typed Errors.
func (c *Client) get(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	req.Header.Set("Accept", "application/json")
	c.authorize(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Kind: ErrNetwork, Message: err.Error()}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	case http.StatusNotFound:
		return nil, &Error{Kind: ErrNotFound, Status: resp.StatusCode, Message: "not found"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, &Error{Kind: ErrGated, Status: resp.StatusCode, Message: "gated or requires a token"}
	default:
		return nil, &Error{Kind: ErrHTTP, Status: resp.StatusCode, Message: "unexpected status"}
	}
}

// isCommitHash reports whether s is a 40-hex git commit hash.
func isCommitHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}

// resolveURL builds the download URL for a repo file at a commit.
func (c *Client) resolveURL(repo, commit, file string) string {
	return c.endpoint + "/" + escapeRepo(repo) + "/resolve/" +
		url.PathEscape(commit) + "/" + strings.TrimPrefix(file, "/")
}
