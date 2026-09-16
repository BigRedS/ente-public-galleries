// Package render turns discovered albums into the static site.
//
// It is deliberately pure presentation: it receives already-decrypted album
// data and writes files, with no API, crypto or caching concerns. That keeps
// the templates the only place HTML exists and makes the whole package
// testable without fixtures for anything but its own inputs.
package render

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"

	"github.com/BigRedS/ente-public-galleries/internal/config"
	"github.com/BigRedS/ente-public-galleries/internal/gallery"
)

//go:embed templates/index.html assets/style.css
var files embed.FS

// MapData carries everything the template needs to draw the map. A nil
// MapData in SiteData means no map at all.
type MapData struct {
	Tiles       string
	Attribution string
	LeafletJS   string
	LeafletCSS  string
}

// Card is one album on the index page.
type Card struct {
	Name        string
	Description string
	ShareURL    string
	// ThumbPath is relative to the site root; empty when there is no cover.
	ThumbPath string
	HasThumb  bool
	// Expires is a pre-formatted date, or empty when the link never does.
	Expires   string
	Password  bool
	FileCount int
}

// Point is one map marker.
type Point struct {
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	Name string  `json:"name"`
	URL  string  `json:"url"`
}

// SiteData is the template's entire world.
type SiteData struct {
	Title  string
	Footer string
	Map    *MapData
	Cards  []Card
	Points []Point
}

// Assemble maps albums and their file indexes onto template data, applying
// the config's presentation choices: title and description overrides,
// ordering, and which dots the map gets.
//
// output is the site root, used only to check which cover thumbnails exist:
// an album whose cover failed to fetch gets a placeholder card rather than a
// broken image, and that has to be known at template time.
func Assemble(cfg *config.Config, output string, albums []gallery.Album, indexes map[int64]*gallery.FileIndex) SiteData {
	site := SiteData{Title: cfg.Site.Title, Footer: cfg.Site.Footer}

	if cfg.MapEnabled() {
		site.Map = &MapData{
			Tiles:       cfg.Map.Tiles,
			Attribution: cfg.Map.Attribution,
			LeafletJS:   cfg.Map.LeafletJS,
			LeafletCSS:  cfg.Map.LeafletCSS,
		}
	}

	ordered := orderAlbums(albums, cfg)
	for _, album := range ordered {
		card := Card{
			// The displayed title is the cleaned one; the map popups
			// and any diagnostics use the same string via card.Name.
			Name:        cfg.Albums.CleanTitle(album.Name),
			Description: album.Description,
			ShareURL:    album.ShareURL,
			ThumbPath:   fmt.Sprintf("thumbs/%d.jpg", album.ID),
			Password:    album.PasswordProtected,
		}
		if _, err := os.Stat(filepath.Join(output, "thumbs", fmt.Sprintf("%d.jpg", album.ID))); err == nil {
			card.HasThumb = true
		}
		if !album.Expires.IsZero() {
			card.Expires = album.Expires.Format("2 Jan 2006")
		}

		if override, ok := cfg.Albums.Overrides[album.ID]; ok {
			if override.Title != "" {
				card.Name = override.Title
			}
			if override.Description != "" {
				card.Description = override.Description
			}
		}

		index := indexes[album.ID]
		if index != nil {
			card.FileCount = len(index.Files)

			switch {
			case cfg.Map.Points == config.PointsAlbums:
				// One dot per album, at its centre of mass.
				if site.Map != nil {
					if lat, lon, ok := centroid(index); ok {
						site.Points = append(site.Points, Point{Lat: lat, Lon: lon, Name: card.Name, URL: album.ShareURL})
					}
				}
			case cfg.Map.Points == config.PointsPhotos:
				// One dot per geotagged photo. This publishes the location of
				// every photo to anyone who loads the page, which is why it
				// is opt-in.
				if site.Map != nil {
					for _, f := range index.Files {
						if f.Lat != nil && f.Lon != nil {
							site.Points = append(site.Points, Point{Lat: *f.Lat, Lon: *f.Lon, Name: card.Name, URL: album.ShareURL})
						}
					}
				}
			}
		}

		site.Cards = append(site.Cards, card)
	}

	// A map with nothing to show would be a blank grey rectangle, which
	// reads as breakage rather than emptiness.
	if site.Map != nil && len(site.Points) == 0 {
		site.Map = nil
	}
	return site
}

