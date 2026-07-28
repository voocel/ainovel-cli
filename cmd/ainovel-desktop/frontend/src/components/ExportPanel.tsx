import { useState } from "react";
import * as api from "../bindings/wails";
import type { ExportResult } from "../bindings/wails";

// ExportPanel 导出面板。导出是只读操作，创作运行中也能随时拿"现阶段成品"。
// 格式由输出路径后缀决定（.txt / .epub），与终端版一致。
export function ExportPanel({
  novelName,
  onClose,
}: {
  novelName: string;
  onClose: () => void;
}) {
  const [format, setFormat] = useState<"txt" | "epub">("txt");
  const [outPath, setOutPath] = useState("");
  const [from, setFrom] = useState("");
  const [to, setTo] = useState("");
  const [overwrite, setOverwrite] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<ExportResult | null>(null);

  const pick = async () => {
    try {
      const name = `${novelName || "novel"}.${format}`;
      const p = await api.PickExportPath(name);
      if (p) {
        setOutPath(p);
        // 用户选的后缀优先：与后端"后缀决定格式"的语义保持一致。
        if (p.toLowerCase().endsWith(".epub")) setFormat("epub");
        else if (p.toLowerCase().endsWith(".txt")) setFormat("txt");
      }
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    }
  };

  const run = async () => {
    if (busy) return;
    setBusy(true);
    setErr(null);
    setResult(null);
    try {
      const res = await api.Export({
        format,
        outPath: outPath.trim(),
        from: Number(from) || 0,
        to: Number(to) || 0,
        overwrite,
      });
      setResult(res);
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="modal-overlay">
      <div className="modal">
        <h2>导出成品</h2>
        <p className="subtle sm">
          合并已完成的章节导出。创作进行中也可以导出，拿到的是当前进度的成品。
        </p>

        <label className="form-label">格式</label>
        <div className="preset-grid">
          {(["txt", "epub"] as const).map((f) => (
            <button
              key={f}
              className={`preset ${format === f ? "active" : ""}`}
              onClick={() => setFormat(f)}
              disabled={busy}
            >
              {f === "txt" ? "TXT 纯文本" : "EPUB 电子书"}
            </button>
          ))}
        </div>

        <label className="form-label">
          输出路径<span className="subtle sm"> · 留空则写到书目录下</span>
        </label>
        <div className="inline-actions">
          <input
            className="text-input"
            value={outPath}
            onChange={(e) => setOutPath(e.target.value)}
            placeholder={`默认 {书目录}/${novelName || "书名"}.${format}`}
            disabled={busy}
          />
          <button onClick={pick} disabled={busy}>
            浏览…
          </button>
        </div>

        <label className="form-label">
          章节范围<span className="subtle sm"> · 留空表示全部</span>
        </label>
        <div className="inline-actions">
          <input
            className="text-input"
            type="number"
            min={1}
            value={from}
            onChange={(e) => setFrom(e.target.value)}
            placeholder="起始章"
            disabled={busy}
          />
          <span className="subtle">至</span>
          <input
            className="text-input"
            type="number"
            min={1}
            value={to}
            onChange={(e) => setTo(e.target.value)}
            placeholder="结束章"
            disabled={busy}
          />
        </div>

        <label className="switch" style={{ marginTop: 12 }}>
          <input
            type="checkbox"
            checked={overwrite}
            onChange={(e) => setOverwrite(e.target.checked)}
            disabled={busy}
          />
          <span>覆盖已存在的文件</span>
        </label>

        {err && <div className="error-banner">{err}</div>}

        {result && (
          <div className="ok-banner">
            已导出 {result.chapters} 章（{(result.bytes / 1024).toFixed(0)} KB）
            <div className="subtle sm" style={{ marginTop: 4 }}>
              {result.path}
            </div>
            {(result.skipped ?? []).length > 0 && (
              <div className="subtle sm" style={{ marginTop: 4 }}>
                跳过未完成章节：{(result.skipped ?? []).join(", ")}
              </div>
            )}
          </div>
        )}

        <div className="modal-actions">
          <button className="ghost" onClick={onClose} disabled={busy}>
            关闭
          </button>
          <button className="primary" onClick={run} disabled={busy}>
            {busy ? "导出中…" : "导出"}
          </button>
        </div>
      </div>
    </div>
  );
}
