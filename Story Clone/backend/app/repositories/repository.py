from __future__ import annotations

import hashlib
import json
import time
import uuid
from contextlib import contextmanager
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from app.core.database import connect, tx


def now() -> str:
    return datetime.now(timezone.utc).isoformat()


def row_to_dict(row):
    return dict(row) if row is not None else None


def stable_json(value: Any) -> str:
    return json.dumps(value, ensure_ascii=False, sort_keys=True, separators=(",", ":"))


def digest(value: Any) -> str:
    if isinstance(value, str):
        raw = value
    else:
        raw = stable_json(value)
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


class Repository:
    @contextmanager
    def db(self):
        con = connect()
        try:
            yield con
            con.commit()
        except Exception:
            con.rollback()
            raise
        finally:
            con.close()

    def create_project(self, name: str, style: str = "default", output_dir: str | None = None, artist_style: str = "history_ink") -> dict[str, Any]:
        project_id = str(uuid.uuid4())
        stamp = now()
        with tx(), self.db() as con:
            con.execute(
                "INSERT INTO projects(id,name,output_dir,style,artist_style,created_at,updated_at) VALUES(?,?,?,?,?,?,?)",
                (project_id, name.strip() or "Tác phẩm mới", output_dir, style or "default", artist_style or "history_ink", stamp, stamp),
            )
            con.execute(
                "INSERT INTO progress(project_id,phase,flow,current_chapter,total_chapters,total_word_count) VALUES(?,?,?,?,?,?)",
                (project_id, "init", "writing", 0, 0, 0),
            )
        return self.get_project(project_id)

    def list_projects(self) -> list[dict[str, Any]]:
        with self.db() as con:
            rows = con.execute(
                """
                SELECT p.*, pr.phase, pr.flow, pr.current_chapter, pr.total_chapters, pr.total_word_count
                FROM projects p LEFT JOIN progress pr ON pr.project_id=p.id
                ORDER BY p.updated_at DESC
                """
            ).fetchall()
            return [dict(r) for r in rows]

    def get_project(self, project_id: str) -> dict[str, Any]:
        with self.db() as con:
            row = con.execute("SELECT * FROM projects WHERE id=?", (project_id,)).fetchone()
            if not row:
                raise KeyError("project not found")
            data = dict(row)
            data["progress"] = row_to_dict(con.execute("SELECT * FROM progress WHERE project_id=?", (project_id,)).fetchone())
            return data

    def touch_project(self, project_id: str) -> None:
        with self.db() as con:
            con.execute("UPDATE projects SET updated_at=? WHERE id=?", (now(), project_id))

    def update_progress(self, project_id: str, **fields: Any) -> None:
        if not fields:
            return
        keys = ",".join(f"{k}=?" for k in fields)
        with tx(), self.db() as con:
            con.execute(f"UPDATE progress SET {keys} WHERE project_id=?", (*fields.values(), project_id))
            con.execute("UPDATE projects SET updated_at=? WHERE id=?", (now(), project_id))

    def progress(self, project_id: str) -> dict[str, Any]:
        with self.db() as con:
            row = con.execute("SELECT * FROM progress WHERE project_id=?", (project_id,)).fetchone()
            if not row:
                raise KeyError("progress not found")
            return dict(row)

    def save_artifact(self, project_id: str, kind: str, key: str, content: Any, content_type: str = "json") -> str:
        text = content if isinstance(content, str) else json.dumps(content, ensure_ascii=False, indent=2)
        with tx(), self.db() as con:
            con.execute(
                """
                INSERT INTO artifacts(project_id,kind,key,content,content_type,updated_at)
                VALUES(?,?,?,?,?,?)
                ON CONFLICT(project_id,kind,key) DO UPDATE SET content=excluded.content, content_type=excluded.content_type, updated_at=excluded.updated_at
                """,
                (project_id, kind, key, text, content_type, now()),
            )
        return f"artifacts/{kind}/{key}"

    def get_artifact(self, project_id: str, kind: str, key: str) -> dict[str, Any] | None:
        with self.db() as con:
            row = con.execute("SELECT * FROM artifacts WHERE project_id=? AND kind=? AND key=?", (project_id, kind, key)).fetchone()
            return row_to_dict(row)

    def list_artifacts(self, project_id: str, kind: str | None = None) -> list[dict[str, Any]]:
        with self.db() as con:
            if kind:
                rows = con.execute("SELECT * FROM artifacts WHERE project_id=? AND kind=? ORDER BY key", (project_id, kind)).fetchall()
            else:
                rows = con.execute("SELECT * FROM artifacts WHERE project_id=? ORDER BY kind,key", (project_id,)).fetchall()
            return [dict(r) for r in rows]

    def checkpoint(self, project_id: str, scope_kind: str, step: str, artifact: str, body: Any, chapter: int = 0, volume: int = 0, arc: int = 0) -> None:
        dg = digest(body)
        with tx(), self.db() as con:
            con.execute(
                """
                INSERT OR IGNORE INTO checkpoints(project_id,scope_kind,scope_chapter,scope_volume,scope_arc,step,artifact,digest,occurred_at)
                VALUES(?,?,?,?,?,?,?,?,?)
                """,
                (project_id, scope_kind, chapter, volume, arc, step, artifact, dg, now()),
            )

    def save_chapter(self, project_id: str, chapter_no: int, title: str, draft: str | None = None, final: str | None = None, plan: Any | None = None, summary: Any | None = None, status: str = "draft") -> None:
        word_count = len((final or draft or "").split())
        with tx(), self.db() as con:
            con.execute(
                """
                INSERT INTO chapters(project_id,chapter_no,title,draft_text,final_text,plan_json,summary_json,word_count,status,committed_at)
                VALUES(?,?,?,?,?,?,?,?,?,?)
                ON CONFLICT(project_id,chapter_no) DO UPDATE SET
                  title=COALESCE(excluded.title, chapters.title),
                  draft_text=COALESCE(excluded.draft_text, chapters.draft_text),
                  final_text=COALESCE(excluded.final_text, chapters.final_text),
                  plan_json=COALESCE(excluded.plan_json, chapters.plan_json),
                  summary_json=COALESCE(excluded.summary_json, chapters.summary_json),
                  word_count=excluded.word_count,
                  status=excluded.status,
                  committed_at=COALESCE(excluded.committed_at, chapters.committed_at)
                """,
                (
                    project_id,
                    chapter_no,
                    title,
                    draft,
                    final,
                    json.dumps(plan, ensure_ascii=False) if plan is not None else None,
                    json.dumps(summary, ensure_ascii=False) if summary is not None else None,
                    word_count,
                    status,
                    now() if status == "committed" else None,
                ),
            )

    def chapters(self, project_id: str) -> list[dict[str, Any]]:
        with self.db() as con:
            rows = con.execute("SELECT * FROM chapters WHERE project_id=? ORDER BY chapter_no", (project_id,)).fetchall()
            return [dict(r) for r in rows]

    def chapter(self, project_id: str, chapter_no: int) -> dict[str, Any] | None:
        with self.db() as con:
            return row_to_dict(con.execute("SELECT * FROM chapters WHERE project_id=? AND chapter_no=?", (project_id, chapter_no)).fetchone())

    def save_review(self, project_id: str, payload: dict[str, Any], chapter_no: int | None = None, volume_no: int | None = None, arc_no: int | None = None) -> None:
        with tx(), self.db() as con:
            con.execute(
                "INSERT INTO reviews(project_id,chapter_no,volume_no,arc_no,verdict,score,payload_json,created_at) VALUES(?,?,?,?,?,?,?,?)",
                (project_id, chapter_no, volume_no, arc_no, payload.get("verdict"), payload.get("score"), json.dumps(payload, ensure_ascii=False), now()),
            )

    def reviews(self, project_id: str) -> list[dict[str, Any]]:
        with self.db() as con:
            return [dict(r) for r in con.execute("SELECT * FROM reviews WHERE project_id=? ORDER BY id DESC", (project_id,)).fetchall()]

    def event(self, project_id: str, category: str, summary: str, level: str = "info", payload: Any | None = None) -> dict[str, Any]:
        ev = {"project_id": project_id, "category": category, "level": level, "summary": summary, "payload": payload, "created_at": now()}
        with tx(), self.db() as con:
            con.execute(
                "INSERT INTO runtime_events(project_id,category,level,summary,payload_json,created_at) VALUES(?,?,?,?,?,?)",
                (project_id, category, level, summary, json.dumps(payload, ensure_ascii=False) if payload is not None else None, ev["created_at"]),
            )
        return ev

    def events(self, project_id: str, after_id: int = 0) -> list[dict[str, Any]]:
        with self.db() as con:
            rows = con.execute("SELECT * FROM runtime_events WHERE project_id=? AND id>? ORDER BY id", (project_id, after_id)).fetchall()
            return [dict(r) for r in rows]

    def save_provider(self, data: dict[str, Any]) -> None:
        with tx(), self.db() as con:
            con.execute(
                """
                INSERT INTO providers(name,type,api_key_encrypted,base_url,models_json,extra_body_json,extra_json)
                VALUES(?,?,?,?,?,?,?)
                ON CONFLICT(name) DO UPDATE SET type=excluded.type, api_key_encrypted=excluded.api_key_encrypted, base_url=excluded.base_url,
                  models_json=excluded.models_json, extra_body_json=excluded.extra_body_json, extra_json=excluded.extra_json
                """,
                (data["name"], data.get("type"), data.get("api_key"), data.get("base_url"), json.dumps(data.get("models", []), ensure_ascii=False), json.dumps(data.get("extra_body", {}), ensure_ascii=False), json.dumps(data.get("extra", {}), ensure_ascii=False)),
            )

    def providers(self) -> list[dict[str, Any]]:
        with self.db() as con:
            out = []
            for r in con.execute("SELECT * FROM providers ORDER BY name").fetchall():
                d = dict(r)
                d["models"] = json.loads(d.pop("models_json") or "[]")
                d["extra_body"] = json.loads(d.pop("extra_body_json") or "{}")
                d["extra"] = json.loads(d.pop("extra_json") or "{}")
                if d.get("api_key_encrypted"):
                    d["api_key"] = "********"
                out.append(d)
            return out

    def provider_secret(self, name: str) -> dict[str, Any] | None:
        with self.db() as con:
            r = con.execute("SELECT * FROM providers WHERE name=?", (name,)).fetchone()
            if not r:
                return None
            d = dict(r)
            d["api_key"] = d.pop("api_key_encrypted")
            d["models"] = json.loads(d.pop("models_json") or "[]")
            d["extra_body"] = json.loads(d.pop("extra_body_json") or "{}")
            d["extra"] = json.loads(d.pop("extra_json") or "{}")
            return d

    def set_role_model(self, role: str, provider: str, model: str, thinking: str | None = None, fallbacks: list[dict[str, str]] | None = None) -> None:
        with tx(), self.db() as con:
            con.execute(
                "INSERT INTO role_models(role,provider,model,thinking,fallbacks_json) VALUES(?,?,?,?,?) ON CONFLICT(role) DO UPDATE SET provider=excluded.provider, model=excluded.model, thinking=excluded.thinking, fallbacks_json=excluded.fallbacks_json",
                (role, provider, model, thinking, json.dumps(fallbacks or [], ensure_ascii=False)),
            )

    def role_models(self) -> list[dict[str, Any]]:
        with self.db() as con:
            rows = con.execute("SELECT * FROM role_models ORDER BY role").fetchall()
            out = []
            for r in rows:
                d = dict(r)
                d["fallbacks"] = json.loads(d.pop("fallbacks_json") or "[]")
                out.append(d)
            return out

    def get_queue(self, project_id: str, key: str) -> list[int]:
        art = self.get_artifact(project_id, "queues", key)
        if not art:
            return []
        try:
            data = json.loads(art["content"])
            return [int(x) for x in data] if isinstance(data, list) else []
        except Exception:
            return []

    def set_queue(self, project_id: str, key: str, items: list[int]) -> None:
        self.save_artifact(project_id, "queues", key, items)

    def push_queue(self, project_id: str, key: str, item: int) -> None:
        items = self.get_queue(project_id, key)
        if item not in items:
            items.append(item)
            self.set_queue(project_id, key, items)

    def shift_queue(self, project_id: str, key: str) -> int | None:
        items = self.get_queue(project_id, key)
        if not items:
            return None
        head = items.pop(0)
        self.set_queue(project_id, key, items)
        return head

    def clear_directive(self, project_id: str, key: str) -> None:
        with tx(), self.db() as con:
            con.execute("DELETE FROM artifacts WHERE project_id=? AND kind='directives' AND key=?", (project_id, key))

    def record_usage(self, project_id: str | None, agent_name: str, provider: str, model: str, input_tokens: int, output_tokens: int, cost_usd: float = 0.0) -> None:
        with tx(), self.db() as con:
            con.execute(
                "INSERT INTO usage_records(project_id,agent_name,provider,model,input_tokens,output_tokens,cost_usd,created_at) VALUES(?,?,?,?,?,?,?,?)",
                (project_id or "", agent_name, provider, model, input_tokens, output_tokens, cost_usd, now()),
            )

    def record_agent_message(self, project_id: str, agent_name: str, role: str, provider: str, model: str, messages: Any) -> None:
        with tx(), self.db() as con:
            con.execute(
                "INSERT INTO agent_messages(project_id,agent_name,role,provider,model,message_json,created_at) VALUES(?,?,?,?,?,?,?)",
                (project_id, agent_name, role, provider, model, json.dumps(messages, ensure_ascii=False), now()),
            )

    def usage_summary(self, project_id: str | None = None) -> list[dict[str, Any]]:
        with self.db() as con:
            if project_id:
                rows = con.execute(
                    "SELECT provider,model,SUM(input_tokens) input_tokens,SUM(output_tokens) output_tokens,SUM(cost_usd) cost_usd FROM usage_records WHERE project_id=? GROUP BY provider,model",
                    (project_id,),
                ).fetchall()
            else:
                rows = con.execute(
                    "SELECT provider,model,SUM(input_tokens) input_tokens,SUM(output_tokens) output_tokens,SUM(cost_usd) cost_usd FROM usage_records GROUP BY provider,model"
                ).fetchall()
            return [dict(r) for r in rows]

    def has_foundation(self, project_id: str) -> bool:
        for key in ("premise", "outline", "characters"):
            if not self.get_artifact(project_id, "foundation", key):
                return False
        return True

repo = Repository()
