# ytserver — self-hosted YouTube download relay

A small HTTP server that lets SpotiFLAC download tracks without any paid
streaming account. It sources audio from YouTube instead of Tidal/Qobuz/Amazon,
so it does not depend on the community relay servers (which rate-limit and go on
cooldown).

It speaks the same contract the desktop client already uses for a **custom
Qobuz instance**, so the client needs no rebuild beyond allowing an `http://`
instance URL: just paste this server's URL into Settings.

## Important: this is not true lossless

YouTube audio is lossy (Opus, ~128–256 kbps). This server transcodes it into a
16-bit FLAC container and tags it honestly:

- `SOURCE=YouTube`
- `comment=Source: YouTube (lossy transcode via SpotiFLAC ytserver) - not true lossless`

The output is a valid `.flac` that tags and plays correctly, but the audio is
YouTube quality, not lossless. It is a free, no-account download, not a quality
win.

## Requirements

- `yt-dlp` on PATH (or pass `-yt-dlp /path/to/yt-dlp`)
- `ffmpeg` on PATH (or pass `-ffmpeg /path/to/ffmpeg`)

## Run

```sh
go run ./cmd/ytserver            # listens on 127.0.0.1:8790
# or build:
go build -o build/bin/ytserver ./cmd/ytserver
./build/bin/ytserver -addr 127.0.0.1:8790
```

Flags:

| flag | default | meaning |
|---|---|---|
| `-addr` | `127.0.0.1:8790` | listen address |
| `-public-url` | (request host) | public base URL used to build file links (set this behind a reverse proxy) |
| `-yt-dlp` | `yt-dlp` | path to yt-dlp |
| `-ffmpeg` | `ffmpeg` | path to ffmpeg |
| `-key` | (random per run) | HMAC key for signing file tokens. Left empty, a random ephemeral key is generated at startup (tokens then last one process lifetime). Set a persistent random value in production. |
| `-search-count` | `6` | YouTube candidates to consider per track (applies to both searches) |
| `-duration-tolerance` | `12` | max seconds a candidate may differ from the track |
| `-music-search` | `true` | also search YouTube Music songs and prefer those hits |
| `-music-probe` | `2` | how many YouTube Music hits missing from the regular search get a metadata probe (about 4s each, run in parallel) |

## Use it from the app

The desktop app now starts this server itself. If the Qobuz custom instance is a
`127.0.0.1` or `localhost` URL and nothing answers there, the app launches the
`ytserver` binary from its bundle (or PATH) and logs to `ytserver.log` next to
`config.json`.

1. Start the server.
2. In SpotiFLAC → Settings → Qobuz custom instance, enter the server URL
   (e.g. `http://127.0.0.1:8790`) and click **Check** — it should report online.
3. Download a track with the Qobuz source selected.

## How it works

1. `GET /api/download-music?track_id=<id>&quality=<code>` — resolves the Qobuz
   track ID to title/artist/duration via Qobuz's public search API (no account),
   searches YouTube, picks the closest-duration official-audio match, and returns
   `{"success":true,"data":{"url":"<server>/file/<token>"}}`.
2. `GET /file/<token>` — yt-dlp downloads the matched audio, ffmpeg transcodes it
   to a 16-bit FLAC, and the server streams the finished file.

The file token is an HMAC-signed, stateless job description, so no server-side
storage is needed between the two calls.

## Notes / limitations

- Resolve takes ~15–20s per track because it fully extracts candidate durations
  to avoid grabbing the wrong (extended / sped-up / live) version.
- Matching prefers YouTube Music song hits (catalog audio, not fan uploads),
  then "Topic" / "official audio" uploads, and rejects candidates whose length
  differs from the track by more than the duration tolerance. The YouTube
  Music search runs in parallel with the regular one; its flat results carry
  no duration, so the top hits the regular search missed get a metadata probe
  before the duration gate. Turn it off with `-music-search=false`.
- For remote/HTTPS hosting, put it behind a reverse proxy and set `-public-url`.
