package gallery

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// FileSummary is what the index needs to know about one file: enough to pick
// a cover, order a timeline, and place a dot on a map.
//
// What is deliberately absent matters more than what is here: no file key, no
// decryption headers, nothing that could decrypt anything. The cache holding
// these is plain JSON on disk, and it must stay inert without the account.
type FileSummary struct {
	ID int64 `json:"id"`
	// CreationTime is microseconds since the epoch, as the decrypted
	// metadata reports it (honouring an editedTime override).
	CreationTime int64 `json:"creationTime"`
	// Lat and Lon are present only when the file is geotagged. Ente treats
	// 0,0 as "stripped", so a genuine null-island photo is lost here too;
	// that is upstream's convention, not ours to improve.
	Lat *float64 `json:"lat,omitempty"`
	Lon *float64 `json:"lon,omitempty"`
}

// FileIndex is the per-album cache: everything known about an album's files,
// plus the marks that make the next run cheap.
type FileIndex struct {
	// UpdationTime is the collection updationTime this index was built
	// under. A mismatch with the server's current value is what triggers
	// a re-walk.
	UpdationTime int64 `json:"updationTime"`
	// LastSyncTime is the sinceTime a future diff should resume from.
	LastSyncTime int64 `json:"lastSyncTime"`
	// Files is keyed by file ID so diff upserts and deletions are O(1).
	Files map[int64]FileSummary `json:"files"`
}

// FileFetcher is the slice of the API the sync needs. The real Client
// satisfies it; tests supply fixtures.
type FileFetcher interface {
	GetFiles(ctx context.Context, collectionID, sinceTime int64) ([]enteapi.File, bool, error)
}

// Syncer accumulates per-album file indexes, backed by an on-disk cache.
type Syncer struct {
	Files    FileFetcher
	CacheDir string
}

// SyncAlbum returns the album's file index, walking the diff only when the
// album has changed since the cache was written.
//
// A file's key is unwrapped from the album key in memory, used to decrypt its
// metadata, and then dropped: the key never reaches the cache. Re-deriving it
// on a later run is one secretbox open, which is not worth persisting a
// secret to avoid.
func (s *Syncer) SyncAlbum(ctx context.Context, album Album) (*FileIndex, error) {
	path := s.albumCachePath(album.ID)

	index := loadFileIndex(path)
	if index != nil && index.UpdationTime == album.UpdationTime {
		// Nothing has changed in this album since the last walk.
		return index, nil
	}
	if index == nil {
		index = &FileIndex{Files: map[int64]FileSummary{}}
	}

	sinceTime := index.LastSyncTime
	for {
		files, hasMore, err := s.Files.GetFiles(ctx, album.ID, sinceTime)
		if err != nil {
			return nil, fmt.Errorf("fetching files for album %q: %w", album.Name, err)
		}

		for _, f := range files {
			if f.UpdationTime > sinceTime {
				sinceTime = f.UpdationTime
			}
			if f.IsRemovedFromAlbum() {
				delete(index.Files, f.ID)
				continue
			}
			summary, err := summariseFile(f, album.Key)
			if err != nil {
				// A file that will not decrypt is recorded and
				// skipped rather than failing the album: one
				// corrupt file should not cost the whole index.
				fmt.Fprintf(os.Stderr, "warning: album %q file %d: %v\n", album.Name, f.ID, err)
				continue
			}
			index.Files[f.ID] = *summary
		}

		if !hasMore {
			// The page is exhausted, so jump the resume mark to the
			// album's own updation time: server-side, any later
			// change to the album's files moves that too.
			sinceTime = album.UpdationTime
			break
		}
	}

	index.UpdationTime = album.UpdationTime
	index.LastSyncTime = sinceTime

	if err := saveFileIndex(path, index); err != nil {
		return nil, err
	}
	return index, nil
}

// summariseFile decrypts one file's metadata with the album key and distils
// it to a FileSummary.
func summariseFile(f enteapi.File, albumKey []byte) (*FileSummary, error) {
	fileKey, err := crypto.SecretBoxOpenBase64(f.EncryptedKey, f.KeyDecryptionNonce, albumKey)
	if err != nil {
		return nil, fmt.Errorf("unwraping file key: %w", err)
	}

	// Metadata is the secretstream blob in f.Metadata; the key that opens it
	// is the file's, not the album's.
	var metadata map[string]any
	if f.Metadata.EncryptedData != "" {
		_, plaintext, err := crypto.DecryptChaChaBase64(f.Metadata.EncryptedData, fileKey, f.Metadata.DecryptionHeader)
		if err != nil {
			return nil, fmt.Errorf("decrypting metadata: %w", err)
		}
		if err := json.Unmarshal(plaintext, &metadata); err != nil {
			return nil, fmt.Errorf("parsing metadata: %w", err)
		}
	}

	// Public magic metadata can override the coordinates (and the creation
	// time, via edits).
	var publicMeta map[string]any
	if f.PubicMagicMetadata != nil {
		_, plaintext, err := crypto.DecryptChaChaBase64(f.PubicMagicMetadata.Data, fileKey, f.PubicMagicMetadata.Header)
		if err != nil {
			return nil, fmt.Errorf("decrypting public metadata: %w", err)
		}
		if err := json.Unmarshal(plaintext, &publicMeta); err != nil {
			return nil, fmt.Errorf("parsing public metadata: %w", err)
		}
	}

	summary := &FileSummary{ID: f.ID}

	// editedTime wins when present, mirroring Ente's own clients.
	if v, ok := metaInt(publicMeta, "editedTime"); ok && v != 0 {
		summary.CreationTime = v
	} else if v, ok := metaInt(metadata, "creationTime"); ok {
		summary.CreationTime = v
	}

	// Coordinates prefer the public override. A 0,0 override means the
	// user stripped the location, which outranks whatever EXIF still says;
	// this is Ente's own convention, mirrored exactly.
	if lat, ok := metaFloat(publicMeta, "lat"); ok {
		if lon, ok := metaFloat(publicMeta, "long"); ok {
			if lat != 0 || lon != 0 {
				summary.Lat = &lat
				summary.Lon = &lon
			}
			return summary, nil
		}
	}
	// EXIF coordinates are trusted as-is except for 0,0, which we drop:
	// a photo genuinely at the intersection of the equator and the prime
	// meridian is vanishingly rare, while a corrupt EXIF fix claiming it
	// is not. (Ente's own clients do keep it; this is our deviation.)
	if lat, ok := metaFloat(metadata, "latitude"); ok && lat != 0 {
		if lon, ok := metaFloat(metadata, "longitude"); ok && lon != 0 {
			summary.Lat = &lat
			summary.Lon = &lon
		}
	}
	return summary, nil
}

func (s *Syncer) albumCachePath(collectionID int64) string {
	return filepath.Join(s.CacheDir, "albums", fmt.Sprintf("%d.json", collectionID))
}

func loadFileIndex(path string) *FileIndex {
	body, err := os.ReadFile(path)
	if err != nil {
		// A missing or unreadable cache is not an error: the worst
		// outcome is re-walking the album from scratch.
		return nil
	}
	var index FileIndex
	if err := json.Unmarshal(body, &index); err != nil {
		return nil
	}
	if index.Files == nil {
		index.Files = map[int64]FileSummary{}
	}
	return &index
}

func saveFileIndex(path string, index *FileIndex) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating cache directory: %w", err)
	}
	body, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding cache: %w", err)
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}
