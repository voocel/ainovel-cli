from __future__ import annotations

import asyncio
import json
import uuid
from typing import Any

from fastapi import APIRouter, HTTPException, Query, WebSocket, WebSocketDisconnect
from fastapi.responses import PlainTextResponse
from pydantic import BaseModel, Field

from app.core.events import bus
from app.core.database import ROOT_DIR, tx
from app.core.providers import list_provider_presets, models_for_provider
from app.core.styles import list_styles, normalize_style
from app.services.artist_style import list_artist_styles, normalize_artist_style
from app.repositories.repository import now, repo
from app.core.assets import AssetBundle
from app.services import chapter_pipeline as cp
from app.services import diagnostics
from app.services.exporter import export_project
from app.services.importer import import_novel
from app.services.runtime import runtime
from app.services.simulation import build_simulation, import_profile

router = APIRouter()

class ProjectCreate(BaseModel):
    name: str = "Tác phẩm mới"
    style: str = "default"
    artist_style: str = "history_ink"
    output_dir: str | None = None

class StartRequest(BaseModel):
    prompt: str
    total_chapters: int = Field(default=5, ge=1, le=1000)
    style: str = "default"

class TextRequest(BaseModel):
    text: str

class ImportRequest(BaseModel):
    path: str
    from_chapter: int = 1
    style: str = "default"

class SimRequest(BaseModel):
    path: str
    style: str = "default"

class CoCreateRequest(BaseModel):
    messages: list[dict[str, str]]
    style: str = "default"

class ReopenRequest(BaseModel):
    chapters: list[int]
    reason: str = ""

class ExportRequest(BaseModel):
    path: str | None = None
    format: str = "txt"
    from_chapter: int = 1
    to_chapter: int | None = None
    overwrite: bool = False

class ProviderRequest(BaseModel):
    name: str
    type: str | None = None
    api_key: str | None = None
    base_url: str | None = None
    models: list[str] = []
    extra_body: dict[str, Any] = {}
    extra: dict[str, Any] = {}

class RoleModelRequest(BaseModel):
    role: str = "default"
    provider: str
    model: str
    thinking: str | None = None
    fallbacks: list[dict[str, str]] = []

class RegenerateArtistRequest(BaseModel):
    chapter_no: int = Field(ge=1)
    run_id: str | None = None

@router.api_route("/health", methods=["GET", "HEAD"])
def health():
    return {"ok": True, "app": "Story Clone", "api": 2}

@router.get("/projects")
def projects():
    return repo.list_projects()

@router.post("/projects")
def create_project(req: ProjectCreate):
    return repo.create_project(
        req.name,
        normalize_style(req.style),
        req.output_dir,
        normalize_artist_style(req.artist_style),
    )

@router.get("/projects/{project_id}")
def get_project(project_id: str):
    return repo.get_project(project_id)

@router.patch("/projects/{project_id}")
def patch_project(project_id: str, payload: dict[str, Any]):
    allowed = {"name", "style", "artist_style", "output_dir"}
    fields = {k: v for k, v in payload.items() if k in allowed}
    if fields:
        if "style" in fields:
            fields["style"] = normalize_style(fields["style"])
        if "artist_style" in fields:
            fields["artist_style"] = normalize_artist_style(fields["artist_style"])
        keys = ",".join(f"{k}=?" for k in fields)
        with tx(), repo.db() as con:
            con.execute(f"UPDATE projects SET {keys}, updated_at=? WHERE id=?", (*fields.values(), now(), project_id))
    return repo.get_project(project_id)

@router.delete("/projects/{project_id}")
async def delete_project(project_id: str):
    await runtime.abort(project_id)
    with tx(), repo.db() as con:
        con.execute("DELETE FROM projects WHERE id=?", (project_id,))
        con.execute("DELETE FROM artifacts WHERE project_id=?", (project_id,))
        con.execute("DELETE FROM checkpoints WHERE project_id=?", (project_id,))
        con.execute("DELETE FROM reviews WHERE project_id=?", (project_id,))
        con.execute("DELETE FROM agent_messages WHERE project_id=?", (project_id,))
        con.execute("DELETE FROM runtime_events WHERE project_id=?", (project_id,))
        con.execute("DELETE FROM usage_records WHERE project_id=?", (project_id,))
    return {"deleted": True}

@router.post("/projects/{project_id}/start")
async def start(project_id: str, req: StartRequest):
    style = normalize_style(req.style)
    with tx(), repo.db() as con:
        con.execute("UPDATE projects SET style=?, updated_at=? WHERE id=?", (style, now(), project_id))
    if req.prompt.strip():
        repo.save_artifact(project_id, "directives", "story_brief", req.prompt.strip(), "markdown")
    try:
        return await runtime.start(project_id, req.prompt, req.total_chapters, style)
    except RuntimeError as exc:
        raise HTTPException(status_code=409, detail=str(exc)) from exc

