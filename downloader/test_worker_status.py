import json
import os
import tempfile
import unittest
from unittest.mock import AsyncMock, Mock, patch

from telegram.error import NetworkError

from pelmeni_messages import ERROR_MESSAGES, SUCCESS_MESSAGES
from telegram_media import PreparedTelegramFile
from worker import DownloadWorker


class WorkerStatusTests(unittest.IsolatedAsyncioTestCase):
    def make_worker(self):
        worker = DownloadWorker.__new__(DownloadWorker)
        worker.redis_client = AsyncMock()
        worker.status_redis = Mock()
        worker.bot = AsyncMock()
        worker.terminal_message_bags = {"done": [], "error": []}
        worker.progress_publish_warning_logged = False
        return worker

    def test_terminal_pool_sizes_and_uniqueness(self):
        self.assertEqual(len(SUCCESS_MESSAGES), 12)
        self.assertEqual(len(ERROR_MESSAGES), 8)
        self.assertEqual(len(set(SUCCESS_MESSAGES + ERROR_MESSAGES)), 20)

    def test_success_messages_do_not_repeat_before_pool_is_exhausted(self):
        worker = self.make_worker()

        first_cycle = [worker.get_terminal_message("done") for _ in SUCCESS_MESSAGES]

        self.assertEqual(set(first_cycle), set(SUCCESS_MESSAGES))

    def test_terminal_statuses_use_dumpling_icon(self):
        worker = self.make_worker()

        self.assertTrue(worker.build_success_status(5).startswith("🥟"))
        self.assertTrue(worker.build_error_status("test error").startswith("🥟"))

    async def test_terminal_status_updates_redis_and_edits_original_message(self):
        worker = self.make_worker()
        task = {
            "chat_id": 42,
            "status_message_id": 77,
            "status_key": "download_status:test",
        }

        edited = await worker.finish_download_status(task, "done", "готово")

        self.assertTrue(edited)
        worker.redis_client.set.assert_awaited_once_with(
            "download_status:test",
            "done",
            ex=300,
        )
        worker.bot.edit_message_text.assert_awaited_once_with(
            chat_id=42,
            message_id=77,
            text="готово",
        )

    async def test_stats_message_disables_link_preview(self):
        worker = self.make_worker()
        stats = {
            "delete_timestamp": 1770000000,
            "format": "mp4",
            "codec": "h264",
            "original_quality": "1280x720 (720p)",
            "downloaded_quality": "best",
            "file_size": 1234,
            "processing_time": 5,
            "source_platform": "Threads",
            "title": "https://www.threads.com/@user/post/example",
        }

        await worker.send_stats_message(42, stats)

        worker.bot.send_message.assert_awaited_once()
        self.assertTrue(
            worker.bot.send_message.await_args.kwargs["disable_web_page_preview"],
        )

    async def test_transient_telegram_failure_is_retried(self):
        worker = self.make_worker()
        request = AsyncMock(side_effect=[NetworkError("offline"), "ok"])

        with (
            patch("worker.TELEGRAM_RETRY_ATTEMPTS", 2),
            patch("worker.TELEGRAM_RETRY_BASE_DELAY_SECONDS", 0),
            patch("worker.TELEGRAM_RETRY_MAX_DELAY_SECONDS", 0),
            patch("worker.asyncio.sleep", new=AsyncMock()),
        ):
            result = await worker.telegram_request("test request", request)

        self.assertEqual(result, "ok")
        self.assertEqual(request.await_count, 2)

    async def test_video_file_is_reopened_for_retry(self):
        worker = self.make_worker()
        payloads = []

        async def send_video(**kwargs):
            payloads.append(kwargs["video"].read())
            if len(payloads) == 1:
                raise NetworkError("offline")

        worker.bot.send_video = AsyncMock(side_effect=send_video)

        with tempfile.NamedTemporaryFile(delete=False) as video_file:
            video_file.write(b"video-payload")
            video_path = video_file.name

        try:
            prepared = PreparedTelegramFile(
                path=video_path,
                width=320,
                height=180,
            )
            with (
                patch("worker.TELEGRAM_RETRY_ATTEMPTS", 2),
                patch("worker.TELEGRAM_RETRY_BASE_DELAY_SECONDS", 0),
                patch("worker.TELEGRAM_RETRY_MAX_DELAY_SECONDS", 0),
                patch("worker.asyncio.sleep", new=AsyncMock()),
            ):
                await worker.send_prepared_video(42, prepared)
        finally:
            os.unlink(video_path)

        self.assertEqual(payloads, [b"video-payload", b"video-payload"])

    def test_real_progress_is_serialized_for_telegram_bot(self):
        worker = self.make_worker()
        task = {"status_key": "download_status:test"}

        worker.publish_download_progress(task, {
            "phase": "video",
            "percent": 42.5,
            "downloaded_bytes": 425,
            "total_bytes": 1000,
            "speed": 50,
            "eta": 12,
        })

        worker.status_redis.set.assert_called_once()
        key, raw_payload = worker.status_redis.set.call_args.args
        payload = json.loads(raw_payload)
        self.assertEqual(key, "download_status:test")
        self.assertEqual(payload["state"], "working")
        self.assertEqual(payload["percent"], 42.5)
        self.assertEqual(worker.status_redis.set.call_args.kwargs["ex"], 4 * 60 * 60)

    async def test_legacy_task_without_status_fields_is_supported(self):
        worker = self.make_worker()

        edited = await worker.finish_download_status(
            {"chat_id": 42},
            "error",
            "ошибка",
        )

        self.assertFalse(edited)
        worker.redis_client.set.assert_not_awaited()
        worker.bot.edit_message_text.assert_not_awaited()

    async def test_download_failure_stops_animation_and_keeps_real_reason(self):
        worker = self.make_worker()
        worker.downloader = SimpleDownloader({"success": False, "error": "extractor exploded"})
        task = {
            "url": "https://example.com/video",
            "quality": "best",
            "user_id": 123,
            "chat_id": 42,
            "status_message_id": 77,
            "status_key": "download_status:test",
        }

        await worker.process_download(task)

        states = [call.args[1] for call in worker.redis_client.set.await_args_list]
        self.assertEqual(states, ["working", "error"])
        final_text = worker.bot.edit_message_text.await_args.kwargs["text"]
        self.assertIn("extractor exploded", final_text)
        worker.bot.send_message.assert_not_awaited()

    async def test_success_moves_from_working_to_delivery_to_done(self):
        worker = self.make_worker()
        worker.downloader = SimpleDownloader({
            "success": True,
            "video_id": "abc",
            "title": "Test video",
            "original_quality": "1280x720 (720p)",
            "file_size": 1234,
            "format": "mp4",
            "codec": "h264",
            "source_platform": "Test",
            "file_path": "/tmp/test.mp4",
            "thumbnail_path": None,
            "width": 1280,
            "height": 720,
            "description": "",
        })
        worker.db = AsyncMock()
        worker.db.get_user_setting.return_value = 0
        worker.send_stats_message = AsyncMock()
        worker.send_file = AsyncMock()
        worker.send_description = AsyncMock()
        task = {
            "url": "https://example.com/video",
            "quality": "best",
            "user_id": 123,
            "chat_id": 42,
            "status_message_id": 77,
            "status_key": "download_status:test",
        }

        await worker.process_download(task)

        states = [call.args[1] for call in worker.redis_client.set.await_args_list]
        self.assertEqual(states, ["working", "delivering", "done"])
        worker.send_stats_message.assert_awaited_once()
        worker.send_file.assert_awaited_once()
        worker.bot.edit_message_text.assert_awaited_once()


class SimpleDownloader:
    def __init__(self, result):
        self.result = result

    async def download(self, url, quality, progress_callback=None):
        return self.result


if __name__ == "__main__":
    unittest.main()
