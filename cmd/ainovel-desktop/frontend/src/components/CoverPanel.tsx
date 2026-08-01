import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { CoverInfo, CoverTitleLayout, ImageGenSettings } from "../bindings/wails";
import { Overlay } from "./Overlay";

const COVER_PLATFORMS = [
  {
    value: "tomato",
    label: "番茄小说",
    description: "大主体、高对比、移动端醒目",
  },
  {
    value: "qidian",
    label: "起点中文网",
    description: "细腻写实、世界层次、成熟质感",
  },
  {
    value: "jinjiang",
    label: "晋江文学城",
    description: "柔和精致、关系优先、梦幻氛围",
  },
  {
    value: "zhihu",
    label: "知乎盐言",
    description: "克制冷调、象征意象、文学留白",
  },
  {
    value: "qimao",
    label: "七猫小说",
    description: "强烈色彩、华丽装备、视觉冲击",
  },
  {
    value: "ciweimao",
    label: "刺猬猫",
    description: "日系插画、清晰线稿、活泼角色",
  },
] as const;

const COVER_GENRES = [
  ["auto", "自动识别"],
  ["xianxia", "玄幻仙侠"],
  ["urban", "都市"],
  ["ancient_romance", "古言宫斗"],
  ["modern_romance", "现言甜宠"],
  ["suspense", "悬疑推理"],
  ["scifi", "科幻末世"],
  ["western_fantasy", "西方奇幻"],
  ["historical", "历史军事"],
  ["supernatural", "灵异恐怖"],
  ["light_novel", "轻小说"],
] as const;

const COVER_COMPOSITIONS = [
  ["auto", "自动"],
  ["portrait", "人物特写"],
  ["dynamic", "全身动态"],
  ["scene", "氛围场景"],
  ["duo", "双人关系"],
] as const;

const TITLE_STYLES = [
  ["auto", "自动"],
  ["gold", "鎏金"],
  ["modern", "现代"],
  ["romance", "言情"],
  ["thriller", "悬疑"],
  ["scifi", "科幻"],
  ["literary", "文学"],
] as const;

