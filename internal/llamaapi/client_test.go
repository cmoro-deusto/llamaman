package llamaapi

import (
	"context"
	"encoding/json"
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
llamacpp:n_tokens_max 8192
llamacpp:n_decode_total 42
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
		{"NTokensMax", got.NTokensMax, 8192},
		{"NDecodeTotal", got.NDecodeTotal, 42},
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

// TestFetchSlotsContextUsage covers the context usage fields extracted
// from /slots: n_prompt_tokens, n_decoded (from next_token), n_ctx,
// n_prompt_tokens_cache, n_prompt_tokens_processed, and breakdown fields.
func TestFetchSlotsContextUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"is_processing":true,"n_ctx":8192,"n_prompt_tokens":100,"n_prompt_tokens_processed":75,"n_prompt_tokens_cache":80,"next_token":[{"n_decoded":50,"n_remain":16000}]},{"is_processing":false,"n_ctx":8192,"n_prompt_tokens":0,"n_prompt_tokens_processed":0,"n_prompt_tokens_cache":0,"next_token":[]}]`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchSlots(context.Background())
	if err != nil {
		t.Fatalf("FetchSlots: %v", err)
	}
	if got.Total != 2 {
		t.Errorf("Total = %d, want 2", got.Total)
	}
	if got.BusyCount != 1 {
		t.Errorf("BusyCount = %d, want 1", got.BusyCount)
	}
	if got.ContextUsed != 150 { // 100 prompt + 50 decoded
		t.Errorf("ContextUsed = %d, want 150", got.ContextUsed)
	}
	if got.ContextMax != 8192 {
		t.Errorf("ContextMax = %d, want 8192", got.ContextMax)
	}
	if got.ContextCacheHits != 80 {
		t.Errorf("ContextCacheHits = %d, want 80", got.ContextCacheHits)
	}
	if got.ContextPromptTokens != 100 {
		t.Errorf("ContextPromptTokens = %d, want 100", got.ContextPromptTokens)
	}
	if got.ContextGenTokens != 50 {
		t.Errorf("ContextGenTokens = %d, want 50", got.ContextGenTokens)
	}
	if got.GenDecoded != 50 {
		t.Errorf("GenDecoded = %d, want 50", got.GenDecoded)
	}
	if got.GenRemain != 16000 {
		t.Errorf("GenRemain = %d, want 16000", got.GenRemain)
	}
	if got.PromptTokensTotal != 100 {
		t.Errorf("PromptTokensTotal = %d, want 100", got.PromptTokensTotal)
	}
	if got.PromptTokensProcessed != 75 {
		t.Errorf("PromptTokensProcessed = %d, want 75", got.PromptTokensProcessed)
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

func TestFetchModelsStatusValue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"m:fast","object":"model","owned_by":"llamacpp","status":{"value":"loaded","args":["/opt/llama-server","--ctx-size","65535","--jinja"]}},
			{"id":"m:slow","object":"model","owned_by":"llamacpp","status":{"value":"unloaded"}}
		]}`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchModels(context.Background())
	if err != nil {
		t.Fatalf("FetchModels: %v", err)
	}
	if len(got.Data) != 2 {
		t.Fatalf("data = %d, want 2", len(got.Data))
	}
	if got.Data[0].ID != "m:fast" || got.Data[0].Status.Value != "loaded" {
		t.Errorf("data[0] = %+v", got.Data[0])
	}
	if len(got.Data[0].Status.Args) != 4 || got.Data[0].Status.Args[1] != "--ctx-size" {
		t.Errorf("data[0] args = %v", got.Data[0].Status.Args)
	}
	if got.Data[1].Status.Value != "unloaded" {
		t.Errorf("data[1] status = %q, want unloaded", got.Data[1].Status.Value)
	}
}

func TestFetchSlotsForQueriesModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/slots" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("model"); got != "m:a:b" {
			t.Errorf("model query = %q, want m:a:b (url-escaped)", got)
		}
		if got := r.URL.Query().Get("autoload"); got != "false" {
			t.Errorf("autoload query = %q, want false (stats polling must never load)", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"is_processing":true,"n_ctx":65536,"n_prompt_tokens":4222,"n_prompt_tokens_processed":4181,"n_prompt_tokens_cache":0,"next_token":[{"n_decoded":41,"n_remain":100}]}]`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchSlotsFor(context.Background(), "m:a:b")
	if err != nil {
		t.Fatalf("FetchSlotsFor: %v", err)
	}
	if got.BusyCount != 1 || got.Total != 1 {
		t.Errorf("busy/total = %d/%d, want 1/1", got.BusyCount, got.Total)
	}
	if got.ContextUsed != 4263 || got.ContextMax != 65536 { // 4222 prompt + 41 decoded
		t.Errorf("context = %d/%d, want 4263/65536", got.ContextUsed, got.ContextMax)
	}
}

func TestFetchMetricsForQueriesModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("model"); got != "m:a:b" {
			t.Errorf("model query = %q, want m:a:b (url-escaped)", got)
		}
		if got := r.URL.Query().Get("autoload"); got != "false" {
			t.Errorf("autoload query = %q, want false (stats polling must never load)", got)
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(`# TYPE llamacpp:predicted_tokens_seconds gauge
llamacpp:predicted_tokens_seconds 12.3
# TYPE llamacpp:prompt_tokens_seconds gauge
llamacpp:prompt_tokens_seconds 500
`))
	}))
	defer srv.Close()

	got, err := clientFor(t, srv).FetchMetricsFor(context.Background(), "m:a:b")
	if err != nil {
		t.Fatalf("FetchMetricsFor: %v", err)
	}
	if got.PredictedTokensSecondsAvg != 12.3 || got.PromptTokensSecondsAvg != 500 {
		t.Errorf("averages = %v/%v, want 12.3/500", got.PredictedTokensSecondsAvg, got.PromptTokensSecondsAvg)
	}
}

func TestLoadModelAndUnloadModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models/load":
			if r.Method != http.MethodPost {
				t.Errorf("load method = %s", r.Method)
			}
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "m:a" {
				t.Errorf("load body = %v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		case "/models/unload":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["model"] != "m:b" {
				t.Errorf("unload body = %v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"success":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := clientFor(t, srv)
	if err := c.LoadModel(context.Background(), "m:a"); err != nil {
		t.Errorf("LoadModel: %v", err)
	}
	if err := c.UnloadModel(context.Background(), "m:b"); err != nil {
		t.Errorf("UnloadModel: %v", err)
	}
}

func TestModelActionErrorParsesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":400,"message":"model is not running","type":"invalid_request_error"}}`))
	}))
	defer srv.Close()

	err := clientFor(t, srv).UnloadModel(context.Background(), "m:x")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "model is not running") {
		t.Errorf("error = %q, want parsed server message", err)
	}
}
