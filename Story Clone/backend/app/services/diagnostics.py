from __future__ import annotations

import json
from app.repositories.repository import repo


def analyze(project_id: str) -> dict:
    progress = repo.progress(project_id)
    chapters = repo.chapters(project_id)
    reviews = repo.reviews(project_id)
    artifacts = repo.list_artifacts(project_id)
    findings = []
    if not artifacts:
        findings.append({"severity": "warning", "category": "planning", "title": "Chưa có artifact nền", "suggestion": "Hãy start hoặc import tác phẩm."})
    if progress.get("phase") == "writing" and progress.get("total_chapters") and progress.get("current_chapter", 0) > progress.get("total_chapters", 0):
        findings.append({"severity": "critical", "category": "flow", "title": "Current chapter vượt total", "suggestion": "Kiểm tra progress/checkpoint."})
    weak = [r for r in reviews if (r.get("score") or 100) < 70]
    if weak:
        findings.append({"severity": "warning", "category": "quality", "title": f"Có {len(weak)} review điểm thấp", "suggestion": "Đưa các chương này vào rewrite queue."})
    pending = repo.get_queue(project_id, "pending_rewrites")
    if pending:
        findings.append({"severity": "info", "category": "flow", "title": f"Hàng đợi viết lại: {pending}", "suggestion": "Resume để xử lý rewrite/polish."})
    if chapters and not any(c.get("summary_json") for c in chapters):
        findings.append({"severity": "info", "category": "context", "title": "Thiếu summary chương", "suggestion": "Chạy review/commit để sinh summary."})
    return {
        "stats": {
            "completed_chapters": len([c for c in chapters if c.get("status") == "committed"]),
            "total_chapters": progress.get("total_chapters"),
            "total_words": progress.get("total_word_count"),
            "phase": progress.get("phase"),
            "flow": progress.get("flow"),
            "review_count": len(reviews),
            "artifact_count": len(artifacts),
        },
        "findings": findings,
        "actions": [f.get("suggestion") for f in findings if f.get("suggestion")],
    }


def export(project_id: str) -> str:
    report = analyze(project_id)
    lines = ["# Story Clone diagnostics", "", "## Stats", "", "```json", json.dumps(report["stats"], ensure_ascii=False, indent=2), "```", "", "## Findings"]
    for f in report["findings"]:
        lines.append(f"- **{f['severity']}** `{f['category']}`: {f['title']} - {f.get('suggestion', '')}")
    return "\n".join(lines)
