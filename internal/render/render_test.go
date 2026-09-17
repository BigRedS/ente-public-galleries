package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BigRedS/ente-public-galleries/internal/config"
	"github.com/BigRedS/ente-public-galleries/internal/gallery"
)

// siteFixture builds the Assemble inputs: two albums, one geotagged, with
// covers written for whichever IDs coverWritten names.
type siteFixture struct {
	cfg     *config.Config
	albums  []gallery.Album
	indexes map[int64]*gallery.FileIndex
}

func newSiteFixture(t *testing.T) *siteFixture {
	t.Helper()

	// Load a config with everything default; the tests then tweak fields.
	cfg, err := config.Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("loading default config: %v", err)
	}
	cfg.Output = t.TempDir()

	lat1, lon1 := 51.5074, -0.1278
	lat2, lon2 := 52.4862, -1.8904

	return &siteFixture{
		cfg: cfg,
		albums: []gallery.Album{
			{ID: 1, Name: "Birthday Trackday", Description: "A fine day", ShareURL: "https://albums.ente.com/?t=a#key1"},
			{ID: 2, Name: "Ducati Centenary", ShareURL: "https://albums.ente.com/?t=b#key2"},
		},
		indexes: map[int64]*gallery.FileIndex{
			1: {
				Files: map[int64]gallery.FileSummary{
					11: {ID: 11, CreationTime: 1000, Lat: &lat1, Lon: &lon1},
					12: {ID: 12, CreationTime: 1001, Lat: &lat2, Lon: &lon2},
				},
			},
			2: {
				Files: map[int64]gallery.FileSummary{
					21: {ID: 21, CreationTime: 2000},
				},
			},
		},
	}
}

// loadCfgWith replaces the fixture's config with one loaded from real
// content, so settings like the title regex arrive compiled, exactly as they
// would for a running build.
func (f *siteFixture) loadCfgWith(t *testing.T, extra string) {
	t.Helper()
	dir := t.TempDir()
	body := "output: " + f.cfg.Output + "\n" + extra
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	f.cfg = cfg
}

// writeCover places a stand-in thumbnail where Assemble looks for it.
func (f *siteFixture) writeCover(t *testing.T, albumID int64) {
	t.Helper()
	dir := filepath.Join(f.cfg.Output, "thumbs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating thumbs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(albumID, 10)+".jpg"), []byte{0xFF, 0xD8, 0xFF}, 0o644); err != nil {
		t.Fatalf("writing stand-in cover: %v", err)
	}
}

// writeRoute places a stand-in route image where Assemble looks for it.
func (f *siteFixture) writeRoute(t *testing.T, albumID int64) {
	t.Helper()
	dir := filepath.Join(f.cfg.Output, "routes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating routes dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, strconv.FormatInt(albumID, 10)+".png"), []byte{0x89, 'P', 'N', 'G'}, 0o644); err != nil {
		t.Fatalf("writing stand-in route image: %v", err)
	}
}

func TestAssembleBuildsCardsAndAlbumPoints(t *testing.T) {
	f := newSiteFixture(t)
	f.writeCover(t, 1)
	// This test is about card contents, not ordering; pin the sort so it
	// doesn't depend on the default (date, descending) sort's behaviour.
	f.cfg.Albums.SortBy = config.SortByName
	f.cfg.Albums.SortOrder = config.SortAsc

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	if len(site.Cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(site.Cards))
	}

	first := site.Cards[0]
	if first.Name != "Birthday Trackday" || first.Description != "A fine day" {
		t.Errorf("card 1 = %+v", first)
	}
	if !first.HasThumb || first.ThumbPath != "thumbs/1.jpg" {
		t.Errorf("card 1 thumb = %q/%v, want thumbs/1.jpg/true", first.ThumbPath, first.HasThumb)
	}
	if first.FileCount != 2 {
		t.Errorf("card 1 FileCount = %d, want 2", first.FileCount)
	}

	second := site.Cards[1]
	if second.HasThumb {
		t.Error("card 2 claims a cover that was never written")
	}

	if site.Map == nil {
		t.Fatal("map data missing despite config enabling it")
	}
	if len(site.Points) != 1 {
		t.Fatalf("got %d points, want 1 (only the geotagged album): %+v", len(site.Points), site.Points)
	}
	// The album dot is the mean of its two files.
	if got, want := site.Points[0].Lat, (51.5074+52.4862)/2; got < want-0.0001 || got > want+0.0001 {
		t.Errorf("point lat = %v, want about %v", got, want)
	}
	if site.Points[0].URL != "https://albums.ente.com/?t=a#key1" {
		t.Errorf("point URL = %q", site.Points[0].URL)
	}
}

