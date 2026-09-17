package gallery

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// RoutePoint is one geotagged photo's position, stripped of everything else:
// the renderer needs coordinates in chronological order, nothing more.
type RoutePoint struct {
	Lat, Lon float64
}

// RouteRenderer is the slice of map-drawing this package needs. The real
// implementation is internal/staticmap.Renderer, wired in main.go; tests
// supply a fake, mirroring CoverFetcher.
type RouteRenderer interface {
	Render(ctx context.Context, points []RoutePoint) ([]byte, error)
}

// Routes materialises each album's route-map thumbnail on disk, mirroring
// Covers: same freshness rule (output mtime vs album.UpdationTime, the same
// known limitation as Covers' cache - see cover.go's Sync doc comment),
// same "write straight into the public site output" placement.
type Routes struct {
	Renderer RouteRenderer
	// Dir is where route images are written, named <collectionID>.png.
	Dir string
	// MinPoints is the fewest geotagged photos needed to draw a route.
	// Zero means the default of 2 (a route needs at least two points to
	// connect).
	MinPoints int
}

// Sync writes album's route image into Dir, unless it is already there and
// fresh, or the album has fewer than MinPoints geotagged photos - in which
// case any stale image left over from a previous run (e.g. the album used
// to qualify, but its photos have since had their locations stripped) is
// removed, so a card never shows a route the album no longer has.
func (r *Routes) Sync(ctx context.Context, album Album, index *FileIndex) error {
	min := r.MinPoints
	if min == 0 {
		min = 2
	}

	path := filepath.Join(r.Dir, fmt.Sprintf("%d.png", album.ID))
	points := routePoints(index)
	if len(points) < min {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("album %q: removing stale route image: %w", album.Name, err)
		}
		return nil
	}

	if info, err := os.Stat(path); err == nil && info.ModTime().UnixMicro() >= album.UpdationTime {
		return nil
	}

	png, err := r.Renderer.Render(ctx, points)
	if err != nil {
		return fmt.Errorf("album %q route render: %w", album.Name, err)
	}

	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return fmt.Errorf("creating routes directory: %w", err)
	}
	// Route images are public site assets; world-readable is the point.
	if err := os.WriteFile(path, png, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// routePoints returns index's geotagged files as RoutePoints in
// chronological (CreationTime) order, ties broken by file ID for
// determinism, matching pickCover's tie-break convention. A nil index
// yields no points.
func routePoints(index *FileIndex) []RoutePoint {
	if index == nil {
		return nil
	}

	var files []FileSummary
	for _, f := range index.Files {
		if f.Lat != nil && f.Lon != nil {
			files = append(files, f)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].CreationTime != files[j].CreationTime {
			return files[i].CreationTime < files[j].CreationTime
		}
		return files[i].ID < files[j].ID
	})

	points := make([]RoutePoint, len(files))
	for i, f := range files {
		points[i] = RoutePoint{Lat: *f.Lat, Lon: *f.Lon}
	}
	return points
}
