import aiosqlite
import time
import os
from typing import List, Dict, Optional

class Database:
    def __init__(self, db_path: str):
        self.db_path = db_path
        self.db = None

    async def initialize(self):
        """Initialize database connection"""
        self.db = await aiosqlite.connect(self.db_path)
        # Ensure database file has correct permissions
        try:
            os.chmod(self.db_path, 0o666)
            os.chmod(os.path.dirname(self.db_path), 0o777)
        except Exception:
            pass  # Ignore permission errors

    async def get_all_videos(self) -> List[Dict]:
        """Get all videos"""
        cursor = await self.db.execute('''
            SELECT * FROM videos
            ORDER BY download_timestamp DESC
        ''')

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

    async def search_videos(self, keyword: str) -> List[Dict]:
        """Search videos by keyword"""
        cursor = await self.db.execute('''
            SELECT * FROM videos
            WHERE title LIKE ?
            ORDER BY download_timestamp DESC
        ''', (f'%{keyword}%',))

        rows = await cursor.fetchall()
        columns = [description[0] for description in cursor.description]

        return [dict(zip(columns, row)) for row in rows]

    async def get_stats(self) -> Dict:
        """Get usage statistics"""
        # Total videos and space used
        cursor = await self.db.execute('''
            SELECT COUNT(*), COALESCE(SUM(file_size), 0)
            FROM videos
        ''')
        row = await cursor.fetchone()
        total_videos, used_space = row

        # Downloads in last 7 days
        seven_days_ago = int(time.time()) - (7 * 24 * 60 * 60)
        cursor = await self.db.execute('''
            SELECT COUNT(*)
            FROM download_stats
            WHERE action = 'download'
            AND timestamp >= ?
        ''', (seven_days_ago,))
        downloads_7d = (await cursor.fetchone())[0]

        # Downloads in last 30 days
        thirty_days_ago = int(time.time()) - (30 * 24 * 60 * 60)
        cursor = await self.db.execute('''
            SELECT COUNT(*)
            FROM download_stats
            WHERE action = 'download'
            AND timestamp >= ?
        ''', (thirty_days_ago,))
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

    async def close(self):
        """Close database connection"""
        if self.db:
            await self.db.close()
