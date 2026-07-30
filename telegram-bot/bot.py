import os
import random
import logging
import asyncio
import time
import uuid
from datetime import datetime, timedelta
from telegram import Update
from telegram.ext import Application, CommandHandler, MessageHandler, filters, ContextTypes
import redis.asyncio as redis
import json
from database import Database
from pelmeni_messages import STATUS_MESSAGE_POOLS
from utils import format_size, format_duration, is_valid_url

# Configure logging
logging.basicConfig(
    format='[%(asctime)s] [%(levelname)s] [telegram-bot] %(message)s',
    level=getattr(logging, os.getenv('LOG_LEVEL', 'INFO'))
)
logger = logging.getLogger(__name__)

# Environment variables
BOT_TOKEN = os.getenv('TELEGRAM_BOT_TOKEN')
# Parse comma-separated list of allowed user IDs
ALLOWED_USER_IDS = set(int(uid.strip()) for uid in os.getenv('TELEGRAM_USER_IDS', '').split(',') if uid.strip())
REDIS_HOST = os.getenv('REDIS_HOST', 'redis')
REDIS_PORT = int(os.getenv('REDIS_PORT', 6379))
DATABASE_PATH = os.getenv('DATABASE_PATH', '/db/televideo.db')
MAX_STORAGE_GB = float(os.getenv('MAX_STORAGE_GB', 5))

# Rate limiting
user_downloads = {}
MAX_DOWNLOADS_PER_HOUR = 10
STATUS_POLL_INTERVAL_SECONDS = 3
JOKE_UPDATE_INTERVAL_SECONDS = 12
DOWNLOAD_STATUS_TTL_SECONDS = 4 * 60 * 60
TERMINAL_STATUS_STATES = {"done", "error"}
PROGRESS_BAR_WIDTH = 16
STATUS_TITLES = {
    "queued": ("🥟", "Ссылка в пельменной очереди"),
    "working": ("🥟", "Пельмени скачивают и приводят видео в порядок"),
    "delivering": ("🥐", "Пельменная доставка готовит отправку"),
}

