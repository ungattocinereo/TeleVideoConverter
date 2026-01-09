import os
import logging
import asyncio
import time
from pathlib import Path
from database import Database

# Configure logging
logging.basicConfig(
    format='[%(asctime)s] [%(levelname)s] [cleanup] %(message)s',
    level=getattr(logging, os.getenv('LOG_LEVEL', 'INFO'))
)
logger = logging.getLogger(__name__)

# Environment variables
DATABASE_PATH = os.getenv('DATABASE_PATH', '/db/televideo.db')
STORAGE_PATH = os.getenv('STORAGE_PATH', '/storage')
MAX_STORAGE_GB = float(os.getenv('MAX_STORAGE_GB', 5))

class CleanupService:
    def __init__(self):
        self.db = Database(DATABASE_PATH)
        self.storage_path = STORAGE_PATH
        self.max_storage_bytes = MAX_STORAGE_GB * 1024 * 1024 * 1024

    async def initialize(self):
        """Initialize database connection"""
        await self.db.initialize()
        logger.info("Cleanup service initialized successfully")

    async def delete_video_files(self, video_id: str, file_path: str, thumbnail_path: str = None):
        """Delete video and thumbnail files"""
        try:
            # Delete video file
            if os.path.exists(file_path):
                os.remove(file_path)
                logger.info(f"Deleted video file: {file_path}")

            # Delete thumbnail
            if thumbnail_path and os.path.exists(thumbnail_path):
                os.remove(thumbnail_path)
                logger.info(f"Deleted thumbnail: {thumbnail_path}")

        except Exception as e:
            logger.error(f"Error deleting files for {video_id}: {e}")

    async def cleanup_expired_videos(self):
        """Delete videos that have exceeded retention period"""
        try:
            expired_videos = await self.db.get_expired_videos()

            if not expired_videos:
                logger.info("No expired videos to clean up")
                return

            logger.info(f"Found {len(expired_videos)} expired videos")

            for video in expired_videos:
                video_id = video['video_id']
                file_path = video['file_path']
                thumbnail_path = video.get('thumbnail_path')

                # Delete files
                await self.delete_video_files(video_id, file_path, thumbnail_path)

                # Delete from database
                await self.db.delete_video(video_id)

                logger.info(f"Cleaned up expired video: {video['title']} ({video_id})")

        except Exception as e:
            logger.error(f"Error during cleanup of expired videos: {e}")

    async def get_total_storage_used(self) -> int:
        """Calculate total storage used by videos"""
        total_size = 0

        videos_dir = os.path.join(self.storage_path, 'videos')
        thumbnails_dir = os.path.join(self.storage_path, 'thumbnails')

        # Calculate videos directory size
        if os.path.exists(videos_dir):
            for file in Path(videos_dir).rglob('*'):
                if file.is_file():
                    total_size += file.stat().st_size

        # Calculate thumbnails directory size
        if os.path.exists(thumbnails_dir):
            for file in Path(thumbnails_dir).rglob('*'):
                if file.is_file():
                    total_size += file.stat().st_size

        return total_size

    async def cleanup_excess_storage(self):
        """Delete oldest videos if storage exceeds limit"""
        try:
            total_used = await self.get_total_storage_used()
            used_gb = total_used / (1024 * 1024 * 1024)

            if total_used <= self.max_storage_bytes:
                logger.info(f"Storage usage OK: {used_gb:.2f} GB / {MAX_STORAGE_GB} GB")
                return

            logger.warning(f"Storage limit exceeded: {used_gb:.2f} GB / {MAX_STORAGE_GB} GB")

            # Get all videos sorted by age (oldest first)
            all_videos = await self.db.get_all_videos_sorted_by_age()

            if not all_videos:
                logger.warning("No videos in database but storage is full. Manual cleanup may be needed.")
                return

            target_size = self.max_storage_bytes * 0.9  # Clean up to 90% of limit
            freed_space = 0

            for video in all_videos:
                if total_used - freed_space <= target_size:
                    break

                video_id = video['video_id']
                file_path = video['file_path']
                thumbnail_path = video.get('thumbnail_path')

                # Get file size before deletion
                file_size = 0
                if os.path.exists(file_path):
                    file_size = os.path.getsize(file_path)

                # Delete files
                await self.delete_video_files(video_id, file_path, thumbnail_path)

                # Delete from database
                await self.db.delete_video(video_id)

                freed_space += file_size

                logger.info(f"Deleted old video to free space: {video['title']} ({self._format_size(file_size)})")

            logger.info(f"Freed {self._format_size(freed_space)} of storage")

        except Exception as e:
            logger.error(f"Error during storage cleanup: {e}")

    async def run_cleanup_cycle(self):
        """Run a complete cleanup cycle"""
        logger.info("Starting cleanup cycle...")

        # Clean up expired videos
        await self.cleanup_expired_videos()

        # Check and clean up excess storage
        await self.cleanup_excess_storage()

        logger.info("Cleanup cycle completed")

    async def run(self):
        """Main cleanup loop - runs every hour"""
        logger.info("Cleanup service started")

        while True:
            try:
                await self.run_cleanup_cycle()

                # Sleep for 1 hour
                await asyncio.sleep(3600)

            except Exception as e:
                logger.error(f"Error in cleanup loop: {e}")
                await asyncio.sleep(300)  # Sleep 5 minutes on error

    def _format_size(self, bytes_size: int) -> str:
        """Format bytes to human readable size"""
        for unit in ['B', 'KB', 'MB', 'GB']:
            if bytes_size < 1024.0:
                return f"{bytes_size:.1f} {unit}"
            bytes_size /= 1024.0
        return f"{bytes_size:.1f} TB"

async def main():
    """Start the cleanup service"""
    service = CleanupService()
    await service.initialize()
    await service.run()

if __name__ == '__main__':
    asyncio.run(main())
