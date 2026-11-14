# CLAUDE.md

Guidance for coding agents working in this repository.

## Project

TeleVideoConverter: a Telegram bot that downloads videos with yt-dlp, makes
them play on every Telegram client, and keeps them for a few days behind a web
page. One Go binary (`cmd/tvc`) runs the bot, the download worker, the web UI
and cleanup. The Python version is under the git tag `python-final`.

## Hard rules

- The image must stay **rootless, distroless, shell-less, `FROM scratch`**:
  only the static `tvc`, static `ffmpeg`/`ffprobe`, static `qjs`, and the
  yt-dlp onedir bundle (which needs `/lib/ld-musl-*`, the `libc.musl-*`
  symlink and `/lib/libz.so.1`). No Alpine/Debian runtime base, no shell.
- `CGO_ENABLED=0`. SQLite is `modernc.org/sqlite`.
- Never log or print the bot token. `internal/tg` scrubs it from errors.
- Keep the SQLite schema compatible with the Python version (only add
  columns/tables).

## Commands

```bash
docker build -t televideoconverter:latest .        # full image (ffmpeg stage is cached)
docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine go test ./...
docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine go vet ./...
docker compose up -d                               # local run with ./storage ./db ./cookies
docker exec televideoconverter tvc doctor          # tool versions
docker exec televideoconverter tvc prepare FILE    # dry-run Telegram compression
```

Go is not installed on the host; use the golang container.

## Flow

1. `internal/bot` long-polls Telegram, checks the whitelist and the rate limit,
   sends a status message and inserts a row into `jobs` (SQLite queue).
2. `bot.Worker` claims jobs one at a time; `fetch.Fetcher` runs yt-dlp
   (`--progress-template`, `-O after_move:` JSON) or a site workaround
   (Instagram carousel via `-J` + `--load-info-json`, Threads web API,
   TikTok slideshow / embed fallback).
3. `media.Normalize`: H.264 8-bit 4:2:0 with even dimensions is **remuxed**
   (`-c copy +faststart`), anything else is re-encoded once (libx264
   `H264_PROFILE`, veryfast, CRF 23). Thumbnails are 320px JPEGs.
4. The row is saved to `videos` (with `download_seconds`/`process_seconds`/
   `send_seconds`), then stats, video (compressed/split by
   `media.PrepareForTelegram` above 48 MB) and description are sent.
5. `bot.Tracker` animates the status message every 3 s; its per-job mutex
   guarantees the final text is never overwritten.
6. Jobs interrupted by shutdown are requeued and resume on start.
7. `internal/cleanup` hourly: expired rows, storage cap, unreferenced files
   older than the retention period.

## Deployment (Greg's Mac, OrbStack)

- Compose: `/Volumes/TimeMachine/OrbStack/compose/televideoconverter/compose.yaml`
  with secrets in `tvc.env` (same folder, never commit). `compose.json` there
  is the old Python stack.
- Data: `/Volumes/TimeMachine/OrbStack/appdata/Users/greg/Documents/Programming/TeleVideoConverter/{storage,db,cookies}`.
  The `storage/db/cookies` folders inside this repo are **not** the live data.
- `televideo.cnr.pw` → Nginx Proxy Manager → `televideoconverter-web-frontend-1:3000`
  (a network alias of the `televideoconverter` container on network `common`).
- Update: `docker build -t televideoconverter:latest <repo>` then
  `docker compose up -d` in the compose folder.
