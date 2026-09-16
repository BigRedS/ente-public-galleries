package gallery

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/encoding"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// fixtureFileKey is the per-file key fixtures seal their metadata with.
var fixtureFileKey = bytes.Repeat([]byte{0x02}, 32)

// fakePage is one GetFiles response: the files, and whether the server says
// another page follows.
type fakePage struct {
	files   []enteapi.File
	hasMore bool
}

type fakeFileFetcher struct {
	pages []fakePage
	calls []struct {
		collectionID int64
		sinceTime    int64
	}
}

func (f *fakeFileFetcher) GetFiles(_ context.Context, collectionID, sinceTime int64) ([]enteapi.File, bool, error) {
	f.calls = append(f.calls, struct {
		collectionID int64
		sinceTime    int64
	}{collectionID, sinceTime})
	if len(f.calls) > len(f.pages) {
		// A walk that keeps asking past its pages gets an empty final
		// answer, like the real server gives.
		return nil, false, nil
	}
	page := f.pages[len(f.calls)-1]
	return page.files, page.hasMore, nil
}

// filePages wraps plain file slices as a single final page, for tests that do
// not exercise pagination.
func filePages(pages ...[]enteapi.File) []fakePage {
	out := make([]fakePage, len(pages))
	for i, files := range pages {
		out[i] = fakePage{files: files}
	}
	return out
}

// buildFile builds a diff entry whose metadata genuinely decrypts to meta and
// whose key genuinely unwraps with the album key.
func buildFile(t *testing.T, id int64, meta map[string]any, updationTime int64) enteapi.File {
	t.Helper()

	sealedKey, keyNonce := seal(fixtureFileKey, fixtureCollectionKey, byte(id))
	f := enteapi.File{
		ID:                 id,
		OwnerID:            fixtureUserID,
		CollectionID:       101,
		EncryptedKey:       sealedKey,
		KeyDecryptionNonce: keyNonce,
		UpdationTime:       updationTime,
	}

	plaintext, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshalling fixture metadata: %v", err)
	}
	data, header, err := crypto.EncryptChaCha20poly1305(plaintext, fixtureFileKey)
	if err != nil {
		t.Fatalf("encrypting fixture metadata: %v", err)
	}
	f.Metadata = enteapi.FileAttributes{
		EncryptedData:    encoding.EncodeBase64(data),
		DecryptionHeader: encoding.EncodeBase64(header),
	}
	f.Info = &enteapi.FileInfo{ThumbnailSize: 1234}
	return f
}

// buildRemoved builds the server's marker for a file that was removed from
// the album: a real file entry whose data field is a literal "-".
func buildRemoved(t *testing.T, id int64, updationTime int64) enteapi.File {
	t.Helper()
	f := buildFile(t, id, map[string]any{}, updationTime)
	f.File.EncryptedData = "-"
	return f
}

func fixtureAlbum(id int64, updationTime int64) Album {
	return Album{ID: id, Name: "Fixture Album", Key: fixtureCollectionKey, UpdationTime: updationTime}
}

func newSyncer(t *testing.T, fetcher FileFetcher) *Syncer {
	t.Helper()
	return &Syncer{Files: fetcher, CacheDir: t.TempDir()}
}

func fileMeta(ts int64, lat, lon *float64) map[string]any {
	m := map[string]any{"creationTime": float64(ts), "title": "IMG_0001.JPG"}
	if lat != nil && lon != nil {
		m["latitude"] = *lat
		m["longitude"] = *lon
	}
	return m
}

func floatPtr(v float64) *float64 { return &v }

func TestSyncAlbumDistilsMetadata(t *testing.T) {
	lat, lon := 51.5074, -0.1278
	files := []enteapi.File{
		buildFile(t, 1, fileMeta(1_700_000_000_000_000, &lat, &lon), 100),
		buildFile(t, 2, fileMeta(1_700_000_000_000_999, nil, nil), 101),
	}
	s := newSyncer(t, &fakeFileFetcher{pages: filePages(files)})

	index, err := s.SyncAlbum(context.Background(), fixtureAlbum(101, 500))
	if err != nil {
		t.Fatalf("SyncAlbum: %v", err)
	}

	if len(index.Files) != 2 {
		t.Fatalf("index has %d files, want 2: %+v", len(index.Files), index.Files)
	}

	geo := index.Files[1]
	if geo.Lat == nil || geo.Lon == nil || *geo.Lat != lat || *geo.Lon != lon {
		t.Errorf("file 1 coordinates = %+v, want %v/%v", geo, lat, lon)
	}
	if geo.CreationTime != 1_700_000_000_000_000 {
		t.Errorf("file 1 CreationTime = %d", geo.CreationTime)
	}

	plain := index.Files[2]
	if plain.Lat != nil || plain.Lon != nil {
		t.Errorf("file 2 has coordinates %+v, want none", plain)
	}
	if plain.CreationTime != 1_700_000_000_000_999 {
		t.Errorf("file 2 CreationTime = %d", plain.CreationTime)
	}

	if index.UpdationTime != 500 {
		t.Errorf("UpdationTime = %d, want 500", index.UpdationTime)
	}
	// The walk saw file times up to 101, but a finished walk jumps to the
	// album's own updation time so the next run resumes past all of it.
	if index.LastSyncTime != 500 {
		t.Errorf("LastSyncTime = %d, want 500", index.LastSyncTime)
	}
}