func TestAssemblePhotosPointsAreOptIn(t *testing.T) {
	f := newSiteFixture(t)

	f.cfg.Map.Points = config.PointsPhotos
	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if got := len(site.Points); got != 2 {
		t.Fatalf("photos mode: got %d points, want 2 (one per geotagged file)", got)
	}

	f.cfg.Map.Points = config.PointsAlbums
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if got := len(site.Points); got != 1 {
		t.Fatalf("albums mode: got %d points, want 1 (one per geotagged album)", got)
	}
}

// A map with nothing to place is dropped entirely, because a blank map reads
// as breakage rather than as emptiness.
func TestAssembleDropsEmptyMap(t *testing.T) {
	f := newSiteFixture(t)
	for _, index := range f.indexes {
		for id, file := range index.Files {
			file.Lat, file.Lon = nil, nil
			index.Files[id] = file
		}
	}

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if site.Map != nil {
		t.Error("map kept despite having no points to show")
	}
}

func TestAssembleAppliesOverrides(t *testing.T) {
	f := newSiteFixture(t)
	f.cfg.Albums.Overrides = map[int64]config.Override{
		2: {Title: "Centenary, Ducati", Description: "Mugello"},
	}

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	for _, card := range site.Cards {
		if card.Name == "Centenary, Ducati" {
			if card.Description != "Mugello" {
				t.Errorf("override description = %q, want Mugello", card.Description)
			}
			return
		}
	}
	t.Error("overridden title never appeared in the cards")
}

// The title substitution reaches cards, map points and the name-sort
// tiebreak; explicit overrides are verbatim and are not themselves cleaned.
func TestAssembleAppliesTitleRegex(t *testing.T) {
	f := newSiteFixture(t)
	// Strip a leading "YYYY-MM ", like a dated album list.
	f.loadCfgWith(t, `albums:
  title_regex: 's/^\d\d\d\d-\d\d //'
  overrides:
    2:
      title: 2026-05 Kept Verbatim
`)
	f.albums[0].Name = "2025-03 Birthday Trackday"
	f.albums[1].Name = "2026-05 Ducati Centenary"

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	var names []string
	for _, card := range site.Cards {
		names = append(names, card.Name)
	}
	// Cleaned, override-verbatim, and no double-cleaning to check: the
	// set as a whole is what matters.
	for _, want := range []string{"Birthday Trackday", "2026-05 Kept Verbatim"} {
		found := false
		for _, name := range names {
			if name == want {
				found = true
			}
		}
		if !found {
			t.Errorf("cards %v missing %q", names, want)
		}
	}

	// The map point carries the cleaned name too, since the popup is what
	// a visitor reads.
	for _, point := range site.Points {
		if point.Name == "Birthday Trackday" {
			return
		}
	}
	t.Errorf("points %v do not carry the cleaned title", site.Points)
}

// Sorting by name sorts by what the page shows, not by the raw Ente name, so
// a stripped date prefix cannot dominate the ordering.
func TestAssembleSortsByCleanedName(t *testing.T) {
	f := newSiteFixture(t)
	f.loadCfgWith(t, `albums:
  sort_by: name
  sort_order: asc
  title_regex: 's/^\d\d\d\d-\d\d //'
`)
	f.albums = []gallery.Album{
		{ID: 1, Name: "2025-01 Zebra", ShareURL: "u1"},
		{ID: 2, Name: "2024-12 Alpha", ShareURL: "u2"},
	}

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if len(site.Cards) != 2 {
		t.Fatalf("got %d cards, want 2", len(site.Cards))
	}
	if site.Cards[0].Name != "Alpha" || site.Cards[1].Name != "Zebra" {
		t.Errorf("order = %q, %q; want Alpha before Zebra by cleaned name", site.Cards[0].Name, site.Cards[1].Name)
	}
}

