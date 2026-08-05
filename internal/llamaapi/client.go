// Package llamaapi is a thin HTTP client for the running llama-server's
// administrative endpoints (/props, /metrics, /slots). It exists so
// the TUI doesn't import net/http and the server-supervision package
// doesn't have to grow API-client concerns alongside process lifecycle.
package llamaapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Props is the minimal projection of llama-server's /props response that
// llamaman cares about. Additional fields can be added incrementally.
type Props struct {
	DefaultGenerationSettings struct {
		NCtx int `json:"n_ctx"`
	} `json:"default_generation_settings"`
}

// Client is a llama-server HTTP client. Construct with NewClient so the
// dial address is normalized for unspecified bind addresses.
type Client struct {
	base string
	http *http.Client
}

// httpTimeout caps both connect and read+body for any single request.
// Localhost-bound calls finish in low single-digit milliseconds; 2s is
// a generous safety net for a busy server.
const httpTimeout = 2 * time.Second

// NewClient returns a Client whose base URL points at host:port,
// normalizing the unspecified bind addresses (0.0.0.0 → 127.0.0.1,
// :: → ::1) to a routable destination. Pass-through for hostnames and
// non-zero literals.
func NewClient(host string, port int) *Client {
	dialHost := normalizeHost(host)
	addr := net.JoinHostPort(dialHost, strconv.Itoa(port))
	return &Client{
		base: "http://" + addr,
		http: &http.Client{Timeout: httpTimeout},
	}
}

// normalizeHost rewrites the unspecified bind addresses to their
// loopback equivalents. Pass-through for everything else, including
// bracketed IPv6 literals already in destination form.
func normalizeHost(h string) string {
	switch h {
	case "", "0.0.0.0":
		return "127.0.0.1"
	case "::", "[::]":
		return "::1"
	}
	return h
}

// Metrics is the projection of llama-server's /metrics
// (Prometheus text-exposition) output that the run-mode header cares
// about. The four counters are sampled by the caller across two ticks
// to derive instantaneous rates; the gauges are lifetime values
// llama-server already maintains.
//
// All fields are zero when /metrics didn't include the corresponding
// line, which is fine — the renderer's now/avg display tolerates
// zeros (a fresh server has not generated any tokens yet).
type Metrics struct {
	// Counters (lifetime monotonic).
	TokensPredictedTotal        float64 // llamacpp:tokens_predicted_total
	TokensPredictedSecondsTotal float64 // llamacpp:tokens_predicted_seconds_total
	PromptTokensTotal           float64 // llamacpp:prompt_tokens_total
	PromptSecondsTotal          float64 // llamacpp:prompt_seconds_total (note: NO "tokens_" infix; llama-server names it asymmetrically vs. tokens_predicted_seconds_total)

	// Gauges (lifetime running averages reported by the server).
	PredictedTokensSecondsAvg float64 // llamacpp:predicted_tokens_seconds
	PromptTokensSecondsAvg    float64 // llamacpp:prompt_tokens_seconds
	RequestsProcessing        float64 // llamacpp:requests_processing
	RequestsDeferred          float64 // llamacpp:requests_deferred (queued)
	NTokensMax                float64 // llamacpp:n_tokens_max (peak tokens in a single decode)
	NDecodeTotal              float64 // llamacpp:n_decode_total (lifetime request count)
}

// Slots is the projection of /slots that the header needs. /slots
// returns an array of per-slot objects; we extract busy count, total
// slots, context usage metrics, and generation progress for the
// active slot.
type Slots struct {
	BusyCount             int
	Total                 int
	ContextUsed           int // tokens currently in context (prompt + generated)
	ContextMax            int // total context window size (n_ctx)
	ContextCacheHits      int // prompt tokens served from cache
	ContextPromptTokens   int // prompt tokens in context (for breakdown bar)
	ContextGenTokens      int // generated tokens in context (for breakdown bar)
	GenDecoded            int // tokens generated so far in current response
	GenRemain             int // tokens remaining before generation limit
	PromptTokensTotal     int // total prompt tokens for current request
	PromptTokensProcessed int // prompt tokens processed so far (for progress bar)
}

