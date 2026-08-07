package hf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// treeHandler serves a canned tree response, capturing the request.
func treeHandler(t *testing.T, body string, status int) (*httptest.Server, func() *http.Request) {
	t.Helper()
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, func() *http.Request { return got }
}

func clientAt(t *testing.T, srv *httptest.Server, token string) *Client {
	t.Helper()
	return NewWithEndpoint(srv.URL, token)
}

func TestTreeParsesFiles(t *testing.T) {
	body := `[
		{"type": "file", "path": "model-Q4_K_M.gguf", "size": 42, "lfs": {"oid": "abc123", "size": 4096}},
		{"type": "file", "path": "model-Q8_0.gguf", "size": 99},
		{"type": "directory", "path": "sub"},
		{"type": "file", "path": "config.json", "size": 5}
	]`
	srv, getReq := treeHandler(t, body, http.StatusOK)
	c := clientAt(t, srv, "")

	files, err := c.Tree(context.Background(), "Qwen/Qwen3-32B-GGUF", "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 { // directory skipped, config.json kept (metadata filtering is later items' job)
		t.Fatalf("Tree() = %d files, want 3: %+v", len(files), files)
	}
	// LFS fields win over the plain ones.
	if files[0].OID != "abc123" || files[0].Size != 4096 {
		t.Errorf("LFS extraction failed: %+v", files[0])
	}
	if files[1].OID != "" || files[1].Size != 99 {
		t.Errorf("non-LFS entry: %+v", files[1])
	}
	if !strings.Contains(files[1].Path, "Q8_0") {
		t.Errorf("missing quant file: %+v", files)
	}

	r := getReq()
	if r == nil {
		t.Fatal("no request captured")
	}
	wantPath := "/api/models/Qwen/Qwen3-32B-GGUF/tree/main?recursive=true"
	if r.URL.Path != "/api/models/Qwen/Qwen3-32B-GGUF/tree/main" ||
		r.URL.RawQuery != "recursive=true" {
		t.Errorf("URL = %s, want %s", r.URL.String(), wantPath)
	}
	if r.Header.Get("Authorization") != "" {
		t.Error("no token set, Authorization must be absent")
	}
}

func TestTreeEmptyRevisionDefaultsMain(t *testing.T) {
	srv, getReq := treeHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "")
	if _, err := c.Tree(context.Background(), "org/repo", ""); err != nil {
		t.Fatal(err)
	}
	if r := getReq(); r == nil || !strings.HasSuffix(r.URL.Path, "/tree/main") {
		t.Errorf("default revision must be main, got %v", r.URL)
	}
}

func TestTreeSendsBearerToken(t *testing.T) {
	srv, getReq := treeHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "hf_secret12345678901234567890")
	if _, err := c.Tree(context.Background(), "org/repo", "main"); err != nil {
		t.Fatal(err)
	}
	if r := getReq(); r == nil || r.Header.Get("Authorization") != "Bearer hf_secret12345678901234567890" {
		t.Errorf("Authorization = %q, want Bearer header", r.Header.Get("Authorization"))
	}
}

func TestTreeEscapesRepoSegments(t *testing.T) {
	srv, getReq := treeHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "")
	if _, err := c.Tree(context.Background(), "org.name/repo-name", "main"); err != nil {
		t.Fatal(err)
	}
	if r := getReq(); r == nil || !strings.Contains(r.URL.Path, "org.name/repo-name") {
		t.Errorf("dots and hyphens must survive the path: %s", r.URL.Path)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		kind   ErrorKind
	}{
		{name: "404", status: http.StatusNotFound, kind: ErrNotFound},
		{name: "401", status: http.StatusUnauthorized, kind: ErrGated},
		{name: "403", status: http.StatusForbidden, kind: ErrGated},
		{name: "500", status: http.StatusInternalServerError, kind: ErrHTTP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := treeHandler(t, `{}`, tt.status)
			c := clientAt(t, srv, "")
			_, err := c.Tree(context.Background(), "org/repo", "main")
			if err == nil {
				t.Fatal("expected an error")
			}
			var he *Error
			if !errors.As(err, &he) {
				t.Fatalf("error is not a *hf.Error: %T %v", err, err)
			}
			if he.Kind != tt.kind || he.Status != tt.status {
				t.Errorf("kind = %v status = %d, want %v/%d", he.Kind, he.Status, tt.kind, tt.status)
			}
		})
	}
}

