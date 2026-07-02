from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any

from app.repositories.repository import repo


@dataclass
class Instruction:
    agent: str
    task: str
    reason: str = ""
    chapter: int = 0


@dataclass
class RouteState:
    progress: dict[str, Any]
    last_completed: int = 0
    pending_rewrites: list[int] = field(default_factory=list)
    foundation_missing: list[str] = field(default_factory=list)
    has_arc_review: bool = False
    has_arc_summary: bool = False
    has_volume_summary: bool = False
    arc_is_end: bool = False
    volume_is_end: bool = False


def load_state(project_id: str) -> RouteState:
    progress = repo.progress(project_id)
    chapters = repo.chapters(project_id)
    committed = [c for c in chapters if c.get("status") == "committed"]
    last_completed = max((c["chapter_no"] for c in committed), default=0)
    pending = repo.get_queue(project_id, "pending_rewrites")
    missing: list[str] = []
    for key in ("premise", "outline", "characters", "world_rules"):
        if not repo.get_artifact(project_id, "foundation", key):
            missing.append(key)
    return RouteState(
        progress=progress,
        last_completed=last_completed,
        pending_rewrites=pending,
        foundation_missing=missing,
    )


def route(state: RouteState) -> Instruction | None:
    p = state.progress
    phase = p.get("phase") or "init"

    if phase == "complete":
        return None

    if phase != "writing":
        if state.foundation_missing:
            agent = "architect_short" if int(p.get("total_chapters") or 0) <= 25 else "architect_long"
            return Instruction(agent, f"Bổ sung foundation: {', '.join(state.foundation_missing)}", "Thiếu artifact nền")
        return None

    if state.pending_rewrites:
        ch = state.pending_rewrites[0]
        verb = "Đánh bóng" if p.get("flow") == "polishing" else "Viết lại"
        return Instruction("writer", f"{verb} chương {ch}", f"Còn {len(state.pending_rewrites)} chương trong hàng đợi", ch)

    if p.get("flow") == "reviewing":
        return None

    if p.get("flow") == "steering":
        return None

    if p.get("layered") and state.arc_is_end:
        vol = int(p.get("current_volume") or 1)
        arc = int(p.get("current_arc") or 1)
        if not state.has_arc_review:
            return Instruction("editor", f"Đánh giá cung {vol}/{arc}", "Thiếu arc review")
        if not state.has_arc_summary:
            return Instruction("editor", f"Tóm tắt cung {vol}/{arc}", "Thiếu arc summary")
        if state.volume_is_end and not state.has_volume_summary:
            return Instruction("editor", f"Tóm tắt volume {vol}", "Thiếu volume summary")

    current = int(p.get("current_chapter") or 0)
    total = int(p.get("total_chapters") or 0)
    if current >= total:
        return None

    return Instruction("writer", f"Viết chương {current + 1}", "Tiếp tục pipeline viết chương", current + 1)
