# ente-public-galleries

**Heavily vibe-coded _and_ a work-in-progress!**

I've been migrating all my galleries to Ente and only part-way through did 
I notice that much as you can have public galleries in Ente, they don't offer
an index you can share with people to go to and see all your public galleries.

So I thought I'd (get Claude and friends to) write this to flesh out the idea
and as an example to append to a feature-request.

## Quick start

This copy-pastes a bunch of the crypto/auth code from the ente CLI tool, so the
mechanics there are similar:

    go build -o ente-public-galleries .
    ./ente-public-galleries login     # once; password (+ 2FA if set)
    ./ente-public-galleries list     # what would be published, and why the rest isn't
    ./ente-public-galleries build
    python3 -m http.server -d out 8001   # then open http://localhost:8001

`build` is non-interactive and safe to run from cron once a session exists.

Discovery is one API call; unchanged albums skip both the file walk and the
cover download, so steady-state runs are nearly free. `build --refresh`
discards the local caches and rebuilds from scratch.

### Docker container

If you're too hip and modern to rsync ./out/ into an Apache DocumentRoot, a
Dockerfile and .dockerignore exists to build an image:

    ./ente-public-galleries build
    docker build -t <registry>/<image>:<tag> .
    docker push <registry>/<image>:<tag>

This will copy the output directory into an nginx container, ignoring any
caches and whatnot.

## Making galleries public and private

There is a `gallery-visibility` tool in the same module:

    go build -o gallery-visibility ./cmd/gallery-visibility
    ./gallery-visibility '^\d{4}-\d{2} '                # show what matches
    ./gallery-visibility -make-public '^\d{4}-\d{2}\s'  # will prompt before changing
    ./gallery-visibility -make-private '^\d{4}-\d{2}\S' # will also prompt before changing

This does it based on the title of the gallery; all my public galleries are
for events and because of a separate Ente issue they're all named beginning 
YYYY-MM; any events I want to keep private I now name beginning YYYY-MMx

     2026-03 Birthday Party
     2026-12 Christmas

are each public, but I keep those galleries from work site visits private

     2026-04x Brentford site-visit
     2025-02x Aldwyc site visit

It's worth noting here that making an album private at Ente deletes the public 
link and so making it public again generates a new one, it doesn't re-enable the
old public one.

This tool right now just creates completely-unrestricted albums, but Ente
does support the idea of password-protected and device-restricted ones.

## Commands

./ente-public-galleries [command]

    login    Authenticate and save a session (use -force to replace one)
    logout   Discard the saved session and its device key
    whoami   Confirm the saved session works (--offline skips the check)
    list     Publishable albums with file/geotag counts; -v names every skip
    covers   Fetch and decrypt cover thumbnails only (the risky path in
             isolation; `file out/thumbs/*.jpg` should say JPEG image data)
    routes   Render route-map thumbnails only (isolates tile-server issues
             from a full build; needs route.enabled, on by default)
    build    Generate the site

./gallery-visibility [options] [regex]

 options:

     -make-public  prompt to make galleries with matching names public
     -make-private prompt to make galleries with matching names private

  without either option just lists matching galleries

## Configuration

`config.yaml.example` serves as the fully documented example config, showing
all options.

It is optional; all it can do is override defaults or add features.

## The device key and headless machines

This project has copy-pasted the auth and crypto bits from the Ente CLI, so 
should function the same.

I haven't yet tested this on a machine with a desktop, but on my machine without
it works just like the CLI, stuffing a key into a file on-disk. 

There are four places this can be, in order of precedence:

1. `--device-key-file` — one-off flag; our own format
2. `ENTE_CLI_SECRETS_PATH` — the same env var that ente CLI checks for (note that
   this tool doesn't delete that on logout)
3. `device_key_file` in config — ours; base64, 0600 enforced
4. the OS keyring — the default on machines that have one (I haven't tested this)

Note the session file itself always lives in `~/.config/ente-public-galleries/`, 
so running from the repo directory (or passing `--config`) is required for the 
config-based paths to be found.

## Status / where things stand

Any justifications for non-obvious implementations are in comments and commit
messages, but shouldn't affect usage. Things that might affect usage include:

Things that have been implemented but not tested:

* Using the OS keyring for auth

Things that might happen in the future:

* Listing somehow of non-dated albums, not sure where to put them in the layout

* Light mode :)

* Some workaround the URLs changing when going public->private->public; perhaps 
  create a bookmarkable link in this? Not sure this is an actual problem though

Things that have been worked-on but decided-against and removed:

* An owner-only 'edit in Ente' button for each album

A small, hidden-by-default control on each card linking straight to that album 
in the owner's own signed-in Ente session, for quickly jumping from the public
page into editing, particularly useful during a migration to Ente. 
Built and then removed, because Ente's webapp doesn't seem to support linking 
to a specific collection
