// Command ytserver is a self-hosted download relay for SpotiFLAC that sources
// audio from YouTube instead of a paid streaming provider. It speaks the same
// HTTP contract the desktop client already uses for a "custom Qobuz instance"
// (GET /api/download-music?track_id=&quality=  ->  {success, data:{url}}), so
// the client needs no changes: point its custom Qobuz URL at this server.
//
// IMPORTANT: YouTube audio is lossy. This server transcodes it into a FLAC
// container and tags the result honestly (SOURCE / COMMENT note that the audio
// originated from YouTube and is not true lossless).
package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spotbye/SpotiFLAC/backend"
)

var (
	listenAddr = flag.String("addr", "127.0.0.1:8790", "listen address")
	publicURL  = flag.String("public-url", "", "public base URL used to build file links (defaults to the request host)")
	ytdlpPath  = flag.String("yt-dlp", "yt-dlp", "path to yt-dlp binary")
	ffmpegPath = flag.String("ffmpeg", "ffmpeg", "path to ffmpeg binary")
	signingKey = flag.String("key", "", "HMAC key for signing file tokens (random ephemeral key generated if empty; set a persistent value in production)")
	searchN    = flag.Int("search-count", 6, "number of YouTube candidates to consider")
	durTolSec  = flag.Int("duration-tolerance", 12, "max seconds a candidate duration may differ from the track")
	appleMatch = flag.Bool("apple-match", true, "use Apple Music (iTunes Search API) to confirm/fill canonical duration for sharper YouTube matching")
	appleStore = flag.String("apple-store", "", "iTunes storefront country code for lookups (e.g. ZA, US); empty = Apple default")

	dlAttempts    = flag.Int("download-attempts", 6, "max download attempts per track before giving up (transient 403s usually clear within a couple)")
	dlBackoffBase = flag.Duration("download-backoff", 2*time.Second, "base delay for exponential backoff between download attempts")
	dlBackoffMax  = flag.Duration("download-backoff-max", 30*time.Second, "cap on the backoff delay between download attempts")
)

// filePayload is the stateless, HMAC-signed job description carried in the file
// URL. It lets /file resolve a request without any server-side storage.
type filePayload struct {
	Video    string `json:"v"`
	Title    string `json:"t"`
	Artist   string `json:"a"`
	Album    string `json:"al"`
	Duration int    `json:"d"`
}

func main() {
	flag.Parse()

	// A guessable signing key lets anyone forge /file tokens and use the server
	// as an open YouTube-transcode proxy. Generate a random ephemeral key when
	// the operator hasn't supplied one; tokens then only survive one process
	// lifetime, which is fine for a single-user local relay.
	if strings.TrimSpace(*signingKey) == "" {
		b := make([]byte, 32)
		if _, err := rand.Read(b); err != nil {
			log.Fatalf("failed to generate signing key: %v", err)
		}
		k := base64.RawURLEncoding.EncodeToString(b)
		signingKey = &k
		log.Printf("no -key set: generated an ephemeral signing key (set -key to a persistent random value in production)")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", handleHealth)
	mux.HandleFunc("/status", handleHealth)
	mux.HandleFunc("/api/download-music", handleResolve)
	mux.HandleFunc("/api/download-youtube", handleResolveVideo)
	mux.HandleFunc("/file/", handleFile)
	// Root returns 200 so the app's API-status probe (which GETs the base URL)
	// reports this local instance as online.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"service": "spotiflac-ytserver", "status": "ok"})
	})

	log.Printf("ytserver listening on http://%s (yt-dlp=%s ffmpeg=%s)", *listenAddr, *ytdlpPath, *ffmpegPath)
	log.Printf("point the client's custom Qobuz instance at this server's public URL")
	if err := http.ListenAndServe(*listenAddr, logging(mux)); err != nil {
		log.Fatal(err)
	}
}

func handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleResolve implements GET /api/download-music?track_id=&quality=.
// It returns {success:true, data:{url}} where url points at /file/<token>.
func handleResolve(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	trackID := strings.TrimSpace(q.Get("track_id"))

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	// Preferred path: the client passes Spotify title/artist directly, so we
	// resolve straight from that metadata and never touch the Qobuz catalog.
	// This is what makes tracks that aren't on Qobuz (niche/regional) work.
	title := strings.TrimSpace(q.Get("title"))
	artist := strings.TrimSpace(q.Get("artist"))
	wantDur, _ := strconv.Atoi(strings.TrimSpace(q.Get("duration")))

	if title == "" || artist == "" {
		// Fallback: only a Qobuz track_id was given; look up its metadata.
		if trackID == "" {
			writeErr(w, http.StatusBadRequest, "provide title+artist, or a track_id")
			return
		}
		track, err := backend.GetQobuzTrackMetadata(trackID)
		if err != nil {
			writeErr(w, http.StatusBadGateway, fmt.Sprintf("metadata lookup failed: %v", err))
			return
		}
		title = strings.TrimSpace(track.Title)
		artist = backend.QobuzTrackDisplayArtist(track)
		if wantDur == 0 {
			wantDur = track.Duration
		}
	}
	if title == "" || artist == "" {
		writeErr(w, http.StatusBadGateway, "track metadata incomplete")
		return
	}

	// Apple Music matching aid: fill/confirm the duration so the YouTube match is
	// sharper (especially for niche tracks). Audio still comes from YouTube.
	if ap, ok := appleMusicLookup(ctx, artist, title); ok {
		if wantDur == 0 {
			wantDur = ap.DurationSec
			log.Printf("apple: filled duration %ds for %q - %q", wantDur, artist, title)
		} else if abs(ap.DurationSec-wantDur) > *durTolSec {
			// Apple disagrees strongly with the client duration; trust Apple's
			// canonical length so we don't reject the right YouTube upload.
			log.Printf("apple: duration %ds overrides client %ds for %q - %q", ap.DurationSec, wantDur, artist, title)
			wantDur = ap.DurationSec
		}
	}

	cand, err := findYouTubeMatch(ctx, artist, title, wantDur)
	if err != nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no youtube match: %v", err))
		return
	}

	payload := filePayload{
		Video:    cand.ID,
		Title:    title,
		Artist:   artist,
		Album:    strings.TrimSpace(q.Get("album")),
		Duration: wantDur,
	}
	token := signToken(payload)
	fileURL := baseURL(r) + "/file/" + token

	src := "meta"
	if trackID != "" && q.Get("title") == "" {
		src = "qobuz:" + trackID
	}
	log.Printf("resolved [%s] %q - %q -> yt:%s (%ds vs %ds)", src, artist, title, cand.ID, cand.Duration, wantDur)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"url": fileURL},
	})
}

// youtubeIDPattern matches a YouTube video id.
var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// handleResolveVideo implements GET /api/download-youtube?v=&title=&artist=.
// The caller names the exact video, so there is no search: it returns the same
// {success:true, data:{url}} shape as /api/download-music for that video.
func handleResolveVideo(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	videoID := strings.TrimSpace(q.Get("v"))
	title := strings.TrimSpace(q.Get("title"))
	artist := strings.TrimSpace(q.Get("artist"))
	if !youtubeIDPattern.MatchString(videoID) {
		writeErr(w, http.StatusBadRequest, "v must be an 11-character YouTube video id")
		return
	}
	if title == "" || artist == "" {
		writeErr(w, http.StatusBadRequest, "provide title and artist")
		return
	}

	token := signToken(filePayload{Video: videoID, Title: title, Artist: artist})
	log.Printf("resolved [link] %q - %q -> yt:%s", artist, title, videoID)
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"data":    map[string]any{"url": baseURL(r) + "/file/" + token},
	})
}

// handleFile streams a transcoded, honestly-labeled FLAC for a signed token.
func handleFile(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/file/")
	payload, ok := verifyToken(token)
	if !ok {
		writeErr(w, http.StatusForbidden, "invalid or expired token")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "ytserver-")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer os.RemoveAll(tmpDir)
	flacPath := tmpDir + "/out.flac"

	comment := "Source: YouTube (lossy transcode via SpotiFLAC ytserver) - not true lossless"

	// Retry the fetch+transcode with bounded exponential backoff. YouTube 403s
	// are transient — a fresh yt-dlp run (new extraction/URLs) usually succeeds,
	// which is why manually retrying "gets all the songs". Bounded (not infinite)
	// so a genuinely dead/region-locked video eventually gives up instead of
	// hanging forever. Each request runs in its own goroutine, so a retrying
	// track never blocks other concurrent downloads.
	var lastErr error
	for attempt := 1; attempt <= *dlAttempts; attempt++ {
		lastErr = fetchAndTranscode(ctx, payload, tmpDir, flacPath, comment)
		if lastErr == nil {
			break
		}
		if ctx.Err() != nil {
			lastErr = ctx.Err()
			break
		}
		if attempt < *dlAttempts {
			d := backoffDelay(attempt)
			log.Printf("yt:%s download attempt %d/%d failed: %v — retrying in %s", payload.Video, attempt, *dlAttempts, lastErr, d)
			t := time.NewTimer(d)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				lastErr = ctx.Err()
			}
		}
	}
	if lastErr != nil {
		writeErr(w, http.StatusBadGateway, fmt.Sprintf("download failed after %d attempts: %v", *dlAttempts, lastErr))
		return
	}

	f, err := os.Open(flacPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Content-Type", "audio/flac")
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", sanitize(payload.Artist+" - "+payload.Title)+".flac"))
	if _, err := io.Copy(w, f); err != nil {
		log.Printf("stream error for yt:%s: %v", payload.Video, err)
	}
}

