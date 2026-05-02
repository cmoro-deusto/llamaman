package llamaapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func clientFor(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()
	host, portStr, ok := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	if !ok {
		t.Fatalf("could not split %q into host:port", srv.URL)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port %q: %v", portStr, err)
	}
	return NewClient(host, port)
}

func TestFetchPropsHappy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":4096}}`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchProps(context.Background())
	if err != nil {
		t.Fatalf("FetchProps: %v", err)
	}
	if got.DefaultGenerationSettings.NCtx != 4096 {
		t.Errorf("n_ctx = %d, want 4096", got.DefaultGenerationSettings.NCtx)
	}
}

func TestFetchPropsMissingSection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"some_other_field":1}`))
	}))
	defer srv.Close()

	if _, err := clientFor(t, srv).FetchProps(context.Background()); err == nil {
		t.Fatal("expected error for missing default_generation_settings")
	}
}

func TestFetchPropsNCtxAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"default_generation_settings":{}}`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchProps(context.Background())
	if err != nil {
		t.Fatalf("FetchProps: %v", err)
	}
	if got.DefaultGenerationSettings.NCtx != 0 {
		t.Errorf("n_ctx = %d, want 0", got.DefaultGenerationSettings.NCtx)
	}
}

func TestFetchProps5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := clientFor(t, srv).FetchProps(context.Background()); err == nil {
		t.Fatal("expected error for 5xx")
	}
}

func TestFetchPropsMalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	if _, err := clientFor(t, srv).FetchProps(context.Background()); err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestFetchPropsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":1}}`))
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := clientFor(t, srv).FetchProps(ctx); err == nil {
		t.Fatal("expected context-deadline error")
	}
}

func TestNormalizeHost(t *testing.T) {
	cases := []struct{ in, want string }{
		{"0.0.0.0", "127.0.0.1"},
		{"::", "::1"},
		{"[::]", "::1"},
		{"", "127.0.0.1"},
		{"localhost", "localhost"},
		{"127.0.0.1", "127.0.0.1"},
		{"10.0.0.5", "10.0.0.5"},
		{"::1", "::1"},
	}
	for _, c := range cases {
		if got := normalizeHost(c.in); got != c.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewClientBaseURL(t *testing.T) {
	cases := []struct {
		host string
		port int
		want string
	}{
		{"0.0.0.0", 8080, "http://127.0.0.1:8080"},
		{"127.0.0.1", 8080, "http://127.0.0.1:8080"},
		{"::", 9090, "http://[::1]:9090"},
		{"localhost", 8000, "http://localhost:8000"},
	}
	for _, c := range cases {
		c := c
		t.Run(c.host, func(t *testing.T) {
			got := NewClient(c.host, c.port)
			if got.base != c.want {
				t.Errorf("base = %q, want %q", got.base, c.want)
			}
		})
	}
}
