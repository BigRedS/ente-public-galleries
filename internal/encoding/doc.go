// Package encoding holds the base64 and JSON helpers that Ente's crypto code
// expects to find alongside it.
//
// encoding.go is a verbatim copy of cli/utils/encoding/encoding.go from
// https://github.com/ente-io/ente (commit
// f3488d3ace21b523d93dacc19ad21c6d14db9586). It exists only because
// internal/crypto references it; see that package's doc.go for why Ente's code
// is copied rather than imported.
//
// Note that these helpers panic on malformed input rather than returning an
// error. That is upstream's choice and is kept as-is so the copy stays
// diffable. Do not feed them untrusted data: decode API responses with the
// standard library instead, and reserve these for values that have already
// been validated.
package encoding
