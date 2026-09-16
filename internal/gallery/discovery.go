package gallery

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// Album is a public gallery ready to be listed and rendered.
//
// It carries the decrypted collection key because later stages need it for
// file keys and thumbnails. It must never be written to disk or into any
// generated output; the share URL deliberately does not include it, and it
// appears nowhere else it could leak from.
type Album struct {
	ID          int64
	Name        string
	Description string
	CoverID     int64
	Asc         bool
	SortOrder   int
	ShareURL    string

	// PasswordProtected means visitors will be asked for the album's link
	// password. It does not stop us reading the album, since we are the
	// owner; it is recorded only so the index can warn people what they
	// are about to click.
	PasswordProtected bool

	// Expires is when the public link stops working; the zero time means
	// it never expires.
	Expires time.Time

	UpdationTime int64

	// Key is the raw collection key. Secret: see the type comment.
	Key []byte
}

// Skip records a collection that will not appear on the index, and why.
type Skip struct {
	ID     int64
	Name   string
	Reason string
}

// Skip reasons. They are human-facing strings first and grouping keys second.
const (
	reasonDeleted       = "deleted"
	reasonNotOwned      = "shared with you, not owned"
	reasonNotAlbum      = "not an album"
	reasonHidden        = "hidden"
	reasonNotPublic     = "not publicly linked"
	reasonExpired       = "public link expired"
	reasonExcluded      = "excluded in config"
	reasonUndecryptable = "key would not decrypt"
)

// Fetcher is the slice of the API discovery needs. The real Client satisfies
// it; tests supply fixtures, since the logic under test is what we do with
// the server's answers, not the transport.
type Fetcher interface {
	GetCollections(ctx context.Context, sinceTime int64) ([]enteapi.Collection, error)
}

// Discoverer selects and decrypts the albums that belong on the index.
type Discoverer struct {
	Fetcher Fetcher
	Creds   *enteapi.Credentials
	// AlbumURLBase rewrites the host of Ente's reported album URLs, for
	// custom domains. Empty means use the reported URL as-is.
	AlbumURLBase string
	// Excluded, when set, is consulted per collection ID and wins over
	// everything except a failure to decrypt.
	Excluded func(id int64) bool
}

// Discover returns the publishable albums and everything skipped on the way.
//
// Skips are returned rather than logged so the caller decides what "loudly"
// means: an interactive run wants each expired link named, a cron run may want
// a count. Ordering within each list is by album name.
func (d *Discoverer) Discover(ctx context.Context, now time.Time) ([]Album, []Skip, error) {
	raw, err := d.Fetcher.GetCollections(ctx, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching collections: %w", err)
	}

	var albums []Album
	var skips []Skip
	for _, c := range raw {
		album, skip, err := d.consider(ctx, c, now)
		if err != nil {
			return nil, nil, err
		}
		if skip != nil {
			skips = append(skips, *skip)
			continue
		}
		albums = append(albums, *album)
	}

	sort.Slice(albums, func(i, j int) bool {
		if albums[i].Name != albums[j].Name {
			return albums[i].Name < albums[j].Name
		}
		return albums[i].ID < albums[j].ID
	})
	sort.Slice(skips, func(i, j int) bool {
		if skips[i].Reason != skips[j].Reason {
			return skips[i].Reason < skips[j].Reason
		}
		return skips[i].ID < skips[j].ID
	})
	return albums, skips, nil
}

// consider applies the publication rules to one collection. Exactly one of
// album or skip is non-nil on success.
func (d *Discoverer) consider(_ context.Context, c enteapi.Collection, now time.Time) (*Album, *Skip, error) {
	skip := func(reason string) *Skip {
		return &Skip{ID: c.ID, Reason: reason, Name: d.bestEffortName(c)}
	}

	switch {
	case c.IsDeleted:
		return nil, skip(reasonDeleted), nil
	case c.Owner.ID != d.Creds.UserID:
		return nil, skip(reasonNotOwned), nil
	case c.Type != enteapi.TypeAlbum:
		return nil, skip(fmt.Sprintf("%s (%s)", reasonNotAlbum, c.Type)), nil
	case len(c.PublicURLs) == 0:
		return nil, skip(reasonNotPublic), nil
	}

	// The private magic metadata says whether the owner has hidden the
	// album. Hidden albums keep their links, so this has to be checked
	// before treating an album as publishable.
	key, err := CollectionKey(c, d.Creds)
	if err != nil {
		// An unhideable-then-undecryptable album is a real problem, not
		// a curation choice, so it surfaces as an error for the caller
		// to report rather than a quiet omission.
		return nil, nil, fmt.Errorf("collection %d: %w", c.ID, err)
	}

	privateMeta, err := decryptMagicMetadata(c.MagicMetadata, key)
	if err != nil {
		return nil, nil, fmt.Errorf("collection %d private metadata: %w", c.ID, err)
	}
	if v, ok := metaInt(privateMeta, "visibility"); ok && v == 2 {
		return nil, skip(reasonHidden), nil
	}

	publicMeta, err := decryptMagicMetadata(c.PublicMagicMetadata, key)
	if err != nil {
		return nil, nil, fmt.Errorf("collection %d public metadata: %w", c.ID, err)
	}

	// Exactly one active link per collection is enforced by a unique index
	// in the server's schema, so the first is the link.
	link := c.PublicURLs[0]

	// validTill is microseconds, zero meaning no expiry. An expired link
	// is still reported by the server as active, so it has to be checked
	// here or a dead link quietly drops off the index between runs.
	var expires time.Time
	if link.ValidTill != 0 {
		expires = time.UnixMicro(link.ValidTill)
		if !expires.After(now) {
			s := skip(reasonExpired)
			s.Reason = fmt.Sprintf("%s on %s", reasonExpired, expires.Format("2006-01-02"))
			return nil, s, nil
		}
	}

	if d.Excluded != nil && d.Excluded(c.ID) {
		return nil, skip(reasonExcluded), nil
	}

	name, err := decryptName(c, key)
	if err != nil {
		return nil, nil, err
	}

	shareURL, err := ShareLink(link.URL, key, d.AlbumURLBase)
	if err != nil {
		return nil, nil, fmt.Errorf("collection %d: %w", c.ID, err)
	}

	album := &Album{
		ID:                c.ID,
		Name:              name,
		ShareURL:          shareURL,
		PasswordProtected: link.PasswordEnabled,
		Expires:           expires,
		UpdationTime:      c.UpdationTime,
		Key:               key,
	}
	if v, ok := metaString(publicMeta, "caption"); ok {
		album.Description = v
	}
	if v, ok := metaInt(publicMeta, "coverID"); ok {
		album.CoverID = v
	}
	if v, ok := metaBool(publicMeta, "asc"); ok {
		album.Asc = v
	}
	if v, ok := metaInt(privateMeta, "order"); ok {
		album.SortOrder = int(v)
	}
	return album, nil, nil
}

// bestEffortName decrypts a name for a skip line, or reports the ID's
// position when it cannot, because "skipped album 4242" is far less useful
// than "skipped album 4242 (Summer 2026)" when the reason turns out to matter.
func (d *Discoverer) bestEffortName(c enteapi.Collection) string {
	if key, err := CollectionKey(c, d.Creds); err == nil {
		if name, err := decryptName(c, key); err == nil && name != "" {
			return name
		}
	}
	if c.Name != "" {
		return c.Name
	}
	return fmt.Sprintf("collection %d", c.ID)
}
