package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/secretbox"

	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// Fixture keys, sealed with the real crypto so buildRows genuinely decrypts.
var (
	fixtureMasterKey     = bytes.Repeat([]byte{0x77}, 32)
	fixtureCollectionKey = bytes.Repeat([]byte{0x01}, 32)
	fixtureUserID        = int64(4242)
)

func sealFixture(plaintext, key []byte, seed byte) (string, string) {
	var nonce [24]byte
	for i := range nonce {
		nonce[i] = seed
	}
	var keyArr [32]byte
	copy(keyArr[:], key)
	sealed := secretbox.Seal(nil, plaintext, &nonce, &keyArr)
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(nonce[:])
}

func fixtureCollection(t *testing.T, id int64, name string, public bool) enteapi.Collection {
	t.Helper()

	sealedKey, keyNonce := sealFixture(fixtureCollectionKey, fixtureMasterKey, 0x11)
	c := enteapi.Collection{
		ID:                 id,
		Owner:              enteapi.CollectionUser{ID: fixtureUserID},
		EncryptedKey:       sealedKey,
		KeyDecryptionNonce: keyNonce,
		Type:               enteapi.TypeAlbum,
	}
	if name != "" {
		sealedName, nameNonce := sealFixture([]byte(name), fixtureCollectionKey, byte(id))
		c.EncryptedName = sealedName
		c.NameDecryptionNonce = nameNonce
	}
	if public {
		c.PublicURLs = []enteapi.PublicURL{{URL: "https://albums.ente.com/?t=x"}}
	}
	return c
}

// fakeLister serves collection fixtures to buildRows.
type fakeLister struct{ collections []enteapi.Collection }

func (f fakeLister) GetCollections(context.Context, int64) ([]enteapi.Collection, error) {
	return f.collections, nil
}

// fakeLinks records mutations for the apply tests.
type fakeLinks struct {
	created  []int64
	disabled []int64
	// failOn makes the mutation for this collection ID error.
	failOn map[int64]error
	// throttle makes the mutation answer 429 this many times first, so
	// the retry path is exercised against something stateful.
	throttle map[int64]int
}

func (f *fakeLinks) CreatePublicLink(_ context.Context, collectionID int64, _ enteapi.LinkOptions) (enteapi.PublicURL, error) {
	if err, ok := f.failOn[collectionID]; ok {
		return enteapi.PublicURL{}, err
	}
	if f.throttle[collectionID] > 0 {
		f.throttle[collectionID]--
		return enteapi.PublicURL{}, &enteapi.Error{StatusCode: 429}
	}
	f.created = append(f.created, collectionID)
	return enteapi.PublicURL{URL: "https://albums.ente.com/?t=new"}, nil
}

func (f *fakeLinks) DisablePublicLink(_ context.Context, collectionID int64) error {
	if err, ok := f.failOn[collectionID]; ok {
		return err
	}
	f.disabled = append(f.disabled, collectionID)
	return nil
}

func TestDecideAction(t *testing.T) {
	tests := []struct {
		public, private bool
		want            action
		wantErr         bool
	}{
		{false, false, actionNone, false},
		{true, false, actionMakePublic, false},
		{false, true, actionMakePrivate, false},
		{true, true, actionNone, true},
	}
	for _, test := range tests {
		got, err := decideAction(test.public, test.private)
		if (err != nil) != test.wantErr {
			t.Errorf("decideAction(%v, %v) error = %v", test.public, test.private, err)
			continue
		}
		if !test.wantErr && got != test.want {
			t.Errorf("decideAction(%v, %v) = %q, want %q", test.public, test.private, got, test.want)
		}
	}
}

func TestBuildRowsDecryptsAndClassifies(t *testing.T) {
	private := fixtureCollection(t, 1, "2025-03 Birthday", false)
	public := fixtureCollection(t, 2, "2026-05 Centenary", true)

	deleted := fixtureCollection(t, 3, "Gone", false)
	deleted.IsDeleted = true
	folder := fixtureCollection(t, 4, "A folder", false)
	folder.Type = enteapi.TypeFolder
	notOwned := fixtureCollection(t, 5, "Someone else's", false)
	notOwned.Owner.ID = 99

	lister := fakeLister{collections: []enteapi.Collection{private, public, deleted, folder, notOwned}}
	rows, counts, err := buildRows(context.Background(), lister, &enteapi.Credentials{UserID: fixtureUserID, MasterKey: fixtureMasterKey})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}

	// Sorted by name: "2025-03 Birthday" before "2026-05 Centenary".
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(rows), rows)
	}
	if rows[0].Name != "2025-03 Birthday" || rows[0].Public {
		t.Errorf("row 0 = %+v, want the private album", rows[0])
	}
	if rows[1].Name != "2026-05 Centenary" || !rows[1].Public {
		t.Errorf("row 1 = %+v, want the public album", rows[1])
	}
	if counts.deleted != 1 || counts.notAlbums != 1 || counts.notOwned != 1 {
		t.Errorf("ignored = %+v, want 1 deleted, 1 not-album, 1 not-owned", counts)
	}
}

