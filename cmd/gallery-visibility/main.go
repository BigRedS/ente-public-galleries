// Command gallery-visibility lists and bulk-changes the public state of
// Ente albums.
//
// It exists because making two hundred dated albums public one at a time in
// the app is not something anyone does twice. A regex selects albums by
// their decrypted Ente title - the raw title, not anything the site config
// rewrites - and the default action is to show the selection and stop, so a
// bad pattern is caught before it publishes anything.
//
// Matching does not mutate; -make-public or -make-private does, after an
// explicit confirmation.
//
// Two behaviours worth knowing before trusting it with a library:
//
//   - Making an album private disables its link permanently. Making it
//     public again mints a new token, so any URL previously handed out
//     stops working and a different one replaces it.
//   - Links it creates are unrestricted: no password, no expiry, no device
//     limit. That matches Ente's own defaults when a link is first made.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/BigRedS/ente-public-galleries/internal/cli"
	"github.com/BigRedS/ente-public-galleries/internal/config"
	"github.com/BigRedS/ente-public-galleries/internal/enteapi"
	"github.com/BigRedS/ente-public-galleries/internal/gallery"

	"golang.org/x/term"
)

const version = "0.1.0-dev"

const usage = `gallery-visibility lists and bulk-changes the public state of Ente albums.

Usage:
  gallery-visibility [flags] REGEX

The regex is matched (unanchored, case-sensitive, Go RE2 syntax) against each
album's decrypted Ente title. Without -make-public or -make-private it only
lists the matches and their current state; either switch, after a
confirmation prompt, changes every matching album.

Flags:
`

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "Cancelled.")
			os.Exit(130)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	fs := flag.NewFlagSet("gallery-visibility", flag.ContinueOnError)
	var flags cli.Flags
	flags.Register(fs)
	makePublic := fs.Bool("make-public", false, "make every matching album public")
	makePrivate := fs.Bool("make-private", false, "make every matching album private (this disables the album's link; re-publishing later mints a new URL)")
	yes := fs.Bool("y", false, "apply changes without the confirmation prompt")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "%s", usage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		fs.Usage()
		return errors.New("exactly one regular expression is required")
	}
	pattern, err := regexp.Compile(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("in regular expression %q: %w", fs.Arg(0), err)
	}

	action, err := decideAction(*makePublic, *makePrivate)
	if err != nil {
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

	rows, ignored, err := buildRows(ctx, client, creds)
	if err != nil {
		return err
	}

	matches := matchRows(rows, pattern)
	printRows(matches, action)

	if action == actionNone {
		fmt.Fprintf(os.Stderr, "\n%d of %d album(s) match. (Also in the library: %s.)\n",
			len(matches), len(rows), describeIgnored(ignored))
		return nil
	}

	fmt.Fprintf(os.Stderr, "\n%d of %d album(s) match; (also in the library: %s.)\n",
		len(matches), len(rows), describeIgnored(ignored))

	changes, unchanged := planChanges(matches, action)
	if len(changes) == 0 {
		fmt.Fprintf(os.Stderr, "Nothing to do: all %d matching album(s) are already %s.\n", len(matches), action)
		return nil
	}

	if !*yes {
		if err := confirm(len(changes), action); err != nil {
			return err
		}
	}

	ok, failed := applyChanges(ctx, client, changes, action)
	fmt.Fprintf(os.Stderr, "\n%d album(s) %s; %d already %s; %d failed.\n",
		ok, action, len(unchanged), action, failed)
	if failed > 0 {
		return fmt.Errorf("%d change(s) failed; the failures are listed above", failed)
	}
	return nil
}

// action is the requested change to an album's public state.
type action string

const (
	actionNone        action = ""
	actionMakePublic  action = "public"
	actionMakePrivate action = "private"
)

func decideAction(makePublic, makePrivate bool) (action, error) {
	if makePublic && makePrivate {
		return actionNone, errors.New("-make-public and -make-private are mutually exclusive")
	}
	if makePublic {
		return actionMakePublic, nil
	}
	if makePrivate {
		return actionMakePrivate, nil
	}
	return actionNone, nil
}

// row is one candidate album: identified, named, and with its current
// link state known.
type row struct {
	ID     int64
	Name   string
	Public bool
}

// ignored counts the collections the tool declined to consider, so the
// operator can tell "no match" from "not even looked at".
type ignored struct {
	deleted, notOwned, notAlbums, undecryptable int
}

type collectionLister interface {
	GetCollections(ctx context.Context, sinceTime int64) ([]enteapi.Collection, error)
}

