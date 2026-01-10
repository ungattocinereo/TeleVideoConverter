# TeleVideoConverter Download

A self-hosted Telegram bot for downloading videos with a web interface for managing files.

## Features

- **Telegram Bot Integration**
  - Download videos from multiple platforms (YouTube, Instagram, TikTok, Vimeo, and more via yt-dlp)
  - Automatic best quality selection with universal device compatibility
  - Post description extraction and optional display (toggle with /description)
  - Per-user settings stored in database
  - Automatic file management with 3-day retention
  - User authentication via whitelist
  - Cookie-based authentication for private content (Instagram, TikTok, etc.)

- **Web Interface**
  - Modern dark-themed UI built with React and shadcn/ui
  - Real-time updates via WebSocket
  - Video grid with thumbnails
  - Search functionality
  - Detailed video information
  - Direct download from browser

- **Storage Management**
  - Automatic cleanup of expired videos
  - 5GB storage limit with automatic management
  - SQLite database for metadata
  - Efficient file organization

- **Video Encoding & Compatibility**
  - Force re-encoding with H.264 Baseline profile for universal compatibility
  - Optimized for iOS, macOS, Android, and Desktop Telegram clients
  - Proper keyframe intervals and streaming support
  - Fixes Instagram/TikTok video playback issues on Apple devices
  - Automatic codec conversion ensuring videos play correctly everywhere

## Prerequisites

- Docker and Docker Compose
- Telegram Bot Token (from @BotFather)
- Your Telegram User ID (from @userinfobot)

## Quick Start

1. **Clone the repository**
```bash
git clone <repository-url>
cd TeleVideoConverter
```

2. **Configure environment variables**

The `.env` file is already configured with your Telegram credentials. All environment variables are set and ready to use.

3. **Start the services**
```bash
docker-compose up -d
```

4. **Access the web interface**

Open your browser and navigate to:
```
http://localhost:3000
```

5. **Use the Telegram bot**

Send a video URL to your bot and select the desired quality!

## Architecture

The system consists of 6 Docker services:

### 1. **Redis**
- Message queue for download tasks
- Handles communication between bot and downloader

### 2. **Telegram Bot**
- Receives video URLs from users
- Presents quality selection interface
- Sends completion notifications with stats
- Manages user interactions

### 3. **Downloader**
- Processes download queue from Redis
- Uses yt-dlp for video extraction
- Generates thumbnails
- Saves metadata to database
- Sends files back to Telegram

### 4. **Web API**
- FastAPI backend with WebSocket support
- RESTful endpoints for video management
- Real-time notifications
- File serving

### 5. **Web Frontend**
- React + TypeScript + Vite
- shadcn/ui components
- Dark theme
- Responsive design
- Real-time updates

### 6. **Cleanup Service**
- Runs hourly cleanup cycle
- Removes expired videos (>3 days old)
- Manages storage limits
- Maintains database integrity

## Telegram Bot Commands

- `/start` - Show welcome message and instructions
- `/list` - List all saved videos with details
- `/search <keyword>` - Search videos by title
- `/stats` - Show usage statistics
- `/description` - Toggle post description display (on/off)

To download a video, simply send a URL to the bot! The bot will:
1. Send technical details (file size, quality, processing time)
2. Send the video file with thumbnail
3. Send the post description (if enabled with `/description`)

## Development

### Running locally (without Docker)

Each service can be run independently for development:

**Telegram Bot:**
```bash
cd telegram-bot
pip install -r requirements.txt
python bot.py
```

**Downloader:**
```bash
cd downloader
pip install -r requirements.txt
python worker.py
```

**Web API:**
```bash
cd web-api
pip install -r requirements.txt
python api.py
```

**Web Frontend:**
```bash
cd web-frontend
npm install
npm run dev
```

**Cleanup:**
```bash
cd cleanup
pip install -r requirements.txt
python cleanup_cron.py
```

### Environment Variables

All configuration is done via `.env`:

