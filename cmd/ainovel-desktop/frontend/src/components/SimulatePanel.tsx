import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { JobDone, JobEvent } from "../bindings/wails";
import { Overlay } from "./Overlay";

function formatElapsed(totalSeconds: number): string {
  if (totalSeconds < 60) return `${totalSeconds} 秒`;
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${minutes} 分 ${seconds} 秒`;
}

// SimulatePanel 仿写画像面板：分析参考文章，提炼结构/节奏/钩子手法供创作时借鉴。
// 语料目录绑定本书（<书目录>/simulate），不是进程当前目录——多书场景下才不会读错。
export function SimulatePanel({ onClose }: { onClose: () => void }) {
  const [sourceDir, setSourceDir] = useState("");
  const [lines, setLines] = useState<JobEvent[]>([]);
  const [running, setRunning] = useState(false);
  const [picking, setPicking] = useState(false);
  const [added, setAdded] = useState(0);
  const [elapsed, setElapsed] = useState(0);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const logRef = useRef<HTMLDivElement>(null);
  const startedAtRef = useRef(0);

  useEffect(() => {
    api.SimulateSourceDir()
      .then(setSourceDir)
      .catch((e) => setErr(String((e as Error)?.message ?? e)));
  }, []);

  useEffect(() => {
    const offs = [
      api.on("job:simulate", (ev: JobEvent) => {
        setLines((prev) => {
          if (ev.key) {
            const at = prev.findIndex((item) => item.key === ev.key);
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

  useEffect(() => {
    if (!running) return;
    if (!startedAtRef.current) startedAtRef.current = Date.now();
    const update = () => setElapsed(Math.max(0, Math.floor((Date.now() - startedAtRef.current) / 1000)));
    update();
    const timer = window.setInterval(update, 1000);
    return () => window.clearInterval(timer);
  }, [running]);

  const start = async () => {
    setErr(null);
    setDone(false);
    setLines([]);
    setElapsed(0);
    startedAtRef.current = Date.now();
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
      setElapsed(0);
      startedAtRef.current = Date.now();
      setRunning(true);
      await api.ImportSimulationProfile(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setRunning(false);
    }
  };

  const addSources = async () => {
    setErr(null);
    setPicking(true);
    try {
      const files = await api.AddSimulationSources();
      setAdded(files?.length ?? 0);
      if (files?.length) setDone(false);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setPicking(false);
    }
  };

  const openSourceDir = async () => {
    setErr(null);
    try {
      const dir = await api.OpenSimulationSourceDir();
      if (dir) setSourceDir(dir);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  return (
    <Overlay layer="sheet" onClose={running || picking ? undefined : onClose} labelledBy="sim-title">
      <div className="modal">
        <h2 id="sim-title">仿写画像</h2>
        <p className="subtle sm">
          分析参考文章的结构、节奏和吸引读者的手法，写作时供各角色借鉴。
          只借鉴写法，不复制原文表达或专有设定。
        </p>

        <label className="form-label">语料目录</label>
        <div className="path-box">{sourceDir || "…"}</div>
        <div className="inline-actions" style={{ marginTop: 8 }}>
          <button className="ghost sm" onClick={addSources} disabled={running || picking}>
            {picking ? "选择中…" : "选择参考文章…"}
          </button>
          <button className="ghost sm" onClick={openSourceDir} disabled={running || picking}>
            打开语料目录
          </button>
          {added > 0 && <span className="subtle sm">已加入 {added} 篇</span>}
        </div>
        <p className="subtle sm">
          把参考文章（.txt / .md）放进这个目录，然后点「生成画像」。
          再次生成时会跳过未变化的文件，没有新内容则不调用模型。
        </p>

        {running && (
          <div className="note accent">
            <strong>{lines[lines.length - 1]?.message || "画像任务已提交，正在准备语料…"}</strong>
            <div className="subtle sm" style={{ marginTop: 4 }}>
              后台模型请求进行中 · 已等待 {formatElapsed(elapsed)}
            </div>
          </div>
        )}

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
          <button className="ghost" onClick={importProfile} disabled={running || picking}>
            导入已有画像…
          </button>
          <button className="ghost" onClick={onClose} disabled={running || picking}>
            关闭
          </button>
          <button className="primary" onClick={start} disabled={running || picking}>
            {running ? "分析中…" : "生成画像"}
          </button>
        </div>
        {running && (
          <button className="ghost sm" onClick={() => api.CancelSimulate()}>
            取消
          </button>
        )}
      </div>
    </Overlay>
  );
}
