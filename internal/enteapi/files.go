package enteapi

import (
	"context"
	"net/url"
	"strconv"
)

// File is one file's metadata as the server stores it.
//
// As with collections, the interesting fields are ciphertext; see
// internal/gallery for the decryption side. The FileAttributes triple are
// secretstream payloads with their headers: the file's bytes, its thumbnail,
// and its metadata JSON.
type File struct {
	ID                 int64          `json:"id"`
	OwnerID            int64          `json:"ownerID"`
	CollectionID       int64          `json:"collectionID"`
	CollectionOwnerID  *int64         `json:"collectionOwnerID"`
	EncryptedKey       string         `json:"encryptedKey"`
	KeyDecryptionNonce string         `json:"keyDecryptionNonce"`
	File               FileAttributes `json:"file"`
	Thumbnail          FileAttributes `json:"thumbnail"`
	Metadata           FileAttributes `json:"metadata"`
	IsDeleted          bool           `json:"isDeleted"`
	UpdationTime       int64          `json:"updationTime"`
	MagicMetadata      *MagicMetadata `json:"magicMetadata,omitempty"`
	// PubicMagicMetadata is spelt that way because the server's JSON field
	// is; it is not this file's typo to fix. Changing the tag would
	// silently stop public metadata (captions, coordinate edits) arriving.
	PubicMagicMetadata *MagicMetadata `json:"pubMagicMetadata,omitempty"`
	Info               *FileInfo      `json:"info,omitempty"`
}

// FileAttributes is a secretstream-encrypted blob plus its header, both
// base64. Used for file bytes, thumbnails, and metadata.
type FileAttributes struct {
	EncryptedData    string `json:"encryptedData,omitempty"`
	DecryptionHeader string `json:"decryptionHeader"`
}

// FileInfo carries sizes, used to know what a thumbnail decrypt should yield.
type FileInfo struct {
	FileSize      int64 `json:"fileSize,omitempty"`
	ThumbnailSize int64 `json:"thumbSize,omitempty"`
}

// IsRemovedFromAlbum reports whether this diff entry means "gone": either a
// real deletion, or the server's convention of a literal "-" in place of the
// encrypted file data for a file that was removed from this album but lives on
// elsewhere. Both must drop the file from an album's index.
func (f File) IsRemovedFromAlbum() bool {
	return f.IsDeleted || f.File.EncryptedData == "-"
}

// GetFiles walks one collection's diff since sinceTime (microseconds; zero
// fetches everything), returning one page. Follow hasMore until false.
//
// Entries include removals, distinguished by IsRemovedFromAlbum; a caller
// accumulating an index must apply them as deletions, not additions.
func (c *Client) GetFiles(ctx context.Context, collectionID, sinceTime int64) ([]File, bool, error) {
	var res struct {
		Files   []File `json:"diff"`
		HasMore bool   `json:"hasMore"`
	}
	if err := c.Get(ctx, "/collections/v2/diff", url.Values{
		"collectionID": {strconv.FormatInt(collectionID, 10)},
		"sinceTime":    {strconv.FormatInt(sinceTime, 10)},
	}, &res); err != nil {
		return nil, false, err
	}
	return res.Files, res.HasMore, nil
}
