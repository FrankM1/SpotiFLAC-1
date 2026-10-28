package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResolveVideoReturnsSignedFileURL(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/download-youtube?v=adDQxrFdK4I&title=Afro+Dite&artist=Brian+Mart", nil)
	req.Host = "127.0.0.1:8790"
	handleResolveVideo(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Success bool `json:"success"`
		Data    struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	prefix := "http://127.0.0.1:8790/file/"
	if !body.Success || !strings.HasPrefix(body.Data.URL, prefix) {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
	payload, ok := verifyToken(strings.TrimPrefix(body.Data.URL, prefix))
	if !ok {
		t.Fatal("file token did not verify")
	}
	if payload.Video != "adDQxrFdK4I" || payload.Title != "Afro Dite" || payload.Artist != "Brian Mart" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestResolveVideoRejectsBadInput(t *testing.T) {
	for name, query := range map[string]string{
		"no id":     "title=a&artist=b",
		"short id":  "v=abc&title=a&artist=b",
		"bad chars": "v=adDQxrFdK4%3F&title=a&artist=b",
		"no title":  "v=adDQxrFdK4I&artist=b",
		"no artist": "v=adDQxrFdK4I&title=a",
	} {
		rec := httptest.NewRecorder()
		handleResolveVideo(rec, httptest.NewRequest(http.MethodGet, "/api/download-youtube?"+query, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, rec.Code)
		}
	}
}

func TestPickCandidatePrefersYouTubeMusicHit(t *testing.T) {
	cands := []ytCandidate{
		{ID: "fan00000001", Title: "Artist - Song", Channel: "Some Fan", Duration: 240},
		{ID: "topic000001", Title: "Song", Channel: "Artist - Topic", Duration: 240},
		{ID: "music000001", Title: "Song", Channel: "Artist", Duration: 241, Music: true},
	}
	got, err := pickCandidate(cands, 240)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "music000001" {
		t.Fatalf("expected the YouTube Music hit, got %s", got.ID)
	}
}

func TestPickCandidateMusicHitStillGatedByDuration(t *testing.T) {
	cands := []ytCandidate{
		{ID: "topic000001", Title: "Song", Channel: "Artist - Topic", Duration: 240},
		{ID: "music000001", Title: "Song (Extended Mix)", Channel: "Artist", Duration: 420, Music: true},
	}
	got, err := pickCandidate(cands, 240)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "topic000001" {
		t.Fatalf("out-of-tolerance music hit should lose to the in-tolerance Topic upload, got %s", got.ID)
	}
}

func TestPickCandidateFallsBackToMusicHitWhenNothingFits(t *testing.T) {
	cands := []ytCandidate{
		{ID: "fan00000001", Title: "Artist - Song", Channel: "Some Fan", Duration: 300},
		{ID: "music000001", Title: "Song", Channel: "Artist", Duration: 260, Music: true},
	}
	got, err := pickCandidate(cands, 240)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "music000001" {
		t.Fatalf("expected fallback to the music hit, got %s", got.ID)
	}
}

func TestPickCandidateWithoutMusicKeepsOldBehaviour(t *testing.T) {
	cands := []ytCandidate{
		{ID: "fan00000001", Title: "Artist - Song", Channel: "Some Fan", Duration: 240},
		{ID: "topic000001", Title: "Song", Channel: "Artist - Topic", Duration: 243},
	}
	got, err := pickCandidate(cands, 240)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "topic000001" {
		t.Fatalf("expected the Topic upload, got %s", got.ID)
	}
	if _, err := pickCandidate([]ytCandidate{{ID: "fan00000001", Duration: 300}}, 240); err == nil {
		t.Fatal("expected an error when nothing fits and nothing is official")
	}
}