// Pinned config order always wins; everything else falls through to the
// default sort (date, descending), and albums with no synced files yet
// always trail the dated ones, tie-broken by name.
func TestAssembleOrdersByConfigThenDateThenName(t *testing.T) {
	f := newSiteFixture(t)
	f.albums = append(f.albums,
		gallery.Album{ID: 3, Name: "Zebra", ShareURL: "u3"},
		gallery.Album{ID: 4, Name: "Aardvark", ShareURL: "u4"},
		gallery.Album{ID: 5, Name: "Middle", ShareURL: "u5"},
	)
	// Pin album 2 (Ducati Centenary) to the front.
	f.cfg.Albums.Order = []int64{2}

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	var names []string
	for _, card := range site.Cards {
		names = append(names, card.Name)
	}
	// Ducati Centenary is pinned; Birthday Trackday is the only other
	// dated album (indexes only cover albums 1 and 2), so it comes next;
	// the three undated albums trail, alphabetically.
	want := []string{"Ducati Centenary", "Birthday Trackday", "Aardvark", "Middle", "Zebra"}
	for i := range want {
		if i >= len(names) {
			t.Fatalf("fewer cards than expected: %v", names)
		}
		if names[i] != want[i] {
			t.Errorf("position %d = %q, want %q (order: %v)", i, names[i], want[i], names)
			break
		}
	}
}

// sort_by: name and sort_by: size both honour sort_order, with the
// displayed name as the final tiebreak.
func TestAssembleSortByNameAndSize(t *testing.T) {
	f := newSiteFixture(t)
	// Album 1 (Birthday Trackday) has 2 files, album 2 (Ducati Centenary) has 1.

	f.cfg.Albums.SortBy = config.SortByName
	f.cfg.Albums.SortOrder = config.SortAsc
	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if got := []string{site.Cards[0].Name, site.Cards[1].Name}; got[0] != "Birthday Trackday" || got[1] != "Ducati Centenary" {
		t.Errorf("name asc order = %v, want Birthday Trackday, Ducati Centenary", got)
	}

	f.cfg.Albums.SortOrder = config.SortDesc
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if got := []string{site.Cards[0].Name, site.Cards[1].Name}; got[0] != "Ducati Centenary" || got[1] != "Birthday Trackday" {
		t.Errorf("name desc order = %v, want Ducati Centenary, Birthday Trackday", got)
	}

	f.cfg.Albums.SortBy = config.SortBySize
	f.cfg.Albums.SortOrder = config.SortDesc
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if got := []string{site.Cards[0].Name, site.Cards[1].Name}; got[0] != "Birthday Trackday" || got[1] != "Ducati Centenary" {
		t.Errorf("size desc order = %v, want Birthday Trackday (2 photos), Ducati Centenary (1 photo)", got)
	}
}

// date_source picks which end of the album's span counts as its date; the
// difference only shows up when that changes which side of another album's
// span it falls on.
func TestAssembleDateSourceAffectsOrder(t *testing.T) {
	f := newSiteFixture(t)
	// Album 1 spans 1000-1001; give album 2 a wide, earlier-starting span
	// that still ends after album 1's, so first/last/midpoint disagree.
	f.indexes[2] = &gallery.FileIndex{Files: map[int64]gallery.FileSummary{
		21: {ID: 21, CreationTime: 500},
		22: {ID: 22, CreationTime: 1500},
	}}
	f.cfg.Albums.SortBy = config.SortByDate
	f.cfg.Albums.SortOrder = config.SortAsc

	f.cfg.Albums.DateSource = config.DateFirst
	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if site.Cards[0].Name != "Ducati Centenary" {
		t.Errorf("date_source first: order = %v, want Ducati Centenary earliest (starts at 500)", cardNames(site))
	}

	f.cfg.Albums.DateSource = config.DateLast
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if site.Cards[0].Name != "Birthday Trackday" {
		t.Errorf("date_source last: order = %v, want Birthday Trackday earliest (ends at 1001)", cardNames(site))
	}
}

