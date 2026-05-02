// Package llamaapi is a thin HTTP client for the running llama-server's
// administrative endpoints (/props today, /metrics and /v1/models in
// future iterations). It exists so the TUI doesn't import net/http and
// the server-supervision package doesn't have to grow API-client
// concerns alongside process lifecycle.
package llamaapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
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
