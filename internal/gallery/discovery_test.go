package gallery

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"crypto/rand"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/encoding"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// fixtureCollectionKey is 32 bytes of 0x01, chosen so its base58 encoding was
// verified against an independent implementation in link_test.go. Using it
// here means fragment assertions in these tests are known constants rather
// than calls back into the code under test.
var fixtureCollectionKey = bytes.Repeat([]byte{0x01}, 32)

const (
	fixtureUserID  = int64(4242)
	fixtureToken   = "sometoken"
	fixtureNowSecs = int64(1_750_000_000) // mid-June 2026
)

// fixedNonce builds a 24-byte nonce from a byte pattern, so ciphertexts are
// deterministic within a test without any two fields ever sharing a nonce.
func fixedNonce(seed byte) [24]byte {
	var n [24]byte
	for i := range n {
		n[i] = seed
	}
	return n
}

// seal wraps plaintext in a secretbox under key, returning the base64 parts
// the API's JSON fields expect.
func seal(plaintext, key []byte, seed byte) (string, string) {
	n := fixedNonce(seed)
	sealed := secretbox.Seal(nil, plaintext, &n, keyPtr(key))
	return encoding.EncodeBase64(sealed), encoding.EncodeBase64(n[:])
}

// sealMeta wraps a value in Ente's metadata form: a secretstream message and
// header, both base64, as the API's MagicMetadata carries them.
func sealMeta(value any, key []byte, seed byte) (*enteapi.MagicMetadata, error) {
	plaintext, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	data, header, err := crypto.EncryptChaCha20poly1305(plaintext, key)
	if err != nil {
		return nil, err
	}
	return &enteapi.MagicMetadata{
		Version: 1,
		Data:    encoding.EncodeBase64(data),
		Header:  encoding.EncodeBase64(header),
	}, nil
}

func mustSealMeta(t *testing.T, value any, seed byte) *enteapi.MagicMetadata {
	t.Helper()
	mm, err := sealMeta(value, fixtureCollectionKey, seed)
	if err != nil {
		t.Fatalf("sealing metadata: %v", err)
	}
	return mm
}

func keyPtr(key []byte) *[32]byte {
	var k [32]byte
	copy(k[:], key)
	return &k
}

// fixtureCredentials builds the account the fixtures are encrypted for: the
// master key that seals owned collection keys, plus the keypair that shared
// collection keys are sealed to.
func fixtureCredentials(t *testing.T) *enteapi.Credentials {
	t.Helper()
	publicKey, secretKey, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generating fixture keypair: %v", err)
	}
	return &enteapi.Credentials{
		UserID:    fixtureUserID,
		Email:     "owner@example.com",
		MasterKey: bytes.Repeat([]byte{0x77}, 32),
		PublicKey: publicKey[:],
		SecretKey: secretKey[:],
	}
}

// fixture describes one collection for buildFixture. Everything is encrypted
// with the real crypto, so discovery has to genuinely decrypt its way through.
type fixture struct {
	id           int64
	name         string
	typ          string
	caption      string
	coverID      int64
	asc          bool
	hidden       bool
	order        int
	deleted      bool
	ownerIsOther bool

	// link configuration
	noLink    bool
	password  bool
	validTill int64 // microseconds

	// reportedURL overrides the link URL Ente is taken to have reported.
	reportedURL string
}

