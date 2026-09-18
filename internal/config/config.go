// Package config loads the YAML file that tunes site generation.
//
// Ente is the source of truth for album titles, descriptions and covers, so
// everything here is optional: a config file exists to override Ente, to
// exclude albums, and to say where output goes. A run with no config file at
// all is valid.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the parsed config file.
type Config struct {
	Account Account `yaml:"account"`
	// DeviceKeyFile keeps the session encryption key in this 0600 file
	// instead of the OS keyring. For machines without a keyring daemon,
	// which is any headless box. A leading ~/ is expanded.
	DeviceKeyFile string `yaml:"device_key_file"`
	// Output is where the generated site is written.
	Output string `yaml:"output"`
	// Cache is where the per-album file indexes live. Deleting it costs a
	// re-walk of every album, nothing else.
	Cache  string `yaml:"cache"`
	Site   Site   `yaml:"site"`
	Map    Map    `yaml:"map"`
	Route  Route  `yaml:"route"`
	Albums Albums `yaml:"albums"`
}

// Account locates the Ente server and identifies who to log in as.
type Account struct {
	Email string `yaml:"email"`
	// API overrides the API endpoint, for self-hosted servers.
	API string `yaml:"api"`
	// AlbumURLBase rewrites the host of the public links Ente reports.
	// Needed when albums are served from a custom domain, since the server
	// still hands back its own configured album host.
	AlbumURLBase string `yaml:"album_url_base"`
}