// fetchAndTranscode downloads the best audio for one video to a temp file and
// transcodes it to a 16-bit FLAC at flacPath. Downloading to a file (not a
// pipe) lets yt-dlp retry/resume and avoids the Opus-header errors piping
// caused. Any partial from a prior attempt is cleared first.
func fetchAndTranscode(ctx context.Context, payload filePayload, tmpDir, flacPath, comment string) error {
	if prev, _ := filepath.Glob(tmpDir + "/src.*"); len(prev) > 0 {
		for _, f := range prev {
			os.Remove(f)
		}
	}

	srcTmpl := tmpDir + "/src.%(ext)s"
	yt := exec.CommandContext(ctx, *ytdlpPath,
		"-f", "bestaudio/best",
		"--no-playlist", "--no-warnings", "--quiet",
		"--retries", "10", "--fragment-retries", "10",
		"-o", srcTmpl,
		"https://www.youtube.com/watch?v="+payload.Video,
	)
	yt.Stderr = os.Stderr
	if err := yt.Run(); err != nil {
		return fmt.Errorf("yt-dlp: %w", err)
	}

	matches, _ := filepath.Glob(tmpDir + "/src.*")
	if len(matches) == 0 {
		return fmt.Errorf("yt-dlp produced no output file")
	}

	ff := exec.CommandContext(ctx, *ffmpegPath,
		"-hide_banner", "-loglevel", "error",
		"-i", matches[0],
		"-vn",
		"-c:a", "flac",
		"-sample_fmt", "s16",
		"-metadata", "title="+payload.Title,
		"-metadata", "artist="+payload.Artist,
		"-metadata", "album="+payload.Album,
		"-metadata", "SOURCE=YouTube",
		"-metadata", "comment="+comment,
		"-y", flacPath,
	)
	ff.Stderr = os.Stderr
	if err := ff.Run(); err != nil {
		return fmt.Errorf("transcode: %w", err)
	}
	return nil
}

// backoffDelay returns base * 2^(attempt-1), capped at the configured max.
func backoffDelay(attempt int) time.Duration {
	d := *dlBackoffBase << (attempt - 1)
	if d <= 0 || d > *dlBackoffMax {
		return *dlBackoffMax
	}
	return d
}

// ---- Apple Music (iTunes Search API) matching aid ----

type appleTrack struct {
	Artist      string
	Title       string
	Album       string
	DurationSec int
}