@router.post("/projects/{project_id}/resume")
async def resume(project_id: str):
    return await runtime.resume(project_id)

@router.post("/projects/{project_id}/continue")
async def continue_run(project_id: str, req: TextRequest):
    return await runtime.continue_run(project_id, req.text)

@router.post("/projects/{project_id}/steer")
async def steer(project_id: str, req: TextRequest):
    return await runtime.steer(project_id, req.text)

@router.post("/projects/{project_id}/abort")
async def abort(project_id: str):
    return await runtime.abort(project_id)

@router.get("/projects/{project_id}/snapshot")
def snapshot(project_id: str):
    progress = repo.progress(project_id)
    story_brief = repo.get_artifact(project_id, "directives", "story_brief")
    premise = repo.get_artifact(project_id, "foundation", "premise")
    return {
        "project": repo.get_project(project_id),
        "chapters": repo.chapters(project_id),
        "running": runtime.running(project_id),
        "events": repo.events(project_id),
        "pending_rewrites": repo.get_queue(project_id, "pending_rewrites"),
        "usage": repo.usage_summary(project_id),
        "progress": progress,
        "story_brief": story_brief.get("content") if story_brief else "",
        "premise": premise.get("content") if premise else "",
    }

@router.get("/projects/{project_id}/events")
def events(project_id: str, after_id: int = 0):
    return repo.events(project_id, after_id)

@router.websocket("/projects/{project_id}/stream")
async def stream(project_id: str, websocket: WebSocket):
    await websocket.accept()
    try:
        async for event in bus.subscribe(project_id):
            await websocket.send_text(json.dumps(event, ensure_ascii=False))
    except WebSocketDisconnect:
        return
    except asyncio.CancelledError:
        # #region agent log
        import time as _time
        try:
            with open(ROOT_DIR / "debug-417abf.log", "a", encoding="utf-8") as _lf:
                _lf.write(json.dumps({"sessionId": "417abf", "runId": "reload-fix", "hypothesisId": "A", "location": "routes.py:stream", "message": "ws cancelled on shutdown/reload", "data": {"project_id": project_id}, "timestamp": int(_time.time() * 1000)}, ensure_ascii=False) + "\n")
        except Exception:
            pass
        # #endregion
        return

@router.get("/projects/{project_id}/chapters")
def chapters(project_id: str):
    return repo.chapters(project_id)

@router.get("/projects/{project_id}/chapters/{chapter_no}")
def chapter(project_id: str, chapter_no: int):
    return repo.chapter(project_id, chapter_no) or {}

@router.patch("/projects/{project_id}/chapters/{chapter_no}")
def patch_chapter(project_id: str, chapter_no: int, payload: dict[str, Any]):
    repo.save_chapter(project_id, chapter_no, payload.get("title") or f"Chương {chapter_no}", draft=payload.get("draft_text"), final=payload.get("final_text"), status=payload.get("status", "draft"))
    return repo.chapter(project_id, chapter_no)

@router.get("/projects/{project_id}/outline")
def outline(project_id: str):
    return repo.get_artifact(project_id, "foundation", "outline") or {"content": "[]"}

@router.patch("/projects/{project_id}/outline")
def patch_outline(project_id: str, payload: Any):
    repo.save_artifact(project_id, "foundation", "outline", payload)
    return repo.get_artifact(project_id, "foundation", "outline")

@router.get("/projects/{project_id}/characters")
def characters(project_id: str):
    return repo.get_artifact(project_id, "foundation", "characters") or {"content": "[]"}

@router.get("/projects/{project_id}/world")
def world(project_id: str):
    return repo.get_artifact(project_id, "foundation", "world_rules") or {"content": "[]"}


@router.get("/projects/{project_id}/artist-prompts")
def artist_prompts(project_id: str):
    items = repo.list_artifacts(project_id, "artist_prompts")
    def sort_key(item: dict[str, Any]) -> int:
        key = str(item.get("key") or "")
        try:
            return int(key.rsplit("_", 1)[-1])
        except Exception:
            return 0
    return sorted(items, key=sort_key)


async def _run_regenerate_artist_prompts(project_id: str, chapter_no: int, run_id: str | None = None) -> dict[str, Any]:
    chapter = repo.chapter(project_id, chapter_no)
    if not chapter:
        raise HTTPException(status_code=404, detail="Không tìm thấy chương")
    text = (chapter.get("final_text") or chapter.get("draft_text") or "").strip()
    if not text:
        raise HTTPException(status_code=400, detail="Chương chưa có nội dung để tạo prompt")
    progress = repo.progress(project_id) or {}
    bundle = AssetBundle(normalize_style(progress.get("style") or "default"))
    regen_run_id = run_id or str(uuid.uuid4())

    async def emit(category: str, summary: str, level: str = "info", payload: Any | None = None) -> None:
        merged = dict(payload) if isinstance(payload, dict) else ({"data": payload} if payload is not None else {})
        merged["run_id"] = regen_run_id
        ev = repo.event(project_id, category, summary, level, merged)
        await bus.publish(project_id, ev)

    await emit(
        "ARTIST",
        f"Bắt đầu tạo lại prompt chương {chapter_no}",
        "info",
        {"chapter": chapter_no, "phase": "start", "current": 0, "target": 0},
    )
    result = await cp.create_artist_prompts(project_id, chapter_no, text, bundle, emit)
    result["run_id"] = regen_run_id
    return result


