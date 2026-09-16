// Package gallery interprets what the Ente server stores: it decrypts album
// names, descriptions and keys, decides what belongs on the index page, and
// rebuilds the public links.
package gallery

import (
	"encoding/json"
	"fmt"

	"github.com/BigRedS/ente-public-galleries/internal/crypto"
	"github.com/BigRedS/ente-public-galleries/internal/encoding"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// CollectionKey unwraps the key a collection is encrypted with.
//
// Which box it comes out of depends on whose collection it is: the owner's
// copy is sealed with the master key, but a copy shared with us is re-wrapped
// in a sealed box to our keypair, because the master key never leaves its
// owner's devices.
func CollectionKey(c enteapi.Collection, creds *enteapi.Credentials) ([]byte, error) {
	if c.Owner.ID == creds.UserID {
		return crypto.SecretBoxOpenBase64(c.EncryptedKey, c.KeyDecryptionNonce, creds.MasterKey)
	}
	// SealedBoxOpen panics rather than erroring on short key material, and
	// a panic on data that came off the wire is worse than an error, so
	// check the lengths it assumes.
	if len(creds.PublicKey) < 32 || len(creds.SecretKey) < 32 {
		return nil, fmt.Errorf("shared collection %d needs a sealed box, but the account keypair is missing or malformed", c.ID)
	}
	sealedKey := encoding.DecodeBase64(c.EncryptedKey)
	if len(sealedKey) < crypto.BoxSealBytes {
		return nil, fmt.Errorf("shared collection %d has a malformed encrypted key", c.ID)
	}
	return crypto.SealedBoxOpen(sealedKey, creds.PublicKey, creds.SecretKey)
}

// decryptName recovers a collection's display name.
//
// Accounts from the early beta store names in the clear, so an absent
// encryptedName is legitimate and falls back to the plaintext field.
func decryptName(c enteapi.Collection, key []byte) (string, error) {
	if c.EncryptedName == "" {
		return c.Name, nil
	}
	name, err := crypto.SecretBoxOpenBase64(c.EncryptedName, c.NameDecryptionNonce, key)
	if err != nil {
		return "", fmt.Errorf("decrypting name of collection %d: %w", c.ID, err)
	}
	return string(name), nil
}

// decryptMagicMetadata opens one metadata blob with the collection key and
// returns its JSON as a map. A nil metadata yields a nil map, so callers can
// probe without nil checks.
func decryptMagicMetadata(mm *enteapi.MagicMetadata, key []byte) (map[string]any, error) {
	if mm == nil {
		return nil, nil
	}
	_, plaintext, err := crypto.DecryptChaChaBase64(mm.Data, key, mm.Header)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(plaintext, &m); err != nil {
		return nil, fmt.Errorf("parsing decrypted metadata: %w", err)
	}
	return m, nil
}

// metaInt, metaString and metaBool read optional values out of decrypted
// metadata maps. JSON numbers arrive as float64, which these absorb; every
// field is optional, so absence is reported rather than defaulted in place.
func metaInt(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	if !ok {
		return 0, false
	}
	return int64(f), true
}

func metaString(m map[string]any, key string) (string, bool) {
	v, ok := m[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func metaBool(m map[string]any, key string) (bool, bool) {
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}
