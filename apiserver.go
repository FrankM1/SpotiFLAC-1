// Local HTTP API for companion apps (soniq8-DJ) to request missing tracks.
// Contract: soniq8-DJ/docs/spotiflac-api-contract.md (v1).
//
// The server binds to loopback only and reuses App.DownloadTrack, so API
// downloads show up in the normal queue UI, respect the user's provider
// settings (downloader/autoOrder, custom Qobuz/Tidal URLs), and land in
// history like any other download.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/afkarxyz/SpotiFLAC/backend"
)

const apiDefaultPort = 8787

type apiJobStatus string

const (
	apiJobQueued      apiJobStatus = "queued"
	apiJobDownloading apiJobStatus = "downloading"
	apiJobDone        apiJobStatus = "done"
	apiJobFailed      apiJobStatus = "failed"
)

type apiDownloadRequest struct {
	SpotifyID string `json:"spotifyId"`
	ISRC      string `json:"isrc"`
	Artist    string `json:"artist"`
	Title     string `json:"title"`
	OutputDir string `json:"outputDir"`
}

type apiJob struct {
	ID        string
	Req       apiDownloadRequest
	Status    apiJobStatus
	FilePath  string
	Source    string
	Error     string
	ItemID    string
	CreatedAt time.Time
}

type apiJobView struct {
	JobID      string   `json:"jobId"`
	Status     string   `json:"status"`
	Progress   *float64 `json:"progress"`
	ProgressMB float64  `json:"progressMb"`
	SpeedMBps  float64  `json:"speedMbps"`
	StatusText string   `json:"statusText,omitempty"`
	FilePath   string   `json:"filePath,omitempty"`
	Source     string   `json:"source,omitempty"`
	Error      *string  `json:"error"`
}

type apiServer struct {
	app    *App
	mu     sync.Mutex
	jobs   map[string]*apiJob
	queue  chan *apiJob
	nextID int
	srv    *http.Server
}

func newAPIServer(app *App) *apiServer {
	return &apiServer{
		app:   app,
		jobs:  make(map[string]*apiJob),
		queue: make(chan *apiJob, 256),
	}
}

func (s *apiServer) start() {
	port := apiDefaultPort
	if v := strings.TrimSpace(os.Getenv("SPOTIFLAC_API_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 && p < 65536 {
			port = p
		} else {
			fmt.Printf("[API] Ignoring invalid SPOTIFLAC_API_PORT=%q\n", v)
		}
	}

	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		fmt.Printf("[API] Failed to listen on %s: %v (companion API disabled)\n", addr, err)
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/download", s.handleDownload)
	mux.HandleFunc("/download/batch", s.handleDownloadBatch)
	mux.HandleFunc("/download/", s.handleJobStatus)

	s.srv = &http.Server{Handler: withAPIHeaders(mux)}

	go s.worker()
	go func() {
		if err := s.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Printf("[API] Server stopped: %v\n", err)
		}
	}()
	fmt.Printf("[API] Companion API listening on http://%s\n", addr)
}

func (s *apiServer) stop() {
	if s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

// apiHostAllowed blocks DNS-rebinding: a page on attacker.com resolving to
// 127.0.0.1 reaches the listener with Host: attacker.com.
func apiHostAllowed(host string) bool {
	h := host
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		h = parsed
	}
	h = strings.Trim(strings.ToLower(h), "[]")
	return h == "127.0.0.1" || h == "localhost" || h == "::1"
}

// apiOriginAllowed gates browser-context callers. Requests without an Origin
// header (curl, native HTTP clients like soniq8-DJ's Rust core) are allowed;
// webview/browser requests are allowed only from local origins, so an
// arbitrary website cannot drive downloads from the user's browser.
func apiOriginAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	o := strings.ToLower(strings.TrimSpace(origin))
	if o == "tauri://localhost" || o == "https://tauri.localhost" {
		return true
	}
	for _, prefix := range []string{"http://localhost", "http://127.0.0.1", "https://localhost", "https://127.0.0.1"} {
		if o == prefix || strings.HasPrefix(o, prefix+":") {
			return true
		}
	}
	return false
}

