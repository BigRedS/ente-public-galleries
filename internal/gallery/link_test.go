package gallery

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
)

// The base58 expectations here were computed by an independent implementation
// (a handful of lines of Python over the Bitcoin alphabet), not by the Go
// library under test. The fragment encoding is the one place where a subtly
// wrong alphabet produces links that look perfectly fine and simply do not
// open, so a shared-bug round trip would prove nothing.
//
// Vectors deliberately include leading zero bytes (base58 encodes them as
// leading '1's, the classic off-by-one in hand-rolled implementations) and the
// all-0xff case (no zero bytes at all).
var linkVectors = []struct {
	name string
	b64  string
	b58  string
}{
	{
		name: "32 x 0x01",
		b64:  "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=",
		b58:  "4vJ9JU1bJJE96FWSJKvHsmmFADCg4gpZQff4P3bkLKi",
	},
	{
		name: "leading zero bytes",
		b64:  "AAABAgMEBQYHCAkKCwwNDg8QERITFBUWFxgZGhscHR4=",
		b58:  "11CiMQsCUhqABwwLyCFeX2iPnBZX3s28dUUCBrirhs",
	},
	{
		name: "bytes 0..31",
		b64:  "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=",
		b58:  "1thX6LZfHDZZKUs92febYZhYRcXddmzfzF2NvTkPNE",
	},
	{
		name: "all 0xff",
		b64:  "//////////////////////////////////////////8=",
		b58:  "JEKNVnkbo3jma5nREBBJCDoXFVeKkD56V3xKrvRmWxFG",
	},
}

// reportedAlbumURL is shaped like what museum hands back for a public album:
// host plus a bare ?t= access token, no fragment.
const reportedAlbumURL = "https://albums.ente.com/?t=sometoken"

func TestShareLinkAppendsBase58Fragment(t *testing.T) {
	for _, vector := range linkVectors {
		t.Run(vector.name, func(t *testing.T) {
			key, err := base64.StdEncoding.DecodeString(vector.b64)
			if err != nil {
				t.Fatalf("decoding vector key: %v", err)
			}

			link, err := ShareLink(reportedAlbumURL, key, "")
			if err != nil {
				t.Fatalf("ShareLink: %v", err)
			}

			want := reportedAlbumURL + "#" + vector.b58
			if link != want {
				t.Errorf("link = %q\nwant   %q", link, want)
			}
		})
	}
}

func TestShareLinkRewritesHost(t *testing.T) {
	key := bytes.Repeat([]byte{0x01}, 32)

	link, err := ShareLink(reportedAlbumURL, key, "https://pics.example.com")
	if err != nil {
		t.Fatalf("ShareLink: %v", err)
	}

	want := "https://pics.example.com/?t=sometoken#4vJ9JU1bJJE96FWSJKvHsmmFADCg4gpZQff4P3bkLKi"
	if link != want {
		t.Errorf("link = %q\nwant   %q", link, want)
	}
}

// A base with a path prefix is respected, so a rewrite can point into a
// subdirectory rather than only at a host root.
func TestShareLinkRewriteBaseMayHaveAPath(t *testing.T) {
	key := bytes.Repeat([]byte{0x01}, 32)

	link, err := ShareLink(reportedAlbumURL, key, "https://example.com/galleries")
	if err != nil {
		t.Fatalf("ShareLink: %v", err)
	}

	want := "https://example.com/galleries/?t=sometoken#4vJ9JU1bJJE96FWSJKvHsmmFADCg4gpZQff4P3bkLKi"
	if link != want {
		t.Errorf("link = %q\nwant   %q", link, want)
	}
}

func TestShareLinkRejectsBadInput(t *testing.T) {
	key := bytes.Repeat([]byte{0x01}, 32)

	for _, test := range []struct {
		name         string
		reportedURL  string
		key          []byte
		albumURLBase string
		wantErr      string
	}{
		{name: "no URL", reportedURL: "", key: key, wantErr: "no URL"},
		{name: "no key", reportedURL: reportedAlbumURL, key: nil, wantErr: "no collection key"},
		{name: "base without host", reportedURL: reportedAlbumURL, key: key, albumURLBase: "galleries.example.com", wantErr: "scheme and host"},
		{name: "unparseable URL", reportedURL: "://not a url", key: key, wantErr: "parsing album URL"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ShareLink(test.reportedURL, test.key, test.albumURLBase)
			if err == nil {
				t.Fatal("ShareLink succeeded, expected an error")
			}
			if !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, test.wantErr)
			}
		})
	}
}
