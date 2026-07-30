import json
import math
import os
import subprocess
import uuid
from dataclasses import dataclass
from pathlib import Path
from typing import List, Optional, Tuple


@dataclass
class MediaProbe:
    duration: float
    width: Optional[int]
    height: Optional[int]
    has_video: bool
    has_audio: bool


@dataclass
class PreparedTelegramFile:
    path: str
    width: Optional[int]
    height: Optional[int]
    part_index: int = 1
    part_count: int = 1
    cleanup: bool = False


class TelegramMediaPreparer:
    """Create Telegram-sized temporary video files without changing stored media."""

    SCALE_LADDER = (720, 540, 480, 360)

    def __init__(
        self,
        storage_path: str,
        send_limit_mb: float = 50,
        target_mb: float = 48,
        min_video_bitrate_k: int = 250,
        audio_bitrate_k: int = 64,
        large_file_policy: str = "compress_then_split",
    ):
        self.send_limit_bytes = int(send_limit_mb * 1024 * 1024)
        self.target_bytes = int(target_mb * 1024 * 1024)
        self.min_video_bitrate_k = int(min_video_bitrate_k)
        self.audio_bitrate_k = int(audio_bitrate_k)
        self.large_file_policy = large_file_policy
        self.temp_path = os.path.join(storage_path, "telegram")
        os.makedirs(self.temp_path, exist_ok=True)

    def prepare_telegram_files(self, file_path: str) -> List[PreparedTelegramFile]:
        """Return one or more files that should fit Telegram cloud Bot API limits."""
        file_size = os.path.getsize(file_path)
        probe = self.probe(file_path)

        if file_size <= self.target_bytes or not probe.has_video:
            return [
                PreparedTelegramFile(
                    path=file_path,
                    width=probe.width,
                    height=probe.height,
                    cleanup=False,
                )
            ]

        if probe.duration <= 0:
            raise RuntimeError("Cannot compress video for Telegram: duration is unknown")

        part_count = self.calculate_part_count(probe.duration, probe.has_audio)
        if part_count > 1 and self.large_file_policy != "compress_then_split":
            raise RuntimeError("Video is too long to fit Telegram limit without splitting")

        prepared = []
        part_duration = probe.duration / part_count

        for index in range(part_count):
            start = index * part_duration
            duration = probe.duration - start if index == part_count - 1 else part_duration
            output_path = self._temp_output_path(file_path, index + 1, part_count)
            video_bitrate_k, audio_bitrate_k = self.calculate_bitrates(duration, probe.has_audio)
            max_side = self.choose_max_side(video_bitrate_k)

            self._compress_part(
                input_path=file_path,
                output_path=output_path,
                start_seconds=start if part_count > 1 else None,
                duration_seconds=duration if part_count > 1 else None,
                has_audio=probe.has_audio,
                video_bitrate_k=video_bitrate_k,
                audio_bitrate_k=audio_bitrate_k,
                max_side=max_side,
            )

            output_size = os.path.getsize(output_path)
            if output_size > self.send_limit_bytes:
                self._cleanup_file(output_path)
                raise RuntimeError(
                    f"Compressed part is still too large for Telegram: "
                    f"{output_size / (1024 * 1024):.1f} MB"
                )

            output_probe = self.probe(output_path)
            prepared.append(
                PreparedTelegramFile(
                    path=output_path,
                    width=output_probe.width,
                    height=output_probe.height,
                    part_index=index + 1,
                    part_count=part_count,
                    cleanup=True,
                )
            )

        return prepared

    def probe(self, file_path: str) -> MediaProbe:
        result = subprocess.run(
            [
                "ffprobe",
                "-v",
                "error",
                "-print_format",
                "json",
                "-show_format",
                "-show_streams",
                file_path,
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            check=True,
            text=True,
        )
        data = json.loads(result.stdout)
        streams = data.get("streams", [])
        video_stream = next((stream for stream in streams if stream.get("codec_type") == "video"), None)
        has_audio = any(stream.get("codec_type") == "audio" for stream in streams)

        duration = self._float_or_zero(data.get("format", {}).get("duration"))
        if duration <= 0 and video_stream:
            duration = self._float_or_zero(video_stream.get("duration"))

        width = self._int_or_none(video_stream.get("width")) if video_stream else None
        height = self._int_or_none(video_stream.get("height")) if video_stream else None

        return MediaProbe(
            duration=duration,
            width=width,
            height=height,
            has_video=video_stream is not None,
            has_audio=has_audio,
        )

    def calculate_bitrates(self, duration_seconds: float, has_audio: bool) -> Tuple[int, int]:
        video_bitrate_k, audio_bitrate_k = self._raw_bitrates(duration_seconds, has_audio)
        return max(self.min_video_bitrate_k, video_bitrate_k), audio_bitrate_k

    def calculate_part_count(self, duration_seconds: float, has_audio: bool) -> int:
        video_bitrate_k, _ = self._raw_bitrates(duration_seconds, has_audio)
        if video_bitrate_k >= self.min_video_bitrate_k:
            return 1

        audio_bitrate_k = self.audio_bitrate_k if has_audio else 0
        usable_bits = self.target_bytes * 8 * 0.92
        minimum_total_bitrate = (self.min_video_bitrate_k + audio_bitrate_k) * 1000
        max_duration_per_part = max(1, usable_bits / minimum_total_bitrate)
        return max(1, math.ceil(duration_seconds / max_duration_per_part))

    def _raw_bitrates(self, duration_seconds: float, has_audio: bool) -> Tuple[int, int]:
        usable_bits = self.target_bytes * 8 * 0.92
        total_bitrate_k = max(1, int(usable_bits / max(duration_seconds, 1) / 1000))
        audio_bitrate_k = self.audio_bitrate_k if has_audio else 0
        video_bitrate_k = total_bitrate_k - audio_bitrate_k
        return video_bitrate_k, audio_bitrate_k

    def choose_max_side(self, video_bitrate_k: int) -> int:
        if video_bitrate_k >= 1000:
            return self.SCALE_LADDER[0]
        if video_bitrate_k >= 700:
            return self.SCALE_LADDER[1]
        if video_bitrate_k >= 450:
            return self.SCALE_LADDER[2]
        return self.SCALE_LADDER[3]

    def cleanup(self, prepared_files: List[PreparedTelegramFile]):
        for prepared_file in prepared_files:
            if prepared_file.cleanup:
                self._cleanup_file(prepared_file.path)

    def _compress_part(
        self,
        input_path: str,
        output_path: str,
        start_seconds: Optional[float],
        duration_seconds: Optional[float],
        has_audio: bool,
        video_bitrate_k: int,
        audio_bitrate_k: int,
        max_side: int,
    ):
        passlog = os.path.join(self.temp_path, f"ffmpeg-pass-{uuid.uuid4().hex}")
        success = False

        try:
            first_pass = self._ffmpeg_base_command(input_path, start_seconds, duration_seconds)
            first_pass.extend(
                self._video_encode_args(video_bitrate_k, max_side)
                + [
                    "-an",
                    "-pass",
                    "1",
                    "-passlogfile",
                    passlog,
                    "-f",
                    "mp4",
                    os.devnull,
                ]
            )

            second_pass = self._ffmpeg_base_command(input_path, start_seconds, duration_seconds)
            second_pass.extend(self._video_encode_args(video_bitrate_k, max_side))
            if has_audio:
                second_pass.extend(["-map", "0:a:0?", "-c:a", "aac", "-b:a", f"{audio_bitrate_k}k", "-ar", "44100"])
            else:
                second_pass.append("-an")
            second_pass.extend(
                [
                    "-pass",
                    "2",
                    "-passlogfile",
                    passlog,
                    "-movflags",
                    "+faststart",
                    output_path,
                ]
            )

            timeout_seconds = self._encode_timeout(duration_seconds or self.probe(input_path).duration)
            subprocess.run(first_pass, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True, timeout=timeout_seconds)
            subprocess.run(second_pass, stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=True, timeout=timeout_seconds)
            success = True
        except subprocess.CalledProcessError as exc:
            stderr_output = exc.stderr.decode(errors="replace") if exc.stderr else "No stderr output"
            raise RuntimeError(f"ffmpeg compression failed: {stderr_output[:800]}") from exc
        except subprocess.TimeoutExpired as exc:
            raise RuntimeError("ffmpeg compression timed out") from exc
        finally:
            self._cleanup_pass_logs(passlog)
            if not success:
                self._cleanup_file(output_path)

    def _ffmpeg_base_command(
        self,
        input_path: str,
        start_seconds: Optional[float],
        duration_seconds: Optional[float],
    ) -> List[str]:
        command = ["ffmpeg", "-y"]
        if start_seconds is not None:
            command.extend(["-ss", f"{start_seconds:.3f}"])
        if duration_seconds is not None:
            command.extend(["-t", f"{duration_seconds:.3f}"])
        command.extend(["-i", input_path, "-map", "0:v:0"])
        return command

    def _video_encode_args(self, video_bitrate_k: int, max_side: int) -> List[str]:
        scale_filter = (
            f"scale='if(gte(iw,ih),min({max_side},iw),-2)':"
            f"'if(gte(iw,ih),-2,min({max_side},ih))',"
            "scale=trunc(iw/2)*2:trunc(ih/2)*2"
        )
        maxrate_k = int(video_bitrate_k * 1.25)
        bufsize_k = int(video_bitrate_k * 2)

        return [
            "-c:v",
            "libx264",
            "-profile:v",
            "baseline",
            "-level",
            "3.0",
            "-preset",
            "medium",
            "-b:v",
            f"{video_bitrate_k}k",
            "-maxrate",
            f"{maxrate_k}k",
            "-bufsize",
            f"{bufsize_k}k",
            "-g",
            "50",
            "-keyint_min",
            "25",
            "-pix_fmt",
            "yuv420p",
            "-vf",
            scale_filter,
            "-fflags",
            "+genpts",
            "-max_muxing_queue_size",
            "9999",
        ]

    def _temp_output_path(self, input_path: str, part_index: int, part_count: int) -> str:
        stem = Path(input_path).stem
        suffix = f"part{part_index:02d}-of-{part_count:02d}" if part_count > 1 else "compressed"
        return os.path.join(self.temp_path, f"{stem}-{suffix}-{uuid.uuid4().hex[:8]}.mp4")

    def _encode_timeout(self, duration_seconds: float) -> int:
        return max(180, min(10800, int(max(duration_seconds, 1) * 30)))

    def _cleanup_pass_logs(self, passlog: str):
        log_dir = os.path.dirname(passlog)
        log_prefix = os.path.basename(passlog)
        if not os.path.isdir(log_dir):
            return

        for filename in os.listdir(log_dir):
            if filename.startswith(log_prefix):
                self._cleanup_file(os.path.join(log_dir, filename))

    def _cleanup_file(self, file_path: str):
        try:
            if os.path.exists(file_path):
                os.remove(file_path)
        except OSError:
            pass

    def _float_or_zero(self, value) -> float:
        try:
            return float(value)
        except (TypeError, ValueError):
            return 0.0

    def _int_or_none(self, value) -> Optional[int]:
        try:
            return int(value)
        except (TypeError, ValueError):
            return None
