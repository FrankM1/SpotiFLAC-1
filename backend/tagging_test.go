package backend

import "testing"

func TestBuildTagMapWritesSource(t *testing.T) {
	tags := buildTagMap("track.flac", Metadata{
		Title:       "Afro Dite",
		Separator:   ";",
		Comment:     "https://open.spotify.com/track/x",
		Description: "Source: YouTube (lossy transcode) - not true lossless",
		Source:      "YouTube",
	})
	if got := tags["SOURCE"]; len(got) != 1 || got[0] != "YouTube" {
		t.Fatalf("expected SOURCE=YouTube, got %v", got)
	}
	if got := tags["DESCRIPTION"]; len(got) != 1 || got[0] != "Source: YouTube (lossy transcode) - not true lossless" {
		t.Fatalf("unexpected DESCRIPTION: %v", got)
	}
}

func TestBuildTagMapOmitsEmptySource(t *testing.T) {
	tags := buildTagMap("track.flac", Metadata{Title: "Afro Dite", Separator: ";"})
	if _, ok := tags["SOURCE"]; ok {
		t.Fatalf("expected no SOURCE tag, got %v", tags["SOURCE"])
	}
}
