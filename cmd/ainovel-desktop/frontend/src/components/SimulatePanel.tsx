import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { JobDone, JobEvent } from "../bindings/wails";

// SimulatePanel 仿写画像面板：分析参考文章，提炼结构/节奏/钩子手法供创作时借鉴。
// 语料目录绑定本书（<书目录>/simulate），不是进程当前目录——多书场景下才不会读错。
export function SimulatePanel({ onClose }: { onClose: () => void }) {
  const [sourceDir, setSourceDir] = useState("");
  const [lines, setLines] = useState<JobEvent[]>([]);
  const [running, setRunning] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    api.SimulateSourceDir().then(setSourceDir).catch(() => {});
  }, []);

  useEffect(() => {
    const offs = [
      api.on("job:simulate", (ev: JobEvent) => {
        setLines((prev) => [...prev, ev]);
        if (ev.error) setErr(ev.error);
      }),
      api.on("job:simulate:done", (d: JobDone) => {
        setRunning(false);
        setDone(d.stage === "done");
        if (d.error) setErr(d.error);
      }),
    ];
    return () => offs.forEach((o) => o());
  }, []);

  useEffect(() => {
    const el = logRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [lines]);

  const start = async () => {
    setErr(null);
    setDone(false);
    setLines([]);
    setRunning(true);
    try {
      await api.StartSimulate();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setRunning(false);
    }
  };

  const importProfile = async () => {
    setErr(null);
    try {
      const p = await api.PickProfileFile();
      if (!p) return;
      setDone(false);
      setLines([]);
      setRunning(true);
      await api.ImportSimulationProfile(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setRunning(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal">
        <h2>仿写画像</h2>
        <p className="subtle sm">
          分析参考文章的结构、节奏和吸引读者的手法，写作时供各角色借鉴。
          只借鉴写法，不复制原文表达或专有设定。
        </p>

        <label className="form-label">语料目录</label>
        <div className="path-box">{sourceDir || "…"}</div>
        <p className="subtle sm">
          把参考文章（.txt / .md）放进这个目录，然后点「生成画像」。
          再次生成时会跳过未变化的文件，没有新内容则不调用模型。
        </p>

        {lines.length > 0 && (
          <div className="import-log" ref={logRef}>
            {lines.map((l, i) => (
              <div key={i} className="log-line">
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
        {done && <div className="ok-banner">画像已更新，后续创作会自动参考。</div>}

        <div className="modal-actions">
          <button className="ghost" onClick={importProfile} disabled={running}>
            导入已有画像…
          </button>
          <button className="ghost" onClick={onClose} disabled={running}>
            关闭
          </button>
          <button className="primary" onClick={start} disabled={running}>
            {running ? "分析中…" : "生成画像"}
          </button>
        </div>
        {running && (
          <button className="ghost sm" onClick={() => api.CancelSimulate()}>
            取消
          </button>
        )}
      </div>
    </div>
  );
}
