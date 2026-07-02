from __future__ import annotations

import json
import re
from pathlib import Path

from app.core.assets import AssetBundle
from app.llm.client import llm_client
from app.repositories.repository import repo

CHAPTER_RE = re.compile(r"(?m)^(?:#{1,3}\s*)?(第\s*[\d一二三四五六七八九十百千]+\s*[章节回话卷幕]|Chapter\s+\d+|Chương\s+\d+|序章|楔子|尾声|番外|外传).*$", re.IGNORECASE)

TAG_RE = {
    "premise": re.compile(r"===\s*PREMISE\s*===(.*?)===\s*CHARACTERS\s*===", re.S | re.I),
    "characters": re.compile(r"===\s*CHARACTERS\s*===(.*?)===\s*WORLD", re.S | re.I),
    "world": re.compile(r"===\s*WORLD[_\s]*RULES?\s*===(.*?)===\s*OUTLINE", re.S | re.I),
    "outline": re.compile(r"===\s*OUTLINE\s*===(.*?)===\s*COMPASS\s*===", re.S | re.I),
    "compass": re.compile(r"===\s*COMPASS\s*===(.*)$", re.S | re.I),
}


def split_text(text: str) -> list[tuple[str, str]]:
    matches = list(CHAPTER_RE.finditer(text))
    if not matches:
        return [("Chương 1", text.strip())] if text.strip() else []
    chapters = []
    for i, m in enumerate(matches):
        start = m.end()
        end = matches[i + 1].start() if i + 1 < len(matches) else len(text)
        title = m.group(0).strip("# \t")
        body = text[start:end].strip()
        chapters.append((title, body))
    return chapters


def _parse_foundation(text: str) -> dict:
    out: dict = {}
    for key, pattern in TAG_RE.items():
        m = pattern.search(text)
        if not m:
            continue
        block = m.group(1).strip()
        if key == "premise":
            out["premise"] = block
        elif key in {"characters", "outline", "compass", "world"}:
            try:
                out[key if key != "world" else "world_rules"] = json.loads(block)
            except Exception:
                out[key if key != "world" else "world_rules"] = block
    return out


async def reverse_foundation(chapters: list[tuple[str, str]], bundle: AssetBundle, project_id: str) -> dict:
    sample = "\n\n".join(f"## {t}\n{b[:8000]}" for t, b in chapters[:5])
    prompt = bundle.prompt("import-foundation", "Bạn là chuyên gia phân tích tiểu thuyết.")
    messages = [
        {"role": "system", "content": prompt},
        {"role": "user", "content": f"Phân tích {len(chapters)} chương. Mẫu:\n\n{sample[:50000]}"},
    ]
    resp = await llm_client.generate("architect", messages, project_id, agent_name="import-foundation")
    parsed = _parse_foundation(resp.text)
    if not parsed:
        try:
            parsed = json.loads(resp.text)
        except Exception:
            parsed = {"premise": resp.text, "outline": [], "characters": [], "world_rules": []}
    return parsed


async def analyze_chapter(chapter_no: int, title: str, body: str, premise: str, bundle: AssetBundle, project_id: str) -> dict:
    prompt = bundle.prompt("import-chapter-analyzer", "Phân tích chương đã import.")
    messages = [
        {"role": "system", "content": prompt + "\n\nTrả về JSON summary với chapter, summary, characters, foreshadow."},
        {"role": "user", "content": json.dumps({"chapter": chapter_no, "title": title, "body": body[:12000], "premise": premise[:3000]}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("editor", messages, project_id, agent_name="import-analyzer")
    try:
        return json.loads(resp.text)
    except Exception:
        return {"chapter": chapter_no, "summary": body[:400]}


async def import_novel(project_id: str, path: str, from_chapter: int = 1, style: str = "default") -> dict:
    source = Path(path)
    text = source.read_text(encoding="utf-8", errors="ignore")
    chunks = split_text(text)
    if not chunks:
        raise ValueError("Không nhận diện được chương nào")
    bundle = AssetBundle(style)
    repo.save_artifact(project_id, "import", "source", {"path": str(source), "chapters": len(chunks)})

    foundation = await reverse_foundation(chunks, bundle, project_id)
    repo.save_artifact(project_id, "foundation", "premise", foundation.get("premise", f"# Import từ {source.name}"), "markdown")
    repo.save_artifact(project_id, "foundation", "outline", foundation.get("outline", []), "json")
    repo.save_artifact(project_id, "foundation", "characters", foundation.get("characters", []), "json")
    repo.save_artifact(project_id, "foundation", "world_rules", foundation.get("world_rules", []), "json")
    if foundation.get("compass"):
        repo.save_artifact(project_id, "foundation", "compass", foundation["compass"], "json")
    repo.checkpoint(project_id, "global", "import_foundation", "foundation/*", foundation)

    premise = foundation.get("premise", "")
    imported = 0
    for idx, (title, body) in enumerate(chunks, start=1):
        if idx < from_chapter:
            continue
        summary = await analyze_chapter(idx, title, body, premise if isinstance(premise, str) else "", bundle, project_id)
        repo.save_chapter(project_id, idx, title, draft=body, final=body, summary=summary, status="committed")
        repo.checkpoint(project_id, "chapter", "import_commit", f"chapters/{idx}/final", body, chapter=idx)
        imported += 1

    repo.update_progress(
        project_id,
        phase="writing",
        current_chapter=len(chunks),
        total_chapters=len(chunks),
        total_word_count=sum(len(c[1].split()) for c in chunks),
        layered=1 if len(chunks) > 50 else 0,
    )
    return {"imported": imported, "total": len(chunks), "foundation": bool(foundation)}