// ErrMetricsNotEnabled is returned by FetchMetrics when llama-server
// responds with 404 (endpoint absent) or 501 (endpoint present but not
// implemented — older builds returned this even with `--metrics`).
// Callers should stop polling /metrics on this error and surface `n/a`
// for derived rates.
var ErrMetricsNotEnabled = errors.New("llamaapi: /metrics endpoint not available")

// FetchMetrics GETs /metrics and parses the Prometheus text-exposition
// body. We only handle the small set of `llamacpp:*` lines we care
// about so we don't pull in a full Prometheus parser dependency.
//
// On a 404 response, returns ErrMetricsNotEnabled (sentinel) so the
// caller can stop polling. Other non-2xx responses + transport errors
// surface as a generic error.
func (c *Client) FetchMetrics(ctx context.Context) (*Metrics, error) {
	return c.fetchMetrics(ctx, "")
}

// FetchMetricsFor GETs /metrics?model=<id> — the router-mode variant,
// which requires the model name and reports that model's metrics only.
func (c *Client) FetchMetricsFor(ctx context.Context, model string) (*Metrics, error) {
	return c.fetchMetrics(ctx, model)
}

func (c *Client) fetchMetrics(ctx context.Context, model string) (*Metrics, error) {
	u := c.base + "/metrics"
	if model != "" {
		// autoload=false: a stats poll must never load a model (see
		// fetchSlots).
		u += "?model=" + url.QueryEscape(model) + "&autoload=false"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: GET /metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNotImplemented {
		return nil, ErrMetricsNotEnabled
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llamaapi: GET /metrics: status %d", resp.StatusCode)
	}
	return parseMetrics(resp.Body)
}

// parseMetrics walks the Prometheus text exposition line-by-line.
// Skips comments (`# HELP`, `# TYPE`); for value lines it splits on
// the last whitespace run so a metric name with `{labels}` still
// parses cleanly. Unknown llamacpp:* names are ignored fail-soft.
func parseMetrics(body interface{ Read([]byte) (int, error) }) (*Metrics, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 4096), 1<<20)
	m := &Metrics{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Last whitespace-separated token is the value; everything
		// before is the metric name (possibly with {labels}).
		idx := strings.LastIndexAny(line, " \t")
		if idx <= 0 {
			continue
		}
		nameWithLabels := strings.TrimSpace(line[:idx])
		valueStr := strings.TrimSpace(line[idx+1:])
		// Strip {labels} since llamacpp metrics don't differentiate
		// by label combination for our purposes.
		name := nameWithLabels
		if brace := strings.IndexByte(nameWithLabels, '{'); brace >= 0 {
			name = nameWithLabels[:brace]
		}
		v, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			continue
		}
		switch name {
		case "llamacpp:tokens_predicted_total":
			m.TokensPredictedTotal = v
		case "llamacpp:tokens_predicted_seconds_total":
			m.TokensPredictedSecondsTotal = v
		case "llamacpp:prompt_tokens_total":
			m.PromptTokensTotal = v
		case "llamacpp:prompt_seconds_total", "llamacpp:prompt_tokens_seconds_total":
			// llama-server emits `prompt_seconds_total`. The
			// `prompt_tokens_seconds_total` alias is kept as a
			// fallback for older builds that may have used it.
			m.PromptSecondsTotal = v
		case "llamacpp:predicted_tokens_seconds":
			m.PredictedTokensSecondsAvg = v
		case "llamacpp:prompt_tokens_seconds":
			m.PromptTokensSecondsAvg = v
		case "llamacpp:requests_processing":
			m.RequestsProcessing = v
		case "llamacpp:requests_deferred":
			m.RequestsDeferred = v
		case "llamacpp:n_tokens_max":
			m.NTokensMax = v
		case "llamacpp:n_decode_total":
			m.NDecodeTotal = v
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("llamaapi: scan /metrics: %w", err)
	}
	return m, nil
}