// buildRows decrypts the name of every album the tool may act on: owned,
// not deleted, of album type. Everything else is counted, not listed, because
// this tool changes account state and its report should account for the
// whole library it scanned.
func buildRows(ctx context.Context, lister collectionLister, creds *enteapi.Credentials) ([]row, ignored, error) {
	collections, err := lister.GetCollections(ctx, 0)
	if err != nil {
		return nil, ignored{}, fmt.Errorf("fetching collections: %w", err)
	}

	var rows []row
	var counts ignored
	for _, c := range collections {
		switch {
		case c.IsDeleted:
			counts.deleted++
			continue
		case c.Owner.ID != creds.UserID:
			counts.notOwned++
			continue
		case c.Type != enteapi.TypeAlbum:
			counts.notAlbums++
			continue
		}

		name, err := gallery.CollectionName(c, creds)
		if err != nil {
			// A name that will not decrypt cannot be matched, so the
			// album cannot be selected safely either. Count it and
			// carry on; one damaged album must not hide the rest.
			counts.undecryptable++
			fmt.Fprintf(os.Stderr, "warning: collection %d name: %v\n", c.ID, err)
			continue
		}
		rows = append(rows, row{ID: c.ID, Name: name, Public: len(c.PublicURLs) > 0})
	}

	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Name != rows[j].Name {
			return rows[i].Name < rows[j].Name
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, counts, nil
}

func matchRows(rows []row, pattern *regexp.Regexp) []row {
	var matches []row
	for _, r := range rows {
		if pattern.MatchString(r.Name) {
			matches = append(matches, r)
		}
	}
	return matches
}

// planChanges splits the selection into albums whose state actually needs
// changing and those already in the target state. Calling the link
// endpoints for an album already in the desired state is harmless but is
// still a mutation request, and an honest report beats a no-op round trip.
func planChanges(matches []row, act action) (changes, unchanged []row) {
	for _, r := range matches {
		if (act == actionMakePublic && r.Public) || (act == actionMakePrivate && !r.Public) {
			unchanged = append(unchanged, r)
			continue
		}
		changes = append(changes, r)
	}
	return changes, unchanged
}

type linkClient interface {
	CreatePublicLink(ctx context.Context, collectionID int64, opts enteapi.LinkOptions) (enteapi.PublicURL, error)
	DisablePublicLink(ctx context.Context, collectionID int64) error
}

// throttleBackoff is how long to wait after the server answers 429. A
// variable so tests do not pay real seconds for it.
var throttleBackoff = 2 * time.Second

// applyChanges mutates each album, retrying briefly when the server asks
// for less haste.
func applyChanges(ctx context.Context, links linkClient, changes []row, act action) (ok, failed int) {
	for _, r := range changes {
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if act == actionMakePublic {
				_, err = links.CreatePublicLink(ctx, r.ID, enteapi.LinkOptions{})
			} else {
				err = links.DisablePublicLink(ctx, r.ID)
			}
			if err == nil || enteapi.StatusCode(err) != 429 {
				break
			}
			// Too many requests: back off and try the same album
			// again rather than abandoning the batch halfway.
			select {
			case <-time.After(throttleBackoff):
			case <-ctx.Done():
				return ok, failed
			}
		}
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  %-8d %s: %v\n", r.ID, r.Name, err)
			continue
		}
		ok++
		fmt.Fprintf(os.Stderr, "  %-8d %s -> %s\n", r.ID, r.Name, act)
	}
	return ok, failed
}

func printRows(matches []row, act action) {
	if len(matches) == 0 {
		fmt.Println("No albums match.")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 8, 2, ' ', 0)
	header := "ID\tNAME\tLINK"
	if act != actionNone {
		header += "\tPLAN"
	}
	fmt.Fprintln(w, header)
	for _, r := range matches {
		state := "private"
		if r.Public {
			state = "public"
		}
		line := fmt.Sprintf("%d\t%s\t%s", r.ID, r.Name, state)
		if act != actionNone {
			switch {
			case (act == actionMakePublic && r.Public) || (act == actionMakePrivate && !r.Public):
				line += "\t(already " + string(act) + ")"
			default:
				line += "\tmake " + string(act)
			}
		}
		fmt.Fprintln(w, line)
	}
	w.Flush()
}

// confirm asks for the full word rather than y/N: this publishes or
// unpublishes photographs in bulk, and a stray keypress should not be able
// to answer it.
func confirm(count int, act action) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return errors.New("refusing to change album visibility without a confirmation prompt; pass -y if this is really intended")
	}
	fmt.Fprintf(os.Stderr, "\nAbout to make %d album(s) %s. Type yes to proceed: ", count, act)
	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading confirmation: %w", err)
	}
	if strings.TrimSpace(answer) != "yes" {
		return errors.New("not confirmed; nothing was changed")
	}
	return nil
}

func describeIgnored(counts ignored) string {
	parts := []string{
		fmt.Sprintf("%d deleted", counts.deleted),
		fmt.Sprintf("%d not albums", counts.notAlbums),
		fmt.Sprintf("%d not owned", counts.notOwned),
	}
	if counts.undecryptable > 0 {
		parts = append(parts, fmt.Sprintf("%d undecryptable", counts.undecryptable))
	}
	return strings.Join(parts, ", ")
}

func userAgent() string {
	return "gallery-visibility/" + version
}
