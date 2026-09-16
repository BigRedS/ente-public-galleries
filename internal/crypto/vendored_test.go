package crypto

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// vendoredFiles pins the contents of every file copied from Ente, relative to
// the repository root.
//
// This exists because the copies are only useful if they stay diffable against
// upstream, and that property is easy to destroy by accident: a stray
// `gofmt -w .` silently reformats stream.go, which is not gofmt-clean upstream.
// A comment asking people not to do that is not enough, so this fails the
// build instead.
//
// Legitimately updating to a newer upstream means replacing the files, running
// this test to get the new hashes, and updating both this map and the commit
// recorded in doc.go and NOTICE in the same change.
var vendoredFiles = map[string]string{
	"internal/crypto/crypto.go":        "e8a2c0e6457ed3718a40dbf75bcfbd796f44ae23c04e86b7e213a6a314e49d1f",
	"internal/crypto/crypto_native.go": "d5f49f9829d1ac2122029c4211baaca9879d32b6fa7aff94a4f6d85c2d256693",
	"internal/crypto/stream.go":        "ede850100301b173240c830346e5bf7d4a1c2d9557cdeb5efdfe8150936160ef",
	"internal/crypto/utils.go":         "f82c1daee03b67fe1d4b9437f1a8fb7fdcd81ebbd7d7ff86917cd5932c34219f",
	"internal/crypto/crypto_test.go":   "caf8dcbe072fd2727e2d655535243e081f6bf86fc300b530caa471fdde95fc36",
	"internal/encoding/encoding.go":    "fbd1a5ece989bfc0bf73ff594264f5551307324696f4f7cd630aa12c0aa9ff46",
}

func TestVendoredFilesAreUnmodified(t *testing.T) {
	// This file lives two directories below the repository root.
	root := filepath.Join("..", "..")

	paths := make([]string, 0, len(vendoredFiles))
	for path := range vendoredFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		body, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		sum := sha256.Sum256(body)
		got := hex.EncodeToString(sum[:])
		if want := vendoredFiles[path]; got != want {
			t.Errorf(`%s has been modified.

  want sha256 %s
  got  sha256 %s

These files are verbatim copies from Ente and must stay that way so future
diffs against upstream remain meaningful; see internal/crypto/doc.go. If a
formatter touched this file, revert it. If you are deliberately syncing a newer
upstream, update vendoredFiles in internal/crypto/vendored_test.go along with
the commit recorded in doc.go and NOTICE.`, path, want, got)
		}
	}
}
