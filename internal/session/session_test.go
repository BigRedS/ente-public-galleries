package session

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
)

// newStore builds a Store in a temp dir using the device-key-file mode.
//
// That mode is used throughout these tests deliberately: it exercises the real
// file handling and the real secretbox seal/open rather than substituting a
// fake, and unlike the keyring mode it does not reach into the developer's
// actual OS keyring and leave entries behind.
func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return &Store{
		Path:          filepath.Join(dir, "session.json"),
		DeviceKeyFile: filepath.Join(dir, "device.key"),
	}
}

// newSharedStore builds a Store whose device key is the ente CLI's file.
func newSharedStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	return &Store{
		Path:                filepath.Join(dir, "session.json"),
		SharedDeviceKeyFile: filepath.Join(dir, "cli.secret"),
	}
}

func sampleCredentials() *enteapi.Credentials {
	return &enteapi.Credentials{
		UserID:    4242,
		Email:     "someone@example.com",
		Token:     []byte("token-bytes"),
		MasterKey: bytes.Repeat([]byte{0xA1}, 32),
		SecretKey: bytes.Repeat([]byte{0xB2}, 32),
		PublicKey: bytes.Repeat([]byte{0xC3}, 32),
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := newStore(t)
	want := sampleCredentials()

	if err := store.Save(want, "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, endpoint, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if endpoint != "https://api.example.com" {
		t.Errorf("endpoint = %q, want %q", endpoint, "https://api.example.com")
	}
	if got.UserID != want.UserID || got.Email != want.Email {
		t.Errorf("identity = %d/%q, want %d/%q", got.UserID, got.Email, want.UserID, want.Email)
	}
	for _, field := range []struct {
		name      string
		got, want []byte
	}{
		{"Token", got.Token, want.Token},
		{"MasterKey", got.MasterKey, want.MasterKey},
		{"SecretKey", got.SecretKey, want.SecretKey},
		{"PublicKey", got.PublicKey, want.PublicKey},
	} {
		if !bytes.Equal(field.got, field.want) {
			t.Errorf("%s = %x, want %x", field.name, field.got, field.want)
		}
	}
}

// The whole point of the device key is that the file alone is useless, so
// assert the secrets really are absent from it.
func TestSavedFileLeaksNoSecrets(t *testing.T) {
	store := newStore(t)
	creds := sampleCredentials()
	if err := store.Save(creds, "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	for _, secret := range [][]byte{creds.Token, creds.MasterKey, creds.SecretKey} {
		if bytes.Contains(body, secret) {
			t.Errorf("session file contains raw secret %x", secret)
		}
		encoded, _ := json.Marshal(secret)
		if bytes.Contains(body, bytes.Trim(encoded, `"`)) {
			t.Errorf("session file contains base64 secret %s", encoded)
		}
	}
}

func TestSavedFileIsPrivate(t *testing.T) {
	store := newStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for _, path := range []string{store.Path, store.DeviceKeyFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %#o, want 0600", path, perm)
		}
	}
}

func TestLoadWithoutSessionReportsErrNoSession(t *testing.T) {
	store := newStore(t)
	_, _, err := store.Load()
	if !errors.Is(err, ErrNoSession) {
		t.Fatalf("Load on empty store: got %v, want ErrNoSession", err)
	}
}

// Losing the keyring entry is a real scenario, and the resulting message is
// the only thing standing between the user and a confusing failure.
func TestLoadWithWrongDeviceKeyExplainsItself(t *testing.T) {
	store := newStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Replace the device key with a different but perfectly valid one, so
	// the failure comes from decryption rather than from parsing.
	replacement := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, deviceKeySize))
	if err := os.WriteFile(store.DeviceKeyFile, []byte(replacement), 0o600); err != nil {
		t.Fatalf("overwriting device key: %v", err)
	}

	_, _, err := store.Load()
	if err == nil {
		t.Fatal("Load with the wrong device key succeeded, expected failure")
	}
	if !strings.Contains(err.Error(), "log in again") {
		t.Errorf("error does not tell the user what to do: %v", err)
	}
}

func TestLoadRejectsLooseDeviceKeyPermissions(t *testing.T) {
	store := newStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(store.DeviceKeyFile, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	_, _, err := store.Load()
	if err == nil {
		t.Fatal("Load accepted a world-readable device key, expected refusal")
	}
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("error does not suggest the fix: %v", err)
	}
}

