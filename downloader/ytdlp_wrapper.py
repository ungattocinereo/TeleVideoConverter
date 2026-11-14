import html
import json
import os
import re
import time
import yt_dlp
import subprocess
import tempfile
import requests
from typing import Callable, Dict, Optional
from urllib.parse import urlparse
from PIL import Image

class VideoDownloader:
    THREADS_ENCODING_CHARS = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_'
    THREADS_GRAPHQL_DOC_ID = '26742885138663415'
    THREADS_GRAPHQL_FRIENDLY_NAME = 'BarcelonaPermalinkMobilePostColumnPageQuery'
    THREADS_GRAPHQL_BOOL_PROVIDERS = (
        'BarcelonaHasDearAlgoConsumption',
        'BarcelonaIsLoggedIn',
        'BarcelonaHasEventBadge',
        'BarcelonaThreadsWebCachingImprovements',
        'BarcelonaIsSearchDiscoveryEnabled',
        'BarcelonaHasCommunities',
        'BarcelonaHasGameScoreShare',
        'BarcelonaHasPublicViewCountCard',
        'BarcelonaHasScorecardCommunity',
        'BarcelonaHasMusic',
        'BarcelonaHasGhostPostEmojiActivation',
        'BarcelonaOptionalCookiesEnabled',
        'BarcelonaHasDearAlgoWebProduction',
        'BarcelonaIsCrawler',
        'BarcelonaHasDisplayNames',
        'BarcelonaHasCommunityTopContributors',
        'BarcelonaCanSeeSponsoredContent',
        'BarcelonaShouldShowFediverseM075Features',
        'BarcelonaIsInternalUser',
    )

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

        # Skip re-encoding for very small files (< 1MB) - likely already optimized
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

            # Calculate timeout based on file size (rough estimate: 2 minutes per 10MB, min 60s, max 1 hour)
            timeout_seconds = max(60, min(3600, int(file_size_mb * 12)))
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
            'fb.watch': 'facebook.txt',
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

    def _is_facebook_url(self, url: str) -> bool:
        url_lower = url.lower()
        return 'facebook.com' in url_lower or 'fb.watch' in url_lower

    def _is_tiktok_url(self, url: str) -> bool:
        return 'tiktok.com' in url.lower()

    def _is_threads_url(self, url: str) -> bool:
        hostname = (urlparse(url).hostname or '').lower()
        return any(
            hostname == domain or hostname.endswith(f'.{domain}')
            for domain in ('threads.com', 'threads.net')
        )

    def _decode_threads_shortcode(self, shortcode: str) -> int:
        """Convert a Threads shortcode to the numeric post ID used by its API."""
        post_id = 0
        for character in shortcode:
            try:
                value = self.THREADS_ENCODING_CHARS.index(character)
            except ValueError as error:
                raise ValueError(f'Invalid Threads shortcode: {shortcode}') from error
            post_id = post_id * len(self.THREADS_ENCODING_CHARS) + value
        return post_id

    def _resolve_threads_post(self, url: str) -> tuple[str, str]:
        """Resolve Threads share links and return the canonical URL and shortcode."""
        match = re.search(r'/(?:post|t)/([A-Za-z0-9_-]+)', url)
        if match:
            return url, match.group(1)

        response = requests.get(
            url,
            allow_redirects=True,
            timeout=30,
        )
        response.raise_for_status()

        canonical_url = response.url
        match = re.search(r'/(?:post|t)/([A-Za-z0-9_-]+)', canonical_url)
        if not match:
            raise ValueError('Unable to resolve Threads post shortcode from share URL')
        return canonical_url, match.group(1)

    def _get_threads_media(self, canonical_url: str, shortcode: str) -> Dict:
        """Fetch public Threads post metadata from the same API used by its web app."""
        variables = {
            'postID': str(self._decode_threads_shortcode(shortcode)),
            **{
                f'__relay_internal__pv__{provider}relayprovider': False
                for provider in self.THREADS_GRAPHQL_BOOL_PROVIDERS
            },
        }
        headers = {
            'User-Agent': 'Mozilla/5.0',
            'X-FB-Friendly-Name': self.THREADS_GRAPHQL_FRIENDLY_NAME,
            'X-FB-LSD': '1',
            'X-IG-App-ID': '238260118697367',
        }
        response = requests.post(
            'https://www.threads.com/api/graphql',
            data={
                'lsd': '1',
                'variables': json.dumps(variables, separators=(',', ':')),
                'doc_id': self.THREADS_GRAPHQL_DOC_ID,
            },
            headers=headers,
            timeout=30,
        )
        response.raise_for_status()
        payload = response.json()
        media = payload.get('data', {}).get('media')
        if not media:
            api_errors = payload.get('errors') or []
            api_message = api_errors[0].get('message') if api_errors else None
            raise ValueError(api_message or 'Threads API returned no post media')
        return media

    def _extract_threads_info(self, url: str, ydl_opts: Dict) -> Dict:
        """Build a yt-dlp info dictionary for Threads and download its first video."""
        canonical_url, shortcode = self._resolve_threads_post(url)
        media = self._get_threads_media(canonical_url, shortcode)
        media_items = media.get('carousel_media') or [media]
        video_media = next(
            (item for item in media_items if item.get('video_versions')),
            None,
        )
        if not video_media:
            raise ValueError('This Threads post contains no downloadable video')

        user = media.get('user') or {}
        uploader = user.get('username')
        caption = (media.get('caption') or {}).get('text')
        title = caption or f'Threads video by {uploader or "unknown user"}'
        thumbnail_candidates = (
            (video_media.get('image_versions2') or {}).get('candidates') or []
        )
        formats = [
            {
                'format_id': str(video.get('type')) if video.get('type') is not None else None,
                'url': video.get('url'),
                'width': video_media.get('original_width'),
                'height': video_media.get('original_height'),
                'http_headers': {'Referer': 'https://www.threads.com/'},
            }
            for video in video_media['video_versions']
            if video.get('url')
        ]
        if not formats:
            raise ValueError('Threads API returned no downloadable video formats')

        info = {
            'id': shortcode,
            'title': title,
            'description': caption or '',
            'webpage_url': canonical_url,
            'extractor': 'Threads',
            'extractor_key': 'Threads',
            'formats': formats,
            'thumbnail': thumbnail_candidates[0].get('url') if thumbnail_candidates else None,
            'uploader': uploader,
            'uploader_id': user.get('pk') or user.get('id'),
            'timestamp': media.get('taken_at'),
            'like_count': media.get('like_count'),
            'comment_count': (media.get('text_post_app_info') or {}).get('direct_reply_count'),
        }

        print(f'Downloading Threads post {shortcode} through the Threads web API')
        with yt_dlp.YoutubeDL(ydl_opts) as ydl:
            return ydl.process_ie_result(info, download=True)

    def _resolve_tiktok_post(self, url: str) -> tuple[str, str, str]:
        """Resolve TikTok short URLs and return post type, ID, and canonical URL."""
        post_match = re.search(r'/(video|photo)/(\d+)', url)
        if post_match:
            return post_match.group(1), post_match.group(2), url

        with requests.get(
            url,
            allow_redirects=True,
            headers=self._default_http_headers(),
            stream=True,
            timeout=30,
        ) as response:
            redirect_urls = [item.url for item in response.history]
            redirect_urls.append(response.url)

            for redirect_url in redirect_urls:
                post_match = re.search(r'/(video|photo)/(\d+)', redirect_url)
                if post_match:
                    return post_match.group(1), post_match.group(2), redirect_url

            response.raise_for_status()

        raise ValueError('Unable to resolve TikTok post ID from URL')

    def _resolve_tiktok_video(self, url: str) -> tuple[str, str]:
        """Resolve TikTok short URLs and return the video ID and canonical URL."""
        post_type, post_id, canonical_url = self._resolve_tiktok_post(url)
        if post_type != 'video':
            raise ValueError('TikTok URL points to a photo post, not a video')
        return post_id, canonical_url

    def _get_tiktok_embed_video_data(self, post_id: str) -> Dict:
        """Read public post data from TikTok's official embed page."""
        embed_url = f'https://www.tiktok.com/embed/v2/{post_id}'
        response = requests.get(
            embed_url,
            headers=self._default_http_headers(),
            timeout=30,
        )
        response.raise_for_status()

        state_match = re.search(
            r'<script[^>]+id=["\']__FRONTITY_CONNECT_STATE__["\'][^>]*>(.*?)</script>',
            response.text,
            re.DOTALL,
        )
        if not state_match:
            raise ValueError('TikTok embed page returned no post data')

        state = json.loads(html.unescape(state_match.group(1)))
        source_data = (state.get('source') or {}).get('data') or {}
        page_data = source_data.get(f'/embed/v2/{post_id}') or {}
        video_data = page_data.get('videoData')
        if not video_data:
            raise ValueError('TikTok embed page returned no video data')
        return video_data

    def _download_url_candidates(
        self,
        urls: list[str],
        destination: str,
        referer: str,
    ) -> None:
        """Download the first working TikTok CDN URL from a candidate list."""
        headers = {
            'User-Agent': self._default_http_headers()['User-Agent'],
            'Referer': referer,
        }
        last_error = None
        for candidate in urls:
            if not candidate:
                continue
            try:
                with requests.get(
                    candidate,
                    headers=headers,
                    stream=True,
                    timeout=60,
                ) as response:
                    response.raise_for_status()
                    with open(destination, 'wb') as output_file:
                        for chunk in response.iter_content(chunk_size=1024 * 256):
                            if chunk:
                                output_file.write(chunk)
                return
            except Exception as error:
                last_error = error

        raise RuntimeError(f'All TikTok CDN URLs failed: {last_error}')

    def _create_tiktok_audio(
        self,
        post_id: str,
        music_urls: list[str],
        referer: str,
    ) -> str:
        """Download a TikTok photo post's soundtrack as MP3."""
        if not music_urls:
            raise ValueError('TikTok photo post has no downloadable soundtrack')

        final_path = os.path.join(self.videos_path, f'{post_id}.mp3')
        with tempfile.TemporaryDirectory(
            prefix=f'tiktok-{post_id}-',
            dir=self.videos_path,
        ) as temp_dir:
            source_path = os.path.join(temp_dir, 'soundtrack')
            self._download_url_candidates(music_urls, source_path, referer)
            result = subprocess.run(
                [
                    'ffmpeg', '-y', '-i', source_path,
                    '-vn', '-c:a', 'libmp3lame', '-b:a', '192k',
                    final_path,
                ],
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=300,
            )
            if result.returncode != 0 or not os.path.exists(final_path):
                error = result.stderr.decode(errors='replace')[-1000:]
                raise RuntimeError(f'Could not convert TikTok soundtrack: {error}')
        return final_path

    def _create_tiktok_slideshow(
        self,
        post_id: str,
        images: list[Dict],
        music_urls: list[str],
        referer: str,
    ) -> tuple[str, int, int]:
        """Render a TikTok photo post to a Telegram-compatible MP4."""
        final_path = os.path.join(self.videos_path, f'{post_id}.mp4')
        slide_seconds = 2.5

        portrait_count = sum(
            1 for image in images
            if (image.get('height') or 0) > (image.get('width') or 0)
        )
        landscape_count = sum(
            1 for image in images
            if (image.get('width') or 0) > (image.get('height') or 0)
        )
        if portrait_count > landscape_count:
            width, height = 720, 1280
        elif landscape_count > portrait_count:
            width, height = 1280, 720
        else:
            width, height = 720, 720

        with tempfile.TemporaryDirectory(
            prefix=f'tiktok-{post_id}-',
            dir=self.videos_path,
        ) as temp_dir:
            slide_paths = []
            for index, image_data in enumerate(images):
                image_urls = image_data.get('urlList') or []
                slide_path = os.path.join(temp_dir, f'slide-{index:03d}.jpg')
                try:
                    self._download_url_candidates(image_urls, slide_path, referer)
                    slide_paths.append(slide_path)
                except Exception as error:
                    print(f'TikTok slide {index + 1} could not be downloaded: {error}')

            if not slide_paths:
                raise ValueError('TikTok photo post has no downloadable images')

            thumbnail_path = os.path.join(self.videos_path, f'{post_id}.jpg')
            with Image.open(slide_paths[0]) as thumbnail:
                if thumbnail.mode != 'RGB':
                    thumbnail = thumbnail.convert('RGB')
                thumbnail.save(thumbnail_path, 'JPEG', quality=90)

            concat_path = os.path.join(temp_dir, 'slides.txt')
            with open(concat_path, 'w', encoding='utf-8') as concat_file:
                for slide_path in slide_paths:
                    concat_file.write(f"file '{slide_path}'\n")
                    concat_file.write(f'duration {slide_seconds}\n')
                concat_file.write(f"file '{slide_paths[-1]}'\n")

            soundtrack_path = None
            if music_urls:
                soundtrack_path = os.path.join(temp_dir, 'soundtrack')
                try:
                    self._download_url_candidates(music_urls, soundtrack_path, referer)
                except Exception as error:
                    print(f'TikTok soundtrack unavailable; rendering silently: {error}')
                    soundtrack_path = None

            ffmpeg_cmd = [
                'ffmpeg', '-y',
                '-f', 'concat', '-safe', '0', '-i', concat_path,
            ]
            if soundtrack_path:
                ffmpeg_cmd.extend(['-i', soundtrack_path])
            ffmpeg_cmd.extend([
                '-vf', (
                    f'scale={width}:{height}:force_original_aspect_ratio=decrease,'
                    f'pad={width}:{height}:(ow-iw)/2:(oh-ih)/2,'
                    'fps=30,format=yuv420p'
                ),
                '-c:v', 'libx264',
                '-profile:v', 'baseline',
                '-level', '3.0',
                '-preset', 'medium',
                '-crf', '23',
                '-g', '50',
                '-keyint_min', '25',
            ])
            if soundtrack_path:
                ffmpeg_cmd.extend([
                    '-c:a', 'aac', '-b:a', '128k', '-ar', '44100',
                ])
            ffmpeg_cmd.extend([
                '-t', str(len(slide_paths) * slide_seconds),
                '-movflags', '+faststart',
                final_path,
            ])

            result = subprocess.run(
                ffmpeg_cmd,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
                timeout=600,
            )
            if result.returncode != 0 or not os.path.exists(final_path):
                error = result.stderr.decode(errors='replace')[-1000:]
                raise RuntimeError(f'Could not render TikTok slideshow: {error}')

        return final_path, width, height

    def _extract_tiktok_slideshow_info(
        self,
        post_id: str,
        canonical_url: str,
        audio_only: bool = False,
    ) -> Dict:
        """Download or render a TikTok photo post using its official embed data."""
        video_data = self._get_tiktok_embed_video_data(post_id)
        image_post_info = video_data.get('imagePostInfo') or {}
        images = image_post_info.get('displayImages') or []
        if not images:
            raise ValueError('TikTok post does not contain a downloadable slideshow')

        item_info = video_data.get('itemInfos') or {}
        author_info = video_data.get('authorInfos') or {}
        music_info = video_data.get('musicInfos') or {}
        music_urls = music_info.get('playUrl') or []
        referer = f'https://www.tiktok.com/embed/v2/{post_id}'
        title = item_info.get('text') or f'TikTok photo post by {author_info.get("uniqueId") or "unknown user"}'

        print(f'Downloading TikTok photo post {post_id} through the official embed endpoint')
        if audio_only:
            self._create_tiktok_audio(post_id, music_urls, referer)
            width = height = None
        else:
            _, width, height = self._create_tiktok_slideshow(
                post_id,
                images,
                music_urls,
                referer,
            )

        return {
            'id': post_id,
            'title': title,
            'description': item_info.get('text') or '',
            'webpage_url': canonical_url,
            'extractor': 'TikTok',
            'extractor_key': 'TikTok',
            'uploader': author_info.get('uniqueId'),
            'uploader_id': author_info.get('userId'),
            'timestamp': int(item_info['createTime']) if item_info.get('createTime') else None,
            'width': width,
            'height': height,
            'vcodec': None if audio_only else 'h264',
            '_telegram_compatible': not audio_only,
        }

    def _get_tiktok_oembed_metadata(self, canonical_url: str) -> Dict:
        """Fetch public TikTok metadata used to enrich the embed fallback."""
        try:
            response = requests.get(
                'https://www.tiktok.com/oembed',
                params={'url': canonical_url},
                headers=self._default_http_headers(),
                timeout=30,
            )
            response.raise_for_status()
            return response.json()
        except Exception as e:
            print(f"TikTok oEmbed metadata unavailable: {e}")
            return {}

    def _should_use_tiktok_embed_fallback(self, url: str, error: Exception) -> bool:
        if not self._is_tiktok_url(url):
            return False

        error_text = str(error).lower()
        return any(message in error_text for message in (
            'tiktok is requiring login',
            'unable to extract universal data',
            'video not available, status code -1',
        ))

    def _extract_instagram_info(self, url: str, ydl_opts: Dict) -> Dict:
        """Select the first video before yt-dlp processes carousel photo entries."""
        with yt_dlp.YoutubeDL(ydl_opts) as ydl:
            info = ydl.extract_info(url, download=False, process=False)
            if info and 'entries' in info:
                playlist = info
                info = next((
                    entry for entry in playlist['entries'] or []
                    if entry and (entry.get('formats') or entry.get('url'))
                ), None)
                if info is None:
                    raise ValueError('Instagram post contains no downloadable videos (photo-only carousel)')
                info = dict(info)
                for key in ('title', 'description', 'uploader', 'extractor_key', 'extractor'):
                    if not info.get(key) and playlist.get(key):
                        info[key] = playlist[key]
            if not info:
                raise ValueError('Instagram returned no media information')
            return ydl.process_ie_result(info, download=True)

    def _extract_info(
        self,
        url: str,
        ydl_opts: Dict,
        audio_only: bool = False,
    ) -> Dict:
        """Extract normally, with an official TikTok embed fallback for login redirects."""
        if self._is_threads_url(url):
            return self._extract_threads_info(url, ydl_opts)

        hostname = (urlparse(url).hostname or '').lower()
        if hostname == 'instagram.com' or hostname.endswith('.instagram.com'):
            return self._extract_instagram_info(url, ydl_opts)

        direct_photo_match = re.search(r'/photo/(\d+)', url)
        if self._is_tiktok_url(url) and direct_photo_match:
            return self._extract_tiktok_slideshow_info(
                direct_photo_match.group(1),
                url,
                audio_only=audio_only,
            )

        try:
            with yt_dlp.YoutubeDL(ydl_opts) as ydl:
                return ydl.extract_info(url, download=True)
        except yt_dlp.utils.DownloadError as original_error:
            if not self._is_tiktok_url(url):
                raise

            try:
                post_type, video_id, canonical_url = self._resolve_tiktok_post(url)
                if post_type == 'photo':
                    return self._extract_tiktok_slideshow_info(
                        video_id,
                        canonical_url,
                        audio_only=audio_only,
                    )

                if not self._should_use_tiktok_embed_fallback(url, original_error):
                    raise original_error

                embed_url = f'https://www.tiktok.com/embed/v2/{video_id}'
                metadata = self._get_tiktok_oembed_metadata(canonical_url)

                fallback_opts = dict(ydl_opts)
                fallback_opts.pop('cookiefile', None)
                fallback_opts.pop('http_headers', None)
                fallback_opts.update({
                    'playlist_items': '1',
                    'writethumbnail': False,
                    'outtmpl': os.path.join(self.videos_path, f'{video_id}.%(ext)s'),
                })

                print(
                    f"TikTok webpage requires login; retrying public video {video_id} "
                    "through the official embed endpoint"
                )
                with yt_dlp.YoutubeDL(fallback_opts) as ydl:
                    info = ydl.extract_info(embed_url, download=True)

                entries = info.get('entries') or []
                if entries:
                    entry = entries[0]
                    entry.update({
                        'id': video_id,
                        'title': metadata.get('title') or entry.get('title') or f'TikTok {video_id}',
                        'description': metadata.get('title') or entry.get('description') or '',
                        'uploader': metadata.get('author_name') or entry.get('uploader'),
                        'uploader_url': metadata.get('author_url') or entry.get('uploader_url'),
                        'thumbnail': metadata.get('thumbnail_url') or entry.get('thumbnail'),
                        'extractor_key': 'TikTok',
                    })

                return info
            except Exception as fallback_error:
                print(f"TikTok embed fallback failed: {fallback_error}")
                raise original_error from fallback_error

    def _default_http_headers(self) -> Dict[str, str]:
        return {
            'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36',
            'Accept': 'text/html,application/xhtml+xml,application/xml;q=0.9,image/webp,*/*;q=0.8',
            'Accept-Language': 'en-US,en;q=0.9',
            'Accept-Encoding': 'gzip, deflate, br',
            'DNT': '1',
            'Connection': 'keep-alive',
            'Upgrade-Insecure-Requests': '1',
        }

    async def download(
        self,
        url: str,
        quality: str,
        progress_callback: Optional[Callable[[Dict], None]] = None,
    ) -> Dict:
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
                # Deno is enabled by default and runs yt-dlp's YouTube EJS challenges.
                'js_runtimes': {'deno': {}},
            }

            if self._is_facebook_url(url):
                print("Using yt-dlp default headers for Facebook URL")
            else:
                # Additional headers to avoid bot detection on platforms that tolerate them.
                ydl_opts['http_headers'] = self._default_http_headers()

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

            # Add a throttled hook for logs and the Telegram progress display.
            last_progress_emit = 0.0
            last_progress_phase = None

            def emit_progress(payload: Dict):
                if not progress_callback:
                    return
                try:
                    progress_callback(payload)
                except Exception as progress_error:
                    print(f"Progress callback failed: {progress_error}")

            def progress_hook(d):
                nonlocal last_progress_emit, last_progress_phase
                if d['status'] == 'downloading':
                    downloaded_bytes = d.get('downloaded_bytes', 0)
                    total_bytes = d.get('total_bytes') or d.get('total_bytes_estimate')
                    percent = None
                    if total_bytes:
                        percent = (downloaded_bytes / total_bytes) * 100
                        print(f"Download progress: {percent:.1f}% ({downloaded_bytes}/{total_bytes} bytes)")
                    else:
                        print(f"Downloaded: {downloaded_bytes} bytes")

                    info_dict = d.get('info_dict') or {}
                    vcodec = info_dict.get('vcodec')
                    acodec = info_dict.get('acodec')
                    if is_audio_only or (vcodec == 'none' and acodec not in (None, 'none')):
                        phase = 'audio'
                    elif acodec == 'none' and vcodec not in (None, 'none'):
                        phase = 'video'
                    else:
                        phase = 'media'

                    now = time.monotonic()
                    if phase != last_progress_phase or now - last_progress_emit >= 1:
                        emit_progress({
                            'state': 'working',
                            'phase': phase,
                            'percent': percent,
                            'downloaded_bytes': downloaded_bytes,
                            'total_bytes': total_bytes,
                            'speed': d.get('speed'),
                            'eta': d.get('eta'),
                        })
                        last_progress_emit = now
                        last_progress_phase = phase
                elif d['status'] == 'finished':
                    print(f"Download finished, now post-processing...")
                elif d['status'] == 'error':
                    print(f"Download error occurred")

            ydl_opts['progress_hooks'] = [progress_hook]

            # Download video
            info = self._extract_info(url, ydl_opts, audio_only=is_audio_only)
            emit_progress({
                'state': 'working',
                'phase': 'processing',
                'percent': 100.0,
            })

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
            if not is_audio_only and info.get('height') and info.get('width'):
                original_quality = f"{info['width']}x{info['height']} ({info['height']}p)"

            # FORCE re-encode video for Telegram compatibility
            # This ensures Instagram and other videos work correctly on iOS/macOS
            # Also converts non-mp4 formats (webm, mkv) to mp4
            if (
                not is_audio_only
                and file_path
                and os.path.exists(file_path)
                and not info.get('_telegram_compatible')
            ):
                file_path = self._reencode_video(file_path, video_id)

            # Get file size (after re-encoding)
            file_size = os.path.getsize(file_path) if file_path and os.path.exists(file_path) else 0

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
