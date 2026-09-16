# AGENTS.md

Instructions for AI coding agents (and future humans) working in this repo.
The user's cross-project preferences live in `~/AGENTS.md`; this file is the
repo-specific part.

## Build and verify

    go vet ./... && go test ./...
    go build -o ente-public-galleries .

`gofmt -l .` always names `internal/crypto/stream.go`. That is expected and
correct: it is a verbatim copy from Ente and is not gofmt-clean upstream.
**Never run a whole-directory `gofmt -w` that includes `internal/crypto`** —
it will reformat the vendored files. Format files by name. Two separate
incidents of exactly this are why `internal/crypto/vendored_test.go` pins
every vendored file's sha256 and fails the build on drift.

## Toolchain pinning — do not `go get -u`

The machine runs Go 1.24 from apt. `x/crypto` is pinned to v0.42.0 because
v0.53+ requires go >= 1.26 and would make every build silently download a
newer toolchain, overriding that choice. `go.mod` says `go 1.24.0` on
purpose. Upgrading means: bump go, bump x/crypto together, and accept the
toolchain switch deliberately. `golang.org/x/sys` is pinned for the same
reason.

## Vendored Ente crypto

Everything in `internal/crypto` (except `doc.go`, `vendored_test.go`) and
`internal/encoding/encoding.go` is copied byte-for-byte from
https://github.com/ente-io/ente `cli/internal/crypto` and `cli/utils/encoding`
— see `internal/crypto/doc.go` for the commit and the reasoning. It cannot be
imported (Ente's CLI is under `internal/` and declares an unfetchable module
path), so copying is the only route, which is also why this repo is AGPL.

Re-syncing to a newer upstream: replace the files, update the hashes in
`vendoredFiles`, and update the commit recorded in both `doc.go` and `NOTICE`
in the same change. Keep `crypto_native.go`'s import rewrite as the only
delta.

## Invariants — check before changing

- **Never call `/public-collection/*` endpoints.** They are the
  browser-facing public album API, and `/info` and `/diff` consume a slot in
  the link's device limit, keyed on IP and User-Agent. Everything this tool
  needs is available authenticated, and generating the site must never eat
  the quota real visitors need. (Reference: `server/pkg/middleware/collection_link.go`,
  `shouldCheckCollectionLinkDeviceLimit`.)
- **Discovery always fetches with `sinceTime=0`.** Creating, disabling or
  editing a public link does not bump a collection's `updation_time` (only
  the `public_collection_tokens` table is touched), so incremental discovery
  would never notice a link appearing or dying — and link state is the one
  fact this tool publishes. File operations *do* bump it
  (`server/pkg/repo/file.go` runs `UPDATE collections SET updation_time` on
  every file insert), which is what makes the per-album cache trigger sound.
- **No key material is ever written to disk outside the session file.** The
  `.cache/albums/*.json` indexes hold only file IDs, timestamps and
  coordinates, and `TestSyncAlbumCacheLeaksNoKeys` enforces it. File keys
  are re-derived on demand — one secretbox open, not worth breaking this for.
- **Never send an `Origin` header** in `enteapi` requests. Museum's link
  middleware trusts an absent Origin and rejects unrecognised ones.
- **The base58 link fragment must match Ente's web client exactly**:
  `publicURL.url + "#" + base58(rawCollectionKey)` (Bitcoin alphabet). Test
  vectors in `internal/gallery/link_test.go` were computed by an independent
  Python implementation, because a wrong alphabet produces links that look
  fine and don't open, and a Go-only round trip would prove nothing.
- **Pass Argon2 `memLimit`/`opsLimit` through from the server response.**
  They vary per account (up to ~1 GiB) and hardcoding them derives a wrong
  key with no useful error.
- AGPL: if more code is copied from Ente, add it to `NOTICE`.

## Where upstream knowledge lives

Everything about Ente's protocol was established by reading the ente/ente
monorepo. The load-bearing files, so you don't have to find them again:

- Decryption chain: `cli/pkg/secrets/key_holder.go`, `cli/pkg/mapper/photo.go`
- Login ladder: `cli/pkg/sign_in.go`, `cli/internal/api/login*.go`
- API shapes: `cli/internal/api/{client,collection,collection_type,files,file_type}.go`
- Share link format: `web/packages/gallery/services/share.ts` (writer),
  `web/apps/albums/src/public-album/access/services/extract-collection-key.ts`
  (reader; legacy links use hex, current use base58)
- `publicURLs` in `/collections/v2` responses: `server/pkg/repo/collection.go`
- Device key / `ENTE_CLI_SECRETS_PATH`: `cli/pkg/secrets/secret.go` (raw
  32 bytes, 0644, keyring service "ente" user "ente-cli-user")

## Testing style

Tests build fixtures with the real crypto (secretbox, secretstream, sealed
box) rather than prebaked ciphertext, so the decryption chain itself is under
test. Transport is faked at small interfaces (`Fetcher`, `FileFetcher`,
`CoverFetcher`); keep it that way rather than mocking crypto or adding
HTTP-level test servers.

## Where things stand

Live-verified: login, discovery, link reconstruction. Fixture-tested only:
file walk, covers, rendering — the next session should run `covers` and
`build` against the real account before building anything new on top.