func TestIsNotFoundIsGated(t *testing.T) {
	if !IsNotFound(&Error{Kind: ErrNotFound}) {
		t.Error("IsNotFound(ErrNotFound) = false")
	}
	if IsNotFound(errors.New("plain")) {
		t.Error("IsNotFound(plain error) = true")
	}
	if !IsGated(&Error{Kind: ErrGated}) {
		t.Error("IsGated(ErrGated) = false")
	}
}

func TestNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	c := clientAt(t, srv, "")
	srv.Close() // now unreachable → transport error
	_, err := c.Tree(context.Background(), "org/repo", "main")
	if err == nil {
		t.Fatal("expected a network error")
	}
	if !IsNotFound(err) && kindOf(err) != ErrNetwork {
		t.Errorf("kind = %v, want ErrNetwork", kindOf(err))
	}
}

func TestMalformedJSON(t *testing.T) {
	srv, _ := treeHandler(t, `not json`, http.StatusOK)
	c := clientAt(t, srv, "")
	if _, err := c.Tree(context.Background(), "org/repo", "main"); err == nil {
		t.Error("malformed JSON must error")
	}
}

func TestRepoMetadata(t *testing.T) {
	body := `{"id": "Qwen/Qwen3-32B-GGUF", "sha": "abc", "downloads": 10, "likes": 2, "tags": ["gguf"]}`
	srv, getReq := treeHandler(t, body, http.StatusOK)
	c := clientAt(t, srv, "")
	m, err := c.Repo(context.Background(), "Qwen/Qwen3-32B-GGUF")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "Qwen/Qwen3-32B-GGUF" || m.SHA != "abc" || m.Downloads != 10 || m.Likes != 2 {
		t.Errorf("RepoMeta = %+v", m)
	}
	if len(m.Tags) != 1 || m.Tags[0] != "gguf" {
		t.Errorf("tags = %v", m.Tags)
	}
	if r := getReq(); r == nil || r.URL.Path != "/api/models/Qwen/Qwen3-32B-GGUF" {
		t.Errorf("URL = %v", r.URL)
	}
}

func TestRepoExists(t *testing.T) {
	// exists
	srv, _ := treeHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "")
	if ok, err := c.RepoExists(context.Background(), "org/repo"); err != nil || !ok {
		t.Errorf("RepoExists(existing) = %v, %v", ok, err)
	}

	// not found → false, nil error
	srv2, _ := treeHandler(t, `{}`, http.StatusNotFound)
	c2 := clientAt(t, srv2, "")
	if ok, err := c2.RepoExists(context.Background(), "org/repo"); err != nil || ok {
		t.Errorf("RepoExists(missing) = %v, %v; want false, nil", ok, err)
	}

	// gated → error surfaces (the repo exists)
	srv3, _ := treeHandler(t, `{}`, http.StatusForbidden)
	c3 := clientAt(t, srv3, "")
	if _, err := c3.RepoExists(context.Background(), "org/repo"); !IsGated(err) {
		t.Errorf("RepoExists(gated) error = %v, want gated", err)
	}
}

func TestNewReadsEnv(t *testing.T) {
	t.Setenv("HF_ENDPOINT", "https://mirror.example")
	t.Setenv("HF_TOKEN", "hf_tok123456789012345678901234567")
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if c.endpoint != "https://mirror.example" {
		t.Errorf("endpoint = %q, want HF_ENDPOINT", c.endpoint)
	}
	if c.token != "hf_tok123456789012345678901234567" {
		t.Errorf("token = %q, want HF_TOKEN", c.token)
	}
}

func TestDefaultEndpoint(t *testing.T) {
	c := NewWithEndpoint("", "")
	if c.endpoint != "https://huggingface.co" {
		t.Errorf("endpoint = %q, want default", c.endpoint)
	}
}
