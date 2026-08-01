package domain

import "testing"

func TestParseGenre(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    Genre
		wantErr bool
	}{
		{name: "empty defaults to novel", raw: "", want: GenreNovel},
		{name: "trimmed novel", raw: "  novel  ", want: GenreNovel},
		{name: "short story", raw: "short_story", want: GenreShortStory},
		{name: "unsupported", raw: "screenplay", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseGenre(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseGenre(%q) 应报错，得到 %q", tt.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseGenre(%q): %v", tt.raw, err)
			}
			if got != tt.want {
				t.Fatalf("ParseGenre(%q) = %q，期望 %q", tt.raw, got, tt.want)
			}
		})
	}
}
