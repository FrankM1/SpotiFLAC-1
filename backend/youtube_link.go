package backend

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// YouTubeLinkFilename builds the file name for an exact-video download: the
// user's template output plus a " [yt-<id>]" suffix, so it never collides with
// an earlier download of the same song from another source.
func YouTubeLinkFilename(title, artist, filenameFormat, videoID string) string {
	name := buildQobuzFilename(sanitizeFilename(title), sanitizeFilename(artist), "", "", "", 0, 0, filenameFormat, false, 0, false)
	ext := filepath.Ext(name)
	return strings.TrimSuffix(name, ext) + " [yt-" + videoID + "]" + ext
}

// DownloadYouTubeVideo fetches the audio of one exact YouTube video through the
// relay at relayURL (cmd/ytserver), saves it as FLAC in outputDir and tags it
// with the caller's title and artist. The result is prefixed with "EXISTS:"
// when a file for this video is already there.
func (q *QobuzDownloader) DownloadYouTubeVideo(relayURL, videoID, outputDir, filenameFormat, title, artist string) (string, error) {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output directory: %w", err)
	}

	path := filepath.Join(outputDir, YouTubeLinkFilename(title, artist, filenameFormat, videoID))
	if info, err := os.Stat(path); err == nil && info.Size() > 0 {
		return "EXISTS:" + path, nil
	}

	endpoint := fmt.Sprintf("%s/api/download-youtube?v=%s&title=%s&artist=%s",
		strings.TrimRight(relayURL, "/"), url.QueryEscape(videoID), url.QueryEscape(title), url.QueryEscape(artist))
	fileURL, err := q.requestCustomFileURL(endpoint)
	if err != nil {
		return "", err
	}

	SetDownloadStatus("Fetching audio & transcoding…")
	if err := q.DownloadFile(fileURL, path); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("failed to download file: %w", err)
	}

	watchURL := "https://www.youtube.com/watch?v=" + videoID
	metadata := Metadata{
		Title:       title,
		Artist:      artist,
		URL:         watchURL,
		Comment:     watchURL,
		Source:      "YouTube",
		Description: "Source: YouTube (lossy transcode) - not true lossless",
	}
	if err := EmbedMetadata(path, metadata, ""); err != nil {
		os.Remove(path)
		return "", fmt.Errorf("failed to embed metadata: %w", err)
	}
	return path, nil
}