func buildFixture(t *testing.T, f fixture, creds *enteapi.Credentials) enteapi.Collection {
	t.Helper()

	ownerID := int64(fixtureUserID)
	if f.ownerIsOther {
		ownerID = 99
	}

	// Owned collections wrap their key in a secretbox under the master
	// key; collections shared with us wrap it in a sealed box to our
	// keypair instead, because that is the only key the server can use.
	var sealedKey, keyNonce string
	if f.ownerIsOther {
		toUs, err := box.SealAnonymous(nil, fixtureCollectionKey, keyPtr(creds.PublicKey), nil)
		if err != nil {
			t.Fatalf("sealing shared collection key: %v", err)
		}
		sealedKey = encoding.EncodeBase64(toUs)
	} else {
		sealedKey, keyNonce = seal(fixtureCollectionKey, creds.MasterKey, 0x11)
	}

	c := enteapi.Collection{
		ID:                 f.id,
		Owner:              enteapi.CollectionUser{ID: ownerID, Email: "owner@example.com"},
		EncryptedKey:       sealedKey,
		KeyDecryptionNonce: keyNonce,
		Type:               f.typ,
		UpdationTime:       1_700_000_000_000_000,
		IsDeleted:          f.deleted,
	}
	if c.Type == "" {
		c.Type = enteapi.TypeAlbum
	}

	if f.name != "" {
		sealed, nonce := seal([]byte(f.name), fixtureCollectionKey, 0x22)
		c.EncryptedName = sealed
		c.NameDecryptionNonce = nonce
	}

	privateMeta := map[string]any{}
	if f.hidden {
		privateMeta["visibility"] = float64(2)
	}
	if f.order != 0 {
		privateMeta["order"] = float64(f.order)
	}
	if len(privateMeta) > 0 {
		c.MagicMetadata = mustSealMeta(t, privateMeta, 0x33)
	}

	publicMeta := map[string]any{}
	if f.caption != "" {
		publicMeta["caption"] = f.caption
	}
	if f.coverID != 0 {
		publicMeta["coverID"] = float64(f.coverID)
	}
	if f.asc {
		publicMeta["asc"] = true
	}
	if len(publicMeta) > 0 {
		c.PublicMagicMetadata = mustSealMeta(t, publicMeta, 0x44)
	}

	if !f.noLink {
		reported := f.reportedURL
		if reported == "" {
			reported = "https://albums.ente.com/?t=" + fixtureToken
		}
		link := enteapi.PublicURL{URL: reported, ValidTill: f.validTill}
		if f.password {
			nonce := "cGFzc3dvcmQtbm9uY2U="
			link.PasswordEnabled = true
			link.Nonce = &nonce
		}
		c.PublicURLs = []enteapi.PublicURL{link}
	}

	return c
}

type fakeFetcher struct {
	collections []enteapi.Collection
}

func (f fakeFetcher) GetCollections(_ context.Context, _ int64) ([]enteapi.Collection, error) {
	return f.collections, nil
}

func discover(t *testing.T, fixtures []fixture, opts func(*Discoverer)) ([]Album, []Skip) {
	t.Helper()

	creds := fixtureCredentials(t)
	collections := make([]enteapi.Collection, len(fixtures))
	for i, f := range fixtures {
		collections[i] = buildFixture(t, f, creds)
	}

	d := &Discoverer{Fetcher: fakeFetcher{collections: collections}, Creds: creds}
	if opts != nil {
		opts(d)
	}
	albums, skips, err := d.Discover(context.Background(), time.Unix(fixtureNowSecs, 0))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	return albums, skips
}

// discoverRaw runs discovery over collections built by hand, for the tests
// that need to corrupt a fixture in ways the builder will not do on purpose.
func discoverRaw(t *testing.T, build func(*enteapi.Credentials) []enteapi.Collection) ([]Album, []Skip, error) {
	t.Helper()

	creds := fixtureCredentials(t)
	d := &Discoverer{Fetcher: fakeFetcher{collections: build(creds)}, Creds: creds}
	return d.Discover(context.Background(), time.Unix(fixtureNowSecs, 0))
}

