from __future__ import annotations

from pathlib import Path

import sys

if getattr(sys, 'frozen', False):
    ASSETS_DIR = Path(sys.executable).resolve().parent / "app" / "assets"
else:
    ASSETS_DIR = Path(__file__).resolve().parents[1] / "assets"

# Khớp config.style của ainovel-cli gốc: default / fantasy / romance / suspense
STYLE_CATALOG: dict[str, dict[str, str | bool]] = {
    "default": {
        "value": "default",
        "label": "Mặc định",
        "label_en": "General",
        "description": "Phong cách tổng quát: nhịp truyện cân bằng, mô tả cụ thể, đối thoại tự nhiên, cảm xúc qua hành động.",
        "has_genre_refs": False,
    },
    "fantasy": {
        "value": "fantasy",
        "label": "Huyền huyễn",
        "label_en": "Fantasy",
        "description": "Phiêu lưu huyền huyễn: thế giới quan dần mở, hệ thống sức mạnh có giá, chiến đấu có chiến thuật, tăng trưởng song song thể chất và tâm trí.",
        "has_genre_refs": True,
    },
    "romance": {
        "value": "romance",
        "label": "Ngôn tình",
        "label_en": "Romance",
        "description": "Ngôn tình: tình cảm leo thang tự nhiên, căng thẳng quan hệ, chi tiết nhỏ truyền cảm, đối thoại có khoảng trống và xung đột có căn cứ.",
        "has_genre_refs": True,
    },
    "suspense": {
        "value": "suspense",
        "label": "Trinh thám / Hồi hộp",
        "label_en": "Suspense",
        "description": "Trinh thám hồi hộp: đa tuyến, manh mối xuất hiện sớm, nhịp căng–thả xen kẽ, môi trường tăng áp lực, sự thật có foreshadow.",
        "has_genre_refs": True,
    },
    "ghost": {
        "value": "ghost",
        "label": "Truyện Ma / Kinh dị",
        "label_en": "Ghost / Horror",
        "description": "Truyện ma kinh dị: không khí u ám, dồn nén nỗi sợ, hiện tượng kỳ bí, âm thanh và bóng tối kích thích tưởng tượng, cú quay xe bất ngờ.",
        "has_genre_refs": True,
    },
    "drama": {
        "value": "drama",
        "label": "Kịch tính / Drama",
        "label_en": "Drama / Realistic",
        "description": "Kịch tính kịch liệt: mâu thuẫn xã hội/gia đình gay gắt, xung đột lợi ích và tình cảm phức tạp, lời thoại sắc bén, cao trào liên tục.",
        "has_genre_refs": True,
    },
    "history": {
        "value": "history",
        "label": "Lịch sử / Dã sử",
        "label_en": "Historical",
        "description": "Lịch sử dã sử: tôn trọng các sự kiện và nhân vật lịch sử cốt lõi, hư cấu sáng tạo thêm các tình tiết và hành trình để cốt truyện phong phú hơn.",
        "has_genre_refs": True,
    },
    "podcast_history": {
        "value": "podcast_history",
        "label": "Podcast lịch sử",
        "label_en": "History Podcast",
        "description": "Kịch bản podcast lịch sử: người dẫn kể sự kiện có căn cứ, mốc thời gian rõ, hook đầu/cuối tập, tối ưu cho nghe audio.",
        "has_genre_refs": True,
    },
    "podcast_geo": {
        "value": "podcast_geo",
        "label": "Podcast địa lý",
        "label_en": "Geography Podcast",
        "description": "Kịch bản podcast địa lý: khám phá vùng miền, địa hình, khí hậu, văn hóa gắn bản đồ và so sánh không gian, giọng dẫn thân thiện.",
        "has_genre_refs": True,
    },
    "wuxia": {
        "value": "wuxia",
        "label": "Kiếm hiệp",
        "label_en": "Wuxia / Martial Arts",
        "description": "Kiếm hiệp giang hồ: ân oán giang hồ, võ học tinh thâm, nghĩa khí hiệp cốt, nhịp đấu võ chi tiết và hành hiệp trượng nghĩa.",
        "has_genre_refs": True,
    },
    "scifi": {
        "value": "scifi",
        "label": "Khoa học viễn tưởng",
        "label_en": "Sci-Fi",
        "description": "Khoa học viễn tưởng: thế giới tương lai, công nghệ cao, logic khoa học giả tưởng vững chắc, khám phá vũ trụ hoặc trí tuệ nhân tạo.",
        "has_genre_refs": True,
    },
    "nsfw": {
        "value": "nsfw",
        "label": "Truyện người lớn / 18+",
        "label_en": "NSFW / 18+",
        "description": "Truyện người lớn 18+: miêu tả chi tiết cảm xúc mãnh liệt, căng thẳng thể xác và tâm lý, hấp dẫn nhục cảm tinh tế hoặc cao trào kịch liệt.",
        "has_genre_refs": True,
    },
}

VALID_STYLES = frozenset(STYLE_CATALOG.keys())


def normalize_style(style: str | None) -> str:
    key = (style or "default").strip().lower()
    return key if key in VALID_STYLES else "default"


def list_styles() -> list[dict[str, str | bool]]:
    styles_dir = ASSETS_DIR / "styles"
    out: list[dict[str, str | bool]] = []
    for key in STYLE_CATALOG.keys():
        meta = dict(STYLE_CATALOG[key])
        path = styles_dir / f"{key}.md"
        meta["asset_ready"] = path.exists()
        out.append(meta)
    return out
