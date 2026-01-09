# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Anithing Download is a self-hosted Telegram bot system for downloading videos from various platforms (YouTube, Vimeo, etc.) with a web interface for file management. The system uses Docker Compose to orchestrate 6 microservices.

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
2. **Telegram Bot → Redis**: Bot pushes download task to queue
3. **Redis → Downloader**: Downloader picks up task from queue
4. **Downloader → yt-dlp**: Downloads video using yt-dlp
5. **Downloader → SQLite**: Saves metadata to database
6. **Downloader → Telegram Bot API**: Sends file back to user
7. **Web API ← SQLite**: Reads video data for web interface
8. **Web Frontend ← Web API**: Displays videos via REST API
9. **Cleanup Service ← SQLite**: Periodically checks for expired videos

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
- `database.py` - SQLite database operations
- `utils.py` - Helper functions (formatting, validation)

**Key Patterns:**
- All handlers check user permissions via `check_user_permission()`
- Rate limiting tracked in-memory with `user_downloads` dict
- Download tasks serialized as JSON and pushed to Redis queue

### Downloader (`downloader/`)
- `worker.py` - Main worker loop consuming Redis queue
- `ytdlp_wrapper.py` - yt-dlp integration and video processing
- `database.py` - Database operations for saving video metadata

**Key Patterns:**
- Runs infinite loop with `brpop` on Redis queue (blocking)
- Quality mapping: `720p` → height<=720, `4k` → height<=2160, `audio` → audio-only MP3
- Always converts to MP4 (H.264) for Telegram compatibility
- Generates 320x180 JPEG thumbnails
- Sends two messages: (1) statistics, (2) actual file

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

## Important Implementation Details

### File Storage
- Videos: `/storage/videos/{video_id}.{ext}`
- Thumbnails: `/storage/thumbnails/{video_id}.jpg`
- Database: `/db/anithing.db`

### Quality Mappings
- `720p` → `bestvideo[height<=720]+bestaudio/best[height<=720]`
- `1080p` → `bestvideo[height<=1080]+bestaudio/best[height<=1080]`
- `4k` → `bestvideo[height<=2160]+bestaudio/best[height<=2160]`
- `audio` → `bestaudio/best` → converted to MP3 192kbps

### Security
- Only one Telegram user ID allowed (hardcoded in .env)
- Rate limit: 10 downloads per hour per user
- No authentication on web interface (assumes private network)
- Storage limit enforced: 5GB maximum

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
1. Check bot logs for URL validation
2. Check downloader logs for yt-dlp errors
3. Test yt-dlp directly: `yt-dlp --list-formats <URL>`
4. Verify ffmpeg is installed in downloader container

### Modifying Storage Limits
1. Update `MAX_STORAGE_GB` in `.env`
2. Update `RETENTION_DAYS` in `.env`
3. Restart services: `docker-compose restart`

## Environment Variables Location
All configuration in `.env` file at project root. Already configured with Telegram credentials.

## Port Mappings
- 3000: Web Frontend (Nginx)
- 3001: Web API (FastAPI)
- 6379: Redis (internal only)

## File Permissions
Storage and database directories need write permissions:
```bash
chmod -R 777 storage/ db/
```
