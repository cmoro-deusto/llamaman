package hf

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// SearchOpts configures a model-search request (DESIGN §16.7).
type SearchOpts struct {
	Query     string   // "" = browse: top GGUF repos by Sort
	Limit     int      // 0 → 50
	Sort      string   // "downloads" | "likes" | "lastModified"; "" → downloads
	Direction int      // -1 desc (default), 1 asc
	Filter    []string // extra tags beyond the fixed "gguf", in order
	// (e.g. "ja", "license:apache-2.0") — language and license filters
}

// SearchResult is one hit of the model search endpoint. The search
// response carries no file sizes (verified: even full=true only adds
// rfilename siblings) — sizes come from a per-repo tree round trip.
type SearchResult struct {
	ID          string
	Downloads   int64
	Likes       int64
	Tags        []string // raw: "license:*", "base_model:*", language codes
	PipelineTag string
}

// Search queries the HF model search endpoint (GET /api/models, DESIGN
// §16.7): the fixed gguf library filter plus any extra tag filters,
// ranked and capped. Errors map through the typed Error kinds (404 →
// ErrNotFound, 401/403 → ErrGated, transport → ErrNetwork, other →
// ErrHTTP) and the Bearer-token rule applies unchanged.
func (c *Client) Search(ctx context.Context, opts SearchOpts) ([]SearchResult, error) {
	u := c.endpoint + "/api/models?" + searchQuery(opts)
	var raw []struct {
		ID          string   `json:"id"`
		Downloads   int64    `json:"downloads"`
		Likes       int64    `json:"likes"`
		Tags        []string `json:"tags"`
		PipelineTag string   `json:"pipeline_tag"`
	}
	if err := c.getJSON(ctx, u, &raw); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(raw))
	for _, m := range raw {
		out = append(out, SearchResult{
			ID:          m.ID,
			Downloads:   m.Downloads,
			Likes:       m.Likes,
			Tags:        m.Tags,
			PipelineTag: m.PipelineTag,
		})
	}
	return out, nil
}

// searchQuery assembles the query string. filter always leads with the
// fixed "gguf" library filter; extra Filter tags append in order. The
// server decodes %2C/%3A back to separators (verified live), so
// url.Values.Encode() is safe.
func searchQuery(opts SearchOpts) string {
	q := url.Values{}
	q.Set("filter", joinFilters(opts.Filter))
	if opts.Query != "" {
		q.Set("search", opts.Query)
	}
	if opts.Sort != "" {
		q.Set("sort", opts.Sort)
	}
	dir := opts.Direction
	if dir != 1 && dir != -1 {
		dir = -1 // default: descending
	}
	q.Set("direction", strconv.Itoa(dir))
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q.Set("limit", strconv.Itoa(limit))
	return q.Encode()
}

// joinFilters comma-joins the fixed "gguf" tag with the extra tags.
// The value is left raw here — url.Values.Encode() escapes it exactly
// once (commas and colons → %2C/%3A), which the server decodes back
// to the separators (verified live).
func joinFilters(extra []string) string {
	parts := make([]string, 0, len(extra)+1)
	parts = append(parts, "gguf")
	for _, f := range extra {
		if f == "" {
			continue
		}
		parts = append(parts, f)
	}
	return strings.Join(parts, ",")
}
