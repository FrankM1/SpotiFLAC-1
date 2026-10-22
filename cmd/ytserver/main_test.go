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
