from __future__ import annotations

import math
from typing import Any

# Giọng đọc bình thường tiếng Việt (audiobook / thuyết minh)
NORMAL_WPM = 150
IMAGE_SECONDS_PER_PROMPT = 20
VIDEO_SECONDS_PER_PROMPT = 20
IMAGE_BATCH_SIZE = 12


def words_for_seconds(seconds: int) -> int:
    return max(1, round(NORMAL_WPM * seconds / 60))


def split_narration_segments(text: str, seconds_per_segment: int = IMAGE_SECONDS_PER_PROMPT) -> list[dict[str, Any]]:
    words = (text or "").split()
    if not words:
        return [{"index": 1, "excerpt": "", "word_count": 0, "estimated_seconds": seconds_per_segment}]
    chunk_size = words_for_seconds(seconds_per_segment)
    segments: list[dict[str, Any]] = []
    for i in range(0, len(words), chunk_size):
        chunk = words[i : i + chunk_size]
        segments.append(
            {
                "index": len(segments) + 1,
                "excerpt": " ".join(chunk),
                "word_count": len(chunk),
                "estimated_seconds": seconds_per_segment,
            }
        )
    return segments


def estimate_narration_timing(text: str) -> dict[str, int | float | str]:
    word_count = len((text or "").split())
    estimated_seconds = max(1, round(word_count * 60 / NORMAL_WPM))
    image_prompt_count = max(1, math.ceil(estimated_seconds / IMAGE_SECONDS_PER_PROMPT))
    video_prompt_count = max(1, math.ceil(estimated_seconds / VIDEO_SECONDS_PER_PROMPT))
    return {
        "word_count": word_count,
        "estimated_audio_seconds": estimated_seconds,
        "wpm_assumption": NORMAL_WPM,
        "image_segment_seconds": str(IMAGE_SECONDS_PER_PROMPT),
        "video_segment_seconds": str(VIDEO_SECONDS_PER_PROMPT),
        "segment_seconds": str(IMAGE_SECONDS_PER_PROMPT),
        "target_image_prompt_count": image_prompt_count,
        "target_video_prompt_count": video_prompt_count,
        "target_prompt_count": image_prompt_count,
        "seconds_per_image_prompt": IMAGE_SECONDS_PER_PROMPT,
        "seconds_per_video_prompt": VIDEO_SECONDS_PER_PROMPT,
    }
