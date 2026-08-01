import { useEffect, useState } from "react";
import * as api from "../bindings/wails";
import type { FoundationView } from "../bindings/wails";
import { Overlay } from "./Overlay";

// FoundationPanel 设定审阅：规划完成后、开始写正文前，完整展示前提 / 大纲 / 人物 /
// 世界观，让用户确认或提修改意见。
//
// 修改走引擎既有的干预通道（Arbiter 裁定 → architect 改），不直接改文件——
// 大纲与进度、伏笔台账有关联，绕过引擎手改会破坏事实层一致性。
export function FoundationPanel({
  onClose,
  onApprove,
}: {
  onClose: () => void;
  onApprove: () => void;
}) {
  const [data, setData] = useState<FoundationView | null>(null);
  const [tab, setTab] = useState<"outline" | "cast" | "world">("outline");
  const [revision, setRevision] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [sent, setSent] = useState(false);

  useEffect(() => {
    api
      .GetFoundation()
      .then(setData)
      .catch((e) => setErr(String(e?.message ?? e)));
  }, []);

  const submitRevision = async () => {
    const text = revision.trim();
    if (!text || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.ReviseFoundation(text);
      setSent(true);
      setRevision("");
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  const approve = async () => {
    if (busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.AdvanceOneChapter();
      onApprove();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  if (!data) {
    return (
      <Overlay layer="sheet" onClose={onClose} labelledBy="found-title">
        <div className="modal wide">
          <h2 id="found-title">设定</h2>
          {err ? <div className="error-banner">{err}</div> : <p className="subtle">读取中…</p>}
          <div className="modal-actions">
            <button className="ghost" onClick={onClose}>
              关闭
            </button>
          </div>
        </div>
      </Overlay>
    );
  }

  const outline = data.outline ?? [];
  const chars = data.characters ?? [];
  const rules = data.worldRules ?? [];

  return (
    <Overlay layer="sheet" onClose={busy ? undefined : onClose} labelledBy="found-title">
      <div className="modal wide foundation">
        <h2 id="found-title">{data.awaitingReview ? "规划已就绪，请审阅设定" : "本书设定"}</h2>
        <p className="subtle sm">
          {data.awaitingReview
            ? `确认无误后放行第 ${data.nextChapter} 章开始写作；需要调整就在下面提出修改意见。`
            : "这是当前生效的设定。提出修改意见会交由 AI 评估并调整。"}
        </p>

        {data.premise && (
          <section className="found-block">
            <h3 className="section-label">前提</h3>
            <div className="found-prose">{data.premise}</div>
          </section>
        )}

        {data.compass && (
          <section className="found-block">
            <h3 className="section-label">终局方向</h3>
            <div className="found-prose">
              {data.compass.endingDirection}
              {data.compass.estimatedScale && (
                <span className="subtle">（{data.compass.estimatedScale}）</span>
              )}
            </div>
          </section>
        )}

        <nav className="tabs inline found-tabs">
          <button
            className={`tab ${tab === "outline" ? "active" : ""}`}
            onClick={() => setTab("outline")}
          >
            大纲 {outline.length > 0 && `(${outline.length})`}
          </button>
          <button className={`tab ${tab === "cast" ? "active" : ""}`} onClick={() => setTab("cast")}>
            人物 {chars.length > 0 && `(${chars.length})`}
          </button>
          <button
            className={`tab ${tab === "world" ? "active" : ""}`}
            onClick={() => setTab("world")}
          >
            世界观 {rules.length > 0 && `(${rules.length})`}
          </button>
        </nav>

        <div className="found-scroll">
          {tab === "outline" &&
            (outline.length === 0 ? (
              <p className="subtle">大纲尚未生成。</p>
            ) : (
              outline.map((e) => (
                <div className="found-item" key={e.chapter}>
                  <div className="found-item-head">
                    <span className="found-num">第 {e.chapter} 章</span>
                    <strong>{e.title}</strong>
                  </div>
                  {e.coreEvent && <div className="found-prose">{e.coreEvent}</div>}
                  {e.hook && <div className="found-hook">钩子：{e.hook}</div>}
                </div>
              ))
            ))}

          {tab === "cast" &&
            (chars.length === 0 ? (
              <p className="subtle">人物尚未生成。</p>
            ) : (
              chars.map((c) => (
                <div className="found-item" key={c.name}>
                  <div className="found-item-head">
                    <strong>{c.name}</strong>
                    {c.role && <span className="tag">{c.role}</span>}
                    {c.tier && <span className="subtle sm">{c.tier}</span>}
                  </div>
                  {c.description && <div className="found-prose">{c.description}</div>}
                  {c.arc && <div className="found-hook">成长线：{c.arc}</div>}
                  {(c.traits ?? []).length > 0 && (
                    <div className="found-tags">
                      {(c.traits ?? []).map((t) => (
                        <span className="chip static" key={t}>
                          {t}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
              ))
            ))}

          {tab === "world" &&
            (rules.length === 0 ? (
              <p className="subtle">世界观规则尚未生成。</p>
            ) : (
              rules.map((r, i) => (
                <div className="found-item" key={i}>
                  <div className="found-item-head">
                    <span className="tag">{r.category}</span>
                  </div>
                  <div className="found-prose">{r.rule}</div>
                  {r.boundary && <div className="found-hook">边界：{r.boundary}</div>}
                </div>
              ))
            ))}
        </div>

        {sent && (
          <div className="ok-banner">
            修改意见已提交，AI 正在评估并调整设定。稍后回到本面板查看结果。
          </div>
        )}
        {err && <div className="error-banner">{err}</div>}

        <label className="form-label">修改意见</label>
        <div className="inline-actions">
          <input
            className="text-input"
            value={revision}
            onChange={(e) => setRevision(e.target.value)}
            placeholder="例如：主角改成女性 / 第 3 章节奏太慢 / 加一个反派"
            disabled={busy}
            onKeyDown={(e) => {
              if (e.key === "Enter") void submitRevision();
            }}
          />
          <button onClick={submitRevision} disabled={busy || !revision.trim()}>
            提交修改
          </button>
        </div>

        <div className="modal-actions">
          <button className="ghost" onClick={onClose} disabled={busy}>
            关闭
          </button>
          {data.awaitingReview && (
            <button className="primary" onClick={approve} disabled={busy}>
              {busy ? "处理中…" : `确认设定，开始写第 ${data.nextChapter} 章`}
            </button>
          )}
        </div>
      </div>
    </Overlay>
  );
}
