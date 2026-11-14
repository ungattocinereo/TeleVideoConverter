import tempfile
import unittest
from unittest.mock import patch

from ytdlp_wrapper import VideoDownloader


class InstagramCarouselTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.downloader = VideoDownloader(self.directory.name)
        self.url = 'https://www.instagram.com/p/Dcq7wNqjdq-/'

    @patch('ytdlp_wrapper.yt_dlp.YoutubeDL')
    def test_selects_first_video_after_photos_and_preserves_caption(self, factory):
        ydl = factory.return_value.__enter__.return_value
        video = {'id': 'video1', 'formats': [{'url': 'https://example.com/video.mp4'}]}
        ydl.extract_info.return_value = {
            '_type': 'playlist', 'description': 'Post caption', 'extractor_key': 'Instagram',
            'entries': iter([None, {'id': 'photo', 'thumbnails': [{}]}, video,
                             {'id': 'video2', 'formats': [{}]}]),
        }
        result = self.downloader._extract_info(self.url, {'cookiefile': 'cookies.txt'})
        ydl.extract_info.assert_called_once_with(self.url, download=False, process=False)
        selected = ydl.process_ie_result.call_args.args[0]
        self.assertEqual(selected['id'], 'video1')
        self.assertEqual(selected['description'], 'Post caption')
        self.assertEqual(selected['extractor_key'], 'Instagram')
        self.assertNotIn('description', video)
        self.assertEqual(result, ydl.process_ie_result.return_value)
        self.assertTrue(ydl.process_ie_result.call_args.kwargs['download'])

    @patch('ytdlp_wrapper.yt_dlp.YoutubeDL')
    def test_photo_only_post_has_clear_error_without_downloading(self, factory):
        ydl = factory.return_value.__enter__.return_value
        ydl.extract_info.return_value = {'entries': [{'id': 'photo', 'formats': []}]}
        with self.assertRaisesRegex(ValueError, 'no downloadable videos'):
            self.downloader._extract_info(self.url, {})
        ydl.process_ie_result.assert_not_called()

    @patch('ytdlp_wrapper.yt_dlp.YoutubeDL')
    def test_single_reel_still_downloads(self, factory):
        ydl = factory.return_value.__enter__.return_value
        info = {'id': 'reel', 'formats': [{'url': 'https://example.com/video.mp4'}]}
        ydl.extract_info.return_value = info
        self.downloader._extract_info(self.url, {})
        ydl.process_ie_result.assert_called_once_with(info, download=True)


if __name__ == '__main__':
    unittest.main()
