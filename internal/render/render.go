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
	"strings"
	"time"

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
	// From and To are the earliest and latest photo dates in the album,
	// pre-formatted; both empty when the album's index has no files yet.
	// To is left empty (and From alone is shown) when the two coincide.
	From string
	To   string
	// SearchName is the lowercased name the client-side search filters
	// against, computed once here rather than in JS on every keystroke.
	SearchName string
	// Year is the album's sort-date year (per config.Albums.DateSource),
	// 0 when the album has no files yet.
	Year int
	// ShowYearHeader marks the first card of a new year group; only ever
	// set when sorting by date with grouping on.
	ShowYearHeader bool
	// RoutePath is relative to the site root; empty when there is no route
	// image (fewer than two geotagged photos, route rendering disabled, or
	// the render failed and was skipped for this album).
	RoutePath string
	HasRoute  bool
}

// CardGroup is a run of cards under one year heading, rendered as its own
// grid. Year is 0 when the group has no heading (grouping off, or a
// heading-less run of undated cards).
type CardGroup struct {
	Year  int
	Cards []Card
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
	// Groups is Cards split into per-year runs for the template to render
	// as separate grids; see groupCards.
	Groups []CardGroup
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

	ordered := orderAlbums(albums, cfg, indexes)
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
		if _, err := os.Stat(filepath.Join(output, "routes", fmt.Sprintf("%d.png", album.ID))); err == nil {
			card.HasRoute = true
			card.RoutePath = fmt.Sprintf("routes/%d.png", album.ID)
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

		card.SearchName = strings.ToLower(card.Name)

		index := indexes[album.ID]
		if index != nil {
			card.FileCount = len(index.Files)
			card.From, card.To = dateRange(index)
			if d, ok := albumDate(index, cfg.Albums.DateSource); ok {
				card.Year = d.Year()
			}

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

	// Year headings only make sense alongside a date sort: any other
	// order scatters years throughout the page, and a heading there would
	// mislead rather than help.
	if cfg.Albums.SortBy == config.SortByDate && cfg.Albums.GroupByYearEnabled() {
		markYearHeaders(site.Cards)
	}
	site.Groups = groupCards(site.Cards)

	// A map with nothing to show would be a blank grey rectangle, which
	// reads as breakage rather than emptiness.
	if site.Map != nil && len(site.Points) == 0 {
		site.Map = nil
	}
	return site
}

// markYearHeaders flags the first card of each run of a given year, skipping
// cards with no date (Year 0) so an undated album never starts a bogus group
// nor breaks up the surrounding one.
func markYearHeaders(cards []Card) {
	lastYear := 0
	for i := range cards {
		if cards[i].Year != 0 && cards[i].Year != lastYear {
			cards[i].ShowYearHeader = true
			lastYear = cards[i].Year
		}
	}
}

// groupCards splits cards at each ShowYearHeader boundary into separate
// groups, each rendered as its own grid: a heading spanning a shared grid
// cell renders as an oddly-sized tile, not a full-width divider, so the
// template gets one grid per group instead. A group's Year of 0 means no
// heading is drawn for it (the ungrouped case, and any undated albums
// trailing the last real group).
func groupCards(cards []Card) []CardGroup {
	if len(cards) == 0 {
		return nil
	}
	groups := []CardGroup{{}}
	for _, c := range cards {
		if c.ShowYearHeader {
			groups = append(groups, CardGroup{Year: c.Year})
		}
		last := &groups[len(groups)-1]
		last.Cards = append(last.Cards, c)
	}
	// The placeholder first group is only real when nothing ended up in
	// it: an undated leading card (a pinned album, say) legitimately
	// belongs in a heading-less group, but a group with no cards at all
	// is just the seed value and must not render as an empty grid.
	if groups[0].Year == 0 && len(groups[0].Cards) == 0 {
		groups = groups[1:]
	}
	return groups
}

// orderAlbums applies the two-level sort: albums pinned in config order come
// first, then the configured sort_by/sort_order for the rest. Pinned albums
// not in the discovered set are ignored; they may have been excluded or lost
// their link, and a stale pin should not invent an entry.
func orderAlbums(albums []gallery.Album, cfg *config.Config, indexes map[int64]*gallery.FileIndex) []gallery.Album {
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
		return albumLess(cfg, indexes, ordered[i], ordered[j])
	})
	return ordered
}

// albumLess orders two unpinned albums per cfg.Albums.SortBy/SortOrder. The
// displayed (cleaned) title is always the final tiebreak, so the page never
// looks randomly ordered when its primary key ties.
func albumLess(cfg *config.Config, indexes map[int64]*gallery.FileIndex, a, b gallery.Album) bool {
	desc := cfg.Albums.SortOrder == config.SortDesc
	nameA, nameB := cfg.Albums.CleanTitle(a.Name), cfg.Albums.CleanTitle(b.Name)

	switch cfg.Albums.SortBy {
	case config.SortByName:
		if desc {
			return nameA > nameB
		}
		return nameA < nameB

	case config.SortBySize:
		ca, cb := fileCount(indexes[a.ID]), fileCount(indexes[b.ID])
		if ca != cb {
			if desc {
				return ca > cb
			}
			return ca < cb
		}

	default: // config.SortByDate
		ta, aok := albumDate(indexes[a.ID], cfg.Albums.DateSource)
		tb, bok := albumDate(indexes[b.ID], cfg.Albums.DateSource)
		switch {
		case aok && bok:
			if !ta.Equal(tb) {
				if desc {
					return ta.After(tb)
				}
				return ta.Before(tb)
			}
		case aok:
			// An album with no synced files yet has nothing to rank
			// chronologically, so it always trails the dated ones
			// rather than flip-flopping to the front under asc.
			return true
		case bok:
			return false
		}
	}
	return nameA < nameB
}

// fileCount is len(index.Files), nil-safe for an album with no index yet.
func fileCount(index *gallery.FileIndex) int {
	if index == nil {
		return 0
	}
	return len(index.Files)
}

// albumDate is "the album's date" per source, derived from its earliest and
// latest file. ok is false when the album has no files yet.
func albumDate(index *gallery.FileIndex, source string) (time.Time, bool) {
	earliest, latest, ok := fileTimeRange(index)
	if !ok {
		return time.Time{}, false
	}
	switch source {
	case config.DateFirst:
		return time.UnixMicro(earliest), true
	case config.DateMidpoint:
		return time.UnixMicro((earliest + latest) / 2), true
	default: // config.DateLast
		return time.UnixMicro(latest), true
	}
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

// dateRange is the earliest and latest CreationTime in an album's index,
// pre-formatted. to comes back empty when it would equal from, so a
// single-day album shows one date rather than a pointless "X - X".
func dateRange(index *gallery.FileIndex) (from, to string) {
	earliest, latest, ok := fileTimeRange(index)
	if !ok {
		return "", ""
	}
	from = time.UnixMicro(earliest).Format("2 Jan 2006")
	to = time.UnixMicro(latest).Format("2 Jan 2006")
	if to == from {
		to = ""
	}
	return from, to
}

// fileTimeRange is the earliest and latest CreationTime among an album's
// files. ok is false for a nil index or one with no files yet.
func fileTimeRange(index *gallery.FileIndex) (earliest, latest int64, ok bool) {
	if index == nil {
		return 0, 0, false
	}
	var n int
	for _, f := range index.Files {
		if n == 0 || f.CreationTime < earliest {
			earliest = f.CreationTime
		}
		if n == 0 || f.CreationTime > latest {
			latest = f.CreationTime
		}
		n++
	}
	return earliest, latest, n > 0
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