func TestDiscoverHappyPath(t *testing.T) {
	albums, _ := discover(t, []fixture{{
		id:      101,
		name:    "Summer 2026",
		caption: "Sun, sea, sand",
		coverID: 555,
		asc:     true,
		order:   3,
	}}, nil)

	if len(albums) != 1 {
		t.Fatalf("got %d albums, want 1", len(albums))
	}
	a := albums[0]
	if a.ID != 101 || a.Name != "Summer 2026" {
		t.Errorf("identity = %d/%q", a.ID, a.Name)
	}
	if a.Description != "Sun, sea, sand" {
		t.Errorf("Description = %q", a.Description)
	}
	if a.CoverID != 555 {
		t.Errorf("CoverID = %d", a.CoverID)
	}
	if !a.Asc {
		t.Error("Asc = false, want true")
	}
	if a.SortOrder != 3 {
		t.Errorf("SortOrder = %d", a.SortOrder)
	}
	wantURL := "https://albums.ente.com/?t=" + fixtureToken + "#4vJ9JU1bJJE96FWSJKvHsmmFADCg4gpZQff4P3bkLKi"
	if a.ShareURL != wantURL {
		t.Errorf("ShareURL = %q\nwant        %q", a.ShareURL, wantURL)
	}
	if a.PasswordProtected {
		t.Error("PasswordProtected = true, want false")
	}
	if !a.Expires.IsZero() {
		t.Errorf("Expires = %v, want zero", a.Expires)
	}
	if !bytes.Equal(a.Key, fixtureCollectionKey) {
		t.Errorf("Key = %x, want the fixture collection key", a.Key)
	}
}

func TestDiscoverSortsByName(t *testing.T) {
	albums, _ := discover(t, []fixture{
		{id: 1, name: "Zeta"},
		{id: 2, name: "alpha"},
		{id: 3, name: "Beta"},
	}, nil)
	if len(albums) != 3 {
		t.Fatalf("got %d albums, want 3", len(albums))
	}
	// Byte order, so uppercase sorts before lowercase.
	want := []string{"Beta", "Zeta", "alpha"}
	for i, name := range want {
		if albums[i].Name != name {
			t.Errorf("position %d = %q, want %q", i, albums[i].Name, name)
		}
	}
}

func TestDiscoverSkips(t *testing.T) {
	tests := []struct {
		name    string
		fixture fixture
		reason  string
	}{
		{"deleted", fixture{id: 1, name: "Gone", deleted: true}, reasonDeleted},
		{"folder type", fixture{id: 2, name: "A Folder", typ: enteapi.TypeFolder}, reasonNotAlbum},
		{"favorites type", fixture{id: 3, name: "Favourites", typ: enteapi.TypeFavorites}, reasonNotAlbum},
		{"not public", fixture{id: 4, name: "Private Stuff", noLink: true}, reasonNotPublic},
		{"hidden", fixture{id: 5, name: "Hidden Album", hidden: true}, reasonHidden},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			albums, skips := discover(t, []fixture{test.fixture}, nil)
			assertOneSkip(t, albums, skips, test.reason)
		})
	}
}

func TestDiscoverSkipsCollectionsOwnedByOthers(t *testing.T) {
	albums, skips := discover(t, []fixture{{id: 7, name: "Someone Else's", ownerIsOther: true}}, nil)
	assertOneSkip(t, albums, skips, reasonNotOwned)
}

func TestDiscoverSkipsExpiredLinksNamingTheDate(t *testing.T) {
	expired := time.Unix(fixtureNowSecs, 0).AddDate(0, -1, 0).UnixMicro()
	albums, skips := discover(t, []fixture{{id: 8, name: "Old Trip", validTill: expired}}, nil)

	if len(albums) != 0 {
		t.Fatalf("got %d albums, want 0", len(albums))
	}
	if len(skips) != 1 {
		t.Fatalf("got %d skips, want 1: %v", len(skips), skips)
	}
	wantReason := "public link expired on " + time.UnixMicro(expired).Format("2006-01-02")
	if skips[0].Reason != wantReason {
		t.Errorf("Reason = %q, want %q", skips[0].Reason, wantReason)
	}
	if skips[0].Name != "Old Trip" {
		t.Errorf("skip Name = %q, want the decrypted album name", skips[0].Name)
	}
}

func TestDiscoverKeepsExpiringLinksAndFlagsTheDate(t *testing.T) {
	later := time.Unix(fixtureNowSecs, 0).AddDate(0, 1, 0).UnixMicro()
	albums, _ := discover(t, []fixture{{id: 9, name: "Soon Gone", validTill: later}}, nil)

	if len(albums) != 1 {
		t.Fatalf("got %d albums, want 1: a link that has not expired yet is still publishable", len(albums))
	}
	if !albums[0].Expires.Equal(time.UnixMicro(later)) {
		t.Errorf("Expires = %v, want %v", albums[0].Expires, time.UnixMicro(later))
	}
}

