import os
import logging
from fastapi import FastAPI, WebSocket, WebSocketDisconnect, HTTPException
from fastapi.responses import FileResponse, StreamingResponse
from fastapi.middleware.cors import CORSMiddleware
from pydantic import BaseModel
from pathlib import Path
from datetime import datetime
from database import Database
from typing import List
import asyncio
import json
import redis.asyncio as redis

# Configure logging
logging.basicConfig(
    format='[%(asctime)s] [%(levelname)s] [web-api] %(message)s',
    level=getattr(logging, os.getenv('LOG_LEVEL', 'INFO'))
)
logger = logging.getLogger(__name__)

# Environment variables
DATABASE_PATH = os.getenv('DATABASE_PATH', '/db/televideo.db')
STORAGE_PATH = os.getenv('STORAGE_PATH', '/storage')
MAX_STORAGE_GB = float(os.getenv('MAX_STORAGE_GB', 5))
API_PORT = int(os.getenv('API_PORT', 3001))
API_HOST = os.getenv('API_HOST', '0.0.0.0')
REDIS_HOST = os.getenv('REDIS_HOST', 'redis')
REDIS_PORT = int(os.getenv('REDIS_PORT', 6379))

# Pydantic models
class UrlSubmission(BaseModel):
    url: str
    quality: str

app = FastAPI(title="TeleVideo Converter API")

# CORS configuration
app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

# Database
db = Database(DATABASE_PATH)

# Redis client
redis_client = None

# WebSocket connections
websocket_connections: List[WebSocket] = []

@app.on_event("startup")
async def startup():
    """Initialize database and Redis on startup"""
    global redis_client
    await db.initialize()
    redis_client = await redis.from_url(f'redis://{REDIS_HOST}:{REDIS_PORT}')
    logger.info("Web API started successfully")

@app.on_event("shutdown")
async def shutdown():
    """Close database and Redis on shutdown"""
    await db.close()
    if redis_client:
        await redis_client.close()
    logger.info("Web API shutdown")

@app.get("/")
async def root():
    """API root endpoint"""
    return {"message": "TeleVideo Converter API", "version": "1.0.0"}

@app.post("/api/submit-url")
async def submit_url(submission: UrlSubmission):
    """Submit a video URL for download"""
    try:
        # Validate URL
        if not submission.url.startswith('http://') and not submission.url.startswith('https://'):
            raise HTTPException(status_code=400, detail="Invalid URL format")

        # Validate quality
        valid_qualities = ['720p', '1080p', '4K', 'Audio Only']
        if submission.quality not in valid_qualities:
            raise HTTPException(status_code=400, detail="Invalid quality option")

        # Create download task
        task = {
            'url': submission.url,
            'quality': submission.quality,
            'user_id': 0,  # Web submissions have user_id 0
            'chat_id': 0   # Web submissions have chat_id 0
        }

        # Push to Redis queue
        await redis_client.rpush('download_queue', json.dumps(task))

        logger.info(f"Queued download from web: {submission.url} ({submission.quality})")

        return {
            "success": True,
            "message": "Download queued successfully"
        }

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error submitting URL: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/videos")
async def get_videos():
    """Get all videos"""
    try:
        videos = await db.get_all_videos()

        # Add time_remaining field
        current_time = datetime.now().timestamp()
        for video in videos:
            time_remaining_seconds = video['delete_timestamp'] - current_time
            video['time_remaining'] = format_time_remaining(int(time_remaining_seconds))
            video['thumbnail_url'] = f"/api/thumbnails/{video['video_id']}.jpg" if video.get('thumbnail_path') else None

        return videos

    except Exception as e:
        logger.error(f"Error getting videos: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/videos/{video_id}")
