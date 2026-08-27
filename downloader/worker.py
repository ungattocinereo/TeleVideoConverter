import os
import html
import logging
import asyncio
import json
import random
import time
from datetime import datetime
import redis as redis_sync
import redis.asyncio as redis
from telegram import Bot
from telegram.request import HTTPXRequest
from ytdlp_wrapper import VideoDownloader
from telegram_media import TelegramMediaPreparer, PreparedTelegramFile
from pelmeni_messages import ERROR_MESSAGES, SUCCESS_MESSAGES
from database import Database

# Configure logging
logging.basicConfig(
    format='[%(asctime)s] [%(levelname)s] [downloader] %(message)s',
    level=getattr(logging, os.getenv('LOG_LEVEL', 'INFO'))
)
logger = logging.getLogger(__name__)
# python-telegram-bot uses token-bearing API URLs internally. Keep them out of logs.
logging.getLogger('httpx').setLevel(logging.WARNING)
logging.getLogger('httpcore').setLevel(logging.WARNING)

# Environment variables
BOT_TOKEN = os.getenv('TELEGRAM_BOT_TOKEN')
REDIS_HOST = os.getenv('REDIS_HOST', 'redis')
REDIS_PORT = int(os.getenv('REDIS_PORT', 6379))
DATABASE_PATH = os.getenv('DATABASE_PATH', '/db/televideo.db')
STORAGE_PATH = os.getenv('STORAGE_PATH', '/storage')
COOKIES_PATH = os.getenv('COOKIES_PATH', '/cookies')
RETENTION_DAYS = int(os.getenv('RETENTION_DAYS', 3))
SEND_POST_DESCRIPTION = os.getenv('SEND_POST_DESCRIPTION', 'true').lower() == 'true'
TELEGRAM_SEND_LIMIT_MB = float(os.getenv('TELEGRAM_SEND_LIMIT_MB', 50))
TELEGRAM_TARGET_MB = float(os.getenv('TELEGRAM_TARGET_MB', 48))
TELEGRAM_MIN_VIDEO_BITRATE_K = int(os.getenv('TELEGRAM_MIN_VIDEO_BITRATE_K', 250))
TELEGRAM_AUDIO_BITRATE_K = int(os.getenv('TELEGRAM_AUDIO_BITRATE_K', 64))
TELEGRAM_LARGE_FILE_POLICY = os.getenv('TELEGRAM_LARGE_FILE_POLICY', 'compress_then_split')
WEB_PUBLIC_URL = os.getenv('WEB_PUBLIC_URL', 'https://televideo.cnr.pw')
DOWNLOAD_STATUS_TTL_SECONDS = 4 * 60 * 60
TERMINAL_STATUS_TTL_SECONDS = 5 * 60

