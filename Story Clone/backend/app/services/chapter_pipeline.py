from __future__ import annotations

import json
from typing import Any, Awaitable, Callable

from app.core.assets import AssetBundle
from app.core.podcast_styles import (
    PODCAST_EDITOR_ADDENDUM,
    PODCAST_WRITER_ADDENDUM,
    episode_label,
    is_podcast_style,
)
from app.llm.client import llm_client
from app.repositories.repository import repo
from app.services.artist_style import (
    default_style_notes,
    strip_artist_visual_style,
    visual_negative_default,
    visual_style_prefix,
)
from app.services.artist_timing import IMAGE_BATCH_SIZE, VIDEO_SECONDS_PER_PROMPT, estimate_narration_timing, split_narration_segments
from app.services.context import build_novel_context

EmitFn = Callable[[str, str, str, str, Any | None], Awaitable[None]]


def _unit_label(bundle: AssetBundle, chapter_no: int) -> str:
    return episode_label(chapter_no) if is_podcast_style(bundle.style) else f"Chương {chapter_no}"


def _writer_role(bundle: AssetBundle) -> str:
    if is_podcast_style(bundle.style):
        return bundle.prompt("writer", "Bạn là người viết kịch bản podcast.")
    return bundle.prompt("writer", "Bạn là nhà văn.")


def _writer_system(bundle: AssetBundle, *, include_style: bool = False, extra: str = "") -> str:
    system = _writer_role(bundle)
    if include_style:
        system += "\n\n" + bundle.style_text()
    if is_podcast_style(bundle.style):
        system += PODCAST_WRITER_ADDENDUM
    if extra:
        system += extra
    return system