func TestSyncAlbumAppliesRemovals(t *testing.T) {
	files := []enteapi.File{
		buildFile(t, 1, fileMeta(1000, nil, nil), 100),
		buildFile(t, 2, fileMeta(1001, nil, nil), 101),
	}
	s := newSyncer(t, &fakeFileFetcher{pages: filePages(files)})

	album := fixtureAlbum(101, 500)
	index, err := s.SyncAlbum(context.Background(), album)
	if err != nil {
		t.Fatalf("first SyncAlbum: %v", err)
	}
	if len(index.Files) != 2 {
		t.Fatalf("index has %d files, want 2", len(index.Files))
	}

	// A later walk sees file 2 removed-from-album and file 3 added.
	later := []enteapi.File{
		buildRemoved(t, 2, 0),
		buildFile(t, 3, fileMeta(1002, nil, nil), 600),
	}
	s2 := &Syncer{Files: &fakeFileFetcher{pages: filePages(later)}, CacheDir: s.CacheDir}
	album.UpdationTime = 700
	index, err = s2.SyncAlbum(context.Background(), album)
	if err != nil {
		t.Fatalf("second SyncAlbum: %v", err)
	}

	if _, present := index.Files[2]; present {
		t.Error("file 2 still indexed after a removal entry")
	}
	if _, present := index.Files[1]; !present {
		t.Error("file 1 dropped from index when it had no diff entry")
	}
	if _, present := index.Files[3]; !present {
		t.Error("file 3 missing from index after being added")
	}
	if index.LastSyncTime != 700 {
		t.Errorf("LastSyncTime = %d, want 700", index.LastSyncTime)
	}
}

// An unchanged album must cost zero API calls: this is what keeps a cron run
// cheap on a large library.
func TestSyncAlbumSkipsUnchangedAlbums(t *testing.T) {
	fetcher := &fakeFileFetcher{pages: filePages([]enteapi.File{
		buildFile(t, 1, fileMeta(1000, nil, nil), 100),
	})}
	s := newSyncer(t, fetcher)

	album := fixtureAlbum(101, 500)
	if _, err := s.SyncAlbum(context.Background(), album); err != nil {
		t.Fatalf("first SyncAlbum: %v", err)
	}
	if len(fetcher.calls) != 1 {
		t.Fatalf("first walk made %d API calls, want 1", len(fetcher.calls))
	}

	if _, err := s.SyncAlbum(context.Background(), album); err != nil {
		t.Fatalf("second SyncAlbum: %v", err)
	}
	if len(fetcher.calls) != 1 {
		t.Errorf("unchanged album made another %d API calls, want 0", len(fetcher.calls)-1)
	}

	// And a changed album resumes from the last sync mark, not from zero.
	album.UpdationTime = 900
	if _, err := s.SyncAlbum(context.Background(), album); err != nil {
		t.Fatalf("third SyncAlbum: %v", err)
	}
	if got := fetcher.calls[len(fetcher.calls)-1].sinceTime; got != 500 {
		t.Errorf("resumed from sinceTime %d, want 500", got)
	}
}

// A multi-page walk must accumulate across pages and only resume where the
// last one ended.
func TestSyncAlbumFollowsPagination(t *testing.T) {
	fetcher := &fakeFileFetcher{pages: []fakePage{
		{files: []enteapi.File{buildFile(t, 1, fileMeta(1000, nil, nil), 100)}, hasMore: true},
		{files: []enteapi.File{buildFile(t, 2, fileMeta(1001, nil, nil), 101)}, hasMore: true},
		{files: []enteapi.File{buildFile(t, 3, fileMeta(1002, nil, nil), 102)}},
	}}
	index, err := newSyncer(t, fetcher).SyncAlbum(context.Background(), fixtureAlbum(101, 500))
	if err != nil {
		t.Fatalf("SyncAlbum: %v", err)
	}
	if len(index.Files) != 3 {
		t.Fatalf("index has %d files, want 3 across the pages", len(index.Files))
	}
	if len(fetcher.calls) != 3 {
		t.Errorf("walk made %d API calls, want 3", len(fetcher.calls))
	}
	// Page two must resume from page one's newest entry, not re-ask.
	if got := fetcher.calls[1].sinceTime; got != 100 {
		t.Errorf("second page asked with sinceTime %d, want 100", got)
	}
	if got := fetcher.calls[2].sinceTime; got != 101 {
		t.Errorf("third page asked with sinceTime %d, want 101", got)
	}
}

