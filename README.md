# TeleVideoConverter

A self-hosted Telegram bot that downloads videos from YouTube, Instagram, TikTok,
X/Twitter, Threads, Vimeo and everything else [yt-dlp](https://github.com/yt-dlp/yt-dlp)
supports, sends them back as files that play on every Telegram client, and keeps
them for a few days behind a small web page.

Since November 2026 it is a single Go binary in one **rootless, shell-less image
built `FROM scratch`**. The previous Python version (6 containers, Redis, React)
is preserved under the git tag `python-final`.

## Why the rewrite: benefits

Measured on the same Mac (OrbStack) with the same links, Python version vs. Go version:

| | Python (6 containers) | Go (1 container) |
|---|---|---|
| Images on disk | ~2.6 GB | ~213 MB (≈12× smaller) |
| Memory when idle | ~180 MB | ~8 MB (≈20× less) |
| Services to run | bot, downloader, web API, React/nginx, cleanup, Redis | one binary |
| Short X clip (2.6 MB) | 4 s | 2.3 s |
| X video (42.7 MB) | 13 s | 4.7 s |
| Instagram reel (VP9) | 10 s | 7.7 s |
| 10-min 1080p video → Telegram copy | minutes (full encode + 2-pass) | 33 s (single pass) |

- **Safer image.** `FROM scratch`, no shell, no package manager, non-root UID,
  read-only root filesystem, no Linux capabilities. Only the binary and the
  tools it runs are inside.
- **Fewer moving parts.** No Redis, no nginx, no Node build, no shared-SQLite
  race between four processes. One process owns the database.
- **Nothing gets lost.** Jobs live in a SQLite queue: a restart or crash
  resumes interrupted downloads instead of dropping them.
- **Faster delivery to Telegram.** Videos are encoded once to constant frame
  rate H.264, which Telegram accepts instantly (remuxed X/Threads clips made
  it think for 15 s to 2 min). A video requested again is re-sent by
  `file_id` without uploading.
- **Smarter large files.** Over 50 MB: one-pass compression on a 720p→360p
  ladder, split into parts only when the video is too long.
- **Resilient networking.** Separate connections for polling, calls and
  uploads; retries with backoff on 429/5xx; dead connections are dropped
  after network errors; the status animation backs off when Telegram is slow.
- **Observable.** Per-video download/processing/sending times in the
  database; slow Telegram calls are logged with upload vs. server time;
  the bot token never appears in logs.
- **Cleaner storage.** Leftover files no database row points at are removed
  after the retention period (the old cleanup left them forever).
- **More fun.** A much larger pelmeni-obsessed status vocabulary for every
  stage of a download, with a dumpling progress bar — and a short,
  joke-free card with a link to the original right before each video.

## What it does

- Send a link to the bot. It edits one status message with progress
  (the "pelmeni" status), then sends the file stats, the video, and the post
  description (toggle with `/description`).
- Videos up to 48 MB are encoded once to constant-frame-rate H.264
  (veryfast, CRF 23) so Telegram accepts them instantly; larger H.264 files
  are only remuxed for storage and get a compressed copy for Telegram.
- Files over Telegram's 50 MB bot limit get a compressed copy (single pass,
  720p→360p ladder) and are split into parts when the video is long.
- If an upload fails, the bot links to the web page instead.
- Videos are deleted after `RETENTION_DAYS` (default 3) or when storage exceeds
  `MAX_STORAGE_GB` (default 5). Unreferenced leftover files are cleaned too.
- Site workarounds: Instagram carousels (first video), Threads (web API),
  TikTok photo posts (rendered slideshow) and TikTok login walls (embed fallback).
- Interrupted downloads (restart, crash) stay in a SQLite queue and resume.

Bot commands: `/start`, `/list`, `/search <keyword>`, `/stats`, `/description`.

## Image

| part | size | notes |
|---|---|---|
| `tvc` | ~14 MB | Go, `CGO_ENABLED=0`, pure-Go SQLite |
| `ffmpeg` + `ffprobe` | ~31 MB | built in the Dockerfile, fully static, only the codecs used |
| `qjs` | ~2.5 MB | QuickJS-ng, static; solves YouTube's JS challenges for yt-dlp |
| `yt-dlp` | ~94 MB | official musl onedir build (bundled CPython + curl_cffi), plus the musl loader and libz it needs |