class DownloadWorker:
    def __init__(self):
        self.db = Database(DATABASE_PATH)
        self.redis_client = None
        self.status_redis = redis_sync.Redis(
            host=REDIS_HOST,
            port=REDIS_PORT,
            decode_responses=True,
            socket_connect_timeout=0.5,
            socket_timeout=0.5,
        )
        self.progress_publish_warning_logged = False
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
        self.terminal_message_bags = {
            "done": [],
            "error": [],
        }
        self.telegram_preparer = TelegramMediaPreparer(
            STORAGE_PATH,
            send_limit_mb=TELEGRAM_SEND_LIMIT_MB,
            target_mb=TELEGRAM_TARGET_MB,
            min_video_bitrate_k=TELEGRAM_MIN_VIDEO_BITRATE_K,
            audio_bitrate_k=TELEGRAM_AUDIO_BITRATE_K,
            large_file_policy=TELEGRAM_LARGE_FILE_POLICY,
        )

    async def initialize(self):
        """Initialize database and Redis connection"""
        await self.db.initialize()
        self.redis_client = await redis.from_url(f'redis://{REDIS_HOST}:{REDIS_PORT}')
        logger.info("Download worker initialized successfully")

    def get_terminal_message(self, state: str) -> str:
        """Return a shuffled success or failure message without early repeats."""
        messages = SUCCESS_MESSAGES if state == "done" else ERROR_MESSAGES
        if not self.terminal_message_bags[state]:
            self.terminal_message_bags[state] = messages.copy()
            random.shuffle(self.terminal_message_bags[state])

        return self.terminal_message_bags[state].pop()

    async def set_download_status(self, task: dict, state: str):
        """Publish a non-terminal state without risking the actual download."""
        status_key = task.get('status_key')
        if not status_key or self.redis_client is None:
            return

        try:
            await self.redis_client.set(
                status_key,
                state,
                ex=DOWNLOAD_STATUS_TTL_SECONDS,
            )
        except Exception as exc:
            logger.warning(f"Could not publish download status {state}: {exc}")

    def publish_download_progress(self, task: dict, progress: dict):
        """Publish throttled yt-dlp progress from its synchronous hook."""
        status_key = task.get('status_key')
        status_redis = getattr(self, 'status_redis', None)
        if not status_key or status_redis is None:
            return

        payload = {
            "state": "working",
            **progress,
        }
        try:
            status_redis.set(
                status_key,
                json.dumps(payload, separators=(",", ":")),
                ex=DOWNLOAD_STATUS_TTL_SECONDS,
            )
            self.progress_publish_warning_logged = False
        except Exception as exc:
            if not getattr(self, 'progress_publish_warning_logged', False):
                logger.warning(f"Could not publish yt-dlp progress: {exc}")
                self.progress_publish_warning_logged = True

    async def finish_download_status(self, task: dict, state: str, text: str) -> bool:
        """Stop animation and replace its message; return whether editing succeeded."""
        status_key = task.get('status_key')
        if status_key and self.redis_client is not None:
            try:
                await self.redis_client.set(
                    status_key,
                    state,
                    ex=TERMINAL_STATUS_TTL_SECONDS,
                )
            except Exception as exc:
                logger.warning(f"Could not publish terminal download status {state}: {exc}")

        status_message_id = task.get('status_message_id')
        chat_id = task.get('chat_id')
        if not status_message_id or not chat_id:
            return False

        try:
            await self.bot.edit_message_text(
                chat_id=chat_id,
                message_id=status_message_id,
                text=text,
            )
            return True
        except Exception as exc:
            logger.warning(f"Could not edit terminal Telegram status: {exc}")
            return False

    def build_success_status(self, processing_time: int) -> str:
        return (
            f"🥟 Пельменный ритуал завершён · "
            f"{self._format_duration(processing_time)}\n\n"
            f"{self.get_terminal_message('done')}"
        )

    def build_error_status(self, reason: str) -> str:
        safe_reason = (reason or "Unknown error")[:1200]
        return (
            "🥟 Пельменный ритуал сорвался\n\n"
            f"{self.get_terminal_message('error')}\n\n"
            f"Техническая причина: {safe_reason}"
        )

    def _code(self, value) -> str:
        return f"<code>{html.escape(str(value))}</code>"

    async def send_stats_message(self, chat_id: int, stats: dict):
        """Send statistics message to user"""
        delete_time = datetime.fromtimestamp(stats['delete_timestamp'])
        format_value = f"{stats['format']} ({stats.get('codec', 'N/A')})"
        message = f"""🥟 Пельменный протокол: файл готов

🥖 Original quality: {self._code(stats.get('original_quality', 'N/A'))}
🥐 Downloaded quality: {self._code(stats['downloaded_quality'])}
🥟 File size: {self._code(self._format_size(stats['file_size']))}
🥨 Processing time: {self._code(self._format_duration(stats['processing_time']))}
🥯 Format: {self._code(format_value)}
🥠 Auto-delete: {self._code(delete_time.strftime('%b %d at %I:%M %p'))}

Source: {self._code(stats.get('source_platform', 'Unknown'))}
Title: {html.escape(stats['title'])}"""

        await self.bot.send_message(
            chat_id=chat_id,
            text=message,
            parse_mode='HTML',
            disable_web_page_preview=True,
        )

    async def send_file(self, chat_id: int, file_path: str, thumbnail_path: str = None, width: int = None, height: int = None):
        """Send video/audio file to user with correct dimensions"""
        prepared_files = []
        try:
            file_size = os.path.getsize(file_path)

            if file_path.endswith('.mp3'):
                if file_size > self.telegram_preparer.send_limit_bytes:
                    await self.send_web_fallback(
                        chat_id,
                        file_size,
                        "Audio file is too large for Telegram Bot API upload"
                    )
                    return

                with open(file_path, 'rb') as audio_file:
                    await self.bot.send_audio(
                        chat_id=chat_id,
                        audio=audio_file
                    )
                return

            if file_size > self.telegram_preparer.target_bytes:
                await self.bot.send_message(
                    chat_id=chat_id,
                    text=(
                        f"🥧 File is {self._format_size(file_size)}. "
                        "Compressing it for Telegram upload..."
                    )
                )

            prepared_files = self.telegram_preparer.prepare_telegram_files(file_path)

            for prepared_file in prepared_files:
                prepared_size = os.path.getsize(prepared_file.path)
                if prepared_size > self.telegram_preparer.send_limit_bytes:
                    raise RuntimeError(
                        f"Prepared file is too large for Telegram: {self._format_size(prepared_size)}"
                    )

                await self.send_prepared_video(
                    chat_id,
                    prepared_file,
                    thumbnail_path,
                    fallback_width=width,
                    fallback_height=height,
                )

        except Exception as e:
            logger.error(f"Error sending file: {e}")
            fallback_size = os.path.getsize(file_path) if os.path.exists(file_path) else 0
            await self.send_web_fallback(chat_id, fallback_size, str(e))
        finally:
            self.telegram_preparer.cleanup(prepared_files)

    async def send_prepared_video(
        self,
        chat_id: int,
        prepared_file: PreparedTelegramFile,
        thumbnail_path: str = None,
        fallback_width: int = None,
        fallback_height: int = None,
    ):
        """Send one Telegram-sized video file."""
        if prepared_file.part_count > 1:
            await self.bot.send_message(
                chat_id=chat_id,
                text=f"🥟 Sending compressed part {prepared_file.part_index}/{prepared_file.part_count}"
            )

        thumb = None
        try:
            with open(prepared_file.path, 'rb') as video_file:
                if thumbnail_path and os.path.exists(thumbnail_path):
                    thumb = open(thumbnail_path, 'rb')

                send_params = {
                    'chat_id': chat_id,
                    'video': video_file,
                    'thumbnail': thumb,
                    'supports_streaming': True
                }

                width = prepared_file.width or fallback_width
                height = prepared_file.height or fallback_height
                if width and height:
                    send_params['width'] = width
                    send_params['height'] = height

                if prepared_file.part_count > 1:
                    send_params['caption'] = f"Part {prepared_file.part_index}/{prepared_file.part_count}"

                await self.bot.send_video(**send_params)
        finally:
            if thumb:
                thumb.close()

    async def send_web_fallback(self, chat_id: int, file_size: int, reason: str):
        size_mb = file_size / (1024 * 1024)
        safe_reason = (reason or "Unknown error")[:1200]
        await self.bot.send_message(
            chat_id=chat_id,
            text=(
                f"⚠️ Could not upload file directly to Telegram "
                f"({size_mb:.1f} MB).\n\n"
                f"Reason: {safe_reason}\n\n"
                f"📥 Download via web interface:\n{WEB_PUBLIC_URL}"
            )
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
        await self.set_download_status(task, "working")

        try:
            # Download video
            progress_callback = None
            if task.get('status_key'):
                progress_callback = lambda progress: self.publish_download_progress(task, progress)

            result = await self.downloader.download(
                url,
                quality,
                progress_callback=progress_callback,
            )

            if not result['success']:
                error_text = self.build_error_status(result['error'])
                status_edited = await self.finish_download_status(
                    task,
                    "error",
                    error_text,
                )
                if chat_id != 0:
                    if not status_edited:
                        await self.bot.send_message(chat_id=chat_id, text=error_text)
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
                await self.set_download_status(task, "delivering")
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

                success_text = self.build_success_status(processing_time)
                status_edited = await self.finish_download_status(
                    task,
                    "done",
                    success_text,
                )
                if not status_edited and task.get('status_message_id'):
                    await self.bot.send_message(chat_id=chat_id, text=success_text)

            logger.info(f"Download complete: {result['video_id']} ({self._format_size(result['file_size'])}, {self._format_duration(processing_time)}) [Source: {'Web' if chat_id == 0 else 'Telegram'}]")

        except Exception as e:
            logger.error(f"Error processing download: {e}")
            error_text = self.build_error_status(str(e))
            status_edited = await self.finish_download_status(task, "error", error_text)
            if chat_id != 0:
                if not status_edited:
                    await self.bot.send_message(chat_id=chat_id, text=error_text)

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
