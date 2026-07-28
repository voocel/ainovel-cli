import { useState } from "react";
import * as api from "../bindings/wails";
import { CoCreatePanel } from "../components/CoCreatePanel";
import { ImportPanel } from "../components/ImportPanel";

// WelcomeScreen 起书页。
//
// 写作节奏在这里就要选定，而不是藏在工作台底部的开关里：
//   分段创作（默认）= 逐章验收模式。引擎先把前提/大纲/人物/世界观规划完，
//     在写第 1 章之前停下让你审阅；之后每章写完再停一次等你放行。
//   一口气写完 = auto 模式，规划完直接连续写到底。
// 这个选择只是写入 RunMeta 的推进模式，之后随时可在工作台切换。
export function WelcomeScreen({
  onStarted,
  onOpenSettings,
  onBackToLibrary,
}: {
  onStarted: () => void;
  onOpenSettings: () => void;
  onBackToLibrary: () => void;
}) {
  const [mode, setMode] = useState<"quick" | "cocreate">("quick");
  const [importing, setImporting] = useState(false);
  const [reviewFirst, setReviewFirst] = useState(true);
  const [prompt, setPrompt] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const start = async () => {
    const text = prompt.trim();
    if (!text || busy) return;
    setBusy(true);
    setErr(null);
    try {
      await api.StartQuick(text, reviewFirst);
      onStarted();
    } catch (e) {
      setErr(String((e as Error)?.message ?? e));
      setBusy(false);
    }
  };

  if (mode === "cocreate") {
    return (
      <CoCreatePanel
        mode="new"
        reviewFirst={reviewFirst}
        onDone={onStarted}
        onCancel={() => setMode("quick")}
      />
    );
  }

  return (
    <div className="welcome">
      <div className="welcome-card">
        <h1>开始创作一本新书</h1>
        <p className="subtle">
          用一句话描述你想写的小说，创作引擎会构建世界观、角色和大纲，然后逐章写作。
        </p>

        <textarea
          className="prompt-input"
          placeholder="例如：写一本东方玄幻长篇，主角从边陲小城起步，一路逆袭…"
          value={prompt}
          onChange={(e) => setPrompt(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && (e.ctrlKey || e.metaKey)) void start();
          }}
          rows={5}
          disabled={busy}
        />

        <div className="pace-choice">
          <button
            className={`pace-option ${reviewFirst ? "active" : ""}`}
            onClick={() => setReviewFirst(true)}
            disabled={busy}
          >
            <span className="pace-title">分段创作</span>
            <span className="pace-desc">
              先规划设定并停下给你看，确认后再逐章推进。每章写完都会等你放行。
            </span>
          </button>
          <button
            className={`pace-option ${!reviewFirst ? "active" : ""}`}
            onClick={() => setReviewFirst(false)}
            disabled={busy}
          >
            <span className="pace-title">一口气写完</span>
            <span className="pace-desc">
              全自动连续创作到完本，中途不停。可以随时暂停或插入修改意见。
            </span>
          </button>
        </div>

        {err && <div className="error-banner">{err}</div>}

        <div className="welcome-actions">
          <span className="hint">Ctrl+Enter 开始</span>
          <div className="inline-actions">
            <button className="ghost" onClick={onBackToLibrary} disabled={busy}>
              返回书库
            </button>
            <button className="ghost" onClick={onOpenSettings} disabled={busy}>
              设置
            </button>
            <button className="ghost" onClick={() => setMode("cocreate")} disabled={busy}>
              共创规划
            </button>
            <button className="primary" onClick={start} disabled={busy || !prompt.trim()}>
              {busy ? "启动中…" : "开始创作"}
            </button>
          </div>
        </div>

        <div className="welcome-foot">
          <p className="subtle sm" style={{ margin: 0 }}>
            没想清楚要写什么？用「共创规划」和助手聊几轮，边聊边生成创作指令。
          </p>
          <p className="subtle sm" style={{ margin: "6px 0 0" }}>
            已经有一本写了一部分的小说？
            <button className="link" onClick={() => setImporting(true)} disabled={busy}>
              导入后接着写
            </button>
          </p>
        </div>
      </div>

      {importing && (
        <ImportPanel
          onClose={() => setImporting(false)}
          onCompleted={onStarted}
        />
      )}
    </div>
  );
}
