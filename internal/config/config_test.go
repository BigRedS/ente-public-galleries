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

// device_key_file is the one path most likely to be written as ~/..., since
// it is per-machine and configs get copied between machines, so ~ must expand
// rather than be passed through to a syscall that cannot open it.
func TestDeviceKeyFileTildeIsExpanded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load(writeConfig(t, "device_key_file: ~/.config/epg/device.key\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(home, ".config", "epg", "device.key"); cfg.DeviceKeyFile != want {
		t.Errorf("DeviceKeyFile = %q, want %q", cfg.DeviceKeyFile, want)
	}
}

// A quoted "~" is the home directory (bare ~ is YAML's null and means the
// keyring), and a plain path is untouched.
func TestDeviceKeyFileTildeEdgeCases(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg, err := Load(writeConfig(t, `device_key_file: "~"`+"\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceKeyFile != home {
		t.Errorf(`"~" = %q, want %q`, cfg.DeviceKeyFile, home)
	}

	cfg, err = Load(writeConfig(t, "device_key_file: /etc/epg/absolute.key\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DeviceKeyFile != "/etc/epg/absolute.key" {
		t.Errorf("absolute path was rewritten to %q", cfg.DeviceKeyFile)
	}
}

func TestParseSubstitution(t *testing.T) {
	tests := []struct {
		input       string
		pattern     string
		replacement string
		global      bool
		wantErr     string
	}{
		{input: `s/^\d\d\d\d-\d\d//`, pattern: `^\d\d\d\d-\d\d`, replacement: ""},
		{input: "s/foo/bar/", pattern: "foo", replacement: "bar"},
		{input: "s/foo/bar/g", pattern: "foo", replacement: "bar", global: true},
		{input: `s/a\/b/c/`, pattern: `a/b`, replacement: "c"},
		{input: "s/a", wantErr: "1 field(s)"},
		{input: "x/a/b/", wantErr: `must look like`},
		{input: "s/a/b/x", wantErr: "unknown flag"},
	}
	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			pattern, replacement, global, err := parseSubstitution(test.input)
			if test.wantErr != "" {
				if err == nil {
					t.Fatalf("parseSubstitution(%q) succeeded, want an error", test.input)
				}
				if !strings.Contains(err.Error(), test.wantErr) {
					t.Errorf("error = %q, want it to mention %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseSubstitution(%q): %v", test.input, err)
			}
			if pattern != test.pattern || replacement != test.replacement || global != test.global {
				t.Errorf("parsed %q/%q/%v, want %q/%q/%v", pattern, replacement, global, test.pattern, test.replacement, test.global)
			}
		})
	}
}

func TestTitleRegexCleansTitles(t *testing.T) {
	// Titles here use a space after the date, matching the shape real
	// Ente albums have; the escaped-delimiter form gets its own case.
	cfg, err := Load(writeConfig(t, `albums:
  title_regex: 's/^\d\d\d\d-\d\d //'
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	tests := []struct{ in, want string }{
		{"2025-03 Birthday Trackday", "Birthday Trackday"},
		{"2026-05 Ducati centenary Trackday", "Ducati centenary Trackday"},
		// No match passes through untouched.
		{"Just A Name", "Just A Name"},
		// The pattern is anchored, so a date mid-title stays.
		{"Trip 2024-12 somewhere", "Trip 2024-12 somewhere"},
	}
	for _, test := range tests {
		if got := cfg.Albums.CleanTitle(test.in); got != test.want {
			t.Errorf("CleanTitle(%q) = %q, want %q", test.in, got, test.want)
		}
	}

	// An escaped delimiter in the pattern is a literal slash, so titles
	// that genuinely use "YYYY-MM/Name" separators strip cleanly.
	slashCfg, err := Load(writeConfig(t, `albums:
  title_regex: 's/^\d\d\d\d-\d\d\///'
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := slashCfg.Albums.CleanTitle("2025-03/Birthday"), "Birthday"; got != want {
		t.Errorf("escaped-delimiter CleanTitle = %q, want %q", got, want)
	}
}

// A substitution that swallows the entire title must not blank the page; the
// original is the better failure than an empty heading.
func TestTitleRegexEmptyResultFallsBack(t *testing.T) {
	cfg, err := Load(writeConfig(t, `albums:
  title_regex: 's/^\d\d\d\d-\d\d.*$//'
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Albums.CleanTitle("2025-03 Trackday"); got != "2025-03 Trackday" {
		t.Errorf("blanked result = %q, want the original kept", got)
	}
}

func TestTitleRegexGlobalFlagAndGroups(t *testing.T) {
	cfg, err := Load(writeConfig(t, `albums:
  title_regex: 's/(\d\d\d\d)-(\d\d)/$2\/$1/g'
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Albums.CleanTitle("2025-03 and 2024-11"), "03/2025 and 11/2024"; got != want {
		t.Errorf("CleanTitle = %q, want %q", got, want)
	}

	cfg, err = Load(writeConfig(t, `albums:
  title_regex: 's/x/y/'
`))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Albums.CleanTitle("axbxc"), "aybxc"; got != want {
		t.Errorf("without g, CleanTitle = %q, want %q (first match only)", got, want)
	}
}

// No title_regex configured must be the identity, including for a hand-built
// config that never went through Load.
func TestCleanTitleWithoutRegexIsIdentity(t *testing.T) {
	var albums Albums
	if got := albums.CleanTitle("2025-03 Untouched"); got != "2025-03 Untouched" {
		t.Errorf("CleanTitle = %q, want identity", got)
	}

	cfg, err := Load(writeConfig(t, "output: ./out\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.Albums.CleanTitle("2025-03 Untouched"); got != "2025-03 Untouched" {
		t.Errorf("CleanTitle = %q, want identity", got)
	}
}

// A broken substitution is a config error at load, not a surprise mid-build.
func TestTitleRegexErrorsAtLoad(t *testing.T) {
	for _, body := range []string{
		"albums:\n  title_regex: 's/[unclosed/'\n",
		"albums:\n  title_regex: 's/a/b/x'\n",
		"albums:\n  title_regex: 's/a/b'\n",
		"albums:\n  title_regex: replace-me\n",
	} {
		_, err := Load(writeConfig(t, body))
		if err == nil {
			t.Errorf("Load accepted %q, expected an error", body)
		}
		if err != nil && !strings.Contains(err.Error(), "title_regex") {
			t.Errorf("error for %q does not name the setting: %v", body, err)
		}
	}
}

func TestRouteDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.RouteEnabled() {
		t.Error("RouteEnabled() = false, want true by default")
	}
	if cfg.Route.Width != 320 || cfg.Route.Height != 320 {
		t.Errorf("Route dimensions = %d/%d, want 320/320", cfg.Route.Width, cfg.Route.Height)
	}
}

func TestRouteCanBeDisabled(t *testing.T) {
	cfg, err := Load(writeConfig(t, "route:\n  enabled: false\n"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.RouteEnabled() {
		t.Error("RouteEnabled() = true despite route.enabled: false")
	}
}

// A width/height of exactly 0 is indistinguishable from "unset" (an int's
// zero value) and so is defaulted rather than rejected, same as every other
// bare-int config field here; only a negative value is unambiguously wrong
// and reaches validation.
func TestInvalidRouteDimensionsIsAnError(t *testing.T) {
	for _, body := range []string{
		"route:\n  width: -1\n",
		"route:\n  height: -10\n",
	} {
		_, err := Load(writeConfig(t, body))
		if err == nil {
			t.Errorf("Load accepted %q, expected an error", body)
		}
	}
}
