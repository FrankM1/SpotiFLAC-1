package backend

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// GetQobuzTrackMetadata fetches a single Qobuz track's public metadata by ID.
// It reuses the signed public-API path (scraped app_id/secret), so it needs no
// subscriber account. Used by the YouTube-backed relay server to turn a Qobuz
// track ID into a title/artist/duration for YouTube matching.
//
// Note: Qobuz's track/get endpoint 404s for the web-player app_id, but
// track/search with the numeric ID as the query reliably returns the same
// track with full metadata, so we resolve via search and match the exact ID.
func GetQobuzTrackMetadata(trackID string) (*QobuzTrack, error) {
	trackID = strings.TrimSpace(trackID)
	if trackID == "" {
		return nil, fmt.Errorf("empty track id")
	}
	var resp qobuzPublicSearchResponse
	if err := doQobuzSignedJSONRequest("track/search", url.Values{"query": {trackID}, "limit": {"10"}}, &resp); err != nil {
		return nil, fmt.Errorf("qobuz track/search failed: %w", err)
	}
	wantID, _ := strconv.ParseInt(trackID, 10, 64)
	for i := range resp.Tracks.Items {
		if resp.Tracks.Items[i].ID == wantID {
			return &resp.Tracks.Items[i], nil
		}
	}
	// Fall back to the first result if the exact ID isn't echoed back.
	if len(resp.Tracks.Items) > 0 {
		return &resp.Tracks.Items[0], nil
	}
	return nil, fmt.Errorf("qobuz track %s not found", trackID)
}

// QobuzTrackDisplayArtist returns the best artist name for a track (exported
// helper around the internal selection logic).
func QobuzTrackDisplayArtist(track *QobuzTrack) string {
	if track == nil {
		return ""
	}
	return qobuzTrackDisplayArtist(*track)
}
