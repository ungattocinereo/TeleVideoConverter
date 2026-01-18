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
        """Add a new video to database or update if exists"""
        # Check if video already exists
        cursor = await self.db.execute(
            'SELECT id FROM videos WHERE video_id = ?',
            (video_data['video_id'],)
        )
        existing = await cursor.fetchone()

        if existing:
            # Update existing video with new download info
            await self.db.execute('''
                UPDATE videos SET
                    telegram_user_id = ?,
                    original_url = ?,
                    title = ?,
                    original_quality = ?,
                    downloaded_quality = ?,
                    file_size = ?,
                    processing_time = ?,
                    format = ?,
                    codec = ?,
                    source_platform = ?,
                    file_path = ?,
                    thumbnail_path = ?,
                    download_timestamp = ?,
                    delete_timestamp = ?
                WHERE video_id = ?
            ''', (
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
                video_data['delete_timestamp'],
                video_data['video_id']
            ))
            await self.db.commit()
            video_id = existing[0]
        else:
            # Insert new video
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
            video_id = cursor.lastrowid

        # Add download stat
        await self.add_stat(
            video_data['telegram_user_id'],
            video_data['video_id'],
            'download'
        )

        return video_id

    async def add_stat(self, user_id: int, video_id: str, action: str):
        """Add a stat entry"""
        await self.db.execute('''
            INSERT INTO download_stats (telegram_user_id, video_id, action, timestamp)
            VALUES (?, ?, ?, ?)
        ''', (user_id, video_id, action, int(time.time())))
        await self.db.commit()

    async def get_user_setting(self, user_id: int, setting_name: str) -> Optional[int]:
        """Get a user setting"""
        cursor = await self.db.execute(f'''
            SELECT {setting_name} FROM user_settings WHERE telegram_user_id = ?
        ''', (user_id,))
        row = await cursor.fetchone()
        return row[0] if row else None

    async def close(self):
        """Close database connection"""
        if self.db:
            await self.db.close()