@router.post("/projects/{project_id}/artist-prompts/regenerate")
async def regenerate_artist_prompts_body(project_id: str, req: RegenerateArtistRequest):
    return await _run_regenerate_artist_prompts(project_id, req.chapter_no, req.run_id)


@router.post("/projects/{project_id}/chapters/{chapter_no}/artist-prompts/regenerate")
async def regenerate_artist_prompts(project_id: str, chapter_no: int):
    return await _run_regenerate_artist_prompts(project_id, chapter_no)
@router.get("/projects/{project_id}/reviews")
def reviews(project_id: str):
    return repo.reviews(project_id)

@router.get("/projects/{project_id}/diagnostics")
def diag(project_id: str):
    return diagnostics.analyze(project_id)

@router.post("/projects/{project_id}/diagnostics/export", response_class=PlainTextResponse)
def diag_export(project_id: str):
    return diagnostics.export(project_id)

@router.post("/projects/{project_id}/reopen")
async def reopen_book(project_id: str, req: ReopenRequest):
    return await runtime.reopen_book(project_id, req.chapters, req.reason)

@router.post("/projects/{project_id}/cocreate")
async def cocreate(project_id: str, req: CoCreateRequest):
    return await runtime.cocreate_stream(project_id, req.messages, req.style)

@router.get("/projects/{project_id}/usage")
def project_usage(project_id: str):
    return repo.usage_summary(project_id)

@router.post("/projects/{project_id}/import")
async def import_endpoint(project_id: str, req: ImportRequest):
    project = repo.get_project(project_id)
    result = await import_novel(project_id, req.path, req.from_chapter, req.style or project.get("style", "default"))
    await runtime.emit(project_id, "IMPORT", f"Đã import {result['imported']} chương")
    return result

@router.post("/projects/{project_id}/simulate")
async def simulate(project_id: str, req: SimRequest):
    project = repo.get_project(project_id)
    result = await build_simulation(project_id, req.path, req.style or project.get("style", "default"))
    await runtime.emit(project_id, "SIMULATION", f"Đã cập nhật simulation profile từ {result['sources']} nguồn")
    return result

@router.post("/projects/{project_id}/simulation/import")
async def import_sim(project_id: str, req: SimRequest):
    result = await import_profile(project_id, req.path)
    await runtime.emit(project_id, "SIMULATION", "Đã import simulation profile")
    return result

@router.post("/projects/{project_id}/export")
def export_endpoint(project_id: str, req: ExportRequest):
    return export_project(project_id, req.path, req.format, req.from_chapter, req.to_chapter, req.overwrite)

@router.get("/settings/styles")
def styles_catalog():
    return list_styles()


@router.get("/settings/artist-styles")
def artist_styles_catalog():
    return list_artist_styles()

@router.get("/settings/providers/presets")
def provider_presets():
    return list_provider_presets()

@router.get("/settings/providers")
def providers():
    return repo.providers()

@router.post("/settings/providers")
def save_provider(req: ProviderRequest):
    repo.save_provider(req.model_dump())
    return {"ok": True}

@router.patch("/settings/providers/{name}")
def update_provider(name: str, payload: dict[str, Any]):
    payload["name"] = name
    repo.save_provider(payload)
    return {"ok": True}

@router.delete("/settings/providers/{name}")
def delete_provider(name: str):
    with tx(), repo.db() as con:
        con.execute("DELETE FROM providers WHERE name=?", (name,))
    return {"deleted": True}

@router.get("/settings/models")
def models():
    roles = repo.role_models()
    providers = repo.providers()
    catalog = {p["name"]: models_for_provider(p["name"], p.get("models") or []) for p in providers}
    for preset in list_provider_presets():
        catalog.setdefault(preset["name"], preset.get("models") or [])
    return {"roles": roles, "providers": providers, "catalog": catalog, "presets": list_provider_presets()}

@router.patch("/settings/models/roles")
def set_role_model(req: RoleModelRequest):
    repo.set_role_model(req.role, req.provider, req.model, req.thinking, req.fallbacks)
    return {"ok": True}

@router.delete("/settings/models/roles/{role}")
def delete_role_model(role: str):
    with tx(), repo.db() as con:
        con.execute("DELETE FROM role_models WHERE role=?", (role,))
    return {"deleted": True}

@router.get("/usage")
def usage(project_id: str | None = Query(default=None)):
    return repo.usage_summary(project_id)



