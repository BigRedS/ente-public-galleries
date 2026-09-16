// Package session stores the credentials a login produces so that later runs
// need no interaction.
//
// # Threat model
//
// What is stored is the Ente API token plus the account's master and secret
// keys. Together they grant full read access to the account, so they are
// encrypted at rest with a 32-byte device key that lives in the OS keyring,
// not in the file. An attacker who copies the session file off the machine
// gets nothing without also extracting the keyring entry.
//
// This is the same arrangement Ente's own CLI uses, and its limits are the
// same: anything running as this user while the keyring is unlocked can ask
// for the device key. The protection is against a stolen backup or a stray
// `scp`, not against local malware.
//
// On headless machines there may be no keyring daemon, so the device key can
// instead be kept in a 0600 file. That is weaker, because file permissions
// are then the only barrier, and callers are expected to say so out loud.
package session

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/nacl/secretbox"

	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

const (
	// keyringService and keyringUser identify our device key in the OS keyring.
	keyringService = "ente-public-galleries"
	keyringUser    = "device-key"

	deviceKeySize = 32
	nonceSize     = 24

	// formatVersion guards against silently misreading a file written by a
	// future version with a different layout.
	formatVersion = 1
)

// ErrNoSession means nothing has been stored yet, so the caller should log in.
var ErrNoSession = errors.New("no saved session; run `ente-public-galleries login` first")

// envelope is the on-disk form. The cleartext fields are there so a human can
// tell whose session this is, and so we can detect a session belonging to a
// different account or server without having to decrypt anything.
type envelope struct {
	Version  int    `json:"version"`
	Email    string `json:"email"`
	UserID   int64  `json:"userID"`
	Endpoint string `json:"endpoint"`

	Nonce  string `json:"nonce"`
	Sealed string `json:"sealed"`
}

// secrets is the encrypted payload of an envelope.
//
// This is deliberately a separate local format rather than a reuse of Ente's
// wire formats: internal/crypto exists to speak Ente's protocol, and mixing
// our own at-rest storage into it would blur which code has to stay
// compatible with the server and which is ours to change freely.
type secrets struct {
	Token     []byte `json:"token"`
	MasterKey []byte `json:"masterKey"`
	SecretKey []byte `json:"secretKey"`
	PublicKey []byte `json:"publicKey"`
}

// Store reads and writes the saved session.
//
// DeviceKeyFile, when set, replaces the OS keyring with a 0600 file at that
// path holding our own device key, base64-encoded. Leave it empty for the
// keyring.
//
// SharedDeviceKeyFile, when set, instead takes the device key from the ente
// CLI's secrets file - the path the CLI itself uses when ENTE_CLI_SECRETS_PATH
// is set on a machine with no keyring. That file holds the key as 32 raw
// bytes and is not permission-checked, because both conventions are the
// CLI's own (it writes the file 0644) and refusing to read its standard
// layout would defeat the point of sharing. When we create it ourselves we
// write raw bytes at 0600: stricter than the CLI's mode, and still exactly
// what it reads.
type Store struct {
	Path string
	// DeviceKeyFile is our own device key file. Base64, and 0600 is
	// enforced.
	DeviceKeyFile string
	// SharedDeviceKeyFile is a device key file shared with the ente CLI:
	// raw 32 bytes, permission-lenient, and never deleted by us, because
	// the CLI's own stored accounts are encrypted under it and removing it
	// would break them.
	SharedDeviceKeyFile string
}

// DefaultPath is where sessions live absent other instruction, following XDG.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating config directory: %w", err)
	}
	return filepath.Join(dir, "ente-public-galleries", "session.json"), nil
}

// Save encrypts creds and writes them, replacing any existing session.
func (s *Store) Save(creds *enteapi.Credentials, endpoint string) error {
	key, err := s.deviceKey(true)
	if err != nil {
		return err
	}

	plaintext, err := json.Marshal(secrets{
		Token:     creds.Token,
		MasterKey: creds.MasterKey,
		SecretKey: creds.SecretKey,
		PublicKey: creds.PublicKey,
	})
	if err != nil {
		return fmt.Errorf("encoding session secrets: %w", err)
	}

	var nonce [nonceSize]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return fmt.Errorf("generating nonce: %w", err)
	}
	sealed := secretbox.Seal(nil, plaintext, &nonce, key)

	body, err := json.MarshalIndent(envelope{
		Version:  formatVersion,
		Email:    creds.Email,
		UserID:   creds.UserID,
		Endpoint: endpoint,
		Nonce:    base64.StdEncoding.EncodeToString(nonce[:]),
		Sealed:   base64.StdEncoding.EncodeToString(sealed),
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding session file: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("creating session directory: %w", err)
	}
	return writeFilePrivate(s.Path, append(body, '\n'))
}