// orderAlbums applies the two-level sort: albums pinned in config order come
// first, then Ente's own manual order for the rest, then by name. Pinned
// albums not in the discovered set are ignored; they may have been excluded
// or lost their link, and a stale pin should not invent an entry.
func orderAlbums(albums []gallery.Album, cfg *config.Config) []gallery.Album {
	position := make(map[int64]int, len(cfg.Albums.Order))
	for i, id := range cfg.Albums.Order {
		position[id] = i
	}

	ordered := make([]gallery.Album, len(albums))
	copy(ordered, albums)
	sort.SliceStable(ordered, func(i, j int) bool {
		pi, iok := position[ordered[i].ID]
		pj, jok := position[ordered[j].ID]
		switch {
		case iok && jok:
			if pi != pj {
				return pi < pj
			}
		case iok:
			return true
		case jok:
			return false
		}

		// Ente's manual album order: 0 means unset, and set orders come
		// before unset ones.
		oi, oj := ordered[i].SortOrder, ordered[j].SortOrder
		switch {
		case oi != 0 && oj != 0 && oi != oj:
			return oi < oj
		case oi != 0:
			return true
		case oj != 0:
			return false
		}
		// The last tiebreak is the title as displayed, so the page reads
		// alphabetically to a human, not alphabetically by whatever
		// prefix the title_regex strips off.
		return cfg.Albums.CleanTitle(ordered[i].Name) < cfg.Albums.CleanTitle(ordered[j].Name)
	})
	return ordered
}

// centroid is an album's map position: the mean of its files' coordinates.
//
// Means are wrong across the antimeridian and fine everywhere else, and an
// album whose photos span the Pacific dateline has bigger map problems than
// this one. An album with no coordinates at all reports not-ok.
func centroid(index *gallery.FileIndex) (lat, lon float64, ok bool) {
	var n int
	for _, f := range index.Files {
		if f.Lat != nil && f.Lon != nil {
			lat += *f.Lat
			lon += *f.Lon
			n++
		}
	}
	if n == 0 {
		return 0, 0, false
	}
	return lat / float64(n), lon / float64(n), true
}

// Render writes the site: index.html, style.css, and points.json when the
// map is on.
func Render(dir string, site SiteData) error {
	indexTemplate, err := template.ParseFS(files, "templates/index.html")
	if err != nil {
		return fmt.Errorf("parsing template: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating output directory: %w", err)
	}

	// Covers are written by gallery.Covers into out/thumbs already; only
	// the page itself, its stylesheet and its data come from here.
	if err := writeRendered(filepath.Join(dir, "index.html"), indexTemplate, site); err != nil {
		return err
	}

	stylesheet, err := files.ReadFile("assets/style.css")
	if err != nil {
		return fmt.Errorf("reading embedded stylesheet: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "style.css"), stylesheet, 0o644); err != nil {
		return fmt.Errorf("writing style.css: %w", err)
	}

	if site.Map != nil {
		points, err := json.MarshalIndent(map[string]any{"points": site.Points}, "", "  ")
		if err != nil {
			return fmt.Errorf("encoding points.json: %w", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "points.json"), append(points, '\n'), 0o644); err != nil {
			return fmt.Errorf("writing points.json: %w", err)
		}
	}
	return nil
}

func writeRendered(path string, indexTemplate *template.Template, site SiteData) error {
	output, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer output.Close()
	if err := indexTemplate.Execute(output, site); err != nil {
		return fmt.Errorf("rendering %s: %w", path, err)
	}
	return nil
}