// withAPIHeaders enforces Host/Origin checks and sets JSON + CORS headers.
// The allowed origin is echoed back (never *): the API is privileged, so only
// local webview clients get cross-origin access.
func withAPIHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if !apiHostAllowed(r.Host) {
			apiWriteError(w, http.StatusForbidden, "forbidden", "invalid Host header")
			return
		}
		origin := r.Header.Get("Origin")
		if !apiOriginAllowed(origin) {
			apiWriteError(w, http.StatusForbidden, "forbidden", "origin not allowed")
			return
		}
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func apiWriteJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func apiWriteError(w http.ResponseWriter, status int, code, msg string) {
	apiWriteJSON(w, status, map[string]string{"error": msg, "code": code})
}

func (s *apiServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	apiWriteJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"version": backend.AppVersion,
	})
}

func validateAPIRequest(req apiDownloadRequest) error {
	hasID := strings.TrimSpace(req.SpotifyID) != "" ||
		(strings.TrimSpace(req.Artist) != "" && strings.TrimSpace(req.Title) != "")
	if !hasID {
		return fmt.Errorf("spotifyId or artist+title required (isrc alone cannot be resolved)")
	}
	if dir := strings.TrimSpace(req.OutputDir); dir != "" {
		clean := filepath.Clean(dir)
		if !filepath.IsAbs(clean) {
			return fmt.Errorf("outputDir must be an absolute path")
		}
		for _, part := range strings.Split(dir, string(os.PathSeparator)) {
			if part == ".." {
				return fmt.Errorf("outputDir must not contain '..'")
			}
		}
	}
	return nil
}

func (s *apiServer) enqueue(req apiDownloadRequest) *apiJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	job := &apiJob{
		ID:        fmt.Sprintf("dl_%d_%d", time.Now().Unix(), s.nextID),
		Req:       req,
		Status:    apiJobQueued,
		CreatedAt: time.Now(),
	}
	s.jobs[job.ID] = job
	s.queue <- job
	return job
}

func (s *apiServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiWriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var req apiDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		apiWriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON body")
		return
	}
	if err := validateAPIRequest(req); err != nil {
		apiWriteError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	job := s.enqueue(req)
	apiWriteJSON(w, http.StatusAccepted, map[string]string{"jobId": job.ID, "status": string(job.Status)})
}

func (s *apiServer) handleDownloadBatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiWriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST required")
		return
	}
	var reqs []apiDownloadRequest
	if err := json.NewDecoder(r.Body).Decode(&reqs); err != nil {
		apiWriteError(w, http.StatusBadRequest, "bad_request", "invalid JSON body (expected array)")
		return
	}
	out := make([]map[string]string, 0, len(reqs))
	for _, req := range reqs {
		if err := validateAPIRequest(req); err != nil {
			out = append(out, map[string]string{"jobId": "", "status": "rejected", "error": err.Error()})
			continue
		}
		job := s.enqueue(req)
		out = append(out, map[string]string{"jobId": job.ID, "status": string(job.Status)})
	}
	apiWriteJSON(w, http.StatusAccepted, out)
}

func (s *apiServer) handleJobStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		apiWriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	jobID := strings.TrimPrefix(r.URL.Path, "/download/")
	s.mu.Lock()
	job, ok := s.jobs[jobID]
	s.mu.Unlock()
	if !ok {
		apiWriteError(w, http.StatusNotFound, "not_found", "unknown job id")
		return
	}
	apiWriteJSON(w, http.StatusOK, s.jobView(job))
}