// Load decrypts the saved session. It returns ErrNoSession if none exists.
func (s *Store) Load() (*enteapi.Credentials, string, error) {
	body, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, "", ErrNoSession
	}
	if err != nil {
		return nil, "", fmt.Errorf("reading session file: %w", err)
	}

	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, "", fmt.Errorf("parsing session file %s: %w", s.Path, err)
	}
	if env.Version != formatVersion {
		return nil, "", fmt.Errorf("session file %s has unsupported version %d (expected %d); delete it and log in again",
			s.Path, env.Version, formatVersion)
	}

	key, err := s.deviceKey(false)
	if err != nil {
		return nil, "", err
	}

	nonceBytes, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return nil, "", fmt.Errorf("session file %s has a malformed nonce: %w", s.Path, err)
	}
	if len(nonceBytes) != nonceSize {
		return nil, "", fmt.Errorf("session file %s has a %d-byte nonce, expected %d", s.Path, len(nonceBytes), nonceSize)
	}
	sealed, err := base64.StdEncoding.DecodeString(env.Sealed)
	if err != nil {
		return nil, "", fmt.Errorf("session file %s has a malformed payload: %w", s.Path, err)
	}

	var nonce [nonceSize]byte
	copy(nonce[:], nonceBytes)
	plaintext, ok := secretbox.Open(nil, sealed, &nonce, key)
	if !ok {
		return nil, "", fmt.Errorf("could not decrypt %s with the stored device key; if the keyring entry was lost or replaced, delete the file and log in again", s.Path)
	}

	var sec secrets
	if err := json.Unmarshal(plaintext, &sec); err != nil {
		return nil, "", fmt.Errorf("parsing decrypted session secrets: %w", err)
	}

	return &enteapi.Credentials{
		UserID:    env.UserID,
		Email:     env.Email,
		Token:     sec.Token,
		MasterKey: sec.MasterKey,
		SecretKey: sec.SecretKey,
		PublicKey: sec.PublicKey,
	}, env.Endpoint, nil
}

// Delete removes the session file and the device key that protects it. It
// reports whether anything was actually there, so callers can avoid claiming
// to have logged out of nothing.
//
// A shared device key file (the ente CLI's) is deliberately never deleted:
// the CLI's own stored accounts are encrypted under it, and removing it would
// break a tool that is not this one. The session file alone is the secret;
// the key by itself decrypts nothing.
//
// A missing file or keyring entry is not an error: discarding a session that
// is already gone is the desired end state either way.
func (s *Store) Delete() (existed bool, err error) {
	if err := os.Remove(s.Path); err == nil {
		existed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return existed, fmt.Errorf("removing session file: %w", err)
	}

	if s.SharedDeviceKeyFile != "" {
		return existed, nil
	}

	if s.DeviceKeyFile != "" {
		if err := os.Remove(s.DeviceKeyFile); err == nil {
			existed = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return existed, fmt.Errorf("removing device key file: %w", err)
		}
		return existed, nil
	}

	if err := keyring.Delete(keyringService, keyringUser); err == nil {
		existed = true
	} else if !errors.Is(err, keyring.ErrNotFound) {
		return existed, fmt.Errorf("removing device key from keyring: %w", err)
	}
	return existed, nil
}

// deviceKey fetches the key that encrypts the session file, creating one when
// create is set and none exists.
func (s *Store) deviceKey(create bool) (*[deviceKeySize]byte, error) {
	switch {
	case s.SharedDeviceKeyFile != "":
		return s.deviceKeyFromSharedFile(create)
	case s.DeviceKeyFile != "":
		return s.deviceKeyFromFile(create)
	default:
		return s.deviceKeyFromKeyring(create)
	}
}

// deviceKeyFromSharedFile reads the ente CLI's device key, matching the
// CLI's own reading of that file: 32 raw bytes. Base64 is also accepted, so
// a file written there by this tool before the shared mode existed keeps
// working.
//
// Unlike our own device key file, permissions are not enforced or warned
// about. The file belongs to the CLI and its conventions (it writes it
// 0644); reading a standard-layout file from another tool beats policing
// that tool's choices from here.
func (s *Store) deviceKeyFromSharedFile(create bool) (*[deviceKeySize]byte, error) {
	body, err := os.ReadFile(s.SharedDeviceKeyFile)
	switch {
	case err == nil:
		if key, ok := deviceKeyFromBytes(body); ok {
			return key, nil
		}
		return nil, fmt.Errorf("%s does not hold a usable device key: expected 32 raw bytes (or their base64), got %d bytes", s.SharedDeviceKeyFile, len(body))
	case errors.Is(err, os.ErrNotExist):
		if !create {
			return nil, fmt.Errorf("device key file %s does not exist, so the session cannot be decrypted: %w", s.SharedDeviceKeyFile, ErrNoSession)
		}
	default:
		return nil, fmt.Errorf("reading shared device key file: %w", err)
	}

	key, err := newDeviceKey()
	if err != nil {
		return nil, err
	}
	// Raw bytes, matching what the CLI reads. 0600 rather than the CLI's
	// 0644: the CLI never inspects the mode, so stricter costs it nothing.
	if err := os.MkdirAll(filepath.Dir(s.SharedDeviceKeyFile), 0o700); err != nil {
		return nil, fmt.Errorf("creating shared device key directory: %w", err)
	}
	if err := os.WriteFile(s.SharedDeviceKeyFile, key[:], 0o600); err != nil {
		return nil, fmt.Errorf("writing shared device key file: %w", err)
	}
	return key, nil
}

