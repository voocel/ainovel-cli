from __future__ import annotations

import os
import sqlite3
from pathlib import Path
from threading import RLock

import sys

if getattr(sys, 'frozen', False):
    APP_DIR = Path(sys.executable).resolve().parent
    ROOT_DIR = APP_DIR.parent
else:
    APP_DIR = Path(__file__).resolve().parents[2]
    ROOT_DIR = Path(__file__).resolve().parents[3]

DATA_DIR = Path(os.getenv("STORE_OPEN_DATA", ROOT_DIR / "data"))
DB_PATH = Path(os.getenv("STORE_OPEN_DB", DATA_DIR / "store_open.sqlite3"))
_LOCK = RLock()


def _maybe_migrate_legacy_db() -> None:
    legacy = APP_DIR / "data" / "store_open.sqlite3"
    if legacy.exists() and not DB_PATH.exists():
        DATA_DIR.mkdir(parents=True, exist_ok=True)
        import shutil
        shutil.copy2(legacy, DB_PATH)

SCHEMA = """
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;
CREATE TABLE IF NOT EXISTS projects (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  output_dir TEXT,
  style TEXT NOT NULL DEFAULT 'default',
  artist_style TEXT NOT NULL DEFAULT 'history_ink',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS progress (
  project_id TEXT PRIMARY KEY,
  novel_name TEXT,
  phase TEXT NOT NULL,
  flow TEXT,
  current_chapter INTEGER DEFAULT 0,
  total_chapters INTEGER DEFAULT 0,
  total_word_count INTEGER DEFAULT 0,
  in_progress_chapter INTEGER DEFAULT 0,
  current_volume INTEGER DEFAULT 0,
  current_arc INTEGER DEFAULT 0,
  layered INTEGER DEFAULT 0,
  rewrite_reason TEXT,
  reopened_from_complete INTEGER DEFAULT 0,
  FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS chapters (
  project_id TEXT NOT NULL,
  chapter_no INTEGER NOT NULL,
  title TEXT,
  final_text TEXT,
  draft_text TEXT,
  plan_json TEXT,
  summary_json TEXT,
  word_count INTEGER DEFAULT 0,
  status TEXT NOT NULL DEFAULT 'draft',
  committed_at TEXT,
  PRIMARY KEY(project_id, chapter_no),
  FOREIGN KEY(project_id) REFERENCES projects(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS checkpoints (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  scope_kind TEXT NOT NULL,
  scope_chapter INTEGER DEFAULT 0,
  scope_volume INTEGER DEFAULT 0,
  scope_arc INTEGER DEFAULT 0,
  step TEXT NOT NULL,
  artifact TEXT,
  digest TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  UNIQUE(project_id, scope_kind, scope_chapter, scope_volume, scope_arc, step, digest)
);
CREATE TABLE IF NOT EXISTS artifacts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  key TEXT NOT NULL,
  content TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT 'json',
  updated_at TEXT NOT NULL,
  UNIQUE(project_id, kind, key)
);
CREATE TABLE IF NOT EXISTS reviews (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  chapter_no INTEGER,
  volume_no INTEGER,
  arc_no INTEGER,
  verdict TEXT,
  score REAL,
  payload_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  agent_name TEXT NOT NULL,
  role TEXT NOT NULL,
  provider TEXT,
  model TEXT,
  message_json TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  category TEXT NOT NULL,
  level TEXT NOT NULL,
  summary TEXT NOT NULL,
  payload_json TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS providers (
  name TEXT PRIMARY KEY,
  type TEXT,
  api_key_encrypted TEXT,
  base_url TEXT,
  models_json TEXT,
  extra_body_json TEXT,
  extra_json TEXT
);
CREATE TABLE IF NOT EXISTS role_models (
  role TEXT PRIMARY KEY,
  provider TEXT NOT NULL,
  model TEXT NOT NULL,
  thinking TEXT,
  fallbacks_json TEXT
);
CREATE TABLE IF NOT EXISTS usage_records (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id TEXT NOT NULL,
  agent_name TEXT,
  provider TEXT,
  model TEXT,
  input_tokens INTEGER DEFAULT 0,
  output_tokens INTEGER DEFAULT 0,
  cost_usd REAL DEFAULT 0,
  created_at TEXT NOT NULL
);
"""

def connect() -> sqlite3.Connection:
    DATA_DIR.mkdir(parents=True, exist_ok=True)
    con = sqlite3.connect(DB_PATH, check_same_thread=False)
    con.row_factory = sqlite3.Row
    con.execute("PRAGMA foreign_keys=ON")
    return con

def _migrate_schema(con: sqlite3.Connection) -> None:
    cols = {row[1] for row in con.execute("PRAGMA table_info(projects)").fetchall()}
    if "artist_style" not in cols:
        con.execute(
            "ALTER TABLE projects ADD COLUMN artist_style TEXT NOT NULL DEFAULT 'history_ink'"
        )


def init_db() -> None:
    _maybe_migrate_legacy_db()
    with _LOCK:
        con = connect()
        try:
            con.executescript(SCHEMA)
            _migrate_schema(con)
            con.commit()
        finally:
            con.close()

def tx():
    return _LOCK
