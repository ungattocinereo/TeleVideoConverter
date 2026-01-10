import os
import yt_dlp
import subprocess
from typing import Dict, Optional
from PIL import Image

class VideoDownloader:
    def __init__(self, storage_path: str, cookies_path: str = None):
        self.storage_path = storage_path
        self.cookies_path = cookies_path
        self.videos_path = os.path.join(storage_path, 'videos')
        self.thumbnails_path = os.path.join(storage_path, 'thumbnails')

        # Ensure directories exist
        os.makedirs(self.videos_path, exist_ok=True)
        os.makedirs(self.thumbnails_path, exist_ok=True)

    def _get_quality_format(self, quality: str) -> str:
        """Map quality string to yt-dlp format"""
        if quality == 'audio':
            return 'bestaudio/best'
        else:
            # Prefer MP4 container with compatible codecs for better Telegram compatibility
            # Fallback to best available if MP4 not available
            return 'bestvideo[ext=mp4]+bestaudio[ext=m4a]/bestvideo+bestaudio/best'

    def _reencode_video(self, input_path: str, video_id: str) -> str:
        """Force re-encode video with Telegram-compatible settings using ffmpeg"""
        output_path = os.path.join(self.videos_path, f"{video_id}_reencoded.mp4")

        # FFmpeg command with Telegram-compatible settings
        ffmpeg_cmd = [
            'ffmpeg', '-i', input_path,
            '-y',  # Overwrite output file
            # Video settings - H.264 Baseline for maximum iOS/macOS compatibility
            '-c:v', 'libx264',
            '-profile:v', 'baseline',
            '-level', '3.0',
            '-preset', 'medium',
            '-crf', '23',
            '-g', '50',  # Keyframe every 50 frames
            '-keyint_min', '25',
            # Audio settings
            '-c:a', 'aac',
            '-b:a', '128k',
            '-ar', '44100',
            # Format settings
            '-pix_fmt', 'yuv420p',
            '-vf', 'scale=trunc(iw/2)*2:trunc(ih/2)*2',
            # Container settings
            '-movflags', '+faststart',
            '-fflags', '+genpts',
            '-max_muxing_queue_size', '9999',
            output_path
        ]

        try:
            print(f"Re-encoding video for Telegram compatibility: {video_id}")
            result = subprocess.run(
                ffmpeg_cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=True
            )

            # Remove original file and rename re-encoded file
            if os.path.exists(output_path):
                os.remove(input_path)
                os.rename(output_path, input_path)
                print(f"Re-encoding successful: {video_id}")
                return input_path
            else:
                print(f"Re-encoding failed: output file not found")
                return input_path

        except subprocess.CalledProcessError as e:
            print(f"FFmpeg re-encoding error: {e.stderr.decode()}")
            # Return original file if re-encoding fails
            if os.path.exists(output_path):
                os.remove(output_path)
            return input_path
        except Exception as e:
            print(f"Re-encoding exception: {e}")
            return input_path

    def _get_cookie_file(self, url: str) -> Optional[str]:
        """Detect platform and return appropriate cookie file path"""
        if not self.cookies_path or not os.path.exists(self.cookies_path):
            return None

        # Platform detection based on URL
        platform_map = {
            'instagram.com': 'instagram.txt',
            'facebook.com': 'facebook.txt',
            'twitter.com': 'twitter.txt',
            'x.com': 'twitter.txt',
            'tiktok.com': 'tiktok.txt',
        }

        for domain, cookie_file in platform_map.items():
            if domain in url.lower():
                cookie_path = os.path.join(self.cookies_path, cookie_file)
                if os.path.exists(cookie_path):
                    print(f"Using cookie file for {domain}: {cookie_path}")
                    return cookie_path
                else:
                    print(f"Note: Cookie file not found for {domain}: {cookie_path}")

        return None

    async def download(self, url: str, quality: str) -> Dict:
        """Download video from URL"""
        try:
            is_audio_only = quality == 'audio'

            # Configure yt-dlp options
            ydl_opts = {
                'format': self._get_quality_format(quality),
                'outtmpl': os.path.join(self.videos_path, '%(id)s.%(ext)s'),
                'writethumbnail': True,
                'quiet': False,
                'no_warnings': False,
                'extract_flat': False,
                'writeinfojson': False,  # Don't write JSON file, just extract info
            }

            if is_audio_only:
                ydl_opts['postprocessors'] = [{
                    'key': 'FFmpegExtractAudio',
                    'preferredcodec': 'mp3',
                    'preferredquality': '192',
                }]
            else:
                # Just merge to MP4, we'll force re-encode manually after download
                ydl_opts['merge_output_format'] = 'mp4'

            # Add cookie support
            cookie_file = self._get_cookie_file(url)
            if cookie_file:
                ydl_opts['cookiefile'] = cookie_file

            # Download video
            with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                info = ydl.extract_info(url, download=True)

                # Get video information
                video_id = info['id']
                title = info['title']
                ext = 'mp3' if is_audio_only else 'mp4'
                file_path = os.path.join(self.videos_path, f"{video_id}.{ext}")

                # Get original quality
                original_quality = None
                if not is_audio_only and 'height' in info:
                    original_quality = f"{info['width']}x{info['height']} ({info['height']}p)"

                # FORCE re-encode video for Telegram compatibility
                # This ensures Instagram and other videos work correctly on iOS/macOS
                if not is_audio_only and os.path.exists(file_path):
                    file_path = self._reencode_video(file_path, video_id)

                # Get file size (after re-encoding)
                file_size = os.path.getsize(file_path) if os.path.exists(file_path) else 0

                # Process thumbnail
                thumbnail_path = None
                if not is_audio_only:
                    thumbnail_path = self._process_thumbnail(video_id, info)

                # Get source platform
                source_platform = info.get('extractor_key', 'Unknown')

                # Get codec info
                codec = None
                width = None
                height = None
                if not is_audio_only:
                    codec = info.get('vcodec', 'unknown')
                    width = info.get('width')
                    height = info.get('height')

                # Get description (try multiple fields for different platforms)
                # Instagram uses 'description', YouTube uses 'description', TikTok might use 'description' or 'title'
                description = (
                    info.get('description') or
                    info.get('caption') or
                    info.get('alt_title') or
                    ''
                )

                return {
                    'success': True,
                    'video_id': video_id,
                    'title': title,
                    'original_quality': original_quality,
                    'file_path': file_path,
                    'file_size': file_size,
                    'format': ext,
                    'codec': codec,
                    'source_platform': source_platform,
                    'thumbnail_path': thumbnail_path,
                    'width': width,
                    'height': height,
                    'description': description
                }

        except Exception as e:
            error_msg = str(e)

            # Provide helpful error messages for authentication issues
            if 'login' in error_msg.lower() or 'private' in error_msg.lower() or 'authentication' in error_msg.lower():
                if 'instagram.com' in url.lower():
                    error_msg = (
                        f"Instagram authentication required.\n\n"
                        f"Original error: {error_msg}\n\n"
                        f"To fix this:\n"
                        f"1. Export Instagram cookies using a browser extension\n"
                        f"2. Save as 'instagram.txt' in the cookies directory\n"
                        f"3. Run: docker-compose restart downloader\n\n"
                        f"See cookies/README.md for detailed instructions."
                    )

            return {
                'success': False,
                'error': error_msg
            }

    def _process_thumbnail(self, video_id: str, info: Dict) -> Optional[str]:
        """Process and save thumbnail preserving original aspect ratio"""
        try:
            # Find downloaded thumbnail
            thumbnail_extensions = ['.jpg', '.jpeg', '.png', '.webp']
            source_thumb = None

            for ext in thumbnail_extensions:
                potential_path = os.path.join(self.videos_path, f"{video_id}{ext}")
                if os.path.exists(potential_path):
                    source_thumb = potential_path
                    break

            if not source_thumb:
                return None

            # Convert and resize thumbnail
            output_path = os.path.join(self.thumbnails_path, f"{video_id}.jpg")

            with Image.open(source_thumb) as img:
                # Get original dimensions
                original_width, original_height = img.size

                # Calculate new dimensions preserving aspect ratio
                # Make the longest side 320 pixels maximum
                max_size = 320
                if original_width > original_height:
                    # Horizontal or square video
                    new_width = max_size
                    new_height = int((original_height / original_width) * max_size)
                else:
                    # Vertical video
                    new_height = max_size
                    new_width = int((original_width / original_height) * max_size)

                # Resize preserving aspect ratio
                img = img.resize((new_width, new_height), Image.Resampling.LANCZOS)

                # Convert to RGB if necessary
                if img.mode != 'RGB':
                    img = img.convert('RGB')

                # Save as JPEG
                img.save(output_path, 'JPEG', quality=85)

            # Remove original thumbnail
            if os.path.exists(source_thumb):
                os.remove(source_thumb)

            return output_path

        except Exception as e:
            print(f"Error processing thumbnail: {e}")
            return None
