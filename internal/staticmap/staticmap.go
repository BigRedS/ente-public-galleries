// Package staticmap renders a per-album route-map thumbnail: a line
// connecting an album's geotagged photos in time order, drawn over a real
// OSM basemap.
//
// It is the only package that imports github.com/flopp/go-staticmaps (and
// the graphics/geometry libraries that come with it), so that dependency
// graph does not spread into internal/gallery, which most of the binary
// depends on. gallery.RouteRenderer is the seam: Renderer implements it,
// but gallery itself never imports this package.
package staticmap

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"image/color"
	"image/png"
	"regexp"
	"strings"

	sm "github.com/flopp/go-staticmaps"
	"github.com/golang/geo/s2"

	"github.com/BigRedS/ente-public-galleries/internal/gallery"
)

// Route line and marker styling. Colours are plain RGBA rather than reusing
// the page's CSS custom properties, since this is a rasterised image with
// no access to a stylesheet; --accent's hex from style.css is used for the
// line so it reads as "the same site" alongside the cover thumbnail.
const (
	routeLineWeight = 3.0
	startMarkerSize = 5.0
	endMarkerSize   = 5.0
	tileSize        = 256
	// maxTileZoom is the highest zoom level standard OSM-style raster tile
	// servers actually render; asking for anything beyond it gets a 400,
	// not a real tile.
	maxTileZoom = 19
)

var (
	routeLineColor   = color.RGBA{R: 0xd8, G: 0xa2, B: 0x4a, A: 0xff} // --accent
	startMarkerColor = color.RGBA{R: 0x4a, G: 0xc8, B: 0x6e, A: 0xff} // green: where the trip started
	endMarkerColor   = color.RGBA{R: 0xd8, G: 0x4a, B: 0x4a, A: 0xff} // red: where it ended
)

var htmlTagPattern = regexp.MustCompile(`<[^>]*>`)

// Renderer draws a chronologically-ordered route over an OSM basemap and
// implements gallery.RouteRenderer.
type Renderer struct {
	provider      *sm.TileProvider
	width, height int
	userAgent     string
	cacheDir      string
}

// New builds a Renderer from the site's own Leaflet-style tile config:
// leafletTiles is cfg.Map.Tiles (a "{z}/{x}/{y}" URL template, optionally
// with "{s}"), attribution is cfg.Map.Attribution. attribution is arbitrary
// HTML, as Leaflet expects it for its own DOM attribution control; it is
// stripped to plain text here, since go-staticmaps burns it directly into
// the rendered pixels rather than a page.
//
// cacheDir is where fetched OSM tiles are cached on disk between renders;
// empty disables tile caching (every render re-fetches every tile).
func New(leafletTiles, attribution string, width, height int, userAgent, cacheDir string) (*Renderer, error) {
	urlPattern, shards, err := ConvertTileURLTemplate(leafletTiles)
	if err != nil {
		return nil, fmt.Errorf("converting map.tiles for route rendering: %w", err)
	}
	provider := &sm.TileProvider{
		Name:        "route-map",
		Attribution: plainTextAttribution(attribution),
		TileSize:    tileSize,
		URLPattern:  urlPattern,
		Shards:      shards,
	}
	return &Renderer{
		provider:  provider,
		width:     width,
		height:    height,
		userAgent: userAgent,
		cacheDir:  cacheDir,
	}, nil
}

// Render draws points as a path through them in the order given (the
// caller, gallery.routePoints, sorts chronologically), with distinct
// markers at the start and end so a route's direction is legible even
// without hover/zoom, fitted automatically to the points' bounding box, and
// returns an encoded PNG.
func (r *Renderer) Render(_ context.Context, points []gallery.RoutePoint) ([]byte, error) {
	if len(points) < 2 {
		return nil, fmt.Errorf("need at least 2 points to draw a route, got %d", len(points))
	}

	latlngs := make([]s2.LatLng, len(points))
	for i, p := range points {
		latlngs[i] = s2.LatLngFromDegrees(p.Lat, p.Lon)
	}

	mapCtx := sm.NewContext()
	mapCtx.SetSize(r.width, r.height)
	mapCtx.SetTileProvider(r.provider)
	mapCtx.SetUserAgent(r.userAgent)
	// go-staticmaps defaults maxZoom to 30 and auto-fits the tightest zoom
	// that fills the image around the given points; for an album whose
	// photos were all taken within a few metres of each other, that can
	// compute a zoom level standard OSM-style tile servers don't render
	// (they max out around 19) and get a 400 back for every tile at that
	// zoom. Capping it here is what actually avoids that, not backing off
	// on request volume.
	mapCtx.SetMaxZoom(maxTileZoom)
	if r.cacheDir != "" {
		mapCtx.SetCache(sm.NewTileCache(r.cacheDir, 0o755))
	} else {
		// nil explicitly disables caching; NewContext otherwise defaults
		// to caching in the OS-wide user cache directory, which would be
		// silent, shared, unbounded state this tool never asked for.
		mapCtx.SetCache(nil)
	}
	mapCtx.AddPath(sm.NewPath(latlngs, routeLineColor, routeLineWeight))
	mapCtx.AddMarker(sm.NewMarker(latlngs[0], startMarkerColor, startMarkerSize))
	mapCtx.AddMarker(sm.NewMarker(latlngs[len(latlngs)-1], endMarkerColor, endMarkerSize))

	img, err := mapCtx.Render()
	if err != nil {
		return nil, fmt.Errorf("rendering route map: %w", err)
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding route map: %w", err)
	}
	return buf.Bytes(), nil
}

// ConvertTileURLTemplate translates a Leaflet-style tile URL template
// ("{z}/{x}/{y}", optionally with a "{s}" subdomain-sharding placeholder)
// into a go-staticmaps TileProvider's URLPattern/Shards shape. Returns an
// error if the template has none of {z}, {x} or {y} to convert.
func ConvertTileURLTemplate(leafletTemplate string) (urlPattern string, shards []string, err error) {
	if !strings.Contains(leafletTemplate, "{z}") ||
		!strings.Contains(leafletTemplate, "{x}") ||
		!strings.Contains(leafletTemplate, "{y}") {
		return "", nil, fmt.Errorf("tile URL template %q has no {z}/{x}/{y} placeholders", leafletTemplate)
	}

	pattern := leafletTemplate
	pattern = strings.ReplaceAll(pattern, "{z}", "%[2]d")
	pattern = strings.ReplaceAll(pattern, "{x}", "%[3]d")
	pattern = strings.ReplaceAll(pattern, "{y}", "%[4]d")
	if strings.Contains(pattern, "{s}") {
		pattern = strings.ReplaceAll(pattern, "{s}", "%[1]s")
		// go-staticmaps has no built-in opinion on shard letters; this
		// matches the a/b/c convention most Leaflet-era tile servers use.
		shards = []string{"a", "b", "c"}
	}
	return pattern, shards, nil
}

// plainTextAttribution strips HTML tags from a Leaflet-style attribution
// string (built for a DOM) and decodes entities, since go-staticmaps draws
// this text directly onto the rendered pixels rather than into a page.
func plainTextAttribution(htmlAttribution string) string {
	return strings.TrimSpace(html.UnescapeString(htmlTagPattern.ReplaceAllString(htmlAttribution, "")))
}