- `TELEGRAM_BOT_TOKEN` - Your Telegram bot token
- `TELEGRAM_USER_IDS` - Comma-separated list of allowed Telegram user IDs
- `REDIS_HOST` - Redis hostname (default: redis)
- `REDIS_PORT` - Redis port (default: 6379)
- `STORAGE_PATH` - Path for video storage (default: /storage)
- `MAX_STORAGE_GB` - Maximum storage in GB (default: 5)
- `RETENTION_DAYS` - Days to keep videos (default: 3)
- `SEND_POST_DESCRIPTION` - Send post descriptions by default (default: true)
- `COOKIES_PATH` - Path for authentication cookies (default: /cookies)
- `DATABASE_PATH` - SQLite database path (default: /db/televideo.db)
- `API_PORT` - Web API port (default: 3001)
- `LOG_LEVEL` - Logging level (default: INFO)

## Storage Structure

```
storage/
├── videos/          # Downloaded video files
└── thumbnails/      # Generated thumbnail images

db/
└── anithing.db     # SQLite database
```

## Database Schema

### Videos Table
Stores video metadata including:
- Video ID, title, URL
- Quality information
- File paths and sizes
- Download and delete timestamps
- Processing statistics

### Download Stats Table
Tracks user actions:
- Download events
- Delete events
- Timestamps for analytics

### User Settings Table
Stores per-user preferences:
- Post description display preference
- Other customizable settings
- Created and updated timestamps

## API Endpoints

- `GET /api/videos` - List all videos
- `GET /api/videos/:id` - Get video details
- `GET /api/videos/:id/download` - Download video file
- `DELETE /api/videos/:id` - Delete video
- `GET /api/stats` - Get usage statistics
- `GET /api/search?q=keyword` - Search videos
- `GET /api/thumbnails/:filename` - Get thumbnail image
- `WebSocket /ws` - Real-time updates

## Video Encoding Technology

This project uses advanced video encoding to ensure **bulletproof compatibility** across all devices:

### Why Re-encoding?
Instagram, TikTok, and other platforms often use H.264 High Profile codecs that don't work properly on iOS/macOS Telegram clients. Videos appear "frozen" with only audio playing.

### Our Solution
Every downloaded video is **automatically re-encoded** with:
- **H.264 Baseline Profile** - Maximum compatibility across all devices
- **Level 3.0** - Universal device support
- **Proper keyframe intervals (GOP 50)** - Smooth playback and seeking
- **AAC audio at 44.1kHz** - Standard compatibility
- **faststart flag** - Enables streaming before full download
- **Even dimensions** - Prevents encoding artifacts

### Result
✅ Videos from Instagram work perfectly on iPhone/iPad/Mac
✅ No more "frozen first frame" issues
✅ Proper playback controls and seeking
✅ Universal compatibility across all Telegram clients

The re-encoding process adds 5-10 seconds to download time but **guarantees** your videos will play correctly everywhere.

## Troubleshooting

### Videos not downloading
1. Check bot logs: `docker-compose logs telegram-bot`
2. Check downloader logs: `docker-compose logs downloader`
3. Ensure yt-dlp supports the platform

### Web interface not loading
1. Check web-api logs: `docker-compose logs web-api`
2. Check web-frontend logs: `docker-compose logs web-frontend`
3. Verify ports 3000 and 3001 are not in use

### Storage full
- Check storage usage in web interface
- Manually run cleanup: `docker-compose restart cleanup-cron`
- Adjust `MAX_STORAGE_GB` in `.env`

## Logs

View logs for any service:
```bash
docker-compose logs -f <service-name>
```

Available services:
- `redis`
- `telegram-bot`
- `downloader`
- `web-api`
- `web-frontend`
- `cleanup-cron`

## Updating

```bash
git pull
docker-compose down
docker-compose build
docker-compose up -d
```

## Backup

Backup database and videos:
```bash
tar -czf backup-$(date +%Y%m%d).tar.gz storage/ db/
```

## License

MIT

## Copyright

Gregory 'Cinereo' Smirnov
