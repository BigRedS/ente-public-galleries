package gallery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/encoding"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// fakeCoverFetcher hands out file metadata and thumbnail bytes that were
// really encrypted with the fixture keys, so the decrypt path under test is
// the real one.
type fakeCoverFetcher struct {
	files      map[int64]enteapi.File // by file ID
	thumbnails map[int64][]byte       // encrypted bytes, by file ID
	gotFile    []int64
}

func (f *fakeCoverFetcher) GetFile(_ context.Context, _, fileID int64) (*enteapi.File, error) {
	f.gotFile = append(f.gotFile, fileID)
	file, ok := f.files[fileID]
	if !ok {
		return nil, errors.New("no such file")
	}
	return &file, nil
}

func (f *fakeCoverFetcher) GetThumbnail(_ context.Context, fileID int64) ([]byte, error) {
	enc, ok := f.thumbnails[fileID]
	if !ok {
		return nil, errors.New("no thumbnail")
	}
	return enc, nil
}

// fakeJPEG is a stand-in for a decrypted thumbnail: real magic bytes plus
// padding, so length checks mean something.
func fakeJPEG() []byte {
	return append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("thumbnail"), 40)...)
}

// buildCoverFile makes a GetFile-shaped entry: key wrapped for the album,
// and a thumbnail sealed with the file key. The sealed bytes are exactly what
// the fake server must serve, so they are read back out of the entry; sealing
// twice would not work, as each seal gets a fresh random header.
func buildCoverFile(t *testing.T, id int64, thumbnail []byte) (enteapi.File, []byte) {
	t.Helper()

	sealedKey, keyNonce := seal(fixtureFileKey, fixtureCollectionKey, 0x77)
	file := enteapi.File{
		ID:                 id,
		OwnerID:            fixtureUserID,
		CollectionID:       101,
		EncryptedKey:       sealedKey,
		KeyDecryptionNonce: keyNonce,
	}
	data, header, err := crypto.EncryptChaCha20poly1305(thumbnail, fixtureFileKey)
	if err != nil {
		t.Fatalf("encrypting fixture thumbnail: %v", err)
	}
	file.Thumbnail = enteapi.FileAttributes{
		EncryptedData:    encoding.EncodeBase64(data),
		DecryptionHeader: encoding.EncodeBase64(header),
	}
	return file, data
}

// coverFixture wires a fetcher for one album cover: the file entry and the
// bytes the server serves, sealed once, together.
func coverFixture(t *testing.T, id int64, thumbnail []byte) *fakeCoverFetcher {
	t.Helper()
	file, served := buildCoverFile(t, id, thumbnail)
	return &fakeCoverFetcher{
		files:      map[int64]enteapi.File{id: file},
		thumbnails: map[int64][]byte{id: served},
	}
}

func indexWith(files ...FileSummary) *FileIndex {
	index := &FileIndex{Files: map[int64]FileSummary{}}
	for _, f := range files {
		index.Files[f.ID] = f
	}
	return index
}

func TestPickCoverHonoursChosenCover(t *testing.T) {
	index := indexWith(
		FileSummary{ID: 1, CreationTime: 5000},
		FileSummary{ID: 2, CreationTime: 1000},
	)
	album := Album{CoverID: 2}

	if got, ok := pickCover(album, index); !ok || got != 2 {
		t.Errorf("pickCover = %d/%v, want 2", got, ok)
	}
}

func TestPickCoverFallsBackWhenChosenCoverIsGone(t *testing.T) {
	index := indexWith(FileSummary{ID: 1, CreationTime: 5000})
	album := Album{CoverID: 99} // deleted since

	if got, ok := pickCover(album, index); !ok || got != 1 {
		t.Errorf("pickCover = %d/%v, want fallback to 1", got, ok)
	}
}

func TestPickCoverFollowsSortOrder(t *testing.T) {
	index := indexWith(
		FileSummary{ID: 1, CreationTime: 1000},
		FileSummary{ID: 2, CreationTime: 5000},
		FileSummary{ID: 3, CreationTime: 3000},
	)

	// Descending (the default): the newest photo fronts the album.
	if got, _ := pickCover(Album{}, index); got != 2 {
		t.Errorf("descending pickCover = %d, want 2 (newest)", got)
	}
	// Ascending: the oldest.
	if got, _ := pickCover(Album{Asc: true}, index); got != 1 {
		t.Errorf("ascending pickCover = %d, want 1 (oldest)", got)
	}
}

