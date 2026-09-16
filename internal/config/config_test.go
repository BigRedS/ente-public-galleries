package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing config: %v", err)
	}
	return path
}

// Running with no config at all is a supported mode, since Ente supplies the
// titles, descriptions and covers by itself.
func TestMissingFileYieldsUsableDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load of a missing file should succeed, got %v", err)
	}
	if cfg.Output != defaultOutput {
		t.Errorf("Output = %q, want %q", cfg.Output, defaultOutput)
	}
	if cfg.Site.Title != defaultTitle {
		t.Errorf("Site.Title = %q, want %q", cfg.Site.Title, defaultTitle)
	}
	if cfg.Map.Points != PointsAlbums {
		t.Errorf("Map.Points = %q, want %q", cfg.Map.Points, PointsAlbums)
	}
	if !cfg.MapEnabled() {
		t.Error("MapEnabled() = false, want true by default")
	}
}

func TestEmptyFileYieldsDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, ""))
	if err != nil {
		t.Fatalf("Load of an empty file should succeed, got %v", err)
	}
	if cfg.Output != defaultOutput {
		t.Errorf("Output = %q, want %q", cfg.Output, defaultOutput)
	}
}

func TestValuesOverrideDefaults(t *testing.T) {
	cfg, err := Load(writeConfig(t, `
account:
  email: me@example.com
  api: https://ente.example.com
  album_url_base: https://pics.example.com
output: ./public
site:
  title: My Galleries
map:
  points: photos
  tiles: https://tiles.example.com/{z}/{x}/{y}.png
albums:
  exclude: [111]
  order: [333, 222]
  overrides:
    222:
      title: Renamed
      description: Words
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Account.Email != "me@example.com" {
		t.Errorf("Account.Email = %q", cfg.Account.Email)
	}
	if cfg.Account.API != "https://ente.example.com" {
		t.Errorf("Account.API = %q", cfg.Account.API)
	}
	if cfg.Output != "./public" {
		t.Errorf("Output = %q", cfg.Output)
	}
	if cfg.Map.Points != PointsPhotos {
		t.Errorf("Map.Points = %q", cfg.Map.Points)
	}
	// Unset within a set section must still pick up its default.
	if cfg.Map.Attribution != defaultAttribution {
		t.Errorf("Map.Attribution = %q, want the default", cfg.Map.Attribution)
	}
	if !cfg.Albums.IsExcluded(111) {
		t.Error("IsExcluded(111) = false, want true")
	}
	if cfg.Albums.IsExcluded(222) {
		t.Error("IsExcluded(222) = true, want false")
	}
	if got := cfg.Albums.Overrides[222].Title; got != "Renamed" {
		t.Errorf("override title = %q, want %q", got, "Renamed")
	}
}

// A mistyped key that silently does nothing is the worst outcome, so it must
// be an error.
func TestUnknownKeyIsAnError(t *testing.T) {
	_, err := Load(writeConfig(t, "site:\n  titel: Oops\n"))
	if err == nil {
		t.Fatal("Load accepted an unknown key, expected an error")
	}
	if !strings.Contains(err.Error(), "titel") {
		t.Errorf("error should name the offending key, got %v", err)
	}
}

func TestInvalidPointsIsAnError(t *testing.T) {
	_, err := Load(writeConfig(t, "map:\n  points: everything\n"))
	if err == nil {
		t.Fatal("Load accepted map.points: everything, expected an error")
	}
	if !strings.Contains(err.Error(), "map.points") {
		t.Errorf("error should name the offending setting, got %v", err)
	}
}

func TestMapCanBeDisabledEitherWay(t *testing.T) {
	for _, body := range []string{
		"map:\n  enabled: false\n",
		"map:\n  points: none\n",
	} {
		cfg, err := Load(writeConfig(t, body))
		if err != nil {
			t.Fatalf("Load(%q): %v", body, err)
		}
		if cfg.MapEnabled() {
			t.Errorf("MapEnabled() = true for config %q, want false", body)
		}
	}
}
