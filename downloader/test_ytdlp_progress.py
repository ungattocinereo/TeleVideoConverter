import os
import tempfile
import unittest

from ytdlp_wrapper import VideoDownloader


class YtDlpProgressTests(unittest.IsolatedAsyncioTestCase):
    async def test_download_reports_real_layer_progress_then_processing(self):
        with tempfile.TemporaryDirectory() as storage_path:
            downloader = VideoDownloader(storage_path)
            events = []

            def fake_extract_info(url, options, audio_only=False):
                self.assertFalse(audio_only)
                self.assertEqual(options["js_runtimes"], {"deno": {}})
                progress_hook = options["progress_hooks"][0]
                progress_hook({
                    "status": "downloading",
                    "downloaded_bytes": 40,
                    "total_bytes": 100,
                    "speed": 10,
                    "eta": 6,
                    "info_dict": {
                        "vcodec": "h264",
                        "acodec": "none",
                    },
                })
                progress_hook({"status": "finished"})

                file_path = os.path.join(storage_path, "videos", "test-id.mp4")
                with open(file_path, "wb") as video_file:
                    video_file.write(b"small test video")

                return {
                    "id": "test-id",
                    "title": "Test video",
                    "width": 1280,
                    "height": 720,
                    "vcodec": "h264",
                    "extractor_key": "Test",
                }

            downloader._extract_info = fake_extract_info

            result = await downloader.download(
                "https://example.com/video",
                "best",
                progress_callback=events.append,
            )

        self.assertTrue(result["success"])
        self.assertEqual(events[0]["phase"], "video")
        self.assertEqual(events[0]["percent"], 40)
        self.assertEqual(events[0]["speed"], 10)
        self.assertEqual(events[-1]["phase"], "processing")
        self.assertEqual(events[-1]["percent"], 100)


if __name__ == "__main__":
    unittest.main()
