import aiosqlite
import time
from typing import List, Dict

class Database:
    def __init__(self, db_path: str):
        self.db_path = db_path
        self.db = None

    async def initialize(self):
        """Initialize database connection"""
        self.db = await aiosqlite.connect(self.db_path)

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

    async def get_all_videos_sorted_by_age(self) -> List[Dict]:
        """Get all videos sorted by age (oldest first)"""
        cursor = await self.db.execute('''
            SELECT * FROM videos
            ORDER BY download_timestamp ASC
        ''')

        rows = await cursor.fetchall()
        columns = [description[0] for description in cursor.description]

        return [dict(zip(columns, row)) for row in rows]

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
