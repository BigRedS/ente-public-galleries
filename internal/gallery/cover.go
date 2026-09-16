package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// jpegMagic is what a successfully decrypted thumbnail must start with.
// Ente stores all thumbnails as JPEG whatever the original file was, so this
// doubles as an end-to-end check that the decryption chain worked: random
// bytes after a wrong key would not open with a JPEG marker.
var jpegMagic = []byte{0xFF, 0xD8}

// CoverFetcher is the slice of the API cover art needs.
type CoverFetcher interface {
	GetFile(ctx context.Context, collectionID, fileID int64) (*enteapi.File, error)
	GetThumbnail(ctx context.Context, fileID int64) ([]byte, error)
}

// Covers materialises each album's cover thumbnail on disk.
type Covers struct {
	Fetcher CoverFetcher
	// Dir is where covers are written, named <collectionID>.jpg.
	Dir string
}

// pickCover decides which file represents the album.
//
// The rule is Ente's own: an explicitly chosen cover (coverID) wins when it
// still exists; otherwise the album's first photo in its sort order, which is
// the oldest when the album is ascending and the newest when it is not.
func pickCover(album Album, index *FileIndex) (int64, bool) {
	if len(index.Files) == 0 {
		return 0, false
	}
	if album.CoverID != 0 {
		if _, ok := index.Files[album.CoverID]; ok {
			return album.CoverID, true
		}
		// A chosen cover that has since been deleted falls back to the
		// first photo rather than to nothing.
	}

	var best int64
	var bestTime int64
	first := true
	for _, f := range index.Files {
		better := first
		if !better {
			if album.Asc {
				// Ascending: the earliest photo is the first shown.
				better = f.CreationTime < bestTime || (f.CreationTime == bestTime && f.ID < best)
			} else {
				better = f.CreationTime > bestTime || (f.CreationTime == bestTime && f.ID > best)
			}
		}
		if better {
			best, bestTime = f.ID, f.CreationTime
			first = false
		}
	}
	return best, true
}

// Sync fetches and decrypts the album's cover thumbnail into Dir, unless it is
// already there and fresh.
//
// Freshness is the cover file's mtime against the album's updation time: an
// album untouched since the cover was written needs nothing. Clock skew
// between this machine and the server can at worst cause a needless
// refetch of one small thumbnail, which is the safe direction to fail in.
func (c *Covers) Sync(ctx context.Context, album Album, index *FileIndex) error {
	coverID, ok := pickCover(album, index)
	if !ok {
		return fmt.Errorf("album %q has no files to pick a cover from", album.Name)
	}

	path := filepath.Join(c.Dir, fmt.Sprintf("%d.jpg", album.ID))
	if info, err := os.Stat(path); err == nil && info.ModTime().UnixMicro() >= album.UpdationTime {
		return nil
	}

	// Key material is fetched per cover rather than cached: the file's
	// wrapped key and the thumbnail's header are secrets, and the cache is
	// built to hold none.
	file, err := c.Fetcher.GetFile(ctx, album.ID, coverID)
	if err != nil {
		return fmt.Errorf("album %q cover file %d: %w", album.Name, coverID, err)
	}
	if file.Thumbnail.DecryptionHeader == "" {
		return fmt.Errorf("album %q cover file %d has no thumbnail", album.Name, coverID)
	}

	encrypted, err := c.Fetcher.GetThumbnail(ctx, coverID)
	if err != nil {
		return fmt.Errorf("album %q cover download: %w", album.Name, err)
	}

	fileKey, err := crypto.SecretBoxOpenBase64(file.EncryptedKey, file.KeyDecryptionNonce, album.Key)
	if err != nil {
		return fmt.Errorf("album %q cover file %d: unwraping file key: %w", album.Name, coverID, err)
	}

	// The thumbnail arrives as raw bytes, and the vendored decryptor takes
	// base64 in; the round trip costs one copy of a small buffer and keeps
	// the vendored file untouched.
	_, plaintext, err := crypto.DecryptChaChaBase64(
		base64.StdEncoding.EncodeToString(encrypted),
		fileKey,
		file.Thumbnail.DecryptionHeader,
	)
	if err != nil {
		return fmt.Errorf("album %q cover decrypt: %w", album.Name, err)
	}
	if !bytes.HasPrefix(plaintext, jpegMagic) {
		return fmt.Errorf("album %q cover decrypted but is not a JPEG (starts %x); refusing to publish it", album.Name, plaintext[:2])
	}

	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return fmt.Errorf("creating thumbs directory: %w", err)
	}
	// Covers are public site assets; world-readable is the point.
	if err := os.WriteFile(path, plaintext, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}
