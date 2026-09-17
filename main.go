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
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/BigRedS/ente-public-galleries/internal/cli"
	"github.com/BigRedS/ente-public-galleries/internal/config"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
	"github.com/BigRedS/ente-public-galleries/internal/gallery"
	"github.com/BigRedS/ente-public-galleries/internal/prompt"
	"github.com/BigRedS/ente-public-galleries/internal/render"
	"github.com/BigRedS/ente-public-galleries/internal/session"
	"github.com/BigRedS/ente-public-galleries/internal/staticmap"
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
  list     Show the albums that would be published
  covers   Fetch cover thumbnails for the publishable albums
  routes   Render route-map thumbnails for the publishable albums
  build    Generate the site

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
	case "list":
		return cmdList(ctx, rest)
	case "covers":
		return cmdCovers(ctx, rest)
	case "routes":
		return cmdRoutes(ctx, rest)
	case "build":
		return cmdBuild(ctx, rest)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return nil
	default:
		fmt.Fprint(os.Stderr, usage)
		return fmt.Errorf("unknown command %q", command)
	}
}

func cmdLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	email := fs.String("email", "", "email address to log in as (default: account.email from config, else prompted)")
	force := fs.Bool("force", false, "log in again even if a session already exists")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	store, err := flags.Store(cfg)
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
	switch {
	case store.SharedDeviceKeyFile != "":
		fmt.Fprintf(os.Stderr,
			"\nNote: this session is encrypted with the ente CLI's device key at %s,\nso both tools now depend on that file. Deleting it (or letting the CLI\nregenerate it) means logging in here again.\n",
			store.SharedDeviceKeyFile)
	case store.DeviceKeyFile != "":
		fmt.Fprintf(os.Stderr,
			"\nWarning: the session encryption key is in %s, protected only by file\npermissions. Anything able to read that file and %s can use your\nEnte account. Prefer the OS keyring where one is available.\n",
			store.DeviceKeyFile, store.Path)
	}
	return nil
}

func cmdLogout(args []string) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	// The config is loaded even to log out, because the device key's
	// location comes from it: logging out must remove the key wherever
	// login actually put it.
	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	store, err := flags.Store(cfg)
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
	var flags cli.Flags
	flags.Register(fs)
	offline := fs.Bool("offline", false, "report what the session file says without contacting the server")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	store, err := flags.Store(cfg)
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

func cmdList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	verbose := fs.Bool("v", false, "list every skipped album and why, instead of a summary")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	creds, client, err := cli.Session(flags, cfg, userAgent())
	if err != nil {
		return err
	}

	albums, indexes, skips, err := discoverAndSync(ctx, client, creds, cfg)
	if err != nil {
		return err
	}

	counts := make([]albumCounts, len(albums))
	for i, album := range albums {
		for _, f := range indexes[album.ID].Files {
			counts[i].files++
			if f.Lat != nil {
				counts[i].geo++
			}
		}
	}

	printAlbums(albums, counts, &cfg.Albums)
	printSkips(skips, *verbose, &cfg.Albums)
	return nil
}

// discoverAndSync is the pipeline every publishing command shares: find the
// publishable albums, then bring each one's file index up to date. It returns
// the albums, their indexes keyed by collection ID, and the discovery skips
// for whoever wants to report them.
func discoverAndSync(ctx context.Context, client *enteapi.Client, creds *enteapi.Credentials, cfg *config.Config) ([]gallery.Album, map[int64]*gallery.FileIndex, []gallery.Skip, error) {
	albums, skips, err := (&gallery.Discoverer{
		Fetcher:      client,
		Creds:        creds,
		AlbumURLBase: cfg.Account.AlbumURLBase,
		Excluded:     cfg.Albums.IsExcluded,
	}).Discover(ctx, time.Now())
	if err != nil {
		return nil, nil, nil, err
	}

	syncer := &gallery.Syncer{Files: client, CacheDir: cfg.Cache}
	indexes := make(map[int64]*gallery.FileIndex, len(albums))
	for _, album := range albums {
		index, err := syncer.SyncAlbum(ctx, album)
		if err != nil {
			return nil, nil, nil, err
		}
		indexes[album.ID] = index
	}
	return albums, indexes, skips, nil
}

