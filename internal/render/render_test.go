package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

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

func TestAssembleBuildsCardsAndAlbumPoints(t *testing.T) {
	f := newSiteFixture(t)
	f.writeCover(t, 1)

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

// The alphabetical tiebreak sorts by what the page shows, not by the raw
// Ente name, so a stripped date prefix cannot dominate the ordering.
func TestAssembleSortsByCleanedName(t *testing.T) {
	f := newSiteFixture(t)
	f.loadCfgWith(t, `albums:
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

func TestAssembleOrdersByConfigThenMagicOrderThenName(t *testing.T) {
	f := newSiteFixture(t)
	f.albums = append(f.albums,
		gallery.Album{ID: 3, Name: "Zebra", ShareURL: "u3", SortOrder: 2},
		gallery.Album{ID: 4, Name: "Aardvark", ShareURL: "u4", SortOrder: 1},
		gallery.Album{ID: 5, Name: "Middle", ShareURL: "u5", SortOrder: 2},
	)
	// Pin album 2 (Ducati Centenary) to the front.
	f.cfg.Albums.Order = []int64{2}

	site := Assemble(f.cfg, f.cfg.Output, f.albums, f.indexes)

	var names []string
	for _, card := range site.Cards {
		names = append(names, card.Name)
	}
	want := []string{"Ducati Centenary", "Aardvark", "Middle", "Zebra", "Birthday Trackday"}
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