The final stage is `FROM scratch`: no shell, no package manager, runs as UID
65532, works with a read-only root filesystem and no capabilities. The health
check is `tvc healthcheck`.

## Run

```bash
cp .env.example .env   # set TELEGRAM_BOT_TOKEN and TELEGRAM_USER_IDS
docker compose up -d --build
```

The web page is on `http://127.0.0.1:3000`. The compose file attaches the
container to the external `common` network with the alias
`televideoconverter-web-frontend-1`, which the reverse proxy uses.

Useful commands (there is no shell inside the container):

```bash
docker exec televideoconverter tvc doctor
docker exec televideoconverter tvc prepare /storage/videos/ID.mp4
docker logs -f televideoconverter
```

`doctor` prints the bundled tool versions; `prepare` dry-runs the Telegram
compression/splitting for a file and leaves the result in `/tmp`.

## Configuration

| variable | default | |
|---|---|---|
| `TELEGRAM_BOT_TOKEN` | — | required |
| `TELEGRAM_USER_IDS` | — | comma-separated allowed user IDs |
| `STORAGE_PATH` / `DATABASE_PATH` / `COOKIES_PATH` | `/storage`, `/db/televideo.db`, `/cookies` | |
| `RETENTION_DAYS` | `3` | |
| `MAX_STORAGE_GB` | `5` | |
| `SEND_POST_DESCRIPTION` | `true` | default for users who never used `/description` |
| `TELEGRAM_SEND_LIMIT_MB` / `TELEGRAM_TARGET_MB` | `50` / `48` | |
| `TELEGRAM_MIN_VIDEO_BITRATE_K` / `TELEGRAM_AUDIO_BITRATE_K` | `250` / `64` | below the minimum, long videos are split |
| `TELEGRAM_LARGE_FILE_POLICY` | `compress_then_split` | anything else refuses to split |
| `H264_PROFILE` | `high` | profile for re-encodes; `baseline` if an old device cannot play them |
| `WEB_PUBLIC_URL` | `https://televideo.cnr.pw` | link sent when an upload fails |
| `WEB_PASSWORD` | empty | if set, the web page and API require HTTP basic auth |
| `MAX_DOWNLOADS_PER_HOUR` | `10` | per user |
| `JOB_TIMEOUT_MINUTES` | `45` | |
| `LOG_LEVEL` | `INFO` | |

Cookies for private or login-walled content go in `cookies/` as Netscape files:
`instagram.txt`, `tiktok.txt`, `twitter.txt`, `facebook.txt` (see `cookies/README.md`).

## Web API

The same paths the old React frontend used: `GET /api/videos`, `GET /api/videos/{id}`,
`GET /api/videos/{id}/download` (supports Range), `DELETE /api/videos/{id}`,
`GET /api/stats`, `GET /api/search?q=`, `GET /api/thumbnails/{id}.jpg`,
`POST /api/submit-url` (`{"url": "...", "quality": "best"|"audio"}`), and `GET /healthz`.

## Layout

```
cmd/tvc/            entry point: serve, healthcheck, doctor, prepare
internal/config     environment variables
internal/store      SQLite (Python-era schema + jobs queue + stage timings)
internal/tg         minimal Telegram Bot API client (streaming uploads, retries, no token in errors)
internal/fetch      yt-dlp runner and site workarounds
internal/media      ffprobe/ffmpeg: remux vs encode, thumbnails, Telegram size limits
internal/bot        commands, queue worker, animated status messages
internal/web        HTML page + JSON API
internal/cleanup    retention, storage cap, orphan files
```

Tests: `docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine go test ./...`

## Updating yt-dlp

Sites change and yt-dlp follows. Bump `YTDLP_VERSION` in the `Dockerfile` and
rebuild; the build verifies the release checksum.

## License

MIT · Gregory 'Cinereo' Smirnov
