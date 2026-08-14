package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// apiClient is a thin, schema-blind client for the REST API a Manifest
// describes. It decodes into map[string]any rather than typed structs,
// because the browser is uncurated and has no per-table type to decode into —
// the same reason the templates render a grid rather than a curated form.
type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func newAPIClient(baseURL, token string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// apiError carries a response's status so a caller can act on it — a 401
// means the token is stale, a 403 or 404 is a row the caller's own
// credentials cannot reach. This is the inherited scoping ADR-0053's revision
// names: the browser shows exactly what the token could already fetch.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("api: %d: %s", e.Status, e.Body) }

func (c *apiClient) do(ctx context.Context, method, path string, query url.Values) (*http.Response, error) {
	u := c.baseURL + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")
	return c.http.Do(req)
}

// listResult mirrors rest.Page[T]'s wire shape (rest/list.go), read as
// map[string]any rows since the browser has no generated type to decode into.
type listResult struct {
	Items      []map[string]any `json:"items"`
	Page       int              `json:"page"`
	PerPage    int              `json:"per_page"`
	HasMore    bool             `json:"has_more"`
	NextCursor *string          `json:"next_cursor,omitempty"`
	Total      *int64           `json:"total,omitempty"`
}

// List calls GET path with query and decodes a rest.Page[T] body.
func (c *apiClient) List(ctx context.Context, path string, query url.Values) (*listResult, error) {
	resp, err := c.do(ctx, http.MethodGet, path, query)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &apiError{Status: resp.StatusCode, Body: string(b)}
	}
	var lr listResult
	if err := json.Unmarshal(b, &lr); err != nil {
		return nil, fmt.Errorf("studio: decoding list response from %s: %w", path, err)
	}
	return &lr, nil
}

// Get calls GET path and decodes a single row body.
func (c *apiClient) Get(ctx context.Context, path string) (map[string]any, error) {
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &apiError{Status: resp.StatusCode, Body: string(b)}
	}
	var row map[string]any
	if err := json.Unmarshal(b, &row); err != nil {
		return nil, fmt.Errorf("studio: decoding row from %s: %w", path, err)
	}
	return row, nil
}
