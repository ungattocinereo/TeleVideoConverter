# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

TeleVideoConverter is a self-hosted Telegram bot system for downloading videos from various platforms (YouTube, Instagram, TikTok, Vimeo, etc.) with a web interface for file management. The system uses Docker Compose to orchestrate 6 microservices with a focus on universal video compatibility through H.264 Baseline profile re-encoding.

## Common Commands

### Development
```bash
# Start all services
docker-compose up -d

# View logs for all services
docker-compose logs -f

# View logs for specific service
docker-compose logs -f telegram-bot
docker-compose logs -f downloader
docker-compose logs -f web-api

# Rebuild and restart services
docker-compose down
docker-compose build
docker-compose up -d

# Stop all services
docker-compose down

# Clean up everything (including volumes)
docker-compose down -v
```

### Testing Individual Services
```bash
# Test Telegram bot locally
cd telegram-bot && python bot.py

# Test downloader locally
cd downloader && python worker.py

# Test web API locally
cd web-api && python api.py

# Test web frontend locally
cd web-frontend && npm install && npm run dev
```

## Architecture

### Service Communication Flow
1. **User → Telegram Bot**: User sends URL to bot
2. **Telegram Bot → Redis**: Bot pushes download task to queue (JSON serialized)
3. **Redis → Downloader**: Downloader picks up task with `brpop` (blocking)
4. **Downloader → yt-dlp**: Downloads video using yt-dlp with cookies
5. **Downloader → ffmpeg**: Force re-encodes all videos (H.264 Baseline)
6. **Downloader → SQLite**: Saves metadata to shared database
7. **Downloader → Telegram Bot API**: Sends file back to user via HTTP
8. **Web API ← SQLite**: Reads video data from shared database
9. **Web API → Web Frontend**: Pushes updates via WebSocket
10. **Web Frontend ← Web API**: Displays videos via REST API
11. **Cleanup Service ← SQLite**: Periodically checks for expired videos

**Critical Notes:**
- SQLite database is **shared** across all services via bind mount (`./db:/db`)
- Each service has its own `database.py` but all connect to same file
- Python services use `aiosqlite` for async database access
- No database locking issues as SQLite handles concurrent reads well

### Key Technologies
- **Backend**: Python 3.11 with asyncio
- **Frontend**: React 18 + TypeScript + Vite
- **UI Framework**: shadcn/ui with Tailwind CSS
- **Bot Library**: python-telegram-bot 20.7
- **Video Downloader**: yt-dlp
- **Database**: SQLite with aiosqlite
- **Message Queue**: Redis
- **Web Framework**: FastAPI with uvicorn
- **WebSocket**: Native WebSocket support in FastAPI

## Code Structure

### Telegram Bot (`telegram-bot/`)
- `bot.py` - Main bot logic with command handlers
- `database.py` - SQLite database operations (aiosqlite)
- `utils.py` - Helper functions (formatting, validation)

**Key Patterns:**
- All handlers check user permissions via `check_user_permission()` against `ALLOWED_USER_IDS` set
- Rate limiting tracked in-memory with `user_downloads` dict (10 downloads/hour)
- Download tasks serialized as JSON and pushed to Redis queue
- User settings (like post description toggle) stored in `user_settings` table
- Commands: `/start`, `/list`, `/search`, `/stats`, `/description`

### Downloader (`downloader/`)
- `worker.py` - Main worker loop consuming Redis queue
- `ytdlp_wrapper.py` - yt-dlp integration and video processing
- `database.py` - Database operations for saving video metadata

**Key Patterns:**
- Runs infinite loop with `brpop` on Redis queue (blocking)
- Format preference: `bestvideo[ext=mp4]+bestaudio[ext=m4a]/bestvideo+bestaudio/best`
- **Critical: All videos force re-encoded using `_reencode_video()` with:**
  - H.264 Baseline profile (level 3.0) for iOS/macOS compatibility
  - Keyframes every 50 frames (GOP 50) for proper seeking
  - AAC audio at 128k, 44.1kHz
  - `faststart` movflag for streaming
  - Even dimensions via `scale=trunc(iw/2)*2:trunc(ih/2)*2`
- Cookie-based auth for private content (Instagram, TikTok) via `_get_cookie_file()`
- Generates 320x180 JPEG thumbnails
- Post description extraction from yt-dlp metadata
- Sends up to three messages: (1) stats, (2) file, (3) description (if enabled)

