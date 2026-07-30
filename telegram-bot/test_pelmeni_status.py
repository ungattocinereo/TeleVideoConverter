import json
import time
import unittest
from types import SimpleNamespace
from unittest.mock import AsyncMock, Mock, patch

from bot import TelegramBot
from pelmeni_messages import QUEUED_MESSAGES, WORKING_MESSAGES, DELIVERING_MESSAGES


class PelmeniStatusTests(unittest.IsolatedAsyncioTestCase):
    def make_bot(self):
        bot = TelegramBot()
        bot.redis_client = AsyncMock()
        return bot

    def test_message_pool_sizes_and_uniqueness(self):
        self.assertEqual(len(QUEUED_MESSAGES), 12)
        self.assertEqual(len(WORKING_MESSAGES), 28)
        self.assertEqual(len(DELIVERING_MESSAGES), 12)

        all_messages = QUEUED_MESSAGES + WORKING_MESSAGES + DELIVERING_MESSAGES
        self.assertEqual(len(all_messages), 52)
        self.assertEqual(len(set(all_messages)), 52)

    def test_messages_do_not_repeat_before_pool_is_exhausted(self):
        bot = self.make_bot()

        first_cycle = [bot.get_status_joke("queued") for _ in QUEUED_MESSAGES]

        self.assertEqual(set(first_cycle), set(QUEUED_MESSAGES))

    def test_elapsed_time_format(self):
        self.assertEqual(TelegramBot.format_elapsed(0), "00:00")
        self.assertEqual(TelegramBot.format_elapsed(73), "01:13")
        self.assertEqual(TelegramBot.format_elapsed(3671), "1:01:11")

    def test_real_progress_bar_shows_percent_size_speed_and_eta(self):
        bot = self.make_bot()
        message = bot.build_status_message(
            "working",
            15,
            payload={
                "state": "working",
                "phase": "video",
                "percent": 40,
                "downloaded_bytes": 40 * 1024 * 1024,
                "total_bytes": 100 * 1024 * 1024,
                "speed": 2 * 1024 * 1024,
                "eta": 30,
            },
            joke="Тестовый пельмень считает байты.",
        )

        self.assertIn("40.0%", message)
        self.assertIn("40.0 MB / 100.0 MB", message)
        self.assertIn("2.0 MB/s", message)
        self.assertIn("ещё ≈ 00:30", message)
        self.assertIn("Качаю видеослой", message)

    def test_unknown_progress_uses_indeterminate_bar(self):
        bot = self.make_bot()

        bar = bot.build_progress_bar(None, 9)

        self.assertIn("▓▓▓", bar)
        self.assertNotIn("%", bar)

    def test_progress_flow_uses_only_dumpling_and_pastry_icons(self):
        bot = self.make_bot()
        messages = [
            bot.build_status_message("queued", 0, joke="Тест"),
            bot.build_status_message(
                "working",
                5,
                payload={"state": "working", "phase": "video", "percent": 20},
                joke="Тест",
            ),
            bot.build_status_message(
                "working",
                10,
                payload={"state": "working", "phase": "processing", "percent": 100},
                joke="Тест",
            ),
            bot.build_status_message("delivering", 15, joke="Тест"),
        ]
        combined = "\n".join(messages)

        for pastry in ("🥟", "🥨", "🥧", "🥐"):
            self.assertIn(pastry, combined)
        for ordinary_icon in ("🕰", "♨️", "📤", "⚙️", "🎞", "🔊", "⬇️"):
            self.assertNotIn(ordinary_icon, combined)

    def test_json_and_legacy_redis_statuses_are_supported(self):
        payload = TelegramBot.parse_status_payload(
            b'{"state":"working","percent":55.5}'
        )
        legacy = TelegramBot.parse_status_payload(b"delivering")

        self.assertEqual(payload["percent"], 55.5)
        self.assertEqual(legacy, {"state": "delivering"})

    async def test_animation_edits_one_message_and_stops_on_done(self):
        bot = self.make_bot()
        bot.redis_client.get = AsyncMock(side_effect=[
            b'{"state":"working","phase":"media","percent":25}',
            b"done",
        ])
        telegram = SimpleNamespace(edit_message_text=AsyncMock())

        with patch("bot.STATUS_POLL_INTERVAL_SECONDS", 0):
            await bot.animate_download_status(
                telegram,
                chat_id=42,
                status_message_id=7,
                status_key="download_status:test",
                started_at=time.monotonic(),
            )

        telegram.edit_message_text.assert_awaited_once()
        edit = telegram.edit_message_text.await_args.kwargs
        self.assertEqual(edit["chat_id"], 42)
        self.assertEqual(edit["message_id"], 7)
        self.assertIn("Пельмени скачивают", edit["text"])

    async def test_edit_failure_does_not_break_animation_cleanup(self):
        bot = self.make_bot()
        bot.redis_client.get = AsyncMock(side_effect=[b"delivering", b"error"])
        telegram = SimpleNamespace(
            edit_message_text=AsyncMock(side_effect=RuntimeError("Telegram unavailable"))
        )

        with patch("bot.STATUS_POLL_INTERVAL_SECONDS", 0):
            await bot.animate_download_status(
                telegram,
                chat_id=42,
                status_message_id=7,
                status_key="download_status:test",
                started_at=time.monotonic(),
            )

        telegram.edit_message_text.assert_awaited_once()

    async def test_url_submission_stores_status_metadata_and_starts_animation(self):
        bot = self.make_bot()
        bot.check_user_permission = Mock(return_value=True)
        bot.check_rate_limit = Mock(return_value=True)
        bot.db.get_stats = AsyncMock(return_value={"used_space_bytes": 0})

        status_message = SimpleNamespace(message_id=77, edit_text=AsyncMock())
        message = SimpleNamespace(
            text="https://example.com/video",
            chat_id=42,
            message_id=11,
            reply_text=AsyncMock(return_value=status_message),
        )
        update = SimpleNamespace(
            effective_user=SimpleNamespace(id=123),
            message=message,
        )

        scheduled = []

        def capture_task(coroutine, update=None):
            scheduled.append((coroutine, update))
            coroutine.close()

        context = SimpleNamespace(
            bot=SimpleNamespace(),
            application=SimpleNamespace(create_task=Mock(side_effect=capture_task)),
        )

        await bot.handle_url(update, context)

        bot.redis_client.set.assert_awaited_once()
        bot.redis_client.lpush.assert_awaited_once()
        task = json.loads(bot.redis_client.lpush.await_args.args[1])
        self.assertEqual(task["status_message_id"], 77)
        self.assertTrue(task["status_key"].startswith("download_status:"))
        self.assertEqual(task["message_id"], 11)
        self.assertEqual(len(scheduled), 1)


if __name__ == "__main__":
    unittest.main()
