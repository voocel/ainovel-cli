from __future__ import annotations

import asyncio
import json
import re
from typing import Any

from app.core.assets import AssetBundle
from app.core.podcast_styles import PODCAST_ARCHITECT_ADDENDUM, is_podcast_style
from app.core.events import bus
from app.llm.client import llm_client
from app.repositories.repository import repo
from app.services import chapter_pipeline as cp
from app.services.flow import Instruction, load_state, route
from app.services.context import build_novel_context


class RuntimeManager:
    def __init__(self) -> None:
        self.tasks: dict[str, asyncio.Task] = {}
        self.abort_flags: set[str] = set()
        self._styles: dict[str, str] = {}

    async def emit(self, project_id: str, category: str, summary: str, level: str = "info", payload: Any | None = None) -> None:
        ev = repo.event(project_id, category, summary, level, payload)
        await bus.publish(project_id, ev)

    async def start(self, project_id: str, prompt: str, total_chapters: int = 5, style: str = "default") -> dict[str, Any]:
        if project_id in self.tasks and not self.tasks[project_id].done():
            raise RuntimeError("Project đang chạy")
        self.abort_flags.discard(project_id)
        self._styles[project_id] = style
        repo.update_progress(project_id, phase="init", flow="writing", current_chapter=0, total_chapters=total_chapters, total_word_count=0)
        task = asyncio.create_task(self._run(project_id, prompt, total_chapters, style))
        self.tasks[project_id] = task
        await self.emit(project_id, "SYSTEM", "Bắt đầu sáng tác")
        return {"running": True}

    async def resume(self, project_id: str) -> dict[str, Any]:
        progress = repo.progress(project_id)
        project = repo.get_project(project_id)
        style = project.get("style") or "default"
        total = int(progress.get("total_chapters") or 5)
        prompt = f"[Khôi phục] Tiếp tục từ chương {progress.get('current_chapter', 0)}"
        if project_id in self.tasks and not self.tasks[project_id].done():
            raise RuntimeError("Project đang chạy")
        self.abort_flags.discard(project_id)
        task = asyncio.create_task(self._run(project_id, prompt, total, style, resume=True))
        self.tasks[project_id] = task
        await self.emit(project_id, "SYSTEM", "Khôi phục phiên chạy")
        return {"running": True}

    async def continue_run(self, project_id: str, text: str) -> dict[str, Any]:
        repo.save_artifact(project_id, "directives", "continue", {"text": text}, "json")
        await self.emit(project_id, "USER", "Đã nhận chỉ thị continue", payload={"text": text})
        if not self.running(project_id):
            project = repo.get_project(project_id)
            progress = repo.progress(project_id)
            total = int(progress.get("total_chapters") or 5)
            await self.start(project_id, text, total, project.get("style") or "default")
        return {"ok": True}

    async def steer(self, project_id: str, text: str) -> dict[str, Any]:
        repo.save_artifact(project_id, "directives", "pending_steer", {"text": text}, "json")
        repo.update_progress(project_id, flow="steering", rewrite_reason=text)
        await self.emit(project_id, "USER", "Đã nhận can thiệp của người dùng", payload={"text": text})
        return {"ok": True}

    async def reopen_book(self, project_id: str, chapters: list[int], reason: str) -> dict[str, Any]:
        for ch in chapters:
            repo.push_queue(project_id, "pending_rewrites", ch)
        repo.update_progress(project_id, phase="writing", flow="rewriting", reopened_from_complete=1, rewrite_reason=reason)
        await self.emit(project_id, "SYSTEM", f"Mở lại sách để sửa {len(chapters)} chương", payload={"chapters": chapters})
        if not self.running(project_id):
            project = repo.get_project(project_id)
            progress = repo.progress(project_id)
            await self.resume(project_id)
        return {"ok": True, "queued": chapters}

    async def abort(self, project_id: str) -> dict[str, Any]:
        self.abort_flags.add(project_id)
        task = self.tasks.get(project_id)
        if task and not task.done():
            task.cancel()
        repo.update_progress(project_id, flow="writing")
        await self.emit(project_id, "SYSTEM", "Đã dừng phiên chạy", "warn")
        return {"aborted": True}

    def running(self, project_id: str) -> bool:
        task = self.tasks.get(project_id)
        return bool(task and not task.done())

    def _bundle(self, project_id: str, style: str) -> AssetBundle:
        return AssetBundle(style or self._styles.get(project_id, "default"))

    async def _emit_fn(self, project_id: str):
        async def _inner(cat: str, summary: str, level: str = "info", payload: Any | None = None):
            await self.emit(project_id, cat, summary, level, payload)
        return _inner

    async def _run(self, project_id: str, prompt: str, total_chapters: int, style: str, resume: bool = False) -> None:
        try:
            bundle = self._bundle(project_id, style)
            emit = await self._emit_fn(project_id)

            if not resume and not repo.has_foundation(project_id):
                architect_key = "architect-short" if total_chapters <= 25 else "architect-long"
                await self._architect(project_id, prompt, total_chapters, bundle, architect_key)

            while True:
                if project_id in self.abort_flags:
                    return

                progress = repo.progress(project_id)
                if progress.get("phase") == "complete":
                    await self.emit(project_id, "SYSTEM", "Tác phẩm đã hoàn thành", payload={"total_chapters": progress.get("total_chapters")})
                    break

                steer_art = repo.get_artifact(project_id, "directives", "pending_steer")
                if steer_art:
                    await self._handle_steer(project_id, steer_art, bundle, emit)
                    continue

                cont_art = repo.get_artifact(project_id, "directives", "continue")
                if cont_art and progress.get("flow") == "steering":
                    repo.clear_directive(project_id, "continue")

                state = load_state(project_id)
                instruction = route(state)

                if instruction is None:
                    current = int(progress.get("current_chapter") or 0)
                    total = int(progress.get("total_chapters") or total_chapters)
                    if current >= total and progress.get("phase") == "writing":
                        repo.update_progress(project_id, phase="complete", flow="writing")
                        continue
                    if progress.get("phase") != "writing":
                        await self._coordinator_planning(project_id, prompt, total_chapters, bundle, emit)
                        continue
                    next_ch = current + 1
                    if next_ch <= total:
                        instruction = Instruction("writer", f"Viết chương {next_ch}", "fallback route", next_ch)
                    else:
                        repo.update_progress(project_id, phase="complete", flow="writing")
                        continue

                if instruction.agent.startswith("architect"):
                    await self._architect(project_id, instruction.task, total_chapters, bundle, instruction.agent.replace("_", "-"))
                elif instruction.agent == "editor":
                    await self._editor_task(project_id, instruction, bundle, emit)
                elif instruction.agent == "writer":
                    if state.pending_rewrites:
                        await self._rewrite_chapter(project_id, instruction.chapter, bundle, emit, polish=progress.get("flow") == "polishing")
                        repo.shift_queue(project_id, "pending_rewrites")
                    else:
                        await self._write_chapter(project_id, instruction.chapter, bundle, emit)

                await asyncio.sleep(0.05)

        except asyncio.CancelledError:
            await self.emit(project_id, "SYSTEM", "Runtime đã bị huỷ", "warn")
        except Exception as exc:
            await self.emit(project_id, "ERROR", f"Runtime lỗi: {exc}", "error")
            raise

    async def _architect(self, project_id: str, prompt: str, total_chapters: int, bundle: AssetBundle, architect_key: str = "architect-long") -> None:
        podcast = is_podcast_style(bundle.style)
        await self.emit(
            project_id,
            "ARCHITECT",
            "Đang lập premise, outline tập và bối cảnh podcast" if podcast else "Đang lập premise, outline, nhân vật và thế giới",
        )
        key = architect_key if architect_key in bundle.prompts else "architect-long"
        arch_ref_keys = ["outline-template", *bundle.genre_reference_keys()]
        if not podcast:
            arch_ref_keys = ["outline-template", "character-template", "longform-planning", *bundle.genre_reference_keys()]
        refs = "\n\n".join(bundle.references.get(k, "")[:3000] for k in arch_ref_keys if k in bundle.references)
        style_block = bundle.style_text()
        architect_role = bundle.prompt(key, "Bạn là người thiết kế series podcast.") if podcast else bundle.prompt(key, "Bạn là kiến trúc sư truyện.")
        system = architect_role + "\n\n" + style_block
        if podcast:
            system += PODCAST_ARCHITECT_ADDENDUM
        system += "\n\n" + refs + "\n\nTrả về JSON: premise, outline[], characters[], world_rules[], compass{}"
        unit_label = "Số tập mục tiêu" if podcast else "Số chương mục tiêu"
        messages = [
            {"role": "system", "content": system},
            {"role": "user", "content": f"Yêu cầu: {prompt}\n{unit_label}: {total_chapters}"},
        ]
        resp = await llm_client.generate("architect", messages, project_id, agent_name=key)
        try:
            data = json.loads(resp.text)
        except Exception:
            data = {"premise": resp.text, "outline": [], "characters": [], "world_rules": []}
        repo.save_artifact(project_id, "foundation", "premise", data.get("premise", ""), "markdown")
        repo.save_artifact(project_id, "foundation", "outline", data.get("outline", []), "json")
        repo.save_artifact(project_id, "foundation", "characters", data.get("characters", []), "json")
        repo.save_artifact(project_id, "foundation", "world_rules", data.get("world_rules", []), "json")
        repo.save_artifact(project_id, "foundation", "compass", data.get("compass", {}), "json")
        if data.get("layered_outline"):
            repo.save_artifact(project_id, "foundation", "layered_outline", data["layered_outline"], "json")
        repo.checkpoint(project_id, "global", "foundation", "foundation/*", data)
        layered = 1 if total_chapters > 50 else 0
        repo.update_progress(project_id, phase="writing", total_chapters=total_chapters, layered=layered, current_volume=1, current_arc=1)
        await self.emit(project_id, "ARCHITECT", "Foundation đã sẵn sàng", payload={"provider": resp.provider, "model": resp.model})

    async def _coordinator_planning(self, project_id: str, prompt: str, total_chapters: int, bundle: AssetBundle, emit) -> None:
        context = build_novel_context(project_id, bundle=bundle)
        messages = [
            {"role": "system", "content": bundle.prompt("coordinator", "Bạn là điều phối viên.") + "\n\nTrả về JSON: action, agent, task"},
            {"role": "user", "content": json.dumps({"prompt": prompt, "context": context, "total_chapters": total_chapters}, ensure_ascii=False)},
        ]
        resp = await llm_client.generate("coordinator", messages, project_id, agent_name="coordinator")
        try:
            decision = json.loads(resp.text)
        except Exception:
            decision = {"agent": "architect_long", "task": prompt}
        agent = (decision.get("agent") or "architect_long").replace("_", "-")
        await self._architect(project_id, decision.get("task") or prompt, total_chapters, bundle, agent)

    async def _handle_steer(self, project_id: str, steer_art: dict, bundle: AssetBundle, emit) -> None:
        try:
            text = json.loads(steer_art["content"]).get("text", "")
        except Exception:
            text = steer_art.get("content", "")
        await self.emit(project_id, "COORDINATOR", "Đang xử lý can thiệp người dùng")
        messages = [
            {"role": "system", "content": bundle.prompt("coordinator", "Bạn là điều phối viên.") + "\n\nPhân loại can thiệp. Trả về JSON: type (continue|rewrite|style|scale), chapters[], directive, task"},
            {"role": "user", "content": f"[用户干预] {text}"},
        ]
        resp = await llm_client.generate("coordinator", messages, project_id, agent_name="coordinator-steer")
        try:
            decision = json.loads(resp.text)
        except Exception:
            decision = {"type": "continue"}
        steer_type = decision.get("type", "continue")
        if steer_type == "rewrite":
            chapters = decision.get("chapters") or decision.get("affected_chapters") or []
            if not chapters:
                m = re.search(r"chương\s*(\d+)", text, re.I)
                if m:
                    chapters = [int(m.group(1))]
            for ch in chapters:
                repo.push_queue(project_id, "pending_rewrites", int(ch))
            repo.update_progress(project_id, flow="rewriting")
        elif steer_type == "style":
            repo.save_artifact(project_id, "directives", f"directive_{len(repo.list_artifacts(project_id, 'directives'))}", decision.get("directive") or text, "markdown")
        elif steer_type == "scale":
            await self._architect(project_id, decision.get("task") or text, int(repo.progress(project_id).get("total_chapters") or 5), bundle)
        repo.clear_directive(project_id, "pending_steer")
        repo.update_progress(project_id, flow="writing")

    async def _write_chapter(self, project_id: str, chapter_no: int, bundle: AssetBundle, emit) -> None:
        repo.update_progress(project_id, in_progress_chapter=chapter_no, flow="writing")
        plan = await cp.plan_chapter(project_id, chapter_no, bundle, emit)
        draft = await cp.draft_chapter(project_id, chapter_no, plan, bundle, emit)
        check = await cp.check_consistency(project_id, chapter_no, draft, bundle, emit)
        if not check.get("passed"):
            draft = await cp.edit_chapter(project_id, chapter_no, draft, check.get("suggestions", ""), bundle, emit)
        review = await cp.review_chapter(project_id, chapter_no, draft, bundle, emit)
        pending = cp.apply_verdict(project_id, chapter_no, review)
        if pending:
            draft = await cp.edit_chapter(project_id, chapter_no, draft, review.get("notes", ""), bundle, emit, polish=(pending == "polish"))
            review = await cp.review_chapter(project_id, chapter_no, draft, bundle, emit)
        await cp.commit_chapter(project_id, chapter_no, draft, review, emit, bundle)
        await cp.create_artist_prompts(project_id, chapter_no, draft, bundle, emit)

    async def _rewrite_chapter(self, project_id: str, chapter_no: int, bundle: AssetBundle, emit, polish: bool = False) -> None:
        repo.update_progress(project_id, in_progress_chapter=chapter_no, flow="polishing" if polish else "rewriting")
        ch = repo.chapter(project_id, chapter_no)
        draft = ch.get("final_text") or ch.get("draft_text") or ""
        reason = repo.progress(project_id).get("rewrite_reason") or ""
        draft = await cp.edit_chapter(project_id, chapter_no, draft, reason, bundle, emit, polish=polish)
        check = await cp.check_consistency(project_id, chapter_no, draft, bundle, emit)
        if not check.get("passed"):
            draft = await cp.edit_chapter(project_id, chapter_no, draft, check.get("suggestions", ""), bundle, emit)
        review = await cp.review_chapter(project_id, chapter_no, draft, bundle, emit)
        await cp.commit_chapter(project_id, chapter_no, draft, review, emit, bundle)
        await cp.create_artist_prompts(project_id, chapter_no, draft, bundle, emit)

    async def _editor_task(self, project_id: str, instruction: Instruction, bundle: AssetBundle, emit) -> None:
        await self.emit(project_id, "EDITOR", instruction.task)
        progress = repo.progress(project_id)
        vol = int(progress.get("current_volume") or 1)
        arc = int(progress.get("current_arc") or 1)
        context = build_novel_context(project_id, bundle=bundle)
        messages = [
            {"role": "system", "content": bundle.prompt("editor", "Bạn là biên tập viên.") + "\n\nTrả về JSON summary."},
            {"role": "user", "content": json.dumps({"task": instruction.task, "context": context}, ensure_ascii=False)},
        ]
        resp = await llm_client.generate("editor", messages, project_id, agent_name="editor-arc")
        try:
            payload = json.loads(resp.text)
        except Exception:
            payload = {"summary": resp.text}
        if "arc" in instruction.task.lower() or "cung" in instruction.task.lower():
            repo.save_artifact(project_id, "summaries", f"arc_{vol}_{arc}", payload, "json")
        elif "volume" in instruction.task.lower():
            repo.save_artifact(project_id, "summaries", f"volume_{vol}", payload, "json")
        else:
            repo.save_review(project_id, payload)

    async def cocreate_stream(self, project_id: str, messages: list[dict[str, str]], style: str = "default") -> dict[str, Any]:
        bundle = self._bundle(project_id, style)
        system = bundle.prompt("coordinator", "Bạn là trợ lý đồng sáng tác.") + "\n\nHỏi đáp với người dùng để hoàn thiện brief trước khi viết."
        full = [{"role": "system", "content": system}, *messages]
        resp = await llm_client.generate("coordinator", full, project_id, agent_name="cocreate")
        return {"reply": resp.text, "provider": resp.provider, "model": resp.model}

    def novel_context(self, project_id: str, chapter_no: int | None = None) -> dict[str, Any]:
        style = self._styles.get(project_id) or repo.get_project(project_id).get("style", "default")
        return build_novel_context(project_id, chapter_no, AssetBundle(style))


runtime = RuntimeManager()