### Web API (`web-api/`)
- `api.py` - FastAPI application with REST endpoints and WebSocket
- `database.py` - Database operations for web interface

**Key Patterns:**
- WebSocket connections stored in global `websocket_connections` list
- CORS enabled for localhost development
- File streaming via `FileResponse` for downloads
- Thumbnail serving with proper MIME types

### Web Frontend (`web-frontend/src/`)
- `App.tsx` - Main application component with state management
- `components/` - Reusable React components
  - `Header.tsx` - Header with stats
  - `VideoCard.tsx` - Individual video card in grid
  - `VideoGrid.tsx` - Grid layout for videos
  - `VideoModal.tsx` - Modal for detailed video view
- `hooks/useWebSocket.ts` - WebSocket connection management
- `types/index.ts` - TypeScript type definitions
- `lib/utils.ts` - Utility functions

**Key Patterns:**
- Dark theme by default (class="dark" on html element)
- Real-time updates via WebSocket
- Search filtering done client-side
- All API calls use `fetch()` with async/await

### Cleanup Service (`cleanup/`)
- `cleanup_cron.py` - Hourly cleanup job
- `database.py` - Database operations for cleanup

**Key Patterns:**
- Runs infinite loop with 1-hour sleep (3600 seconds)
- Two cleanup strategies:
  1. Time-based: Delete videos older than 3 days
  2. Size-based: Delete oldest videos if storage > 5GB

## Database Schema

### Videos Table
```sql
CREATE TABLE videos (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id TEXT UNIQUE NOT NULL,
    telegram_user_id INTEGER NOT NULL,
    original_url TEXT NOT NULL,
    title TEXT NOT NULL,
    original_quality TEXT,
    downloaded_quality TEXT NOT NULL,
    file_size INTEGER NOT NULL,
    processing_time INTEGER NOT NULL,
    format TEXT NOT NULL,
    codec TEXT,
    source_platform TEXT,
    file_path TEXT NOT NULL,
    thumbnail_path TEXT,
    download_timestamp INTEGER NOT NULL,
    delete_timestamp INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)
```

### Download Stats Table
```sql
CREATE TABLE download_stats (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    telegram_user_id INTEGER NOT NULL,
    video_id TEXT NOT NULL,
    action TEXT NOT NULL,  -- 'download' or 'delete'
    timestamp INTEGER NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
)
```

### User Settings Table
```sql
CREATE TABLE user_settings (
    telegram_user_id INTEGER PRIMARY KEY,
    send_description INTEGER DEFAULT 1,  -- 1=enabled, 0=disabled
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
)
```

**Important Indexes:**
- `idx_delete_timestamp` on `videos(delete_timestamp)` - for cleanup queries
- `idx_telegram_user_id` on `videos(telegram_user_id)` - for user-specific queries
- `idx_stats_timestamp` on `download_stats(timestamp)` - for stats queries

## Important Implementation Details

### File Storage
- Videos: `/storage/videos/{video_id}.{ext}` (always `.mp4` after re-encoding)
- Thumbnails: `/storage/thumbnails/{video_id}.jpg` (320x180 JPEG)
- Database: `/db/televideo.db` (SQLite, shared across services)
- Cookies: `/cookies/{platform}.txt` (Netscape format for yt-dlp)

### Cookie Authentication
Platform-specific cookie files in `/cookies/` directory:
- `instagram.txt` - Instagram authentication
- `tiktok.txt` - TikTok authentication
- `facebook.txt`, `twitter.txt` - Other platforms
- Auto-detected via `_get_cookie_file()` based on URL domain

### Video Re-encoding (Critical)
**All downloaded videos are force re-encoded** to fix compatibility issues:
- **Problem**: Instagram/TikTok use H.264 High Profile → frozen videos on iOS/macOS
- **Solution**: Re-encode to H.264 Baseline Profile (level 3.0)
- **Implementation**: `ytdlp_wrapper.py::_reencode_video()` using ffmpeg
- **Settings**: CRF 23, GOP 50, AAC 128k, faststart flag, even dimensions
- **Trade-off**: Adds 5-10 seconds processing time but guarantees universal playback

### Security
- Multiple Telegram user IDs allowed (comma-separated in `TELEGRAM_USER_IDS`)
- Rate limit: 10 downloads per hour per user (in-memory tracking)
- No authentication on web interface (assumes private network deployment)
- Storage limit enforced: 5GB maximum (configurable)