// cmdBuild generates the site: discovery, file sync, cover thumbnails, and
// the index page itself.
func cmdBuild(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	refresh := fs.Bool("refresh", false, "discard the cached file indexes and cover thumbnails, then rebuild everything from scratch")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	creds, client, err := cli.Session(flags, cfg, userAgent())
	if err != nil {
		return err
	}

	if *refresh {
		// Both of these are derived data: discarding them costs a full
		// re-walk and re-download, and nothing else. That is exactly
		// what a from-scratch rebuild means; discovery itself is always
		// full and needs no resetting.
		for _, dir := range []string{
			filepath.Join(cfg.Cache, "albums"),
			filepath.Join(cfg.Output, "thumbs"),
			filepath.Join(cfg.Output, "routes"),
		} {
			if err := os.RemoveAll(dir); err != nil {
				return err
			}
		}
		// cfg.Cache/tiles is deliberately left alone: it holds raw OSM
		// tile data, not this tool's own derived output, and wiping it on
		// every refresh would mean re-downloading a world's worth of
		// tiles and would be unkind to the tile server.
		fmt.Fprintf(os.Stderr, "Refresh: discarded cached file indexes, cover thumbnails and route images.\n")
	}

	albums, indexes, skips, err := discoverAndSync(ctx, client, creds, cfg)
	if err != nil {
		return err
	}
	printSkips(skips, false, &cfg.Albums)

	covers := &gallery.Covers{Fetcher: client, Dir: filepath.Join(cfg.Output, "thumbs")}
	coverFailures := 0
	for _, album := range albums {
		if err := covers.Sync(ctx, album, indexes[album.ID]); err != nil {
			fmt.Fprintf(os.Stderr, "warning: cover for %q: %v (the album will use a placeholder)\n", album.Name, err)
			coverFailures++
		}
	}

	routes, err := newRoutes(cfg, userAgent())
	if err != nil {
		return err
	}
	routeFailures := 0
	if routes != nil {
		for _, album := range albums {
			if err := routes.Sync(ctx, album, indexes[album.ID]); err != nil {
				fmt.Fprintf(os.Stderr, "warning: route image for %q: %v (the album will show no route)\n", album.Name, err)
				routeFailures++
			}
		}
	}

	site := render.Assemble(cfg, cfg.Output, albums, indexes)
	if err := render.Render(cfg.Output, site); err != nil {
		return err
	}

	fmt.Printf("Built %s: %d album(s)", cfg.Output, len(site.Cards))
	if site.Map != nil {
		fmt.Printf(", %d map point(s)", len(site.Points))
	}
	if coverFailures > 0 {
		fmt.Printf(", %d cover(s) failed", coverFailures)
	}
	if routeFailures > 0 {
		fmt.Printf(", %d route(s) failed", routeFailures)
	}
	fmt.Println()
	return nil
}

// albumCounts is the per-album tally list prints.
type albumCounts struct {
	files int
	geo   int
}

