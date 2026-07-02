from __future__ import annotations

import json
from typing import Any

from app.core.assets import AssetBundle
from app.repositories.repository import repo

REFERENCE_KEYS = [
    "anti-ai-tone",
    "chapter-guide",
    "consistency",
    "dialogue-writing",
    "character-building",
    "hook-techniques",
    "quality-checklist",
    "style-references",
    "arc-templates",
]


def _parse(content: str | None, content_type: str = "json") -> Any:
    if content is None:
        return None
    if content_type == "markdown":
        return content
    try:
        return json.loads(content)
    except Exception:
        return content


def build_novel_context(project_id: str, chapter_no: int | None = None, bundle: AssetBundle | None = None) -> dict[str, Any]:
    progress = repo.progress(project_id)
    chapters = repo.chapters(project_id)
    committed = [c for c in chapters if c.get("status") == "committed"]
    recent = committed[-5:]
    prev = repo.chapter(project_id, chapter_no - 1) if chapter_no and chapter_no > 1 else None

    foundation: dict[str, Any] = {}
    for key in ("premise", "outline", "characters", "world_rules", "compass", "layered_outline"):
        art = repo.get_artifact(project_id, "foundation", key)
        if art:
            foundation[key] = _parse(art["content"], art.get("content_type", "json"))

    directives = repo.list_artifacts(project_id, "directives")
    user_directives = [_parse(d["content"], d.get("content_type", "json")) for d in directives if d["key"] != "pending_steer"]

    sim = repo.get_artifact(project_id, "simulation", "profile")
    simulation_profile = _parse(sim["content"]) if sim else None

    refs: dict[str, str] = {}
    if bundle:
        for key in REFERENCE_KEYS:
            if key in bundle.references:
                refs[key] = bundle.references[key][:8000]
        rules = bundle.rules.get("default", "")
        if rules:
            refs["rules"] = rules[:6000]

    related = []
    if chapter_no and foundation.get("outline"):
        outline = foundation["outline"]
        if isinstance(outline, list):
            for item in outline:
                if isinstance(item, dict) and item.get("chapter") == chapter_no:
                    related.append(item)

    return {
        "progress_status": progress,
        "chapter": chapter_no,
        "foundation": foundation,
        "recent_chapters": [
            {
                "chapter_no": c["chapter_no"],
                "title": c.get("title"),
                "summary": _parse(c.get("summary_json") or ""),
                "tail": (c.get("final_text") or "")[-1500:],
            }
            for c in recent
        ],
        "previous_chapter_tail": (prev.get("final_text") or "")[-2000:] if prev else "",
        "simulation_profile": simulation_profile,
        "user_directives": user_directives,
        "reference_pack": {"references": refs, "style": bundle.style_text() if bundle else ""},
        "related_outline": related,
        "pending_rewrites": repo.get_queue(project_id, "pending_rewrites"),
        "loading_summary": f"chapter={chapter_no or 'global'} committed={len(committed)} refs={len(refs)}",
    }
