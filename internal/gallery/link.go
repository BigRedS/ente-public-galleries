package gallery

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/mr-tron/base58"
)

// ShareLink rebuilds the URL a visitor uses to open a public album.
//
// Ente's server only knows half of it. The URL it reports in a collection's
// publicURLs carries the access token, which is all the server needs; the
// collection key is appended as a fragment by whichever client created the
// link, and fragments are never sent in an HTTP request. That is the whole
// point: museum can serve the album without ever being able to read it.
//
// So to produce a working link we re-attach the key ourselves, in the encoding
// Ente's web client uses. See appendCollectionKeyToShareURL in
// web/packages/gallery/services/share.ts: the fragment is the raw key bytes in
// base58, Bitcoin alphabet.
//
// (Ente still reads much older links whose fragment is hex; those are
// distinguished on the reading side by length. We only ever write base58,
// which is what current clients produce.)
//
// albumURLBase, when non-empty, replaces the scheme and host of the reported
// URL. Museum always reports its own configured album host, so anyone serving
// albums from a custom domain needs this to point links at the right place.
func ShareLink(reportedURL string, collectionKey []byte, albumURLBase string) (string, error) {
	if reportedURL == "" {
		return "", fmt.Errorf("album has a public link with no URL")
	}
	if len(collectionKey) == 0 {
		return "", fmt.Errorf("album has no collection key")
	}

	parsed, err := url.Parse(reportedURL)
	if err != nil {
		return "", fmt.Errorf("parsing album URL %q: %w", reportedURL, err)
	}

	if albumURLBase != "" {
		base, err := url.Parse(albumURLBase)
		if err != nil {
			return "", fmt.Errorf("parsing album_url_base %q: %w", albumURLBase, err)
		}
		if base.Scheme == "" || base.Host == "" {
			return "", fmt.Errorf("album_url_base %q needs a scheme and host, like https://albums.example.com", albumURLBase)
		}
		parsed.Scheme = base.Scheme
		parsed.Host = base.Host
		// A base with a path prefix is respected, so album_url_base can
		// be something like https://example.com/galleries.
		if trimmed := strings.TrimRight(base.Path, "/"); trimmed != "" {
			parsed.Path = trimmed + parsed.Path
		}
	}

	// Assign Fragment rather than RawFragment: base58 output is
	// alphanumeric, so there is nothing for url.String to escape, and
	// letting it handle encoding avoids a hand-rolled mistake.
	parsed.Fragment = base58.Encode(collectionKey)
	return parsed.String(), nil
}