// cmdCovers exists because the thumbnail path is the least-trusted part of
// the pipeline: it is the piece where server, storage and crypto all have to
// agree, and where an earlier design expected trouble. It runs exactly what
// build will run, and stops before rendering, so a failure here is a failure
// in fetching and nothing else.
func cmdCovers(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("covers", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	refresh := fs.Bool("refresh", false, "refetch covers even when fresh on disk")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	creds, client, err := cli.Session(flags, cfg, userAgent())
	if err != nil {
		return err
	}

	albums, indexes, _, err := discoverAndSync(ctx, client, creds, cfg)
	if err != nil {
		return err
	}

	covers := &gallery.Covers{Fetcher: client, Dir: filepath.Join(cfg.Output, "thumbs")}

	failed := 0
	for _, album := range albums {
		coverPath := filepath.Join(covers.Dir, fmt.Sprintf("%d.jpg", album.ID))
		if *refresh {
			if err := os.Remove(coverPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}

		if err := covers.Sync(ctx, album, indexes[album.ID]); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", album.Name, err)
			failed++
			continue
		}
		if info, err := os.Stat(coverPath); err == nil {
			fmt.Printf("  %-40s %s (%d bytes)\n", album.Name, coverPath, info.Size())
		}
	}

	fmt.Fprintf(os.Stderr, "%d album(s), %d cover failure(s).\n", len(albums), failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d covers failed; run with the failures above for detail", failed, len(albums))
	}
	return nil
}

// newRoutes builds the gallery.Routes for the site, wiring in the
// internal/staticmap renderer built from the site's own tile config
// (cfg.Map.Tiles/Attribution - see internal/config's Route doc comment for
// why route rendering deliberately reuses the map's tile config rather than
// declaring a second one). Returns nil, nil when route rendering is
// disabled, so callers just skip the sync loop.
func newRoutes(cfg *config.Config, agent string) (*gallery.Routes, error) {
	if !cfg.RouteEnabled() {
		return nil, nil
	}
	renderer, err := staticmap.New(cfg.Map.Tiles, cfg.Map.Attribution, cfg.Route.Width, cfg.Route.Height,
		agent, filepath.Join(cfg.Cache, "tiles"))
	if err != nil {
		return nil, fmt.Errorf("configuring route renderer: %w", err)
	}
	return &gallery.Routes{Renderer: renderer, Dir: filepath.Join(cfg.Output, "routes")}, nil
}

// cmdRoutes exists for the same reason cmdCovers does: route rendering
// depends on a new external service (an OSM tile server) with its own
// failure modes, separate from the cover-thumbnail fetch, and this runs
// exactly what build will run, stopping before rendering the page, so a
// failure here is a failure in tile-fetching/drawing and nothing else.
func cmdRoutes(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("routes", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	refresh := fs.Bool("refresh", false, "re-render routes even when fresh on disk")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(flags.ConfigPath)
	if err != nil {
		return err
	}
	creds, client, err := cli.Session(flags, cfg, userAgent())
	if err != nil {
		return err
	}

	albums, indexes, _, err := discoverAndSync(ctx, client, creds, cfg)
	if err != nil {
		return err
	}

	routes, err := newRoutes(cfg, userAgent())
	if err != nil {
		return err
	}
	if routes == nil {
		fmt.Fprintln(os.Stderr, "route.enabled is false; nothing to do.")
		return nil
	}

	failed := 0
	for _, album := range albums {
		routePath := filepath.Join(routes.Dir, fmt.Sprintf("%d.png", album.ID))
		if *refresh {
			if err := os.Remove(routePath); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}

		if err := routes.Sync(ctx, album, indexes[album.ID]); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", album.Name, err)
			failed++
			continue
		}
		if info, err := os.Stat(routePath); err == nil {
			fmt.Printf("  %-40s %s (%d bytes)\n", album.Name, routePath, info.Size())
		}
	}

	fmt.Fprintf(os.Stderr, "%d album(s), %d route failure(s).\n", len(albums), failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d routes failed; run with the failures above for detail", failed, len(albums))
	}
	return nil
}

func printAlbums(albums []gallery.Album, counts []albumCounts, albumsCfg *config.Albums) {
	if len(albums) == 0 {
		fmt.Println("No publishable albums found.")
		return
	}

	// Titles print as the site would show them, cleaned by the same
	// substitution, so the listing previews the page rather than Ente's
	// raw naming.
	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tEXPIRES\tFILES\tLINK")
	for i, a := range albums {
		expires := "-"
		if !a.Expires.IsZero() {
			expires = a.Expires.Format("2006-01-02")
		}
		files := "-"
		if i < len(counts) {
			files = fmt.Sprintf("%d (%d geo)", counts[i].files, counts[i].geo)
		}
		fmt.Fprintf(w, "%d\t%s%s\t%s\t%s\t%s\n", a.ID, albumsCfg.CleanTitle(a.Name), notesFor(a), expires, files, a.ShareURL)
	}
	w.Flush()

	fmt.Fprintf(os.Stderr, "\n%d album(s) publishable.\n", len(albums))
}

// notesFor renders the in-table annotations: a password flag for albums whose
// visitors will meet a prompt, and a description snippet where one exists.
func notesFor(a gallery.Album) string {
	notes := ""
	if a.PasswordProtected {
		notes += " [password]"
	}
	if a.Description != "" {
		snippet := []rune(a.Description)
		if len(snippet) > 40 {
			snippet = append(snippet[:40], []rune("...")...)
		}
		notes += " (" + string(snippet) + ")"
	}
	return notes
}

func printSkips(skips []gallery.Skip, verbose bool, albumsCfg *config.Albums) {
	if len(skips) == 0 {
		return
	}

	fmt.Fprintln(os.Stderr, "Not published:")
	if verbose {
		for _, s := range skips {
			fmt.Fprintf(os.Stderr, "  %-8d %s: %s\n", s.ID, albumsCfg.CleanTitle(s.Name), s.Reason)
		}
		fmt.Fprintln(os.Stderr)
	}

	byReason := make(map[string]int)
	for _, s := range skips {
		byReason[s.Reason]++
	}
	reasons := make([]string, 0, len(byReason))
	for reason := range byReason {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		fmt.Fprintf(os.Stderr, "  %d %s\n", byReason[reason], reason)
	}
}
