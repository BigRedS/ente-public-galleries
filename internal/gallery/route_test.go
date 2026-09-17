package gallery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fakeRouteRenderer records each call and returns canned bytes or an error,
// so Routes.Sync's caching/skip logic can be tested without any real image
// rendering or network access.
type fakeRouteRenderer struct {
	calls [][]RoutePoint
	png   []byte
	err   error
}

func (f *fakeRouteRenderer) Render(_ context.Context, points []RoutePoint) ([]byte, error) {
	f.calls = append(f.calls, points)
	if f.err != nil {
		return nil, f.err
	}
	return f.png, nil
}

func ptr(f float64) *float64 { return &f }

func TestRoutePointsOrdersChronologically(t *testing.T) {
	index := &FileIndex{Files: map[int64]FileSummary{
		3: {ID: 3, CreationTime: 3000, Lat: ptr(3), Lon: ptr(3)},
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
		2: {ID: 2, CreationTime: 2000, Lat: ptr(2), Lon: ptr(2)},
	}}
	points := routePoints(index)
	if len(points) != 3 {
		t.Fatalf("got %d points, want 3", len(points))
	}
	want := []RoutePoint{{Lat: 1, Lon: 1}, {Lat: 2, Lon: 2}, {Lat: 3, Lon: 3}}
	for i := range want {
		if points[i] != want[i] {
			t.Errorf("points = %v, want %v", points, want)
			break
		}
	}
}

func TestRoutePointsSkipsUngeotaggedFiles(t *testing.T) {
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
		2: {ID: 2, CreationTime: 2000}, // no Lat/Lon
	}}
	if points := routePoints(index); len(points) != 1 {
		t.Errorf("got %d points, want 1 (the ungeotagged file should be skipped)", len(points))
	}
}

func TestRoutesSyncSkipsBelowMinimumPoints(t *testing.T) {
	dir := t.TempDir()
	renderer := &fakeRouteRenderer{png: []byte("png")}
	routes := &Routes{Renderer: renderer, Dir: dir}
	album := Album{ID: 1, Name: "One Photo"}
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
	}}

	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(renderer.calls) != 0 {
		t.Errorf("renderer called %d times, want 0 (fewer than 2 geotagged points)", len(renderer.calls))
	}
	if _, err := os.Stat(filepath.Join(dir, "1.png")); !os.IsNotExist(err) {
		t.Errorf("route file written despite too few points (err %v)", err)
	}
}

func TestRoutesSyncRemovesStaleImageWhenBelowMinimum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "1.png")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("writing stale route file: %v", err)
	}

	renderer := &fakeRouteRenderer{png: []byte("png")}
	routes := &Routes{Renderer: renderer, Dir: dir}
	album := Album{ID: 1, Name: "Locations Stripped"}
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000},
	}}

	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("stale route file was not removed")
	}
}

func TestRoutesSyncWritesRendererOutput(t *testing.T) {
	dir := t.TempDir()
	renderer := &fakeRouteRenderer{png: []byte("a-real-png-would-go-here")}
	routes := &Routes{Renderer: renderer, Dir: dir}
	album := Album{ID: 7, Name: "Trip", UpdationTime: 1000}
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
		2: {ID: 2, CreationTime: 2000, Lat: ptr(2), Lon: ptr(2)},
	}}

	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "7.png"))
	if err != nil {
		t.Fatalf("reading written route file: %v", err)
	}
	if string(got) != string(renderer.png) {
		t.Errorf("written file = %q, want %q", got, renderer.png)
	}
	if len(renderer.calls) != 1 || len(renderer.calls[0]) != 2 {
		t.Errorf("renderer calls = %+v, want one call with 2 points", renderer.calls)
	}
}

func TestRoutesSyncIsFreshWhenUpToDate(t *testing.T) {
	dir := t.TempDir()
	renderer := &fakeRouteRenderer{png: []byte("png")}
	routes := &Routes{Renderer: renderer, Dir: dir}
	album := Album{ID: 1, Name: "Trip", UpdationTime: 1000}
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
		2: {ID: 2, CreationTime: 2000, Lat: ptr(2), Lon: ptr(2)},
	}}

	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(renderer.calls) != 1 {
		t.Errorf("renderer called %d times across two Sync calls, want 1 (second should be fresh)", len(renderer.calls))
	}
}

// A stale route image - one written before the album's last change - is
// re-rendered, mirroring TestCoversSyncRefetchesStaleCover: an UpdationTime
// set in the future relative to wall-clock "now" means the file's mtime is
// stale the moment it's written, on every Sync call.
func TestRoutesSyncRefetchesStaleRoute(t *testing.T) {
	dir := t.TempDir()
	renderer := &fakeRouteRenderer{png: []byte("png")}
	routes := &Routes{Renderer: renderer, Dir: dir}
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
		2: {ID: 2, CreationTime: 2000, Lat: ptr(2), Lon: ptr(2)},
	}}
	album := Album{ID: 1, Name: "Trip", UpdationTime: time.Now().Add(time.Hour).UnixMicro()}

	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("first Sync: %v", err)
	}
	if err := routes.Sync(context.Background(), album, index); err != nil {
		t.Fatalf("second Sync: %v", err)
	}
	if len(renderer.calls) != 2 {
		t.Errorf("renderer called %d times, want 2 (the stale route image must be re-rendered)", len(renderer.calls))
	}
}

func TestRoutesSyncPropagatesRendererErrors(t *testing.T) {
	dir := t.TempDir()
	renderer := &fakeRouteRenderer{err: errors.New("tile server unreachable")}
	routes := &Routes{Renderer: renderer, Dir: dir}
	album := Album{ID: 1, Name: "Trip"}
	index := &FileIndex{Files: map[int64]FileSummary{
		1: {ID: 1, CreationTime: 1000, Lat: ptr(1), Lon: ptr(1)},
		2: {ID: 2, CreationTime: 2000, Lat: ptr(2), Lon: ptr(2)},
	}}

	err := routes.Sync(context.Background(), album, index)
	if err == nil {
		t.Fatal("Sync succeeded despite a renderer error")
	}
	if _, statErr := os.Stat(filepath.Join(dir, "1.png")); !os.IsNotExist(statErr) {
		t.Error("route file written despite a renderer error")
	}
}