async def plan_chapter(project_id: str, chapter_no: int, bundle: AssetBundle, emit: EmitFn) -> dict[str, Any]:
    context = build_novel_context(project_id, chapter_no, bundle)
    outline = context.get("foundation", {}).get("outline") or []
    goal = "Đẩy xung đột chính"
    if isinstance(outline, list):
        for item in outline:
            if isinstance(item, dict) and item.get("chapter") == chapter_no:
                goal = item.get("goal") or item.get("title") or goal
                break
    unit = _unit_label(bundle, chapter_no)
    default_hook = "Giữ teaser cuối tập" if is_podcast_style(bundle.style) else "Giữ móc cuối chương"
    messages = [
        {
            "role": "system",
            "content": _writer_system(bundle, include_style=True)
            + "\n\nTrả về JSON: {\"goal\": \"...\", \"required_beats\": [], \"hook_goal\": \"...\"}",
        },
        {"role": "user", "content": json.dumps({"chapter": chapter_no, "context": context, "default_goal": goal}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("writer", messages, project_id, agent_name="writer-plan")
    try:
        plan = json.loads(resp.text)
    except Exception:
        plan = {"chapter": chapter_no, "goal": goal, "required_beats": [], "hook_goal": default_hook}
    plan["chapter"] = chapter_no
    repo.save_chapter(project_id, chapter_no, unit, plan=plan, status="planned")
    repo.checkpoint(project_id, "chapter", "plan", f"chapters/{chapter_no}/plan", plan, chapter=chapter_no)
    await emit("WRITER", f"Đã lập kế hoạch {unit.lower()}", "info", {"plan": plan})
    return plan


async def draft_chapter(project_id: str, chapter_no: int, plan: dict[str, Any], bundle: AssetBundle, emit: EmitFn, mode: str = "write") -> str:
    context = build_novel_context(project_id, chapter_no, bundle)
    unit = _unit_label(bundle, chapter_no)
    system = _writer_system(bundle, include_style=True)
    ref_keys = ["anti-ai-tone", "hook-techniques"] if is_podcast_style(bundle.style) else ["anti-ai-tone", "dialogue-writing", "hook-techniques"]
    for ref in ref_keys:
        if ref in bundle.references:
            system += f"\n\n## {ref}\n{bundle.references[ref][:4000]}"
    user_payload = {"chapter": chapter_no, "plan": plan, "context": context, "mode": mode}
    messages = [{"role": "system", "content": system}, {"role": "user", "content": json.dumps(user_payload, ensure_ascii=False)}]
    resp = await llm_client.generate("writer", messages, project_id, agent_name="writer-draft")
    draft = resp.text.strip()
    repo.save_chapter(project_id, chapter_no, unit, draft=draft, status="draft")
    repo.checkpoint(project_id, "chapter", "draft", f"chapters/{chapter_no}/draft", draft, chapter=chapter_no)
    await emit("WRITER", f"Đã viết draft {unit.lower()}", "info", {"words": len(draft.split())})
    return draft


async def check_consistency(project_id: str, chapter_no: int, draft: str, bundle: AssetBundle, emit: EmitFn) -> dict[str, Any]:
    context = build_novel_context(project_id, chapter_no, bundle)
    messages = [
        {"role": "system", "content": "Bạn là kiểm tra nhất quán. Trả về JSON: {\"passed\": bool, \"issues\": [], \"suggestions\": \"\"}"},
        {"role": "user", "content": json.dumps({"chapter": chapter_no, "draft": draft[:12000], "context": context}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("writer", messages, project_id, agent_name="writer-check")
    try:
        result = json.loads(resp.text)
    except Exception:
        result = {"passed": True, "issues": [], "suggestions": ""}
    repo.checkpoint(project_id, "chapter", "consistency", f"chapters/{chapter_no}/check", result, chapter=chapter_no)
    if not result.get("passed"):
        await emit("WRITER", f"Phát hiện vấn đề nhất quán chương {chapter_no}", "warn", result)
    return result


async def edit_chapter(project_id: str, chapter_no: int, draft: str, feedback: str, bundle: AssetBundle, emit: EmitFn, polish: bool = False) -> str:
    context = build_novel_context(project_id, chapter_no, bundle)
    unit = _unit_label(bundle, chapter_no)
    task = "Đánh bóng" if polish else "Viết lại"
    unit_word = "tập" if is_podcast_style(bundle.style) else "chương"
    messages = [
        {
            "role": "system",
            "content": _writer_system(bundle, include_style=True, extra=f"\n\nNhiệm vụ: {task} {unit_word} theo phản hồi biên tập."),
        },
        {"role": "user", "content": json.dumps({"chapter": chapter_no, "original": draft[:12000], "feedback": feedback, "context": context}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("writer", messages, project_id, agent_name="writer-edit")
    revised = resp.text.strip()
    repo.save_chapter(project_id, chapter_no, unit, draft=revised, status="draft")
    repo.checkpoint(project_id, "chapter", "edit", f"chapters/{chapter_no}/edit", revised, chapter=chapter_no)
    await emit("WRITER", f"{task} {unit.lower()}", "info")
    return revised


async def review_chapter(project_id: str, chapter_no: int, draft: str, bundle: AssetBundle, emit: EmitFn) -> dict[str, Any]:
    repo.update_progress(project_id, flow="reviewing")
    context = build_novel_context(project_id, chapter_no, bundle)
    unit = _unit_label(bundle, chapter_no)
    editor_system = bundle.prompt("editor", "Bạn là biên tập viên.")
    if is_podcast_style(bundle.style):
        editor_system += PODCAST_EDITOR_ADDENDUM
    editor_system += "\n\nTrả về JSON với verdict: accept|polish|rewrite, score, dimensions, notes, affected_chapters."
    messages = [
        {"role": "system", "content": editor_system},
        {"role": "user", "content": json.dumps({"chapter": chapter_no, "draft": draft[:12000], "context": context}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("editor", messages, project_id, agent_name="editor")
    try:
        review = json.loads(resp.text)
    except Exception:
        review = {"verdict": "accept", "score": 75, "notes": resp.text}
    
    # Chuẩn hóa các trường dữ liệu để tránh lỗi SQLite binding (ví dụ: notes hoặc verdict là list)
    verdict = review.get("verdict")
    review["verdict"] = str(verdict).lower() if verdict else "accept"
    
    try:
        review["score"] = float(review.get("score", 75))
    except Exception:
        review["score"] = 75.0
        
    notes = review.get("notes", "")
    if isinstance(notes, list):
        review["notes"] = "\n".join(str(n) for n in notes)
    elif not isinstance(notes, str):
        review["notes"] = str(notes) if notes is not None else ""
        
    repo.save_review(project_id, review, chapter_no=chapter_no)
    repo.checkpoint(project_id, "chapter", "review", f"reviews/{chapter_no}", review, chapter=chapter_no)
    await emit("EDITOR", f"Review {unit.lower()}: {review.get('verdict')}", "info", {"score": review.get("score")})
    return review



async def _generate_image_prompt_batch(
    project_id: str,
    chapter_no: int,
    segments: list[dict[str, Any]],
    bundle: AssetBundle,
    context: dict[str, Any],
    chapter: dict[str, Any],
    artist_style: str,
) -> list[dict[str, Any]]:
    required = len(segments)
    style_prefix = visual_style_prefix(artist_style)
    neg_default = visual_negative_default(artist_style)
    system = bundle.prompt("artist-image-batch", bundle.prompt("artist", "Bạn là hoạ sĩ AI.")) + "\n\n" + bundle.style_text()
    user_payload = {
        "chapter": chapter_no,
        "title": chapter.get("title") or _unit_label(bundle, chapter_no),
        "required_count": required,
        "segments": segments,
        "visual_style_prefix": style_prefix,
        "artist_style": artist_style,
        "context": {
            "characters": (context.get("foundation") or {}).get("characters"),
            "style": context.get("style"),
            "reference_pack": context.get("reference_pack"),
        },
    }
    messages = [
        {"role": "system", "content": system + "\n\nChỉ trả về JSON hợp lệ, không bọc Markdown."},
        {"role": "user", "content": json.dumps(user_payload, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("artist", messages, project_id, agent_name="artist-image-batch")
    try:
        payload = json.loads(resp.text)
    except Exception:
        payload = {}
    items = payload.get("image_prompts") if isinstance(payload, dict) else []
    if not isinstance(items, list) or len(items) < required:
        resp = await llm_client.generate("artist", messages, project_id, agent_name="artist-image-batch-retry")
        try:
            payload = json.loads(resp.text)
            items = payload.get("image_prompts") if isinstance(payload, dict) else []
        except Exception:
            items = []
    if not isinstance(items, list):
        items = []
    out: list[dict[str, Any]] = []
    for i, seg in enumerate(segments):
        item = items[i] if i < len(items) and isinstance(items[i], dict) else {}
        out.append(
            {
                "segment_index": seg["index"],
                "scene": item.get("scene") or f"Đoạn {seg['index']}",
                "moment": item.get("moment") or seg.get("excerpt", "")[:120],
                "source_excerpt": item.get("source_excerpt") or seg.get("excerpt", ""),
                "prompt": strip_artist_visual_style(item.get("prompt") or seg.get("excerpt", ""), artist_style),
                "negative_prompt": item.get("negative_prompt") or neg_default,
                "style_notes": default_style_notes(item.get("style_notes"), artist_style),
                "characters": item.get("characters") if isinstance(item.get("characters"), list) else [],
            }
        )
    return out


async def _generate_video_prompt_batch(
    project_id: str,
    chapter_no: int,
    segments: list[dict[str, Any]],
    bundle: AssetBundle,
    context: dict[str, Any],
    chapter: dict[str, Any],
    artist_style: str,
) -> list[dict[str, Any]]:
    required = len(segments)
    style_prefix = visual_style_prefix(artist_style)
    neg_default = visual_negative_default(artist_style)
    system = bundle.prompt("artist-video-batch", bundle.prompt("artist", "Bạn là hoạ sĩ AI.")) + "\n\n" + bundle.style_text()
    user_payload = {
        "chapter": chapter_no,
        "title": chapter.get("title") or _unit_label(bundle, chapter_no),
        "required_count": required,
        "segments": segments,
        "visual_style_prefix": style_prefix,
        "artist_style": artist_style,
        "context": {
            "characters": (context.get("foundation") or {}).get("characters"),
            "style": context.get("style"),
            "reference_pack": context.get("reference_pack"),
        },
    }
    messages = [
        {"role": "system", "content": system + "\n\nChỉ trả về JSON hợp lệ, không bọc Markdown."},
        {"role": "user", "content": json.dumps(user_payload, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("artist", messages, project_id, agent_name="artist-video-batch")
    try:
        payload = json.loads(resp.text)
    except Exception:
        payload = {}
    items = payload.get("video_prompts") if isinstance(payload, dict) else []
    if not isinstance(items, list):
        items = []
    out: list[dict[str, Any]] = []
    for i, seg in enumerate(segments):
        item = items[i] if i < len(items) and isinstance(items[i], dict) else {}
        excerpt = seg.get("excerpt", "")
        out.append(
            {
                "segment_index": seg["index"],
                "scene": item.get("scene") or f"Đoạn {seg['index']}",
                "duration": "20s",
                "prompt": strip_artist_visual_style(item.get("prompt") or f"Cảnh video minh họa: {excerpt[:200]}", artist_style),
                "negative_prompt": item.get("negative_prompt") or neg_default,
                "camera": item.get("camera") or "slow pan, medium shot",
                "motion": item.get("motion") or "chuyển động nhẹ theo nhịp kể",
                "sound_mood": item.get("sound_mood") or "thuyết minh lịch sử",
            }
        )
    return out


async def _generate_video_prompts(
    project_id: str,
    chapter_no: int,
    final_text: str,
    video_segments: list[dict[str, Any]],
    bundle: AssetBundle,
    context: dict[str, Any],
    chapter: dict[str, Any],
    emit: EmitFn,
    artist_style: str,
) -> list[dict[str, Any]]:
    video_prompts: list[dict[str, Any]] = []
    for i in range(0, len(video_segments), IMAGE_BATCH_SIZE):
        batch = video_segments[i : i + IMAGE_BATCH_SIZE]
        batch_prompts = await _generate_video_prompt_batch(project_id, chapter_no, batch, bundle, context, chapter, artist_style)
        video_prompts.extend(batch_prompts)
        await emit(
            "ARTIST",
            f"Đã tạo prompt video chương {chapter_no}: {len(video_prompts)}/{len(video_segments)}",
            "info",
            {"chapter": chapter_no, "phase": "video", "current": len(video_prompts), "target": len(video_segments)},
        )
    return video_prompts


async def create_artist_prompts(project_id: str, chapter_no: int, final_text: str, bundle: AssetBundle, emit: EmitFn) -> dict[str, Any]:
    project = repo.get_project(project_id)
    artist_style = str(project.get("artist_style") or "history_ink")
    context = build_novel_context(project_id, chapter_no, bundle)
    chapter = repo.chapter(project_id, chapter_no) or {}
    segments = split_narration_segments(final_text)
    video_segments = split_narration_segments(final_text, VIDEO_SECONDS_PER_PROMPT)
    narration_timing = estimate_narration_timing(final_text)
    narration_timing["target_image_prompt_count"] = len(segments)
    narration_timing["target_video_prompt_count"] = len(video_segments)
    narration_timing["target_prompt_count"] = len(segments)
    narration_timing["artist_style"] = artist_style

    image_prompts: list[dict[str, Any]] = []
    for i in range(0, len(segments), IMAGE_BATCH_SIZE):
        batch = segments[i : i + IMAGE_BATCH_SIZE]
        batch_prompts = await _generate_image_prompt_batch(project_id, chapter_no, batch, bundle, context, chapter, artist_style)
        image_prompts.extend(batch_prompts)
        await emit(
            "ARTIST",
            f"Đã tạo prompt ảnh chương {chapter_no}: {len(image_prompts)}/{len(segments)}",
            "info",
            {"chapter": chapter_no, "phase": "image", "current": len(image_prompts), "target": len(segments)},
        )

    video_prompts = await _generate_video_prompts(project_id, chapter_no, final_text, video_segments, bundle, context, chapter, emit, artist_style)

    payload: dict[str, Any] = {
        "chapter": chapter_no,
        "image_prompts": image_prompts,
        "video_prompts": video_prompts,
        "narration_timing": narration_timing,
        "artist_style": artist_style,
    }
    repo.save_artifact(project_id, "artist_prompts", f"chapter_{chapter_no}", payload, "json")
    repo.checkpoint(project_id, "chapter", "artist_prompts", f"artist_prompts/chapter_{chapter_no}", payload, chapter=chapter_no)
    await emit(
        "ARTIST",
        f"Đã tạo prompt hoạ sĩ cho chương {chapter_no}",
        "info",
        {
            "chapter": chapter_no,
            "phase": "done",
            "current": len(image_prompts) + len(video_prompts),
            "target": len(segments) + len(video_segments),
            "images": len(image_prompts),
            "videos": len(video_prompts),
            "estimated_audio_seconds": narration_timing["estimated_audio_seconds"],
            "target_prompt_count": len(segments),
            "target_image_prompt_count": len(segments),
            "target_video_prompt_count": len(video_segments),
        },
    )
    return payload
def apply_verdict(project_id: str, chapter_no: int, review: dict[str, Any]) -> str | None:
    verdict = (review.get("verdict") or "accept").lower()
    if verdict == "rewrite":
        repo.push_queue(project_id, "pending_rewrites", chapter_no)
        repo.update_progress(project_id, flow="rewriting", rewrite_reason=review.get("notes", ""))
        return "rewrite"
    if verdict == "polish":
        repo.push_queue(project_id, "pending_rewrites", chapter_no)
        repo.update_progress(project_id, flow="polishing", rewrite_reason=review.get("notes", ""))
        return "polish"
    return None


async def commit_chapter(project_id: str, chapter_no: int, final_text: str, review: dict[str, Any], emit: EmitFn, bundle: AssetBundle | None = None) -> None:
    project = repo.get_project(project_id)
    style = (bundle.style if bundle else None) or str(project.get("style") or "default")
    unit = episode_label(chapter_no) if is_podcast_style(style) else f"Chương {chapter_no}"
    summary = {"chapter": chapter_no, "summary": final_text[:400], "review_verdict": review.get("verdict")}
    repo.save_chapter(project_id, chapter_no, unit, final=final_text, summary=summary, status="committed")
    repo.checkpoint(project_id, "chapter", "commit", f"chapters/{chapter_no}/final", final_text, chapter=chapter_no)
    chapters = repo.chapters(project_id)
    total_words = sum(int(c.get("word_count") or 0) for c in chapters if c.get("status") == "committed")
    repo.update_progress(project_id, current_chapter=chapter_no, in_progress_chapter=0, total_word_count=total_words, flow="writing")
    await emit("WRITER", f"Đã commit {unit.lower()}", "info", {"words": len(final_text.split())})