// LoadModel POSTs /models/load {"model": id} — the router-mode load
// action. The router returns before the child is ready; the model then
// transitions through loading → loaded as observed via GET /models.
func (c *Client) LoadModel(ctx context.Context, model string) error {
	return c.postModelAction(ctx, "/models/load", model)
}

// UnloadModel POSTs /models/unload {"model": id} — the router-mode
// unload action. In-flight work drains up to the preset's stop-timeout
// before the child is killed.
func (c *Client) UnloadModel(ctx context.Context, model string) error {
	return c.postModelAction(ctx, "/models/unload", model)
}

// postModelAction POSTs a router model action and returns a readable
// error (parsed from the OpenAI-style {"error":{"message":...}} body)
// on non-2xx responses.
func (c *Client) postModelAction(ctx context.Context, path, model string) error {
	body, err := json.Marshal(map[string]string{"model": model})
	if err != nil {
		return fmt.Errorf("llamaapi: %s: %w", path, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("llamaapi: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("llamaapi: POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		return nil
	}
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	msg := e.Error.Message
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	return fmt.Errorf("llamaapi: POST %s: %s (status %d)", path, msg, resp.StatusCode)
}

// FetchSlots GETs /slots and extracts busy count, total slots, and
// context usage metrics. The response is a JSON array of per-slot
// objects; llama-server has shipped two flavors of the busy field
// over time (`is_processing` boolean today, `state` enum on older
// builds), so we tolerate both.
//
// Context usage is aggregated across slots: ContextUsed sums
// n_prompt_tokens + n_decoded (tokens generated so far), ContextMax
// takes the max n_ctx across slots, and ContextCacheHits sums
// n_prompt_tokens_cache.
func (c *Client) FetchSlots(ctx context.Context) (*Slots, error) {
	return c.fetchSlots(ctx, "")
}

// FetchSlotsFor GETs /slots?model=<id> — the router-mode variant, which
// requires the model name and reports that model's slots only.
func (c *Client) FetchSlotsFor(ctx context.Context, model string) (*Slots, error) {
	return c.fetchSlots(ctx, model)
}

func (c *Client) fetchSlots(ctx context.Context, model string) (*Slots, error) {
	u := c.base + "/slots"
	if model != "" {
		// autoload=false: a stats poll must never load a model (the
		// router would otherwise reload an unloaded model every second).
		u += "?model=" + url.QueryEscape(model) + "&autoload=false"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: GET /slots: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llamaapi: GET /slots: status %d", resp.StatusCode)
	}
	var raw []struct {
		IsProcessing           bool        `json:"is_processing"`
		State                  json.Number `json:"state"`
		NCtx                   int         `json:"n_ctx"`
		NPromptTokens          int         `json:"n_prompt_tokens"`
		NPromptTokensProcessed int         `json:"n_prompt_tokens_processed"`
		NPromptTokensCache     int         `json:"n_prompt_tokens_cache"`
		NextToken              []struct {
			NDecoded int `json:"n_decoded"`
			NRemain  int `json:"n_remain"`
		} `json:"next_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("llamaapi: decode /slots: %w", err)
	}
	out := &Slots{Total: len(raw)}
	for _, s := range raw {
		switch {
		case s.IsProcessing:
			out.BusyCount++
		case s.State != "" && s.State != "0":
			// older builds: state != 0 == busy
			out.BusyCount++
		}
		// Context usage: prompt tokens + tokens generated so far
		decoded := 0
		remain := 0
		for _, nt := range s.NextToken {
			decoded += nt.NDecoded
			remain += nt.NRemain
		}
		out.ContextUsed += s.NPromptTokens + decoded
		out.ContextPromptTokens += s.NPromptTokens
		out.ContextGenTokens += decoded
		if s.NCtx > out.ContextMax {
			out.ContextMax = s.NCtx
		}
		out.ContextCacheHits += s.NPromptTokensCache
		out.GenDecoded += decoded
		out.GenRemain += remain
		out.PromptTokensTotal += s.NPromptTokens
		out.PromptTokensProcessed += s.NPromptTokensProcessed
	}
	return out, nil
}

// FetchProps GETs /props. Returns the parsed Props on 2xx with valid
// JSON. Errors on transport failure, non-2xx status, malformed body, or
// missing default_generation_settings. A response with the section
// present but n_ctx absent returns Props{NCtx: 0} and no error;
// callers treat 0 as "unavailable".
func (c *Client) FetchProps(ctx context.Context) (*Props, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/props", nil)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: GET /props: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llamaapi: GET /props: status %d", resp.StatusCode)
	}
	// Two-stage decode so we can distinguish "default_generation_settings
	// is missing" from "it's present but n_ctx is missing/zero". The
	// former is a server we don't recognize; the latter is a server that
	// hasn't reported a value yet.
	var raw struct {
		DefaultGenerationSettings *json.RawMessage `json:"default_generation_settings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("llamaapi: decode /props: %w", err)
	}
	if raw.DefaultGenerationSettings == nil {
		return nil, fmt.Errorf("llamaapi: /props missing default_generation_settings")
	}
	var p Props
	if err := json.Unmarshal(*raw.DefaultGenerationSettings, &p.DefaultGenerationSettings); err != nil {
		return nil, fmt.Errorf("llamaapi: decode default_generation_settings: %w", err)
	}
	return &p, nil
}

// ModelStatus is the per-model load state reported by the router in
// GET /models ("status.value": loaded / loading / unloaded). Args is
// the child llama-server's full command line while loaded.
type ModelStatus struct {
	Value string   `json:"value"`
	Args  []string `json:"args"`
}

// ModelInfo is one entry of llama-server's GET /models response
// (OpenAI-style object list). Router mode lists every model registered
// in the --models-preset file, plus models from --models-dir and the
// HF download cache. Source distinguishes them ("preset" vs "cache");
// older builds report the same distinction as in_cache. Cache-only
// models can be filtered out of the models panel.
type ModelInfo struct {
	ID      string      `json:"id"`
	Object  string      `json:"object"`
	OwnedBy string      `json:"owned_by"`
	Source  string      `json:"source"`
	InCache bool        `json:"in_cache"`
	Status  ModelStatus `json:"status"`
	Meta    struct {
		NCtx int `json:"n_ctx"`
	} `json:"meta"`
}

// IsCache reports whether the model is a cache-only leftover (HF
// download) rather than an ini/models-dir entry. Accepts both the
// current "source":"cache" field and the older in_cache flag.
func (m ModelInfo) IsCache() bool {
	return m.Source == "cache" || m.InCache
}

// Models is the envelope of GET /models: {"object": "list", "data": [...]}.
type Models struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// FetchModels GETs /models and returns the model list.
func (c *Client) FetchModels(ctx context.Context) (*Models, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: GET /models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llamaapi: GET /models: status %d", resp.StatusCode)
	}
	var m Models
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		return nil, fmt.Errorf("llamaapi: decode /models: %w", err)
	}
	return &m, nil
}

// Health is the projection of GET /health. Router mode lists the
// currently loaded model ids; non-router servers return a single
// element list.
type Health struct {
	Status string   `json:"status"`
	Models []string `json:"models"`
}

// FetchHealth GETs /health.
func (c *Client) FetchHealth(ctx context.Context) (*Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/health", nil)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: GET /health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("llamaapi: GET /health: status %d", resp.StatusCode)
	}
	var h Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return nil, fmt.Errorf("llamaapi: decode /health: %w", err)
	}
	return &h, nil
}
