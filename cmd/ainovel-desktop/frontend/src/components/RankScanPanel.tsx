import { useEffect, useRef, useState } from "react";
import * as api from "../bindings/wails";
import type { JobEvent, RankScanOptions, ScanDone } from "../bindings/wails";
import { Segmented } from "./Menu";
import { Overlay } from "./Overlay";

// 阶段顺序用于步骤条。扫榜没有停靠点：数据给齐后一路跑到选题。
const STAGES: { key: string; label: string }[] = [
  { key: "fetching", label: "读取数据" },
  { key: "parsing", label: "解析条目" },
  { key: "cleaning", label: "清洗质检" },
  { key: "analyzing", label: "趋势分析" },
  { key: "topicing", label: "选题决策" },
];

type SourceKind = "paste" | "file" | "dir";

// RankScanPanel 扫榜面板：把榜单数据整理成市场趋势报告与可执行选题。
//
// 第一版不联网抓取——数据由你提供：粘贴榜单页文本，或指定本地文件/目录。
// 与拆文同一形状但没有停靠点，所以没有「放行」按钮，取消后再扫会从缺失那一步接着跑。
export function RankScanPanel({ onClose }: { onClose: () => void }) {
  const [kind, setKind] = useState<SourceKind>("paste");
  const [pasted, setPasted] = useState("");
  const [filePath, setFilePath] = useState("");
  const [dirPath, setDirPath] = useState("");
  const [platform, setPlatform] = useState("");
  const [rankName, setRankName] = useState("");
  const [scanDate, setScanDate] = useState("");
  const [lines, setLines] = useState<JobEvent[]>([]);
  const [stage, setStage] = useState("");
  const [running, setRunning] = useState(false);
  const [finished, setFinished] = useState(false);
  const [sparse, setSparse] = useState(false);
  const [entries, setEntries] = useState(0);
  const [dir, setDir] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const logRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const offs = [
      api.on("job:scan", (ev: JobEvent) => {
        setStage(ev.stage);
        setLines((prev) => {
          // Key 非空时同 Key 原地更新（退避重试在一行里变动，不刷屏）。
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
      api.on("job:scan:done", (d: ScanDone) => {
        setRunning(false);
        setFinished(d.stage === "done" && !d.error);
        setSparse(!!d.sparse);
        setEntries(d.entries ?? 0);
        if (d.dir) setDir(d.dir);
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
    setRunning(true);
    const opts: RankScanOptions = {
      // 三选一：只提交当前选中的数据源，避免后端按 粘贴>文件>目录 的优先级
      // 取到一个你以为已经换掉的旧数据源。
      pastedText: kind === "paste" ? pasted : "",
      filePath: kind === "file" ? filePath : "",
      dirPath: kind === "dir" ? dirPath : "",
      platform,
      rankName,
      libraryDir: "",
      scanDate,
    };
    try {
      await api.StartRankScan(opts);
      const p = await api.RankScanLibraryPath(opts.libraryDir, platform, rankName, scanDate);
      if (p) setDir(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setRunning(false);
    }
  };

  const pickFile = async () => {
    try {
      const p = await api.PickRankFile();
      if (p) setFilePath(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const pickDir = async () => {
    try {
      const p = await api.PickDirectory("选择榜单数据目录");
      if (p) setDirPath(p);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const cur = STAGES.findIndex((s) => s.key === stage);
  const started = lines.length > 0 || running;
  const ready =
    kind === "paste" ? pasted.trim() !== "" : kind === "file" ? filePath.trim() !== "" : dirPath.trim() !== "";

  return (
    <Overlay layer="sheet" onClose={running ? undefined : onClose} labelledBy="scan-title">
      <div className="modal wide">
        <h2 id="scan-title">扫榜</h2>
        <p className="subtle sm">
          把榜单数据整理成市场趋势报告与选题决策。第一版不联网抓取：把榜单页文本复制过来，
          或指定本地文件/目录。产物落独立的扫榜库，不动你正在写的书。
        </p>

        {!started && (
          <>
            <Segmented<SourceKind>
              value={kind}
              onChange={setKind}
              ariaLabel="数据来源"
              options={[
                { key: "paste", label: "粘贴文本" },
                { key: "file", label: "本地文件" },
                { key: "dir", label: "目录" },
              ]}
            />

            {kind === "paste" && (
              <>
                <label className="form-label" style={{ marginTop: 12 }}>
                  榜单文本（在榜单页全选复制即可，模板文字会自动剔除）
                </label>
                <textarea
                  className="prompt-input compact"
                  rows={8}
                  value={pasted}
                  onChange={(e) => setPasted(e.target.value)}
                  placeholder="1 都市修复师 作者名 都市 简介…"
                />
              </>
            )}

            {kind === "file" && (
              <>
                <label className="form-label" style={{ marginTop: 12 }}>
                  榜单文件（txt / md）
                </label>
                <div className="inline-actions">
                  <input
                    className="text-input"
                    value={filePath}
                    onChange={(e) => setFilePath(e.target.value)}
                    placeholder="选择或粘贴文件路径"
                  />
                  <button onClick={pickFile}>浏览…</button>
                </div>
              </>
            )}

            {kind === "dir" && (
              <>
                <label className="form-label" style={{ marginTop: 12 }}>
                  榜单目录（目录下的 txt / md 都会读入，一次扫多个榜）
                </label>
                <div className="inline-actions">
                  <input
                    className="text-input"
                    value={dirPath}
                    onChange={(e) => setDirPath(e.target.value)}
                    placeholder="选择或粘贴目录路径"
                  />
                  <button onClick={pickDir}>浏览…</button>
                </div>
              </>
            )}

            <label className="form-label" style={{ marginTop: 12 }}>
              平台 / 榜单名 / 采集日期（都可留空；决定扫榜库子目录名）
            </label>
            <div className="inline-actions">
              <input
                className="text-input"
                value={platform}
                onChange={(e) => setPlatform(e.target.value)}
                placeholder="平台，如 qidian"
              />
              <input
                className="text-input"
                value={rankName}
                onChange={(e) => setRankName(e.target.value)}
                placeholder="榜单名，如 月票榜"
              />
              <input
                className="text-input"
                value={scanDate}
                onChange={(e) => setScanDate(e.target.value)}
                placeholder="YYYYMMDD"
              />
            </div>
            <p className="subtle sm" style={{ marginTop: 8 }}>
              采集日期留空取今天。同一天同一份数据重扫会复用已有工件；换数据或跨日自然重扫。
            </p>
          </>
        )}

        {started && (
          <div className="stepper">
            {STAGES.map((s, i) => {
              const state = cur < 0 ? "todo" : i < cur ? "done" : i === cur ? "active" : "todo";
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

        {finished && sparse && (
          <div className="note accent">
            扫榜完成，但有效条目只有 {entries} 条，样本偏少。
            <div className="subtle sm" style={{ marginTop: 4 }}>
              产物已标记「数据稀疏」，选题的可行性评级也被相应下调——先扫够样本再定方向更稳。
            </div>
            {dir && <div className="subtle sm" style={{ marginTop: 4 }}>{dir}</div>}
          </div>
        )}

        {finished && !sparse && (
          <div className="ok-banner">
            扫榜完成，{entries} 条有效条目，扫榜报告与选题决策已就绪。
            {dir && <div className="subtle sm" style={{ marginTop: 4 }}>{dir}</div>}
          </div>
        )}

        <div className="modal-actions">
          {running && (
            <button className="ghost" onClick={() => api.CancelRankScan()}>
              取消扫榜
            </button>
          )}
          {!started && (
            <button className="primary" onClick={() => void start()} disabled={running || !ready}>
              开始扫榜
            </button>
          )}
          <button className="ghost" onClick={onClose} disabled={running}>
            关闭
          </button>
        </div>
        {running && (
          <p className="subtle sm">
            取消只中断当前步骤，已完成的阶段工件会保留，之后可以接着扫、不会重复花钱。
          </p>
        )}
      </div>
    </Overlay>
  );
}
