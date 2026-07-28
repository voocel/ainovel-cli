import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { CoverInfo, CoverTitleLayout, ImageGenSettings } from "../bindings/wails";

// CoverPanel 封面：用生图模型生成，或从本地导入。
//
// 生图与创作引擎完全解耦（引擎的模型层是纯文本的），所以生图服务要单独配置一次。
// 未配置时面板直接引导去填，而不是等到点生成才报错。
//
// 书名不交给生图模型画，而是本地排版叠上去（中文字形在生图模型里几乎必糊）。
// 所以面板分两段：上半段出画面（要花钱），下半段排文字（免费，随便调）。
export function CoverPanel({ onClose, onChanged }: { onClose: () => void; onChanged: () => void }) {
  const [cover, setCover] = useState<CoverInfo | null>(null);
  const [settings, setSettings] = useState<ImageGenSettings | null>(null);
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState<"" | "gen" | "prompt" | "optimize" | "import" | "save" | "apply">("");
  // 生图进度：后端每秒播报已等待秒数。生图可能要几分钟，只给转圈用户会以为卡死。
  const [elapsed, setElapsed] = useState(0);
  const [budget, setBudget] = useState(0);
  const [err, setErr] = useState<string | null>(null);
  const [editingCfg, setEditingCfg] = useState(false);
  const [saved, setSaved] = useState(false);
  // 记录校验未通过的字段，用于高亮到底缺哪一栏
  const [invalid, setInvalid] = useState<string[]>([]);

  // 生图配置草稿
  const [baseURL, setBaseURL] = useState("");
  const [model, setModel] = useState("");
  const [size, setSize] = useState("1024x1536");
  const [apiKey, setApiKey] = useState("");

  // 叠字排版草稿 + 预览
  const [layout, setLayout] = useState<CoverTitleLayout | null>(null);
  const [preview, setPreview] = useState("");
  const [layoutDirty, setLayoutDirty] = useState(false);
  const [titleErr, setTitleErr] = useState<string | null>(null);

  const load = async (first = false) => {
    try {
      const [c, s] = await Promise.all([api.GetCover(), api.GetImageGenSettings()]);
      setCover(c);
      setSettings(s);
      setBaseURL(s.baseURL);
      setModel(s.model);
      setSize(s.size || "1024x1536");
      setLayout(c.layout);
      setLayoutDirty(false);
      setPreview("");
      if (c.prompt && !c.prompt.startsWith("（本地导入")) setPrompt(c.prompt);
      // 只在首次进入且未配置时自动展开配置区。不能每次 load 都展开——
      // 保存后也会 load，那样会把刚收起的配置区又弹回来，像是"保存没生效"。
      if (first && (!s.baseURL || !s.model)) setEditingCfg(true);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  useEffect(() => {
    void load(true);
  }, []);

  useEffect(() => {
    const off = api.on("cover:progress", (d: api.CoverProgress) => {
      setElapsed(d.elapsedSec);
      setBudget(d.budgetSec);
    });
    return off;
  }, []);

  // 排版预览：改一次参数就要重新栅格化一张图，不能每个按键都发。
  // 250ms 防抖 + 请求序号丢弃过期结果（后端渲染耗时不定，晚回的旧请求会盖掉新的）。
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
          if (seq === previewSeq.current) setTitleErr(String((e as Error)?.message ?? e));
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
      setPrompt(await api.SuggestCoverPrompt());
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  // optimize 让文本模型按本书设定重写提示词。失败不静默降级——草稿按钮就在旁边，
  // 用户自己决定是重试还是用草稿。
  const optimize = async () => {
    setBusy("optimize");
    setErr(null);
    try {
      setPrompt(await api.OptimizeCoverPrompt(prompt));
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
      const c = await api.GenerateCover(prompt);
      setCover(c);
      setLayout(c.layout);
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
      const c = await api.ApplyCoverTitle(layout);
      setCover(c);
      setLayout(c.layout);
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
      const c = await api.ImportCoverFile();
      setCover(c);
      setLayout(c.layout);
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
      const c = await api.GetCover();
      setCover(c);
      setLayout(c.layout);
      setPreview("");
      setLayoutDirty(false);
      onChanged();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const saveCfg = async () => {
    // 必填项当面校验：以前缺项时后端照存（空串），load() 又把配置区弹回来，
    // 看起来就像"保存没反应"。这里明确指出缺什么。
    const missing: string[] = [];
    const bad: string[] = [];
    if (!baseURL.trim()) {
      missing.push("Base URL");
      bad.push("baseURL");
    }
    if (!model.trim()) {
      missing.push("模型");
      bad.push("model");
    }
    setInvalid(bad);
    if (missing.length > 0) {
      setErr("请先填写：" + missing.join("、"));
      return;
    }

    setBusy("save");
    setErr(null);
    try {
      await api.SaveImageGenSettings({
        baseURL,
        model,
        size,
        apiKeyAction: apiKey ? "replace" : "keep",
        apiKey,
      });
      setApiKey("");
      await load();
      // 收起配置区必须在 load 之后：load 会按"是否已配置"决定展开与否，
      // 顺序颠倒会被它覆盖掉。
      setEditingCfg(false);
      setSaved(true);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy("");
    }
  };

  // 预览优先显示未落盘的排版结果，让"调了但还没应用"看得见。
  const shownCover = preview || cover?.dataURL;

  return (
    <div className="modal-overlay">
      <div className="modal wide">
        <h2>小说封面</h2>
        <p className="subtle sm">
          用生图模型按本书设定生成画面，书名由本地字体排上去。封面会显示在书库，并在导出 EPUB 时嵌入为电子书封面。
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
            {!configured && !editingCfg && (
              <div className="note accent">
                还没配置生图服务。
                <button className="link" onClick={() => setEditingCfg(true)}>
                  现在配置
                </button>
              </div>
            )}

            {editingCfg ? (
              <>
                <h3>生图服务</h3>
                <p className="subtle sm">
                  兼容 OpenAI 的 <code>/v1/images/generations</code> 接口
                  （官方 gpt-image-2，以及多数中转网关）；JarlessAPI
                  会自动使用异步任务并在完成后下载图片。
                </p>
                <label className="form-label">
                  Base URL<span className="req"> *必填</span>
                </label>
                <input
                  className={`text-input ${invalid.includes("baseURL") ? "invalid" : ""}`}
                  value={baseURL}
                  onChange={(e) => { setBaseURL(e.target.value); setSaved(false); setInvalid((v) => v.filter((x) => x !== "baseURL")); }}
                  placeholder="https://api.openai.com/v1"
                />
                <label className="form-label">
                  模型<span className="req"> *必填</span>
                </label>
                <input
                  className={`text-input ${invalid.includes("model") ? "invalid" : ""}`}
                  value={model}
                  onChange={(e) => { setModel(e.target.value); setSaved(false); setInvalid((v) => v.filter((x) => x !== "model")); }}
                  placeholder="官方 gpt-image-2"
                />
                <label className="form-label">
                  API Key
                  {settings?.hasAPIKey && (
                    <span className="subtle sm"> · 当前 {settings.apiKeyHint}，留空则不改</span>
                  )}
                </label>
                <input
                  className="text-input"
                  type="password"
                  value={apiKey}
                  onChange={(e) => setApiKey(e.target.value)}
                  placeholder="sk-…"
                />
                <label className="form-label">尺寸</label>
                <div className="preset-grid">
                  {["1024x1536", "1024x1024", "1536x1024"].map((s) => (
                    <button
                      key={s}
                      className={`preset ${size === s ? "active" : ""}`}
                      onClick={() => setSize(s)}
                    >
                      {s === "1024x1536" ? `${s} 竖版` : s}
                    </button>
                  ))}
                </div>
                {/* 提示必须紧贴按钮：放在模态框底部会被长表单挤出可视区，
                    用户点了保存却看不到任何反馈，误以为"保存无效"。 */}
                {err && <div className="error-banner">{err}</div>}
                <div className="inline-actions" style={{ marginTop: 12 }}>
                  {configured && (
                    <button className="ghost" onClick={() => setEditingCfg(false)}>
                      取消
                    </button>
                  )}
                  <button className="primary" onClick={saveCfg} disabled={busy === "save"}>
                    {busy === "save" ? "保存中…" : "保存配置"}
                  </button>
                </div>
              </>
            ) : (
              <>
                <div className="cover-cfg-line subtle sm">
                  {settings?.model} · {settings?.size || "1024x1536"}
                  <button className="link" onClick={() => setEditingCfg(true)}>
                    修改
                  </button>
                </div>

                <label className="form-label">封面提示词</label>
                <textarea
                  className="prompt-input compact"
                  rows={6}
                  value={prompt}
                  onChange={(e) => setPrompt(e.target.value)}
                  placeholder="描述你想要的封面画面（英文效果更好）"
                  disabled={!!busy}
                />
                <div className="inline-actions" style={{ marginTop: 8 }}>
                  <button onClick={suggest} disabled={!!busy} title="按本书设定即时拼一段提示词，不调用模型">
                    {busy === "prompt" ? "生成中…" : "按设定草稿"}
                  </button>
                  <button onClick={optimize} disabled={!!busy} title="让文本模型按本书设定重写提示词（会消耗少量 token）">
                    {busy === "optimize" ? "润色中…" : "AI 润色提示词"}
                  </button>
                </div>
                <div className="inline-actions" style={{ marginTop: 8 }}>
                  <button
                    className="primary"
                    onClick={generate}
                    disabled={!!busy || !prompt.trim() || !configured}
                  >
                    {busy === "gen" ? "绘制中…" : cover?.exists ? "重新生成画面" : "生成封面"}
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
                      已等待 {elapsed}s
                      {/* 说清楚慢是服务端与带宽决定的，不是软件卡住了 */}
                      {" · 生图取决于服务商，通常 1-3 分钟，慢时可达 15 分钟"}
                      <button className="link" onClick={() => api.CancelCover()}>
                        取消
                      </button>
                    </p>
                  </div>
                )}

                {cover?.exists && layout && (
                  <div className="cover-title-block">
                    <div className="cover-title-head">
                      <h3>封面书名</h3>
                      <label className="check sm">
                        <input
                          type="checkbox"
                          checked={layout.enabled}
                          onChange={(e) => patchLayout({ enabled: e.target.checked })}
                        />
                        显示书名
                      </label>
                    </div>
                    {/* 说明为什么书名是本地排的：用户第一反应会是"为什么不让 AI 画上去" */}
                    <p className="subtle sm">
                      书名用系统字体排在画面上，改这里不用重新生图，也不会出现糊掉的中文。
                    </p>

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
                        ] as const).map(([v, text]) => (
                          <button
                            key={v}
                            className={`preset ${layout.position === v ? "active" : ""}`}
                            onClick={() => patchLayout({ position: v })}
                            disabled={!layout.enabled}
                          >
                            {text}
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
                        ] as const).map(([v, text]) => (
                          <button
                            key={v}
                            className={`preset ${layout.font === v ? "active" : ""}`}
                            onClick={() => patchLayout({ font: v })}
                            disabled={!layout.enabled}
                          >
                            {text}
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
                        ] as const).map(([v, text]) => (
                          <button
                            key={v}
                            className={`preset ${layout.theme === v ? "active" : ""}`}
                            onClick={() => patchLayout({ theme: v })}
                            disabled={!layout.enabled}
                          >
                            {text}
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
                    <div className="inline-actions" style={{ marginTop: 10 }}>
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
              </>
            )}
          </div>
        </div>

        {err && !editingCfg && <div className="error-banner">{err}</div>}
        {saved && !err && <div className="ok-banner">生图配置已保存</div>}

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
    </div>
  );
}