// deviceKeyFromBytes accepts the two shapes a shared device key file can
// hold: 32 raw bytes, or 32 bytes encoded as base64.
func deviceKeyFromBytes(body []byte) (*[deviceKeySize]byte, bool) {
	trimmed := []byte(strings.TrimSpace(string(body)))
	if len(trimmed) == deviceKeySize {
		var key [deviceKeySize]byte
		copy(key[:], trimmed)
		return &key, true
	}
	if decoded, err := base64.StdEncoding.DecodeString(string(trimmed)); err == nil && len(decoded) == deviceKeySize {
		var key [deviceKeySize]byte
		copy(key[:], decoded)
		return &key, true
	}
	return nil, false
}

func (s *Store) deviceKeyFromKeyring(create bool) (*[deviceKeySize]byte, error) {
	encoded, err := keyring.Get(keyringService, keyringUser)
	switch {
	case err == nil:
		return decodeDeviceKey(encoded, "OS keyring")
	case errors.Is(err, keyring.ErrNotFound):
		if !create {
			return nil, fmt.Errorf("no device key in the OS keyring for %q; the session cannot be decrypted, so log in again: %w",
				keyringService, ErrNoSession)
		}
	default:
		// Most often this is a headless box with no keyring daemon.
		// Say so, and point at the escape hatch, rather than emitting
		// a bare dbus error.
		return nil, fmt.Errorf("cannot reach the OS keyring (%w); on a machine with no keyring daemon, pass --device-key-file to keep the device key in a 0600 file instead", err)
	}

	key, err := newDeviceKey()
	if err != nil {
		return nil, err
	}
	if err := keyring.Set(keyringService, keyringUser, base64.StdEncoding.EncodeToString(key[:])); err != nil {
		return nil, fmt.Errorf("storing device key in the OS keyring: %w", err)
	}
	return key, nil
}

func (s *Store) deviceKeyFromFile(create bool) (*[deviceKeySize]byte, error) {
	encoded, err := os.ReadFile(s.DeviceKeyFile)
	switch {
	case err == nil:
		info, statErr := os.Stat(s.DeviceKeyFile)
		if statErr == nil && info.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("device key file %s is mode %#o; it must not be readable by group or others. Fix with: chmod 600 %s",
				s.DeviceKeyFile, info.Mode().Perm(), s.DeviceKeyFile)
		}
		return decodeDeviceKey(string(encoded), s.DeviceKeyFile)
	case errors.Is(err, os.ErrNotExist):
		if !create {
			return nil, fmt.Errorf("device key file %s does not exist, so the session cannot be decrypted: %w", s.DeviceKeyFile, ErrNoSession)
		}
	default:
		return nil, fmt.Errorf("reading device key file: %w", err)
	}

	key, err := newDeviceKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.DeviceKeyFile), 0o700); err != nil {
		return nil, fmt.Errorf("creating device key directory: %w", err)
	}
	if err := writeFilePrivate(s.DeviceKeyFile, []byte(base64.StdEncoding.EncodeToString(key[:])+"\n")); err != nil {
		return nil, err
	}
	return key, nil
}

func newDeviceKey() (*[deviceKeySize]byte, error) {
	var key [deviceKeySize]byte
	if _, err := rand.Read(key[:]); err != nil {
		return nil, fmt.Errorf("generating device key: %w", err)
	}
	return &key, nil
}

func decodeDeviceKey(encoded, source string) (*[deviceKeySize]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return nil, fmt.Errorf("device key from %s is not valid base64: %w", source, err)
	}
	if len(raw) != deviceKeySize {
		return nil, fmt.Errorf("device key from %s is %d bytes, expected %d", source, len(raw), deviceKeySize)
	}
	var key [deviceKeySize]byte
	copy(key[:], raw)
	return &key, nil
}

// writeFilePrivate writes data to path with 0600 permissions, creating the
// file with those permissions rather than relaxing them afterwards, so the
// contents are never briefly world-readable.
func writeFilePrivate(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s for writing: %w", path, err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	return nil
}
