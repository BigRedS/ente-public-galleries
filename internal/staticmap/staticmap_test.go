package staticmap

import "testing"

func TestConvertTileURLTemplate(t *testing.T) {
	tests := []struct {
		name        string
		template    string
		wantPattern string
		wantShards  []string
		wantErr     bool
	}{
		{
			name:        "plain zxy",
			template:    "https://tile.openstreetmap.org/{z}/{x}/{y}.png",
			wantPattern: "https://tile.openstreetmap.org/%[2]d/%[3]d/%[4]d.png",
		},
		{
			name:        "sharded subdomain",
			template:    "https://{s}.tile.example.com/{z}/{x}/{y}.png",
			wantPattern: "https://%[1]s.tile.example.com/%[2]d/%[3]d/%[4]d.png",
			wantShards:  []string{"a", "b", "c"},
		},
		{
			name:     "missing placeholders",
			template: "https://tile.example.com/static.png",
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pattern, shards, err := ConvertTileURLTemplate(test.template)
			if test.wantErr {
				if err == nil {
					t.Fatalf("ConvertTileURLTemplate(%q) succeeded, want an error", test.template)
				}
				return
			}
			if err != nil {
				t.Fatalf("ConvertTileURLTemplate(%q): %v", test.template, err)
			}
			if pattern != test.wantPattern {
				t.Errorf("pattern = %q, want %q", pattern, test.wantPattern)
			}
			if len(shards) != len(test.wantShards) {
				t.Errorf("shards = %v, want %v", shards, test.wantShards)
			}
			for i := range shards {
				if i >= len(test.wantShards) || shards[i] != test.wantShards[i] {
					t.Errorf("shards = %v, want %v", shards, test.wantShards)
					break
				}
			}
		})
	}
}

func TestPlainTextAttribution(t *testing.T) {
	tests := []struct{ in, want string }{
		{
			in:   `&copy; <a href="https://www.openstreetmap.org/copyright">OpenStreetMap</a> contributors`,
			want: "© OpenStreetMap contributors",
		},
		{in: "Plain text already", want: "Plain text already"},
		{in: "", want: ""},
	}
	for _, test := range tests {
		if got := plainTextAttribution(test.in); got != test.want {
			t.Errorf("plainTextAttribution(%q) = %q, want %q", test.in, got, test.want)
		}
	}
}
