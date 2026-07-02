from __future__ import annotations

import hashlib
import json
from pathlib import Path

from app.core.assets import AssetBundle
from app.llm.client import llm_client
from app.repositories.repository import repo


def _fingerprint(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


async def analyze_source(text: str, relative_path: str, bundle: AssetBundle, project_id: str) -> dict:
    prompt = bundle.prompt("simulation-source", "Phân tích văn phong nguồn mẫu.")
    messages = [
        {"role": "system", "content": prompt + "\n\nTrả về JSON: style, lexicon, pacing, hooks, dialogue_ratio."},
        {"role": "user", "content": json.dumps({"path": relative_path, "text": text[:60000]}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("architect", messages, project_id, agent_name="simulation-source")
    try:
        report = json.loads(resp.text)
    except Exception:
        report = {"summary": text[:500], "style": resp.text[:1000]}
    report["relative_path"] = relative_path
    report["sha256"] = hashlib.sha256(text.encode()).hexdigest()
    return report


async def merge_synthesis(reports: list[dict], existing: dict | None, bundle: AssetBundle, project_id: str) -> dict:
    prompt = bundle.prompt("simulation-merge", "Hợp nhất hồ sơ mô phỏng văn phong.")
    messages = [
        {"role": "system", "content": prompt + "\n\nTrả về JSON synthesis với style, lexicon, pacing_density, reader_engagement."},
        {"role": "user", "content": json.dumps({"existing": existing, "reports": reports}, ensure_ascii=False)},
    ]
    resp = await llm_client.generate("architect", messages, project_id, agent_name="simulation-merge")
    try:
        return json.loads(resp.text)
    except Exception:
        return {
            "style": "Hồ sơ phong cách được tổng hợp từ ngữ liệu.",
            "lexicon": resp.text[:2000],
            "pacing_density": "Theo corpus mẫu",
            "reader_engagement": "Giữ móc cuối đoạn/chương",
        }


async def build_simulation(project_id: str, source_dir: str, style: str = "default") -> dict:
    root = Path(source_dir)
    files = [p for p in root.rglob("*") if p.suffix.lower() in {".txt", ".md", ".markdown"}]
    if not files:
        raise ValueError("Không có file .txt/.md/.markdown trong thư mục mô phỏng")
    bundle = AssetBundle(style)
    existing_art = repo.get_artifact(project_id, "simulation", "profile")
    existing = json.loads(existing_art["content"]) if existing_art else None
    existing_fps = {s.get("sha256") for s in (existing or {}).get("corpus", {}).get("sources", [])}

    reports = []
    for p in files:
        text = p.read_text(encoding="utf-8", errors="ignore")
        fp = _fingerprint(p)
        if fp in existing_fps:
            continue
        rel = str(p.relative_to(root))
        reports.append(await analyze_source(text, rel, bundle, project_id))

    if not reports and existing:
        return {"sources": len(files), "profile": existing, "skipped": True}

    all_reports = list((existing or {}).get("corpus", {}).get("sources", [])) + reports
    synthesis = await merge_synthesis(reports or all_reports[:3], existing, bundle, project_id)
    profile = {
        "version": 1,
        "corpus": {"source_dir": str(root), "sources": all_reports},
        "synthesis": synthesis,
    }
    repo.save_artifact(project_id, "simulation", "profile", profile)
    repo.checkpoint(project_id, "global", "simulation_profile", "simulation/profile", profile)
    return {"sources": len(files), "analyzed": len(reports), "profile": profile}


async def import_profile(project_id: str, path: str) -> dict:
    data = json.loads(Path(path).read_text(encoding="utf-8"))
    repo.save_artifact(project_id, "simulation", "profile", data)
    return {"imported": True, "sources": len(data.get("corpus", {}).get("sources", []))}
