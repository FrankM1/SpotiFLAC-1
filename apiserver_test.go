package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testMux builds the API routes without starting the listener or the worker,
// so no test hits the network or a real download.
func testMux(s *apiServer) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/download", s.handleDownload)
	mux.HandleFunc("/download/batch", s.handleDownloadBatch)
	mux.HandleFunc("/download/", s.handleJobStatus)
	return withAPIHeaders(mux)
}

// newAPIRequest builds a request with a loopback Host, matching what the
// real listener receives (httptest defaults to example.com, which the
// anti-rebinding check rejects).
func newAPIRequest(method, target string, body *strings.Reader) *http.Request {
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, body)
	}
	req.Host = "127.0.0.1:8787"
	return req
}

func TestHealth(t *testing.T) {
	s := newAPIServer(&App{})
	rec := httptest.NewRecorder()
	testMux(s).ServeHTTP(rec, newAPIRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Fatalf("expected status ok, got %q", body["status"])
	}
}

func TestValidateAPIRequest(t *testing.T) {
	cases := []struct {
		name    string
		req     apiDownloadRequest
		wantErr bool
	}{
		{"spotify id only", apiDownloadRequest{SpotifyID: "3n3Ppam7vgaVa1iaRUc9Lp"}, false},
		{"artist and title", apiDownloadRequest{Artist: "Oscar Mbo", Title: "Vuka"}, false},
		{"isrc only", apiDownloadRequest{ISRC: "ZAA012000123"}, true},
		{"artist only", apiDownloadRequest{Artist: "Oscar Mbo"}, true},
		{"title only", apiDownloadRequest{Title: "Vuka"}, true},
		{"empty", apiDownloadRequest{}, true},
		{"whitespace id", apiDownloadRequest{SpotifyID: "  "}, true},
	}
	for _, tc := range cases {
		err := validateAPIRequest(tc.req)
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestDownloadEnqueueAndPoll(t *testing.T) {
	s := newAPIServer(&App{})
	mux := testMux(s)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"spotifyId":"3n3Ppam7vgaVa1iaRUc9Lp","outputDir":"/tmp/x"}`)
	mux.ServeHTTP(rec, newAPIRequest(http.MethodPost, "/download", body))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["jobId"] == "" || resp["status"] != "queued" {
		t.Fatalf("unexpected enqueue response: %v", resp)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, newAPIRequest(http.MethodGet, "/download/"+resp["jobId"], nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on poll, got %d", rec.Code)
	}
	var view apiJobView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if view.JobID != resp["jobId"] || view.Status != "queued" {
		t.Fatalf("unexpected job view: %+v", view)
	}
	if view.Error != nil {
		t.Fatalf("expected null error, got %q", *view.Error)
	}
}

func TestDownloadRejectsBadRequests(t *testing.T) {
	s := newAPIServer(&App{})
	mux := testMux(s)

	for name, payload := range map[string]string{
		"invalid json":       `{`,
		"no ids":             `{"outputDir":"/tmp/x"}`,
		"isrc only":          `{"isrc":"ZAA012000123"}`,
		"relative outputDir": `{"spotifyId":"x","outputDir":"Music/library"}`,
		"dotdot outputDir":   `{"spotifyId":"x","outputDir":"/Users/frank/../../etc"}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newAPIRequest(http.MethodPost, "/download", strings.NewReader(payload)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: invalid error JSON: %v", name, err)
			continue
		}
		if body["code"] != "bad_request" {
			t.Errorf("%s: expected code bad_request, got %q", name, body["code"])
		}
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, newAPIRequest(http.MethodGet, "/download", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /download: expected 405, got %d", rec.Code)
	}
}

func TestDownloadBatch(t *testing.T) {
	s := newAPIServer(&App{})
	mux := testMux(s)

	rec := httptest.NewRecorder()
	body := strings.NewReader(`[
		{"spotifyId":"3n3Ppam7vgaVa1iaRUc9Lp"},
		{"isrc":"ZAA012000123"},
		{"artist":"Oscar Mbo","title":"Vuka"}
	]`)
	mux.ServeHTTP(rec, newAPIRequest(http.MethodPost, "/download/batch", body))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	var out []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 results, got %d", len(out))
	}
	if out[0]["status"] != "queued" || out[0]["jobId"] == "" {
		t.Errorf("entry 0: expected queued with jobId, got %v", out[0])
	}
	if out[1]["status"] != "rejected" || out[1]["error"] == "" {
		t.Errorf("entry 1: expected rejected with error, got %v", out[1])
	}
	if out[2]["status"] != "queued" || out[2]["jobId"] == "" {
		t.Errorf("entry 2: expected queued with jobId, got %v", out[2])
	}
}

func TestJobStatusUnknownID(t *testing.T) {
	s := newAPIServer(&App{})
	rec := httptest.NewRecorder()
	testMux(s).ServeHTTP(rec, newAPIRequest(http.MethodGet, "/download/dl_nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestStartServesOverTCP(t *testing.T) {
	t.Setenv("SPOTIFLAC_API_PORT", "18787")
	s := newAPIServer(&App{})
	s.start()
	if s.srv == nil {
		t.Fatal("server did not start (port in use?)")
	}
	defer s.stop()

	resp, err := http.Get("http://127.0.0.1:18787/health")
	if err != nil {
		t.Fatalf("GET /health failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestCORSPreflightAllowedOrigin(t *testing.T) {
	s := newAPIServer(&App{})
	rec := httptest.NewRecorder()
	req := newAPIRequest(http.MethodOptions, "/download", nil)
	req.Header.Set("Origin", "tauri://localhost")
	testMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 on preflight, got %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("expected origin echoed, got %q", got)
	}
}

func TestRejectsForeignOrigin(t *testing.T) {
	s := newAPIServer(&App{})
	mux := testMux(s)
	for _, origin := range []string{"https://evil.example", "http://localhost.evil.example", "null"} {
		rec := httptest.NewRecorder()
		req := newAPIRequest(http.MethodPost, "/download", strings.NewReader(`{"spotifyId":"x"}`))
		req.Header.Set("Origin", origin)
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("origin %q: expected 403, got %d", origin, rec.Code)
		}
	}
	if len(s.jobs) != 0 {
		t.Fatalf("rejected requests must not enqueue jobs, got %d", len(s.jobs))
	}
}

func TestRejectsForeignHost(t *testing.T) {
	s := newAPIServer(&App{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/download", strings.NewReader(`{"spotifyId":"x"}`))
	req.Host = "attacker.example:8787"
	testMux(s).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for DNS-rebinding Host, got %d", rec.Code)
	}
}

func TestNoCORSHeaderWithoutOrigin(t *testing.T) {
	s := newAPIServer(&App{})
	rec := httptest.NewRecorder()
	testMux(s).ServeHTTP(rec, newAPIRequest(http.MethodGet, "/health", nil))
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("no Origin header should mean no CORS grant")
	}
}

func TestLossyJobReportsYouTubeSource(t *testing.T) {
	s := newAPIServer(&App{})
	job := s.enqueue(apiDownloadRequest{Artist: "Brian Mart", Title: "Afro Dite"})
	s.completeJob(job, "qobuz", DownloadResponse{
		Success:     true,
		File:        "/tmp/x/Afro Dite.flac",
		SourceLabel: "Brian Mart - Afro Dite",
		Lossy:       true,
	})

	rec := httptest.NewRecorder()
	testMux(s).ServeHTTP(rec, newAPIRequest(http.MethodGet, "/download/"+job.ID, nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"lossy":true`) {
		t.Fatalf("expected lossy:true in %s", body)
	}
	var view apiJobView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if view.Status != "done" || view.Source != "youtube (Brian Mart - Afro Dite)" {
		t.Fatalf("unexpected job view: %+v", view)
	}
}

func TestLosslessJobKeepsServiceSource(t *testing.T) {
	s := newAPIServer(&App{})
	job := s.enqueue(apiDownloadRequest{Artist: "Brian Mart", Title: "Afro Dite"})
	s.completeJob(job, "tidal", DownloadResponse{Success: true, File: "/tmp/x/a.flac", SourceLabel: "Brian Mart - Afro Dite"})

	view := s.jobView(job)
	if view.Lossy || view.Source != "tidal (Brian Mart - Afro Dite)" {
		t.Fatalf("unexpected job view: %+v", view)
	}
	rec := httptest.NewRecorder()
	testMux(s).ServeHTTP(rec, newAPIRequest(http.MethodGet, "/download/"+job.ID, nil))
	if strings.Contains(rec.Body.String(), "lossy") {
		t.Fatalf("lossless job should omit lossy: %s", rec.Body.String())
	}
}

func TestParseYouTubeVideoID(t *testing.T) {
	const id = "adDQxrFdK4I"
	for _, raw := range []string{
		"https://www.youtube.com/watch?v=adDQxrFdK4I",
		"https://www.youtube.com/watch?v=adDQxrFdK4I&list=RDadDQxrFdK4I&t=42s",
		"https://youtube.com/watch?feature=share&v=adDQxrFdK4I",
		"http://m.youtube.com/watch?v=adDQxrFdK4I",
		"youtube.com/watch?v=adDQxrFdK4I",
		"https://youtu.be/adDQxrFdK4I",
		"https://youtu.be/adDQxrFdK4I?si=abc123&t=10",
		"https://music.youtube.com/watch?v=adDQxrFdK4I&feature=share",
		"https://www.youtube.com/shorts/adDQxrFdK4I",
		"https://youtube.com/shorts/adDQxrFdK4I?feature=share",
		"adDQxrFdK4I",
		"  adDQxrFdK4I  ",
	} {
		got, ok := parseYouTubeVideoID(raw)
		if !ok || got != id {
			t.Errorf("%q: got (%q, %v), want %q", raw, got, ok, id)
		}
	}

	for _, raw := range []string{
		"",
		"not a link",
		"adDQxrFdK4",
		"adDQxrFdK4I1",
		"adDQxrFdK4?",
		"https://www.youtube.com/watch",
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/channel/UCadDQxrFdK4I",
		"https://www.youtube.com/playlist?list=PLadDQxrFdK4I",
		"https://vimeo.com/watch?v=adDQxrFdK4I",
		"https://evil-youtube.com/watch?v=adDQxrFdK4I",
		"https://youtu.be/",
		"ftp://youtu.be/adDQxrFdK4I",
	} {
		if got, ok := parseYouTubeVideoID(raw); ok {
			t.Errorf("%q: expected rejection, got %q", raw, got)
		}
	}
}

func TestYouTubeLinkRequestValidation(t *testing.T) {
	s := newAPIServer(&App{})
	mux := testMux(s)

	for name, payload := range map[string]string{
		"junk link":    `{"youtubeUrl":"https://example.com/nope","artist":"Brian Mart","title":"Afro Dite"}`,
		"channel link": `{"youtubeUrl":"https://www.youtube.com/@brianmart","artist":"Brian Mart","title":"Afro Dite"}`,
		"no artist":    `{"youtubeUrl":"https://youtu.be/adDQxrFdK4I","title":"Afro Dite"}`,
		"no title":     `{"youtubeUrl":"https://youtu.be/adDQxrFdK4I","artist":"Brian Mart"}`,
		"spotify only": `{"youtubeUrl":"https://youtu.be/adDQxrFdK4I","spotifyId":"3n3Ppam7vgaVa1iaRUc9Lp"}`,
		"blank artist": `{"youtubeUrl":"adDQxrFdK4I","artist":"  ","title":"Afro Dite"}`,
		"relative dir": `{"youtubeUrl":"adDQxrFdK4I","artist":"Brian Mart","title":"Afro Dite","outputDir":"music"}`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, newAPIRequest(http.MethodPost, "/download", strings.NewReader(payload)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: expected 400, got %d", name, rec.Code)
			continue
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["code"] != "bad_request" || body["error"] == "" {
			t.Errorf("%s: unexpected error body %s", name, rec.Body.String())
		}
	}
	if len(s.jobs) != 0 {
		t.Fatalf("rejected requests must not enqueue jobs, got %d", len(s.jobs))
	}

	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"youtubeUrl":"https://www.youtube.com/watch?v=adDQxrFdK4I&t=5","artist":"Brian Mart","title":"Afro Dite","outputDir":"/tmp/x"}`)
	mux.ServeHTTP(rec, newAPIRequest(http.MethodPost, "/download", body))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid youtube link: expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestYouTubeLinkJobView(t *testing.T) {
	s := newAPIServer(&App{})
	job := s.enqueue(apiDownloadRequest{YouTubeURL: "https://youtu.be/adDQxrFdK4I", Artist: "Brian Mart", Title: "Afro Dite"})
	s.completeYouTubeLinkJob(job, "adDQxrFdK4I", "/tmp/x/Afro Dite - Brian Mart [yt-adDQxrFdK4I].flac")

	rec := httptest.NewRecorder()
	testMux(s).ServeHTTP(rec, newAPIRequest(http.MethodGet, "/download/"+job.ID, nil))
	var view apiJobView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if view.Status != "done" || !view.Lossy || view.Source != "youtube (adDQxrFdK4I)" ||
		view.FilePath != "/tmp/x/Afro Dite - Brian Mart [yt-adDQxrFdK4I].flac" || view.Error != nil {
		t.Fatalf("unexpected job view: %+v", view)
	}
}

func TestYouTubeRelayURL(t *testing.T) {
	cases := map[string]string{
		"":                          defaultYTRelayURL,
		"https://qobuz.example/api": defaultYTRelayURL,
		"http://127.0.0.1:8790/":    "http://127.0.0.1:8790",
		"http://localhost:9000":     "http://localhost:9000",
	}
	for custom, want := range cases {
		got := ytRelayURL(map[string]interface{}{"customQobuzApi": custom})
		if got != want {
			t.Errorf("%q: got %q, want %q", custom, got, want)
		}
	}
}
