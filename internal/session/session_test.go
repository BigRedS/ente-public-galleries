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