func cardNames(site SiteData) []string {
	var names []string
	for _, c := range site.Cards {
		names = append(names, c.Name)
	}
	return names
}

// Year headers only appear when sorting by date, in front of the first
// album of each new year, and never on an undated album.
func TestAssembleGroupsByYear(t *testing.T) {
	f := newSiteFixture(t)
	f.indexes[1].Files = map[int64]gallery.FileSummary{
		11: {ID: 11, CreationTime: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
	}
	f.indexes[2].Files = map[int64]gallery.FileSummary{
		21: {ID: 21, CreationTime: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
	}
	f.albums = append(f.albums, gallery.Album{ID: 3, Name: "No Files Yet", ShareURL: "u3"})
	f.cfg.Albums.SortBy = config.SortByDate
	f.cfg.Albums.SortOrder = config.SortAsc

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	var headers []string
	for _, c := range site.Cards {
		if c.ShowYearHeader {
			headers = append(headers, c.Name)
		}
	}
	want := []string{"Birthday Trackday", "Ducati Centenary"}
	if len(headers) != len(want) || headers[0] != want[0] || headers[1] != want[1] {
		t.Errorf("year headers on %v, want %v (cards: %v)", headers, want, cardNames(site))
	}

	// Disabling grouping drops every header.
	disabled := false
	f.cfg.Albums.GroupByYear = &disabled
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	for _, c := range site.Cards {
		if c.ShowYearHeader {
			t.Errorf("card %q has a year header despite group_by_year: false", c.Name)
		}
	}

	// Sorting by name instead must not draw year headers even though
	// grouping is on, since a name order scatters years across the page.
	enabled := true
	f.cfg.Albums.GroupByYear = &enabled
	f.cfg.Albums.SortBy = config.SortByName
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	for _, c := range site.Cards {
		if c.ShowYearHeader {
			t.Errorf("card %q has a year header while sorted by name", c.Name)
		}
	}
}

// Grouping splits the flat card list into one Group per year, each holding
// only its own cards, with the trailing undated album folded into the last
// group rather than starting a heading-less one of its own.
func TestAssembleGroupsSplitCardsPerYear(t *testing.T) {
	f := newSiteFixture(t)
	f.indexes[1].Files = map[int64]gallery.FileSummary{
		11: {ID: 11, CreationTime: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
	}
	f.indexes[2].Files = map[int64]gallery.FileSummary{
		21: {ID: 21, CreationTime: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
	}
	f.albums = append(f.albums, gallery.Album{ID: 3, Name: "No Files Yet", ShareURL: "u3"})
	f.cfg.Albums.SortBy = config.SortByDate
	f.cfg.Albums.SortOrder = config.SortAsc

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	if len(site.Groups) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(site.Groups), site.Groups)
	}
	if site.Groups[0].Year != 2023 || len(site.Groups[0].Cards) != 1 || site.Groups[0].Cards[0].Name != "Birthday Trackday" {
		t.Errorf("group 0 = %+v, want year 2023 with just Birthday Trackday", site.Groups[0])
	}
	if site.Groups[1].Year != 2024 || len(site.Groups[1].Cards) != 2 {
		t.Errorf("group 1 = %+v, want year 2024 with 2 cards (Ducati Centenary + the undated trailer)", site.Groups[1])
	}
	if names := []string{site.Groups[1].Cards[0].Name, site.Groups[1].Cards[1].Name}; names[0] != "Ducati Centenary" || names[1] != "No Files Yet" {
		t.Errorf("group 1 cards = %v, want Ducati Centenary then No Files Yet", names)
	}

	// With grouping off, everything lands in a single heading-less group.
	disabled := false
	f.cfg.Albums.GroupByYear = &disabled
	site = Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if len(site.Groups) != 1 || site.Groups[0].Year != 0 || len(site.Groups[0].Cards) != 3 {
		t.Errorf("ungrouped Groups = %+v, want one heading-less group of 3", site.Groups)
	}
}

// No albums at all yields no groups, so the template falls back to its
// empty-state message instead of rendering a blank grid.
func TestAssembleGroupsEmptyWhenNoAlbums(t *testing.T) {
	f := newSiteFixture(t)
	f.albums = nil
	f.indexes = nil

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if len(site.Groups) != 0 {
		t.Errorf("Groups = %+v, want none", site.Groups)
	}
}

// The date range is the earliest and latest file in the album, and collapses
// to a single date when every photo was taken the same day.
func TestAssembleComputesDateRange(t *testing.T) {
	f := newSiteFixture(t)
	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	var first, second Card
	for _, card := range site.Cards {
		switch card.Name {
		case "Birthday Trackday":
			first = card
		case "Ducati Centenary":
			second = card
		}
	}

	// Album 1 has two files a microsecond apart: same calendar day, so To
	// is left empty rather than repeating From.
	wantDay := time.UnixMicro(1000).Format("2 Jan 2006")
	if first.From != wantDay || first.To != "" {
		t.Errorf("album 1 range = %q..%q, want %q..\"\"", first.From, first.To, wantDay)
	}

	// Album 2 has a single file, so it too is a single day with no To.
	if second.From == "" || second.To != "" {
		t.Errorf("album 2 range = %q..%q, want a single date and no To", second.From, second.To)
	}
}

// An album with no synced files yet (index nil, or absent from the map) gets
// no date range rather than a zero-value date.
func TestAssembleOmitsDateRangeWithoutFiles(t *testing.T) {
	f := newSiteFixture(t)
	f.albums = append(f.albums, gallery.Album{ID: 3, Name: "No Index Yet", ShareURL: "u3"})

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	for _, card := range site.Cards {
		if card.Name == "No Index Yet" && (card.From != "" || card.To != "") {
			t.Errorf("card with no index has dates %q..%q, want none", card.From, card.To)
		}
	}
}

// SearchName is the displayed name lowercased, computed after title cleanup
// and overrides so the client-side search matches what a visitor reads.
func TestAssembleSearchNameFollowsDisplayedName(t *testing.T) {
	f := newSiteFixture(t)
	f.cfg.Albums.Overrides = map[int64]config.Override{
		2: {Title: "Centenary, Ducati"},
	}

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	for _, card := range site.Cards {
		if card.SearchName != strings.ToLower(card.Name) {
			t.Errorf("card %q: SearchName = %q, want %q", card.Name, card.SearchName, strings.ToLower(card.Name))
		}
	}
}

func TestAssembleSetsHasRouteWhenRouteImageExists(t *testing.T) {
	f := newSiteFixture(t)
	f.writeRoute(t, 1)

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	for _, card := range site.Cards {
		if card.Name == "Birthday Trackday" {
			if !card.HasRoute || card.RoutePath != "routes/1.png" {
				t.Errorf("card 1 route = %q/%v, want routes/1.png/true", card.RoutePath, card.HasRoute)
			}
			return
		}
	}
	t.Fatal("Birthday Trackday card not found")
}

func TestAssembleLeavesHasRouteFalseWithoutOne(t *testing.T) {
	f := newSiteFixture(t)

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	for _, card := range site.Cards {
		if card.HasRoute {
			t.Errorf("card %q has HasRoute despite no route image being written", card.Name)
		}
	}
}

// Each year group renders as its own <div class="grid">, with the heading
// as a plain sibling between them - not a heading squeezed into one grid
// cell of a single shared grid.
func TestRenderSplitsGroupsIntoSeparateGrids(t *testing.T) {
	f := newSiteFixture(t)
	f.indexes[1].Files = map[int64]gallery.FileSummary{
		11: {ID: 11, CreationTime: time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
	}
	f.indexes[2].Files = map[int64]gallery.FileSummary{
		21: {ID: 21, CreationTime: time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC).UnixMicro()},
	}
	f.cfg.Albums.SortBy = config.SortByDate
	f.cfg.Albums.SortOrder = config.SortAsc

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if err := Render(f.cfg.Output, site); err != nil {
		t.Fatalf("Render: %v", err)
	}
	index, err := os.ReadFile(filepath.Join(f.cfg.Output, "index.html"))
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	page := string(index)

	if got := strings.Count(page, `<div class="grid">`); got != 2 {
		t.Errorf(`page has %d <div class="grid"> elements, want 2 (one per year)`, got)
	}
	firstHeader := strings.Index(page, `<h2 class="year-header">2023</h2>`)
	secondHeader := strings.Index(page, `<h2 class="year-header">2024</h2>`)
	firstGrid := strings.Index(page, `<div class="grid">`)
	if firstHeader == -1 || secondHeader == -1 {
		t.Fatalf("year headers missing from page")
	}
	if firstHeader > firstGrid {
		t.Error("first year heading should come before its grid, not inside it")
	}
	if secondHeader < firstHeader {
		t.Error("year headings out of order")
	}
}

func TestRenderWritesSite(t *testing.T) {
	f := newSiteFixture(t)
	f.writeCover(t, 1)
	f.writeCover(t, 2)

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if err := Render(f.cfg.Output, site); err != nil {
		t.Fatalf("Render: %v", err)
	}

	index, err := os.ReadFile(filepath.Join(f.cfg.Output, "index.html"))
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	page := string(index)

	for _, want := range []string{
		"Galleries", // default title
		"Birthday Trackday",
		`href="https://albums.ente.com/?t=a#key1"`,
		`src="thumbs/1.jpg"`,
		"2 photos",
		`id="map"`,
		`id="search"`,
		`data-name="birthday trackday"`,
		"class=\"dates\"",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("index.html missing %q", want)
		}
	}

	style, err := os.ReadFile(filepath.Join(f.cfg.Output, "style.css"))
	if err != nil || len(style) == 0 {
		t.Errorf("style.css missing or empty (err %v)", err)
	}

	var points struct {
		Points []Point `json:"points"`
	}
	body, err := os.ReadFile(filepath.Join(f.cfg.Output, "points.json"))
	if err != nil {
		t.Fatalf("reading points.json: %v", err)
	}
	if err := json.Unmarshal(body, &points); err != nil {
		t.Fatalf("parsing points.json: %v", err)
	}
	if len(points.Points) != 1 {
		t.Errorf("points.json has %d points, want 1", len(points.Points))
	}
}

// Album names are the site owner's own text, but they flow through a real
// HTML template all the same, so the escaping has to hold.
func TestRenderEscapesAlbumNames(t *testing.T) {
	f := newSiteFixture(t)
	f.albums[0].Name = `<script>alert(1)</script>`

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if err := Render(f.cfg.Output, site); err != nil {
		t.Fatalf("Render: %v", err)
	}

	index, err := os.ReadFile(filepath.Join(f.cfg.Output, "index.html"))
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	if strings.Contains(string(index), "<script>alert(1)") {
		t.Error("album name reached the page unescaped")
	}
	if !strings.Contains(string(index), "&lt;script&gt;") {
		t.Error("album name was not escaped into text")
	}
}

func TestRenderOmitsMapArtifactsWhenDisabled(t *testing.T) {
	f := newSiteFixture(t)
	enabled := false
	f.cfg.Map.Enabled = &enabled

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)
	if err := Render(f.cfg.Output, site); err != nil {
		t.Fatalf("Render: %v", err)
	}

	index, err := os.ReadFile(filepath.Join(f.cfg.Output, "index.html"))
	if err != nil {
		t.Fatalf("reading index.html: %v", err)
	}
	if strings.Contains(string(index), `id="map"`) {
		t.Error("map div present despite the map being disabled")
	}
	if _, err := os.Stat(filepath.Join(f.cfg.Output, "points.json")); !os.IsNotExist(err) {
		t.Errorf("points.json exists despite the map being disabled (err %v)", err)
	}
}