// Route configures the per-album route-map thumbnail: a line connecting an
// album's geotagged photos in time order, drawn over a real OSM basemap and
// rendered once at build time. It deliberately reuses Map.Tiles and
// Map.Attribution rather than declaring a second tile-server config - it is
// the same physical basemap, just rendered to a static image instead of a
// live Leaflet map, and a second tile URL would be one more thing to keep
// in sync. Route is independent of Map.Enabled: a site can have route
// badges without the live map, or the live map without route badges.
type Route struct {
	Enabled *bool `yaml:"enabled"`
	// Width and Height are the rendered PNG's pixel dimensions. The same
	// image is shown small in the card and CSS-enlarged on hover, so this
	// should be sized for the larger, on-hover display, not the small one.
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

// Site holds presentational text for the generated page.
//
// Subtitle and Footer are rendered as raw HTML, not escaped text: both are
// written by whoever owns the site (this is a config file, not visitor
// input), so the usual XSS concern doesn't apply, and it's the simplest way
// to let a link or bit of emphasis into either without pulling in a Markdown
// parser for what's normally a line or two of text.
type Site struct {
	Title string `yaml:"title"`
	// Subtitle sits under the title, at the top of the page.
	Subtitle string `yaml:"subtitle"`
	// Footer sits at the bottom of the page, under the gallery grid.
	Footer string `yaml:"footer"`
}

// Map configures the Leaflet map.
type Map struct {
	Enabled *bool `yaml:"enabled"`
	// Points selects what each marker represents: "albums", "photos" or
	// "none". Photos publishes every geotagged photo's coordinates to a
	// world-readable file, so it is opt-in.
	Points      string `yaml:"points"`
	Tiles       string `yaml:"tiles"`
	Attribution string `yaml:"attribution"`
	// LeafletJS and LeafletCSS are where the Leaflet library itself comes
	// from. They default to a CDN; pointing them at self-hosted copies
	// removes the last third party from the generated page.
	LeafletJS  string `yaml:"leaflet_js"`
	LeafletCSS string `yaml:"leaflet_css"`
}

// Albums selects and overrides individual albums, keyed by Ente's collection
// ID. IDs are used rather than names because names change and are not unique.
type Albums struct {
	Exclude   []int64            `yaml:"exclude"`
	Order     []int64            `yaml:"order"`
	Overrides map[int64]Override `yaml:"overrides"`
	// TitleRegex is a sed-style substitution applied to album titles for
	// display, for stripping the parts Ente titles carry that a gallery
	// page should not show: 's/^\d\d\d\d-\d\d\///' drops a leading
	// YYYY-MM. The pattern is Go regexp (RE2) syntax; the replacement may
	// use $1-style group references. Without a trailing g flag only the
	// first match is replaced, as in sed. A substitution that empties the
	// title leaves the original in place, so a pattern cannot blank the
	// page by accident.
	TitleRegex string `yaml:"title_regex"`

	// SortBy chooses what orders the (unpinned) albums: "date", "name" or
	// "size" (photo count - Ente has no separate notion of album size).
	SortBy string `yaml:"sort_by"`
	// SortOrder is "asc" or "desc", applied whatever SortBy is.
	SortOrder string `yaml:"sort_order"`
	// DateSource picks which photo stands for "the album's date" when
	// SortBy is "date": "first", "last" or "midpoint" between them. Ente
	// itself has no per-album date.
	DateSource string `yaml:"date_source"`
	// GroupByYear draws a year heading above the first album of each new
	// year. Only meaningful when SortBy is "date".
	GroupByYear *bool `yaml:"group_by_year"`

	// titleRe and friends are the compiled form of TitleRegex, set by
	// Load. The zero value of Albums is safe to use without them.
	titleRe          *regexp.Regexp
	titleReplacement string
	titleGlobal      bool
}

// CleanTitle applies the configured title substitution, if any.
//
// It is a method on Albums rather than a free function so the compiled
// pattern travels with the config that declared it, and so a hand-built
// Albums with no regex is naturally a no-op.
func (a *Albums) CleanTitle(title string) string {
	if a.titleRe == nil {
		return title
	}
	result := title
	if a.titleGlobal {
		result = a.titleRe.ReplaceAllString(title, a.titleReplacement)
	} else if match := a.titleRe.FindStringSubmatchIndex(title); match != nil {
		// Go's regexp has no replace-first; expand the replacement
		// against the single match and splice it in by hand.
		expanded := a.titleRe.ExpandString(nil, a.titleReplacement, title, match)
		result = title[:match[0]] + string(expanded) + title[match[1]:]
	}
	if strings.TrimSpace(result) == "" {
		// A pattern that swallows the whole title would leave a blank
		// card heading, which reads as breakage; the original title is
		// the better failure.
		return title
	}
	return result
}

// parseSubstitution splits a sed-style s/pattern/replacement/flags command.
// A backslash-escaped delimiter (\/) is unescaped to a plain / in whichever
// field it appears: that is what the escape means in sed, the pattern side
// wants a literal slash, and the replacement side must not carry a stray
// backslash into Go's $-syntax replacement. Other backslash sequences pass
// through untouched for the regexp engine to interpret.
// The flags field may be empty or g.
func parseSubstitution(substitution string) (pattern, replacement string, global bool, err error) {
	if !strings.HasPrefix(substitution, "s/") {
		return "", "", false, errors.New(`must look like s/pattern/replacement/ (start with "s/")`)
	}
	body := substitution[2:]

	var fields []string
	var current strings.Builder
	for i := 0; i < len(body); i++ {
		switch body[i] {
		case '\\':
			if i+1 < len(body) {
				i++
				if body[i] == '/' {
					// Escaped delimiter: the field wants a
					// literal slash, not a split.
					current.WriteByte('/')
				} else {
					current.WriteByte('\\')
					current.WriteByte(body[i])
				}
			} else {
				current.WriteByte('\\')
			}
		case '/':
			fields = append(fields, current.String())
			current.Reset()
		default:
			current.WriteByte(body[i])
		}
	}
	// The final field is only committed by a closing delimiter, as in sed:
	// s/a/b has no flags field and is a syntax error.
	fields = append(fields, current.String())
	if len(fields) != 3 {
		return "", "", false, fmt.Errorf("expected pattern/replacement/flags, got %d field(s); a missing final / is the usual cause", len(fields))
	}

	pattern, replacement, flags := fields[0], fields[1], fields[2]
	switch flags {
	case "":
	case "g":
		global = true
	default:
		return "", "", false, fmt.Errorf("unknown flag %q; only g is supported", flags)
	}
	return pattern, replacement, global, nil
}

// Override replaces what Ente reports for one album. Empty fields defer to
// Ente rather than blanking the value.
type Override struct {
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

// Point choices for Map.Points.
const (
	PointsAlbums = "albums"
	PointsPhotos = "photos"
	PointsNone   = "none"
)

// Choices for Albums.SortBy.
const (
	SortByDate = "date"
	SortByName = "name"
	SortBySize = "size"
)

// Choices for Albums.SortOrder.
const (
	SortAsc  = "asc"
	SortDesc = "desc"
)

// Choices for Albums.DateSource.
const (
	DateFirst    = "first"
	DateLast     = "last"
	DateMidpoint = "midpoint"
)

// Defaults, applied to any field the config file leaves unset.
const (
	defaultOutput      = "./out"
	defaultCache       = "./.cache"
	defaultTitle       = "Galleries"
	defaultTiles       = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
	defaultAttribution = `&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors`
	defaultLeafletJS   = "https://unpkg.com/leaflet@1.9.4/dist/leaflet.js"
	defaultLeafletCSS  = "https://unpkg.com/leaflet@1.9.4/dist/leaflet.css"
	defaultRouteWidth  = 320
	defaultRouteHeight = 320
)

// Load reads path. A missing file yields defaults, because the tool is usable
// with no configuration at all; an unreadable or malformed one is an error,
// since that is a mistake rather than a choice.
func Load(path string) (*Config, error) {
	var cfg Config

	body, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// Leave cfg zero and fall through to defaults.
	case err != nil:
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	default:
		// KnownFields makes a mistyped key an error rather than a
		// setting that silently does nothing, which is the difference
		// between noticing `discription:` now and wondering why an
		// override never applied later.
		dec := yaml.NewDecoder(bytes.NewReader(body))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("parsing config %s: %w", path, err)
		}
	}

	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("invalid config %s: %w", path, err)
	}
	return &cfg, nil
}