func TestPickCoverIsDeterministicOnEqualTimes(t *testing.T) {
	index := indexWith(
		FileSummary{ID: 7, CreationTime: 1000},
		FileSummary{ID: 3, CreationTime: 1000},
	)
	if got, _ := pickCover(Album{}, index); got != 7 {
		t.Errorf("descending tie pickCover = %d, want 7 (higher ID)", got)
	}
	if got, _ := pickCover(Album{Asc: true}, index); got != 3 {
		t.Errorf("ascending tie pickCover = %d, want 3 (lower ID)", got)
	}
}

func TestPickCoverEmptyIndex(t *testing.T) {
	if _, ok := pickCover(Album{CoverID: 5}, indexWith()); ok {
		t.Error("pickCover on an empty index returned a cover")
	}
}

func TestCoversSyncDecryptsAndWritesJPEG(t *testing.T) {
	thumbnail := fakeJPEG()
	fetcher := coverFixture(t, 2, thumbnail)

	covers := &Covers{Fetcher: fetcher, Dir: filepath.Join(t.TempDir(), "thumbs")}
	album := fixtureAlbum(101, 500)
	index := indexWith(
		FileSummary{ID: 1, CreationTime: 1000},
		FileSummary{ID: 2, CreationTime: 5000},
	)

	if err := covers.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(covers.Dir, "101.jpg"))
	if err != nil {
		t.Fatalf("reading written cover: %v", err)
	}
	if !bytes.Equal(body, thumbnail) {
		t.Errorf("written cover differs from the decrypted thumbnail:\n got %x...\nwant %x...", body[:8], thumbnail[:8])
	}
	if info, err := os.Stat(filepath.Join(covers.Dir, "101.jpg")); err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("cover permissions = %v (err %v), want 0644 for a public asset", info, err)
	}

	// A second sync of the same album must fetch nothing: the cover is
	// already fresh.
	if err := covers.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(fetcher.gotFile) != 1 {
		t.Errorf("fetches after a fresh cover = %d, want 1", len(fetcher.gotFile))
	}
}

// A stale cover - one written before the album's last change - is refetched,
// so a cover swap in Ente reaches the site on the next run.
func TestCoversSyncRefetchesStaleCover(t *testing.T) {
	thumbnail := fakeJPEG()
	fetcher := coverFixture(t, 1, thumbnail)
	covers := &Covers{Fetcher: fetcher, Dir: t.TempDir()}

	// An album whose updation time is in the future relative to the file
	// we are about to write - i.e. the cover is stale the moment it lands.
	album := fixtureAlbum(101, time.Now().Add(time.Hour).UnixMicro())
	index := indexWith(FileSummary{ID: 1, CreationTime: 1000})

	if err := covers.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if err := covers.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(fetcher.gotFile) != 2 {
		t.Errorf("fetches = %d, want 2 (the stale cover must be refetched)", len(fetcher.gotFile))
	}
}

func TestCoversSyncRefusesNonJPEG(t *testing.T) {
	// Something that decrypts fine but is not a thumbnail - the shape of a
	// wrong-key or wrong-slot decryption.
	notJPEG := []byte("this is definitely not a jpeg")
	fetcher := coverFixture(t, 1, notJPEG)
	covers := &Covers{Fetcher: fetcher, Dir: t.TempDir()}

	err := covers.Sync(context.Background(), fixtureAlbum(101, 500), indexWith(FileSummary{ID: 1}))
	if err == nil {
		t.Fatal("Sync accepted a non-JPEG cover, expected refusal")
	}
	if _, statErr := os.Stat(filepath.Join(covers.Dir, "101.jpg")); statErr == nil {
		t.Error("a rejected cover was written to disk anyway")
	}
}

func TestCoversSyncPropagatesFetchErrors(t *testing.T) {
	covers := &Covers{
		Fetcher: &fakeCoverFetcher{
			files:      map[int64]enteapi.File{},
			thumbnails: map[int64][]byte{},
		},
		Dir: t.TempDir(),
	}
	err := covers.Sync(context.Background(), fixtureAlbum(101, 500), indexWith(FileSummary{ID: 1}))
	if err == nil {
		t.Fatal("Sync with an unfetchable cover succeeded, expected an error")
	}
}
