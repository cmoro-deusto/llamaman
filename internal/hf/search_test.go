package hf

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func searchHandler(t *testing.T, body string, status int) (*httptest.Server, func() *http.Request) {
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

func TestSearchQueryAssembly(t *testing.T) {
	tests := []struct {
		name string
		opts SearchOpts
		want url.Values
	}{
		{
			name: "defaults",
			opts: SearchOpts{},
			want: url.Values{
				"filter":    {"gguf"},
				"direction": {"-1"},
				"limit":     {"50"},
			},
		},
		{
			name: "query and filter tags",
			opts: SearchOpts{
				Query:  "llama 3",
				Limit:  25,
				Sort:   "likes",
				Filter: []string{"ja", "license:apache-2.0"},
			},
			want: url.Values{
				"search":    {"llama 3"},
				"filter":    {"gguf,ja,license:apache-2.0"},
				"sort":      {"likes"},
				"direction": {"-1"},
				"limit":     {"25"},
			},
		},
		{
			name: "empty filter tags skipped, ascending kept",
			opts: SearchOpts{
				Direction: 1,
				Filter:    []string{"", "ja"},
			},
			want: url.Values{
				"filter":    {"gguf,ja"},
				"direction": {"1"},
				"limit":     {"50"},
			},
		},
		{
			name: "query escapes plus and slash",
			opts: SearchOpts{Query: "qwen 3+ultra/gguf"},
			want: url.Values{
				"filter":    {"gguf"},
				"search":    {"qwen 3+ultra/gguf"},
				"direction": {"-1"},
				"limit":     {"50"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchQuery(tt.opts)
			parsed, err := url.ParseQuery(got)
			if err != nil {
				t.Fatalf("searchQuery returned invalid query %q: %v", got, err)
			}
			for k, wantVals := range tt.want {
				gotVals := parsed[k]
				if strings.Join(gotVals, ",") != strings.Join(wantVals, ",") {
					t.Errorf("param %s = %v, want %v (raw query %q)", k, gotVals, wantVals, got)
				}
			}
			// The server decodes %2C/%3A back to separators (verified
			// live), so a round trip through the query string must
			// reproduce the exact filter.
			if f := parsed.Get("filter"); f != tt.want.Get("filter") {
				t.Errorf("filter round trip = %q, want %q (raw %q)", f, tt.want.Get("filter"), got)
			}
		})
	}
}

func TestSearchParsesResults(t *testing.T) {
	body := `[
		{
			"id": "org/one", "downloads": 743450, "likes": 17,
			"tags": ["gguf", "en", "ja", "license:llama3.1", "base_model:meta-llama/Llama-3.1-8B-Instruct"],
			"pipeline_tag": "text-generation", "createdAt": "2025-01-02T11:48:03.000Z",
			"modelId": "org/one", "_id": "abc"
		},
		{"id": "org/two"}
	]`
	srv, getReq := searchHandler(t, body, http.StatusOK)
	c := clientAt(t, srv, "")

	results, err := c.Search(context.Background(), SearchOpts{Query: "llama", Filter: []string{"ja"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("Search() = %d results, want 2: %+v", len(results), results)
	}
	r := results[0]
	if r.ID != "org/one" || r.Downloads != 743450 || r.Likes != 17 {
		t.Errorf("result fields = %+v", r)
	}
	if r.PipelineTag != "text-generation" {
		t.Errorf("pipeline_tag = %q", r.PipelineTag)
	}
	if len(r.Tags) != 5 || r.Tags[2] != "ja" {
		t.Errorf("tags = %v", r.Tags)
	}
	// Absent fields → zero values.
	if r2 := results[1]; r2.ID != "org/two" || r2.Downloads != 0 || r2.Likes != 0 || r2.PipelineTag != "" || r2.Tags != nil {
		t.Errorf("sparse result = %+v", r2)
	}

	rq := getReq()
	if rq == nil {
		t.Fatal("no request captured")
	}
	if rq.URL.Path != "/api/models" {
		t.Errorf("path = %q, want /api/models", rq.URL.Path)
	}
	q := rq.URL.Query()
	if q.Get("search") != "llama" || q.Get("filter") != "gguf,ja" {
		t.Errorf("query = %v", q)
	}
	if strings.Contains(rq.URL.RawQuery, "full=") {
		t.Error("full=true must not be requested")
	}
	if rq.Header.Get("Authorization") != "" {
		t.Error("no token set, Authorization must be absent")
	}
}

func TestSearchBearerToken(t *testing.T) {
	srv, getReq := searchHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "hf_secret12345678901234567890")
	if _, err := c.Search(context.Background(), SearchOpts{}); err != nil {
		t.Fatal(err)
	}
	if r := getReq(); r == nil || r.Header.Get("Authorization") != "Bearer hf_secret12345678901234567890" {
		t.Errorf("Authorization = %q, want Bearer header", r.Header.Get("Authorization"))
	}
}

func TestSearchErrors(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		kind   ErrorKind
	}{
		{name: "404", status: http.StatusNotFound, kind: ErrNotFound},
		{name: "401", status: http.StatusUnauthorized, kind: ErrGated},
		{name: "500", status: http.StatusInternalServerError, kind: ErrHTTP},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := searchHandler(t, `{}`, tt.status)
			c := clientAt(t, srv, "")
			_, err := c.Search(context.Background(), SearchOpts{})
			if err == nil {
				t.Fatal("expected an error")
			}
			var he *Error
			if !errors.As(err, &he) || he.Kind != tt.kind {
				t.Errorf("kind = %v, want %v", err, tt.kind)
			}
		})
	}
}

func TestSearchNetworkError(t *testing.T) {
	srv, _ := searchHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "")
	srv.Close() // now unreachable → transport error
	if _, err := c.Search(context.Background(), SearchOpts{}); err == nil || kindOf(err) != ErrNetwork {
		t.Errorf("kind = %v, want ErrNetwork", kindOf(err))
	}
}

func TestSearchEmptyAndMalformed(t *testing.T) {
	srv, _ := searchHandler(t, `[]`, http.StatusOK)
	c := clientAt(t, srv, "")
	res, err := c.Search(context.Background(), SearchOpts{})
	if err != nil || len(res) != 0 {
		t.Errorf("empty list: %v %v", res, err)
	}

	srv2, _ := searchHandler(t, `not json`, http.StatusOK)
	c2 := clientAt(t, srv2, "")
	if _, err := c2.Search(context.Background(), SearchOpts{}); err == nil {
		t.Error("malformed JSON must error")
	}
}
