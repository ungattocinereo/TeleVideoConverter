import os
import logging
import asyncio
import json
import time
from datetime import datetime
import redis.asyncio as redis
from telegram import Bot
from telegram.request import HTTPXRequest
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
SEND_POST_DESCRIPTION = os.getenv('SEND_POST_DESCRIPTION', 'true').lower() == 'true'

class DownloadWorker:
    def __init__(self):
        self.db = Database(DATABASE_PATH)
        self.redis_client = None
        # Configure larger timeouts for large file uploads
        # read_timeout: time to wait for server response
        # write_timeout: time to wait for upload to complete
        # connect_timeout: time to wait for connection
        request = HTTPXRequest(
            connection_pool_size=8,
            read_timeout=300.0,      # 5 minutes for reading response
            write_timeout=300.0,     # 5 minutes for uploading large files
            connect_timeout=30.0,    # 30 seconds for connection
            pool_timeout=30.0
        )
        self.bot = Bot(token=BOT_TOKEN, request=request)
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

            # Telegram Bot API has a 50MB file size limit for send_video
            # Files larger than 50MB must be downloaded via web interface
            if file_size > 50 * 1024 * 1024:
                size_mb = file_size / (1024 * 1024)
                await self.bot.send_message(
                    chat_id=chat_id,
                    text=f"⚠️ File too large for Telegram Bot API ({size_mb:.1f} MB > 50 MB).\n\n📥 Download via web interface:\nhttps://televideo.cnr.pw"
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

    async def send_description(self, chat_id: int, description: str, title: str):
        """Send post description to user as a separate message"""
        try:
            if not description or not description.strip():
                logger.debug("No description available, skipping")
                return

            # Truncate description if too long (Telegram message limit is 4096 characters)
            max_length = 4000
            if len(description) > max_length:
                description = description[:max_length] + "..."

            message = f"📝 Description:\n\n{description}"

            await self.bot.send_message(
                chat_id=chat_id,
                text=message
            )
            logger.info(f"Description sent for: {title}")

        except Exception as e:
            logger.error(f"Error sending description: {e}")
            # Don't send error message to user - description is optional

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

                # Send description if enabled (check user setting)
                user_setting = await self.db.get_user_setting(user_id, 'send_description')
                # Default to global env var if user hasn't set preference
                send_desc = bool(user_setting) if user_setting is not None else SEND_POST_DESCRIPTION

                if send_desc:
                    await self.send_description(
                        chat_id,
                        result.get('description', ''),
                        result['title']
                    )

            logger.info(f"Download complete: {result['video_id']} ({self._format_size(result['file_size'])}, {self._format_duration(processing_time)}) [Source: {'Web' if chat_id == 0 else 'Telegram'}]")

        except Exception as e:
            logger.error(f"Error processing download: {e}")
            if chat_id != 0:
                await self.bot.send_message(
                    chat_id=chat_id,
                    text=f"❌ Error: {str(e)}"
                )

    async def ensure_redis_connection(self):
        """Ensure Redis connection is alive, reconnect if needed"""
        try:
            await self.redis_client.ping()
            return True
        except Exception as e:
            logger.warning(f"Redis connection lost, reconnecting: {e}")
            try:
                self.redis_client = await redis.from_url(f'redis://{REDIS_HOST}:{REDIS_PORT}')
                await self.redis_client.ping()
                logger.info("Redis reconnected successfully")
                return True
            except Exception as reconnect_error:
                logger.error(f"Redis reconnection failed: {reconnect_error}")
                return False

    async def run(self):
        """Main worker loop"""
        logger.info("Download worker started, waiting for tasks...")

        while True:
            try:
                # Ensure Redis connection is alive
                if not await self.ensure_redis_connection():
                    await asyncio.sleep(5)
                    continue

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
