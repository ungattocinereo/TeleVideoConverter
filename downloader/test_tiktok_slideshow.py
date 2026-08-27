import html
import json
import tempfile
import unittest
from unittest.mock import Mock, patch

from ytdlp_wrapper import VideoDownloader


class TikTokSlideshowTests(unittest.TestCase):
    def make_downloader(self):
        temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(temp_dir.cleanup)
        return VideoDownloader(temp_dir.name)

    def test_resolves_direct_photo_post(self):
        downloader = self.make_downloader()

        self.assertEqual(
            downloader._resolve_tiktok_post(
                'https://www.tiktok.com/@creator/photo/7589795597490605332?lang=en',
            ),
            (
                'photo',
                '7589795597490605332',
                'https://www.tiktok.com/@creator/photo/7589795597490605332?lang=en',
            ),
        )

    @patch('ytdlp_wrapper.requests.get')
    def test_reads_slideshow_from_official_embed_page(self, mock_get):
        downloader = self.make_downloader()
        post_id = '7589795597490605332'
        video_data = {
            'itemInfos': {'id': post_id},
            'imagePostInfo': {'displayImages': [{'urlList': ['https://cdn/image.jpg']}]},
        }
        state = {
            'source': {
                'data': {
                    f'/embed/v2/{post_id}': {'videoData': video_data},
                },
            },
        }
        response = Mock()
        response.text = (
            '<script id="__FRONTITY_CONNECT_STATE__" type="application/json">'
            f'{html.escape(json.dumps(state))}'
            '</script>'
        )
        mock_get.return_value = response

        result = downloader._get_tiktok_embed_video_data(post_id)

        self.assertEqual(result, video_data)
        response.raise_for_status.assert_called_once_with()

    @patch.object(VideoDownloader, '_create_tiktok_slideshow')
    @patch.object(VideoDownloader, '_get_tiktok_embed_video_data')
    def test_builds_download_info_for_photo_post(self, mock_embed_data, mock_create):
        downloader = self.make_downloader()
        post_id = '7589795597490605332'
        mock_embed_data.return_value = {
            'itemInfos': {'id': post_id, 'text': 'Seven photos', 'createTime': '1'},
            'authorInfos': {'uniqueId': 'creator', 'userId': '42'},
            'musicInfos': {'playUrl': ['https://cdn/audio.mp3']},
            'imagePostInfo': {
                'displayImages': [{'urlList': ['https://cdn/image.jpg']}],
            },
        }
        mock_create.return_value = ('unused.mp4', 720, 1280)

        result = downloader._extract_tiktok_slideshow_info(
            post_id,
            f'https://www.tiktok.com/@creator/photo/{post_id}',
        )

        self.assertEqual(result['id'], post_id)
        self.assertEqual(result['title'], 'Seven photos')
        self.assertEqual(result['extractor_key'], 'TikTok')
        self.assertEqual((result['width'], result['height']), (720, 1280))
        self.assertTrue(result['_telegram_compatible'])
        mock_create.assert_called_once()


if __name__ == '__main__':
    unittest.main()