export function CoverPanel({
  onClose,
  onChanged,
  onOpenImageSettings,
}: {
  onClose: () => void;
  onChanged: () => void;
  onOpenImageSettings: () => void;
}) {
  const [cover, setCover] = useState<CoverInfo | null>(null);
  const [settings, setSettings] = useState<ImageGenSettings | null>(null);
  const [platform, setPlatform] = useState("tomato");
  const [genre, setGenre] = useState("auto");
  const [composition, setComposition] = useState("auto");
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState<"" | "gen" | "prompt" | "optimize" | "import" | "apply">("");
  const [elapsed, setElapsed] = useState(0);
  const [budget, setBudget] = useState(0);
  const [err, setErr] = useState<string | null>(null);

  const [layout, setLayout] = useState<CoverTitleLayout | null>(null);
  const [preview, setPreview] = useState("");
  const [layoutDirty, setLayoutDirty] = useState(false);
  const [titleErr, setTitleErr] = useState<string | null>(null);

  const load = async () => {
    try {
      const [nextCover, nextSettings] = await Promise.all([
        api.GetCover(),
        api.GetImageGenSettings(),
      ]);
      setCover(nextCover);
      setSettings(nextSettings);
      setPlatform(nextCover.platform || nextCover.preset || "tomato");
      setGenre(nextCover.genre || "auto");
      setComposition(nextCover.composition || "auto");
      setLayout(nextCover.layout);
      setLayoutDirty(false);
      setPreview("");
      if (nextCover.prompt && !nextCover.prompt.startsWith("（本地导入")) {
        setPrompt(nextCover.prompt);
      }
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  useEffect(() => {
    void load();
  }, []);

  useEffect(() => {
    const off = api.on("cover:progress", (d: api.CoverProgress) => {
      setElapsed(d.elapsedSec);
      setBudget(d.budgetSec);
    });
    return off;
  }, []);

  const previewSeq = useRef(0);
  useEffect(() => {
    if (!layout || !cover?.exists) return;
    const seq = ++previewSeq.current;
    const timer = window.setTimeout(() => {
      api
        .PreviewCoverTitle(layout)
        .then((url) => {
          if (seq === previewSeq.current) {
            setPreview(url);
            setTitleErr(null);
          }
        })
        .catch((e) => {
          if (seq === previewSeq.current) {
            setTitleErr(String((e as Error)?.message ?? e));
          }
        });
    }, 250);
    return () => window.clearTimeout(timer);
  }, [layout, cover?.exists]);

  const configured = !!settings?.baseURL && !!settings?.model;

  const patchLayout = (patch: Partial<CoverTitleLayout>) => {
    setLayout((cur) => (cur ? { ...cur, ...patch } : cur));
    setLayoutDirty(true);
  };

  const suggest = async () => {
    setBusy("prompt");
    setErr(null);
    try {
      setPrompt(await api.SuggestCoverPrompt(platform, genre, composition));
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  const optimize = async () => {
    setBusy("optimize");
    setErr(null);
    try {
      setPrompt(await api.OptimizeCoverPrompt(prompt, platform, genre, composition));
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  const generate = async () => {
    setBusy("gen");
    setErr(null);
    setElapsed(0);
    try {
      const next = await api.GenerateCover(prompt, platform, genre, composition);
      setCover(next);
      setPlatform(next.platform || platform);
      setGenre(next.genre || genre);
      setComposition(next.composition || composition);
      setLayout(next.layout);
      setLayoutDirty(false);
      setPreview("");
      onChanged();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  const applyTitle = async () => {
    if (!layout) return;
    setBusy("apply");
    setTitleErr(null);
    try {
      const next = await api.ApplyCoverTitle(layout);
      setCover(next);
      setLayout(next.layout);
      setLayoutDirty(false);
      onChanged();
    } catch (e) {
      setTitleErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  const importLocal = async () => {
    setBusy("import");
    setErr(null);
    try {
      const next = await api.ImportCoverFile();
      setCover(next);
      setPlatform(next.platform || platform);
      setGenre(next.genre || genre);
      setComposition(next.composition || composition);
      setLayout(next.layout);
      setLayoutDirty(false);
      setPreview("");
      onChanged();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  const remove = async () => {
    setErr(null);
    try {
      await api.RemoveCover();
      const next = await api.GetCover();
      setCover(next);
      setPlatform(next.platform || "tomato");
      setGenre(next.genre || "auto");
      setComposition(next.composition || "auto");
      setLayout(next.layout);
      setPreview("");
      setLayoutDirty(false);
      onChanged();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const shownCover = preview || cover?.dataURL;

  return (
    <Overlay
      layer="sheet"
      onClose={busy === "gen" ? undefined : onClose}
      labelledBy="cover-title"
    >
      <div className="modal wide">
        <h2 id="cover-title">小说封面</h2>
        <p className="subtle sm">
          书名由本地字体排版，封面会显示在书库并嵌入 EPUB。
        </p>

        <div className="cover-layout">
          <div className="cover-preview">
            {shownCover ? (
              <img src={shownCover} alt="封面" />
            ) : (
              <div className="cover-empty subtle">尚无封面</div>
            )}
            {preview && layoutDirty && <div className="cover-preview-tag">预览（未应用）</div>}
          </div>

          <div className="cover-side">
            <div className="cover-cfg-line subtle sm">
              <span>
                {configured
                  ? `${settings?.model} · ${settings?.size || "1024x1536"}${settings?.hasAPIKey ? " · Key 已配置" : ""}`
                  : "图片生成服务未配置"}
              </span>
              <button className="link" onClick={onOpenImageSettings} disabled={!!busy}>
                {configured ? "设置" : "前往设置"}
              </button>
            </div>

            {!configured && (
              <div className="note accent">
                请先在“设置 &gt; 图片生成”中填写 Base URL、模型和 API Key。
              </div>
            )}

            <label className="form-label">发布平台</label>
            <div className="cover-style-grid">
              {COVER_PLATFORMS.map((item) => (
                <button
                  key={item.value}
                  className={`cover-style-option ${platform === item.value ? "active" : ""}`}
                  onClick={() => setPlatform(item.value)}
                  disabled={!!busy}
                  aria-pressed={platform === item.value}
                >
                  <strong>{item.label}</strong>
                  <span>{item.description}</span>
                </button>
              ))}
            </div>

            <div className="cover-design-controls">
              <label>
                <span className="form-label">小说题材</span>
                <select
                  className="text-input"
                  value={genre}
                  onChange={(e) => setGenre(e.target.value)}
                  disabled={!!busy}
                >
                  {COVER_GENRES.map(([value, label]) => (
                    <option key={value} value={value}>{label}</option>
                  ))}
                </select>
              </label>
              <div>
                <span className="form-label">画面构图</span>
                <div className="preset-grid tight cover-composition-options">
                  {COVER_COMPOSITIONS.map(([value, label]) => (
                    <button
                      key={value}
                      className={`preset ${composition === value ? "active" : ""}`}
                      onClick={() => setComposition(value)}
                      disabled={!!busy}
                    >
                      {label}
                    </button>
                  ))}
                </div>
              </div>
            </div>
            {genre === "auto" && cover?.resolvedGenre && (
              <p className="subtle sm cover-resolved-genre">
                当前识别：{COVER_GENRES.find(([value]) => value === cover.resolvedGenre)?.[1] || cover.resolvedGenre}
              </p>
            )}

            <label className="form-label">画面描述</label>
            <textarea
              className="prompt-input compact"
              rows={6}
              value={prompt}
              onChange={(e) => setPrompt(e.target.value)}
              placeholder="描述主体人物、情绪、场景和关键冲突"
              disabled={!!busy}
            />
            <div className="inline-actions cover-prompt-actions">
              <button
                onClick={suggest}
                disabled={!!busy}
                title="按本书设定生成提示词草稿"
              >
                {busy === "prompt" ? "生成中…" : "按设定草稿"}
              </button>
              <button
                onClick={optimize}
                disabled={!!busy}
                title="使用文本模型润色提示词"
              >
                {busy === "optimize" ? "润色中…" : "AI 润色"}
              </button>
              <button
                className="primary"
                onClick={generate}
                disabled={!!busy || !prompt.trim() || !configured}
              >
                {busy === "gen" ? "绘制中…" : cover?.exists ? "重新生成" : "生成封面"}
              </button>
            </div>

            {busy === "gen" && (
              <div className="cover-progress">
                <div className="cover-progress-bar">
                  <div
                    className="cover-progress-fill"
                    style={{
                      width: budget > 0 ? `${Math.min(100, (elapsed / budget) * 100)}%` : "0%",
                    }}
                  />
                </div>
                <p className="subtle sm">
                  已等待 {elapsed}s · 通常 1-3 分钟，服务繁忙时可能更久
                  <button className="link" onClick={() => api.CancelCover()}>
                    取消
                  </button>
                </p>
              </div>
            )}

            {cover?.hasPlatformArtifact && platform === "tomato" && (
              <div className="ok-banner cover-artifact-ready">
                已生成番茄上传版：cover-fanqie.png（600×800）
              </div>
            )}

            {cover?.exists && layout && (
              <div className="cover-title-block">
                <div className="cover-title-head">
                  <h3 className="section-label">封面书名</h3>
                  <label className="check sm">
                    <input
                      type="checkbox"
                      checked={layout.enabled}
                      onChange={(e) => patchLayout({ enabled: e.target.checked })}
                    />
                    显示书名
                  </label>
                </div>
                <p className="subtle sm">本地排版不会消耗生图额度，也能避免中文变形。</p>

                <div className="cover-title-fields">
                  <input
                    className="text-input"
                    value={layout.title}
                    onChange={(e) => patchLayout({ title: e.target.value })}
                    placeholder="书名"
                    disabled={!layout.enabled}
                  />
                  <input
                    className="text-input"
                    value={layout.author}
                    onChange={(e) => patchLayout({ author: e.target.value })}
                    placeholder="作者（可留空）"
                    disabled={!layout.enabled}
                  />
                </div>

                <div className="cover-title-row">
                  <span className="form-label inline">位置</span>
                  <div className="preset-grid tight">
                    {([
                      ["top", "顶部"],
                      ["center", "居中"],
                      ["bottom", "底部"],
                    ] as const).map(([value, label]) => (
                      <button
                        key={value}
                        className={`preset ${layout.position === value ? "active" : ""}`}
                        onClick={() => patchLayout({ position: value })}
                        disabled={!layout.enabled}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="cover-title-row">
                  <span className="form-label inline">效果</span>
                  <div className="preset-grid tight cover-title-style-options">
                    {TITLE_STYLES.map(([value, label]) => (
                      <button
                        key={value}
                        className={`preset ${layout.style === value ? "active" : ""}`}
                        onClick={() => patchLayout({ style: value })}
                        disabled={!layout.enabled}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="cover-title-row">
                  <span className="form-label inline">字体</span>
                  <div className="preset-grid tight">
                    {([
                      ["hei", "黑体"],
                      ["song", "宋体"],
                      ["kai", "楷体"],
                    ] as const).map(([value, label]) => (
                      <button
                        key={value}
                        className={`preset ${layout.font === value ? "active" : ""}`}
                        onClick={() => patchLayout({ font: value })}
                        disabled={!layout.enabled}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="cover-title-row">
                  <span className="form-label inline">配色</span>
                  <div className="preset-grid tight">
                    {([
                      ["light", "浅色字"],
                      ["dark", "深色字"],
                    ] as const).map(([value, label]) => (
                      <button
                        key={value}
                        className={`preset ${layout.theme === value ? "active" : ""}`}
                        onClick={() => patchLayout({ theme: value })}
                        disabled={!layout.enabled}
                      >
                        {label}
                      </button>
                    ))}
                  </div>
                </div>

                <div className="cover-title-row">
                  <span className="form-label inline">字号</span>
                  <input
                    className="cover-slider"
                    type="range"
                    min={0.5}
                    max={2}
                    step={0.05}
                    value={layout.scale}
                    onChange={(e) => patchLayout({ scale: Number(e.target.value) })}
                    disabled={!layout.enabled}
                  />
                  <span className="subtle sm">{layout.scale.toFixed(2)}×</span>
                </div>

                {titleErr && <div className="error-banner">{titleErr}</div>}
                <div className="inline-actions cover-title-actions">
                  <button
                    className="primary"
                    onClick={applyTitle}
                    disabled={!!busy || !layoutDirty}
                  >
                    {busy === "apply" ? "应用中…" : layoutDirty ? "应用到封面" : "已应用"}
                  </button>
                </div>
              </div>
            )}
          </div>
        </div>

        {err && <div className="error-banner">{err}</div>}

        <div className="modal-actions">
          <button className="ghost" onClick={importLocal} disabled={!!busy}>
            {busy === "import" ? "导入中…" : "用本地图片…"}
          </button>
          {cover?.exists && (
            <button className="ghost" onClick={remove} disabled={!!busy}>
              删除封面
            </button>
          )}
          <button className="primary" onClick={onClose} disabled={busy === "gen"}>
            完成
          </button>
        </div>
      </div>
    </Overlay>
  );
}
