package llamaapi

import (
	"context"
	"errors"
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

// ---- FetchMetrics tests ----

const sampleMetricsBody = `# HELP llamacpp:tokens_predicted_total Total number of tokens predicted.
# TYPE llamacpp:tokens_predicted_total counter
llamacpp:tokens_predicted_total 1234.0
# HELP llamacpp:tokens_predicted_seconds_total Predicted tokens elapsed seconds total.
# TYPE llamacpp:tokens_predicted_seconds_total counter
llamacpp:tokens_predicted_seconds_total 12.5
llamacpp:prompt_tokens_total 4321
llamacpp:prompt_seconds_total 1.5
llamacpp:predicted_tokens_seconds 60.5
llamacpp:prompt_tokens_seconds 2300
llamacpp:requests_processing 2
llamacpp:requests_deferred 1
some_other_metric{label="x"} 99
`

func TestFetchMetricsHappy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(sampleMetricsBody))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchMetrics(context.Background())
	if err != nil {
		t.Fatalf("FetchMetrics: %v", err)
	}
	cases := []struct {
		name string
		got  float64
		want float64
	}{
		{"TokensPredictedTotal", got.TokensPredictedTotal, 1234},
		{"TokensPredictedSecondsTotal", got.TokensPredictedSecondsTotal, 12.5},
		{"PromptTokensTotal", got.PromptTokensTotal, 4321},
		{"PromptSecondsTotal", got.PromptSecondsTotal, 1.5},
		{"PredictedTokensSecondsAvg", got.PredictedTokensSecondsAvg, 60.5},
		{"PromptTokensSecondsAvg", got.PromptTokensSecondsAvg, 2300},
		{"RequestsProcessing", got.RequestsProcessing, 2},
		{"RequestsDeferred", got.RequestsDeferred, 1},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestFetchMetrics404IsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).FetchMetrics(context.Background())
	if !errors.Is(err, ErrMetricsNotEnabled) {
		t.Fatalf("expected ErrMetricsNotEnabled sentinel; got %v", err)
	}
}

// TestFetchMetrics501IsSentinel covers older llama-server builds that
// return 501 (Not Implemented) for /metrics even when --metrics is in
// the argv. The endpoint exists but isn't functional — same treatment
// as 404: stop polling, show n/a.
func TestFetchMetrics501IsSentinel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer srv.Close()

	_, err := clientFor(t, srv).FetchMetrics(context.Background())
	if !errors.Is(err, ErrMetricsNotEnabled) {
		t.Fatalf("expected ErrMetricsNotEnabled sentinel for 501; got %v", err)
	}
}

func TestFetchMetrics5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := clientFor(t, srv).FetchMetrics(context.Background()); err == nil {
		t.Fatal("expected error for 5xx")
	}
}

// TestFetchMetricsAcceptsLegacyPromptSecondsAlias guards the
// `prompt_tokens_seconds_total` legacy name in case any older
// llama-server build (or fork) emits that variant. Current
// llama-server uses `prompt_seconds_total` (no `tokens_` infix).
func TestFetchMetricsAcceptsLegacyPromptSecondsAlias(t *testing.T) {
	body := `llamacpp:prompt_tokens_total 100
llamacpp:prompt_tokens_seconds_total 0.5
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchMetrics(context.Background())
	if err != nil {
		t.Fatalf("FetchMetrics: %v", err)
	}
	if got.PromptTokensTotal != 100 {
		t.Errorf("PromptTokensTotal = %v, want 100", got.PromptTokensTotal)
	}
	if got.PromptSecondsTotal != 0.5 {
		t.Errorf("PromptSecondsTotal = %v, want 0.5 (legacy alias should map)", got.PromptSecondsTotal)
	}
}

// TestFetchMetricsTolerantOfUnknownLines pins the fail-soft contract:
// unknown metric names and malformed lines should be skipped, not
// propagate as parse errors. A future llama-server adding new metrics
// must not break this client.
func TestFetchMetricsTolerantOfUnknownLines(t *testing.T) {
	body := `# garbage
llamacpp:tokens_predicted_total 100
this_is_not_a_valid_line
malformed not_a_number
llamacpp:requests_processing 3
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchMetrics(context.Background())
	if err != nil {
		t.Fatalf("FetchMetrics: %v", err)
	}
	if got.TokensPredictedTotal != 100 || got.RequestsProcessing != 3 {
		t.Errorf("known lines lost: got %+v", got)
	}
}

// ---- FetchSlots tests ----

func TestFetchSlotsHappyIsProcessing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/slots" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`[{"is_processing":true},{"is_processing":false},{"is_processing":true},{"is_processing":false}]`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchSlots(context.Background())
	if err != nil {
		t.Fatalf("FetchSlots: %v", err)
	}
	if got.Total != 4 {
		t.Errorf("Total = %d, want 4", got.Total)
	}
	if got.BusyCount != 2 {
		t.Errorf("BusyCount = %d, want 2", got.BusyCount)
	}
}

// TestFetchSlotsLegacyStateField covers older llama-server builds
// that exposed busy-ness via a `state` enum (0 = idle) instead of
// the `is_processing` boolean. The client tolerates both.
func TestFetchSlotsLegacyStateField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"state":0},{"state":1},{"state":2}]`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchSlots(context.Background())
	if err != nil {
		t.Fatalf("FetchSlots: %v", err)
	}
	if got.Total != 3 {
		t.Errorf("Total = %d, want 3", got.Total)
	}
	if got.BusyCount != 2 {
		t.Errorf("BusyCount = %d, want 2", got.BusyCount)
	}
}

func TestFetchSlots5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	if _, err := clientFor(t, srv).FetchSlots(context.Background()); err == nil {
		t.Fatal("expected error for 5xx")
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