func (s *apiServer) jobView(job *apiJob) apiJobView {
	s.mu.Lock()
	view := apiJobView{
		JobID:    job.ID,
		Status:   string(job.Status),
		FilePath: job.FilePath,
		Source:   job.Source,
	}
	if job.Error != "" {
		e := job.Error
		view.Error = &e
	}
	itemID := job.ItemID
	s.mu.Unlock()

	if view.Status == string(apiJobDownloading) && itemID != "" {
		queueInfo := backend.GetDownloadQueue()
		for _, item := range queueInfo.Queue {
			if item.ID == itemID {
				view.ProgressMB = item.Progress
				view.SpeedMBps = item.Speed
				break
			}
		}
		view.StatusText = backend.GetDownloadProgress().StatusText
	}
	return view
}

func (s *apiServer) worker() {
	for job := range s.queue {
		s.runJob(job)
	}
}

func (s *apiServer) setJobStatus(job *apiJob, status apiJobStatus) {
	s.mu.Lock()
	job.Status = status
	s.mu.Unlock()
}

func (s *apiServer) failJob(job *apiJob, msg string) {
	s.mu.Lock()
	job.Status = apiJobFailed
	job.Error = msg
	itemID := job.ItemID
	s.mu.Unlock()
	if itemID != "" {
		backend.FailDownloadItem(itemID, msg)
	}
	fmt.Printf("[API] Job %s failed: %s\n", job.ID, msg)
}