// appleMusicLookup queries Apple's public iTunes Search API (no account, no DRM)
// for a canonical match. It's used only to confirm/fill the track duration so
// the YouTube match is sharper — the audio still comes from YouTube.
func appleMusicLookup(ctx context.Context, artist, title string) (*appleTrack, bool) {
	if !*appleMatch {
		return nil, false
	}
	endpoint := "https://itunes.apple.com/search?media=music&entity=song&limit=8&term=" +
		url.QueryEscape(artist+" "+title)
	if s := strings.TrimSpace(*appleStore); s != "" {
		endpoint += "&country=" + url.QueryEscape(s)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "SpotiFLAC-ytserver")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		log.Printf("apple: request error: %v", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("apple: status %d", resp.StatusCode)
		return nil, false
	}
	var data struct {
		Results []struct {
			ArtistName      string `json:"artistName"`
			TrackName       string `json:"trackName"`
			CollectionName  string `json:"collectionName"`
			TrackTimeMillis int    `json:"trackTimeMillis"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil || len(data.Results) == 0 {
		return nil, false
	}
	lt := strings.ToLower(title)
	// Prefer a result whose title overlaps the requested title; else take the top.
	pick := data.Results[0]
	for _, r := range data.Results {
		rt := strings.ToLower(r.TrackName)
		if rt == lt || strings.Contains(rt, lt) || strings.Contains(lt, rt) {
			pick = r
			break
		}
	}
	return &appleTrack{
		Artist:      pick.ArtistName,
		Title:       pick.TrackName,
		Album:       pick.CollectionName,
		DurationSec: pick.TrackTimeMillis / 1000,
	}, pick.TrackTimeMillis > 0
}

// ---- YouTube search / matching ----

type ytCandidate struct {
	ID       string
	Title    string
	Channel  string
	Duration int
}

// findYouTubeMatch runs a fast flat search (which, via -J, still carries
// per-result duration) and picks the candidate whose duration is closest to the
// track, preferring official/"Topic" audio uploads and rejecting obviously-wrong
// lengths. Flat search avoids fully extracting every candidate, cutting resolve
// time from ~15-20s to ~2s.
//
// Note: `--flat-playlist --print "%(duration)s"` yields empty durations, but the
// `--flat-playlist -J` JSON dump does populate `duration` — hence -J here.
func findYouTubeMatch(ctx context.Context, artist, title string, wantDur int) (*ytCandidate, error) {
	query := fmt.Sprintf("ytsearch%d:%s %s", *searchN, artist, title)
	cmd := exec.CommandContext(ctx, *ytdlpPath,
		"--flat-playlist", "-J", "--no-warnings", "--quiet",
		query,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("yt-dlp search: %w", err)
	}

	var res struct {
		Entries []struct {
			ID       string  `json:"id"`
			Title    string  `json:"title"`
			Channel  string  `json:"channel"`
			Uploader string  `json:"uploader"`
			Duration float64 `json:"duration"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return nil, fmt.Errorf("parse search results: %w", err)
	}

	// Pick the best candidate whose duration is within tolerance. Separately
	// track the best "official audio" (Topic / official-audio) candidate so that,
	// when nothing passes the duration filter (common for niche tracks where the
	// only upload is an official one with a slightly different length), we still
	// return the authoritative upload instead of failing.
	var best, bestOfficial *ytCandidate
	bestScore := 1 << 30
	bestOfficialDiff := 1 << 30
	for _, e := range res.Entries {
		if e.ID == "" {
			continue
		}
		c := ytCandidate{ID: e.ID, Duration: int(e.Duration), Channel: firstNonEmpty(e.Channel, e.Uploader), Title: e.Title}
		lc := strings.ToLower(c.Channel + " " + c.Title)
		official := strings.Contains(lc, "topic") || strings.Contains(lc, "official audio")
		durDiff := abs(c.Duration - wantDur)

		if official {
			if durDiff < bestOfficialDiff {
				bestOfficialDiff = durDiff
				cc := c
				bestOfficial = &cc
			}
		}

		if wantDur > 0 && c.Duration > 0 && durDiff > *durTolSec {
			continue // wrong length -> almost certainly the wrong track
		}
		score := durDiff
		if official {
			score -= 100 // strongly prefer official audio uploads
		}
		if strings.Contains(lc, "live") || strings.Contains(lc, "cover") || strings.Contains(lc, "remix") || strings.Contains(lc, "sped up") {
			score += 50
		}
		if score < bestScore {
			bestScore = score
			cc := c
			best = &cc
		}
	}
	if best != nil {
		return best, nil
	}
	if bestOfficial != nil {
		log.Printf("no candidate within %ds of %ds; falling back to official upload yt:%s (%ds)", *durTolSec, wantDur, bestOfficial.ID, bestOfficial.Duration)
		return bestOfficial, nil
	}
	return nil, fmt.Errorf("no candidate within %ds of %ds", *durTolSec, wantDur)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ---- token signing (stateless file URLs) ----

func signToken(p filePayload) string {
	raw, _ := json.Marshal(p)
	body := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmacHex(body)
	return body + "." + mac
}

func verifyToken(token string) (filePayload, bool) {
	var p filePayload
	dot := strings.LastIndexByte(token, '.')
	if dot <= 0 {
		return p, false
	}
	body, mac := token[:dot], token[dot+1:]
	if !hmac.Equal([]byte(mac), []byte(hmacHex(body))) {
		return p, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return p, false
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.Video == "" {
		return p, false
	}
	return p, true
}

func hmacHex(body string) string {
	h := hmac.New(sha256.New, []byte(*signingKey))
	h.Write([]byte(body))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// ---- helpers ----

func baseURL(r *http.Request) string {
	if strings.TrimSpace(*publicURL) != "" {
		return strings.TrimRight(*publicURL, "/")
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"success": false, "error": msg})
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func sanitize(s string) string {
	repl := strings.NewReplacer("/", "-", "\\", "-", ":", "-", "*", "", "?", "", "\"", "", "<", "", ">", "", "|", "")
	return strings.TrimSpace(repl.Replace(s))
}