async def get_video(video_id: str):
    """Get video by ID"""
    try:
        video = await db.get_video_by_id(video_id)

        if not video:
            raise HTTPException(status_code=404, detail="Video not found")

        # Add time_remaining field
        current_time = datetime.now().timestamp()
        time_remaining_seconds = video['delete_timestamp'] - current_time
        video['time_remaining'] = format_time_remaining(int(time_remaining_seconds))
        video['thumbnail_url'] = f"/api/thumbnails/{video['video_id']}.jpg" if video.get('thumbnail_path') else None

        return video

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error getting video {video_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/videos/{video_id}/download")
async def download_video(video_id: str):
    """Download video file"""
    try:
        video = await db.get_video_by_id(video_id)

        if not video:
            raise HTTPException(status_code=404, detail="Video not found")

        file_path = video['file_path']

        if not os.path.exists(file_path):
            raise HTTPException(status_code=404, detail="Video file not found")

        # Determine filename
        ext = os.path.splitext(file_path)[1]
        filename = f"{video['title']}{ext}"

        # Stream the file
        return FileResponse(
            path=file_path,
            filename=filename,
            media_type='application/octet-stream'
        )

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error downloading video {video_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.delete("/api/videos/{video_id}")
async def delete_video(video_id: str):
    """Delete video"""
    try:
        video = await db.get_video_by_id(video_id)

        if not video:
            raise HTTPException(status_code=404, detail="Video not found")

        # Delete files
        file_path = video['file_path']
        thumbnail_path = video.get('thumbnail_path')

        if os.path.exists(file_path):
            os.remove(file_path)

        if thumbnail_path and os.path.exists(thumbnail_path):
            os.remove(thumbnail_path)

        # Delete from database
        await db.delete_video(video_id)

        # Notify WebSocket clients
        await broadcast_websocket({
            "type": "video_deleted",
            "data": {"video_id": video_id}
        })

        logger.info(f"Deleted video: {video_id}")

        return {"success": True}

    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Error deleting video {video_id}: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/stats")
async def get_stats():
    """Get usage statistics"""
    try:
        stats = await db.get_stats()

        used_gb = stats['used_space_bytes'] / (1024 ** 3)
        free_gb = MAX_STORAGE_GB - used_gb

        return {
            "total_videos": stats['total_videos'],
            "used_space_bytes": stats['used_space_bytes'],
            "used_space_gb": round(used_gb, 2),
            "max_space_gb": MAX_STORAGE_GB,
            "free_space_gb": round(free_gb, 2),
            "downloads_7d": stats['downloads_7d'],
            "downloads_30d": stats['downloads_30d']
        }

    except Exception as e:
        logger.error(f"Error getting stats: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/search")
async def search_videos(q: str):
    """Search videos"""
    try:
        videos = await db.search_videos(q)

        # Add time_remaining field
        current_time = datetime.now().timestamp()
        for video in videos:
            time_remaining_seconds = video['delete_timestamp'] - current_time
            video['time_remaining'] = format_time_remaining(int(time_remaining_seconds))
            video['thumbnail_url'] = f"/api/thumbnails/{video['video_id']}.jpg" if video.get('thumbnail_path') else None

        return videos

    except Exception as e:
        logger.error(f"Error searching videos: {e}")
        raise HTTPException(status_code=500, detail=str(e))

@app.get("/api/thumbnails/{filename}")
async def get_thumbnail(filename: str):
    """Get thumbnail image"""
    thumbnail_path = os.path.join(STORAGE_PATH, 'thumbnails', filename)

    if not os.path.exists(thumbnail_path):
        raise HTTPException(status_code=404, detail="Thumbnail not found")

    return FileResponse(thumbnail_path, media_type='image/jpeg')

@app.websocket("/ws")
async def websocket_endpoint(websocket: WebSocket):
    """WebSocket endpoint for real-time updates"""
    await websocket.accept()
    websocket_connections.append(websocket)

    logger.info(f"WebSocket connected. Total connections: {len(websocket_connections)}")

    try:
        while True:
            # Keep connection alive
            await websocket.receive_text()

    except WebSocketDisconnect:
        websocket_connections.remove(websocket)
        logger.info(f"WebSocket disconnected. Total connections: {len(websocket_connections)}")

async def broadcast_websocket(message: dict):
    """Broadcast message to all WebSocket clients"""
    if not websocket_connections:
        return

    message_json = json.dumps(message)
    disconnected = []

    for websocket in websocket_connections:
        try:
            await websocket.send_text(message_json)
        except:
            disconnected.append(websocket)

    # Remove disconnected clients
    for websocket in disconnected:
        websocket_connections.remove(websocket)

def format_time_remaining(seconds: int) -> str:
    """Format seconds to human readable time remaining"""
    if seconds < 0:
        return "Expired"

    if seconds < 3600:
        minutes = seconds // 60
        return f"{minutes}m"
    elif seconds < 86400:
        hours = seconds // 3600
        minutes = (seconds % 3600) // 60
        return f"{hours}h {minutes}m"
    else:
        days = seconds // 86400
        hours = (seconds % 86400) // 3600
        return f"{days}d {hours}h"

if __name__ == "__main__":
    import uvicorn
    uvicorn.run(app, host=API_HOST, port=API_PORT)