func TestDiscoverKeepsPasswordProtectedAlbumsAndFlagsThem(t *testing.T) {
	albums, _ := discover(t, []fixture{{id: 10, name: "Locked", password: true}}, nil)

	if len(albums) != 1 {
		t.Fatalf("got %d albums, want 1: password protection affects visitors, not publication", len(albums))
	}
	if !albums[0].PasswordProtected {
		t.Error("PasswordProtected = false, want true")
	}
}

func TestDiscoverHonoursExclusions(t *testing.T) {
	albums, skips := discover(t, []fixture{
		{id: 11, name: "Kept"},
		{id: 12, name: "Dropped"},
	}, func(d *Discoverer) {
		d.Excluded = func(id int64) bool { return id == 12 }
	})

	if len(albums) != 1 || albums[0].Name != "Kept" {
		t.Fatalf("albums = %+v, want only Kept", albums)
	}
	if len(skips) != 1 || skips[0].Name != "Dropped" || skips[0].Reason != reasonExcluded {
		t.Fatalf("skips = %+v, want Dropped for reason %q", skips, reasonExcluded)
	}
}

func TestDiscoverLegacyPlaintextName(t *testing.T) {
	// Accounts from before name encryption store the name in the clear.
	albums, skips, err := discoverRaw(t, func(creds *enteapi.Credentials) []enteapi.Collection {
		c := buildFixture(t, fixture{id: 13}, creds)
		c.EncryptedName = ""
		c.NameDecryptionNonce = ""
		c.Name = "Plain Old Album"
		return []enteapi.Collection{c}
	})
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(skips) != 0 {
		t.Fatalf("unexpected skips: %+v", skips)
	}
	if len(albums) != 1 || albums[0].Name != "Plain Old Album" {
		t.Fatalf("albums = %+v, want one named from the legacy plaintext field", albums)
	}
}

// An album whose key does not decrypt is a real error, not a skip: something
// is wrong with the account or the server, and hiding it as a missing album
// would make that undiagnosable.
func TestDiscoverUndecryptableKeyIsAnError(t *testing.T) {
	_, _, err := discoverRaw(t, func(creds *enteapi.Credentials) []enteapi.Collection {
		c := buildFixture(t, fixture{id: 14, name: "Broken"}, creds)
		c.EncryptedKey = encoding.EncodeBase64(bytes.Repeat([]byte{0xEE}, 48))
		return []enteapi.Collection{c}
	})
	if err == nil {
		t.Fatal("Discover succeeded with an undecryptable collection key, expected an error")
	}
}

func TestDiscoverRewritesAlbumHost(t *testing.T) {
	albums, _ := discover(t, []fixture{{id: 15, name: "Custom Domain"}}, func(d *Discoverer) {
		d.AlbumURLBase = "https://pics.example.com"
	})
	if len(albums) != 1 {
		t.Fatalf("got %d albums, want 1", len(albums))
	}
	want := "https://pics.example.com/?t=" + fixtureToken + "#4vJ9JU1bJJE96FWSJKvHsmmFADCg4gpZQff4P3bkLKi"
	if albums[0].ShareURL != want {
		t.Errorf("ShareURL = %q\nwant       %q", albums[0].ShareURL, want)
	}
}

func assertOneSkip(t *testing.T, albums []Album, skips []Skip, wantReason string) {
	t.Helper()
	if len(albums) != 0 {
		t.Fatalf("got %d albums, want 0: %+v", len(albums), albums)
	}
	if len(skips) != 1 {
		t.Fatalf("got %d skips, want 1: %+v", len(skips), skips)
	}
	if !strings.HasPrefix(skips[0].Reason, wantReason) {
		t.Errorf("Reason = %q, want it to start with %q", skips[0].Reason, wantReason)
	}
}
