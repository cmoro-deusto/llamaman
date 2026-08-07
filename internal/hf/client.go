// Package hf is the Hugging Face API client (DESIGN §16.2) — the
// shared network layer for the quant picker, the Storage & Downloads
// manager's download action, the config editor's repo check, and the
// HF browser. Requests happen only on explicit caller actions (P7),
// only to the resolved endpoint (HF_ENDPOINT, default
// huggingface.co). No download loop — that lives in the manager item.
package hf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"
)

// defaultEndpoint is huggingface.co; override with $HF_ENDPOINT
// (mirrors llama.cpp's common_get_model_endpoint).
const defaultEndpoint = "https://huggingface.co"

// requestTimeout bounds every request; callers cancel via ctx.
const requestTimeout = 30 * time.Second

// Client talks to the Hugging Face API.
type Client struct {
	endpoint string
	token    string
	http     *http.Client
}

// New builds a Client from the environment: $HF_ENDPOINT (or the
// default) and $HF_TOKEN (optional, for gated repos).
func New() (*Client, error) {
	return NewWithEndpoint(os.Getenv("HF_ENDPOINT"), os.Getenv("HF_TOKEN")), nil
}

// NewWithEndpoint builds a Client with an explicit endpoint and token.
// An empty endpoint falls back to the default. Endpoint and token are
// used verbatim; the caller is responsible for their trustworthiness
// (P7 — only user-specified hosts).
func NewWithEndpoint(endpoint, token string) *Client {
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Client{
		endpoint: strings.TrimRight(endpoint, "/"),
		token:    token,
		http:     &http.Client{Timeout: requestTimeout},
	}
}

// Tree lists the files of a repo at a revision (branch or commit).
// Directories are skipped. The single round trip behind existence
// checks, quant lists, sizes, and sha256 verification.
func (c *Client) Tree(ctx context.Context, repo, revision string) ([]RepoFile, error) {
	if revision == "" {
		revision = "main"
	}
	u := c.endpoint + "/api/models/" + escapeRepo(repo) + "/tree/" +
		url.PathEscape(revision) + "?recursive=true"
	var entries []struct {
		Type string `json:"type"`
		Path string `json:"path"`
		Size int64  `json:"size"`
		OID  string `json:"oid"`
		LFS  *struct {
			OID  string `json:"oid"`
			Size int64  `json:"size"`
		} `json:"lfs"`
	}
	if err := c.getJSON(ctx, u, &entries); err != nil {
		return nil, err
	}
	var out []RepoFile
	for _, e := range entries {
		if e.Type != "file" || e.Path == "" {
			continue
		}
		f := RepoFile{Path: e.Path, Size: e.Size, OID: e.OID}
		if e.LFS != nil {
			if e.LFS.Size > 0 {
				f.Size = e.LFS.Size
			}
			if e.LFS.OID != "" {
				f.OID = e.LFS.OID
			}
		}
		out = append(out, f)
	}
	return out, nil
}

// Repo fetches the metadata of a repo.
func (c *Client) Repo(ctx context.Context, repo string) (RepoMeta, error) {
	u := c.endpoint + "/api/models/" + escapeRepo(repo)
	var m struct {
		ID        string   `json:"id"`
		SHA       string   `json:"sha"`
		Downloads int64    `json:"downloads"`
		Likes     int64    `json:"likes"`
		Tags      []string `json:"tags"`
	}
	if err := c.getJSON(ctx, u, &m); err != nil {
		return RepoMeta{}, err
	}
	return RepoMeta{ID: m.ID, SHA: m.SHA, Downloads: m.Downloads, Likes: m.Likes, Tags: m.Tags}, nil
}

// RepoExists reports whether the repo resolves. A gated repo exists:
// only ErrNotFound maps to false, all other errors surface.
func (c *Client) RepoExists(ctx context.Context, repo string) (bool, error) {
	_, err := c.Tree(ctx, repo, "main")
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// ErrorKind classifies a failed request (DESIGN §16.2, P3) so callers
// can give distinct messages: not found vs gated vs network vs other.
type ErrorKind int

const (
	ErrNotFound ErrorKind = iota // HTTP 404 — repo or file does not exist
	ErrGated                     // HTTP 401/403 — gated or requires a token
	ErrNetwork                   // transport-level failure (DNS, timeout, …)
	ErrHTTP                      // any other HTTP status
)

// Error is a typed client error carrying the classification.
type Error struct {
	Kind    ErrorKind
	Status  int // 0 for ErrNetwork
	Message string
}

func (e *Error) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("hf: %s (HTTP %d)", e.Message, e.Status)
	}
	return "hf: " + e.Message
}

// IsNotFound reports whether err classifies as a 404.
func IsNotFound(err error) bool { return kindOf(err) == ErrNotFound }

// IsGated reports whether err classifies as 401/403.
func IsGated(err error) bool { return kindOf(err) == ErrGated }

func kindOf(err error) ErrorKind {
	var he *Error
	if errors.As(err, &he) {
		return he.Kind
	}
	return ErrNetwork
}

// getJSON GETs u and decodes the JSON body into v.
func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	body, err := c.get(ctx, u)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, v); err != nil {
		return &Error{Kind: ErrNetwork, Message: "invalid JSON: " + err.Error()}
	}
	return nil
}

// escapeRepo path-escapes each repo segment (repos may contain dots and
// hyphens, which must not be interpreted as path structure).
func escapeRepo(repo string) string {
	segs := strings.Split(repo, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return path.Join(segs...)
}
