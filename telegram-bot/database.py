import aiosqlite
import time
import os
from typing import List, Dict, Optional

class Database:
    def __init__(self, db_path: str):
        self.db_path = db_path
        self.db = None

    async def initialize(self):
        """Initialize database and create tables"""
        self.db = await aiosqlite.connect(self.db_path)
        # Ensure database file has correct permissions
        try:
            os.chmod(self.db_path, 0o666)
            os.chmod(os.path.dirname(self.db_path), 0o777)
        except Exception:
            pass  # Ignore permission errors
        await self._create_tables()

    async def _create_tables(self):
        """Create database tables if they don't exist"""
        await self.db.execute('''
            CREATE TABLE IF NOT EXISTS videos (
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
        ''')

        await self.db.execute('''
            CREATE INDEX IF NOT EXISTS idx_delete_timestamp ON videos(delete_timestamp)
        ''')

        await self.db.execute('''
            CREATE INDEX IF NOT EXISTS idx_telegram_user_id ON videos(telegram_user_id)
        ''')

        await self.db.execute('''
            CREATE TABLE IF NOT EXISTS download_stats (
                id INTEGER PRIMARY KEY AUTOINCREMENT,
                telegram_user_id INTEGER NOT NULL,
                video_id TEXT NOT NULL,
                action TEXT NOT NULL,
                timestamp INTEGER NOT NULL,
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP
            )
        ''')

        await self.db.execute('''
            CREATE INDEX IF NOT EXISTS idx_stats_timestamp ON download_stats(timestamp)
        ''')

        await self.db.execute('''
            CREATE TABLE IF NOT EXISTS user_settings (
                telegram_user_id INTEGER PRIMARY KEY,
                send_description INTEGER DEFAULT 1,
                created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
                updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
            )
        ''')

        await self.db.commit()

    async def add_video(self, video_data: Dict) -> int:
        """Add a new video to database"""
        cursor = await self.db.execute('''
            INSERT INTO videos (
                video_id, telegram_user_id, original_url, title,
                original_quality, downloaded_quality, file_size,
                processing_time, format, codec, source_platform,
                file_path, thumbnail_path, download_timestamp,
                delete_timestamp
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ''', (
            video_data['video_id'],
            video_data['telegram_user_id'],
            video_data['original_url'],
            video_data['title'],
            video_data.get('original_quality'),
            video_data['downloaded_quality'],
            video_data['file_size'],
            video_data['processing_time'],
            video_data['format'],
            video_data.get('codec'),
            video_data.get('source_platform'),
            video_data['file_path'],
            video_data.get('thumbnail_path'),
            video_data['download_timestamp'],
            video_data['delete_timestamp']
        ))
        await self.db.commit()

        # Add download stat
        await self.add_stat(
            video_data['telegram_user_id'],
            video_data['video_id'],
            'download'
        )

        return cursor.lastrowid

    async def get_all_videos(self, user_id: int) -> List[Dict]:
        """Get all videos for a user"""
        cursor = await self.db.execute('''
            SELECT * FROM videos
            WHERE telegram_user_id = ?
            ORDER BY download_timestamp DESC
        ''', (user_id,))

        rows = await cursor.fetchall()
        columns = [description[0] for description in cursor.description]

        return [dict(zip(columns, row)) for row in rows]

    async def search_videos(self, user_id: int, keyword: str) -> List[Dict]:
        """Search videos by keyword"""
        cursor = await self.db.execute('''
            SELECT * FROM videos
            WHERE telegram_user_id = ?
            AND title LIKE ?
            ORDER BY download_timestamp DESC
        ''', (user_id, f'%{keyword}%'))

        rows = await cursor.fetchall()
        columns = [description[0] for description in cursor.description]

        return [dict(zip(columns, row)) for row in rows]

    async def get_video_by_id(self, video_id: str) -> Optional[Dict]:
        """Get video by video_id"""
        cursor = await self.db.execute('''
            SELECT * FROM videos WHERE video_id = ?
        ''', (video_id,))

        row = await cursor.fetchone()
        if not row:
            return None

        columns = [description[0] for description in cursor.description]
        return dict(zip(columns, row))

    async def delete_video(self, video_id: str) -> bool:
        """Delete video from database"""
        cursor = await self.db.execute('''
            SELECT telegram_user_id FROM videos WHERE video_id = ?
        ''', (video_id,))
        row = await cursor.fetchone()

        if not row:
            return False

        user_id = row[0]

        await self.db.execute('''
            DELETE FROM videos WHERE video_id = ?
        ''', (video_id,))
        await self.db.commit()

        # Add delete stat
        await self.add_stat(user_id, video_id, 'delete')

        return True

    async def get_stats(self, user_id: int) -> Dict:
        """Get usage statistics"""
        # Total videos and space used
        cursor = await self.db.execute('''
            SELECT COUNT(*), COALESCE(SUM(file_size), 0)
            FROM videos
            WHERE telegram_user_id = ?
        ''', (user_id,))
        row = await cursor.fetchone()
        total_videos, used_space = row

        # Downloads in last 7 days
        seven_days_ago = int(time.time()) - (7 * 24 * 60 * 60)
        cursor = await self.db.execute('''
            SELECT COUNT(*)
            FROM download_stats
            WHERE telegram_user_id = ?
            AND action = 'download'
            AND timestamp >= ?
        ''', (user_id, seven_days_ago))
        downloads_7d = (await cursor.fetchone())[0]

        # Downloads in last 30 days
        thirty_days_ago = int(time.time()) - (30 * 24 * 60 * 60)
        cursor = await self.db.execute('''
            SELECT COUNT(*)
            FROM download_stats
            WHERE telegram_user_id = ?
            AND action = 'download'
            AND timestamp >= ?
        ''', (user_id, thirty_days_ago))
        downloads_30d = (await cursor.fetchone())[0]

        return {
            'total_videos': total_videos,
            'used_space_bytes': used_space,
            'downloads_7d': downloads_7d,
            'downloads_30d': downloads_30d
        }

    async def add_stat(self, user_id: int, video_id: str, action: str):
        """Add a stat entry"""
        await self.db.execute('''
            INSERT INTO download_stats (telegram_user_id, video_id, action, timestamp)
            VALUES (?, ?, ?, ?)
        ''', (user_id, video_id, action, int(time.time())))
        await self.db.commit()

    async def get_expired_videos(self) -> List[Dict]:
        """Get videos that should be deleted"""
        current_time = int(time.time())
        cursor = await self.db.execute('''
            SELECT * FROM videos
            WHERE delete_timestamp <= ?
        ''', (current_time,))

        rows = await cursor.fetchall()
        columns = [description[0] for description in cursor.description]

        return [dict(zip(columns, row)) for row in rows]

    async def get_user_setting(self, user_id: int, setting_name: str) -> Optional[int]:
        """Get a user setting"""
        cursor = await self.db.execute(f'''
            SELECT {setting_name} FROM user_settings WHERE telegram_user_id = ?
        ''', (user_id,))
        row = await cursor.fetchone()
        return row[0] if row else None

    async def set_user_setting(self, user_id: int, setting_name: str, value: int):
        """Set a user setting (creates user if doesn't exist)"""
        # Try to insert, if user exists, update
        await self.db.execute(f'''
            INSERT INTO user_settings (telegram_user_id, {setting_name}, updated_at)
            VALUES (?, ?, CURRENT_TIMESTAMP)
            ON CONFLICT(telegram_user_id) DO UPDATE SET
                {setting_name} = excluded.{setting_name},
                updated_at = CURRENT_TIMESTAMP
        ''', (user_id, value))
        await self.db.commit()

    async def close(self):
        """Close database connection"""
        if self.db:
            await self.db.close()