// A name that will not decrypt is counted and reported, never silently
// droppable from the scan: an operator bulk-publishing needs to know the
// selection is complete.
func TestBuildRowsCountsUndecryptable(t *testing.T) {
	broken := fixtureCollection(t, 7, "Fine", false)
	broken.EncryptedKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xEE}, 48))
	okay := fixtureCollection(t, 8, "Readable", false)

	rows, counts, err := buildRows(context.Background(),
		fakeLister{collections: []enteapi.Collection{broken, okay}},
		&enteapi.Credentials{UserID: fixtureUserID, MasterKey: fixtureMasterKey})
	if err != nil {
		t.Fatalf("buildRows: %v", err)
	}
	if len(rows) != 1 || rows[0].Name != "Readable" {
		t.Errorf("rows = %+v, want only the readable album", rows)
	}
	if counts.undecryptable != 1 {
		t.Errorf("undecryptable = %d, want 1", counts.undecryptable)
	}
}

func TestMatchRows(t *testing.T) {
	rows := []row{
		{ID: 1, Name: "2025-03 Birthday Trackday"},
		{ID: 2, Name: "Trip to the Alps"},
		{ID: 3, Name: "2026-05 Ducati Centenary"},
	}
	re := regexp.MustCompile(`^\d{4}-\d{2} `)

	matches := matchRows(rows, re)
	if len(matches) != 2 || matches[0].ID != 1 || matches[1].ID != 3 {
		t.Errorf("matches = %+v, want albums 1 and 3", matches)
	}
}

func TestPlanChanges(t *testing.T) {
	matches := []row{
		{ID: 1, Name: "already public", Public: true},
		{ID: 2, Name: "private", Public: false},
		{ID: 3, Name: "also private", Public: false},
	}

	changes, unchanged := planChanges(matches, actionMakePublic)
	if len(changes) != 2 || changes[0].ID != 2 || changes[1].ID != 3 {
		t.Errorf("changes = %+v, want albums 2 and 3", changes)
	}
	if len(unchanged) != 1 || unchanged[0].ID != 1 {
		t.Errorf("unchanged = %+v, want album 1", unchanged)
	}

	changes, unchanged = planChanges(matches, actionMakePrivate)
	if len(changes) != 1 || changes[0].ID != 1 {
		t.Errorf("private changes = %+v, want album 1 only", changes)
	}
	if len(unchanged) != 2 {
		t.Errorf("unchanged = %+v, want albums 2 and 3", unchanged)
	}
}

func TestApplyChangesPublic(t *testing.T) {
	links := &fakeLinks{}
	changes := []row{{ID: 1, Name: "one"}, {ID: 2, Name: "two"}}

	ok, failed := applyChanges(context.Background(), links, changes, actionMakePublic)
	if ok != 2 || failed != 0 {
		t.Fatalf("ok=%d failed=%d, want 2/0", ok, failed)
	}
	if len(links.created) != 2 || links.created[0] != 1 || links.created[1] != 2 {
		t.Errorf("created = %v, want [1 2]", links.created)
	}
	if len(links.disabled) != 0 {
		t.Errorf("disabled = %v, want none", links.disabled)
	}
}

func TestApplyChangesRetriesThrottling(t *testing.T) {
	oldBackoff := throttleBackoff
	throttleBackoff = time.Millisecond
	t.Cleanup(func() { throttleBackoff = oldBackoff })
	links := &fakeLinks{throttle: map[int64]int{1: 2}}
	changes := []row{{ID: 1, Name: "throttled twice, then accepted"}}

	ok, failed := applyChanges(context.Background(), links, changes, actionMakePublic)
	if ok != 1 || failed != 0 {
		t.Fatalf("ok=%d failed=%d, want the retry to succeed", ok, failed)
	}
	if len(links.created) != 1 {
		t.Errorf("created = %v", links.created)
	}
}

func TestApplyChangesIsolatesFailures(t *testing.T) {
	links := &fakeLinks{failOn: map[int64]error{2: errors.New("boom")}}
	changes := []row{{ID: 1, Name: "fine"}, {ID: 2, Name: "doomed"}, {ID: 3, Name: "also fine"}}

	ok, failed := applyChanges(context.Background(), links, changes, actionMakePrivate)
	if ok != 2 || failed != 1 {
		t.Fatalf("ok=%d failed=%d, want 2/1", ok, failed)
	}
	if len(links.disabled) != 2 || links.disabled[0] != 1 || links.disabled[1] != 3 {
		t.Errorf("disabled = %v, want [1 3]", links.disabled)
	}
}
