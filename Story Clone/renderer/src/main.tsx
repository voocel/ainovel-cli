import React, { useEffect, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { api, stream, type ArtistStyleOption } from "./api/client";
import { mergeProviderModels, PROVIDERS, type ProviderPreset } from "./providers";
import "./styles/app.css";

type Tab = "dashboard" | "chapters" | "outline" | "reviews" | "artist" | "diagnostics" | "tools" | "settings" | "guide";
type StartMode = "quick" | "cocreate";

const APP_NAME = "Story Clone";

type StyleOption = { value: string; label: string; description: string; has_genre_refs?: boolean };

const STYLES_FALLBACK: StyleOption[] = [
  { value: "default", label: "Mặc định", description: "Phong cách tổng quát: nhịp truyện cân bằng, mô tả cụ thể, đối thoại tự nhiên." },
  { value: "fantasy", label: "Huyền huyễn", description: "Phiêu lưu huyền huyễn: thế giới quan dần mở, hệ thống sức mạnh có giá, chiến đấu có chiến thuật.", has_genre_refs: true },
  { value: "romance", label: "Ngôn tình", description: "Ngôn tình: tình cảm leo thang tự nhiên, căng thẳng quan hệ, chi tiết nhỏ truyền cảm.", has_genre_refs: true },
  { value: "suspense", label: "Trinh thám / Hồi hộp", description: "Trinh thám hồi hộp: đa tuyến, manh mối sớm, nhịp căng–thả xen kẽ, foreshadow rõ ràng.", has_genre_refs: true },
  { value: "ghost", label: "Truyện Ma / Kinh dị", description: "Truyện ma kinh dị: không khí u ám, dồn nén nỗi sợ, hiện tượng kỳ bí, cú quay xe bất ngờ.", has_genre_refs: true },
  { value: "drama", label: "Kịch tính / Drama", description: "Kịch tính kịch liệt: mâu thuẫn xã hội/gia đình gay gắt, xung đột lợi ích và tình cảm phức tạp.", has_genre_refs: true },
  { value: "history", label: "Lịch sử / Dã sử", description: "Lịch sử dã sử: tôn trọng các sự kiện và nhân vật lịch sử cốt lõi, hư cấu sáng tạo thêm tình tiết.", has_genre_refs: true },
  { value: "podcast_history", label: "Podcast lịch sử", description: "Kịch bản podcast lịch sử: người dẫn kể sự kiện có căn cứ, mốc thời gian rõ, hook đầu/cuối tập, tối ưu cho nghe audio.", has_genre_refs: true },
  { value: "podcast_geo", label: "Podcast địa lý", description: "Kịch bản podcast địa lý: khám phá vùng miền, địa hình, khí hậu, văn hóa gắn bản đồ và so sánh không gian.", has_genre_refs: true },
  { value: "wuxia", label: "Kiếm hiệp", description: "Kiếm hiệp giang hồ: ân oán giang hồ, võ học tinh thâm, nghĩa khí hiệp cốt, nhịp đấu võ chi tiết.", has_genre_refs: true },
  { value: "scifi", label: "Khoa học viễn tưởng", description: "Khoa học viễn tưởng: thế giới tương lai, công nghệ cao, logic khoa học giả tưởng vững chắc.", has_genre_refs: true }
];

const ARTIST_STYLES_FALLBACK: ArtistStyleOption[] = [
  {
    value: "history_ink",
    label: "Lịch sử / Dã sử — Mực tàu & khắc gỗ",
    description: "Minh họa mực tàu, khắc gỗ Đông Hồ, giấy da cừu sepia, gạch chéo tạo bóng.",
    prompt: "traditional Vietnamese historical art, ink and wash illustration, woodblock print aesthetic, vintage parchment texture, sepia tones, earthy palette, cross-hatching shadows, epic historical narrative"
  },
  {
    value: "history_cinematic",
    label: "Điện ảnh số cổ",
    description: "Minh họa kỹ thuật số cinematic, nội thất gỗ khắc, áo dài, ánh sáng ấm đất nung.",
    prompt: "realistic Vietnamese historical digital illustration, cinematic warm directional lighting, ornate dark carved wooden interior, Indochine period architecture, traditional áo dài and period dress, high-fidelity fabric and wood grain textures, shallow depth of field, romantic narrative atmosphere, warm mahogany and terracotta earthy palette"
  },
  {
    value: "history_oil_dramatic",
    label: "Sơn dầu kịch (chiaroscuro)",
    description: "Tranh sơn dầu chiaroscuro, nét cọ texture, cảm xúc kịch tính, trang phục lịch sử Việt.",
    prompt: "Vietnamese historical oil painting on canvas, dramatic chiaroscuro lighting, visible textured brushstrokes, emotional narrative realism, traditional áo dài and bà ba clothing, dark atmospheric background, cream and ochre highlights against deep shadows, cinematic portrait composition, classical fine art"
  },
  {
    value: "history_oil_indochine",
    label: "Sơn dầu Đông Dương",
    description: "Hội họa Indochine, impasto, ánh sáng vàng ấm, trang phục dân gian, chiếu cói.",
    prompt: "classical Indochine Vietnamese oil painting, impasto brushwork on textured canvas, warm golden directional light, historical folk linen blouse and indigo silk trousers, woven straw mat interior, earthy ochre and indigo palette, nostalgic fine art portrait, Tô Ngọc Vân inspired historical realism"
  },
  { value: "dong_ho_folk", label: "Tranh dân gian Đông Hồ", description: "Khắc gỗ dân gian, màu phẳng đỏ–vàng–xanh, lễ hội làng quê.", prompt: "Vietnamese Dong Ho folk woodblock print, bold black outlines, flat symbolic colors red yellow green, traditional village festival scene, decorative folk art composition, handmade paper texture, naive expressive figures, cultural heritage illustration" },
  { value: "silk_painting", label: "Tranh lụa Việt Nam", description: "Nét mảnh trên lụa, màu loang thanh tao, sen và phong cảnh Việt.", prompt: "Vietnamese traditional silk painting, delicate flowing brush lines on silk fabric, soft translucent watercolor washes, lotus flowers and birds, poetic landscape, elegant minimal composition, muted jade green and rose pink palette, fine art heritage" },
  { value: "lacquer_sonmai", label: "Sơn mài Việt Nam", description: "Nền đen bóng, chạm vàng đỏ son, hoa văn tinh xảo.", prompt: "Vietnamese lacquer painting son mai style, glossy black lacquer background, gold leaf and vermillion red accents, intricate traditional motifs, crackled lacquer texture, luxurious decorative fine art, deep jewel tones" },
  { value: "fantasy_epic", label: "Huyền huyễn epic", description: "Fantasy digital epic, ma thuật, núi sương, palette tím–vàng.", prompt: "epic fantasy digital painting, magical glowing atmosphere, misty mountains and ancient ruins, dramatic scale, ethereal purple and gold palette, cinematic concept art, detailed armor and mystical energy, high fantasy illustration" },
  { value: "wuxia_ink", label: "Kiếm hiệp mực tàu", description: "Thủy mặc kiếm hiệp, núi mây, áo bào bay, không gian âm dương.", prompt: "wuxia martial arts ink wash painting, flowing robes in wind, sword qi energy, misty bamboo forest and mountain peaks, generous negative space, classical East Asian brushwork, dynamic action pose, monochrome ink with subtle color accents" },
  { value: "romance_soft", label: "Ngôn tình pastel", description: "Pastel mềm, ánh sáng mơ màng, bìa tiểu thuyết lãng mạn.", prompt: "soft romantic illustration, dreamy pastel pink and lavender palette, gentle golden hour lighting, shallow depth of field bokeh, tender emotional atmosphere, novel cover art style, delicate facial expressions, warm intimate mood" },
  { value: "suspense_noir", label: "Trinh thám noir", description: "Film noir, bóng đổ sắc, mưa đêm, căng thẳng.", prompt: "film noir thriller illustration, high contrast chiaroscuro, rainy night urban alley, deep shadows and venetian blind light stripes, tense mysterious atmosphere, cinematic dutch angle composition, desaturated blue-black palette" },
  { value: "horror_dark", label: "Ma / Kinh dị tối", description: "Tông lạnh, sương mù, silhouette, không khí oán khí.", prompt: "dark horror illustration, cold desaturated blue-gray palette, thick fog and moonlight, eerie silhouettes, subtle unsettling details, gothic atmosphere, psychological dread mood, cinematic horror concept art" },
  { value: "scifi_concept", label: "Khoa học viễn tưởng", description: "Concept art sci-fi, thành phố tương lai, hologram, ánh sáng cyan.", prompt: "science fiction concept art, futuristic spacecraft and megacity skyline, holographic displays, cool cyan and steel blue lighting, hard-surface mechanical detail, cinematic wide shot, plausible futuristic technology, matte painting quality" },
  { value: "drama_cinematic", label: "Kịch tính điện ảnh", description: "Still frame phim, ánh sáng tự nhiên, cảm xúc chân thực.", prompt: "cinematic drama still frame, natural motivated lighting, neutral realistic color grade, authentic emotional expression, shallow depth of field, contemporary film photography aesthetic, grounded human storytelling" },
  { value: "anime_illustration", label: "Anime minh họa", description: "Cel-shaded, light novel key visual, màu tươi sáng.", prompt: "anime illustration key visual, clean cel-shaded coloring, expressive eyes, vibrant saturated palette, soft rim lighting, light novel cover style, detailed background with atmospheric perspective, polished Japanese animation art" },
  { value: "watercolor_story", label: "Watercolor truyện", description: "Màu loang sách truyện, viền mềm, ấm áp.", prompt: "storybook watercolor illustration, soft wet-on-wet color bleeds, gentle edges, warm inviting palette, hand-painted paper texture, narrative scene composition, charming editorial illustration style" },
  { value: "comic_dynamic", label: "Truyện tranh động", description: "Comic hành động, nét đậm, speed lines, halftone.", prompt: "dynamic comic book illustration, bold ink outlines, speed lines and action motion, high contrast flat colors with halftone shading, dramatic foreshortening, superhero manga hybrid style, energetic panel composition" }
];

function styleLabel(value: string, styles: StyleOption[]) {
  return styles.find(s => s.value === value)?.label ?? value;
}

const ROLES = [
  { value: "default", label: "Mặc định" },
  { value: "coordinator", label: "Điều phối" },
  { value: "architect", label: "Kiến trúc truyện" },
  { value: "writer", label: "Người viết" },
  { value: "editor", label: "Biên tập" },
  { value: "artist", label: "Hoạ sĩ AI" }
];

const THINKING_LEVELS = [
  { value: "", label: "Theo mặc định của mô hình" },
  { value: "off", label: "Tắt suy luận" },
  { value: "minimal", label: "Tối thiểu" },
  { value: "low", label: "Thấp" },
  { value: "medium", label: "Trung bình" },
  { value: "high", label: "Cao" },
  { value: "xhigh", label: "Rất cao" },
  { value: "max", label: "Tối đa" }
];

const PHASE_LABELS: Record<string, string> = {
  init: "Khởi tạo",
  premise: "Tiền đề",
  outline: "Dàn ý",
  writing: "Đang viết",
  complete: "Hoàn thành"
};

const FLOW_LABELS: Record<string, string> = {
  writing: "Viết chương",
  reviewing: "Đang đánh giá",
  rewriting: "Viết lại",
  polishing: "Đánh bóng",
  steering: "Đang xử lý góp ý"
};

const EVENT_LABELS: Record<string, string> = {
  SYSTEM: "Hệ thống",
  USER: "Người dùng",
  WRITER: "Người viết",
  EDITOR: "Biên tập",
  ARTIST: "Hoạ sĩ AI",
  ARCHITECT: "Kiến trúc",
  COORDINATOR: "Điều phối",
  IMPORT: "Nhập dữ liệu",
  SIMULATION: "Mô phỏng",
  ERROR: "Lỗi"
};

function styleDescriptionBody(option: StyleOption) {
  const idx = option.description.indexOf(":");
  if (idx > 0) return option.description.slice(idx + 1).trim();
  return option.description;
}

function StyleCallout({ option, compact }: { option: StyleOption; compact?: boolean }) {
  return (
    <div className={compact ? "styleCallout compact" : "styleCallout"} data-style={option.value}>
      <div className="styleCalloutIcon" aria-hidden="true">✦</div>
      <div className="styleCalloutBody">
        <div className="styleCalloutHead">
          <strong>{option.label}</strong>
          {option.has_genre_refs ? <span className="styleCalloutBadge">Tài liệu thể loại</span> : null}
        </div>
        <p className="styleCalloutText">{styleDescriptionBody(option)}</p>
      </div>
    </div>
  );
}

function StylePicker({ styles, value, onChange, compact, showCallout = true }: { styles: StyleOption[]; value: string; onChange: (v: string) => void; compact?: boolean; showCallout?: boolean }) {
  const selected = styles.find(s => s.value === value) ?? styles[0];
  return (
    <div className={compact ? "stylePicker compact" : "stylePicker"}>
      <label className="styleField">
        Phong cách
        <select value={value} onChange={e => onChange(e.target.value)}>
          {styles.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}
        </select>
      </label>
      {showCallout && selected ? <StyleCallout option={selected} compact={compact} /> : null}
    </div>
  );
}

function ArtistStyleCallout({ option, compact }: { option: ArtistStyleOption; compact?: boolean }) {
  return (
    <div className={compact ? "styleCallout artistStyleCallout compact" : "styleCallout artistStyleCallout"} data-artist-style={option.value}>
      <div className="styleCalloutIcon" aria-hidden="true">🎨</div>
      <div className="styleCalloutBody">
        <div className="styleCalloutHead">
          <strong>{option.label}</strong>
          <span className="styleCalloutBadge">Phong cách hoạ sĩ</span>
        </div>
        <p className="styleCalloutText">{option.description}</p>
      </div>
    </div>
  );
}

function ArtistStylePicker({ styles, value, onChange, compact, showCallout = true }: { styles: ArtistStyleOption[]; value: string; onChange: (v: string) => void; compact?: boolean; showCallout?: boolean }) {
  const selected = styles.find(s => s.value === value) ?? styles[0];
  return (
    <div className={compact ? "stylePicker artistStylePicker compact" : "stylePicker artistStylePicker"}>
      <label className="styleField">
        Phong cách hoạ sĩ
        <select value={value} onChange={e => onChange(e.target.value)}>
          {styles.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}
        </select>
      </label>
      {showCallout && selected ? <ArtistStyleCallout option={selected} compact={compact} /> : null}
    </div>
  );
}

function pretty(value: any) {
  if (value == null) return "";
  if (typeof value === "string") return value;
  return JSON.stringify(value, null, 2);
}

function parseMaybe(data: any) {
  if (typeof data !== "string") return data;
  try { return JSON.parse(data); } catch { return data; }
}

function label(tab: Tab) {
  return { dashboard: "Vận hành", chapters: "Chương", outline: "Cốt truyện", reviews: "Đánh giá", artist: "Prompt hoạ sĩ", diagnostics: "Chẩn đoán", tools: "Công cụ", settings: "Mô hình", guide: "Hướng dẫn" }[tab];
}

function phaseLabel(value: string | undefined) {
  return PHASE_LABELS[value || ""] ?? value ?? "Khởi tạo";
}

function flowLabel(value: string | undefined) {
  return FLOW_LABELS[value || ""] ?? value ?? "Viết chương";
}

function eventLabel(value: string | undefined) {
  return EVENT_LABELS[value || ""] ?? value ?? "Sự kiện";
}

function DataPanel({ title, data }: { title: string; data: any }) {
  return <div className="panel"><h2>{title}</h2><pre>{pretty(parseMaybe(data))}</pre></div>;
}


function promptItems(value: any, key: "image_prompts" | "video_prompts") {
  const parsed = parseMaybe(value?.content ?? value);
  const items = parsed?.[key];
  return Array.isArray(items) ? items : [];
}

function chapterNoFromArtifact(artifact: any): number {
  const payload = parseMaybe(artifact?.content ?? artifact);
  const fromKey = String(artifact?.key ?? "").replace(/^chapter_/, "");
  const n = Number(payload?.chapter ?? fromKey);
  return Number.isFinite(n) ? n : 0;
}

const DEFAULT_EXPORT_NEGATIVE =
  "photorealistic modern, 3D render, anime, neon, chữ, logo, watermark, xe hơi, kiến trúc hiện đại, hiện đại hóa sai bối cảnh";

function allArtistStylePrefixes(styles: ArtistStyleOption[]): string[] {
  return [...new Set(styles.map(s => s.prompt.trim()).filter(Boolean))];
}

function stripVisualStylePrefix(prompt: string, stylePrefixes: string[]): string {
  const text = (prompt || "").trim();
  if (!text) return text;
  const lower = text.toLowerCase();
  for (const prefix of [...stylePrefixes].sort((a, b) => b.length - a.length)) {
    const p = prefix.replace(/\.$/, "").toLowerCase();
    const idx = lower.indexOf(p);
    if (idx < 0) continue;
    const rest = text.slice(idx);
    const dot = rest.indexOf(".");
    if (dot >= 0 && dot < 280) return rest.slice(dot + 1).trim();
  }
  return text;
}

function defaultImageStyleFor(artistStyle: string, catalog: ArtistStyleOption[]): string {
  return (catalog.find(s => s.value === artistStyle) ?? catalog[0])?.prompt?.replace(/\.$/, "") ?? ARTIST_STYLES_FALLBACK[0].prompt;
}
const DEFAULT_VIDEO_CAMERA = "slow pan, medium shot";
const DEFAULT_VIDEO_MOTION = "chuyển động nhẹ theo nhịp kể";

type ImagePromptExport = { prompt: string; Negative: string; Style: string };
type VideoPromptExport = { prompt: string; Negative: string; Camera: string; Motion: string };
type PromptExportItem = ImagePromptExport | VideoPromptExport;

function buildPromptExportArray(
  items: any[],
  type: "image" | "video",
  chapterFrom: number,
  chapterTo: number,
  stylePrefixes: string[],
  defaultImageStyle: string
): PromptExportItem[] {
  const key = type === "image" ? "image_prompts" : "video_prompts";
  const lo = Math.min(chapterFrom, chapterTo);
  const hi = Math.max(chapterFrom, chapterTo);
  const sorted = [...items].sort((a, b) => chapterNoFromArtifact(a) - chapterNoFromArtifact(b));
  const prompts: PromptExportItem[] = [];
  for (const artifact of sorted) {
    const ch = chapterNoFromArtifact(artifact);
    if (ch < lo || ch > hi) continue;
    for (const item of promptItems(artifact, key)) {
      const prompt = stripVisualStylePrefix(String(item?.prompt ?? ""), stylePrefixes);
      if (!prompt) continue;
      if (type === "image") {
        prompts.push({
          prompt,
          Negative: String(item?.negative_prompt ?? DEFAULT_EXPORT_NEGATIVE).trim(),
          Style: String(item?.style_notes ?? defaultImageStyle).trim(),
        });
      } else {
        prompts.push({
          prompt: stripVisualStylePrefix(String(item?.prompt ?? ""), stylePrefixes),
          Negative: String(item?.negative_prompt ?? DEFAULT_EXPORT_NEGATIVE).trim(),
          Camera: String(item?.camera ?? DEFAULT_VIDEO_CAMERA).trim(),
          Motion: String(item?.motion ?? DEFAULT_VIDEO_MOTION).trim(),
        });
      }
    }
  }
  return prompts;
}

function PromptCard({ item, type }: { item: any; type: "image" | "video" }) {
  return (
    <article className="promptCard">
      <div className="promptCardHead">
        <b>{item.scene || "Cảnh"}</b>
        {type === "video" && item.duration ? <span>{item.duration}</span> : null}
      </div>
      {item.moment ? <p>{item.moment}</p> : null}
      <pre>{item.prompt || pretty(item)}</pre>
      {item.negative_prompt ? <small>Negative: {item.negative_prompt}</small> : null}
      {item.camera ? <small>Camera: {item.camera}</small> : null}
      {item.motion ? <small>Motion: {item.motion}</small> : null}
      {item.style_notes ? <small>Style: {item.style_notes}</small> : null}
      {item.characters?.length ? <small>Nhân vật: {item.characters.join(", ")}</small> : null}
    </article>
  );
}

function artistRegenProgress(events: any[], chapterNo: number, runId: string | null) {
  let imageCurrent = 0;
  let imageTarget = 0;
  let videoCurrent = 0;
  let videoTarget = 0;
  let phase: "image" | "video" | "done" = "image";
  for (const e of events) {
    if (e.category !== "ARTIST") continue;
    const p = typeof e.payload === "object" && e.payload ? e.payload : parseMaybe(e.payload_json);
    if (p?.chapter !== chapterNo) continue;
    if (runId && p?.run_id !== runId) continue;
    if (p.phase === "start") {
      imageCurrent = 0;
      imageTarget = 0;
      videoCurrent = 0;
      videoTarget = 0;
      phase = "image";
      continue;
    }
    if (p.phase === "image") {
      if (p.current != null) imageCurrent = Number(p.current);
      if (p.target != null) imageTarget = Number(p.target);
      phase = "image";
    }
    if (p.phase === "video") {
      if (p.current != null) videoCurrent = Number(p.current);
      if (p.target != null) videoTarget = Number(p.target);
      phase = "video";
    }
    if (p.phase === "done") {
      if (p.images != null) imageCurrent = Number(p.images);
      if (p.videos != null) videoCurrent = Number(p.videos);
      if (p.target_image_prompt_count != null) imageTarget = Number(p.target_image_prompt_count);
      if (p.target_video_prompt_count != null) videoTarget = Number(p.target_video_prompt_count);
      phase = "done";
    }
  }
  const total = (imageTarget || 0) + (videoTarget || 0) || 1;
  const imageDone = phase === "video" || phase === "done" ? imageTarget : imageCurrent;
  const current = imageDone + videoCurrent;
  const pct = phase === "done" ? 100 : Math.min(99, Math.round((current / total) * 100));
  return { imageCurrent, imageTarget, videoCurrent, videoTarget, phase, pct, current, total };
}

function ArtistPromptPanel({
  items,
  projectId,
  events,
  regenerating,
  setRegenerating,
  regenRunId,
  setRegenRunId,
  regenError,
  setRegenError,
  onRefresh,
  artistStyle,
  artistStyleCatalog,
}: {
  items: any[];
  projectId: string;
  events: any[];
  regenerating: number | null;
  setRegenerating: (n: number | null) => void;
  regenRunId: string | null;
  setRegenRunId: (s: string | null) => void;
  regenError: string;
  setRegenError: (s: string) => void;
  onRefresh: () => Promise<void>;
  artistStyle: string;
  artistStyleCatalog: ArtistStyleOption[];
}) {
  const chapters = useMemo(
    () => [...items].map(a => chapterNoFromArtifact(a)).filter(n => n > 0).sort((a, b) => a - b),
    [items]
  );
  const [exportType, setExportType] = useState<"image" | "video">("image");
  const [chapterFrom, setChapterFrom] = useState(1);
  const [chapterTo, setChapterTo] = useState(1);
  const [exportMsg, setExportMsg] = useState("");

  const regenProgress = regenerating ? artistRegenProgress(events, regenerating, regenRunId) : null;

  async function ensureBackendReady(): Promise<boolean> {
    let health = await api.health().catch(() => ({ ok: false, api: 0 }));
    if (health.api && health.api >= 2) return true;
    if (window.storeOpen?.restartBackend) {
      const restarted = await window.storeOpen.restartBackend();
      if (restarted.ok) return true;
    }
    health = await api.health().catch(() => ({ ok: false, api: 0 }));
    return !!(health.api && health.api >= 2);
  }

  async function regenerateChapter(chapterNo: number) {
    if (!projectId || regenerating) return;
    setRegenError("");
    const ready = await ensureBackendReady();
    if (!ready) {
      setRegenError("Backend chưa cập nhật. Đóng app hoàn toàn rồi chạy lại npm run dev.");
      return;
    }
    const runId = crypto.randomUUID();
    setRegenRunId(runId);
    setRegenerating(chapterNo);
    // #region agent log
    fetch('http://127.0.0.1:7663/ingest/84a5a951-a0be-4832-99f9-4f7d3f37f8f6',{method:'POST',headers:{'Content-Type':'application/json','X-Debug-Session-Id':'417abf'},body:JSON.stringify({sessionId:'417abf',location:'main.tsx:regenerateChapter',message:'regenerate started',data:{projectId,chapterNo,runId},timestamp:Date.now(),hypothesisId:'accum',runId:'fix'})}).catch(()=>{});
    // #endregion
    try {
      await api.regenerateArtistPrompts(projectId, chapterNo, runId);
      await onRefresh();
      // #region agent log
      fetch('http://127.0.0.1:7663/ingest/84a5a951-a0be-4832-99f9-4f7d3f37f8f6',{method:'POST',headers:{'Content-Type':'application/json','X-Debug-Session-Id':'417abf'},body:JSON.stringify({sessionId:'417abf',location:'main.tsx:regenerateChapter',message:'regenerate done',data:{chapterNo,runId},timestamp:Date.now(),hypothesisId:'accum',runId:'fix'})}).catch(()=>{});
      // #endregion
    } catch (err: any) {
      const msg = String(err?.message || err);
      setRegenError(msg.includes("Not Found") ? "API chưa cập nhật. Hãy chạy lại npm run dev để khởi động lại backend." : (msg || "Không tạo lại được prompt hoạ sĩ."));
      // #region agent log
      fetch('http://127.0.0.1:7663/ingest/84a5a951-a0be-4832-99f9-4f7d3f37f8f6',{method:'POST',headers:{'Content-Type':'application/json','X-Debug-Session-Id':'417abf'},body:JSON.stringify({sessionId:'417abf',location:'main.tsx:regenerateChapter',message:'regenerate failed',data:{chapterNo,error:String(err?.message||err)},timestamp:Date.now(),hypothesisId:'regen',runId:'pre-fix'})}).catch(()=>{});
      // #endregion
    } finally {
      setRegenerating(null);
      setRegenRunId(null);
    }
  }

  useEffect(() => {
    if (chapters.length) {
      setChapterFrom(chapters[0]);
      setChapterTo(chapters[chapters.length - 1]);
    }
  }, [chapters.join(",")]);

  const stylePrefixes = useMemo(() => allArtistStylePrefixes(artistStyleCatalog), [artistStyleCatalog]);
  const defaultImageStyle = useMemo(
    () => defaultImageStyleFor(artistStyle, artistStyleCatalog),
    [artistStyle, artistStyleCatalog]
  );

  const exportArray = useMemo(
    () => buildPromptExportArray(items, exportType, chapterFrom, chapterTo, stylePrefixes, defaultImageStyle),
    [items, exportType, chapterFrom, chapterTo, stylePrefixes, defaultImageStyle]
  );
  const exportJson = useMemo(() => JSON.stringify(exportArray, null, 2), [exportArray]);

  async function copyExportJson() {
    // #region agent log
    fetch('http://127.0.0.1:7663/ingest/84a5a951-a0be-4832-99f9-4f7d3f37f8f6',{method:'POST',headers:{'Content-Type':'application/json','X-Debug-Session-Id':'417abf'},body:JSON.stringify({sessionId:'417abf',location:'main.tsx:copyExportJson',message:'artist prompt json export',data:{type:exportType,chapterFrom,chapterTo,count:exportArray.length},timestamp:Date.now(),hypothesisId:'export',runId:'feature'})}).catch(()=>{});
    // #endregion
    await navigator.clipboard.writeText(exportJson);
    setExportMsg(`Đã sao chép ${exportArray.length} prompt ${exportType === "image" ? "ảnh" : "video"}.`);
  }

  async function downloadExportJson() {
    const defaultName = `prompts-ch${chapterFrom}-${chapterTo}-${exportType}.json`;
    const path = await window.storeOpen?.saveFile(defaultName, "json");
    const blob = new Blob([exportJson], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = path ? path.split(/[/\\]/).pop() ?? defaultName : defaultName;
    a.click();
    URL.revokeObjectURL(url);
    setExportMsg(`Đã tải ${exportArray.length} prompt → ${a.download}`);
  }

  return (
    <section className="artistPage">
      {items.length === 0 ? (
        <div className="panel artistEmpty"><h2>Prompt hoạ sĩ</h2><p>Chưa có prompt. Khi một chương được commit, Hoạ sĩ AI sẽ tự tạo prompt ảnh và video theo từng cảnh.</p></div>
      ) : (
        <>
          {regenerating ? (
            <div className="panel artistRegenStatus">
              <p>
                Đang tạo lại prompt chương {regenerating}
                {regenProgress?.phase === "done"
                  ? " · Hoàn tất"
                  : regenProgress?.phase === "video"
                    ? ` · Video ${regenProgress.videoCurrent}/${regenProgress.videoTarget || "…"}`
                    : ` · Ảnh ${regenProgress?.imageCurrent ?? 0}/${regenProgress?.imageTarget || "…"}`}
              </p>
              <div className="progress artistRegenBar"><span style={{ width: `${regenProgress?.pct || 8}%` }} /></div>
            </div>
          ) : null}
          {regenError ? <div className="panel artistRegenError">{regenError}</div> : null}
          <div className="panel artistExport">
            <h2>Xuất JSON prompt</h2>
            <p>Mảng JSON theo thứ tự từ trên xuống (chương thấp → cao). Ảnh: <code>prompt</code>, <code>Negative</code>, <code>Style</code>. Video: <code>prompt</code>, <code>Negative</code>, <code>Camera</code>, <code>Motion</code>.</p>
            <div className="artistExportRow">
              <label className="artistExportField">
                Loại
                <select className="artistExportSelect" value={exportType} onChange={e => setExportType(e.target.value as "image" | "video")}>
                  <option value="image">Prompt ảnh</option>
                  <option value="video">Prompt video</option>
                </select>
              </label>
              <label className="artistExportField">
                Từ chương
                <select className="artistExportSelect" value={chapterFrom} onChange={e => setChapterFrom(Number(e.target.value))}>
                  {chapters.map(ch => <option key={ch} value={ch}>Chương {ch}</option>)}
                </select>
              </label>
              <label className="artistExportField">
                Đến chương
                <select className="artistExportSelect" value={chapterTo} onChange={e => setChapterTo(Number(e.target.value))}>
                  {chapters.map(ch => <option key={ch} value={ch}>Chương {ch}</option>)}
                </select>
              </label>
              <div className="artistExportCount">{exportArray.length} prompt</div>
            </div>
            <div className="artistExportActions">
              <button type="button" onClick={copyExportJson} disabled={exportArray.length === 0}>Sao chép JSON</button>
              <button type="button" onClick={downloadExportJson} disabled={exportArray.length === 0}>Tải file .json</button>
            </div>
            {exportMsg ? <small className="artistExportMsg">{exportMsg}</small> : null}
            <pre className="artistExportPreview">{exportJson}</pre>
          </div>
          {items.map((artifact: any) => {
        const payload = parseMaybe(artifact.content);
        const chapterNo = Number(payload?.chapter ?? String(artifact.key ?? "").replace(/^chapter_/, ""));
        const imageItems = promptItems(payload, "image_prompts");
        const videoItems = promptItems(payload, "video_prompts");
        return (
          <div className="panel artistChapter" key={artifact.key}>
            <div className="artistChapterHead">
              <h2>Chương {chapterNo || payload?.chapter || String(artifact.key ?? "").replace(/^chapter_/, "")}</h2>
              <button
                type="button"
                disabled={!projectId || !chapterNo || regenerating !== null}
                onClick={() => regenerateChapter(chapterNo)}
              >
                {regenerating === chapterNo ? "Đang tạo lại…" : "Tạo lại prompt"}
              </button>
            </div>
            {payload?.narration_timing ? (
              <p className="artistTiming">
                ~{payload.narration_timing.estimated_audio_seconds}s audio thuyết minh ({payload.narration_timing.word_count} từ, {payload.narration_timing.wpm_assumption} từ/phút)
                {" · "}
                {imageItems.length}/{payload.narration_timing.target_image_prompt_count ?? payload.narration_timing.target_prompt_count} prompt ảnh × {payload.narration_timing.image_segment_seconds ?? payload.narration_timing.segment_seconds}s
                {" · "}
                {videoItems.length}/{payload.narration_timing.target_video_prompt_count ?? videoItems.length} prompt video × {payload.narration_timing.video_segment_seconds ?? "20"}s
              </p>
            ) : null}
            <div className="artistColumns">
              <div className="artistColumn">
                <h3>Prompt tạo ảnh</h3>
                {imageItems.length === 0 ? <p>Chưa có prompt ảnh.</p> : imageItems.map((item: any, i: number) => <PromptCard key={i} item={item} type="image" />)}
              </div>
              <div className="artistColumn">
                <h3>Prompt tạo video</h3>
                {videoItems.length === 0 ? <p>Chưa có prompt video.</p> : videoItems.map((item: any, i: number) => <PromptCard key={i} item={item} type="video" />)}
              </div>
            </div>
          </div>
        );
      })}
        </>
      )}
    </section>
  );
}
function ToolPanel({ title, text, action }: { title: string; text: string; action: () => void }) {
  return <div className="panel"><h2>{title}</h2><p>{text}</p><button onClick={action}>Chạy</button></div>;
}

function ProviderList({ items, onDelete }: { items: any[]; onDelete: (name: string) => void }) {
  return <div className="panel"><h2>Nhà cung cấp đã lưu</h2><div className="miniList">{items.length === 0 ? <p>Chưa có nhà cung cấp nào.</p> : items.map((item, index) => <div className="miniCard" key={`${item.name}-${index}`}><div className="miniCardHead"><b>{item.name}</b><button className="dangerButton" onClick={() => onDelete(item.name)}>Xóa</button></div><span>Giao thức: {item.type || "tự động"}</span><span>Địa chỉ API: {item.base_url || "mặc định của nhà cung cấp"}</span><span>Mô hình: {(item.models || []).join(", ") || "chưa khai báo"}</span></div>)}</div></div>;
}

function RoleModelList({ items, onDelete }: { items: any[]; onDelete: (role: string) => void }) {
  return <div className="panel"><h2>Phân vai mô hình</h2><div className="miniList">{items.length === 0 ? <p>Chưa gán mô hình cho vai trò nào.</p> : items.map((item, index) => <div className="miniCard" key={`${item.role}-${index}`}><div className="miniCardHead"><b>{ROLES.find(role => role.value === item.role)?.label ?? item.role}</b><button className="dangerButton" onClick={() => onDelete(item.role)}>Xóa</button></div><span>Nhà cung cấp: {item.provider}</span><span>Mô hình: {item.model}</span><span>Suy luận: {THINKING_LEVELS.find(level => level.value === item.thinking)?.label ?? "Theo mặc định"}</span></div>)}</div></div>;
}

type GuideBlock = {
  title: string;
  badge?: string;
  intro?: string;
  steps: string[];
  tip?: string;
};

const GUIDE_QUICK_START = [
  "Nhập tên tác phẩm ở sidebar → nhấn + để tạo dự án mới.",
  "Vào tab Mô hình → chọn nhà cung cấp AI, nhập API key → Lưu thiết lập mô hình.",
  "Quay tab Vận hành → chọn Nhanh hoặc Đồng sáng tác, phong cách, số chương.",
  "Nhập ý tưởng truyện → nhấn Tạo tiểu thuyết và theo dõi khung Gần đây.",
  "Sau khi xong, xem tab Chương / Cốt truyện → xuất TXT hoặc EPUB ở tab Công cụ."
];

const GUIDE_BLOCKS: GuideBlock[] = [
  {
    title: "Thanh bên & Thư viện tác phẩm",
    badge: "Sidebar",
    intro: "Quản lý nhiều tác phẩm độc lập; mỗi tác phẩm có tiến độ, chương và dữ liệu riêng trong SQLite.",
    steps: [
      "Ô Tên tác phẩm: gõ tên truyện trước khi tạo hoặc sửa tên tác phẩm đang chọn.",
      "Nút +: tạo tác phẩm mới với tên đã nhập (mặc định \"Tác phẩm mới\" nếu để trống).",
      "Lưu tên: cập nhật tên tác phẩm đang chọn mà không tạo mới.",
      "Xóa: xóa vĩnh viễn tác phẩm đang chọn (không hoàn tác).",
      "Thư viện: danh sách tác phẩm — nhấn để chuyển dự án; mục sáng là tác phẩm đang mở."
    ]
  },
  {
    title: "Tab Vận hành — Khởi động",
    badge: "Vận hành",
    steps: [
      "Nhanh: nhập ý tưởng một lần rồi bắt đầu viết ngay.",
      "Đồng sáng tác: trao đổi với AI để hoàn thiện brief trước khi viết; nhấn Gửi sau mỗi tin nhắn.",
      "Phong cách (4 loại): Mặc định · Huyền huyễn · Ngôn tình · Trinh thám/Hồi hộp — ảnh hưởng prompt writer và tài liệu tham chiếu thể loại.",
      "Số chương: mục tiêu tổng số chương (1–1000); engine dừng khi đạt mục tiêu.",
      "Tạo tiểu thuyết: bắt đầu pipeline AI (cần chọn tác phẩm trước).",
      "Tiếp tục: khôi phục phiên viết từ checkpoint sau khi dừng hoặc đóng app.",
      "Dừng: hủy phiên đang chạy ngay lập tức."
    ],
    tip: "Chưa cấu hình API key? Ứng dụng dùng mock LLM để demo pipeline; nội dung sẽ là văn bản mẫu."
  },
  {
    title: "Tab Vận hành — Tinh chỉnh & Theo dõi",
    badge: "Vận hành",
    steps: [
      "Gửi can thiệp: đổi hướng truyện, yêu cầu viết lại chương, điều chỉnh phong cách khi engine đang chạy.",
      "Tiếp tục (khung Tinh chỉnh): thêm chỉ thị bổ sung; nếu engine đã dừng sẽ tự khởi động lại.",
      "Token / Chi phí: theo dõi token đầu vào/ra theo provider và model.",
      "Gần đây: nhật ký realtime — Kiến trúc, Người viết, Biên tập, Lỗi, v.v.",
      "Hàng đợi viết lại: hiện khi biên tập yêu cầu rewrite/polish; nhấn Tiếp tục để xử lý."
    ]
  },
  {
    title: "Pipeline AI (cách engine viết)",
    badge: "Kỹ thuật",
    intro: "Mô phỏng workflow đa-agent từ ainovel-cli gốc:",
    steps: [
      "1. Kiến trúc: lập premise, dàn ý, nhân vật, luật thế giới, compass.",
      "2. Mỗi chương — Lập kế hoạch → Viết draft → Kiểm tra nhất quán → Biên tập đánh giá.",
      "3. Verdict biên tập: accept (chấp nhận) / polish (đánh bóng) / rewrite (viết lại).",
      "4. Commit chương vào SQLite; cập nhật tiến độ và checkpoint.",
      "5. Điều phối xử lý can thiệp người dùng và hàng đợi viết lại."
    ]
  },
  {
    title: "Tab Chương",
    badge: "Chương",
    steps: [
      "Danh sách trái: tất cả chương kèm trạng thái (đã lập kế hoạch / nháp / đã hoàn tất) và số từ.",
      "Chọn chương để đọc và chỉnh sửa nội dung trong khung bên phải.",
      "Lưu chỉnh sửa: ghi đè nội dung chương đã chọn (commit thủ công).",
      "Yêu cầu viết lại: đưa chương vào hàng đợi rewrite và khởi động lại engine."
    ]
  },
  {
    title: "Tab Cốt truyện",
    badge: "Cốt truyện",
    steps: [
      "Dàn ý: outline từng chương do Kiến trúc tạo hoặc suy luận khi import.",
      "Nhân vật: danh sách nhân vật, vai trò, mô tả.",
      "Thế giới: luật thế giới và thiết lập cần giữ nhất quán xuyên suốt."
    ],
    tip: "Dữ liệu hiển thị dạng JSON — dùng để kiểm tra engine đã lập nền đúng chưa."
  },
  {
    title: "Tab Đánh giá",
    badge: "Đánh giá",
    steps: [
      "Mỗi thẻ là kết quả biên tập một chương (hoặc phạm vi arc/volume).",
      "Chú ý verdict: accept / polish / rewrite và điểm score.",
      "Ghi chú notes giải thích lý do cần sửa — tham khảo khi chỉnh tay hoặc can thiệp."
    ]
  },
  {
    title: "Tab Chẩn đoán",
    badge: "Chẩn đoán",
    steps: [
      "Thống kê: số chương, từ, phase, flow, số review và artifact.",
      "Phát hiện: cảnh báo thiếu dữ liệu, review điểm thấp, hàng đợi rewrite.",
      "Xuất báo cáo MD: lưu file markdown để debug hoặc chia sẻ."
    ],
    tip: "Dùng khi truyện kẹt tiến độ, thiếu foundation, hoặc nhiều chương bị reject."
  },
  {
    title: "Tab Công cụ — Nhập truyện",
    badge: "Công cụ",
    steps: [
      "Nhập tiểu thuyết: chọn file .txt / .md → tách chương tự động.",
      "LLM suy luận premise, nhân vật, thế giới từ nội dung import.",
      "Phân tích từng chương và lưu vào DB để tiếp tục viết.",
      "Từ chương: chỉ import từ chương N trở đi (bỏ qua phần đầu file)."
    ]
  },
  {
    title: "Tab Công cụ — Mô phỏng & Xuất",
    badge: "Công cụ",
    steps: [
      "Hồ sơ mô phỏng: quét thư mục chứa .txt/.md mẫu → LLM phân tích văn phong.",
      "Nhập hồ sơ mô phỏng: nạp file profile JSON có sẵn.",
      "Profile ảnh hưởng phong cách khi viết — mượn cấu trúc/nhịp, không sao chép nội dung.",
      "Xuất TXT / EPUB: chọn đường dẫn lưu → chỉ xuất chương đã hoàn tất (committed)."
    ]
  },
  {
    title: "Tab Mô hình — Cấu hình AI",
    badge: "Mô hình",
    steps: [
      "Chọn nhà cung cấp: OpenRouter, Anthropic, Gemini, OpenAI, DeepSeek, Qwen, GLM, Grok, Mimo, Ollama, Bedrock.",
      "Vai trò áp dụng: Mặc định / Điều phối / Kiến trúc / Người viết / Biên tập — mỗi vai trò có thể dùng model khác.",
      "Mức suy luận: điều chỉnh reasoning (nếu provider hỗ trợ).",
      "Địa chỉ API & Khóa API: bắt buộc với cloud; Ollama local không cần key.",
      "Lưu thiết lập mô hình: ghi provider và gán model cho vai trò đã chọn."
    ],
    tip: "Gợi ý: Writer dùng model nhanh; Architect/Editor dùng model mạnh hơn cho chất lượng."
  },
  {
    title: "Chạy AI cục bộ (Ollama)",
    badge: "Mô hình",
    steps: [
      "Cài Ollama và kéo model, ví dụ: ollama pull qwen3:14b",
      "Tab Mô hình → chọn Ollama cục bộ.",
      "Giữ API: http://localhost:11434/v1 — không cần API key.",
      "Chọn model đã kéo, lưu và gán cho vai trò Người viết / Mặc định."
    ]
  },
];

const GUIDE_FAQ: GuideBlock = {
  title: "Xử lý sự cố",
  badge: "FAQ",
  steps: [
    "Giao diện cũ sau khi sửa code: nhấn Ctrl+R trong cửa sổ Electron.",
    "Backend không phản hồi: mở http://127.0.0.1:8766/health — phải trả {\"ok\": true}.",
    "Không tạo được tác phẩm: kiểm tra backend đang chạy (npm run dev).",
    "Nội dung toàn văn bản mock: chưa cấu hình API key hoặc key sai.",
    "Phiên viết dừng giữa chừng: nhấn Tiếp tục để resume từ checkpoint.",
    "Event ERROR trong Gần đây: đọc summary → tab Chẩn đoán để biết hướng xử lý."
  ]
};

function GuideCard({ block }: { block: GuideBlock }) {
  return (
    <article className="panel guideCard">
      <div className="guideCardHead">
        <h2>{block.title}</h2>
        {block.badge ? <span className="guideBadge">{block.badge}</span> : null}
      </div>
      {block.intro ? <p className="guideIntro">{block.intro}</p> : null}
      <ol className="guideSteps">
        {block.steps.map((step, i) => <li key={i}>{step}</li>)}
      </ol>
      {block.tip ? <p className="guideTip"><b>Mẹo:</b> {block.tip}</p> : null}
    </article>
  );
}
function App() {
  const [projects, setProjects] = useState<any[]>([]);
  const [projectId, setProjectId] = useState<string>("");
  const [snapshot, setSnapshot] = useState<any>(null);
  const [tab, setTab] = useState<Tab>("dashboard");
  const [prompt, setPrompt] = useState("");
  const [chaptersTarget, setChaptersTarget] = useState(5);
  const [steer, setSteer] = useState("");
  const [selectedChapter, setSelectedChapter] = useState<any>(null);
  const [events, setEvents] = useState<any[]>([]);
  const [providers, setProviders] = useState<any[]>([]);
  const [models, setModels] = useState<any>({ roles: [], providers: [], catalog: {}, presets: PROVIDERS });
  const [diag, setDiag] = useState<any>(null);
  const [reviews, setReviews] = useState<any[]>([]);
  const [artistPrompts, setArtistPrompts] = useState<any[]>([]);
  const [artistRegenerating, setArtistRegenerating] = useState<number | null>(null);
  const [artistRegenRunId, setArtistRegenRunId] = useState<string | null>(null);
  const [artistRegenError, setArtistRegenError] = useState("");
  const artistRefreshTimerRef = React.useRef<number | null>(null);
  const [outline, setOutline] = useState<any>(null);
  const [characters, setCharacters] = useState<any>(null);
  const [world, setWorld] = useState<any>(null);
  const [generatedPremise, setGeneratedPremise] = useState("");
  const [formProvider, setFormProvider] = useState({ provider: "9router", role: "default", model: "ag/gemini-3.5-flash-low", thinking: "", api_key: "", base_url: "http://localhost:20128/v1", customModel: "" });
  const [startMode, setStartMode] = useState<StartMode>("quick");
  const [style, setStyle] = useState("default");
  const [artistStyle, setArtistStyle] = useState("history_ink");
  const [cocreateMessages, setCocreateMessages] = useState<{ role: string; content: string }[]>([]);
  const [cocreateInput, setCocreateInput] = useState("");
  const [cocreateBrief, setCocreateBrief] = useState("");
  const [importFrom, setImportFrom] = useState(1);
  const [chapterEdit, setChapterEdit] = useState("");
  const [usage, setUsage] = useState<any[]>([]);
  const [projectName, setProjectName] = useState("");
  const [styleCatalog, setStyleCatalog] = useState<StyleOption[]>(STYLES_FALLBACK);
  const [artistStyleCatalog, setArtistStyleCatalog] = useState<ArtistStyleOption[]>(ARTIST_STYLES_FALLBACK);
  const [starting, setStarting] = useState(false);
  const [actionError, setActionError] = useState("");
  const [backendOnline, setBackendOnline] = useState(true);
  const backendFailStreakRef = React.useRef(0);
  const snapshotDebounceRef = React.useRef<number | null>(null);
  const [renameProjectId, setRenameProjectId] = useState<string | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const activeProject = projects.find(p => p.id === projectId);
  const selectedPreset = useMemo(() => PROVIDERS.find(p => p.name === formProvider.provider) ?? PROVIDERS[0], [formProvider.provider]);
  const availableModels = useMemo(() => {
    const saved = providers.find(p => p.name === formProvider.provider)?.models;
    return mergeProviderModels(formProvider.provider, models.catalog, saved);
  }, [formProvider.provider, models.catalog, providers]);

  // Ref to cancel in-flight refreshSnapshot when project changes
  const snapshotAbortRef = React.useRef<AbortController | null>(null);

  async function refreshProjects(activeId?: string) {
    const items = await api.projects();
    // Sort by created_at descending so the most recently created project is first
    const sorted = [...items].sort((a, b) => {
      const ta = a.created_at ?? "";
      const tb = b.created_at ?? "";
      return tb.localeCompare(ta);
    });
    setProjects(sorted);
    const currentId = activeId !== undefined ? activeId : projectId;
    if (!currentId && sorted[0]) setProjectId(sorted[0].id);
  }

  async function refreshSnapshot(id = projectId) {
    if (!id) return;
    // Cancel any previous in-flight snapshot request for this slot
    if (snapshotAbortRef.current) snapshotAbortRef.current.abort();
    const ctrl = new AbortController();
    snapshotAbortRef.current = ctrl;
    try {
      const snap = await api.snapshot(id);
      // If this call was superseded by a newer one, discard result
      if (ctrl.signal.aborted) return;
      setSnapshot(snap);
      setEvents(snap.events ?? []);
      setUsage(snap.usage ?? []);
      // Always sync prompt and premise from the loaded project (clear if empty)
      setPrompt(typeof snap.story_brief === "string" ? snap.story_brief : "");
      setGeneratedPremise(typeof snap.premise === "string" ? snap.premise : "");
      if (snap.progress?.total_chapters) setChaptersTarget(Number(snap.progress.total_chapters));
      if (selectedChapter) {
        const updated = (snap.chapters ?? []).find((c: any) => c.chapter_no === selectedChapter.chapter_no);
        if (updated) {
          setSelectedChapter(updated);
          setChapterEdit(updated.final_text || updated.draft_text || "");
        }
      }
    } catch (err: any) {
      if (err?.name === "AbortError") return; // Intentionally cancelled — ignore
      throw err;
    }
  }

  async function refreshTabData() {
    if (!projectId) return;
    if (tab === "outline") {
      setOutline(await api.outline(projectId));
      setCharacters(await api.characters(projectId));
      setWorld(await api.world(projectId));
    }
    if (tab === "reviews") setReviews(await api.reviews(projectId));
    if (tab === "artist") refreshArtistPrompts().catch(console.error);
    if (tab === "diagnostics") setDiag(await api.diagnostics(projectId));
    if (tab === "settings") {
      setProviders(await api.providers());
      setModels(await api.models());
    }
  }

  async function refreshArtistPrompts() {
    if (!projectId) return;
    setArtistPrompts(await api.artistPrompts(projectId));
  }

  useEffect(() => { refreshProjects().catch(console.error); api.styles().then(setStyleCatalog).catch(() => setStyleCatalog(STYLES_FALLBACK)); api.artistStyles().then(setArtistStyleCatalog).catch(() => setArtistStyleCatalog(ARTIST_STYLES_FALLBACK)); }, []);
  useEffect(() => {
    let alive = true;
    const checkBackend = async () => {
      try {
        await api.health();
        if (!alive) return;
        backendFailStreakRef.current = 0;
        setBackendOnline(true);
      } catch {
        if (!alive) return;
        backendFailStreakRef.current += 1;
        if (backendFailStreakRef.current >= 2) setBackendOnline(false);
      }
    };
    checkBackend();
    const timer = window.setInterval(checkBackend, 4000);
    return () => { alive = false; window.clearInterval(timer); };
  }, []);
  useEffect(() => {
    if (activeProject?.style) setStyle(activeProject.style);
    if (activeProject?.artist_style) setArtistStyle(activeProject.artist_style);
  }, [projectId, activeProject?.style, activeProject?.artist_style]);
  useEffect(() => {
    setArtistRegenerating(null);
    setArtistRegenRunId(null);
    setArtistRegenError("");
  }, [projectId]);
  useEffect(() => { refreshSnapshot().catch(console.error); }, [projectId]);
  useEffect(() => { refreshTabData().catch(console.error); }, [tab, projectId]);

  useEffect(() => {
    if (!projectId || artistRegenerating === null) return;
    const chapterNo = artistRegenerating;
    const hasArtist = events.some(e => {
      if (e.category !== "ARTIST") return false;
      const p = typeof e.payload === "object" && e.payload ? e.payload : (() => { try { return JSON.parse(e.payload_json || "{}"); } catch { return null; } })();
      if (p?.chapter !== chapterNo) return false;
      if (artistRegenRunId && p?.run_id !== artistRegenRunId) return false;
      return true;
    });
    if (!hasArtist) return;
    if (artistRefreshTimerRef.current) window.clearTimeout(artistRefreshTimerRef.current);
    artistRefreshTimerRef.current = window.setTimeout(() => {
      refreshArtistPrompts().catch(console.error);
    }, 400);
    return () => {
      if (artistRefreshTimerRef.current) window.clearTimeout(artistRefreshTimerRef.current);
    };
  }, [events, artistRegenerating, artistRegenRunId, projectId]);
  // WebSocket effect: reconnects automatically if connection drops
  useEffect(() => {
    if (!projectId) return;
    let ws: WebSocket | null = null;
    let destroyed = false;
    let reconnectTimer: number | null = null;

    function connect() {
      if (destroyed) return;
      ws = stream(projectId);
      ws.onmessage = event => {
        try {
          const data = JSON.parse(event.data);
          setEvents(prev => [...prev.slice(-200), data]);
          if (snapshotDebounceRef.current) window.clearTimeout(snapshotDebounceRef.current);
          snapshotDebounceRef.current = window.setTimeout(() => {
            refreshSnapshot(projectId).catch(console.error);
          }, 600);
        } catch { /* ignore malformed frames */ }
      };
      ws.onerror = () => {
        ws?.close();
      };
      ws.onclose = () => {
        if (!destroyed) {
          // Auto-reconnect after 3 seconds so the app never gets stuck
          reconnectTimer = window.setTimeout(connect, 3000);
        }
      };
    }

    connect();
    return () => {
      destroyed = true;
      if (reconnectTimer !== null) window.clearTimeout(reconnectTimer);
      if (snapshotDebounceRef.current) window.clearTimeout(snapshotDebounceRef.current);
      ws?.close();
    };
  }, [projectId]);

  // Global Escape key: dismiss stuck rename overlay (prevents invisible overlay blocking clicks)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === "Escape") setRenameProjectId(null);
    };
    window.addEventListener("keydown", handler);
    return () => window.removeEventListener("keydown", handler);
  }, []);

  const chapters = snapshot?.chapters ?? [];
  const progress = snapshot?.progress ?? snapshot?.project?.progress ?? activeProject?.progress ?? {};
  const pendingRewrites: number[] = snapshot?.pending_rewrites ?? [];
  const percent = progress.total_chapters ? Math.round((progress.current_chapter / progress.total_chapters) * 100) : 0;
  const selectedStyle = styleCatalog.find(s => s.value === style) ?? styleCatalog[0];
  const selectedArtistStyle = artistStyleCatalog.find(s => s.value === artistStyle) ?? artistStyleCatalog[0];
  const isPodcastStyle = style === "podcast_history" || style === "podcast_geo";

  useEffect(() => {
    if (activeProject) setProjectName(activeProject.name || "");
  }, [projectId, activeProject?.name]);

  async function changeStyle(next: string) {
    setStyle(next);
    if (projectId) {
      await api.patchProject(projectId, { style: next });
      await refreshProjects(projectId);
    }
  }

  async function changeArtistStyle(next: string) {
    setArtistStyle(next);
    if (projectId) {
      await api.patchProject(projectId, { artist_style: next });
      await refreshProjects(projectId);
    }
  }

  async function createProject() {
    const name = projectName.trim() || "Tác phẩm mới";
    const project = await api.createProject({ name, style, artist_style: artistStyle });
    // Clear all stale content from previous project before switching
    setPrompt("");
    setGeneratedPremise("");
    setSnapshot(null);
    setEvents([]);
    setUsage([]);
    setArtistPrompts([]);
    setSelectedChapter(null);
    setChapterEdit("");
    setChaptersTarget(5);
    await refreshProjects(project.id);
    setProjectId(project.id);
    setProjectName(name);
  }

  async function saveProjectName() {
    if (!projectId) return;
    const name = projectName.trim() || "Tác phẩm mới";
    await api.patchProject(projectId, { name, style, artist_style: artistStyle });
    await refreshProjects(projectId);
    await refreshSnapshot();
  }

  async function removeProject() {
    if (!projectId || !window.confirm("Xóa tác phẩm này?")) return;
    await api.deleteProject(projectId);
    setProjectId("");
    setSnapshot(null);
    await refreshProjects("");
  }

  async function start() {
    if (!projectId || starting) return;
    if (!prompt.trim()) {
      setActionError("Vui lòng nhập mô tả cốt truyện trước khi tạo tiểu thuyết.");
      return;
    }
    setStarting(true);
    setActionError("");
    try {
      await api.health();
      setBackendOnline(true);
      backendFailStreakRef.current = 0;
      const brief = startMode === "cocreate" && cocreateBrief ? cocreateBrief : prompt;
      await api.start(projectId, { prompt: brief, total_chapters: chaptersTarget, style });
      try {
        await refreshSnapshot();
      } catch {
        // Pipeline có thể vẫn chạy; WebSocket sẽ cập nhật sự kiện sau.
      }
    } catch (err: any) {
      const msg = String(err?.message || err || "Không thể khởi động pipeline");
      setActionError(msg.length > 240 ? `${msg.slice(0, 240)}…` : msg);
    } finally {
      setStarting(false);
    }
  }

  async function sendCocreate() {
    if (!cocreateInput.trim() || !projectId) return;
    const msgs = [...cocreateMessages, { role: "user", content: cocreateInput.trim() }];
    setCocreateMessages(msgs);
    setCocreateInput("");
    try {
      const res = await api.cocreate(projectId, { messages: msgs, style });
      setCocreateMessages([...msgs, { role: "assistant", content: res.reply }]);
      setCocreateBrief(res.reply);
    } catch (err: any) {
      // Revert the optimistic user message on failure
      setCocreateMessages(cocreateMessages);
      setActionError(String(err?.message || "Không thể kết nối AI để tư vấn cốt truyện."));
    }
  }

  async function saveChapterEdit() {
    if (!selectedChapter || !projectId) return;
    await api.patchChapter(projectId, selectedChapter.chapter_no, { final_text: chapterEdit, status: "committed" });
    await refreshSnapshot();
  }

  async function reopenChapter() {
    if (!selectedChapter || !projectId) return;
    await api.reopen(projectId, { chapters: [selectedChapter.chapter_no], reason: steer || "Yêu cầu sửa từ UI" });
    await refreshSnapshot();
  }

  function chooseProvider(providerName: string) {
    const preset = PROVIDERS.find(p => p.name === providerName) ?? PROVIDERS[0];
    const saved = providers.find(p => p.name === providerName)?.models;
    const modelList = mergeProviderModels(providerName, models.catalog, saved);
    setFormProvider({ ...formProvider, provider: preset.name, model: modelList[0] || preset.models[0], base_url: preset.base_url, customModel: "" });
  }

  async function saveProvider() {
    const model = formProvider.customModel.trim() || formProvider.model;
    const presetModels = mergeProviderModels(selectedPreset.name, models.catalog, selectedPreset.models);
    const modelsToSave = presetModels.includes(model) ? presetModels : [...presetModels, model];
    await api.saveProvider({
      name: selectedPreset.name,
      type: selectedPreset.type,
      api_key: formProvider.api_key,
      base_url: formProvider.base_url,
      models: modelsToSave
    });
    await api.setRoleModel({ role: formProvider.role, provider: selectedPreset.name, model, thinking: formProvider.thinking });
    await refreshTabData();
  }

  async function deleteProvider(name: string) {
    if (!window.confirm(`Xóa nhà cung cấp ${name}? Các phân vai đang dùng provider này có thể không chạy được.`)) return;
    await api.deleteProvider(name);
    await refreshTabData();
  }

  async function deleteRoleModel(role: string) {
    const label = ROLES.find(r => r.value === role)?.label ?? role;
    if (!window.confirm(`Xóa cấu hình mô hình cho vai trò ${label}?`)) return;
    await api.deleteRoleModel(role);
    await refreshTabData();
  }

  async function chooseImport() {
    const path = await window.storeOpen?.pickFile();
    if (path) await api.importNovel(projectId, { path, from_chapter: importFrom, style: activeProject?.style ?? style });
    await refreshSnapshot();
  }

  async function chooseSimFolder() {
    const path = await window.storeOpen?.pickFolder();
    if (path) await api.simulate(projectId, { path, style: activeProject?.style ?? style });
  }

  async function chooseSimFile() {
    const path = await window.storeOpen?.pickFile();
    if (path) await api.importSimulation(projectId, { path });
  }

  async function exportBook(format: "txt" | "epub" | "docx" | "pdf") {
    const defaultName = `${activeProject?.name ?? "novel"}.${format}`;
    const path = await window.storeOpen?.saveFile(defaultName, format);
    if (!path) return;
    try {
      const result = await api.exportProject(projectId, { path, format, overwrite: true });
      await window.storeOpen?.notify(APP_NAME, `Đã xuất ${result.chapters} chương → ${result.path}`);
    } catch (err) {
      setActionError(String((err as Error).message || err));
    }
  }

  async function exportDiag() {
    const text = await api.exportDiagnostics(projectId);
    const path = await window.storeOpen?.saveFile("diagnostics.md", "md");
    if (path) {
      await fetch(`file://${path}`, { method: "PUT", body: text }).catch(() => null);
    }
    const blob = new Blob([text], { type: "text/markdown" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = "diagnostics.md";
    a.click();
  }

  function selectChapter(c: any) {
    setSelectedChapter(c);
    setChapterEdit(c.final_text || c.draft_text || "");
  }

  return (
    <div className="app">
      <aside className="sidebar">
        <div className="brand"><span className="brandIcon">SC</span><span><b>{APP_NAME}</b><small>XƯỞNG VIẾT TIỂU THUYẾT AI CỤC BỘ</small></span></div>
        <div className="projectNameBox">
          <label className="projectNameLabel" htmlFor="project-name">Tên tác phẩm</label>
          <div className="projectNameRow">
            <input
              id="project-name"
              className="projectNameInput"
              type="text"
              placeholder="Nhập tên truyện..."
              value={projectName}
              onChange={e => setProjectName(e.target.value)}
              onKeyDown={e => { if (e.key === "Enter" && !projectId) createProject(); if (e.key === "Enter" && projectId) saveProjectName(); }}
            />
            <button className="addVoice" type="button" title="Tạo tác phẩm mới" onClick={createProject}>+</button>
          </div>
        </div>

        <div className="sidebarLibrary">
        <div className="libraryTitle">THƯ VIỆN TÁC PHẨM</div>
        <div className="projectList">
          {projects.length === 0 ? (
            <p className="projectEmpty">Chưa có tác phẩm. Nhập tên ở trên rồi nhấn <b>+</b>.</p>
          ) : projects.map(p => (
            <div key={p.id} className={p.id === projectId ? "projectItem active" : "projectItem"}
              onClick={() => setProjectId(p.id)}>
              <div className="projectItemInfo">
                <strong>{p.name || "Tác phẩm mới"}</strong>
                <span>{styleLabel(p.style ?? "default", styleCatalog)} · {phaseLabel(p.phase ?? p.progress?.phase)} · {p.current_chapter ?? p.progress?.current_chapter ?? 0}/{p.total_chapters ?? p.progress?.total_chapters ?? 0}</span>
              </div>
              <div className="projectItemActions" onClick={e => e.stopPropagation()}>
                <button className="projectActionBtn renameBtn" title="Đổi tên" onClick={() => {
                  setRenameProjectId(p.id);
                  setRenameValue(p.name || "");
                }}>✏️</button>
                <button className="projectActionBtn deleteBtn" title="Xóa" onClick={async () => {
                  if (!window.confirm(`Xóa "${p.name || 'Tác phẩm này'}"?`)) return;
                  await api.deleteProject(p.id);
                  if (p.id === projectId) setProjectId("");
                  await refreshProjects();
                }}>🗑</button>
              </div>
            </div>
          ))}
        </div>
        </div>
      </aside>
      <main>
        {!backendOnline ? (
          <div className="backendBanner">
            Backend chưa sẵn sàng. Hãy chờ vài giây hoặc chạy <code>npm run dev</code> trong thư mục Story Clone; Electron sẽ tự thử khởi động backend khi mở app.
          </div>
        ) : null}
        <header className="topbar">
          <div><p className="eyebrow">SÁNG TẠO BẰNG AI CỦA BẠN</p><h1>Biến ý tưởng thành <span>tiểu thuyết của bạn.</span></h1><p>{phaseLabel(progress.phase)} · {flowLabel(progress.flow)} · {percent}% hoàn thành</p></div>
          <nav>{(["dashboard", "chapters", "outline", "reviews", "artist", "diagnostics", "tools", "settings", "guide"] as Tab[]).map(t => <button key={t} className={tab === t ? "active" : ""} onClick={() => setTab(t)}>{label(t)}</button>)}</nav>
        </header>
        {tab === "dashboard" && <section className="grid dashboard">
          <div className="panel wide">
            <h2>Khởi động</h2>
            <div className="startupBlock">
              <div className="startupModes">
                <button type="button" className={startMode === "quick" ? "active" : ""} onClick={() => setStartMode("quick")}>Nhanh</button>
                <button type="button" className={startMode === "cocreate" ? "active" : ""} onClick={() => setStartMode("cocreate")}>Đồng sáng tác</button>
              </div>
              <div className="startupFields">
                <StylePicker styles={styleCatalog} value={style} onChange={changeStyle} compact showCallout={false} />
                <ArtistStylePicker styles={artistStyleCatalog} value={artistStyle} onChange={changeArtistStyle} compact showCallout={false} />
              </div>
              <div className="startupMeta">
                <div className="startupMetaItem">
                  <span className="startupMetaLabel">Thể loại</span>
                  <p className="startupMetaText">
                    <strong>{selectedStyle.label}</strong>
                    {selectedStyle.has_genre_refs ? <span className="startupMetaBadge">Tài liệu thể loại</span> : null}
                    {" — "}{styleDescriptionBody(selectedStyle)}
                  </p>
                </div>
                <div className="startupMetaItem">
                  <span className="startupMetaLabel">Hoạ sĩ AI</span>
                  <p className="startupMetaText">
                    <strong>{selectedArtistStyle.label}</strong>
                    {" — "}{selectedArtistStyle.description}
                  </p>
                </div>
              </div>
            </div>
            {startMode === "quick" ? (
              <div className="storyBriefBox">
                <label className="fieldLabel storyBriefLabel" htmlFor="story-brief">
                  {isPodcastStyle ? "Mô tả chủ đề podcast" : "Mô tả cốt truyện"}
                </label>
                <textarea
                  id="story-brief"
                  className="storyBrief"
                  value={prompt}
                  onChange={e => setPrompt(e.target.value)}
                  placeholder={isPodcastStyle
                    ? "Mô tả chủ đề, phạm vi thời gian/địa lý, đối tượng người nghe và tone giọng host..."
                    : "Mô tả bối cảnh, nhân vật, xung đột, tone và hướng đi của truyện..."}
                />
              </div>
            ) : (
              <div className="cocreateBox">
                <div className="cocreateLog">{cocreateMessages.map((m, i) => <div key={i} className={`cocreateMsg ${m.role}`}><b>{m.role === "user" ? "Bạn" : "AI"}</b><p>{m.content}</p></div>)}</div>
                <textarea value={cocreateInput} onChange={e => setCocreateInput(e.target.value)} placeholder="Trao đổi với AI để hoàn thiện brief..." />
                <button onClick={sendCocreate}>Gửi</button>
              </div>
            )}
            <div className="startupFooter">
              <label className="chapterTarget">
                <span>{isPodcastStyle ? "Số tập" : "Số chương"}</span>
                <input
                  className="chapterTargetInput"
                  type="number"
                  min={1}
                  max={1000}
                  value={chaptersTarget}
                  onChange={e => setChaptersTarget(Number(e.target.value))}
                />
              </label>
              <div className="startupActions">
                <button className="primary" disabled={!projectId || starting} onClick={start}>
                  {starting ? "Đang khởi động…" : isPodcastStyle ? "Tạo podcast" : "Tạo tiểu thuyết"}
                </button>
                <button disabled={!projectId || starting} onClick={() => api.resume(projectId).then(refreshSnapshot).catch(err => setActionError(String(err.message || err)))}>Tiếp tục</button>
                <button disabled={!projectId} onClick={() => api.abort(projectId).then(refreshSnapshot).catch(err => setActionError(String(err.message || err)))}>Dừng</button>
              </div>
            </div>
            {actionError ? <p className="actionError">{actionError}</p> : null}
            {snapshot?.running ? <p className="hint runningHint">Pipeline đang chạy — theo dõi khung Gần đây bên dưới.</p> : null}
            <div className="progress"><span style={{ width: `${percent}%` }} /></div>
            {pendingRewrites.length > 0 && <p className="hint">Hàng đợi viết lại: {pendingRewrites.join(", ")}</p>}
          </div>
          <div className="panel"><h2>Tinh chỉnh</h2><textarea value={steer} onChange={e => setSteer(e.target.value)} placeholder="Can thiệp hướng truyện, yêu cầu viết lại chương..." /><div className="row"><button onClick={() => api.steer(projectId, steer).then(refreshSnapshot)}>Gửi can thiệp</button><button onClick={() => api.cont(projectId, steer).then(refreshSnapshot)}>Tiếp tục</button></div></div>
          <div className="panel"><h2>Token / Chi phí</h2><div className="miniList">{usage.length === 0 ? <p>Chưa có dữ liệu usage.</p> : usage.map((u, i) => <div key={i} className="miniCard"><b>{u.provider} / {u.model}</b><span>In: {u.input_tokens} · Out: {u.output_tokens}</span></div>)}</div></div>
          <div className="panel full"><h2>Gần đây</h2><div className="events">{events.slice().reverse().map((e, i) => <div key={i} className={`event ${e.level}`}><b>{eventLabel(e.category)}</b><span>{e.summary}</span><small>{e.created_at}</small></div>)}</div></div>
        </section>}
        {tab === "chapters" && <section className="split"><div className="panel list">{chapters.map((c: any) => <button key={c.chapter_no} onClick={() => selectChapter(c)} className={selectedChapter?.chapter_no === c.chapter_no ? "active item" : "item"}><b>{c.chapter_no}. {c.title}</b><span>{c.status === "committed" ? "Đã hoàn tất" : c.status === "draft" ? "Bản nháp" : "Đã lập kế hoạch"} · {c.word_count} từ</span></button>)}</div><div className="panel reader"><h2>{selectedChapter?.title ?? "Chọn chương"}</h2><textarea className="chapterEditor" value={chapterEdit} onChange={e => setChapterEdit(e.target.value)} /><div className="row"><button onClick={saveChapterEdit} disabled={!selectedChapter}>Lưu chỉnh sửa</button><button onClick={reopenChapter} disabled={!selectedChapter}>Yêu cầu viết lại</button></div></div></section>}
        {tab === "outline" && <section className="grid"><DataPanel title="Mô tả cốt truyện (brief)" data={snapshot?.story_brief || prompt} /><DataPanel title="Tiền đề (AI sinh)" data={generatedPremise || snapshot?.premise} /><DataPanel title="Dàn ý" data={outline?.content} /><DataPanel title="Nhân vật" data={characters?.content} /><DataPanel title="Thế giới" data={world?.content} /></section>}
        {tab === "reviews" && <section className="grid">{reviews.map((r, i) => <DataPanel key={i} title={`Đánh giá ${r.chapter_no ? `chương ${r.chapter_no}` : "toàn cục"}`} data={r.payload_json} />)}</section>}
        {tab === "artist" && <ArtistPromptPanel items={artistPrompts} projectId={projectId} events={events} regenerating={artistRegenerating} setRegenerating={setArtistRegenerating} regenRunId={artistRegenRunId} setRegenRunId={setArtistRegenRunId} regenError={artistRegenError} setRegenError={setArtistRegenError} onRefresh={refreshArtistPrompts} artistStyle={artistStyle} artistStyleCatalog={artistStyleCatalog} />}
        {tab === "diagnostics" && <section className="grid"><DataPanel title="Thống kê" data={diag?.stats} /><DataPanel title="Phát hiện" data={diag?.findings} /><div className="panel"><h2>Hành động</h2><pre>{pretty(diag?.actions)}</pre><button onClick={exportDiag}>Xuất báo cáo MD</button></div></section>}
        {tab === "tools" && <section className="grid"><ToolPanel title="Nhập tiểu thuyết" text="Nhập file .txt/.md, LLM suy luận nền truyện và phân tích từng chương." action={chooseImport} /><div className="panel"><h2>Từ chương</h2><input type="number" min={1} value={importFrom} onChange={e => setImportFrom(Number(e.target.value))} /></div><ToolPanel title="Hồ sơ mô phỏng" text="Quét thư mục ngữ liệu, LLM phân tích và hợp nhất profile." action={chooseSimFolder} /><ToolPanel title="Nhập hồ sơ mô phỏng" text="Nhập profile JSON đã có." action={chooseSimFile} /><ToolPanel title="Xuất TXT" text="Chọn đường dẫn và xuất TXT." action={() => exportBook("txt")} /><ToolPanel title="Xuất EPUB" text="Chọn đường dẫn và xuất EPUB." action={() => exportBook("epub")} /><ToolPanel title="Xuất Word (.docx)" text="Chọn đường dẫn và xuất Word (.docx)." action={() => exportBook("docx")} /><ToolPanel title="Xuất PDF" text="Chọn đường dẫn và xuất PDF." action={() => exportBook("pdf")} /></section>}
        {tab === "settings" && <section className="grid settings"><div className="panel"><h2>Thiết lập mô hình</h2><label className="fieldLabel">Nhà cung cấp</label><select value={formProvider.provider} onChange={e => chooseProvider(e.target.value)}>{PROVIDERS.map(p => <option key={p.name} value={p.name}>{p.label}</option>)}</select><label className="fieldLabel">Vai trò áp dụng</label><select value={formProvider.role} onChange={e => setFormProvider({ ...formProvider, role: e.target.value })}>{ROLES.map(role => <option key={role.value} value={role.value}>{role.label}</option>)}</select><label className="fieldLabel">Mô hình có sẵn</label><select value={formProvider.model} onChange={e => setFormProvider({ ...formProvider, model: e.target.value, customModel: "" })}>{availableModels.map(model => <option key={model} value={model}>{model}</option>)}</select><label className="fieldLabel">Hoặc nhập tên mô hình tùy chỉnh</label><input placeholder="vd. deepseek-v3.2 / google/gemini-3.1-pro" value={formProvider.customModel} onChange={e => setFormProvider({ ...formProvider, customModel: e.target.value })} /><label className="fieldLabel">Mức suy luận</label><select value={formProvider.thinking} onChange={e => setFormProvider({ ...formProvider, thinking: e.target.value })}>{THINKING_LEVELS.map(level => <option key={level.value} value={level.value}>{level.label}</option>)}</select><label className="fieldLabel">Địa chỉ API</label><input placeholder="Nhập địa chỉ API" value={formProvider.base_url} onChange={e => setFormProvider({ ...formProvider, base_url: e.target.value })} /><label className="fieldLabel">Khóa API</label><input placeholder="Nhập khóa API" type="password" value={formProvider.api_key} onChange={e => setFormProvider({ ...formProvider, api_key: e.target.value })} /><button className="primary" onClick={saveProvider}>Lưu thiết lập mô hình</button></div><ProviderList items={providers} onDelete={deleteProvider} /><RoleModelList items={models.roles} onDelete={deleteRoleModel} /></section>}
        {tab === "guide" && <section className="guidePage">
          <div className="guideHero panel guideFull">
            <p className="eyebrow">HƯỚNG DẪN SỬ DỤNG</p>
            <h2>Vận hành {APP_NAME} từ A đến Z</h2>
            <p>
              {APP_NAME} là xưởng viết tiểu thuyết AI chạy cục bộ (Electron + FastAPI + SQLite).
              Dữ liệu nằm trên máy bạn. Ứng dụng mô phỏng pipeline đa-agent của ainovel-cli gốc:
              Kiến trúc → Người viết → Biên tập → Commit từng chương, kèm can thiệp realtime và hàng đợi viết lại.
            </p>
          </div>

          <div className="panel guideQuick guideFull">
            <h2>Bắt đầu nhanh — 5 bước</h2>
            <ol className="guideSteps guideStepsCompact">
              {GUIDE_QUICK_START.map((step, i) => <li key={i}><span className="guideStepNo">{i + 1}</span>{step}</li>)}
            </ol>
          </div>

          <div className="panel guideWorkflow guideFull">
            <h2>Luồng làm việc đề xuất</h2>
            <div className="guideFlowChart">
              <span>Tạo tác phẩm</span>
              <span className="guideFlowArrow">→</span>
              <span>Cấu hình AI</span>
              <span className="guideFlowArrow">→</span>
              <span>Viết / Import</span>
              <span className="guideFlowArrow">→</span>
              <span>Theo dõi &amp; Can thiệp</span>
              <span className="guideFlowArrow">→</span>
              <span>Xuất bản</span>
            </div>
            <p className="guideIntro">
              Có thể <b>nhập truyện có sẵn</b> thay vì viết mới; có thể bật <b>hồ sơ mô phỏng văn phong</b> trước khi viết.
              Mỗi tab trên thanh điều hướng phục vụ một giai đoạn — xem chi tiết bên dưới.
            </p>
          </div>

          <h3 className="guideSectionHead">Chi tiết từng phần</h3>
          <div className="guideGrid">
            {GUIDE_BLOCKS.map(block => <GuideCard key={block.title} block={block} />)}
          </div>

          <div className="guideBottomRow">
            <GuideCard block={GUIDE_FAQ} />
            <div className="panel guideTabsRef">
              <h2>Ánh xạ tab điều hướng</h2>
              <ul className="guideTabMap">
                <li><b>Vận hành</b> — khởi động, can thiệp, theo dõi event và token</li>
                <li><b>Chương</b> — đọc, sửa, yêu cầu viết lại từng chương</li>
                <li><b>Cốt truyện</b> — dàn ý, nhân vật, thế giới</li>
                <li><b>Đánh giá</b> — kết quả biên tập AI</li>
                <li><b>Chẩn đoán</b> — sức khỏe dự án, xuất báo cáo</li>
                <li><b>Công cụ</b> — import, mô phỏng văn phong, xuất TXT/EPUB</li>
                <li><b>Mô hình</b> — provider, API key, phân vai LLM</li>
                <li><b>Hướng dẫn</b> — tài liệu này</li>
              </ul>
            </div>
          </div>
        </section>}      </main>
      {/* Rename Modal — rendered at app root so it overlays the full window */}
      {renameProjectId && (
        <div className="renameOverlay" onClick={() => setRenameProjectId(null)}>
          <div className="renameModal" onClick={e => e.stopPropagation()}>
            <h3 className="renameModalTitle">Đổi tên tác phẩm</h3>
            <input
              className="renameModalInput"
              type="text"
              value={renameValue}
              autoFocus
              onChange={e => setRenameValue(e.target.value)}
              onKeyDown={async e => {
                if (e.key === "Enter") {
                  const name = renameValue.trim() || "Tác phẩm mới";
                  await api.patchProject(renameProjectId, { name });
                  if (renameProjectId === projectId) setProjectName(name);
                  await refreshProjects(projectId);
                  setRenameProjectId(null);
                }
                if (e.key === "Escape") setRenameProjectId(null);
              }}
            />
            <div className="renameModalActions">
              <button className="renameModalCancel" onClick={() => setRenameProjectId(null)}>Hủy</button>
              <button className="renameModalConfirm primary" onClick={async () => {
                const name = renameValue.trim() || "Tác phẩm mới";
                await api.patchProject(renameProjectId, { name });
                if (renameProjectId === projectId) setProjectName(name);
                await refreshProjects(projectId);
                setRenameProjectId(null);
              }}>Lưu tên</button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<App />);






