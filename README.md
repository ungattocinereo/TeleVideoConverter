# TeleVideoConverter Download

A self-hosted Telegram bot for downloading videos with a web interface for managing files.

## Features

- **Telegram Bot Integration**
  - Download videos from multiple platforms (YouTube, Vimeo, and more via yt-dlp)
  - Quality selection (720p, 1080p, 4K, Audio Only)
  - Automatic file management with 3-day retention
  - User authentication via whitelist

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

To download a video, simply send a URL to the bot!

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
- `TELEGRAM_USER_ID` - Your Telegram user ID
- `REDIS_HOST` - Redis hostname (default: redis)
- `REDIS_PORT` - Redis port (default: 6379)
- `STORAGE_PATH` - Path for video storage (default: /storage)
- `MAX_STORAGE_GB` - Maximum storage in GB (default: 5)
- `RETENTION_DAYS` - Days to keep videos (default: 3)
- `DATABASE_PATH` - SQLite database path (default: /db/anithing.db)
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

## API Endpoints

- `GET /api/videos` - List all videos
- `GET /api/videos/:id` - Get video details
- `GET /api/videos/:id/download` - Download video file
- `DELETE /api/videos/:id` - Delete video
- `GET /api/stats` - Get usage statistics
- `GET /api/search?q=keyword` - Search videos
- `GET /api/thumbnails/:filename` - Get thumbnail image
- `WebSocket /ws` - Real-time updates

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
