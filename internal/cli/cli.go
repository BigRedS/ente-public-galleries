// Package cli holds the command-line plumbing shared by this project's
// binaries: the common flags, the device-key precedence, and session loading.
//
// It exists as a package so that the precedence rules exist in exactly one
// place. They are security-relevant - they decide what may decrypt the
// session - and a second binary copying them by hand would be one refactor
// away from silently disagreeing with the first.
package cli

import (
	"flag"
	"fmt"
	"os"

	"github.com/BigRedS/ente-public-galleries/internal/config"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
	"github.com/BigRedS/ente-public-galleries/internal/session"
)

// Flags are the flags every command in every binary shares.
type Flags struct {
	ConfigPath    string
	SessionPath   string
	DeviceKeyFile string
}

// Register adds the common flags to fs.
func (f *Flags) Register(fs *flag.FlagSet) {
	fs.StringVar(&f.ConfigPath, "config", "config.yaml",
		"path to the config file; absent is fine, defaults are used")
	fs.StringVar(&f.SessionPath, "session", "",
		"path to the session file (default: the user config directory)")
	fs.StringVar(&f.DeviceKeyFile, "device-key-file", "",
		"keep the session encryption key in this 0600 file instead of the OS keyring; weaker, for headless machines with no keyring daemon")
}

// store builds the session store. cfg may be nil for commands that have not
// loaded a config, in which case only the flags apply.
//
// Device key precedence, first match wins: the --device-key-file flag for a
// one-off, then ENTE_CLI_SECRETS_PATH to share the ente CLI's device key file
// (the CLI's own headless fallback, so one key file serves both tools), then
// the config's device_key_file, then the OS keyring.
func (f *Flags) store(cfg *config.Config) (*session.Store, error) {
	path := f.SessionPath
	if path == "" {
		var err error
		if path, err = session.DefaultPath(); err != nil {
			return nil, err
		}
	}

	if f.DeviceKeyFile != "" {
		return &session.Store{Path: path, DeviceKeyFile: f.DeviceKeyFile}, nil
	}
	if shared := os.Getenv("ENTE_CLI_SECRETS_PATH"); shared != "" {
		return &session.Store{Path: path, SharedDeviceKeyFile: shared}, nil
	}

	deviceKeyFile := ""
	if cfg != nil {
		deviceKeyFile = cfg.DeviceKeyFile
	}
	return &session.Store{Path: path, DeviceKeyFile: deviceKeyFile}, nil
}

// Session loads the saved session and returns it alongside an authenticated
// client pointed at the server that session came from. userAgent identifies
// the calling binary for server-side logs.
//
// The session's server wins over the config's when they disagree: the session's
// keys only decrypt collections from the server they came from, so asking the
// configured server with them would produce nonsense rather than an obvious
// error.
func Session(f Flags, cfg *config.Config, userAgent string) (*enteapi.Credentials, *enteapi.Client, error) {
	store, err := f.store(cfg)
	if err != nil {
		return nil, nil, err
	}
	creds, endpoint, err := store.Load()
	if err != nil {
		return nil, nil, err
	}

	if cfg != nil && cfg.Account.API != "" && cfg.Account.API != endpoint {
		fmt.Fprintf(os.Stderr,
			"Warning: config names %s but the saved session is from %s; using the session's server. Re-login to switch.\n",
			cfg.Account.API, endpoint)
	}

	client := enteapi.New(endpoint, userAgent)
	client.SetToken(creds.TokenHeader())
	return creds, client, nil
}

// Store loads the raw session store for commands that need the file itself,
// such as login and logout. cfg may be nil.
func (f *Flags) Store(cfg *config.Config) (*session.Store, error) {
	return f.store(cfg)
}
