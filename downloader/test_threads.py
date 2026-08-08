import tempfile
import unittest
from unittest.mock import Mock, patch

from ytdlp_wrapper import VideoDownloader


class ThreadsDownloaderTests(unittest.TestCase):
    def make_downloader(self):
        temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(temp_dir.cleanup)
        return VideoDownloader(temp_dir.name)

    def test_detects_threads_domains(self):
        downloader = self.make_downloader()

        self.assertTrue(downloader._is_threads_url('https://www.threads.com/t/abc'))
        self.assertTrue(downloader._is_threads_url('https://threads.net/@user/post/abc'))
        self.assertFalse(downloader._is_threads_url('https://example.com/threads.com/video'))

    def test_decodes_known_shortcode(self):
        downloader = self.make_downloader()

        self.assertEqual(
            downloader._decode_threads_shortcode('Dbv0cqSEQKk'),
            3958613265446142628,
        )

    @patch('ytdlp_wrapper.requests.get')
    def test_resolves_share_url(self, mock_get):
        downloader = self.make_downloader()
        response = Mock()
        response.url = 'https://www.threads.com/@bereket.tinictiq/post/Dbv0cqSEQKk?xmt=1'
        mock_get.return_value = response

        canonical_url, shortcode = downloader._resolve_threads_post(
            'https://www.threads.com/share/BA8BQEdWJl/',
        )

        self.assertEqual(canonical_url, response.url)
        self.assertEqual(shortcode, 'Dbv0cqSEQKk')
        response.raise_for_status.assert_called_once_with()

    @patch('ytdlp_wrapper.requests.post')
    def test_threads_api_request_uses_numeric_post_id(self, mock_post):
        downloader = self.make_downloader()
        response = Mock()
        response.json.return_value = {'data': {'media': {'video_versions': []}}}
        mock_post.return_value = response

        result = downloader._get_threads_media(
            'https://www.threads.com/@bereket.tinictiq/post/Dbv0cqSEQKk',
            'Dbv0cqSEQKk',
        )

        self.assertEqual(result, {'video_versions': []})
        request_data = mock_post.call_args.kwargs['data']
        self.assertIn('"postID":"3958613265446142628"', request_data['variables'])
        response.raise_for_status.assert_called_once_with()


if __name__ == '__main__':
    unittest.main()
