package enteapi

import (
	"context"
	"fmt"
)

// LinkOptions tunes a public link at creation. The zero value is what
// Ente's own clients default to: no password, no expiry, no device limit,
// and uploading into the album by visitors disabled.
type LinkOptions struct {
	// EnableCollect lets visitors upload into the album.
	EnableCollect bool
	// EnableComment lets visitors comment.
	EnableComment bool
}

// CreatePublicLink makes an album reachable via a public link.
//
// The values sent are the web client's defaults: no expiry (validTill 0),
// no device limit (0, unlimited), and joining left unset so the server
// applies its own default of enabled. If the album already has an active
// link, the server returns the existing one rather than erroring, so this
// is safe to repeat - though callers should skip the call anyway, since a
// repeat means the earlier link is still live.
func (c *Client) CreatePublicLink(ctx context.Context, collectionID int64, opts LinkOptions) (PublicURL, error) {
	var res struct {
		Result PublicURL `json:"result"`
	}
	if err := c.Post(ctx, "/collections/share-url", map[string]any{
		"collectionID":  collectionID,
		"enableCollect": opts.EnableCollect,
		"enableComment": opts.EnableComment,
		"validTill":     0,
		"deviceLimit":   0,
	}, &res); err != nil {
		return PublicURL{}, fmt.Errorf("creating public link for collection %d: %w", collectionID, err)
	}
	return res.Result, nil
}

// DisablePublicLink removes an album's public link.
//
// The disable is server-side permanent for that token: a later
// CreatePublicLink mints a fresh token, so the URL visitors had before
// stops working and a new one takes its place. Callers that care should say
// so to the user.
func (c *Client) DisablePublicLink(ctx context.Context, collectionID int64) error {
	if err := c.Delete(ctx, fmt.Sprintf("/collections/share-url/%d", collectionID), nil); err != nil {
		return fmt.Errorf("disabling public link for collection %d: %w", collectionID, err)
	}
	return nil
}
