from __future__ import annotations

from pathlib import Path

from app.core.styles import normalize_style

import sys

if getattr(sys, 'frozen', False):
    ASSETS_DIR = Path(sys.executable).resolve().parent / "app" / "assets"
else:
    ASSETS_DIR = Path(__file__).resolve().parents[1] / "assets"
SIMULATION_GUIDANCE = """\n\n## Hồ sơ mô phỏng văn phong\n\nKhi novel_context trả về simulation_profile, hãy xem nó là ràng buộc phong cách cho tác phẩm hiện tại. Hãy mượn cấu trúc, nhịp, móc truyện và cách giữ độc giả; không sao chép câu chữ, nhân vật, địa danh hoặc thiết lập độc quyền từ nguồn mẫu.\n"""

class AssetBundle:
    def __init__(self, style: str = "default") -> None:
        self.style = normalize_style(style)
        self.prompts = self._load_dir("prompts")
        self.styles = self._load_dir("styles")
        self.rules = self._load_dir("rules")
        self.references = self._load_references()
        for key in ["coordinator", "architect-short", "architect-long", "writer", "editor"]:
            if key in self.prompts:
                self.prompts[key] += SIMULATION_GUIDANCE

    def _load_dir(self, name: str) -> dict[str, str]:
        root = ASSETS_DIR / name
        out: dict[str, str] = {}
        if not root.exists():
            return out
        for path in root.rglob("*.md"):
            rel = path.relative_to(root).as_posix()
            key = rel[:-3]
            out[key] = path.read_text(encoding="utf-8")
        return out

    def _load_references(self) -> dict[str, str]:
        refs = self._load_dir("references")
        if self.style != "default":
            for suffix in ["style-references", "arc-templates"]:
                key = f"genres/{self.style}/{suffix}"
                if key in refs:
                    refs[suffix] = refs[key]
                genre_path = ASSETS_DIR / "references" / "genres" / self.style / f"{suffix}.md"
                if genre_path.exists() and suffix not in refs:
                    refs[suffix] = genre_path.read_text(encoding="utf-8")
        return refs

    def prompt(self, key: str, default: str = "") -> str:
        return self.prompts.get(key, default)

    def style_text(self) -> str:
        return self.styles.get(self.style) or self.styles.get("default", "")

    def genre_reference_keys(self) -> list[str]:
        if self.style == "default":
            return []
        return [k for k in ("style-references", "arc-templates") if k in self.references]
