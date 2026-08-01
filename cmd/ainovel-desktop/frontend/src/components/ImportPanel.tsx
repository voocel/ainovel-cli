import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { ImportOptions, JobDone, JobEvent } from "../bindings/wails";
import { Overlay } from "./Overlay";

// 阶段顺序用于步骤条展示。等待类阶段不占独立节点，作为所处节点的“暂停”状态。
const STAGES: { key: string; label: string }[] = [
  { key: "ingesting", label: "读取源文件" },
  { key: "segmenting", label: "识别章节" },
  { key: "analyzing", label: "逐章分析" },
  { key: "synthesizing", label: "综合设定" },
  { key: "validating", label: "校验" },
  { key: "publishing", label: "发布" },
];

// 等待阶段 → 所归属的步骤节点
const WAIT_OWNER: Record<string, string> = {
  awaiting_confirmation: "segmenting",
  awaiting_story_status: "synthesizing",
};

function stageIndex(stage: string): number {
  const owner = WAIT_OWNER[stage] ?? stage;
  const i = STAGES.findIndex((s) => s.key === owner);
  return i;
}

// ImportPanel 导入面板。
//
// 关键机制：导入管线在需要用户裁定处会**停下并关闭事件通道**（不是在途应答）。
// 因此“确认切分”“选择故事状态”“调整切分”都是带上相应选项**再启动一次**导入——
// 管线恢复是无状态的，会从缺失的那一步继续，已完成阶段不重跑、不重复花钱。
export function ImportPanel({
  onClose,
  onCompleted,
}: {
  onClose: () => void;
  onCompleted: () => void;
}) {
  const [source, setSource] = useState("");
  const [continueAfter, setContinueAfter] = useState(false);
  const [lines, setLines] = useState<JobEvent[]>([]);
  const [stage, setStage] = useState("");
  const [running, setRunning] = useState(false);
  const [paused, setPaused] = useState(false);
  const [finished, setFinished] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [guidance, setGuidance] = useState("");
  const [resumeHint, setResumeHint] = useState("");
  const logRef = useRef<HTMLDivElement>(null);
  const onCompletedRef = useRef(onCompleted);
  const completedRef = useRef(false);

  useEffect(() => {
    onCompletedRef.current = onCompleted;
  }, [onCompleted]);

  useEffect(() => {
    api.ImportResumeHint().then(setResumeHint).catch(() => {});
  }, []);

  useEffect(() => {
    const offs = [
      api.on("job:import", (ev: JobEvent) => {
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
      api.on("job:import:done", (d: JobDone) => {
        setRunning(false);
        setPaused(!!d.paused);
        const succeeded = d.stage === "done" && !d.error;
        setFinished(succeeded);
        if (d.error) setErr(d.error);
        if (succeeded && !completedRef.current) {
          completedRef.current = true;
          onCompletedRef.current();
        }
      }),
    ];
    return () => offs.forEach((o) => o());
  }, []);

  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  const launch = async (extra: Partial<ImportOptions>) => {
    setErr(null);
    setRunning(true);
    setPaused(false);
    try {
      await api.StartImport({
        sourcePath: "",
        autoConfirm: false,
        storyResolution: "",
        continueAfter,
        guidance: "",
        acceptSegmentation: false,
        ...extra,
      });
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setRunning(false);
    }
  };

  const pick = async () => {
    try {
      const p = await api.PickImportFile();
      if (p) setSource(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const cur = stageIndex(stage);
  const awaitingSeg = paused && stage === "awaiting_confirmation";
  const awaitingStory = paused && stage === "awaiting_story_status";
  const started = lines.length > 0 || running;

  return (
    <Overlay layer="sheet" onClose={running ? undefined : onClose} labelledBy="import-title">
      <div className="modal wide">
        <h2 id="import-title">导入已有小说</h2>
        <p className="subtle sm">
          把一本已有的小说语义编译进项目：识别章节 → 逐章提取事实 → 归纳设定 → 逐章落盘，
          之后可以接着往下写。只能导入到空书。
        </p>

        {resumeHint && !started && (
          <div className="note accent">
            检测到未完成的导入：{resumeHint}
            <div className="inline-actions" style={{ marginTop: 8 }}>
              <button onClick={() => void launch({})} disabled={running}>
                继续未完成的导入
              </button>
            </div>
          </div>
        )}

        {!started && (
          <>
            <label className="form-label">源文件（txt / md，UTF-8 或 GB18030）</label>
            <div className="inline-actions">
              <input
                className="text-input"
                value={source}
                onChange={(e) => setSource(e.target.value)}
                placeholder="选择或粘贴文件路径"
              />
              <button onClick={pick}>浏览…</button>
            </div>
            <label className="switch" style={{ marginTop: 12 }}>
              <input
                type="checkbox"
                checked={continueAfter}
                onChange={(e) => setContinueAfter(e.target.checked)}
              />
              <span>导入完成后直接接力续写（否则停在验收等你确认）</span>
            </label>
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

        {awaitingSeg && (
          <div className="note accent">
            <strong>切分完成，请核对上面的章节标题。</strong>
            <div className="subtle sm" style={{ marginTop: 4 }}>
              没问题就确认继续；不对则用自然语言说明后重新识别（例如「幕间·X 也是独立章节」）。
            </div>
            <div className="inline-actions" style={{ marginTop: 10 }}>
              <button
                className="primary"
                onClick={() => void launch({ acceptSegmentation: true })}
                disabled={running}
              >
                确认切分，继续
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

        {awaitingStory && (
          <div className="note accent">
            <strong>无法确定这本书是否已完结，请明确告知。</strong>
            <div className="inline-actions" style={{ marginTop: 10 }}>
              <button onClick={() => void launch({ storyResolution: "closed" })} disabled={running}>
                已完结
              </button>
              <button onClick={() => void launch({ storyResolution: "open" })} disabled={running}>
                未完待续
              </button>
            </div>
          </div>
        )}

        {finished && (
          <div className="ok-banner">
            导入完成，设定与章节已就绪。
            {continueAfter ? "已尝试自动接力续写。" : "关闭面板后可继续创作。"}
          </div>
        )}

        <div className="modal-actions">
          {running && (
            <button className="ghost" onClick={() => api.CancelImport()}>
              取消导入
            </button>
          )}
          {!started && (
            <button
              className="primary"
              onClick={() => void launch({ sourcePath: source })}
              disabled={running || !source.trim()}
            >
              开始导入
            </button>
          )}
          <button className="ghost" onClick={onClose} disabled={running}>
            关闭
          </button>
        </div>
        {running && (
          <p className="subtle sm">
            取消只中断当前步骤，已完成的阶段产物会保留，之后可以接着导入、不会重复花钱。
          </p>
        )}
      </div>
    </Overlay>
  );
}
