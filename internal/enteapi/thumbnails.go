package enteapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// maxThumbnailBytes caps a thumbnail download. Real thumbnails are tens of
// kilobytes; this exists so a misbehaving or hijacked URL cannot wedge the
// tool reading an unbounded stream into memory.
const maxThumbnailBytes = 64 << 20

// GetFile fetches one file's metadata by ID, within a collection.
//
// The file's wrapped key and thumbnail header live here. They are fetched on
// demand rather than cached, because they are secrets and the cache is
// deliberately inert; one call per album cover is not worth changing that.
func (c *Client) GetFile(ctx context.Context, collectionID, fileID int64) (*File, error) {
	var res struct {
		File File `json:"file"`
	}
	if err := c.Get(ctx, "/collections/file", url.Values{
		"collectionID": {fmt.Sprint(collectionID)},
		"fileID":       {fmt.Sprint(fileID)},
	}, &res); err != nil {
		return nil, err
	}
	return &res.File, nil
}

// GetThumbnail returns one file's encrypted thumbnail bytes.
//
// It tries the current endpoint first, which answers with a short-lived
// presigned storage URL that is then fetched plainly (the signature is the
// authentication; no token goes to storage). Servers from before that
// endpoint, and self-hosted ones mid-migration, answer 404, and the legacy
// path is used instead: the API itself 307-redirects to storage, with the
// token in the query, and the redirect is followed for us.
func (c *Client) GetThumbnail(ctx context.Context, fileID int64) ([]byte, error) {
	var res struct {
		URL string `json:"url"`
	}
	err := c.Get(ctx, fmt.Sprintf("/files/thumbnail/v3/%d", fileID), nil, &res)
	if err == nil {
		if res.URL == "" {
			return nil, fmt.Errorf("thumbnail endpoint returned an empty URL for file %d", fileID)
		}
		return c.FetchBytes(ctx, res.URL)
	}
	if StatusCode(err) == http.StatusNotFound {
		legacy := fmt.Sprintf("%s/files/preview/%d?token=%s",
			c.endpoint, fileID, url.QueryEscape(c.token))
		return c.FetchBytes(ctx, legacy)
	}
	return nil, err
}

// FetchBytes GETs an absolute URL and returns its body. No authentication
// headers are attached: it exists for presigned storage URLs and legacy
// redirect endpoints, where the URL itself carries whatever access is needed.
func (c *Client) FetchBytes(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("building request for %s: %w", target, err)
	}
	if c.userAgent != "" {
		req.Header.Set("User-Agent", c.userAgent)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", target, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, &Error{StatusCode: resp.StatusCode, Method: http.MethodGet, Path: target}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxThumbnailBytes))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", target, err)
	}
	return body, nil
}
