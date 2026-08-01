import { useState } from "react";
import * as api from "../bindings/wails";
import type { StoryGenre } from "../bindings/wails";
import { CoCreatePanel } from "../components/CoCreatePanel";
import { ImportPanel } from "../components/ImportPanel";
import { RankScanPanel } from "../components/RankScanPanel";
import { RipPanel } from "../components/RipPanel";
import { SimulatePanel } from "../components/SimulatePanel";

// WelcomeScreen 起书页。
//
// 写作节奏在这里就要选定，而不是藏在工作台底部的开关里：
//   分段创作（默认）= 逐章验收模式。引擎先把前提/大纲/人物/世界观规划完，
//     在写第 1 章之前停下让你审阅；之后每章写完再停一次等你放行。
//   一口气写完 = auto 模式，规划完直接连续写到底。
// 这个选择只是写入 RunMeta 的推进模式，之后随时可在工作台切换。
//
// 「起书前的准备」三个入口（扫榜 / 拆文 / 仿写）都是**可跳过的前置动作**，不是
// 「本次创作启用什么」的开关，所以做成打开面板的卡片而不是勾选项：
//   扫榜、拆文是只读分析，产物落独立的扫榜库/拆文库，不进本书 Store，也不会
//     自动改变引擎写出来的内容——你看完结论，自己把方向写进下面那句话。
//   仿写画像不一样，它写进本书 Store 且会被创作自动读取（见
//     internal/tools/novel_context_builders.go 的 buildSimulationProfile），
//     所以起书前先做完，规划阶段就带着它跑。
// 三者与创作引擎互斥（Host.acquireExclusive），但此刻引擎还没起，空书上必然能拿到锁。
type Prep = "none" | "scan" | "rip" | "simulate";

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
  const [genre, setGenre] = useState<StoryGenre>("novel");
  const [importing, setImporting] = useState(false);
  const [prep, setPrep] = useState<Prep>("none");
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
      await api.StartQuick(text, reviewFirst, genre);
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
        genre={genre}
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

        <div className="story-type-choice">
          <div className="segmented" role="group" aria-label="作品类型">
            <button
              type="button"
              className={genre === "novel" ? "active" : ""}
              aria-pressed={genre === "novel"}
              onClick={() => setGenre("novel")}
              disabled={busy}
            >
              长篇小说
            </button>
            <button
              type="button"
              className={genre === "short_story" ? "active" : ""}
              aria-pressed={genre === "short_story"}
              onClick={() => setGenre("short_story")}
              disabled={busy}
            >
              短篇小说
            </button>
          </div>
          <p className="story-type-hint subtle sm">
            {genre === "short_story"
              ? "聚焦一个核心冲突，按 1-3 万字的紧凑结构创作。"
              : "适合多阶段成长、多线剧情和持续连载。"}
          </p>
        </div>

        <h3 className="section-label">起书前的准备（可跳过）</h3>
        <div className="prep-choice">
          <button className="prep-card" onClick={() => setPrep("scan")} disabled={busy}>
            <span className="prep-title">扫榜</span>
            <span className="prep-desc">看榜单趋势，定选题方向。结论自己写进下面那句话。</span>
          </button>
          <button className="prep-card" onClick={() => setPrep("rip")} disabled={busy}>
            <span className="prep-title">拆文</span>
            <span className="prep-desc">把一本对标小说拆成结构分析，学它怎么搭骨架。</span>
          </button>
          <button className="prep-card" onClick={() => setPrep("simulate")} disabled={busy}>
            <span className="prep-title">仿写</span>
            <span className="prep-desc">分析参考文章提炼写法，创作时各角色自动借鉴。</span>
          </button>
        </div>

        <textarea
          className="prompt-input"
          placeholder={
            genre === "short_story"
              ? "例如：写一篇悬疑短篇，一场暴雨困住六名陌生人，真相在天亮前揭开。"
              : "例如：写一本东方玄幻长篇，主角从边陲小城起步，一路逆袭。"
          }
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

      {prep === "scan" && <RankScanPanel onClose={() => setPrep("none")} />}
      {prep === "rip" && <RipPanel onClose={() => setPrep("none")} />}
      {prep === "simulate" && <SimulatePanel onClose={() => setPrep("none")} />}
    </div>
  );
}