func TestLoadRejectsUnknownFormatVersion(t *testing.T) {
	store := newStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatalf("reading session file: %v", err)
	}
	var env map[string]any
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("parsing session file: %v", err)
	}
	env["version"] = formatVersion + 1
	rewritten, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("re-encoding session file: %v", err)
	}
	if err := os.WriteFile(store.Path, rewritten, 0o600); err != nil {
		t.Fatalf("writing session file: %v", err)
	}

	if _, _, err := store.Load(); err == nil {
		t.Fatal("Load accepted an unknown format version, expected refusal")
	}
}

func TestDeleteRemovesBothFiles(t *testing.T) {
	store := newStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	existed, err := store.Delete()
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !existed {
		t.Error("Delete reported nothing existed, but a session had just been saved")
	}
	for _, path := range []string{store.Path, store.DeviceKeyFile} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists after Delete (err=%v)", path, err)
		}
	}
	// Deleting twice must not be an error; it is what `logout` does when
	// there is nothing to log out of. It must, though, admit that there was
	// nothing there.
	existed, err = store.Delete()
	if err != nil {
		t.Errorf("second Delete: %v", err)
	}
	if existed {
		t.Error("second Delete reported something existed, want false")
	}
}

// The ente CLI writes its headless device key as 32 raw bytes at 0644
// (secrets/secret.go in its repo); a shared session must decrypt under
// exactly that file, since the point is one key for both tools.
func TestSharedDeviceKeyReadsCLIRawFormat(t *testing.T) {
	store := newSharedStore(t)

	// The file the CLI would have left behind: raw 32 bytes, mode 0644.
	cliKey := bytes.Repeat([]byte{0x5C}, deviceKeySize)
	if err := os.WriteFile(store.SharedDeviceKeyFile, cliKey, 0o644); err != nil {
		t.Fatalf("writing CLI-style key: %v", err)
	}

	creds := sampleCredentials()
	if err := store.Save(creds, "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _, err := store.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !bytes.Equal(got.MasterKey, creds.MasterKey) {
		t.Error("session did not round-trip under the CLI's raw device key")
	}
}

// When we create the shared file ourselves it must be in the format the CLI
// reads: 32 raw bytes.
func TestSharedDeviceKeyCreatedInCLIFormat(t *testing.T) {
	store := newSharedStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	body, err := os.ReadFile(store.SharedDeviceKeyFile)
	if err != nil {
		t.Fatalf("reading shared key: %v", err)
	}
	if len(body) != deviceKeySize {
		t.Errorf("shared key file holds %d bytes, want %d raw (the CLI reads raw bytes, not base64)", len(body), deviceKeySize)
	}

	// And it must decrypt what it just sealed.
	if _, _, err := store.Load(); err != nil {
		t.Fatalf("Load after creating the shared key: %v", err)
	}
}

// A shared file in our older base64 form keeps working, so pointing the env
// var at a file this tool wrote before the shared mode existed is not a trap.
func TestSharedDeviceKeyAcceptsBase64Form(t *testing.T) {
	store := newSharedStore(t)
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x71}, deviceKeySize))
	if err := os.WriteFile(store.SharedDeviceKeyFile, []byte(key+"\n"), 0o600); err != nil {
		t.Fatalf("writing base64 key: %v", err)
	}

	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, _, err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestSharedDeviceKeyRejectsWrongSize(t *testing.T) {
	store := newSharedStore(t)
	if err := os.WriteFile(store.SharedDeviceKeyFile, bytes.Repeat([]byte{0x00}, 31), 0o644); err != nil {
		t.Fatalf("writing short key: %v", err)
	}
	if _, _, err := store.Load(); err == nil {
		t.Fatal("Load accepted a 31-byte shared key, expected an error")
	}
}

// The CLI's stored accounts are encrypted under its device key, so logging
// out here must leave the shared file alone.
func TestDeleteNeverRemovesTheSharedDeviceKey(t *testing.T) {
	store := newSharedStore(t)
	if err := store.Save(sampleCredentials(), "https://api.example.com"); err != nil {
		t.Fatalf("Save: %v", err)
	}

	existed, err := store.Delete()
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !existed {
		t.Error("Delete claimed nothing existed, but the session file did")
	}
	if _, err := os.Stat(store.SharedDeviceKeyFile); err != nil {
		t.Errorf("the shared device key was removed by logout: %v", err)
	}
	if _, err := os.Stat(store.Path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("session file survived logout (err=%v)", err)
	}
}
