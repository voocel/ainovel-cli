from __future__ import annotations

ARTIST_NEGATIVE_DEFAULT = (    "photorealistic modern, 3D render, anime, neon, chữ, logo, watermark, "
    "xe hơi, kiến trúc hiện đại, hiện đại hóa sai bối cảnh"
)

ARTIST_STYLES: dict[str, dict[str, str]] = {
    "history_ink": {
        "value": "history_ink",
        "label": "Lịch sử / Dã sử — Mực tàu & khắc gỗ",
        "description": (
            "Minh họa mực tàu và nước trên giấy da cừu, aesthetic khắc gỗ Đông Hồ / tranh dã sử cố, "
            "tông sepia, gạch chéo tạo bóng, cảm giác tài liệu lịch sử phai màu."
        ),
        "prompt": (
            "traditional Vietnamese historical art, ink and wash illustration, woodblock print aesthetic, "
            "vintage parchment texture, sepia tones, earthy palette, cross-hatching shadows, epic historical narrative"
        ),
        "negative": ARTIST_NEGATIVE_DEFAULT,
    },
    "history_cinematic": {
        "value": "history_cinematic",
        "label": "Điện ảnh số cổ",
        "description": (
            "Minh họa kỹ thuật số cinematic, ánh sáng ấm định hướng, nội thất gỗ khắc Việt Nam, "
            "áo dài thời kỳ, texture vải và gỗ chi tiết, palette đất nung — như khung hình poster phim lịch sử."
        ),
        "prompt": (
            "realistic Vietnamese historical digital illustration, cinematic warm directional lighting, "
            "ornate dark carved wooden interior, Indochine period architecture, traditional áo dài and period dress, "
            "high-fidelity fabric and wood grain textures, shallow depth of field, romantic narrative atmosphere, "
            "warm mahogany and terracotta earthy palette"
        ),
        "negative": (
            "anime, cartoon, 3D render, neon, flat vector, chữ, logo, watermark, smartphone, "
            "xe hơi, kiến trúc hiện đại, hiện đại hóa sai bối cảnh"
        ),
    },
    "history_oil_dramatic": {
        "value": "history_oil_dramatic",
        "label": "Sơn dầu kịch (chiaroscuro)",
        "description": (
            "Tranh sơn dầu chiaroscuro, tương phản sáng tối mạnh, nét cọ có texture, "
            "cảm xúc kịch tính, trang phục lịch sử Việt (áo dài, bà ba), nền tối atmospheric."
        ),
        "prompt": (
            "Vietnamese historical oil painting on canvas, dramatic chiaroscuro lighting, visible textured brushstrokes, "
            "emotional narrative realism, traditional áo dài and bà ba clothing, dark atmospheric background, "
            "cream and ochre highlights against deep shadows, cinematic portrait composition, classical fine art"
        ),
        "negative": (
            "anime, cartoon, 3D render, digital illustration, neon, flat colors, chữ, logo, watermark, "
            "xe hơi, kiến trúc hiện đại"
        ),
    },
    "history_oil_indochine": {
        "value": "history_oil_indochine",
        "label": "Sơn dầu Đông Dương",
        "description": (
            "Hội họa Đông Dương cổ điển, impasto, ánh sáng vàng ấm, trang phục dân gian lịch sử, "
            "chiếu cói, palette ochre và chàm — gợi Tô Ngọc Vân / tranh chân dung phong cách Indochine."
        ),
        "prompt": (
            "classical Indochine Vietnamese oil painting, impasto brushwork on textured canvas, "
            "warm golden directional light, historical folk linen blouse and indigo silk trousers, "
            "woven straw mat interior, earthy ochre and indigo palette, nostalgic fine art portrait, "
            "Tô Ngọc Vân inspired historical realism"
        ),
        "negative": (
            "anime, cartoon, 3D render, neon, ink wash, woodblock print, chữ, logo, watermark, "
            "xe hơi, kiến trúc hiện đại"
        ),
    },
    "dong_ho_folk": {
        "value": "dong_ho_folk",
        "label": "Tranh dân gian Đông Hồ",
        "description": (
            "Tranh khắc gỗ dân gian Đông Hồ: viền đậm, màu phẳng đỏ–vàng–xanh, "
            "nhân vật biểu tượng, cảm giác lễ hội và đời sống làng quê Việt."
        ),
        "prompt": (
            "Vietnamese Dong Ho folk woodblock print, bold black outlines, flat symbolic colors red yellow green, "
            "traditional village festival scene, decorative folk art composition, handmade paper texture, "
            "naive expressive figures, cultural heritage illustration"
        ),
        "negative": (
            "photorealistic, 3D render, anime, neon, gradient shading, chữ, logo, watermark, "
            "xe hơi, smartphone, kiến trúc hiện đại"
        ),
    },
    "silk_painting": {
        "value": "silk_painting",
        "label": "Tranh lụa Việt Nam",
        "description": (
            "Tranh lụa truyền thống: nét mảnh uyển chuyển, màu loang trong suốt trên lụa, "
            "sen, chim, phong cảnh thuần Việt thanh tao."
        ),
        "prompt": (
            "Vietnamese traditional silk painting, delicate flowing brush lines on silk fabric, "
            "soft translucent watercolor washes, lotus flowers and birds, poetic landscape, "
            "elegant minimal composition, muted jade green and rose pink palette, fine art heritage"
        ),
        "negative": (
            "heavy oil paint, 3D render, anime, neon, harsh contrast, chữ, logo, watermark, "
            "xe hơi, kiến trúc hiện đại"
        ),
    },
    "lacquer_sonmai": {
        "value": "lacquer_sonmai",
        "label": "Sơn mài Việt Nam",
        "description": (
            "Sơn mài cổ truyền: nền đen bóng, chạm vàng, đỏ son, "
            "hoa văn tinh xảo, cảm giác sang trọng và huyền bí."
        ),
        "prompt": (
            "Vietnamese lacquer painting son mai style, glossy black lacquer background, "
            "gold leaf and vermillion red accents, intricate traditional motifs, "
            "crackled lacquer texture, luxurious decorative fine art, deep jewel tones"
        ),
        "negative": (
            "flat cartoon, anime, 3D render, neon, matte plastic look, chữ, logo, watermark, "
            "xe hơi, kiến trúc hiện đại"
        ),
    },
    "fantasy_epic": {
        "value": "fantasy_epic",
        "label": "Huyền huyễn epic",
        "description": (
            "Fantasy digital epic: ánh sáng ma thuật, kiếm khí, núi sương mù, "
            "scale hoành tráng, palette tím–vàng huyền ảo."
        ),
        "prompt": (
            "epic fantasy digital painting, magical glowing atmosphere, misty mountains and ancient ruins, "
            "dramatic scale, ethereal purple and gold palette, cinematic concept art, "
            "detailed armor and mystical energy, high fantasy illustration"
        ),
        "negative": (
            "photorealistic modern city, smartphone, neon cyberpunk, chữ, logo, watermark, "
            "flat clipart, low detail"
        ),
    },
    "wuxia_ink": {
        "value": "wuxia_ink",
        "label": "Kiếm hiệp mực tàu",
        "description": (
            "Kiếm hiệp tranh thủy mặc: núi mây, áo bào bay, kiếm khí, "
            "không gian negative rộng, phong cách Trung Hoa–Việt cổ điển."
        ),
        "prompt": (
            "wuxia martial arts ink wash painting, flowing robes in wind, sword qi energy, "
            "misty bamboo forest and mountain peaks, generous negative space, "
            "classical East Asian brushwork, dynamic action pose, monochrome ink with subtle color accents"
        ),
        "negative": (
            "western comic, 3D render, neon cyberpunk, gun, modern clothing, chữ, logo, watermark, "
            "photorealistic portrait"
        ),
    },
    "romance_soft": {
        "value": "romance_soft",
        "label": "Ngôn tình pastel",
        "description": (
            "Minh họa ngôn tình mềm: pastel hồng–tím, ánh sáng mơ màng, "
            "bokeh nhẹ, cảm xúc ấm áp, phong cách bìa tiểu thuyết lãng mạn."
        ),
        "prompt": (
            "soft romantic illustration, dreamy pastel pink and lavender palette, gentle golden hour lighting, "
            "shallow depth of field bokeh, tender emotional atmosphere, "
            "novel cover art style, delicate facial expressions, warm intimate mood"
        ),
        "negative": (
            "horror, gore, harsh noir shadows, neon, chữ, logo, watermark, "
            "ugly distortion, low quality"
        ),
    },
    "suspense_noir": {
        "value": "suspense_noir",
        "label": "Trinh thám noir",
        "description": (
            "Film noir trinh thám: tương phản cao, bóng đổ sắc, mưa đêm, "
            "không khí căng thẳng, góc máy nghi ngờ."
        ),
        "prompt": (
            "film noir thriller illustration, high contrast chiaroscuro, rainy night urban alley, "
            "deep shadows and venetian blind light stripes, tense mysterious atmosphere, "
            "cinematic dutch angle composition, desaturated blue-black palette"
        ),
        "negative": (
            "bright cheerful colors, cartoon, anime, flat lighting, chữ, logo, watermark, "
            "daylight picnic scene"
        ),
    },
    "horror_dark": {
        "value": "horror_dark",
        "label": "Ma / Kinh dị tối",
        "description": (
            "Kinh dị u ám: tông lạnh xanh–xám, sương mù, silhouette, "
            "chi tiết rùng rợn tinh tế, không khí oán khí."
        ),
        "prompt": (
            "dark horror illustration, cold desaturated blue-gray palette, thick fog and moonlight, "
            "eerie silhouettes, subtle unsettling details, gothic atmosphere, "
            "psychological dread mood, cinematic horror concept art"
        ),
        "negative": (
            "bright daylight, cheerful cartoon, comedy, neon, chữ, logo, watermark, "
            "cute chibi style"
        ),
    },
    "scifi_concept": {
        "value": "scifi_concept",
        "label": "Khoa học viễn tưởng",
        "description": (
            "Sci-fi concept art: tàu vũ trụ, thành phố tương lai, hologram, "
            "ánh sáng lạnh xanh–cyan, chi tiết công nghệ cao."
        ),
        "prompt": (
            "science fiction concept art, futuristic spacecraft and megacity skyline, "
            "holographic displays, cool cyan and steel blue lighting, hard-surface mechanical detail, "
            "cinematic wide shot, plausible futuristic technology, matte painting quality"
        ),
        "negative": (
            "medieval fantasy, woodblock print, folk art, chữ, logo, watermark, "
            "low poly, blurry"
        ),
    },
    "anime_illustration": {
        "value": "anime_illustration",
        "label": "Anime minh họa",
        "description": (
            "Anime cel-shaded: nét sạch, mắt biểu cảm, màu tươi, "
            "light novel / visual novel key visual."
        ),
        "prompt": (
            "anime illustration key visual, clean cel-shaded coloring, expressive eyes, "
            "vibrant saturated palette, soft rim lighting, light novel cover style, "
            "detailed background with atmospheric perspective, polished Japanese animation art"
        ),
        "negative": (
            "photorealistic, western oil painting, 3D render, chữ, logo, watermark, "
            "deformed anatomy, extra limbs"
        ),
    },
    "watercolor_story": {
        "value": "watercolor_story",
        "label": "Watercolor truyện",
        "description": (
            "Minh họa watercolor sách truyện: màu loang tự nhiên, viền mềm, "
            "cảm giác ấm áp, phù hợp kể chuyện đời thường."
        ),
        "prompt": (
            "storybook watercolor illustration, soft wet-on-wet color bleeds, gentle edges, "
            "warm inviting palette, hand-painted paper texture, narrative scene composition, "
            "charming editorial illustration style"
        ),
        "negative": (
            "photorealistic, 3D render, neon, harsh black outlines only, chữ, logo, watermark, "
            "oversaturated HDR"
        ),
    },
    "comic_dynamic": {
        "value": "comic_dynamic",
        "label": "Truyện tranh động",
        "description": (
            "Comic book động lực: nét đậm, speed lines, tương phản mạnh, "
            "khung hình hành động, màu flat hoặc halftone."
        ),
        "prompt": (
            "dynamic comic book illustration, bold ink outlines, speed lines and action motion, "
            "high contrast flat colors with halftone shading, dramatic foreshortening, "
            "superhero manga hybrid style, energetic panel composition"
        ),
        "negative": (
            "soft watercolor, photorealistic, blurry, chữ, logo, watermark, "
            "static portrait only"
        ),
    },
    "drama_cinematic": {
        "value": "drama_cinematic",
        "label": "Kịch tính điện ảnh",
        "description": (
            "Cinematic realism cho drama: ánh sáng tự nhiên, tông trung tính, "
            "cảm xúc chân thực, như still frame phim đời thường."
        ),
        "prompt": (
            "cinematic drama still frame, natural motivated lighting, neutral realistic color grade, "
            "authentic emotional expression, shallow depth of field, "
            "contemporary film photography aesthetic, grounded human storytelling"
        ),
        "negative": (
            "fantasy magic, anime, cartoon, neon, oversaturated, chữ, logo, watermark, "
            "stock photo fake smile"
        ),
    },
}

