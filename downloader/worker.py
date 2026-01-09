import os
import logging
import asyncio
import json
import time
from datetime import datetime
import redis.asyncio as redis
from telegram import Bot
from ytdlp_wrapper import VideoDownloader
from database import Database

# Configure logging
logging.basicConfig(
    format='[%(asctime)s] [%(levelname)s] [downloader] %(message)s',
    level=getattr(logging, os.getenv('LOG_LEVEL', 'INFO'))
)
logger = logging.getLogger(__name__)

# Environment variables
BOT_TOKEN = os.getenv('TELEGRAM_BOT_TOKEN')
REDIS_HOST = os.getenv('REDIS_HOST', 'redis')
REDIS_PORT = int(os.getenv('REDIS_PORT', 6379))
DATABASE_PATH = os.getenv('DATABASE_PATH', '/db/televideo.db')
STORAGE_PATH = os.getenv('STORAGE_PATH', '/storage')
COOKIES_PATH = os.getenv('COOKIES_PATH', '/cookies')
RETENTION_DAYS = int(os.getenv('RETENTION_DAYS', 3))

class DownloadWorker:
    def __init__(self):
        self.db = Database(DATABASE_PATH)
        self.redis_client = None
        self.bot = Bot(token=BOT_TOKEN)
        self.downloader = VideoDownloader(STORAGE_PATH, COOKIES_PATH)

    async def initialize(self):
        """Initialize database and Redis connection"""
        await self.db.initialize()
        self.redis_client = await redis.from_url(f'redis://{REDIS_HOST}:{REDIS_PORT}')
        logger.info("Download worker initialized successfully")

    async def send_stats_message(self, chat_id: int, stats: dict):
        """Send statistics message to user"""
        delete_time = datetime.fromtimestamp(stats['delete_timestamp'])
        message = f"""✅ Download complete

📹 Original quality: {stats.get('original_quality', 'N/A')}
⬇️ Downloaded quality: {stats['downloaded_quality']}
📦 File size: {self._format_size(stats['file_size'])}
⏱ Processing time: {self._format_duration(stats['processing_time'])}
🎬 Format: {stats['format']} ({stats.get('codec', 'N/A')})
🗑 Auto-delete: {delete_time.strftime('%b %d at %I:%M %p')}

Source: {stats.get('source_platform', 'Unknown')}
Title: {stats['title']}"""

        await self.bot.send_message(chat_id=chat_id, text=message)

    async def send_file(self, chat_id: int, file_path: str, thumbnail_path: str = None, width: int = None, height: int = None):
        """Send video/audio file to user with correct dimensions"""
        try:
            file_size = os.path.getsize(file_path)

            # Telegram has a 2GB file size limit
            if file_size > 2 * 1024 * 1024 * 1024:
                await self.bot.send_message(
                    chat_id=chat_id,
                    text="⚠️ File too large for Telegram (>2GB).\nDownload via web interface: http://localhost:3000"
                )
                return

            # Send video or audio
            if file_path.endswith('.mp3'):
                with open(file_path, 'rb') as audio_file:
                    await self.bot.send_audio(
                        chat_id=chat_id,
                        audio=audio_file
                    )
            else:
                with open(file_path, 'rb') as video_file:
                    thumb = None
                    if thumbnail_path and os.path.exists(thumbnail_path):
                        thumb = open(thumbnail_path, 'rb')

                    # Prepare send_video parameters
                    send_params = {
                        'chat_id': chat_id,
                        'video': video_file,
                        'thumbnail': thumb,
                        'supports_streaming': True
                    }

                    # Add dimensions if available to preserve aspect ratio
                    if width and height:
                        send_params['width'] = width
                        send_params['height'] = height

                    await self.bot.send_video(**send_params)

                    if thumb:
                        thumb.close()

        except Exception as e:
            logger.error(f"Error sending file: {e}")
            await self.bot.send_message(
                chat_id=chat_id,
                text=f"❌ Error sending file: {str(e)}"
            )

    async def process_download(self, task: dict):
        """Process a download task"""
        url = task['url']
        quality = task['quality']
        user_id = task['user_id']
        chat_id = task['chat_id']

        logger.info(f"Processing download: {url} ({quality}) for user {user_id}")

        start_time = time.time()

        try:
            # Download video
            result = await self.downloader.download(url, quality)

            if not result['success']:
                if chat_id != 0:
                    await self.bot.send_message(
                        chat_id=chat_id,
                        text=f"❌ Download failed: {result['error']}"
                    )
                logger.error(f"Download failed: {url} - {result['error']}")
                return

            processing_time = int(time.time() - start_time)
            delete_timestamp = int(time.time()) + (RETENTION_DAYS * 24 * 60 * 60)

            # Prepare video data
            video_data = {
                'video_id': result['video_id'],
                'telegram_user_id': user_id,
                'original_url': url,
                'title': result['title'],
                'original_quality': result.get('original_quality'),
                'downloaded_quality': quality,
                'file_size': result['file_size'],
                'processing_time': processing_time,
                'format': result['format'],
                'codec': result.get('codec'),
                'source_platform': result.get('source_platform'),
                'file_path': result['file_path'],
                'thumbnail_path': result.get('thumbnail_path'),
                'download_timestamp': int(time.time()),
                'delete_timestamp': delete_timestamp
            }

            # Save to database
            await self.db.add_video(video_data)

            # Send stats message and file (only for Telegram bot requests)
            if chat_id != 0:
                await self.send_stats_message(chat_id, video_data)
                await self.send_file(
                    chat_id,
                    result['file_path'],
                    result.get('thumbnail_path'),
                    result.get('width'),
                    result.get('height')
                )

            logger.info(f"Download complete: {result['video_id']} ({self._format_size(result['file_size'])}, {self._format_duration(processing_time)}) [Source: {'Web' if chat_id == 0 else 'Telegram'}]")

        except Exception as e:
            logger.error(f"Error processing download: {e}")
            if chat_id != 0:
                await self.bot.send_message(
                    chat_id=chat_id,
                    text=f"❌ Error: {str(e)}"
                )

    async def run(self):
        """Main worker loop"""
        logger.info("Download worker started, waiting for tasks...")

        while True:
            try:
                # Wait for task from Redis queue
                task_data = await self.redis_client.brpop('download_queue', timeout=5)

                if task_data:
                    _, task_json = task_data
                    task = json.loads(task_json)

                    await self.process_download(task)

                # Small delay to prevent CPU spinning
                await asyncio.sleep(0.1)

            except Exception as e:
                logger.error(f"Error in worker loop: {e}")
                await asyncio.sleep(5)

    def _format_size(self, bytes_size: int) -> str:
        """Format bytes to human readable size"""
        for unit in ['B', 'KB', 'MB', 'GB']:
            if bytes_size < 1024.0:
                return f"{bytes_size:.1f} {unit}"
            bytes_size /= 1024.0
        return f"{bytes_size:.1f} TB"

    def _format_duration(self, seconds: int) -> str:
        """Format seconds to human readable duration"""
        if seconds < 60:
            return f"{seconds}s"
        elif seconds < 3600:
            minutes = seconds // 60
            secs = seconds % 60
            return f"{minutes}m {secs}s"
        else:
            hours = seconds // 3600
            minutes = (seconds % 3600) // 60
            return f"{hours}h {minutes}m"

async def main():
    """Start the worker"""
    worker = DownloadWorker()
    await worker.initialize()
    await worker.run()

if __name__ == '__main__':
    asyncio.run(main())
