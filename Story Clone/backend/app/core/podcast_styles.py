from __future__ import annotations

PODCAST_STYLES = frozenset({"podcast_history", "podcast_geo"})


def is_podcast_style(style: str | None) -> bool:
    return (style or "").strip().lower() in PODCAST_STYLES


def episode_label(chapter_no: int) -> str:
    return f"Tập {chapter_no}"


PODCAST_WRITER_ADDENDUM = """
## GHI ĐÈ BẮT BUỘC — ĐỊNH DẠNG PODCAST (ưu tiên cao hơn mọi hướng dẫn tiểu thuyết ở trên)

Bạn đang viết **kịch bản podcast / thuyết minh audio**, KHÔNG phải tiểu thuyết.

- Trả về **script đọc mic** bằng tiếng Việt, có thể dùng Markdown nhẹ.
- Giọng **người dẫn (host)** nói trực tiếp với người nghe: "các bạn", "chúng ta", "hôm nay".
- **Không** viết tiểu thuyết hư cấu: không cảnh phim, không đối thoại nhân vật kiểu kịch, không mô tả nội tâm văn học dài.
- Câu **ngắn–vừa**, dễ nghe; lặp lại ý chính; có **signpost** ("trước hết", "tiếp theo", "tóm lại").
- Cấu trúc mỗi tập (gọi là chương trong hệ thống):
  1. **[MỞ ĐẦU / HOOK]** — 2–4 câu gây tò mò
  2. **[GIỚI THIỆU TẬP]** — chủ đề, phạm vi thời gian/địa lý
  3. **[THÂN BÀI]** — 3–6 phần có tiêu đề ngắn trong ngoặc vuông
  4. **[TÓM TẮT]** — 3–5 bullet hoặc đoạn ngắn
  5. **[KẾT / TEASER]** — câu hỏi mở hoặc preview tập sau
- Cho phép chèn cue sản xuất: `[NHẠC NỀN]`, `[PAUSE]`, `[CHUYỂN CẢNH]` — không lạm dụng.
- Khi lập kế hoạch: `required_beats` = các phần thân bài; `hook_goal` = câu kết gợi tập tiếp theo.
- Khi viết draft: toàn văn script tập, không JSON, không giải thích meta.
"""

PODCAST_EDITOR_ADDENDUM = """
Đánh giá theo chuẩn **kịch bản podcast**, không phải tiểu thuyết:
- Rõ ràng khi nghe, mạch logic, fact/signpost đủ.
- Không phạt vì thiếu đối thoại hư cấu hoặc miêu tả cảnh văn học.
- Phạt nếu giống tiểu thuyết, quá học thuật khô, hoặc thiếu hook đầu/cuối tập.
"""

PODCAST_ARCHITECT_ADDENDUM = """
## GHI ĐÈ — OUTLINE PODCAST (ưu tiên cao hơn mẫu tiểu thuyết)

- Mỗi mục trong `outline[]` = **một tập podcast** (episode), không phải chương tiểu thuyết.
- `premise`: chủ đề series, đối tượng người nghe, góc nhìn host.
- Mỗi phần tử outline: `chapter` (số tập), `title`, `goal`, `hook`, `segments` (3–5 phần thân bài).
- `characters[]`: chỉ mô tả host (giọng, persona) nếu cần — không nhân vật hư cấu.
- `world_rules[]`: nguyên tắc sự thật / phạm vi địa lý-thời gian, không luật thế giới hư cấu.
"""
