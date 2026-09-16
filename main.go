// Command ente-public-galleries generates a static index page for the albums
// you have shared publicly from Ente.
//
// Ente has no "all my public albums" page. This tool logs into your account,
// finds every album with a live public link, and renders a grid of them with
// covers, titles, descriptions and a map, so there is one address to hand out
// instead of many.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/BigRedS/ente-public-galleries/internal/config"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
	"github.com/BigRedS/ente-public-galleries/internal/prompt"
	"github.com/BigRedS/ente-public-galleries/internal/session"
)

// version is reported in the User-Agent so server-side logs can attribute
// traffic to this tool.
const version = "0.1.0-dev"

func main() {
	err := run(os.Args[1:])
	switch {
	case err == nil:
	case errors.Is(err, flag.ErrHelp):
		// The flag package has already printed the usage the user asked
		// for, so asking for help is a success, not a failure.
	case errors.Is(err, context.Canceled):
		// Ctrl-C is not a failure worth a stack of text.
		fmt.Fprintln(os.Stderr, "Cancelled.")
		os.Exit(130)
	default:
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

const usage = `ente-public-galleries generates a static index of your public Ente albums.

Usage:
  ente-public-galleries <command> [flags]

Commands:
  login    Authenticate with Ente and save a session
  logout   Discard the saved session and its device key
  whoami   Show who the saved session belongs to

Run a command with -h for its flags.
`

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return errors.New("no command given")
	}

	command, rest := args[0], args[1:]
	switch command {
	case "login":
		return cmdLogin(ctx, rest)
	case "logout":
		return cmdLogout(rest)
	case "whoami":
		return cmdWhoami(ctx, rest)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

// commonFlags are the flags every command shares: where config and session
// live, and how the device key is kept.
type commonFlags struct {
	configPath    string
	sessionPath   string
	deviceKeyFile string
}

func (f *commonFlags) register(fs *flag.FlagSet) {
	fs.StringVar(&f.configPath, "config", "config.yaml",
		"path to the config file; absent is fine, defaults are used")
	fs.StringVar(&f.sessionPath, "session", "",
		"path to the session file (default: the user config directory)")
	fs.StringVar(&f.deviceKeyFile, "device-key-file", "",
		"keep the session encryption key in this 0600 file instead of the OS keyring; weaker, for headless machines with no keyring daemon")
}

func (f *commonFlags) store() (*session.Store, error) {
	path := f.sessionPath
	if path == "" {
		var err error
		if path, err = session.DefaultPath(); err != nil {
			return nil, err
		}
	}
	return &session.Store{Path: path, DeviceKeyFile: f.deviceKeyFile}, nil
}

func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	var common commonFlags
	common.register(fs)
	email := fs.String("email", "", "email address to log in as (default: account.email from config, else prompted)")
	force := fs.Bool("force", false, "log in again even if a session already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(common.configPath)
	if err != nil {
		return err
	}
	store, err := common.store()
	if err != nil {
		return err
	}

	if !*force {
		// Re-logging in needlessly costs an Argon2 run and, on
		// 2FA accounts, a code. Check first.
		if existing, endpoint, err := store.Load(); err == nil {
			return fmt.Errorf("already logged in as %s on %s; use -force to replace that session, or `logout` to discard it",
				existing.Email, endpoint)
		} else if !errors.Is(err, session.ErrNoSession) {
			// A session that exists but will not decrypt should be
			// reported, not quietly overwritten.
			return err
		}
	}

	prompter := prompt.NewTerminal()

	address := *email
	if address == "" {
		address = cfg.Account.Email
	}
	if address == "" {
		if address, err = prompter.Line("Email address"); err != nil {
			return err
		}
	}
	address = strings.TrimSpace(address)

	client := enteapi.New(cfg.Account.API, userAgent())
	creds, err := enteapi.Login(ctx, client, address, prompter)
	if err != nil {
		return err
	}

	// Prove the credentials work before saving them, so a broken session
	// is never what a later run has to diagnose.
	client.SetToken(creds.TokenHeader())
	details, err := client.FetchUserDetails(ctx)
	if err != nil {
		return fmt.Errorf("verifying the new session: %w", err)
	}

	if err := store.Save(creds, client.Endpoint()); err != nil {
		return err
	}

	fmt.Printf("Logged in as %s.\n", details.Email)
	if common.deviceKeyFile != "" {
		fmt.Fprintf(os.Stderr,
			"\nWarning: the session encryption key is in %s, protected only by file\npermissions. Anything able to read that file and %s can use your\nEnte account. Prefer the OS keyring where one is available.\n",
			common.deviceKeyFile, store.Path)
	}
	return nil
}

func cmdLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	var common commonFlags
	common.register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := common.store()
	if err != nil {
		return err
	}
	existed, err := store.Delete()
	if err != nil {
		return err
	}
	if existed {
		fmt.Println("Session discarded.")
	} else {
		fmt.Println("No saved session to discard.")
	}
	return nil
}

func cmdWhoami(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ContinueOnError)
	var common commonFlags
	common.register(fs)
	offline := fs.Bool("offline", false, "report what the session file says without contacting the server")
	if err := fs.Parse(args); err != nil {
		return err
	}
	store, err := common.store()
	if err != nil {
		return err
	}
	creds, endpoint, err := store.Load()
	if err != nil {
		return err
	}

	fmt.Printf("%s (user %d) on %s\n", creds.Email, creds.UserID, endpoint)
	if *offline {
		return nil
	}

	client := enteapi.New(endpoint, userAgent())
	client.SetToken(creds.TokenHeader())
	details, err := client.FetchUserDetails(ctx)
	if err != nil {
		return fmt.Errorf("the saved session was rejected by the server; log in again: %w", err)
	}
	fmt.Printf("Session is valid; server agrees the account is %s.\n", details.Email)
	return nil
}

func userAgent() string {
	return "ente-public-galleries/" + version
}
