// Package enteapi is a minimal client for the Ente ("museum") HTTP API,
// covering only the account-authenticated endpoints this tool needs.
//
// It deliberately does not use the /public-collection/* endpoints. Those are
// what a browser hits when someone opens a public album link, and two of them
// (/info and /diff) consume a slot in the link's device limit, keyed on the
// caller's IP and User-Agent. Reading album data through the account API
// instead means generating the site never eats into the quota that real
// visitors need.
package enteapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultEndpoint is Ente's hosted API. Note the .com: the web apps live on
// ente.io but the API is served from ente.com, and pointing at the wrong one
// fails in confusing ways.
const DefaultEndpoint = "https://api.ente.com"

// clientPackage identifies us to the server, which uses the X-Client-Package
// prefix to decide which app is calling. We must claim to be Photos: anything
// else is routed to the Auth or Locker app and will not serve collections.
const clientPackage = "io.ente.photos"

// maxErrorBody caps how much of a failed response we keep for the error
// message. Museum returns compact JSON errors, so this is generous.
const maxErrorBody = 8 << 10

// Client talks to one Ente server, optionally as one authenticated user.
//
// The zero value is not usable; call New. A Client is safe for concurrent use
// once Token has been set, but SetToken itself is not concurrency-safe, so do
// all authentication before fanning out.
type Client struct {
	http      *http.Client
	endpoint  string
	token     string
	userAgent string
}

// New returns a Client for endpoint, which may be empty to mean
// DefaultEndpoint. Any trailing slash is trimmed so path joining stays simple.
func New(endpoint, userAgent string) *Client {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	return &Client{
		// No redirect policy override: museum's file endpoints answer with
		// 307s to presigned storage URLs and we want those followed.
		http: &http.Client{Timeout: 60 * time.Second},

		endpoint:  strings.TrimRight(endpoint, "/"),
		userAgent: userAgent,
	}
}

// SetToken installs the API token returned by a successful login. The value is
// the base64url encoding of the decrypted token bytes, which is what museum
// expects in X-Auth-Token.
func (c *Client) SetToken(token string) { c.token = token }

// Endpoint reports the server this client talks to, for logging and for
// deciding whether a cached session belongs to the configured server.
func (c *Client) Endpoint() string { return c.endpoint }

// Error is a non-2xx response from the API.
type Error struct {
	StatusCode int
	Method     string
	Path       string
	Body       string
}

func (e *Error) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// StatusCode returns the HTTP status carried by err, or 0 if err is not an
// API error. Callers branch on this for expected conditions, such as the 404
// from /users/srp/attributes that means "this account uses email OTP".
func StatusCode(err error) int {
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// Get issues a GET and decodes a JSON response into out, which may be nil to
// discard the body.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// Post issues a POST with a JSON body and decodes a JSON response into out.
// Both body and out may be nil.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

// Delete issues a DELETE and decodes a JSON response into out, which may be
// nil for endpoints that answer with an empty body.
func (c *Client) Delete(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil, out)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding %s %s request: %w", method, path, err)
		}
		reader = bytes.NewReader(encoded)
	}

	target := c.endpoint + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("building %s %s request: %w", method, path, err)
	}

	// Never set Origin. Museum's public-link middleware treats an absent
	// Origin as trusted but rejects unrecognised ones, and Go's client
	// omits it unless asked, so the safe thing is simply not to touch it.
	req.Header.Set("X-Client-Package", clientPackage)
	req.Header.Set("Accept", "application/json")
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("X-Auth-Token", c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return &Error{
			StatusCode: resp.StatusCode,
			Method:     method,
			Path:       path,
			Body:       strings.TrimSpace(string(snippet)),
		}
	}

	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding %s %s response: %w", method, path, err)
	}
	return nil
}
