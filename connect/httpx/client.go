// Package httpx provides an HTTP client and server built on rig endpoints.
//
// In tests, construct from a resolved environment endpoint:
//
//	client := httpx.New(env.Endpoint("api"))
//	resp, err := client.Get("/health")
//
// In service code, construct from parsed wiring:
//
//	w, _ := connect.ParseWiring(ctx)
//	client := httpx.New(w.Egress("api"))
package httpx

import (
	"io"
	"net/http"
	"net/url"

	"github.com/matgreaves/rig/connect"
)

// Client is an HTTP client that prepends a base URL to all request paths.
type Client struct {
	// BaseURL is prepended to all request paths (e.g. "http://127.0.0.1:8080").
	// Must not have a trailing slash.
	BaseURL string

	// HTTP is the underlying http.Client. If nil, http.DefaultClient is used.
	HTTP *http.Client
}

// New creates an HTTP client from a resolved endpoint.
func New(ep connect.Endpoint) *Client {
	return &Client{BaseURL: "http://" + ep.Addr()}
}

// NewClient creates an HTTP client for the given base URL string.
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL}
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// Get sends a GET request to BaseURL + path.
func (c *Client) Get(path string) (*http.Response, error) {
	return c.httpClient().Get(c.BaseURL + path)
}

// Head sends a HEAD request to BaseURL + path.
func (c *Client) Head(path string) (*http.Response, error) {
	return c.httpClient().Head(c.BaseURL + path)
}

// Post sends a POST request to BaseURL + path.
func (c *Client) Post(path, contentType string, body io.Reader) (*http.Response, error) {
	return c.httpClient().Post(c.BaseURL+path, contentType, body)
}

// Do sends an HTTP request. If the request URL has no host (i.e. is a
// relative path like "/orders/1"), it is resolved against BaseURL.
// Absolute URLs are sent as-is.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.URL.Host == "" {
		base, err := url.Parse(c.BaseURL)
		if err != nil {
			return nil, err
		}
		req.URL = base.ResolveReference(req.URL)
	}
	return c.httpClient().Do(req)
}
