# TeleVideoConverter - Comprehensive Project Overview

## Project Summary

TeleVideoConverter is a self-hosted Telegram bot for downloading videos from multiple platforms with a web interface for managing files. The system combines a Telegram bot, downloader service, web API, and web frontend to provide a complete video downloading and management solution.

## Architecture

The system is composed of 6 interconnected Docker services:

1. **Redis**: Message queue for download tasks and inter-service communication
2. **Telegram Bot**: Handles user interactions, receives URLs, and sends videos back to users
3. **Downloader**: Processes download queue using yt-dlp, generates thumbnails, and manages file encoding
4. **Web API**: FastAPI backend with WebSocket support for real-time updates and REST endpoints
5. **Web Frontend**: React + TypeScript + Tailwind CSS interface for video management
6. **Cleanup Service**: Automated cleanup of expired videos and storage management

## Key Features

### Telegram Bot Integration
- Download videos from multiple platforms (YouTube, Instagram, TikTok, Vimeo, etc.) via yt-dlp
- Automatic best quality selection with universal device compatibility
- Post description extraction with optional display toggle (`/description`)
- Per-user settings stored in database
- Automatic file management with configurable retention (default 3 days)
- User authentication via whitelist
- Cookie-based authentication for private content

### Web Interface
- Modern dark-themed UI built with React and shadcn/ui
- Real-time updates via WebSocket
- Video grid with thumbnails
- Search functionality
- Detailed video information
- Direct download capability

### Storage Management
- Automatic cleanup of expired videos
- Configurable storage limit (default 5GB) with automatic management
- SQLite database for metadata storage
- Efficient file organization in storage directory

### Video Encoding & Compatibility
- Automatic re-encoding with H.264 Baseline profile for universal compatibility
- Optimized for iOS, macOS, Android, and Desktop Telegram clients
- Proper keyframe intervals and streaming support
- Fixes Instagram/TikTok video playback issues on Apple devices
- Uses faststart flag for streaming before full download

## Building and Running

### Prerequisites
- Docker and Docker Compose
- Telegram Bot Token (from @BotFather)
- Your Telegram User ID (from @userinfobot)

### Quick Start
1. Clone the repository
```bash
git clone <repository-url>
cd TeleVideoConverter
```

2. Configure environment variables in `.env` file:
```bash
TELEGRAM_BOT_TOKEN=your_bot_token_here
TELEGRAM_USER_IDS=comma_separated_list_of_user_ids
```

3. Start all services:
```bash
docker-compose up -d
```

4. Access the web interface at:
```
http://localhost:3000
```

### Individual Service Development
For development purposes, each service can be run independently:

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

**Cleanup Service:**
```bash
cd cleanup
pip install -r requirements.txt
python cleanup_cron.py
```

## Configuration

Environment variables for all services:

- `TELEGRAM_BOT_TOKEN` - Telegram bot token
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

## File Structure

```
storage/
├── videos/          # Downloaded video files
└── thumbnails/      # Generated thumbnail images

db/
└── televideo.db     # SQLite database
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
- `POST /api/submit-url` - Submit URL for download
- `WebSocket /ws` - Real-time updates

## Telegram Bot Commands

- `/start` - Show welcome message and instructions
- `/list` - List all saved videos with details
- `/search <keyword>` - Search videos by title
- `/stats` - Show usage statistics
- `/description` - Toggle post description display (on/off)

To download a video, simply send a URL to the bot. The bot will:
1. Send technical details (file size, quality, processing time)
2. Send the video file with thumbnail
3. Send the post description (if enabled with `/description`)

## Video Encoding Technology

This project addresses compatibility issues with video formats across different platforms and devices:

### Problem
Instagram, TikTok, and other platforms often use H.264 High Profile codecs that don't work properly on iOS/macOS Telegram clients. Videos appear "frozen" with only audio playing.

### Solution
Every downloaded video is automatically re-encoded with:
- **H.264 Baseline Profile** - Maximum compatibility across all devices
- **Level 3.0** - Universal device support
- **Proper keyframe intervals (GOP 50)** - Smooth playback and seeking
- **AAC audio at 44.1kHz** - Standard compatibility
- **faststart flag** - Enables streaming before full download
- **Even dimensions** - Prevents encoding artifacts

### Result
- Videos from Instagram work perfectly on iPhone/iPad/Mac
- No more "frozen first frame" issues
- Proper playback controls and seeking
- Universal compatibility across all Telegram clients

The re-encoding process adds 5-10 seconds to download time but guarantees videos will play correctly everywhere.

## Development Conventions

- Services communicate via Redis queues and shared storage/database
- Use async/await throughout for optimal performance
- Follow consistent logging patterns across services
- Implement proper error handling and validation
- Use environment variables for configuration
- Maintain consistent data structures across services

## Monitoring and Maintenance

### Viewing Logs
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

### Updating
```bash
git pull
docker-compose down
docker-compose build
docker-compose up -d
```

### Backup
```bash
tar -czf backup-$(date +%Y%m%d).tar.gz storage/ db/
```

### Troubleshooting
- **Videos not downloading**: Check bot and downloader logs
- **Web interface not loading**: Check web-api and web-frontend logs
- **Storage full**: Manually run cleanup or adjust storage limits

## Technologies Used

- **Backend**: Python (FastAPI, asyncio)
- **Frontend**: React, TypeScript, Tailwind CSS, Vite
- **Database**: SQLite
- **Message Queue**: Redis
- **Containerization**: Docker, Docker Compose
- **Video Processing**: yt-dlp, ffmpeg
- **UI Components**: shadcn/ui, Radix UI
- **Icons**: Lucide React