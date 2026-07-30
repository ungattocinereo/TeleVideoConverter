import tempfile
import unittest

from telegram_media import TelegramMediaPreparer


class TelegramMediaPreparerTests(unittest.TestCase):
    def make_preparer(self):
        temp_dir = tempfile.TemporaryDirectory()
        self.addCleanup(temp_dir.cleanup)
        return TelegramMediaPreparer(temp_dir.name)

    def test_short_video_uses_single_part_with_high_bitrate(self):
        preparer = self.make_preparer()

        part_count = preparer.calculate_part_count(60, has_audio=True)
        video_bitrate_k, audio_bitrate_k = preparer.calculate_bitrates(60, has_audio=True)

        self.assertEqual(part_count, 1)
        self.assertGreater(video_bitrate_k, 1000)
        self.assertEqual(audio_bitrate_k, 64)

    def test_long_video_is_split_to_keep_minimum_video_bitrate(self):
        preparer = self.make_preparer()

        part_count = preparer.calculate_part_count(7200, has_audio=True)
        video_bitrate_k, audio_bitrate_k = preparer.calculate_bitrates(7200 / part_count, has_audio=True)

        self.assertGreater(part_count, 1)
        self.assertGreaterEqual(video_bitrate_k, preparer.min_video_bitrate_k)
        self.assertEqual(audio_bitrate_k, 64)

    def test_video_without_audio_gets_video_only_budget(self):
        preparer = self.make_preparer()

        video_bitrate_k, audio_bitrate_k = preparer.calculate_bitrates(300, has_audio=False)

        self.assertGreater(video_bitrate_k, preparer.min_video_bitrate_k)
        self.assertEqual(audio_bitrate_k, 0)

    def test_resolution_ladder_follows_bitrate(self):
        preparer = self.make_preparer()

        self.assertEqual(preparer.choose_max_side(1200), 720)
        self.assertEqual(preparer.choose_max_side(800), 540)
        self.assertEqual(preparer.choose_max_side(500), 480)
        self.assertEqual(preparer.choose_max_side(300), 360)


if __name__ == "__main__":
    unittest.main()
