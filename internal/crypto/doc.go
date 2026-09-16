// Package crypto implements the primitives Ente uses to protect album and
// file data: XSalsa20-Poly1305 secretboxes, anonymous sealed boxes, and the
// libsodium secretstream (XChaCha20-Poly1305) construction.
//
// # Provenance
//
// Every file in this package except this one is a verbatim copy from Ente's
// own Go CLI, at https://github.com/ente-io/ente, path cli/internal/crypto.
// Upstream commit af2da7efacd2c7e4db7a15837b74c41ae51f0f49 (2026-08-20), taken
// from repository HEAD f3488d3ace21b523d93dacc19ad21c6d14db9586 (2026-09-16).
//
// The copies are deliberately byte-for-byte, with exactly one change: the
// import of Ente's utils/encoding in crypto_native.go now points at this
// project's internal/encoding (itself a verbatim copy of that same package).
// Keeping the files otherwise untouched means a future `diff` against upstream
// shows only real changes, so tracking Ente's crypto is a mechanical job rather
// than an archaeological one. Resist the urge to reformat or tidy them.
//
// One visible consequence: stream.go is not gofmt-clean, because it is not
// gofmt-clean upstream either. `gofmt -l` will always name it. Do not fix
// that; formatting it would make every future upstream diff noisy for no gain.
//
// Copying rather than importing is forced, not chosen. The code lives under
// cli/internal/, which Go's internal-package rule makes unimportable from
// another module, and the CLI declares its module path as github.com/ente/cli,
// which is not a fetchable location. Vendoring is the only route.
//
// Ente is licensed AGPL-3.0, so this project is too. See LICENSE and NOTICE.
//
// # Why a hand-rolled secretstream
//
// stream.go reimplements crypto_secretstream_xchacha20poly1305 on top of
// golang.org/x/crypto rather than binding libsodium. That keeps the build
// pure Go with no cgo, which is what makes this tool a single static binary.
// The tag semantics matter: Ente's photo metadata and thumbnails are written
// as a single message terminated with TagFinal, which is why
// decryptChaCha20poly1305 insists on that tag and rejects anything else.
package crypto