// The cache is plain JSON on disk with no protection, so it must contain no
// key material of any kind: no album key, no file key, no ciphertexts, no
// decryption headers.
func TestSyncAlbumCacheLeaksNoKeys(t *testing.T) {
	files := []enteapi.File{buildFile(t, 1, fileMeta(1000, nil, nil), 100)}
	s := newSyncer(t, &fakeFileFetcher{pages: filePages(files)})

	if _, err := s.SyncAlbum(context.Background(), fixtureAlbum(101, 500)); err != nil {
		t.Fatalf("SyncAlbum: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(s.CacheDir, "albums", "101.json"))
	if err != nil {
		t.Fatalf("reading cache: %v", err)
	}

	secrets := map[string]string{
		"album key":     base64.StdEncoding.EncodeToString(fixtureCollectionKey),
		"file key":      base64.StdEncoding.EncodeToString(fixtureFileKey),
		"ciphertext":    files[0].Metadata.EncryptedData,
		"stream header": files[0].Metadata.DecryptionHeader,
		"sealed key":    files[0].EncryptedKey,
	}
	for what, secret := range secrets {
		if strings.Contains(string(body), secret) {
			t.Errorf("cache contains the %s (%s...)", what, secret[:12])
		}
	}
}

// A corrupt cache is a cache, not a database: throwing it away and walking
// again must be automatic, because it is always rebuildable.
func TestSyncAlbumSurvivesCorruptCache(t *testing.T) {
	s := newSyncer(t, &fakeFileFetcher{pages: filePages([]enteapi.File{
		buildFile(t, 1, fileMeta(1000, nil, nil), 100),
	})})
	if err := os.MkdirAll(filepath.Join(s.CacheDir, "albums"), 0o700); err != nil {
		t.Fatalf("creating cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(s.CacheDir, "albums", "101.json"), []byte("not json"), 0o600); err != nil {
		t.Fatalf("writing corrupt cache: %v", err)
	}

	index, err := s.SyncAlbum(context.Background(), fixtureAlbum(101, 500))
	if err != nil {
		t.Fatalf("SyncAlbum over a corrupt cache: %v", err)
	}
	if len(index.Files) != 1 {
		t.Fatalf("index has %d files, want 1", len(index.Files))
	}
}

// One file that will not decrypt must not take the album with it; the rest of
// the index still gets built, and the failure is a warning.
func TestSyncAlbumToleratesUndecryptableFile(t *testing.T) {
	good := buildFile(t, 1, fileMeta(1000, nil, nil), 100)
	bad := buildFile(t, 2, fileMeta(1001, nil, nil), 101)
	// Corrupt the wrapped key so the unwrap fails.
	bad.EncryptedKey = encoding.EncodeBase64(bytes.Repeat([]byte{0xEE}, 48))

	index, err := newSyncer(t, &fakeFileFetcher{pages: filePages([]enteapi.File{good, bad})}).SyncAlbum(context.Background(), fixtureAlbum(101, 500))
	if err != nil {
		t.Fatalf("SyncAlbum: %v", err)
	}
	if len(index.Files) != 1 {
		t.Fatalf("index has %d files, want 1 (only the good one)", len(index.Files))
	}
	if _, present := index.Files[2]; present {
		t.Error("the undecryptable file was indexed anyway")
	}
}

func TestSummariseFilePublicOverridesWin(t *testing.T) {
	lat, lon := -33.8688, 151.2093
	exifLat, exifLon := 51.5074, -0.1278

	f := buildFile(t, 7, fileMeta(1_700_000_000_000_000, &exifLat, &exifLon), 100)

	// Attach public magic metadata that overrides both position and time.
	pubMeta := map[string]any{"lat": lat, "long": lon, "editedTime": float64(1_700_000_000_999_999)}
	mm, err := sealMeta(pubMeta, fixtureFileKey, 0x55)
	if err != nil {
		t.Fatalf("sealing public metadata: %v", err)
	}
	f.PubicMagicMetadata = mm

	summary, err := summariseFile(f, fixtureCollectionKey)
	if err != nil {
		t.Fatalf("summariseFile: %v", err)
	}
	if summary.Lat == nil || *summary.Lat != lat || *summary.Lon != lon {
		t.Errorf("coordinates = %+v, want the override %v/%v", summary, lat, lon)
	}
	if summary.CreationTime != 1_700_000_000_999_999 {
		t.Errorf("CreationTime = %d, want the editedTime override", summary.CreationTime)
	}
}

// A 0,0 public override means the location was stripped, and must beat EXIF.
func TestSummariseFileStrippedLocationBeatsEXIF(t *testing.T) {
	exifLat, exifLon := 51.5074, -0.1278
	f := buildFile(t, 8, fileMeta(1000, &exifLat, &exifLon), 100)

	zero := 0.0
	mm, err := sealMeta(map[string]any{"lat": zero, "long": zero}, fixtureFileKey, 0x66)
	if err != nil {
		t.Fatalf("sealing public metadata: %v", err)
	}
	f.PubicMagicMetadata = mm

	summary, err := summariseFile(f, fixtureCollectionKey)
	if err != nil {
		t.Fatalf("summariseFile: %v", err)
	}
	if summary.Lat != nil || summary.Lon != nil {
		t.Errorf("coordinates = %+v, want none: a stripped location must outrank EXIF", summary)
	}
}
