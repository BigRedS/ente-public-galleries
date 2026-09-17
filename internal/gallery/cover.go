package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
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
	// StateDir is where each album's coverState marker is written, named
	// <collectionID>.json. It should be a cache directory (deletable with
	// no consequence beyond a resync), not the public site output: unlike
	// the cover image itself, this marker is derived bookkeeping, not a
	// site asset.
	StateDir string
}

// coverState is what Sync persists to StateDir after a successful fetch, so
// a later run can tell whether the album has changed since without relying
// on the cover file's own mtime.
//
// Both fields are checked because Ente's UpdationTime is bumped only by a
// file insert into the collection (see Discover's doc comment in
// discovery.go), not by a magic-metadata edit - and choosing a different
// cover photo is exactly such an edit (it lives in pubMagicMetadata.coverID,
// see Album.CoverID). MetadataVersion is what actually changes when that
// happens, so relying on UpdationTime alone would silently miss a cover
// swap forever.
type coverState struct {
	UpdationTime    int64 `json:"updationTime"`
	MetadataVersion int   `json:"metadataVersion"`
}

func readCoverState(path string) (coverState, error) {
	var state coverState
	body, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(body, &state); err != nil {
		return state, err
	}
	return state, nil
}

func writeCoverState(path string, state coverState) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o644)
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
// Freshness is judged against a coverState marker recorded in StateDir on
// the last successful write, not the cover file's own mtime: a plain mtime
// comparison against UpdationTime cannot detect a cover-photo change (see
// coverState's doc comment), and needs a marker that actually reflects the
// state present in the file, rather than a timestamp coincidence.
func (c *Covers) Sync(ctx context.Context, album Album, index *FileIndex) error {
	coverID, ok := pickCover(album, index)
	if !ok {
		return fmt.Errorf("album %q has no files to pick a cover from", album.Name)
	}

	path := filepath.Join(c.Dir, fmt.Sprintf("%d.jpg", album.ID))
	statePath := filepath.Join(c.StateDir, fmt.Sprintf("%d.json", album.ID))
	want := coverState{UpdationTime: album.UpdationTime, MetadataVersion: album.MetadataVersion}

	if _, err := os.Stat(path); err == nil {
		if have, err := readCoverState(statePath); err == nil && have == want {
			return nil
		}
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

	if err := os.MkdirAll(c.StateDir, 0o755); err != nil {
		return fmt.Errorf("creating cover state directory: %w", err)
	}
	if err := writeCoverState(statePath, want); err != nil {
		return fmt.Errorf("writing %s: %w", statePath, err)
	}
	return nil
}