// resolveTrack fills in full Spotify metadata for the request. Returns the
// resolved track or an error when nothing usable was found.
func (s *apiServer) resolveTrack(job *apiJob) (*backend.TrackMetadata, error) {
	spotifyID := strings.TrimSpace(job.Req.SpotifyID)

	if spotifyID == "" {
		query := strings.TrimSpace(job.Req.Artist + " " + job.Req.Title)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		resp, err := backend.SearchSpotify(ctx, query, 5)
		cancel()
		if err != nil {
			return nil, fmt.Errorf("spotify search failed: %v", err)
		}
		if resp == nil || len(resp.Tracks) == 0 {
			return nil, fmt.Errorf("no spotify match for %q", query)
		}
		spotifyID = resp.Tracks[0].ID
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	trackURL := fmt.Sprintf("https://open.spotify.com/track/%s", spotifyID)
	data, err := backend.GetFilteredSpotifyData(ctx, trackURL, false, 0, ", ", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch spotify metadata: %v", err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("failed to decode spotify metadata: %v", err)
	}
	var trackResp backend.TrackResponse
	if err := json.Unmarshal(raw, &trackResp); err != nil {
		return nil, fmt.Errorf("failed to decode spotify metadata: %v", err)
	}
	track := trackResp.Track
	if track.Name == "" || track.Artists == "" {
		return nil, fmt.Errorf("spotify metadata incomplete for track %s", spotifyID)
	}
	if track.SpotifyID == "" {
		track.SpotifyID = spotifyID
	}
	return &track, nil
}

func apiSettingString(settings map[string]interface{}, key string) string {
	if settings == nil {
		return ""
	}
	if v, ok := settings[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func apiSettingBool(settings map[string]interface{}, key string) bool {
	if settings == nil {
		return false
	}
	if v, ok := settings[key].(bool); ok {
		return v
	}
	return false
}

func (s *apiServer) runJob(job *apiJob) {
	s.setJobStatus(job, apiJobDownloading)

	track, err := s.resolveTrack(job)
	if err != nil {
		s.failJob(job, err.Error())
		return
	}

	settings, _ := s.app.LoadSettings()

	outputDir := strings.TrimSpace(job.Req.OutputDir)
	if outputDir == "" {
		outputDir = apiSettingString(settings, "downloadPath")
	}
	if outputDir == "" {
		outputDir = backend.GetDefaultMusicPath()
	}

	// Mirror the frontend's provider selection: explicit downloader, or the
	// user's auto order.
	downloader := apiSettingString(settings, "downloader")
	var order []string
	switch downloader {
	case "tidal", "qobuz", "amazon":
		order = []string{downloader}
	default:
		autoOrder := apiSettingString(settings, "autoOrder")
		if autoOrder == "" {
			autoOrder = "tidal-qobuz-amazon"
		}
		order = strings.Split(autoOrder, "-")
	}

	is24Bit := apiSettingString(settings, "autoQuality") == "24"
	customTidalAPI := apiSettingString(settings, "customTidalApi")
	customQobuzAPI := strings.TrimRight(apiSettingString(settings, "customQobuzApi"), "/")
	filenameFormat := apiSettingString(settings, "filenameTemplate")

	itemID := fmt.Sprintf("api-%s", job.ID)
	backend.AddToQueue(itemID, track.Name, track.Artists, track.AlbumName, track.SpotifyID)
	s.mu.Lock()
	job.ItemID = itemID
	s.mu.Unlock()

	durationSeconds := 0
	if track.DurationMS > 0 {
		durationSeconds = (track.DurationMS + 500) / 1000
	}

	var errorsSeen []string
	for _, service := range order {
		audioFormat := ""
		switch service {
		case "tidal":
			if is24Bit {
				audioFormat = "HI_RES_LOSSLESS"
			} else {
				audioFormat = "LOSSLESS"
			}
		case "qobuz":
			if is24Bit {
				audioFormat = "27"
			} else {
				audioFormat = "6"
			}
		case "amazon":
			if is24Bit {
				audioFormat = "24"
			} else {
				audioFormat = "16"
			}
		default:
			continue
		}

		req := DownloadRequest{
			Service:              service,
			SpotifyID:            track.SpotifyID,
			TrackName:            track.Name,
			ArtistName:           track.Artists,
			Artists:              track.Artists,
			AlbumName:            track.AlbumName,
			AlbumArtist:          track.AlbumArtist,
			ReleaseDate:          track.ReleaseDate,
			CoverURL:             track.Images,
			OutputDir:            outputDir,
			AudioFormat:          audioFormat,
			FilenameFormat:       filenameFormat,
			Duration:             durationSeconds,
			ItemID:               itemID,
			SpotifyTrackNumber:   track.TrackNumber,
			SpotifyDiscNumber:    track.DiscNumber,
			SpotifyTotalTracks:   track.TotalTracks,
			SpotifyTotalDiscs:    track.TotalDiscs,
			ISRC:                 strings.TrimSpace(job.Req.ISRC),
			Copyright:            track.Copyright,
			Publisher:            track.Publisher,
			Composer:             track.Composer,
			UPC:                  track.UPC,
			EmbedLyrics:          apiSettingBool(settings, "embedLyrics"),
			EmbedMaxQualityCover: apiSettingBool(settings, "embedMaxQualityCover"),
			UseFirstArtistOnly:   apiSettingBool(settings, "useFirstArtistOnly"),
			UseSingleGenre:       apiSettingBool(settings, "useSingleGenre"),
			EmbedGenre:           apiSettingBool(settings, "embedGenre"),
			SaveCover:            apiSettingBool(settings, "saveCover"),
			TidalAPIURL:          customTidalAPI,
			QobuzAPIURL:          customQobuzAPI,
		}

		resp, err := s.app.DownloadTrack(req)
		if resp.Success {
			s.mu.Lock()
			job.Status = apiJobDone
			job.FilePath = resp.File
			job.Source = service
			if resp.SourceLabel != "" {
				job.Source = fmt.Sprintf("%s (%s)", service, resp.SourceLabel)
			}
			s.mu.Unlock()
			fmt.Printf("[API] Job %s done via %s: %s\n", job.ID, service, resp.File)
			return
		}
		msg := resp.Error
		if msg == "" && err != nil {
			msg = err.Error()
		}
		if msg == "" {
			msg = "download failed"
		}
		errorsSeen = append(errorsSeen, fmt.Sprintf("[%s] %s", service, msg))
		if resp.Cancelled {
			break
		}
	}

	s.failJob(job, strings.Join(errorsSeen, " | "))
}