class TelegramBot:
    def __init__(self):
        self.db = Database(DATABASE_PATH)
        self.redis_client = None
        self.status_message_bags = {state: [] for state in STATUS_MESSAGE_POOLS}

    async def initialize(self):
        """Initialize database and Redis connection"""
        await self.db.initialize()
        self.redis_client = await redis.from_url(f'redis://{REDIS_HOST}:{REDIS_PORT}')
        logger.info(f"Bot initialized successfully. Authorized users: {ALLOWED_USER_IDS}")

    def check_user_permission(self, user_id: int) -> bool:
        """Check if user is allowed to use the bot"""
        return user_id in ALLOWED_USER_IDS

    def check_rate_limit(self, user_id: int) -> bool:
        """Check if user has exceeded rate limit"""
        now = datetime.now()
        if user_id not in user_downloads:
            user_downloads[user_id] = []

        # Remove downloads older than 1 hour
        user_downloads[user_id] = [
            ts for ts in user_downloads[user_id]
            if now - ts < timedelta(hours=1)
        ]

        if len(user_downloads[user_id]) >= MAX_DOWNLOADS_PER_HOUR:
            return False

        user_downloads[user_id].append(now)
        return True

    def get_status_joke(self, state: str) -> str:
        """Return a shuffled stage-specific message without early repeats."""
        if state not in STATUS_MESSAGE_POOLS:
            state = "working"

        if not self.status_message_bags[state]:
            self.status_message_bags[state] = STATUS_MESSAGE_POOLS[state].copy()
            random.shuffle(self.status_message_bags[state])

        return self.status_message_bags[state].pop()

    @staticmethod
    def format_elapsed(elapsed_seconds: float) -> str:
        total_seconds = max(0, int(elapsed_seconds))
        minutes, seconds = divmod(total_seconds, 60)
        hours, minutes = divmod(minutes, 60)
        if hours:
            return f"{hours:d}:{minutes:02d}:{seconds:02d}"
        return f"{minutes:02d}:{seconds:02d}"

    @staticmethod
    def format_progress_size(bytes_size) -> str:
        if bytes_size is None:
            return ""

        value = float(bytes_size)
        for unit in ("B", "KB", "MB", "GB"):
            if value < 1024 or unit == "GB":
                return f"{value:.1f} {unit}"
            value /= 1024
        return f"{value:.1f} GB"

    @staticmethod
    def parse_status_payload(raw_state) -> dict:
        text = raw_state.decode() if isinstance(raw_state, bytes) else str(raw_state)
        try:
            payload = json.loads(text)
        except (json.JSONDecodeError, TypeError):
            payload = None

        if isinstance(payload, dict) and payload.get("state"):
            return payload
        return {"state": text}

    @staticmethod
    def clamp_percent(percent):
        try:
            return max(0.0, min(100.0, float(percent)))
        except (TypeError, ValueError):
            return None

    def build_progress_bar(self, percent, elapsed_seconds: float) -> str:
        percent = self.clamp_percent(percent)
        if percent is not None:
            filled = round(PROGRESS_BAR_WIDTH * percent / 100)
            bar = "█" * filled + "░" * (PROGRESS_BAR_WIDTH - filled)
            return f"[{bar}] {percent:5.1f}%"

        pulse_width = 3
        travel = PROGRESS_BAR_WIDTH - pulse_width
        step = int(elapsed_seconds // STATUS_POLL_INTERVAL_SECONDS)
        cycle_position = step % (travel * 2 or 1)
        pulse_start = cycle_position if cycle_position <= travel else travel * 2 - cycle_position
        bar = (
            "░" * pulse_start
            + "▓" * pulse_width
            + "░" * (PROGRESS_BAR_WIDTH - pulse_start - pulse_width)
        )
        return f"[{bar}]"

    def build_progress_details(self, state: str, elapsed_seconds: float, payload: dict) -> str:
        if state == "queued":
            return (
                f"{self.build_progress_bar(None, elapsed_seconds)}\n"
                "🥨 Кастрюля ищет свободную конфорку"
            )

        if state == "delivering":
            return (
                f"{self.build_progress_bar(100, elapsed_seconds)}\n"
                "🥐 Скачивание завершено · пельмени несут файл в Telegram"
            )

        phase = payload.get("phase")
        if phase == "processing":
            return (
                f"{self.build_progress_bar(100, elapsed_seconds)}\n"
                "🥧 Скачивание завершено · перекодирую и проверяю начинку"
            )

        percent = self.clamp_percent(payload.get("percent"))
        lines = [self.build_progress_bar(percent, elapsed_seconds)]
        phase_labels = {
            "video": "🥟 Качаю видеослой",
            "audio": "🥨 Качаю аудиослой",
            "media": "🥖 Качаю файл",
        }
        lines.append(phase_labels.get(phase, "🥯 Качаю то, что удалось поймать шумовкой"))

        downloaded = payload.get("downloaded_bytes")
        total = payload.get("total_bytes")
        speed = payload.get("speed")
        eta = payload.get("eta")
        metrics = []
        if downloaded is not None:
            amount = self.format_progress_size(downloaded)
            if total:
                amount += f" / {self.format_progress_size(total)}"
            metrics.append(amount)
        if speed:
            metrics.append(f"{self.format_progress_size(speed)}/s")
        if eta is not None:
            metrics.append(f"ещё ≈ {self.format_elapsed(eta)}")
        if metrics:
            lines.append(" · ".join(metrics))

        return "\n".join(lines)

    def build_status_message(
        self,
        state: str,
        elapsed_seconds: float,
        payload: dict = None,
        joke: str = None,
    ) -> str:
        emoji, title = STATUS_TITLES.get(state, STATUS_TITLES["working"])
        payload = payload or {"state": state}
        joke = joke or self.get_status_joke(state)
        elapsed = self.format_elapsed(elapsed_seconds)
        progress = self.build_progress_details(state, elapsed_seconds, payload)
        return f"{emoji} {title} · {elapsed}\n{progress}\n\n{joke}"

    async def animate_download_status(
        self,
        bot,
        chat_id: int,
        status_message_id: int,
        status_key: str,
        started_at: float,
        initial_joke: str = None,
    ):
        """Refresh one Telegram message until the worker reports a terminal state."""
        active_joke = initial_joke or self.get_status_joke("queued")
        joke_state = "queued"
        last_joke_update = started_at

        while True:
            await asyncio.sleep(STATUS_POLL_INTERVAL_SECONDS)

            try:
                raw_state = await self.redis_client.get(status_key)
            except Exception as exc:
                logger.warning(f"Could not read download status {status_key}: {exc}")
                return

            if raw_state is None:
                return

            payload = self.parse_status_payload(raw_state)
            state = payload["state"]
            if state in TERMINAL_STATUS_STATES:
                return

            if state not in STATUS_MESSAGE_POOLS:
                state = "working"

            now = time.monotonic()
            if state != joke_state or now - last_joke_update >= JOKE_UPDATE_INTERVAL_SECONDS:
                active_joke = self.get_status_joke(state)
                joke_state = state
                last_joke_update = now

            message = self.build_status_message(
                state,
                now - started_at,
                payload=payload,
                joke=active_joke,
            )
            try:
                await bot.edit_message_text(
                    chat_id=chat_id,
                    message_id=status_message_id,
                    text=message,
                )
            except Exception as exc:
                logger.warning(f"Could not refresh Telegram status message: {exc}")

    async def start_command(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle /start command"""
        user_id = update.effective_user.id
        logger.info(f"Received /start from user {user_id}")

        if not self.check_user_permission(user_id):
            logger.warning(f"Unauthorized access attempt from user {user_id}")
            return

        welcome_message = """
🎬 Welcome to TeleVideo Converter Bot!

Send me a video URL and I'll download it for you.

📝 Commands:
/start - Show this message
/list - Show all saved videos
/search <keyword> - Search videos
/stats - Show usage statistics
/description - Toggle post description

Supported platforms: YouTube, Vimeo, and many more!
"""
        await update.message.reply_text(welcome_message)
        logger.info(f"User {user_id} started the bot")

    async def list_command(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle /list command"""
        user_id = update.effective_user.id

        if not self.check_user_permission(user_id):
            return

        videos = await self.db.get_all_videos(user_id)

        if not videos:
            await update.message.reply_text("📁 No videos found")
            return

        message = "📁 Your videos:\n\n"
        for video in videos:
            delete_time = datetime.fromtimestamp(video['delete_timestamp'])
            time_remaining = delete_time - datetime.now()

            hours = int(time_remaining.total_seconds() // 3600)
            minutes = int((time_remaining.total_seconds() % 3600) // 60)

            message += f"📹 {video['title'][:50]}\n"
            message += f"📦 {format_size(video['file_size'])}\n"
            message += f"🗑 Deletes in: {hours}h {minutes}m\n"
            message += f"📅 {datetime.fromtimestamp(video['download_timestamp']).strftime('%b %d, %I:%M %p')}\n\n"

        await update.message.reply_text(message)
        logger.info(f"User {user_id} listed videos")

    async def search_command(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle /search command"""
        user_id = update.effective_user.id

        if not self.check_user_permission(user_id):
            return

        if not context.args:
            await update.message.reply_text("❌ Please provide a search keyword\nUsage: /search <keyword>")
            return

        keyword = ' '.join(context.args)
        videos = await self.db.search_videos(user_id, keyword)

        if not videos:
            await update.message.reply_text(f"🔍 No videos found for: {keyword}")
            return

        message = f"🔍 Search results for '{keyword}':\n\n"
        for video in videos:
            delete_time = datetime.fromtimestamp(video['delete_timestamp'])
            time_remaining = delete_time - datetime.now()

            hours = int(time_remaining.total_seconds() // 3600)
            minutes = int((time_remaining.total_seconds() % 3600) // 60)

            message += f"📹 {video['title'][:50]}\n"
            message += f"📦 {format_size(video['file_size'])}\n"
            message += f"🗑 Deletes in: {hours}h {minutes}m\n\n"

        await update.message.reply_text(message)
        logger.info(f"User {user_id} searched for: {keyword}")

    async def stats_command(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle /stats command"""
        user_id = update.effective_user.id

        if not self.check_user_permission(user_id):
            return

        stats = await self.db.get_stats(user_id)

        used_gb = stats['used_space_bytes'] / (1024**3)
        free_gb = MAX_STORAGE_GB - used_gb

        message = f"""
📊 Usage Statistics

💾 Storage: {used_gb:.2f} GB / {MAX_STORAGE_GB} GB
📁 Free space: {free_gb:.2f} GB
📹 Total videos: {stats['total_videos']}
⬇️ Downloads (7 days): {stats['downloads_7d']}
⬇️ Downloads (30 days): {stats['downloads_30d']}
"""
        await update.message.reply_text(message)
        logger.info(f"User {user_id} checked stats")

    async def description_command(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle /description command - toggle post description"""
        user_id = update.effective_user.id

        if not self.check_user_permission(user_id):
            return

        # Get current setting (default is 1 = enabled)
        current_setting = await self.db.get_user_setting(user_id, 'send_description')
        if current_setting is None:
            current_setting = 1  # Default to enabled

        # Toggle the setting
        new_setting = 0 if current_setting == 1 else 1
        await self.db.set_user_setting(user_id, 'send_description', new_setting)

        status = "enabled ✅" if new_setting == 1 else "disabled ❌"
        message = f"📝 Post description is now {status}"

        await update.message.reply_text(message)
        logger.info(f"User {user_id} toggled description: {status}")

    async def handle_url(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle URL messages"""
        user_id = update.effective_user.id
        logger.info(f"Received message from user {user_id}")

        if not self.check_user_permission(user_id):
            logger.warning(f"Unauthorized message from user {user_id}")
            return

        url = update.message.text.strip()

        if not is_valid_url(url):
            await update.message.reply_text("❌ Invalid URL")
            return

        # Check rate limit
        if not self.check_rate_limit(user_id):
            await update.message.reply_text("⚠️ Rate limit exceeded. Maximum 10 downloads per hour.")
            return

        # Check storage space
        stats = await self.db.get_stats(user_id)
        used_gb = stats['used_space_bytes'] / (1024**3)
        if used_gb >= MAX_STORAGE_GB:
            await update.message.reply_text("❌ Storage limit reached (5GB). Please wait for old files to be deleted.")
            return

        # Send one status message that will be edited throughout the download.
        status_started_at = time.monotonic()
        initial_joke = self.get_status_joke("queued")
        status_message = await update.message.reply_text(
            self.build_status_message("queued", 0, joke=initial_joke)
        )
        status_key = f"download_status:{uuid.uuid4().hex}"

        # Create download task with best quality
        task = {
            'url': url,
            'quality': 'best',
            'user_id': user_id,
            'chat_id': update.message.chat_id,
            'message_id': update.message.message_id,
            'status_message_id': status_message.message_id,
            'status_key': status_key,
        }

        try:
            await self.redis_client.set(
                status_key,
                "queued",
                ex=DOWNLOAD_STATUS_TTL_SECONDS,
            )
            await self.redis_client.lpush('download_queue', json.dumps(task))
        except Exception as exc:
            logger.error(f"Could not queue download task: {exc}")
            try:
                await self.redis_client.set(status_key, "error", ex=300)
                await status_message.edit_text(
                    "❌ Пельменная очередь исчезла вместе с номерками.\n\n"
                    f"Техническая причина: {str(exc)[:800]}"
                )
            except Exception as status_exc:
                logger.warning(f"Could not report queue failure in Telegram: {status_exc}")
            return

        context.application.create_task(
            self.animate_download_status(
                context.bot,
                update.message.chat_id,
                status_message.message_id,
                status_key,
                status_started_at,
                initial_joke,
            ),
            update=update,
        )

        logger.info(f"Added download task to queue: {url} (best quality) for user {user_id}")

    async def handle_message(self, update: Update, context: ContextTypes.DEFAULT_TYPE):
        """Handle generic text messages"""
        text = update.message.text

        # Check if it's a URL
        if text.startswith('http://') or text.startswith('https://'):
            await self.handle_url(update, context)

def main():
    """Start the bot"""
    bot = TelegramBot()

    # Create application
    application = Application.builder().token(BOT_TOKEN).build()

    # Add handlers
    application.add_handler(CommandHandler("start", bot.start_command))
    application.add_handler(CommandHandler("list", bot.list_command))
    application.add_handler(CommandHandler("search", bot.search_command))
    application.add_handler(CommandHandler("stats", bot.stats_command))
    application.add_handler(CommandHandler("description", bot.description_command))
    application.add_handler(MessageHandler(filters.TEXT & ~filters.COMMAND, bot.handle_message))

    # Initialize bot asynchronously
    async def init_bot(app):
        await bot.initialize()
        logger.info("Starting Telegram bot...")

    # Run initialization
    application.post_init = init_bot

    # Start bot
    application.run_polling(allowed_updates=Update.ALL_TYPES)

if __name__ == '__main__':
    main()
