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
        final_path = os.path.join(self.videos_path, f"{video_id}.mp4")

        # Check file size before re-encoding
        file_size = os.path.getsize(input_path)
        file_size_mb = file_size / (1024 * 1024)

        # Skip re-encoding for files over 50MB (Telegram Bot API limit)
        # These will be sent via web interface link anyway
        if file_size_mb > 50:
            print(f"Skipping re-encode for large file ({file_size_mb:.1f}MB > 50MB): {video_id}")
            # Just ensure it has .mp4 extension
            if not input_path.endswith('.mp4'):
                os.rename(input_path, final_path)
                return final_path
            return input_path

        # Also skip re-encoding for very small files (< 1MB) - likely already optimized
        if file_size_mb < 1:
            print(f"Skipping re-encode for small file ({file_size_mb:.2f}MB): {video_id}")
            if not input_path.endswith('.mp4'):
                os.rename(input_path, final_path)
                return final_path
            return input_path

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
            print(f"Re-encoding video for Telegram compatibility: {video_id} ({file_size_mb:.1f}MB)")

            # Calculate timeout based on file size (rough estimate: 2 minutes per 10MB, min 60s, max 10 minutes)
            timeout_seconds = max(60, min(600, int(file_size_mb * 12)))
            print(f"Re-encoding timeout set to {timeout_seconds}s")

            result = subprocess.run(
                ffmpeg_cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                check=True,
                timeout=timeout_seconds  # Add timeout to prevent indefinite hanging
            )

            # Remove original file and rename re-encoded file to .mp4
            if os.path.exists(output_path):
                os.remove(input_path)
                # Always rename to .mp4 extension
                os.rename(output_path, final_path)
                print(f"Re-encoding successful: {video_id}")
                return final_path
            else:
                print(f"Re-encoding failed: output file not found")
                return input_path

        except subprocess.TimeoutExpired:
            print(f"FFmpeg re-encoding timeout after {timeout_seconds}s for {video_id} ({file_size_mb:.1f}MB)")
            # Clean up partial output file
            if os.path.exists(output_path):
                os.remove(output_path)
            # Return original file
            return input_path
        except subprocess.CalledProcessError as e:
            stderr_output = e.stderr.decode() if e.stderr else 'No stderr output'
            print(f"FFmpeg re-encoding error: {stderr_output[:500]}")  # Truncate long errors
            # Clean up partial output file
            if os.path.exists(output_path):
                os.remove(output_path)
            return input_path
        except Exception as e:
            print(f"Re-encoding exception: {e}")
            # Clean up partial output file
            if os.path.exists(output_path):
                os.remove(output_path)
            return input_path

    def _find_video_file(self, video_id: str) -> Optional[str]:
        """Find video file with any extension"""
        video_extensions = ['.mp4', '.webm', '.mkv', '.avi', '.mov', '.flv', '.m4v']
        for ext in video_extensions:
            potential_path = os.path.join(self.videos_path, f"{video_id}{ext}")
            if os.path.exists(potential_path):
                return potential_path
        return None

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
                # Network resilience settings for large files and slow connections
                'socket_timeout': 180,  # Socket timeout 3 minutes - very slow servers like 9gag
                'retries': 20,  # Retry failed downloads - increased to 20
                'fragment_retries': 30,  # Retry failed fragments - increased to 30 for problematic servers
                'extractor_retries': 5,  # Retry failed metadata extraction
                'file_access_retries': 5,  # Retry file access errors
                'http_chunk_size': 5242880,  # 5MB chunks - smaller chunks for unstable connections
                'nocheckcertificate': True,  # Ignore SSL certificate errors
                'retry_sleep_functions': {
                    'http': lambda n: min(60, 3 ** n),  # More aggressive backoff: 3s, 9s, 27s, 60s max
                    'fragment': lambda n: min(60, 3 ** n),  # More aggressive backoff
                    'file_access': lambda n: min(60, 3 ** n),  # More aggressive backoff
                    'extractor': lambda n: min(60, 3 ** n),  # More aggressive backoff
                },
                # Continue downloads even if errors occur
                'ignoreerrors': False,  # Don't ignore errors, but retry instead
                'no_color': True,  # Disable colored output for cleaner logs
                # Additional headers to avoid bot detection
                'http_headers': {
                    'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
                    'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8',
                    'Accept-Language': 'en-US,en;q=0.9',
                    'Accept-Encoding': 'gzip, deflate, br',
                    'DNT': '1',
                    'Connection': 'keep-alive',
                    'Upgrade-Insecure-Requests': '1',
                },
            }

            if is_audio_only:
                ydl_opts['postprocessors'] = [{
                    'key': 'FFmpegExtractAudio',
                    'preferredcodec': 'mp3',
                    'preferredquality': '192',
                }]
            else:
                # Merge to MP4 and recode non-mp4 formats (like webm from 9gag)
                ydl_opts['merge_output_format'] = 'mp4'
                ydl_opts['postprocessors'] = [{
                    'key': 'FFmpegVideoConvertor',
                    'preferedformat': 'mp4',
                }]

            # Add cookie support
            cookie_file = self._get_cookie_file(url)
            if cookie_file:
                ydl_opts['cookiefile'] = cookie_file

            # Add progress hook for better logging
            def progress_hook(d):
                if d['status'] == 'downloading':
                    if 'total_bytes' in d:
                        percent = (d['downloaded_bytes'] / d['total_bytes']) * 100
                        print(f"Download progress: {percent:.1f}% ({d['downloaded_bytes']}/{d['total_bytes']} bytes)")
                    elif 'downloaded_bytes' in d:
                        print(f"Downloaded: {d['downloaded_bytes']} bytes")
                elif d['status'] == 'finished':
                    print(f"Download finished, now post-processing...")
                elif d['status'] == 'error':
                    print(f"Download error occurred")

            ydl_opts['progress_hooks'] = [progress_hook]

            # Download video
            with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                info = ydl.extract_info(url, download=True)

                # Handle playlists (e.g. Twitter threads with multiple videos)
                # When yt-dlp returns a playlist, process the first video entry
                if info.get('_type') == 'playlist' or 'entries' in info:
                    entries = info.get('entries', [])
                    if entries:
                        # Use the first entry as the main video
                        info = entries[0]
                        print(f"Playlist detected with {len(entries)} entries, using first: {info.get('id')}")
                    else:
                        return {
                            'success': False,
                            'error': 'Playlist is empty — no downloadable videos found'
                        }

                # Get video information
                video_id = info['id']
                title = info['title']
                ext = 'mp3' if is_audio_only else 'mp4'
                file_path = os.path.join(self.videos_path, f"{video_id}.{ext}")

                # If expected file doesn't exist, search for file with any extension
                if not is_audio_only and not os.path.exists(file_path):
                    file_path = self._find_video_file(video_id)
                    if file_path:
                        print(f"Found video file: {file_path}")

                # Get original quality
                original_quality = None
                if not is_audio_only and 'height' in info:
                    original_quality = f"{info['width']}x{info['height']} ({info['height']}p)"

                # FORCE re-encode video for Telegram compatibility
                # This ensures Instagram and other videos work correctly on iOS/macOS
                # Also converts non-mp4 formats (webm, mkv) to mp4
                if not is_audio_only and file_path and os.path.exists(file_path):
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
            url_lower = url.lower()

            # Twitter/X requires authentication for all video content
            if ('twitter.com' in url_lower or 'x.com' in url_lower) and 'no video could be found' in error_msg.lower():
                cookie_file = os.path.join(self.cookies_path, 'twitter.txt') if self.cookies_path else None
                if not cookie_file or not os.path.exists(cookie_file):
                    error_msg = (
                        f"Twitter/X requires authentication to access videos.\n\n"
                        f"To fix this:\n"
                        f"1. Install 'Get cookies.txt LOCALLY' browser extension\n"
                        f"2. Log into x.com in your browser\n"
                        f"3. Export cookies and save as 'cookies/twitter.txt'\n"
                        f"4. Run: docker-compose restart downloader"
                    )
            elif 'login' in error_msg.lower() or 'private' in error_msg.lower() or 'authentication' in error_msg.lower():
                if 'instagram.com' in url_lower:
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
