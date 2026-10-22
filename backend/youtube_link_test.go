package backend

import "testing"

func TestYouTubeLinkFilename(t *testing.T) {
	cases := []struct {
		format string
		want   string
	}{
		{"", "Afro Dite - Brian Mart [yt-adDQxrFdK4I].flac"},
		{"artist-title", "Brian Mart - Afro Dite [yt-adDQxrFdK4I].flac"},
		{"{artist} - {title}", "Brian Mart - Afro Dite [yt-adDQxrFdK4I].flac"},
	}
	for _, tc := range cases {
		if got := YouTubeLinkFilename("Afro Dite", "Brian Mart", tc.format, "adDQxrFdK4I"); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.format, got, tc.want)
		}
	}
}
