# ente-public-galleries

**Heavily vibe-coded _and_ a work-in-progress!**

Ente has no page that shows all of your publicly-shared albums in one place.
This tool fills that gap: it logs into your Ente account, finds every album
with a live public link, and generates a static index page — cover thumbnails,
titles, descriptions, and a map — linking out to Ente's own album pages for
viewing. It was built to replace Piwigo's gallery index during a migration to
Ente.

AGPL-3.0, because it incorporates crypto code from Ente's own CLI. See
`NOTICE` for exactly what came from where.

## Quick start

    go build -o ente-public-galleries .
    ./ente-public-galleries login     # once; password (+ 2FA if set)
    ./ente-public-galleries list     # what would be published, and why the rest isn't
    ./ente-public-galleries build
    python3 -m http.server -d out 8001   # then open http://localhost:8001

Serve the output over HTTP, not `file://` — the map loads `points.json` with
`fetch()`, which browsers block on the `file://` scheme. The cards and covers
work either way; only the map needs a server.

`build` is non-interactive and safe to run from cron once a session exists.
Discovery is one API call; unchanged albums skip both the file walk and the
cover download, so steady-state runs are nearly free. `build --refresh`
discards the local caches and rebuilds from scratch.

## Commands

Bulk visibility changes are a separate tool in the same module:

    go build -o gallery-visibility ./cmd/gallery-visibility
    ./gallery-visibility '^\d{4}-\d{2} '            # show what matches
    ./gallery-visibility -make-public '^\d{4}-\d{2} '  # after a typed confirmation
    ./gallery-visibility -make-private '...'

It matches Go RE2 against decrypted album titles and is read-only unless a
switch is given. Two things to know before using it: making an album private
disables its link permanently (re-publishing mints a new URL, so previously
shared links die), and links it creates are unrestricted — no password, no
expiry, no device limit, matching Ente's own defaults.

## Commands

    login    Authenticate and save a session (use -force to replace one)
    logout   Discard the saved session and its device key
    whoami   Confirm the saved session works (--offline skips the check)
    list     Publishable albums with file/geotag counts; -v names every skip
    covers   Fetch and decrypt cover thumbnails only (the risky path in
             isolation; `file out/thumbs/*.jpg` should say JPEG image data)
    build    Generate the site

## Configuration

Copy `config.yaml.example` to `config.yaml` and edit; every key, default and
gotcha is documented there. The file is optional — Ente itself is the source
of truth for album titles, descriptions and covers, so config exists only to
override (including `albums.title_regex` for stripping date prefixes from
titles), exclude, order, and point at non-default paths.

Unknown config keys are errors, so a typo cannot silently disable anything.

## The device key and headless machines

The saved session (API token + account keys — full account access) is
encrypted with a 32-byte device key that never sits in the session file. Where
that key lives, in order of precedence:

1. `--device-key-file` — one-off flag; our own format, base64 in a 0600 file
2. `ENTE_CLI_SECRETS_PATH` — the **ente CLI's own key file**, so one key
   serves both tools; the CLI's raw-bytes format, and `logout` deliberately
   never deletes it (the CLI's stored accounts depend on it too)
3. `device_key_file` in config — ours; base64, 0600 enforced
4. the OS keyring — the default on machines that have one

On a headless box, set `ENTE_CLI_SECRETS_PATH` if you also run the ente CLI
there, else `device_key_file` in config.yaml. Note the session file itself
always lives in `~/.config/ente-public-galleries/`, so running from the repo
directory (or passing `--config`) is required for the config-based paths to
be found.

## Status / where things stand

Live-verified against hosted Ente: login (SRP, headless), discovery, link
reconstruction (generated links open the right album), skip accounting.

Cover fetch/decrypt and the rendered page are fixture-tested but have **not
been exercised against the real server** — run `covers` and `build` before
relying on them, and compare `list`'s file counts against the Ente UI. If
cover decryption ever fails, the error will name the album and the reason;
the album gets a placeholder tile rather than a broken image.

Design rationale for the non-obvious decisions (why discovery never goes
incremental, why no key material is ever cached, why the `/public-collection`
endpoints are never touched) lives in code comments and commit messages, by
policy: docs here would drift, comments can't.