func (c *Config) applyDefaults() {
	c.DeviceKeyFile = expandTilde(c.DeviceKeyFile)
	c.Output = expandTilde(c.Output)
	c.Cache = expandTilde(c.Cache)
	if c.Output == "" {
		c.Output = defaultOutput
	}
	if c.Cache == "" {
		c.Cache = defaultCache
	}
	if c.Site.Title == "" {
		c.Site.Title = defaultTitle
	}
	if c.Map.Enabled == nil {
		enabled := true
		c.Map.Enabled = &enabled
	}
	if c.Map.Points == "" {
		c.Map.Points = PointsAlbums
	}
	if c.Map.Tiles == "" {
		c.Map.Tiles = defaultTiles
	}
	if c.Map.Attribution == "" {
		c.Map.Attribution = defaultAttribution
	}
	if c.Map.LeafletJS == "" {
		c.Map.LeafletJS = defaultLeafletJS
	}
	if c.Map.LeafletCSS == "" {
		c.Map.LeafletCSS = defaultLeafletCSS
	}
	if c.Albums.SortBy == "" {
		c.Albums.SortBy = SortByDate
	}
	if c.Albums.SortOrder == "" {
		// One default regardless of SortBy: whoever wants A-Z or
		// fewest-first sets sort_order: asc explicitly.
		c.Albums.SortOrder = SortDesc
	}
	if c.Albums.DateSource == "" {
		c.Albums.DateSource = DateLast
	}
	if c.Albums.GroupByYear == nil {
		grouped := true
		c.Albums.GroupByYear = &grouped
	}
	if c.Route.Enabled == nil {
		enabled := true
		c.Route.Enabled = &enabled
	}
	if c.Route.Width == 0 {
		c.Route.Width = defaultRouteWidth
	}
	if c.Route.Height == 0 {
		c.Route.Height = defaultRouteHeight
	}
}

func (c *Config) validate() error {
	switch c.Map.Points {
	case PointsAlbums, PointsPhotos, PointsNone:
	default:
		return fmt.Errorf("map.points is %q, expected one of %q, %q or %q",
			c.Map.Points, PointsAlbums, PointsPhotos, PointsNone)
	}

	switch c.Albums.SortBy {
	case SortByDate, SortByName, SortBySize:
	default:
		return fmt.Errorf("albums.sort_by is %q, expected one of %q, %q or %q",
			c.Albums.SortBy, SortByDate, SortByName, SortBySize)
	}
	switch c.Albums.SortOrder {
	case SortAsc, SortDesc:
	default:
		return fmt.Errorf("albums.sort_order is %q, expected %q or %q", c.Albums.SortOrder, SortAsc, SortDesc)
	}
	switch c.Albums.DateSource {
	case DateFirst, DateLast, DateMidpoint:
	default:
		return fmt.Errorf("albums.date_source is %q, expected one of %q, %q or %q",
			c.Albums.DateSource, DateFirst, DateLast, DateMidpoint)
	}

	if c.Route.Width <= 0 || c.Route.Height <= 0 {
		return fmt.Errorf("route.width/height are %d/%d, both must be positive", c.Route.Width, c.Route.Height)
	}

	if c.Albums.TitleRegex != "" {
		pattern, replacement, global, err := parseSubstitution(c.Albums.TitleRegex)
		if err != nil {
			return fmt.Errorf("albums.title_regex %q: %w", c.Albums.TitleRegex, err)
		}
		// Compile here rather than at first use, so a bad pattern is a
		// config error at startup, not a surprise mid-build.
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("albums.title_regex %q: %w", c.Albums.TitleRegex, err)
		}
		c.Albums.titleRe = re
		c.Albums.titleReplacement = replacement
		c.Albums.titleGlobal = global
	}
	return nil
}

// MapEnabled reports whether a map should be drawn at all, folding together
// the enabled flag and a points setting of "none".
func (c *Config) MapEnabled() bool {
	return c.Map.Enabled != nil && *c.Map.Enabled && c.Map.Points != PointsNone
}

// GroupByYearEnabled reports whether year headings should be drawn. It is
// only meaningful when SortBy is "date"; callers check that separately.
func (a *Albums) GroupByYearEnabled() bool {
	return a.GroupByYear != nil && *a.GroupByYear
}

// RouteEnabled reports whether route-map thumbnails should be generated.
func (c *Config) RouteEnabled() bool {
	return c.Route.Enabled != nil && *c.Route.Enabled
}

// IsExcluded reports whether an album has been explicitly excluded.
func (a *Albums) IsExcluded(id int64) bool {
	for _, excluded := range a.Exclude {
		if excluded == id {
			return true
		}
	}
	return false
}

// expandTilde resolves a leading ~ to the user's home directory, so configs
// stay portable across machines with different usernames. Anything else is
// returned untouched, including a ~ that belongs to someone else's expansion.
func expandTilde(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}
