import os
import logging
import asyncio
from datetime import datetime, timedelta
from telegram import Update
from telegram.ext import Application, CommandHandler, MessageHandler, filters, ContextTypes
import redis.asyncio as redis
import json
from database import Database
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

class TelegramBot:
    def __init__(self):
        self.db = Database(DATABASE_PATH)
        self.redis_client = None

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

        # Send processing message
        await update.message.reply_text("⏳ Downloading in best quality...")

        # Create download task with best quality
        task = {
            'url': url,
            'quality': 'best',
            'user_id': user_id,
            'chat_id': update.message.chat_id,
            'message_id': update.message.message_id
        }

        # Add task to Redis queue
        await self.redis_client.lpush('download_queue', json.dumps(task))

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
