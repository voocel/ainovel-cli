import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { DeconstructOptions, JobEvent, RipDone } from "../bindings/wails";
import { Overlay } from "./Overlay";

// 阶段顺序用于步骤条展示。等待类阶段不占独立节点，作为所处节点的「暂停」状态。
const STAGES: { key: string; label: string }[] = [
  { key: "ingesting", label: "读取原文" },
  { key: "bounding", label: "识别章节" },
  { key: "previewing", label: "黄金三章" },
  { key: "summarizing", label: "逐章拆解" },
  { key: "aggregating", label: "剧情单元" },
  { key: "profiling", label: "角色设定" },
  { key: "reporting", label: "拆文报告" },
  { key: "styling", label: "文风" },
];

// 等待阶段 → 所归属的步骤节点
const WAIT_OWNER: Record<string, string> = {
  awaiting_form: "ingesting",
  awaiting_preview: "previewing",
};

function stageIndex(stage: string): number {
  const owner = WAIT_OWNER[stage] ?? stage;
  return STAGES.findIndex((s) => s.key === owner);
}

// RipPanel 拆文面板：把一本对标小说拆成结构化分析产物。
//
// 关键机制与导入同构：管线在需要用户裁定处**停下并关闭事件通道**（灰区字数待裁定、
// 黄金三章待放行）。「放行全书」「裁定长短篇」「调整切分」都是带上相应选项**再启动一次**
// 拆解——恢复是无状态的，从缺失那一步继续，已完成阶段不重跑、不重复花钱。
//
// 拆文是只读分析：产物落独立的拆文库目录，不动本书的任何创作产物。
export function RipPanel({ onClose }: { onClose: () => void }) {
  const [source, setSource] = useState("");
  const [bookName, setBookName] = useState("");
  const [lines, setLines] = useState<JobEvent[]>([]);
  const [stage, setStage] = useState("");
  const [running, setRunning] = useState(false);
  const [paused, setPaused] = useState(false);
  const [finished, setFinished] = useState(false);
  const [degraded, setDegraded] = useState<number[]>([]);
  const [libPath, setLibPath] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [guidance, setGuidance] = useState("");
  const logRef = useRef<HTMLDivElement>(null);
  // 启动参数存在 ref 里：停靠点重入必须带回同一个源路径/书名，
  // 否则会去开另一个拆文库，等于从零重拆一遍。
  const launchedRef = useRef<
    Pick<
      DeconstructOptions,
      "sourcePath" | "libraryDir" | "bookName" | "form" | "autoConfirm" | "guidance"
    >
  >({
    sourcePath: "",
    libraryDir: "",
    bookName: "",
    form: "",
    autoConfirm: false,
    guidance: "",
  });

  useEffect(() => {
    const offs = [
      api.on("job:rip", (ev: JobEvent) => {
        setStage(ev.stage);
        setLines((prev) => {
          // Key 非空时对同 Key 原地更新（退避重试在一行变动，不刷屏）。
          if (ev.key) {
            const at = prev.findIndex((p) => p.key === ev.key);
            if (at >= 0) {
              const next = prev.slice();
              next[at] = ev;
              return next;
            }
          }
          return [...prev, ev];
        });
        if (ev.error) setErr(ev.error);
      }),
      api.on("job:rip:done", (d: RipDone) => {
        setRunning(false);
        setPaused(!!d.paused);
        setFinished(d.stage === "done" && !d.error);
        setDegraded(d.degraded ? d.failed ?? [] : []);
        if (d.error) setErr(d.error);
      }),
    ];
    return () => offs.forEach((o) => o());
  }, []);

  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  const launch = async (extra: Partial<DeconstructOptions>) => {
    setErr(null);
    setRunning(true);
    setPaused(false);
    const base = launchedRef.current;
    const opts: DeconstructOptions = {
      ...base,
      acceptPreview: false,
      retryFailed: false,
      ...extra,
    };
    launchedRef.current = {
      sourcePath: opts.sourcePath,
      libraryDir: opts.libraryDir,
      bookName: opts.bookName,
      form: opts.form,
      autoConfirm: opts.autoConfirm,
      guidance: opts.guidance,
    };
    try {
      await api.StartDeconstruct(opts);
      const p = await api.DeconstructLibraryPath(
        opts.libraryDir,
        opts.bookName || deriveBookName(opts.sourcePath),
      );
      if (p) setLibPath(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setRunning(false);
    }
  };

  const pick = async () => {
    try {
      const p = await api.PickNovelFile();
      if (p) setSource(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const cur = stageIndex(stage);
  const awaitingPreview = paused && stage === "awaiting_preview";
  const awaitingForm = paused && stage === "awaiting_form";
  const started = lines.length > 0 || running;

  return (
    <Overlay layer="sheet" onClose={running ? undefined : onClose} labelledBy="rip-title">
      <div className="modal wide">
        <h2 id="rip-title">拆解对标小说</h2>
        <p className="subtle sm">
          把一本已有的小说拆成结构化分析：章节边界 → 黄金三章 → 逐章拆解 → 剧情单元与节奏 →
          角色设定 → 拆文报告 → 文风。只读分析，产物落独立的拆文库，不动你正在写的书。
        </p>

        {!started && (
          <>
            <label className="form-label">原文（txt / md，UTF-8 或 GB18030）</label>
            <div className="inline-actions">
              <input
                className="text-input"
                value={source}
                onChange={(e) => setSource(e.target.value)}
                placeholder="选择或粘贴文件路径"
              />
              <button onClick={pick}>浏览…</button>
            </div>
            <label className="form-label" style={{ marginTop: 12 }}>
              书名（可选，决定拆文库子目录；留空取文件名）
            </label>
            <input
              className="text-input"
              value={bookName}
              onChange={(e) => setBookName(e.target.value)}
              placeholder="留空即用文件名"
            />
            <p className="subtle sm" style={{ marginTop: 8 }}>
              长篇会先拆黄金三章给你过目，确认后再逐章拆完全书。一本几百章的书是几百次模型调用，
              放行前可以先看看方向对不对。
            </p>
          </>
        )}

        {started && (
          <div className="stepper">
            {STAGES.map((s, i) => {
              const state =
                cur < 0 ? "todo" : i < cur ? "done" : i === cur ? (paused ? "paused" : "active") : "todo";
              return (
                <div className={`step ${state}`} key={s.key}>
                  <span className="step-dot" />
                  <span className="step-label">{s.label}</span>
                </div>
              );
            })}
          </div>
        )}

        {started && (
          <div className="import-log" ref={logRef}>
            {lines.map((l, i) => (
              <div key={l.key || i} className={`log-line ${l.level === "warn" ? "warn" : ""}`}>
                {l.total > 0 && (
                  <span className="log-prog">
                    {l.current}/{l.total}
                  </span>
                )}
                <span>{l.message}</span>
              </div>
            ))}
          </div>
        )}

        {err && <div className="error-banner">{err}</div>}

        {awaitingPreview && (
          <div className="note accent">
            <strong>黄金三章已拆完，快速预览就绪。</strong>
            <div className="subtle sm" style={{ marginTop: 4 }}>
              看过拆文库里的「快速预览.md」再决定是否拆完全书。切分不对可以先重新识别。
            </div>
            <div className="inline-actions" style={{ marginTop: 10 }}>
              <button
                className="primary"
                onClick={() => void launch({ acceptPreview: true })}
                disabled={running}
              >
                放行，拆完全书
              </button>
            </div>
            <div className="inline-actions" style={{ marginTop: 8 }}>
              <input
                className="text-input"
                value={guidance}
                onChange={(e) => setGuidance(e.target.value)}
                placeholder="切分不对？用一句话说明如何切分"
              />
              <button
                onClick={() => void launch({ guidance })}
                disabled={running || !guidance.trim()}
              >
                重新识别
              </button>
            </div>
          </div>
        )}

        {awaitingForm && (
          <div className="note accent">
            <strong>字数介于长短篇之间，需要你裁定按哪种拆。</strong>
            <div className="subtle sm" style={{ marginTop: 4 }}>
              长篇会逐章拆解并先给黄金三章预览；短篇一路拆完，不做逐章分层。
            </div>
            <div className="inline-actions" style={{ marginTop: 10 }}>
              <button onClick={() => void launch({ form: "long" })} disabled={running}>
                按长篇拆
              </button>
              <button onClick={() => void launch({ form: "short" })} disabled={running}>
                按短篇拆
              </button>
            </div>
          </div>
        )}

        {degraded.length > 0 && (
          <div className="note accent">
            拆解完成，但有 {degraded.length} 章重试后仍失败（第 {degraded.slice(0, 12).join("、")}
            {degraded.length > 12 ? " 等" : ""} 章），产物不完整但可用。
            <div className="subtle sm" style={{ marginTop: 4 }}>
              失败详情在拆文库的 failures/ 目录；再拆一次只会重试失败章。
            </div>
            <div className="inline-actions" style={{ marginTop: 10 }}>
              <button
                className="primary"
                onClick={() => void launch({ retryFailed: true, acceptPreview: true })}
                disabled={running}
              >
                重试失败章节
              </button>
            </div>
          </div>
        )}

        {finished && degraded.length === 0 && (
          <div className="ok-banner">
            拆解完成，拆文报告 / 情节节点 / 文风等产物已就绪。
            {libPath && <div className="subtle sm" style={{ marginTop: 4 }}>{libPath}</div>}
          </div>
        )}

        <div className="modal-actions">
          {running && (
            <button className="ghost" onClick={() => api.CancelDeconstruct()}>
              取消拆解
            </button>
          )}
          {!started && (
            <button
              className="primary"
              onClick={() => void launch({ sourcePath: source, bookName })}
              disabled={running || !source.trim()}
            >
              开始拆解
            </button>
          )}
          <button className="ghost" onClick={onClose} disabled={running}>
            关闭
          </button>
        </div>
        {running && (
          <p className="subtle sm">
            取消只中断当前步骤，已完成的阶段工件会保留，之后可以接着拆、不会重复花钱。
          </p>
        )}
      </div>
    </Overlay>
  );
}

function deriveBookName(sourcePath: string): string {
  const file = sourcePath.trim().split(/[\\/]/).pop() ?? "";
  return file.replace(/\.[^.]+$/, "");
}
