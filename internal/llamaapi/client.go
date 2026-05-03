// Package llamaapi is a thin HTTP client for the running llama-server's
// administrative endpoints (/props, /metrics, /slots). It exists so
// the TUI doesn't import net/http and the server-supervision package
// doesn't have to grow API-client concerns alongside process lifecycle.
package llamaapi

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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
// to derive instantaneous rates; the four gauges are the lifetime
// values llama-server already maintains.
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
}

// Slots is the projection of /slots that the header needs: total slot
// count and how many are currently processing. /slots returns an array
// of slot objects whose other fields we ignore for now.
type Slots struct {
	BusyCount int
	Total     int
}

// ErrMetricsNotEnabled is returned by FetchMetrics when llama-server
// responds with 404 — i.e. the server was launched without `--metrics`
// (or the equivalent preset key). Callers should stop polling /metrics
// on this error and surface `n/a` for derived rates.
var ErrMetricsNotEnabled = errors.New("llamaapi: /metrics endpoint not enabled (--metrics off)")

// FetchMetrics GETs /metrics and parses the Prometheus text-exposition
// body. We only handle the small set of `llamacpp:*` lines we care
// about so we don't pull in a full Prometheus parser dependency.
//
// On a 404 response, returns ErrMetricsNotEnabled (sentinel) so the
// caller can stop polling. Other non-2xx responses + transport errors
// surface as a generic error.
func (c *Client) FetchMetrics(ctx context.Context) (*Metrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/metrics", nil)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llamaapi: GET /metrics: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
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
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("llamaapi: scan /metrics: %w", err)
	}
	return m, nil
}

// FetchSlots GETs /slots and counts which slots are currently
// processing. The response is a JSON array of per-slot objects;
// llama-server has shipped two flavors of the busy field over time
// (`is_processing` boolean today, `state` enum on older builds), so
// we tolerate both.
func (c *Client) FetchSlots(ctx context.Context) (*Slots, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/slots", nil)
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
		IsProcessing bool        `json:"is_processing"`
		State        json.Number `json:"state"`
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