DEFAULT_ARTIST_STYLE = "history_ink"

# Backward compatibility
ARTIST_VISUAL_STYLE = ARTIST_STYLES[DEFAULT_ARTIST_STYLE]["prompt"] + "."


def normalize_artist_style(value: str | None) -> str:
    key = (value or "").strip()
    if key in ARTIST_STYLES:
        return key
    return DEFAULT_ARTIST_STYLE


def get_artist_style(value: str | None) -> dict[str, str]:
    return ARTIST_STYLES[normalize_artist_style(value)]


def list_artist_styles() -> list[dict[str, str]]:
    order = [
        # Lịch sử / Việt Nam
        "history_ink",
        "history_cinematic",
        "history_oil_dramatic",
        "history_oil_indochine",
        "dong_ho_folk",
        "silk_painting",
        "lacquer_sonmai",
        # Thể loại truyện
        "fantasy_epic",
        "wuxia_ink",
        "romance_soft",
        "suspense_noir",
        "horror_dark",
        "scifi_concept",
        "drama_cinematic",
        # Minh họa phổ biến
        "anime_illustration",
        "watercolor_story",
        "comic_dynamic",
    ]
    return [
        {
            "value": ARTIST_STYLES[k]["value"],
            "label": ARTIST_STYLES[k]["label"],
            "description": ARTIST_STYLES[k]["description"],
            "prompt": ARTIST_STYLES[k]["prompt"],
        }
        for k in order
    ]


def visual_style_prefix(value: str | None) -> str:
    return get_artist_style(value)["prompt"]


def visual_negative_default(value: str | None) -> str:
    return get_artist_style(value).get("negative") or ARTIST_NEGATIVE_DEFAULT


def strip_artist_visual_style(prompt: str, artist_style: str | None = None) -> str:
    text = (prompt or "").strip()
    if not text:
        return text
    lower = text.lower()
    prefixes = [visual_style_prefix(artist_style)]
    prefixes.extend(s["prompt"] for s in ARTIST_STYLES.values())
    seen: set[str] = set()
    for prefix in sorted(prefixes, key=len, reverse=True):
        p = prefix.rstrip(".").lower()
        if not p or p in seen:
            continue
        seen.add(p)
        idx = lower.find(p)
        if idx < 0:
            continue
        rest = text[idx:]
        dot = rest.find(".")
        if dot >= 0 and dot < 280:
            return rest[dot + 1 :].strip()
    return text


def default_style_notes(extra: str | None = None, artist_style: str | None = None) -> str:
    base = visual_style_prefix(artist_style).rstrip(".")
    extra = (extra or "").strip()
    if not extra:
        return base
    if base.lower() in extra.lower():
        return extra
    return f"{base}. {extra}"
