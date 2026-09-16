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
	Output        string `yaml:"output"`
	Site          Site   `yaml:"site"`
	Map           Map    `yaml:"map"`
	Albums        Albums `yaml:"albums"`
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

// Site holds presentational text for the generated page.
type Site struct {
	Title  string `yaml:"title"`
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
}

// Albums selects and overrides individual albums, keyed by Ente's collection
// ID. IDs are used rather than names because names change and are not unique.
type Albums struct {
	Exclude   []int64            `yaml:"exclude"`
	Order     []int64            `yaml:"order"`
	Overrides map[int64]Override `yaml:"overrides"`
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

// Defaults, applied to any field the config file leaves unset.
const (
	defaultOutput      = "./out"
	defaultTitle       = "Galleries"
	defaultTiles       = "https://tile.openstreetmap.org/{z}/{x}/{y}.png"
	defaultAttribution = `&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors`
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
	if c.Output == "" {
		c.Output = defaultOutput
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
}

func (c *Config) validate() error {
	switch c.Map.Points {
	case PointsAlbums, PointsPhotos, PointsNone:
	default:
		return fmt.Errorf("map.points is %q, expected one of %q, %q or %q",
			c.Map.Points, PointsAlbums, PointsPhotos, PointsNone)
	}
	return nil
}

// MapEnabled reports whether a map should be drawn at all, folding together
// the enabled flag and a points setting of "none".
func (c *Config) MapEnabled() bool {
	return c.Map.Enabled != nil && *c.Map.Enabled && c.Map.Points != PointsNone
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
