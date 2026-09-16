package enteapi

import (
	"context"
	"net/url"
	"strconv"
)

// Collection is one album (or folder, or the pseudo-collections like
// favourites) as the server stores it.
//
// Most fields are ciphertext: the server stores names, keys and per-album
// metadata encrypted, and this type deliberately keeps them opaque. Turning
// them into usable values needs the account keys and lives in internal/gallery.
type Collection struct {
	ID int64 `json:"id"`
	// Owner identifies whose collection this is; for collections in the
	// user's library that are someone else's, the key is wrapped for us
	// rather than for the owner.
	Owner               CollectionUser   `json:"owner"`
	EncryptedKey        string           `json:"encryptedKey"`
	KeyDecryptionNonce  string           `json:"keyDecryptionNonce"`
	Name                string           `json:"name"`
	EncryptedName       string           `json:"encryptedName"`
	NameDecryptionNonce string           `json:"nameDecryptionNonce"`
	Type                string           `json:"type"`
	Sharees             []CollectionUser `json:"sharees"`
	PublicURLs          []PublicURL      `json:"publicURLs"`
	UpdationTime        int64            `json:"updationTime"`
	IsDeleted           bool             `json:"isDeleted"`

	MagicMetadata       *MagicMetadata `json:"magicMetadata,omitempty"`
	PublicMagicMetadata *MagicMetadata `json:"pubMagicMetadata,omitempty"`
}

// CollectionType values the server uses. Only albums are publishable
// galleries; the others are structural and never make sense on an index page.
const (
	TypeAlbum         = "album"
	TypeFolder        = "folder"
	TypeFavorites     = "favorites"
	TypeUncategorized = "uncategorized"
)

// CollectionUser is a participant in a collection.
type CollectionUser struct {
	ID    int64  `json:"id"`
	Email string `json:"email"`
	// Name is deprecated upstream and empty in practice.
	Name string `json:"name"`
	Role string `json:"role"`
}

// MagicMetadata is a small encrypted JSON blob attached to a collection or
// file. The three variants differ only in who may see them and which keys
// decrypt them, not in shape.
type MagicMetadata struct {
	Version int `json:"version"`
	Count   int `json:"count"`
	// Data and Header are base64: ciphertext and secretstream header.
	Data   string `json:"data"`
	Header string `json:"header"`
}

// PublicURL is a live sharing link for a collection.
//
// The URL here is what the server knows, which is without the collection key
// fragment. It is not the URL to hand to a visitor; see gallery.ShareLink.
type PublicURL struct {
	URL             string  `json:"url"`
	DeviceLimit     int     `json:"deviceLimit"`
	ValidTill       int64   `json:"validTill"`
	EnableDownload  bool    `json:"enableDownload"`
	EnableCollect   bool    `json:"enableCollect"`
	EnableComment   bool    `json:"enableComment"`
	PasswordEnabled bool    `json:"passwordEnabled"`
	Nonce           *string `json:"nonce,omitempty"`
	MemLimit        *int64  `json:"memLimit,omitempty"`
	OpsLimit        *int64  `json:"opsLimit,omitempty"`
	EnableJoin      bool    `json:"enableJoin"`
	MinRole         *string `json:"minRole,omitempty"`
}

// GetCollections returns the user's collections changed since sinceTime, in
// microseconds. Zero fetches everything, which is what a fresh run wants.
//
// The response includes collections shared with the user, not just those they
// own; callers must check Owner.
//
// There is no pagination on this endpoint, so a library of any realistic size
// arrives in one response.
func (c *Client) GetCollections(ctx context.Context, sinceTime int64) ([]Collection, error) {
	var res struct {
		Collections []Collection `json:"collections"`
	}
	if err := c.Get(ctx, "/collections/v2", url.Values{
		"sinceTime": {strconv.FormatInt(sinceTime, 10)},
	}, &res); err != nil {
		return nil, err
	}
	return res.Collections, nil
}