### Telegram File Size Limit
- Max 2GB per file
- Files >2GB: Bot sends link to web interface instead of file

### Auto-Deletion Logic
- Videos stored for exactly 3 days (259200 seconds)
- Cleanup service runs every hour
- Storage limit triggers immediate cleanup of oldest files

## Common Development Tasks

### Adding a New Telegram Command
1. Add handler in `telegram-bot/bot.py`:
```python
async def new_command(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
    user_id = update.effective_user.id
    if not self.check_user_permission(user_id):
        return
    # Implementation here

# Register in main()
application.add_handler(CommandHandler("newcommand", bot.new_command))
```

### Adding a New API Endpoint
1. Add route in `web-api/api.py`:
```python
@app.get("/api/newendpoint")
async def new_endpoint():
    try:
        # Implementation here
        return {"data": result}
    except Exception as e:
        logger.error(f"Error: {e}")
        raise HTTPException(status_code=500, detail=str(e))
```

### Adding a New React Component
1. Create file in `web-frontend/src/components/`
2. Import and use shadcn/ui components from `@/components/ui/`
3. Use TypeScript interfaces from `@/types/`
4. Apply Tailwind CSS classes for styling

### Debugging Download Issues
1. Check bot logs for URL validation: `docker-compose logs -f telegram-bot`
2. Check downloader logs for yt-dlp errors: `docker-compose logs -f downloader`
3. Test yt-dlp directly in container:
   ```bash
   docker-compose exec downloader yt-dlp --list-formats <URL>
   docker-compose exec downloader yt-dlp --cookies /cookies/instagram.txt <URL>
   ```
4. Verify ffmpeg is installed: `docker-compose exec downloader ffmpeg -version`
5. Check Redis queue:
   ```bash
   docker-compose exec redis redis-cli LLEN download_queue
   docker-compose exec redis redis-cli LRANGE download_queue 0 -1
   ```

### Debugging Re-encoding Issues
- Re-encoding failures fall back to original file (no crash)
- Check ffmpeg stderr in downloader logs
- Common issues: unsupported codecs, corrupted downloads
- Test ffmpeg manually:
  ```bash
  docker-compose exec downloader ffmpeg -i /storage/videos/test.mp4 -c:v libx264 -profile:v baseline test_out.mp4
  ```

### Debugging Cookie Authentication
1. Cookie files must be in Netscape format
2. Export from browser using extensions (e.g., "Get cookies.txt")
3. Place in `./cookies/{platform}.txt`
4. Verify file permissions: `chmod 644 cookies/*.txt`
5. Test with yt-dlp: `yt-dlp --cookies cookies/instagram.txt <URL>`

### Modifying Storage Limits
1. Update `MAX_STORAGE_GB` in `.env`
2. Update `RETENTION_DAYS` in `.env`
3. Restart services: `docker-compose restart`

## Environment Variables

All configuration in `.env` file at project root:

**Required:**
- `TELEGRAM_BOT_TOKEN` - Bot token from @BotFather
- `TELEGRAM_USER_IDS` - Comma-separated user IDs (e.g., "100269722,40882420")

**Paths:**
- `STORAGE_PATH=/storage`
- `DATABASE_PATH=/db/televideo.db`
- `COOKIES_PATH=/cookies`

**Storage:**
- `MAX_STORAGE_GB=5` - Storage limit triggers cleanup
- `RETENTION_DAYS=3` - Auto-delete after 3 days (259200 seconds)

**Features:**
- `SEND_POST_DESCRIPTION=true` - Default for new users (toggleable per-user)

**Network:**
- `REDIS_HOST=redis`, `REDIS_PORT=6379`
- `API_PORT=3001`, `API_HOST=0.0.0.0`

**Frontend:**
- `VITE_API_URL=http://localhost:3001`
- `VITE_WS_URL=ws://localhost:3001`

## Port Mappings
- 3000: Web Frontend (Nginx serving Vite build)
- 3001: Web API (FastAPI with uvicorn)
- 6379: Redis (internal network only, not exposed)

## Docker Volumes
- `redis_data` - Named volume for Redis persistence
- `./storage:/storage` - Bind mount for videos/thumbnails
- `./db:/db` - Bind mount for SQLite database
- `./cookies:/cookies` - Bind mount for authentication cookies (downloader only)
